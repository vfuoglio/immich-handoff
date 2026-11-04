package hub

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// event appends to the activity log; problems writing it never stop the action it describes.
func (h *Hub) event(ctx context.Context, service string, user uuid.UUID, kind, text string) {
	if h.Store == nil {
		return
	}
	if err := h.Store.AddEvent(context.WithoutCancel(ctx), store.Event{Service: service, UserID: user, Kind: kind, Text: text}); err != nil {
		h.Log.Warn("could not write the activity log", "err", err)
	}
}

// KeyRejected implements syncer.Watcher: Immich no longer accepts the key behind this account, so it
// was deleted or revoked. The link (or the service, for a receiver key) is marked broken, nothing more
// is attempted with it, both sides are told, and no photo is touched.
func (h *Hub) KeyRejected(acct *syncer.Account) {
	if h.Store == nil {
		return
	}
	h.mu.RLock()
	id := ""
	for k, a := range h.accounts {
		if a == acct {
			id = k
			break
		}
	}
	h.mu.RUnlock()
	if id == "" {
		return // a key from the configuration: the pass already reports it
	}
	ctx := context.Background()
	note := "Immich rejected the API key (it was deleted or revoked) on " + time.Now().UTC().Format("2006-01-02")
	if ok, _ := h.Store.SetLinkState(ctx, id, store.StateBroken, note); ok {
		links, _ := h.Store.Links(ctx)
		for _, l := range links {
			if l.ID == id {
				h.event(ctx, l.Service, l.ImmichUserID, "broken", l.Name+"'s connection to "+l.Service+" was cut: their API key was deleted")
				h.tell(fmt.Sprintf("%s's API key for %q was deleted in Immich; the link is paused. They can clean up and join again.", l.Name, l.Service))
			}
		}
	} else if ok, _ := h.Store.SetServiceState(ctx, id, store.StateBroken, note); ok {
		svcs, _ := h.Store.Services(ctx)
		for _, v := range svcs {
			if v.ID == id {
				h.event(ctx, v.Name, v.OwnerID, "broken", v.Name+" cannot receive: its owner's API key was deleted")
				h.tell(fmt.Sprintf("The receiver key of %q (owner %s) was deleted in Immich; senders are paused until the owner reconnects.", v.Name, v.OwnerName))
			}
		}
	} else {
		return
	}
	h.Log.Warn("an API key was rejected by Immich; the connection is paused", "account", acct.Name)
	if err := h.Reload(ctx); err != nil {
		h.Log.Error("reload after a rejected key failed", "err", err)
	}
}

func (h *Hub) tell(msg string) {
	if h.Notify != nil {
		h.Notify("immich-handoff: " + msg)
	}
}

// CheckKeys asks Immich whether each stored key is still accepted. It catches deleted keys even when
// no photo has been tagged lately.
func (h *Hub) CheckKeys(ctx context.Context) {
	h.mu.RLock()
	accts := make([]*syncer.Account, 0, len(h.accounts))
	for _, a := range h.accounts {
		accts = append(accts, a)
	}
	h.mu.RUnlock()
	for _, a := range accts {
		if alive, _, err := a.API.KeyAlive(ctx); err == nil && !alive {
			h.KeyRejected(a)
		}
	}
}

// ReconnectService gives a service whose receiver key was deleted a new key, so senders can resume.
func (h *Hub) ReconnectService(ctx context.Context, sess *immich.API, owner immich.User, name string) error {
	if !h.SelfService() {
		return ErrNotJoinable
	}
	stored, err := h.Store.Services(ctx)
	if err != nil {
		return err
	}
	var v *store.Service
	for i := range stored {
		if stored[i].Name == name && stored[i].OwnerID == owner.ID {
			v = &stored[i]
		}
	}
	if v == nil {
		return ErrNotOwner
	}
	keyID, secret, err := sess.CreateAPIKey(ctx, keyName(name)+" (receiving)", immich.ReceiverPermissions)
	if err != nil {
		return fmt.Errorf("could not create an API key in your Immich account: %w", err)
	}
	undo := func() { sess.DeleteAPIKey(context.WithoutCancel(ctx), keyID) }
	api, err := h.Connect(secret)
	if err != nil {
		undo()
		return err
	}
	if me, err := api.Me(ctx); err != nil || me.ID != owner.ID {
		undo()
		return fmt.Errorf("the new key does not work for your account: %v", err)
	}
	if err := h.Store.ReplaceServiceKey(ctx, v.ID, keyID, secret); err != nil {
		undo()
		return err
	}
	sess.DeleteAPIKey(ctx, v.KeyID) // the old key, if it is still there
	h.mu.Lock()
	delete(h.accounts, v.ID) // force a fresh client with the new key
	h.mu.Unlock()
	if err := h.Reload(ctx); err != nil {
		return err
	}
	h.event(ctx, name, owner.ID, "reconnected", owner.Name+" reconnected "+name)
	h.Log.Info("service reconnected", "service", name, "owner", owner.Email)
	if h.Wake != nil {
		h.Wake("reconnected: " + name)
	}
	return nil
}

// BrokenLink is a connection of the person that Immich rejected.
type BrokenLink struct{ Service, Note string }

func (h *Hub) BrokenFor(ctx context.Context, user uuid.UUID) ([]BrokenLink, error) {
	if !h.SelfService() {
		return nil, nil
	}
	links, err := h.Store.Links(ctx)
	if err != nil {
		return nil, err
	}
	var out []BrokenLink
	for _, l := range links {
		if l.ImmichUserID == user && l.State == store.StateBroken {
			out = append(out, BrokenLink{l.Service, l.Note})
		}
	}
	return out, nil
}

// Member is a sender of a service the person owns, with the state of their connection.
type Member struct{ Name, State string }

func (h *Hub) MembersOf(ctx context.Context, service string) ([]Member, error) {
	links, err := h.Store.Links(ctx)
	if err != nil {
		return nil, err
	}
	var out []Member
	for _, l := range links {
		if l.Service == service && l.Role == store.RoleSender && l.State != store.StateOrphaned {
			out = append(out, Member{l.Name, l.State})
		}
	}
	return out, nil
}

// Activity is a line of the log the person may see: events about services they take part in.
type Activity struct {
	At   time.Time
	Text string
}

func (h *Hub) Activity(ctx context.Context, user uuid.UUID, limit int) ([]Activity, error) {
	if !h.SelfService() {
		return nil, nil
	}
	mine := map[string]bool{}
	for _, svc := range h.Services() {
		if Takes(svc, user) || svc.OwnerID == user {
			mine[svc.Label()] = true
		}
	}
	links, err := h.Store.Links(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		if l.ImmichUserID == user {
			mine[l.Service] = true
		}
	}
	events, err := h.Store.Events(ctx, 200)
	if err != nil {
		return nil, err
	}
	var out []Activity
	for _, e := range events {
		if mine[e.Service] || e.UserID == user {
			out = append(out, Activity{e.At, e.Text})
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}
