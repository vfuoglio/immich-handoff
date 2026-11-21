package hub

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/albumtree"
	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
)

// The album tree (#274) shows albums named "Trips / 2024 / Rome" as a tree. It reads the person's albums with their own session and
// needs no store: anyone who is signed in to Immich can use it. See docs/design/album-tree-design.md.

var ErrBadAlbumPath = errors.New("write the folders and the album's own name, up to 100 characters in all")

// AlbumTree is the person's albums as two trees: their own, and the ones shared with them.
type AlbumTree struct {
	Mine, Shared *albumtree.Node
}

// AlbumTreeOf lists the person's own albums and the albums shared with them, with their own session.
func (h *Hub) AlbumTreeOf(ctx context.Context, sess *immich.API) (AlbumTree, error) {
	own, err := sess.OwnedAlbums(ctx)
	if err != nil {
		return AlbumTree{}, err
	}
	shared, err := sess.AlbumsSharedWithMe(ctx)
	if err != nil {
		return AlbumTree{}, err
	}
	var mine, theirs []albumtree.Entry
	for _, a := range own {
		e := albumtree.Entry{ID: uuid.UUID(a.ID).String(), Name: a.Name, Count: a.Count}
		if a.Cover != nil {
			e.Cover = e.ID
		}
		mine = append(mine, e)
	}
	for _, a := range shared {
		e := albumtree.Entry{ID: uuid.UUID(a.ID).String(), Name: a.Name, Count: a.Count, Owner: a.Owner, Shared: true}
		if a.Cover != nil {
			e.Cover = e.ID
		}
		theirs = append(theirs, e)
	}
	return AlbumTree{Mine: albumtree.Build(mine), Shared: albumtree.Build(theirs)}, nil
}

// AlbumCover finds the photo Immich shows for an album the person owns or has been given, by the album's id. The page never takes a
// photo id from the address: it takes an album id and looks the cover up in the person's own list.
func (h *Hub) AlbumCover(ctx context.Context, sess *immich.API, album uuid.UUID) (uuid.UUID, bool, error) {
	own, err := sess.OwnedAlbums(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}
	for _, a := range own {
		if uuid.UUID(a.ID) == album && a.Cover != nil {
			return uuid.UUID(*a.Cover), true, nil
		}
	}
	shared, err := sess.AlbumsSharedWithMe(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}
	for _, a := range shared {
		if uuid.UUID(a.ID) == album && a.Cover != nil {
			return uuid.UUID(*a.Cover), true, nil
		}
	}
	return uuid.Nil, false, nil
}

// MoveAlbum renames one of the person's own albums so that it sits in a folder path ("Trips / 2024"; empty: the top), keeping its
// own title. It returns the new name. An album that is not the person's own is refused, whatever the form says.
func (h *Hub) MoveAlbum(ctx context.Context, sess *immich.API, user immich.User, album uuid.UUID, folder string) (string, error) {
	own, err := sess.OwnedAlbums(ctx)
	if err != nil {
		return "", err
	}
	var found *immich.AlbumInfo
	for i := range own {
		if uuid.UUID(own[i].ID) == album {
			found = &own[i]
		}
	}
	if found == nil {
		return "", ErrNoAlbum
	}
	name, ok := albumtree.Rename(found.Name, folder)
	if !ok {
		return "", ErrBadAlbumPath
	}
	if name == found.Name {
		return name, nil
	}
	if err := sess.RenameAlbum(ctx, found.ID, name); err != nil {
		return "", err
	}
	if h.Store != nil && h.SelfService() {
		h.event(ctx, "", uuid.UUID(user.ID), "album", i18n.Pack("{name} renamed the album {old} to {new}", "name", user.Name, "old", found.Name, "new", name))
	}
	return name, nil
}
