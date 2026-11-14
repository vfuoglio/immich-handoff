package hub

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/peer"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Taking photos out of a shared album, under the rule both people agreed (docs/shared-albums-design.md). With "nothing", removing
// a photo changes nothing on the other side. With "follow", a photo taken out of the album is taken out of the other album too:
// only out of the album, the photo stays in the library, and deleting a photo never travels. A large removal waits for the
// person's yes, and a removal that came from the other side is not sent back.

// RemovedMsg lists the photos taken out of the sender's album.
type RemovedMsg struct {
	To        string   `json:"to"`
	Checksums []string `json:"checksums"`
}

var ErrNoRemoval = errors.New("there is no removal waiting for an answer")

const (
	removalAskOver = 5 // more photos than this at once ask first
	removalAskMin  = 2 // and a quarter of what is shared, at least this many
	maxRemovedMsg  = 2000
)

// askFirst says whether a removal is large enough to wait for the person's yes: more than five photos at once, or a quarter of
// what the album shares (and at least two).
func askFirst(removed, tracked int) bool {
	return removed > removalAskOver || (removed >= removalAskMin && removed*4 >= tracked)
}

// followRemovals finds the photos the person took out of the album since the last look and, under the rule "follow", tells the
// other app (or waits for the person's yes when it is a lot). A photo that is gone from the album because it was deleted or put
// in the trash is not a removal and is never told.
func (h *Hub) followRemovals(ctx context.Context, a store.SharedAlbum, p store.Peer, api *immich.API, in map[string]immich.AssetResponseDto) {
	rows, err := h.Store.LedgerTracked(ctx, a.ID)
	if err != nil {
		return
	}
	var removed []string
	for _, r := range rows {
		if _, here := in[r.Checksum]; here {
			if !r.Present {
				h.Store.LedgerSetPresent(ctx, a.ID, r.Checksum, true)
			}
			continue
		}
		if !r.Present {
			continue
		}
		h.Store.LedgerSetPresent(ctx, a.ID, r.Checksum, false)
		id, err := uuid.Parse(r.AssetID)
		if err != nil {
			continue
		}
		info, err := api.Asset(ctx, id)
		if err != nil || info.IsTrashed {
			continue // deleted: that never travels
		}
		removed = append(removed, r.Checksum)
	}
	if a.Deletion != store.DeleteFollow {
		// the photos are out of the album on this side; nothing is told, and they stay on the other side
		h.Store.LedgerGone(ctx, a.ID, removed)
		return
	}
	removed = append(removed, a.Pending...)
	removed = dedupe(removed)
	if len(removed) == 0 {
		return
	}
	if askFirst(len(removed), len(rows)) {
		if len(a.Pending) == 0 {
			h.event(ctx, "", a.OwnerID, "sharing_problem", i18n.PackN("You took {n} photo out of {album}; {other} is not told until you say so", "You took {n} photos out of {album}; {other} is not told until you say so", len(removed), "album", a.Name, "other", p.Name))
		}
		h.Store.SetSharedPending(ctx, a.ID, removed)
		return
	}
	h.tellRemoved(ctx, a, p, removed)
}

func dedupe(l []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range l {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// tellRemoved sends a removal to the other app and, if it took it, remembers the photos as gone.
func (h *Hub) tellRemoved(ctx context.Context, a store.SharedAlbum, p store.Peer, checksums []string) error {
	for len(checksums) > 0 {
		n := min(len(checksums), maxRemovedMsg)
		batch := checksums[:n]
		var reply struct{}
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		code, why, err := peer.Call(cctx, h.peerClient(), p.Address, p.TheirID, p.Secret, "POST", "/peer/v1/album/removed", RemovedMsg{To: a.TheirID, Checksums: batch}, &reply)
		cancel()
		if err != nil || code != 200 {
			if err == nil {
				err = errors.New(why)
			}
			h.Log.Debug("a removal could not be told", "err", err)
			return err // the photos stay marked as pending until it can
		}
		h.Store.LedgerGone(ctx, a.ID, batch)
		checksums = checksums[n:]
	}
	h.Store.SetSharedPending(ctx, a.ID, nil)
	return nil
}

// PeerRemoved takes photos out of this person's album because the other person took them out of theirs. It does it only under
// the rule "follow", only for photos the ledger knows (so only photos of this album), and never touches the photo itself.
func (h *Hub) PeerRemoved(ctx context.Context, p store.Peer, m RemovedMsg) error {
	a, err := h.sharedFor(ctx, p, m.To)
	if err != nil {
		return err
	}
	if a.Deletion != store.DeleteFollow {
		return nil
	}
	if len(m.Checksums) > maxRemovedMsg {
		return ErrNoSharedAlbum
	}
	api, err := h.Connect(a.Key)
	if err != nil {
		return err
	}
	var gone []string
	for _, c := range m.Checksums {
		origin, _, known, err := h.Store.LedgerHas(ctx, a.ID, c)
		if err != nil {
			return err
		}
		if !known || origin == store.OriginRemoved || origin == store.OriginBaseline || origin == store.OriginSkipped {
			continue
		}
		rows, err := h.Store.LedgerTracked(ctx, a.ID)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Checksum != c {
				continue
			}
			if id, err := uuid.Parse(r.AssetID); err == nil {
				if err := api.RemoveFromAlbum(ctx, a.AlbumID, id); err != nil && !immich.IsNotFound(err) {
					return err
				}
			}
			gone = append(gone, c)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	h.Store.LedgerGone(ctx, a.ID, gone)
	h.event(ctx, "", a.OwnerID, "share", i18n.PackN("{n} photo was taken out of {album} because {other} took it out of theirs", "{n} photos were taken out of {album} because {other} took them out of theirs", len(gone), "album", a.Name, "other", p.Name))
	return nil
}

// AnswerRemoval is the person's answer to a large removal: tell the other side (follow) or leave their photos where they are.
func (h *Hub) AnswerRemoval(ctx context.Context, owner immich.User, id string, follow bool) error {
	a, err := h.sharedOf(ctx, owner.ID, id)
	if err != nil {
		return err
	}
	if len(a.Pending) == 0 {
		return ErrNoRemoval
	}
	if !follow {
		h.Store.LedgerGone(ctx, a.ID, a.Pending)
		return h.Store.SetSharedPending(ctx, a.ID, nil)
	}
	p, err := h.peerOf(ctx, owner.ID, a.PeerID)
	if err != nil || p.State != store.PeerActive {
		return ErrPeerNotActive
	}
	if err := h.tellRemoved(ctx, a, p, a.Pending); err != nil {
		return ErrPeerUnreachable
	}
	return nil
}
