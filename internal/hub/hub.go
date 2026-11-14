// Package hub combines the services from the configuration with the people who joined them from
// the web page, and implements joining and leaving.
package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/bridge"
	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/live"
	"github.com/vfuoglio/immich-handoff/internal/notify"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// Errors a person can be shown as they are.
var (
	ErrNotJoinable = errors.New("this service does not accept new senders")
	// ErrPoolConfirm: joining an event pool needs a yes to what it means.
	ErrPoolConfirm = errors.New("joining a pool means every photo you tag goes to everyone in it, and everyone's photos come to you: say that you understand")
	// ErrPoolEnded: the pool has reached its end.
	ErrPoolEnded     = errors.New("this pool has ended")
	ErrAlready       = errors.New("you already take part in this service")
	ErrNotJoined     = errors.New("you have not joined this service from this page")
	ErrNeedsApproval = errors.New("the owner of this service has not approved you yet")
)

// Options are the defaults for services created on the web page.
type Options struct {
	InboxTag, RefusedTag, SentTag string
	ExpiryDays                    int
}

type Hub struct {
	Static       []*syncer.Service // from the environment or the routes file
	Store        *store.Store      // nil: self-service is switched off
	Connect      func(key string) (*immich.API, error)
	Hook         syncer.Webhook // used to create the sender's workflow when someone joins
	Wake         func(reason string)
	Retry        func()           // sets up missing workflows in the background (Immich may still be loading its plugins)
	Notify       notify.Func      // tells an admin something needs attention (optional)
	Bus          *live.Bus        // tells the open pages what changed, as it happens (optional)
	People       *People          // writes to the people who asked for email (optional)
	Push         *Push            // sends notifications to the devices people allowed (optional)
	MailIn       *MailIn          // reads the mailbox that photos are mailed to (optional)
	AppURL       string           // how people reach the page; the links in messages are built from it (optional: without it a message carries no action links)
	Now          func() time.Time // the clock; nil: time.Now (tests set it)
	FrameRefresh time.Duration    // how often frames read their album again (FRAME_REFRESH; zero: two minutes)
	Log          *slog.Logger
	Options      Options // defaults for services people create

	// ReceiverDelete: new receiver keys may move copies to their owner's trash, and existing ones get or lose the permission
	// when their owner opens the page (RECEIVER_DELETE, on by default).
	ReceiverDelete bool

	// Bridge: whether services may hand photos to a receiver on another Immich server, and which addresses the app
	// may then connect to (BRIDGE_ENABLE, BRIDGE_ALLOW_PRIVATE; both off by default).
	Bridge bridge.Settings

	// StageDir is where files wait for a host's approval in a drop box that asks for it (empty: no such drop boxes).
	StageDir string

	// PhoneDir is where the pieces of photos sent from a phone wait until the file is whole (empty: phones cannot send).
	// PhoneCap is the most one person may have waiting, PhoneMaxFile the most for one file and PhoneKeep how long
	// unfinished pieces are kept; zero means the defaults (4 GB, 2 GB, 48 hours).
	PhoneDir     string
	PhoneCap     int64
	PhoneMaxFile int64
	PhoneKeep    time.Duration

	mu           sync.RWMutex
	snapshot     []*syncer.Service
	accounts     map[string]*syncer.Account // by link id, so pointers stay stable between reloads
	granted      map[uuid.UUID]bool         // people whose keys were given the stack permissions in this run
	drop         dropGuard                  // what the drop boxes' limits know about their guests
	wall         wallCache                  // the pictures a wall fetched last
	hearts       heartGuard                 // when each guest gave hearts in the last hour
	slots        chan struct{}              // places for downloads of originals (see originals)
	slotsOnce    sync.Once
	phoneLocks   sync.Map   // one lock for each file being received, by its id
	phoneStartMu sync.Mutex // starting a file checks what is waiting
	zipping      sync.Map   // the boxes a ZIP is being made of
	zipMu        sync.Mutex
	zipCount     map[string]int          // how many ZIPs of a box guests are having made
	hlRun        map[string]bool         // boxes whose best-of is being made now
	digestRun    map[string]bool         // services whose digest is being made now
	frames       frameCache              // the albums frames last read
	remotes      map[string]*remoteEntry // receivers on other servers, by service, kept between reloads
	retries      map[string]*time.Timer  // when each unreachable receiver on another server is tried again
	bridgeTries  bridgeGuard             // what the invitation pages know about who tried
}

// SelfService reports whether people can join and leave from the web page.
func (h *Hub) SelfService() bool { return h.Store != nil }

// Services implements syncer.Provider.
func (h *Hub) Services() []*syncer.Service {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.snapshot == nil {
		return h.Static
	}
	return h.snapshot
}

// Reload rebuilds the merged view from the store.
func (h *Hub) Reload(ctx context.Context) error {
	var links []store.Link
	var owned []store.Service
	var bridges map[string]store.Bridge
	if h.Store != nil {
		var err error
		if links, err = h.Store.Links(ctx); err != nil {
			return err
		}
		if owned, err = h.Store.Services(ctx); err != nil {
			return err
		}
		all, err := h.Store.Bridges(ctx)
		if err != nil {
			return err
		}
		bridges = map[string]store.Bridge{}
		for _, b := range all {
			bridges[b.Service] = b
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.accounts == nil {
		h.accounts = map[string]*syncer.Account{}
	}
	account := func(id, name string, user uuid.UUID, key, mode string, album uuid.UUID, albumName string, paused bool) *syncer.Account {
		// the account is kept between reloads, unless the sender linked or unlinked an album since: an
		// account's fields never change while the syncer may be reading them, so it is replaced instead
		if a, ok := h.accounts[id]; ok && a.WatchAlbum == album && a.Paused == paused {
			return a
		}
		api, err := h.Connect(key)
		if err != nil {
			h.Log.Error("cannot use stored key", "id", id, "err", err)
			return nil
		}
		a := &syncer.Account{Name: name, API: api, UserID: user, Mode: mode, WatchAlbum: album, WatchAlbumName: albumName, Paused: paused}
		h.accounts[id] = a
		return a
	}
	live := map[string]bool{}
	extra := map[string][]*syncer.Account{}
	for _, l := range links {
		if l.State != store.StateActive || l.Role != store.RoleSender {
			continue
		}
		live[l.ID] = true
		if a := account(l.ID, l.Name, l.ImmichUserID, l.Key, l.Mode, l.AlbumID, l.AlbumName, l.Paused); a != nil {
			extra[l.Service] = append(extra[l.Service], a)
		}
	}
	merged := make([]*syncer.Service, 0, len(h.Static)+len(owned))
	for _, svc := range h.Static {
		cp := *svc
		cp.Senders = append(append([]*syncer.Account(nil), svc.Senders...), extra[svc.Label()]...)
		merged = append(merged, &cp)
	}
	for _, v := range owned {
		live[v.ID] = true
		rcvMode := ""
		if v.Pool {
			rcvMode = syncer.ModeCopy // the owner's own photos stay with them too
		}
		rcv := account(v.ID, v.OwnerName, v.OwnerID, v.Key, rcvMode, uuid.Nil, "", false)
		if rcv == nil {
			continue
		}
		svc := &syncer.Service{
			Name: v.Name, Tag: v.Tag, InboxTag: v.InboxTag, RefusedTag: v.Refused, ExpiryDays: v.ExpiryDays,
			OwnerID: v.OwnerID, AllowJoin: v.Policy == store.PolicyOpen, NeedsApproval: v.Policy == store.PolicyApproval,
			Unlisted: v.Visibility == store.VisInvite, Invite: v.Invite, SentTag: v.SentTag, Album: v.Album,
			Paused: v.Paused, LimitPhotos: v.LimitPhotos, LimitMB: v.LimitMB, LifetimeDays: v.LifetimeDays,
			Quiet:   syncer.Quiet{From: v.QuietFrom, To: v.QuietTo, LargeMB: v.QuietLargeMB},
			Accept:  syncer.Accept{MinRating: v.AcceptRating, PhotosOnly: v.AcceptPhotos, Orientation: v.AcceptOrient, MaxMB: v.AcceptMaxMB},
			Senders: extra[v.Name], Receivers: []*syncer.Account{rcv},
			Pool: v.Pool, EndsAt: v.EndsAt,
		}
		if v.Pool {
			// everybody sends and everybody receives: the owner's account and each member's are in both lists
			svc.Mode = syncer.ModeCopy
			svc.Senders = append([]*syncer.Account{rcv}, extra[v.Name]...)
			svc.Receivers = append([]*syncer.Account{rcv}, extra[v.Name]...)
		}
		if b, ok := bridges[v.Name]; ok {
			// a service with a receiver on another server never runs without it: the original would be removed
			// with a copy in only some of the places it was promised
			if r, why := h.remoteFor(b); r != nil {
				svc.Remote = r
			} else {
				svc.Broken, svc.BrokenFix, svc.Receivers = why, "Set BRIDGE_ENABLE=true again, or remove the receiver on another server on the service's card.", nil
			}
		}
		if v.State == store.StateBroken {
			svc.Broken, svc.Receivers = v.Note, nil // nothing may run until the owner reconnects
		}
		merged = append(merged, svc)
	}
	for id := range h.accounts {
		if !live[id] {
			delete(h.accounts, id)
		}
	}
	for name := range h.remotes {
		if _, ok := bridges[name]; !ok {
			delete(h.remotes, name)
		}
	}
	h.snapshot = merged
	return nil
}

func (h *Hub) service(name string) *syncer.Service {
	for _, s := range h.Services() {
		if s.Label() == name {
			return s
		}
	}
	return nil
}

// Takes reports whether the user already sends to or receives from the service.
func Takes(svc *syncer.Service, user uuid.UUID) bool {
	for _, group := range [][]*syncer.Account{svc.Senders, svc.Receivers} {
		for _, a := range group {
			if a.UserID == user {
				return true
			}
		}
	}
	return false
}

// Joinable lists the services this person may add themselves to.
func (h *Hub) Joinable(user uuid.UUID) []*syncer.Service {
	if !h.SelfService() {
		return nil
	}
	var out []*syncer.Service
	for _, svc := range h.Services() {
		if svc.AllowJoin && !Takes(svc, user) {
			out = append(out, svc)
		}
	}
	return out
}

// Joined lists the services this person joined from the page (and so can leave from it).
func (h *Hub) Joined(ctx context.Context, user uuid.UUID) (map[string]bool, error) {
	out := map[string]bool{}
	if !h.SelfService() {
		return out, nil
	}
	links, err := h.Store.Links(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		if l.ImmichUserID == user && l.Role == store.RoleSender {
			out[l.Service] = true
		}
	}
	return out, nil
}

func keyName(service string) string { return "handoff: " + service }

// Join makes the signed-in person a sender of the service. sess acts with their own Immich session.
// If the key or the membership cannot be created, everything already done is undone, so no orphan
// key or record is left behind. If only the workflow cannot be created yet (Immich is still loading
// its plugins), the person is a member anyway and the workflow is retried in the background.
func (h *Hub) Join(ctx context.Context, sess *immich.API, user immich.User, service, invite, mode string) error {
	return h.JoinWith(ctx, sess, user, service, invite, mode, false)
}

// JoinWith is Join for a person who has been told what joining an event pool means and said yes: every photo they tag goes
// to everyone in the pool, and everyone's photos come to them. Without that yes a pool refuses (ErrPoolConfirm); for any
// other service poolConfirmed makes no difference.
func (h *Hub) JoinWith(ctx context.Context, sess *immich.API, user immich.User, service, invite, mode string, poolConfirmed bool) error {
	if !h.SelfService() {
		return ErrNotJoinable
	}
	svc := h.service(service)
	if svc == nil || !(svc.AllowJoin || svc.NeedsApproval) {
		return ErrNotJoinable
	}
	if svc.Pool {
		if svc.Ended(h.now()) {
			return ErrPoolEnded
		}
		if !poolConfirmed {
			return ErrPoolConfirm
		}
		mode = syncer.ModeCopy // nobody's originals are ever removed in a pool
	}
	if Takes(svc, user.ID) {
		return ErrAlready
	}
	if err := h.tagFree(user.ID, svc); err != nil {
		return err
	}
	var approval *store.Request
	if svc.NeedsApproval {
		var err error
		if approval, err = h.request(ctx, service, user.ID); err != nil {
			return err
		}
		if approval == nil || approval.Status != store.StatusApproved {
			return ErrNeedsApproval
		}
	} else if !invited(svc, invite) { // an approved person already got in through the invitation
		return ErrNotJoinable
	}

	// What happens to the sender's originals is the sender's own choice, made here. Keeping them is the
	// default, and then the key gets no delete permission at all.
	if mode != syncer.ModeMove {
		mode = syncer.ModeCopy
	}
	perms := immich.SenderPermissions
	if mode == syncer.ModeCopy {
		perms = immich.SenderPermissionsCopy
	}
	if svc.Pool { // the same key sends and receives
		perms = immich.PoolPermissionsFor(svc.Album != "", h.ReceiverDelete)
	}
	keyID, secret, err := sess.CreateAPIKey(ctx, keyName(service), perms)
	if err != nil {
		return fmt.Errorf("could not create an API key in your Immich account: %w", err)
	}
	undoKey := func() {
		if err := sess.DeleteAPIKey(context.WithoutCancel(ctx), keyID); err != nil {
			h.Log.Error("could not remove the key created for a failed join", "err", err)
		}
	}

	api, err := h.Connect(secret)
	if err != nil {
		undoKey()
		return err
	}
	if me, err := api.Me(ctx); err != nil || me.ID != user.ID {
		undoKey()
		return fmt.Errorf("the new key does not work for your account: %v", err)
	}
	link, err := h.Store.AddLink(ctx, store.Link{
		Service: service, Role: store.RoleSender, ImmichUserID: user.ID, Name: user.Name, Email: user.Email, KeyID: keyID, Key: secret, Mode: mode,
	})
	if err != nil {
		undoKey()
		if errors.Is(err, store.ErrExists) {
			return ErrAlready
		}
		return err
	}
	if err := h.Reload(ctx); err != nil {
		h.Store.RemoveLink(ctx, link.ID)
		undoKey()
		return err
	}

	if h.Hook.URL != "" {
		if err := h.provision(ctx, svc, api); err != nil {
			h.Log.Warn("joined, but the workflow is not ready yet; retrying in the background", "user", user.Email, "err", err)
			if h.Retry != nil {
				go h.Retry()
			}
		}
	}
	if approval != nil {
		h.Store.RemoveRequest(ctx, approval.ID) // the approval is used up; the link is the record now
	}
	if mode == syncer.ModeMove {
		h.event(ctx, service, user.ID, "joined", i18n.Pack("{name} started sending to {service} (originals are moved to their trash)", "name", user.Name, "service", service))
	} else {
		h.event(ctx, service, user.ID, "joined", i18n.Pack("{name} started sending to {service} (originals are kept)", "name", user.Name, "service", service))
	}
	h.Log.Info("joined", "service", service, "user", user.Email)
	if h.Wake != nil {
		h.Wake("joined: " + user.Email)
	}
	return nil
}

func (h *Hub) provision(ctx context.Context, svc *syncer.Service, api *immich.API) error {
	tag, err := api.FindTag(ctx, svc.Tag, true)
	if err != nil || tag == nil {
		return fmt.Errorf("tag %q: %v", svc.Tag, err)
	}
	_, err = api.EnsureWorkflow(ctx, immich.WorkflowSpec{
		Name: svc.WorkflowName(), TagID: *tag, WebhookURL: h.Hook.URL, HeaderName: h.Hook.Header, HeaderSecret: h.Hook.Secret,
	})
	return err
}

// workflowName is the workflow a service keeps in each sender's account (see syncer.Service.WorkflowName).
func workflowName(service string) string {
	if service == "default" {
		return "immich-handoff"
	}
	return "immich-handoff: " + service
}

// Leave removes the person's workflow and key from Immich and forgets the membership, also when the
// service has since been removed by its owner. Problems removing things in Immich are logged but do
// not stop the membership from ending.
func (h *Hub) Leave(ctx context.Context, sess *immich.API, user immich.User, service string) error {
	if !h.SelfService() {
		return ErrNotJoined
	}
	links, err := h.Store.Links(ctx)
	if err != nil {
		return err
	}
	var link *store.Link
	for i := range links {
		if links[i].Service == service && links[i].Role == store.RoleSender && links[i].ImmichUserID == user.ID {
			link = &links[i]
		}
	}
	if link == nil {
		return ErrNotJoined
	}
	if id, err := sess.FindWorkflow(ctx, workflowName(service)); err != nil {
		h.Log.Warn("leave: could not look up the workflow", "err", err)
	} else if id != nil {
		if err := sess.DeleteWorkflow(ctx, *id); err != nil {
			h.Log.Warn("leave: could not delete the workflow", "err", err)
		}
	}
	if err := sess.DeleteAPIKey(ctx, link.KeyID); err != nil {
		h.Log.Warn("leave: could not delete the API key (it may already be gone)", "err", err)
	}
	if err := h.Store.RemoveLink(ctx, link.ID); err != nil {
		return err
	}
	h.event(ctx, service, user.ID, "left", i18n.Pack("{name} stopped sending to {service}", "name", user.Name, "service", service))
	h.Log.Info("left", "service", service, "user", user.Email)
	return h.Reload(ctx)
}

func modeWords(mode string) string {
	if mode == syncer.ModeMove {
		return "originals are moved to their trash"
	}
	return "originals are kept"
}

func (h *Hub) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}
