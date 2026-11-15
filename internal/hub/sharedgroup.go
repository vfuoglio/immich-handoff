package hub

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/peer"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Groups through the creator (docs/shared-albums-design.md). More than two servers share one album by every member pairing with
// the person who made it: the creator's app has the album shared with each member, one connection each, and what arrives from one
// member lands in the creator's album, where the pass of every other member's connection finds it as new and sends it on. The
// ledger of each connection is what prevents a loop: a photo that arrived over a connection is never sent back over it, and one a
// member already has is not stored again. Members never connect to each other; they are told the others' names, nothing more.
// If the creator's server is off, the members' passes fail and are tried again, so nothing is lost.

var ErrGroupFull = errors.New("this album is already shared with as many people as it can be; end one of them first")

func (h *Hub) groupMax() int {
	if h.GroupMax > 0 {
		return h.GroupMax
	}
	return 10
}

// sharedWith counts the people an album of this person is shared with (agreed or waiting to be).
func (h *Hub) sharedWith(ctx context.Context, owner, album uuid.UUID) (int, error) {
	list, err := h.Store.SharedAlbumsOf(ctx, owner)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, a := range list {
		if a.AlbumID == album && (a.State == store.ShareActive || a.State == store.ShareProposed) {
			n++
		}
	}
	return n, nil
}

func (h *Hub) checkGroupRoom(ctx context.Context, owner, album uuid.UUID) error {
	if album == uuid.Nil {
		return nil
	}
	if n, err := h.sharedWith(ctx, owner, album); err != nil {
		return err
	} else if n >= h.groupMax() {
		return ErrGroupFull
	}
	return nil
}

// OthersIn names the other people an album of this person is agreed with, besides one connection: what the creator tells a member.
func (h *Hub) OthersIn(ctx context.Context, a store.SharedAlbum) []string {
	list, err := h.Store.SharedAlbumsOf(ctx, a.OwnerID)
	if err != nil {
		return nil
	}
	var names []string
	for _, x := range list {
		if x.ID == a.ID || x.AlbumID != a.AlbumID || x.State != store.ShareActive {
			continue
		}
		if p, err := h.Store.PeerByID(ctx, x.PeerID); err == nil && p.State == store.PeerActive {
			names = append(names, p.Name)
		}
	}
	slices.Sort(names)
	return names
}

// MembersMsg tells a member who else is in the album, by name.
type MembersMsg struct {
	To    string   `json:"to"`
	Names []string `json:"names"`
}

// tellMembers, as the creator, tells the other app who else is in the album when that has changed since it last did.
func (h *Hub) tellMembers(ctx context.Context, a store.SharedAlbum, p store.Peer) {
	names := h.OthersIn(ctx, a)
	if slices.Equal(names, a.GroupSent) {
		return
	}
	var reply struct{}
	code, _, err := peer.Call(ctx, h.peerClient(), p.Address, p.TheirID, p.Secret, "POST", "/peer/v1/album/members", MembersMsg{To: a.TheirID, Names: names}, &reply)
	if err == nil && code == 200 {
		h.Store.SetSharedGroupSent(ctx, a.ID, names)
	}
}

// PeerMembers is what a signed "members" does: the creator says who else is in the album. Only names are kept, cut to a sane size.
func (h *Hub) PeerMembers(ctx context.Context, p store.Peer, m MembersMsg) error {
	a, err := h.sharedFor(ctx, p, m.To)
	if err != nil {
		return err
	}
	var names []string
	for _, n := range m.Names {
		n = strings.TrimSpace(n)
		if k := utf8.RuneCountInString(n); k < 1 || k > 40 || strings.ContainsAny(n, "\r\n\x00") || slices.Contains(names, n) {
			continue
		}
		if names = append(names, n); len(names) == 20 {
			break
		}
	}
	slices.Sort(names)
	if slices.Equal(names, a.GroupNames) {
		return nil
	}
	if err := h.Store.SetSharedGroupNames(ctx, a.ID, names); err != nil {
		return err
	}
	for _, n := range names {
		if !slices.Contains(a.GroupNames, n) {
			h.event(ctx, "", a.OwnerID, "share", i18n.Pack("{name} joined the shared album {album} through {other}", "name", n, "album", a.Name, "other", p.Name))
		}
	}
	return nil
}
