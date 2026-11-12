package hub

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// The one-tap actions a message can carry. Each is an existing action of the page, done by the person it was made for.
const (
	ActionApprove       = "approve"        // the owner approves a request to join
	ActionDecline       = "decline"        // the owner declines it
	ActionResumeService = "resume-service" // the owner resumes a service they paused
	ActionResumeSender  = "resume-sender"  // a sender resumes their own sending
)

var (
	// ErrActionInvalid: the link is unknown, already used, expired, or about something that no longer needs doing.
	ErrActionInvalid = errors.New("this link is no longer valid")
	// ErrAlreadyDecided: the request was decided (on the page, or by the other link) before this one was used.
	ErrAlreadyDecided = errors.New("that request was already decided")
)

// ActionSpec says which action to offer to whom, with an event.
type ActionSpec struct {
	Recipient     uuid.UUID
	RecipientName string
	Kind          string
	Service       string
	Target        string // what it is about: a request's id
	Group         string // specs made for one decision share a group
}

// ActionLink is a link in a message. Label is the English words of the button, which the channel translates for the
// reader; URL is absolute.
type ActionLink struct{ Label, URL string }

func actionLabel(kind string) string {
	switch kind {
	case ActionApprove:
		return "Approve"
	case ActionDecline:
		return "Decline"
	case ActionResumeService:
		return "Resume service"
	}
	return "Resume sending"
}

// linker makes the links of an event for the people who will read it, only for those who ask (a person who gets no mail
// and no notification costs no link), and the same links for every channel. It is nil when there is nothing to offer or
// no address to build links from.
func (h *Hub) linker(ctx context.Context, specs []ActionSpec) func(uuid.UUID) []ActionLink {
	if len(specs) == 0 || h.AppURL == "" || h.Store == nil {
		return nil
	}
	var mu sync.Mutex
	made := map[uuid.UUID][]ActionLink{}
	base := strings.TrimRight(h.AppURL, "/") + "/a/"
	return func(user uuid.UUID) []ActionLink {
		mu.Lock()
		defer mu.Unlock()
		if links, ok := made[user]; ok {
			return links
		}
		var links []ActionLink
		for _, s := range specs {
			if s.Recipient != user {
				continue
			}
			token, err := h.Store.ActionMake(ctx, store.ActionLink{UserID: s.Recipient, UserName: s.RecipientName, Kind: s.Kind, Service: s.Service, Target: s.Target, Group: s.Group})
			if err != nil {
				h.Log.Warn("could not make an action link", "err", err)
				continue
			}
			links = append(links, ActionLink{Label: actionLabel(s.Kind), URL: base + token})
		}
		made[user] = links
		return links
	}
}

// ActionInfo is what an action link is about, for the page that asks before it does it.
type ActionInfo struct {
	Kind, Service string
	Name          string // approve and decline: who asked
}

// ActionPage describes a link that can still be used, without using it. A link whose reason has passed (the request
// was decided on the page, the service was resumed already) is as good as unknown.
func (h *Hub) ActionPage(ctx context.Context, token string) (ActionInfo, bool) {
	if h.Store == nil {
		return ActionInfo{}, false
	}
	a, ok, err := h.Store.ActionGet(ctx, token)
	if err != nil || !ok {
		return ActionInfo{}, false
	}
	info := ActionInfo{Kind: a.Kind, Service: a.Service}
	switch a.Kind {
	case ActionApprove, ActionDecline:
		r, found := h.pendingRequest(ctx, a.Service, a.Target)
		if !found {
			return ActionInfo{}, false
		}
		info.Name = r.Name
	case ActionResumeService:
		if svc := h.service(a.Service); svc == nil || !svc.Paused || svc.OwnerID != a.UserID {
			return ActionInfo{}, false
		}
	case ActionResumeSender:
		if !h.senderPaused(ctx, a.UserID, a.Service) {
			return ActionInfo{}, false
		}
	default:
		return ActionInfo{}, false
	}
	return info, true
}

func (h *Hub) pendingRequest(ctx context.Context, service, id string) (store.Request, bool) {
	all, err := h.Store.Requests(ctx)
	if err != nil {
		return store.Request{}, false
	}
	for _, r := range all {
		if r.ID == id && r.Service == service && r.Status == store.StatusPending {
			return r, true
		}
	}
	return store.Request{}, false
}

func (h *Hub) senderPaused(ctx context.Context, user uuid.UUID, service string) bool {
	links, err := h.Store.Links(ctx)
	if err != nil {
		return false
	}
	for _, l := range links {
		if l.ImmichUserID == user && l.Service == service && l.Role == store.RoleSender {
			return l.Paused
		}
	}
	return false
}

// RunAction spends the link and does what it says, as the person it was made for, through the same calls the page's
// buttons use (so every check, Activity line and message is the one they give). The link is spent first, so two taps
// cannot both act; if the action is then refused, the reason is returned and the person is sent to their page.
func (h *Hub) RunAction(ctx context.Context, token string) (ActionInfo, error) {
	if h.Store == nil {
		return ActionInfo{}, ErrActionInvalid
	}
	a, ok, err := h.Store.ActionUse(ctx, token)
	if err != nil {
		return ActionInfo{}, err
	}
	if !ok {
		return ActionInfo{}, ErrActionInvalid
	}
	info := ActionInfo{Kind: a.Kind, Service: a.Service}
	person := immich.User{ID: a.UserID, Name: a.UserName}
	switch a.Kind {
	case ActionApprove, ActionDecline:
		r, found := h.pendingRequest(ctx, a.Service, a.Target)
		if !found {
			return info, ErrAlreadyDecided
		}
		info.Name = r.Name
		return info, h.Decide(ctx, person, a.Service, a.Target, a.Kind == ActionApprove)
	case ActionResumeService:
		return info, h.SetServicePaused(ctx, person, a.Service, false)
	case ActionResumeSender:
		return info, h.SetMemberPaused(ctx, person, a.Service, false)
	}
	return info, ErrActionInvalid
}

// ownerName is the name of the person who owns a service made on the page, for the link made for them.
func (h *Hub) ownerName(ctx context.Context, service string) string {
	all, err := h.Store.Services(ctx)
	if err != nil {
		return ""
	}
	for _, v := range all {
		if v.Name == service {
			return v.OwnerName
		}
	}
	return ""
}
