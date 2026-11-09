package hub

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

var (
	ErrBadName   = errors.New("a service name is 2 to 40 letters, digits, spaces, dashes or underscores, starting with a letter or digit")
	ErrBadTag    = errors.New("a tag is up to 64 letters, digits, spaces, dots, dashes or underscores (no slashes or commas)")
	ErrNameTaken = errors.New("a service with this name already exists")
	ErrTagInUse  = errors.New("you already use this tag for another service you send to")
	ErrNotOwner  = errors.New("only the owner of a service can do that")
	ErrNoRequest = errors.New("there is no such request")
	ErrNoInvite  = errors.New("this invitation is not valid any more")
	ErrBadExpiry = errors.New("the number of days must be between 1 and 3650")
	ErrBadAlbum  = errors.New("the album name must be at most 100 characters and have no line breaks")
)

var (
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]{1,39}$`)
	tagRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,63}$`)
)

// tagFree reports ErrTagInUse if the person already sends to another service with the same tag.
func (h *Hub) tagFree(user uuid.UUID, joining *syncer.Service) error {
	for _, svc := range h.Services() {
		if svc.Label() == joining.Label() || !strings.EqualFold(svc.Tag, joining.Tag) {
			continue
		}
		for _, a := range svc.Senders {
			if a.UserID == user {
				return ErrTagInUse
			}
		}
	}
	return nil
}

// CreateParams describe a new service. Zero values mean the defaults.
type CreateParams struct {
	Name, Tag  string
	Policy     string // approval (default) | open
	Visibility string // listed (default) | invite
	InboxTag   string
	ExpiryDays int
	Album      string // optional: an album, in the owner's account, that handed-over photos are put in
	// optional daily caps per sender: photos and megabytes, 0 for none
	LimitPhotos, LimitMB int
}

// CreateService makes the signed-in person the owner (receiver) of a new service. A receiver key is
// created in their account through their own session and kept encrypted; nobody ever sees it.
func (h *Hub) CreateService(ctx context.Context, sess *immich.API, owner immich.User, p CreateParams) error {
	if !h.SelfService() {
		return ErrNotJoinable
	}
	name, tag := strings.TrimSpace(p.Name), strings.TrimSpace(p.Tag)
	if !nameRe.MatchString(name) || strings.EqualFold(name, "default") {
		return ErrBadName
	}
	if !tagRe.MatchString(tag) {
		return ErrBadTag
	}
	policy := p.Policy
	if policy != store.PolicyOpen && policy != store.PolicyApproval {
		policy = store.PolicyApproval
	}
	visibility, invite := store.VisListed, ""
	if p.Visibility == store.VisInvite {
		visibility, invite = store.VisInvite, newToken()
	}
	// Each service gets its own nested inbox tag ("inbox/Holiday 2026") so an owner with several services
	// sees what arrived through which; Immich shows it as a child of "inbox".
	inboxTag := strings.TrimSpace(p.InboxTag)
	if inboxTag == "" {
		inboxTag = h.Options.InboxTag + "/" + name
	} else if !tagRe.MatchString(inboxTag) {
		return ErrBadTag
	}
	album := strings.TrimSpace(p.Album)
	if utf8.RuneCountInString(album) > 100 || strings.ContainsAny(album, "\r\n\x00") {
		return ErrBadAlbum
	}
	expiry := p.ExpiryDays
	if expiry <= 0 {
		expiry = h.Options.ExpiryDays
	} else if expiry > 3650 {
		return ErrBadExpiry
	}
	if err := checkLimits(p.LimitPhotos, p.LimitMB); err != nil {
		return err
	}
	for _, s := range h.Services() {
		if strings.EqualFold(s.Label(), name) {
			return ErrNameTaken
		}
	}
	// The key only gets the right to touch albums when the service uses one.
	keyPerms := immich.ReceiverPermissionsFor(album != "", h.ReceiverDelete)
	keyID, secret, err := sess.CreateAPIKey(ctx, keyName(name)+" (receiving)", keyPerms)
	if err != nil {
		return fmt.Errorf("could not create an API key in your Immich account: %w", err)
	}
	undo := func() {
		if err := sess.DeleteAPIKey(context.WithoutCancel(ctx), keyID); err != nil {
			h.Log.Error("could not remove the key created for a failed service", "err", err)
		}
	}
	if api, err := h.Connect(secret); err != nil {
		undo()
		return err
	} else if me, err := api.Me(ctx); err != nil || me.ID != owner.ID {
		undo()
		return fmt.Errorf("the new key does not work for your account: %v", err)
	}
	if _, err := h.Store.AddService(ctx, store.Service{
		Name: name, Tag: tag, InboxTag: inboxTag, Refused: h.Options.RefusedTag, ExpiryDays: expiry, SentTag: h.Options.SentTag,
		OwnerID: owner.ID, OwnerName: owner.Name, OwnerEmail: owner.Email, KeyID: keyID, Key: secret, Policy: policy,
		Visibility: visibility, Invite: invite, Album: album, LimitPhotos: p.LimitPhotos, LimitMB: p.LimitMB,
	}); err != nil {
		undo()
		if errors.Is(err, store.ErrExists) {
			return ErrNameTaken
		}
		return err
	}
	if err := h.Reload(ctx); err != nil {
		return err
	}
	h.event(ctx, name, owner.ID, "created", i18n.Pack("{name} started offering {service}", "name", owner.Name, "service", name))
	h.Log.Info("service created", "service", name, "owner", owner.Email, "policy", policy)
	return nil
}

// DeleteService removes a service its owner created: the receiver key is deleted from Immich and the
// members' links are marked orphaned, so each member can clean up their own key with Leave.
func (h *Hub) DeleteService(ctx context.Context, sess *immich.API, owner immich.User, name string) error {
	svc := h.service(name)
	if svc == nil || svc.OwnerID != owner.ID {
		return ErrNotOwner
	}
	stored, err := h.Store.Services(ctx)
	if err != nil {
		return err
	}
	for _, v := range stored {
		if v.Name == name {
			if err := sess.DeleteAPIKey(ctx, v.KeyID); err != nil {
				h.Log.Warn("delete service: could not delete the receiver key (it may already be gone)", "err", err)
			}
		}
	}
	if err := h.Store.RemoveService(ctx, name); err != nil {
		return err
	}
	h.event(ctx, name, owner.ID, "deleted", i18n.Pack("{name} removed the service {service}", "name", owner.Name, "service", name))
	h.Log.Info("service deleted", "service", name, "owner", owner.Email)
	return h.Reload(ctx)
}

// request returns the person's request for the service, if any.
func (h *Hub) request(ctx context.Context, service string, user uuid.UUID) (*store.Request, error) {
	all, err := h.Store.Requests(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Service == service && all[i].UserID == user {
			return &all[i], nil
		}
	}
	return nil, nil
}

// Ask records that the person wants to send to a service that needs the owner's approval.
func (h *Hub) Ask(ctx context.Context, user immich.User, service, invite string) error {
	if !h.SelfService() {
		return ErrNotJoinable
	}
	svc := h.service(service)
	if svc == nil || !svc.NeedsApproval || !invited(svc, invite) {
		return ErrNotJoinable
	}
	if Takes(svc, user.ID) {
		return ErrAlready
	}
	if err := h.tagFree(user.ID, svc); err != nil {
		return err
	}
	if _, err := h.Store.AddRequest(ctx, store.Request{Service: service, UserID: user.ID, Name: user.Name, Email: user.Email}); err != nil {
		if errors.Is(err, store.ErrExists) {
			return nil // asking again changes nothing
		}
		return err
	}
	h.event(ctx, service, user.ID, "asked", i18n.Pack("{name} asked to send to {service}", "name", user.Name, "service", service))
	h.Log.Info("join requested", "service", service, "user", user.Email)
	return nil
}

// Decide lets the owner approve or decline a request.
func (h *Hub) Decide(ctx context.Context, owner immich.User, service, requestID string, approve bool) error {
	svc := h.service(service)
	if svc == nil || svc.OwnerID != owner.ID {
		return ErrNotOwner
	}
	all, err := h.Store.Requests(ctx)
	if err != nil {
		return err
	}
	for _, r := range all {
		if r.ID == requestID && r.Service == service {
			status := store.StatusDeclined
			if approve {
				status = store.StatusApproved
			}
			if approve {
				h.event(ctx, service, r.UserID, "decided", i18n.Pack("The owner approved {name} for {service}", "name", r.Name, "service", service))
			} else {
				h.event(ctx, service, r.UserID, "decided", i18n.Pack("The owner declined {name} for {service}", "name", r.Name, "service", service))
			}
			h.Log.Info("join request decided", "service", service, "user", r.Email, "status", status)
			return h.Store.SetRequestStatus(ctx, r.ID, status)
		}
	}
	return ErrNoRequest
}

// Offer is a service the person is not part of yet, with what they can do about it.
type Offer struct {
	Service *syncer.Service
	State   string // join | request | pending | approved | declined
}

// Offers lists the services the person could send to, newest state first.
func (h *Hub) Offers(ctx context.Context, user uuid.UUID) ([]Offer, error) {
	if !h.SelfService() {
		return nil, nil
	}
	var out []Offer
	for _, svc := range h.Services() {
		if Takes(svc, user) || svc.OwnerID == user || svc.Unlisted {
			continue
		}
		switch {
		case svc.AllowJoin:
			out = append(out, Offer{svc, "join"})
		case svc.NeedsApproval:
			r, err := h.request(ctx, svc.Label(), user)
			if err != nil {
				return nil, err
			}
			state := "request"
			if r != nil {
				state = r.Status
			}
			out = append(out, Offer{svc, state})
		}
	}
	return out, nil
}

// Owned is a service the person created, with the requests waiting for their decision.
type Owned struct {
	Service  *syncer.Service
	Requests []store.Request // pending only
}

func (h *Hub) Owned(ctx context.Context, user uuid.UUID) ([]Owned, error) {
	if !h.SelfService() {
		return nil, nil
	}
	reqs, err := h.Store.Requests(ctx)
	if err != nil {
		return nil, err
	}
	var out []Owned
	for _, svc := range h.Services() {
		if svc.OwnerID != user {
			continue
		}
		o := Owned{Service: svc}
		for _, r := range reqs {
			if r.Service == svc.Label() && r.Status == store.StatusPending {
				o.Requests = append(o.Requests, r)
			}
		}
		out = append(out, o)
	}
	return out, nil
}

// Orphaned lists services the person joined that their owner has since removed.
func (h *Hub) Orphaned(ctx context.Context, user uuid.UUID) ([]string, error) {
	if !h.SelfService() {
		return nil, nil
	}
	links, err := h.Store.Links(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range links {
		if l.ImmichUserID == user && l.State == store.StateOrphaned {
			out = append(out, l.Service)
		}
	}
	return out, nil
}

// IsUserError reports whether err is something to tell the person as it is (a refusal), not a fault.
func IsUserError(err error) bool {
	for _, e := range []error{ErrNotJoinable, ErrAlready, ErrNotJoined, ErrNeedsApproval, ErrBadName, ErrBadTag, ErrNameTaken, ErrTagInUse, ErrNotOwner, ErrNoRequest, ErrNoInvite, ErrBadExpiry, ErrBadAlbum, ErrNoAlbum, ErrBadLimit, ErrBadRules, ErrDeleteOff, ErrNothingToTakeBack, ErrReceiverCannotDelete, ErrBadLifetime, ErrBadDropName, ErrBadDropLimit, ErrBadDropText, ErrBadDropSecret, ErrNoDropBox, ErrDropFinished, ErrDropRole, ErrBadDropAlbum, ErrBadDropWhen, ErrNoStaging, ErrNoSuchPerson, ErrHostSelf, ErrBadHostRole, ErrTooManyHosts, ErrNoEnd, ErrNothingChosen, ErrSomeNotStored, ErrBadDropLook, ErrBadCover, ErrBadTemplate, ErrNoTemplate, ErrDropDeleteOff, ErrDropKept, ErrBadPrompts, ErrDropPrompt, ErrBadMessage, ErrBadVoice, ErrGuestBookOff, ErrGuestBookFull, ErrNoShare, ErrBadShare, ErrNothingToShare, ErrNoPhotosYet, ErrHighlightsRunning, ErrNoHighlights, ErrOnwardService, ErrNoOnward, ErrBadScope} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// invited reports whether the service may be reached with this token: listed services need none.
func invited(svc *syncer.Service, token string) bool {
	return !svc.Unlisted || (token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(svc.Invite)) == 1)
}

func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system random source failing is not recoverable
	}
	return hex.EncodeToString(b)
}

// SetVisibility lists a service for everyone or makes it invitation-only (making a link on first use).
func (h *Hub) SetVisibility(ctx context.Context, owner immich.User, service, visibility string) error {
	v, err := h.ownedService(ctx, owner, service)
	if err != nil {
		return err
	}
	invite := v.Invite
	if visibility == store.VisInvite {
		if invite == "" {
			invite = newToken()
		}
	} else {
		visibility = store.VisListed
	}
	if err := h.Store.SetServiceVisibility(ctx, v.ID, visibility, invite); err != nil {
		return err
	}
	return h.Reload(ctx)
}

// NewInvite replaces the invitation link; the old one stops working at once.
func (h *Hub) NewInvite(ctx context.Context, owner immich.User, service string) error {
	v, err := h.ownedService(ctx, owner, service)
	if err != nil {
		return err
	}
	if err := h.Store.SetServiceVisibility(ctx, v.ID, store.VisInvite, newToken()); err != nil {
		return err
	}
	h.event(ctx, service, owner.ID, "invite", i18n.Pack("{name} made a new invitation link for {service}", "name", owner.Name, "service", service))
	return h.Reload(ctx)
}

func (h *Hub) ownedService(ctx context.Context, owner immich.User, name string) (*store.Service, error) {
	if !h.SelfService() {
		return nil, ErrNotJoinable
	}
	stored, err := h.Store.Services(ctx)
	if err != nil {
		return nil, err
	}
	for i := range stored {
		if stored[i].Name == name && stored[i].OwnerID == owner.ID {
			return &stored[i], nil
		}
	}
	return nil, ErrNotOwner
}

// InviteOffer is what an invitation leads to.
func (h *Hub) InviteOffer(ctx context.Context, user uuid.UUID, token string) (*Offer, error) {
	if !h.SelfService() || token == "" {
		return nil, ErrNoInvite
	}
	for _, svc := range h.Services() {
		if svc.Unlisted && invited(svc, token) {
			if Takes(svc, user) || svc.OwnerID == user {
				return nil, ErrAlready
			}
			state := "join"
			if svc.NeedsApproval {
				state = "request"
				if r, err := h.request(ctx, svc.Label(), user); err != nil {
					return nil, err
				} else if r != nil {
					state = r.Status
				}
			}
			return &Offer{Service: svc, State: state}, nil
		}
	}
	return nil, ErrNoInvite
}

// InviteOf returns the owner's invitation token for a service they own (empty if it is listed).
func (h *Hub) InviteOf(svc *syncer.Service) string {
	if svc.Unlisted {
		return svc.Invite
	}
	return ""
}
