package hub

import (
	"context"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// EnsureStackPermissions gives the person's keys the two permissions that handing over a whole stack needs, once per
// person per run: their sender keys read stacks, and the receiver keys of the services they own create them. Keys made
// before stacks were handed over lack both, and a key cannot change itself, so this acts with the person's own session
// when they open the page. Without the permissions nothing breaks: only the tagged photo of a stack goes, and a
// receiver's photos arrive separately (the app says so in its log).
func (h *Hub) EnsureStackPermissions(ctx context.Context, sess *immich.API, user immich.User) {
	if !h.SelfService() {
		return
	}
	h.mu.Lock()
	if h.granted[user.ID] {
		h.mu.Unlock()
		return
	}
	if h.granted == nil {
		h.granted = map[uuid.UUID]bool{}
	}
	h.granted[user.ID] = true // claimed now so two page loads at once do not both do the work
	h.mu.Unlock()

	ok := true
	if links, err := h.Store.Links(ctx); err != nil {
		ok = false
	} else {
		for _, l := range links {
			if l.ImmichUserID != user.ID || l.Role != store.RoleSender || l.State == store.StateOrphaned || l.KeyID == uuid.Nil {
				continue
			}
			if err := sess.AddKeyPermission(ctx, l.KeyID, "stack.read"); err != nil {
				h.Log.Debug("could not add stack.read to a sender key", "service", l.Service, "err", err)
				ok = false
			}
		}
	}
	if svcs, err := h.Store.Services(ctx); err != nil {
		ok = false
	} else {
		for _, v := range svcs {
			if v.OwnerID != user.ID || v.KeyID == uuid.Nil {
				continue
			}
			if err := sess.AddKeyPermission(ctx, v.KeyID, "stack.create"); err != nil {
				h.Log.Debug("could not add stack.create to a receiver key", "service", v.Name, "err", err)
				ok = false
			}
		}
	}
	if !ok { // try again the next time the page opens
		h.mu.Lock()
		delete(h.granted, user.ID)
		h.mu.Unlock()
	}
}
