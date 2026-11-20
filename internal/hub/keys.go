package hub

import (
	"context"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// EnsureKeyPermissions brings the person's keys in line with what this version of the app wants of them, once per person
// per run: their sender keys read stacks, and the receiver keys of the services they own create them (a whole stack is
// handed over) and, when the administrator allows it (RECEIVER_DELETE, on by default), move copies to the trash (copies
// that expire, a hand-over taken back). When the administrator has switched that off, the permission is taken off again. Keys made
// before stacks were handed over lack both, and a key cannot change itself, so this acts with the person's own session
// when they open the page. Without the permissions nothing breaks: only the tagged photo of a stack goes, and a
// receiver's photos arrive separately (the app says so in its log).
func (h *Hub) EnsureKeyPermissions(ctx context.Context, sess *immich.API, user immich.User) {
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
	h.syncPetKeys(ctx, sess, user)
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
			if d, on, _ := h.Store.DigestFor(ctx, v.Name); on && d.Enabled { // a key made again keeps what the digest needs
				for _, p := range immich.DigestPermissions {
					if err := sess.AddKeyPermission(ctx, v.KeyID, p); err != nil {
						h.Log.Debug("could not add a digest permission to a receiver key", "service", v.Name, "permission", p, "err", err)
						ok = false
						break
					}
				}
			}
			if h.ReceiverDelete {
				if err := sess.AddKeyPermission(ctx, v.KeyID, "asset.delete"); err != nil {
					h.Log.Debug("could not add asset.delete to a receiver key", "service", v.Name, "err", err)
					ok = false
				}
			} else if err := sess.RemoveKeyPermission(ctx, v.KeyID, "asset.delete"); err != nil {
				h.Log.Debug("could not take asset.delete off a receiver key", "service", v.Name, "err", err)
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

// ForgetGranted makes EnsureKeyPermissions do its work again for everyone, as it does after a restart (for tests).
func (h *Hub) ForgetGranted() {
	h.mu.Lock()
	h.granted = nil
	h.mu.Unlock()
}

// syncPetKeys gives the sender key of each pet rule that uses the sidecar the permission to look at previews (the sidecar is sent a
// preview, never the original), and takes it off again when the sidecar is switched off (PET_SIDECAR_URL removed).
func (h *Hub) syncPetKeys(ctx context.Context, sess *immich.API, user immich.User) {
	rules, err := h.Store.RulesOf(ctx, user.ID)
	if err != nil {
		return
	}
	links, err := h.Store.Links(ctx)
	if err != nil {
		return
	}
	done := map[uuid.UUID]bool{}
	for _, r := range rules {
		if !r.Cond.Pet() {
			continue
		}
		for _, l := range links {
			if l.ImmichUserID != user.ID || l.Service != r.Service || l.Role != store.RoleSender || l.KeyID == uuid.Nil || done[l.KeyID] {
				continue
			}
			done[l.KeyID] = true
			if h.Pets != nil {
				if prints, _ := h.Store.PetPrints(ctx, r.ID); len(prints) > 0 {
					if err := sess.AddKeyPermission(ctx, l.KeyID, "asset.view"); err != nil {
						h.Log.Debug("could not add asset.view to a sender key for a pet rule", "service", l.Service, "err", err)
					}
				}
			} else if err := sess.RemoveKeyPermission(ctx, l.KeyID, "asset.view"); err != nil {
				h.Log.Debug("could not take asset.view off a sender key", "service", l.Service, "err", err)
			}
		}
	}
}
