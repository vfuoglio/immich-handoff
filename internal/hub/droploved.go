package hub

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// ErrNoLoved is returned when no photo has a heart and none is pinned, so there is nothing to put in the album.
var ErrNoLoved = errors.New("no photo has a heart yet, and none is pinned")

// maxLoved is the most photos the album of loved photos holds.
const maxLoved = 100

// MakeLovedAlbum puts the photos guests loved, and the ones the host pinned, in an album of the owner's called "<box> loved
// by guests": the pinned first, then the ones with the most hearts, at most a hundred, never one the host hid. Running it
// again corrects the album (what is no longer loved leaves it, anything the owner added themselves stays). It uses the
// owner's own session, so only the owner may. It returns the album's name and how many photos it holds.
func (h *Hub) MakeLovedAlbum(ctx context.Context, sess *immich.API, owner immich.User, id string) (string, int, error) {
	b, _, err := h.dropBoxFor(ctx, owner.ID, id, "owner")
	if err != nil {
		return "", 0, err
	}
	loved, err := h.Store.LovedAssetIDs(ctx, b.ID, maxLoved)
	if err != nil {
		return "", 0, err
	}
	if len(loved) == 0 {
		return "", 0, ErrNoLoved
	}
	all, err := h.Store.DropAssetIDs(ctx, b.ID)
	if err != nil {
		return "", 0, err
	}
	keep := map[uuid.UUID]bool{}
	for _, s := range loved {
		if u, err := uuid.Parse(s); err == nil {
			keep[u] = true
		}
	}
	var boxAssets []immich.AssetResponseDto
	for _, s := range all {
		if u, err := uuid.Parse(s); err == nil {
			boxAssets = append(boxAssets, immich.AssetResponseDto{Id: u})
		}
	}
	name := b.Name + " loved by guests"
	if err := h.fillAlbum(ctx, sess, name, boxAssets, keep); err != nil {
		return "", 0, fmt.Errorf("could not fill the album %q: %w", name, err)
	}
	h.dropEvent(ctx, b.ID, store.DropCreated, i18n.Pack("{name} put {n} loved photos in the album {album}", "name", owner.Name, "n", len(keep), "album", name), "")
	return name, len(keep), nil
}
