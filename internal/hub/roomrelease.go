package hub

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Releasing a client room's originals. Until the photographer releases them (or the room does it by itself when a client
// sends a choice) the room's key has no permission to download, and the app gives out no original. A client gets the
// originals of their own selection only, and only once it has been sent.

var (
	ErrNothingToRelease = errors.New("no client has sent a choice yet, so there is nothing to release")
	ErrNotReleased      = errors.New("the originals have not been released")
)

const roomZipsPerRoom = 3

// ReleaseRoom lets clients download the originals of the choices they sent. It is done with the photographer's own session,
// which adds the download permission to the room's key now and not before. days is how long they can be downloaded (0: as long
// as the room works).
func (h *Hub) ReleaseRoom(ctx context.Context, sess *immich.API, owner immich.User, roomID string, days int) error {
	r, err := h.Store.RoomByID(ctx, owner.ID, roomID)
	if err != nil {
		return ErrNoRoom
	}
	if r.State == store.RoomEnded {
		return ErrRoomNotActive
	}
	if days < 0 || days > 730 {
		return ErrBadReleaseDays
	}
	sels, err := h.Store.SelectionsOf(ctx, r.ID, r.CurrentRound())
	if err != nil {
		return err
	}
	sent := false
	for _, s := range sels {
		sent = sent || s.Done
	}
	if !sent {
		return ErrNothingToRelease
	}
	if err := sess.AddKeyPermission(ctx, r.KeyID, "asset.download"); err != nil {
		return err
	}
	return h.markReleased(ctx, r, owner.ID, days, owner.Name)
}

// markReleased records the release and tells the photographer's log.
func (h *Hub) markReleased(ctx context.Context, r store.Room, owner uuid.UUID, days int, by string) error {
	now := h.now()
	var until time.Time
	if days > 0 {
		until = now.Add(time.Duration(days) * 24 * time.Hour)
	}
	if err := h.Store.SetRoomRelease(ctx, r.ID, now, until); err != nil {
		return err
	}
	text := i18n.Pack("The originals of {room} were released", "room", r.Name)
	if by != "" {
		text = i18n.Pack("{name} released the originals of {room}", "name", by, "room", r.Name)
	}
	h.event(ctx, "", owner, "released", text)
	return nil
}

// WithdrawRoom takes the release back: the download permission leaves the room's key and clients can download nothing. A
// room that released by itself stops doing so.
func (h *Hub) WithdrawRoom(ctx context.Context, sess *immich.API, owner immich.User, roomID string) error {
	r, err := h.Store.RoomByID(ctx, owner.ID, roomID)
	if err != nil {
		return ErrNoRoom
	}
	if err := sess.RemoveKeyPermission(ctx, r.KeyID, "asset.download"); err != nil {
		return err
	}
	if err := h.Store.SetRoomAutoRelease(ctx, r.ID, false); err != nil {
		return err
	}
	if err := h.Store.SetRoomRelease(ctx, r.ID, time.Time{}, time.Time{}); err != nil {
		return err
	}
	h.event(ctx, "", owner.ID, "released", i18n.Pack("{name} took the release of {room} back", "name", owner.Name, "room", r.Name))
	return nil
}

// RoomReleased reports whether clients may download originals from the room now.
func (h *Hub) RoomReleased(r store.Room) bool {
	if r.ReleasedAt.IsZero() || r.State != store.RoomActive {
		return false
	}
	return r.ReleaseUntil.IsZero() || h.now().Before(r.ReleaseUntil)
}

// releaseIfAuto releases by itself in a room that was made to, when a choice has just been sent.
func (h *Hub) releaseIfAuto(ctx context.Context, r store.Room) {
	if !r.AutoRelease || !r.ReleasedAt.IsZero() {
		return
	}
	if err := h.markReleased(ctx, r, r.OwnerID, r.ReleaseDays, ""); err != nil {
		h.Log.Warn("could not release a room by itself", "room", r.ID, "err", err)
	}
}

// Download is what a client may take from a released room: the photos of their own sent selection.
type Download struct {
	Photos []RoomPhoto
	Until  time.Time
}

// downloadPhotos lists the photos of the selection a viewer may download, or nothing: the room must be released and the
// selection sent, and in a named room the viewer must be the one who chose.
func (h *Hub) downloadPhotos(ctx context.Context, r store.Room, v RoomViewer) ([]RoomPhoto, *roomSnap, error) {
	if !h.RoomReleased(r) {
		return nil, nil, ErrNotReleased
	}
	sel, err := h.roomSelection(ctx, r, v)
	if err != nil {
		return nil, nil, err
	}
	if !sel.Done || (r.PickMode == store.RoomNamed && r.ClaimWho != v.Who) {
		return nil, nil, ErrNotReleased
	}
	s, err := h.roomSnapshot(ctx, r)
	if err != nil {
		return nil, nil, err
	}
	var out []RoomPhoto
	for _, p := range s.photos {
		if sel.Picked[p.ID] {
			out = append(out, p)
		}
	}
	return out, s, nil
}

// RoomDownloads says what a viewer may download now (nothing: ErrNotReleased).
func (h *Hub) RoomDownloads(ctx context.Context, r store.Room, v RoomViewer) (Download, error) {
	photos, _, err := h.downloadPhotos(ctx, r, v)
	if err != nil {
		return Download{}, err
	}
	return Download{Photos: photos, Until: r.ReleaseUntil}, nil
}

// RoomOriginal opens the original of one photo of the viewer's sent selection, for a client of a released room. The caller closes
// the response's body.
func (h *Hub) RoomOriginal(ctx context.Context, r store.Room, v RoomViewer, id string) (*http.Response, error) {
	photos, s, err := h.downloadPhotos(ctx, r, v)
	if err != nil {
		return nil, err
	}
	ok := false
	for _, p := range photos {
		ok = ok || p.ID == id
	}
	if !ok {
		return nil, ErrDropGone
	}
	api, err := h.Connect(r.Key)
	if err != nil {
		return nil, err
	}
	select {
	case h.originals() <- struct{}{}:
	default:
		return nil, ErrDropBusy
	}
	resp, err := api.Original(ctx, s.by[id])
	if err != nil {
		<-h.originals()
		if immich.IsNotFound(err) {
			return nil, ErrDropGone
		}
		return nil, err
	}
	resp.Body = original{resp.Body, func() { <-h.originals() }}
	return resp, nil
}

// RoomZip writes the originals of the viewer's sent selection into a ZIP on w. A viewer has one ZIP at a time and a room at most
// three, so a crowd cannot make the app carry every original at the same moment.
func (h *Hub) RoomZip(ctx context.Context, r store.Room, v RoomViewer, w io.Writer) (int, error) {
	photos, s, err := h.downloadPhotos(ctx, r, v)
	if err != nil {
		return 0, err
	}
	key := r.ID + "|zip|" + v.Who
	if _, busy := h.zipping.LoadOrStore(key, true); busy {
		return 0, ErrZipBusy
	}
	defer h.zipping.Delete(key)
	h.zipMu.Lock()
	if h.zipCount[r.ID] >= roomZipsPerRoom {
		h.zipMu.Unlock()
		return 0, ErrZipBusy
	}
	if h.zipCount == nil {
		h.zipCount = map[string]int{}
	}
	h.zipCount[r.ID]++
	h.zipMu.Unlock()
	defer func() {
		h.zipMu.Lock()
		h.zipCount[r.ID]--
		h.zipMu.Unlock()
	}()
	api, err := h.Connect(r.Key)
	if err != nil {
		return 0, err
	}
	_ = h.Store.AddRoomDownload(ctx, r.ID)
	var ids []string
	for _, p := range photos {
		ids = append(ids, s.by[p.ID].String())
	}
	return writeZip(ctx, w, ids, func(ctx context.Context, asset uuid.UUID) (*http.Response, error) {
		select {
		case h.originals() <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		resp, err := api.Original(ctx, asset)
		if err != nil {
			<-h.originals()
			return nil, err
		}
		resp.Body = original{resp.Body, func() { <-h.originals() }}
		return resp, nil
	})
}

// RoomOf finds one of a person's rooms.
func (h *Hub) RoomOf(ctx context.Context, owner uuid.UUID, id string) (store.Room, error) {
	if h.Store == nil {
		return store.Room{}, ErrNoRoom
	}
	r, err := h.Store.RoomByID(ctx, owner, id)
	if err != nil {
		return store.Room{}, ErrNoRoom
	}
	return r, nil
}

// ReviewSelection is one selection of a room with the photos in it, for the photographer's review.
type ReviewSelection struct {
	store.SelectionSummary
	Photos []RoomPhoto
	Notes  map[string]store.Note // by photo
	Picked map[string]bool       // the photos in the choice (the others only have a note)
}

// RoomReview lists each selection of a room with its photos in the order of the shoot, with the file names, so the photographer
// can find them in their own library.
func (h *Hub) RoomReview(ctx context.Context, r store.Room) ([]ReviewSelection, error) {
	sels, err := h.Store.SelectionsOf(ctx, r.ID, r.CurrentRound())
	if err != nil {
		return nil, err
	}
	s, err := h.roomSnapshot(ctx, r)
	if err != nil {
		return nil, err
	}
	var out []ReviewSelection
	for _, sel := range sels {
		who := sel.Who
		picks, err := h.Store.Picks(ctx, r.ID, r.CurrentRound(), &who)
		if err != nil {
			return nil, err
		}
		picked := map[string]bool{}
		for _, p := range picks {
			picked[p.Photo] = true
		}
		rs := ReviewSelection{SelectionSummary: sel, Notes: map[string]store.Note{}, Picked: picked}
		if ns, err := h.Store.Notes(ctx, r.ID, r.CurrentRound(), &who); err == nil {
			for _, n := range ns {
				rs.Notes[n.Photo] = n
			}
		}
		for _, p := range s.photos {
			if picked[p.ID] || rs.Notes[p.ID].Body != "" {
				rs.Photos = append(rs.Photos, p)
			}
		}
		out = append(out, rs)
	}
	return out, nil
}
