package hub

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// ErrNoAlbum is returned when the album to link is not one the person owns.
var ErrNoAlbum = errors.New("that album is not one of your own albums")

// senderLink finds the person's active membership as a sender of the service, if they joined from the page.
func (h *Hub) senderLink(ctx context.Context, user uuid.UUID, service string) (*store.Link, error) {
	links, err := h.Store.Links(ctx)
	if err != nil {
		return nil, err
	}
	for i := range links {
		l := links[i]
		if l.Service == service && l.Role == store.RoleSender && l.ImmichUserID == user && l.State == store.StateActive {
			return &l, nil
		}
	}
	return nil, ErrNotJoined
}

// LinkAlbum makes everything in one of the person's own albums be handed over to the service, now and
// later, without a tag. The person's key gets permission to read albums (album.read) through their own
// session, so nobody has to join again. Linking another album replaces the first.
func (h *Hub) LinkAlbum(ctx context.Context, sess *immich.API, user immich.User, service string, album uuid.UUID) error {
	if !h.SelfService() {
		return ErrNotJoined
	}
	link, err := h.senderLink(ctx, user.ID, service)
	if err != nil {
		return err
	}
	albums, err := sess.OwnedAlbums(ctx)
	if err != nil {
		return fmt.Errorf("could not list your albums: %w", err)
	}
	name := ""
	for _, a := range albums {
		if a.ID == album {
			name = a.Name
		}
	}
	if name == "" {
		return ErrNoAlbum
	}
	if err := sess.AddKeyPermission(ctx, link.KeyID, "album.read"); err != nil {
		return fmt.Errorf("could not allow the app to read your albums: %w", err)
	}
	if err := h.Store.SetLinkAlbum(ctx, link.ID, album, name); err != nil {
		return err
	}
	h.event(ctx, service, user.ID, "album", user.Name+" linked the album "+name+" to "+service+": everything in it is handed over")
	h.Log.Info("album linked", "service", service, "user", user.Email, "album", name)
	return h.Reload(ctx)
}

// UnlinkAlbum stops handing over an album. Photos already handed over stay where they are. The key keeps its
// permission to read albums; the person can delete the key, or leave the service, whenever they like.
func (h *Hub) UnlinkAlbum(ctx context.Context, user immich.User, service string) error {
	if !h.SelfService() {
		return ErrNotJoined
	}
	link, err := h.senderLink(ctx, user.ID, service)
	if err != nil {
		return err
	}
	if link.AlbumID == uuid.Nil {
		return nil
	}
	if err := h.Store.SetLinkAlbum(ctx, link.ID, uuid.Nil, ""); err != nil {
		return err
	}
	h.event(ctx, service, user.ID, "album", user.Name+" stopped handing over the album "+link.AlbumName+" to "+service)
	h.Log.Info("album unlinked", "service", service, "user", user.Email, "album", link.AlbumName)
	return h.Reload(ctx)
}

// AlbumHandedOver implements syncer.Reporter: it adds a line to the person's recent events when a pass handed
// over photos from their linked album.
func (h *Hub) AlbumHandedOver(svc *syncer.Service, snd *syncer.Account, handed, refused, failed int) {
	text := fmt.Sprintf("%d %s from the album %s handed over to %s", handed, plural(handed), snd.WatchAlbumName, svc.Label())
	if refused > 0 {
		text += fmt.Sprintf(", %d refused (the receiver has them in the trash)", refused)
	}
	if failed > 0 {
		text += fmt.Sprintf(", %d failed and will be retried", failed)
	}
	h.event(context.Background(), svc.Label(), snd.UserID, "album", text)
}

func plural(n int) string {
	if n == 1 {
		return "photo"
	}
	return "photos"
}
