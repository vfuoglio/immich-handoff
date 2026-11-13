package hub

import (
	"context"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// The moderator link: a page with a link of its own where a helper, who needs no account anywhere, looks at the files
// that wait in a box and approves or rejects them. It is what a friend at the wedding is given. It shows what waits and
// what the box kept, and can do those two things; it shows nothing of the owner's library, the box's settings, its link
// or its key, and it can neither pause nor end the box.

// SetDropModerator makes, renews or ends a box's moderator link. Only the owner may. With on, a new link replaces the old
// one; without, the link stops working. It returns the token ("" when ended).
func (h *Hub) SetDropModerator(ctx context.Context, owner immich.User, id string, on bool) (string, error) {
	b, _, err := h.dropBoxFor(ctx, owner.ID, id, "owner")
	if err != nil {
		return "", err
	}
	token := ""
	if on {
		token = newToken() + newToken()
	} else if b.ModToken == "" {
		return "", nil
	}
	if err := h.Store.SetDropMod(ctx, b.ID, token); err != nil {
		return "", err
	}
	text := "{name} made a moderator link"
	if !on {
		text = "{name} ended the moderator link"
	}
	h.dropEvent(ctx, b.ID, store.DropCreated, i18n.Pack(text, "name", owner.Name), "")
	return token, nil
}

// ModBox finds the box a moderator link belongs to. A link that is unknown, ended or of a revoked box gives ErrDropGone.
func (h *Hub) ModBox(ctx context.Context, token string) (store.DropBox, error) {
	if h.Store == nil || len(token) < 32 || len(token) > 128 {
		return store.DropBox{}, ErrDropGone
	}
	b, err := h.Store.DropBoxByMod(ctx, token)
	if err != nil {
		return store.DropBox{}, ErrDropGone
	}
	b.Key = "" // a moderator never has it
	return b, nil
}

// ModPending lists the files of a box that wait for a decision, and how many there are in all.
func (h *Hub) ModPending(ctx context.Context, b store.DropBox, limit int) ([]store.DropPending, int, error) {
	n, _, err := h.Store.DropPendingTotals(ctx, b.ID)
	if err != nil {
		return nil, 0, err
	}
	list, err := h.Store.DropPendingOf(ctx, b.ID, limit)
	return list, n, err
}

// ModPendingFile finds one waiting file of the box, so the moderator can see it before deciding.
func (h *Hub) ModPendingFile(ctx context.Context, b store.DropBox, id string) (store.DropPending, error) {
	p, err := h.Store.DropPendingByID(ctx, id)
	if err != nil || p.BoxID != b.ID {
		return store.DropPending{}, ErrNoDropBox
	}
	return p, nil
}

// ModDecide approves or rejects waiting files of the box, the ones named or all of them, as a moderator: the same as a
// co-host who manages does, and logged as "a moderator". It returns how many were done and how many could not be stored.
func (h *Hub) ModDecide(ctx context.Context, b store.DropBox, only []string, approve bool) (done, failed int, err error) {
	full, err := h.Store.DropBox(ctx, b.ID) // the key, which only the app holds
	if err != nil || full.State == store.DropRevoked {
		return 0, 0, ErrDropGone
	}
	return h.decide(ctx, full, "", only, approve)
}
