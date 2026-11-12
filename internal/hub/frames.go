package hub

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

var (
	// ErrBadFrameName: the name is empty, too long or has control characters.
	ErrBadFrameName = errors.New("give the frame a name of one to sixty characters")
	// ErrBadFrameEnd: the end date is not a number of days from 1 to 366 (or none).
	ErrBadFrameEnd = errors.New("a frame can end after 1 to 366 days, or never")
	// ErrTooManyFrames: the person already has the most frames there can be.
	ErrTooManyFrames = errors.New("you already have 10 frames; remove one first")
)

// maxFrames is how many frames one person can have; frameRefresh is how long the list of an album is kept before it is read
// again, so a photo taken out of the album disappears from the screen within about this long; frameMax is the most photos
// one frame shows (the newest).
const (
	maxFrames    = 10
	frameRefresh = 2 * time.Minute // the default; FRAME_REFRESH changes it
	frameMax     = 500
)

// FrameRefreshEvery is how often a frame looks at its album again, and how long the app keeps what it read.
func (h *Hub) FrameRefreshEvery() time.Duration {
	if h.FrameRefresh >= 5*time.Second {
		return h.FrameRefresh
	}
	return frameRefresh
}

// FrameParams is what a person chooses when making a frame.
type FrameParams struct {
	Name        string
	AlbumID     uuid.UUID
	EndsInDays  int // 0: never
	HoldSeconds int // how long each photo is shown: 8, 15, 30 or 60 (anything else: 15)
}

// CreateFrame makes a frame of one of the person's own albums: a long link that shows the album's photos on a screen with
// no sign-in. It has a key of its own, made with the person's session, that can only look at pictures and list an album's
// photos; the app asks it for nothing but the photos that are in the album right then.
func (h *Hub) CreateFrame(ctx context.Context, sess *immich.API, owner immich.User, p FrameParams) (store.Frame, error) {
	if !h.SelfService() {
		return store.Frame{}, ErrNotJoinable
	}
	name := strings.TrimSpace(p.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 60 || strings.ContainsAny(name, "\r\n\x00") {
		return store.Frame{}, ErrBadFrameName
	}
	if p.EndsInDays < 0 || p.EndsInDays > 366 {
		return store.Frame{}, ErrBadFrameEnd
	}
	hold := p.HoldSeconds
	if hold != 8 && hold != 15 && hold != 30 && hold != 60 {
		hold = 15
	}
	if n, err := h.Store.CountFrames(ctx, owner.ID); err != nil {
		return store.Frame{}, err
	} else if n >= maxFrames {
		return store.Frame{}, ErrTooManyFrames
	}
	albums, err := sess.OwnedAlbums(ctx)
	if err != nil {
		return store.Frame{}, err
	}
	var album *immich.AlbumInfo
	for i := range albums {
		if uuid.UUID(albums[i].ID) == p.AlbumID {
			album = &albums[i]
		}
	}
	if album == nil {
		return store.Frame{}, ErrNoAlbum
	}
	keyID, secret, err := sess.CreateAPIKey(ctx, keyName("frame "+name), immich.FramePermissions)
	if err != nil {
		return store.Frame{}, err
	}
	undo := func() {
		if err := sess.DeleteAPIKey(context.WithoutCancel(ctx), keyID); err != nil {
			h.Log.Error("could not remove the key made for a failed frame", "err", err)
		}
	}
	api, err := h.Connect(secret)
	if err != nil {
		undo()
		return store.Frame{}, err
	}
	if _, err := api.AlbumAssets(ctx, p.AlbumID); err != nil { // the key can list the album
		undo()
		return store.Frame{}, err
	}
	f := store.Frame{Token: newToken() + newToken(), OwnerID: owner.ID, OwnerName: owner.Name, Name: name, AlbumID: p.AlbumID, AlbumName: album.Name,
		KeyID: keyID, Key: secret, Hold: hold}
	if p.EndsInDays > 0 {
		f.EndsAt = h.now().Add(time.Duration(p.EndsInDays) * 24 * time.Hour)
	}
	f, err = h.Store.AddFrame(ctx, f)
	if err != nil {
		undo()
		return store.Frame{}, err
	}
	h.Log.Info("frame made", "owner", owner.Email, "album", album.Name)
	return f, nil
}

// EndFrame ends a frame and removes it: its link stops working at once and its key is deleted from the person's account.
func (h *Hub) EndFrame(ctx context.Context, sess *immich.API, owner immich.User, id string) error {
	if !h.SelfService() {
		return ErrNotJoinable
	}
	f, err := h.Store.FrameByID(ctx, owner.ID, id)
	if err != nil {
		return ErrNoFrame
	}
	if err := h.Store.DeleteFrame(ctx, f.ID); err != nil {
		return err
	}
	h.frames.forget(f.ID)
	if err := sess.DeleteAPIKey(ctx, f.KeyID); err != nil {
		h.Log.Warn("end a frame: could not delete its key (it may already be gone)", "err", err)
	}
	return nil
}

// ErrNoFrame: the frame is not one of the person's.
var ErrNoFrame = errors.New("that frame is not yours")

// Frames lists a person's frames.
func (h *Hub) Frames(ctx context.Context, owner uuid.UUID) ([]store.Frame, error) {
	if h.Store == nil {
		return nil, nil
	}
	return h.Store.FramesOf(ctx, owner)
}

// FrameFor finds the frame a link shows. One that is unknown, ended or past its end date gives ErrDropGone, always the
// same answer, so a link says nothing about why it does not work.
func (h *Hub) FrameFor(ctx context.Context, token string) (store.Frame, error) {
	if h.Store == nil || len(token) < 32 || len(token) > 128 {
		return store.Frame{}, ErrDropGone
	}
	f, err := h.Store.FrameByToken(ctx, token)
	if err != nil || (!f.EndsAt.IsZero() && !f.EndsAt.After(h.now())) {
		return store.Frame{}, ErrDropGone
	}
	return f, nil
}

// FrameMaintenance ends the frames whose end date has come (their links stop; the owner removes them, which deletes their
// keys: a key cannot be deleted without the owner's session).
func (h *Hub) FrameMaintenance(ctx context.Context) {
	if h.Store == nil {
		return
	}
	if n, err := h.Store.EndFramesDue(ctx, h.now()); err != nil {
		h.Log.Warn("could not end frames", "err", err)
	} else if n > 0 {
		h.Log.Info("frames ended", "n", n)
	}
}

// FrameItem is a photo of a frame: an opaque id, made from the frame's link and the photo, so the page never learns
// an Immich id and an id from one frame means nothing in another.
type FrameItem struct{ ID string }

type frameSnap struct {
	at    time.Time
	items []FrameItem
	by    map[string]uuid.UUID
}

type frameCache struct {
	mu    sync.Mutex
	snaps map[string]*frameSnap
}

func (c *frameCache) forget(id string) {
	c.mu.Lock()
	delete(c.snaps, id)
	c.mu.Unlock()
}

func frameID(token string, asset uuid.UUID) string {
	m := hmac.New(sha256.New, []byte(token))
	m.Write(asset[:])
	return hex.EncodeToString(m.Sum(nil))[:16]
}

// frameSnapshot is the frame's album as it was read in the last two minutes, read again when older. The album is read with
// the frame's own key; only photos (not videos), not archived, not trashed, newest first, at most frameMax.
func (h *Hub) frameSnapshot(ctx context.Context, f store.Frame) (*frameSnap, error) {
	h.frames.mu.Lock()
	defer h.frames.mu.Unlock()
	if s := h.frames.snaps[f.ID]; s != nil && h.now().Sub(s.at) < h.FrameRefreshEvery() {
		return s, nil
	}
	api, err := h.Connect(f.Key)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	list, err := api.AlbumAssets(cctx, f.AlbumID)
	if err != nil {
		return nil, err
	}
	var keep []immich.AssetResponseDto
	for _, a := range list {
		if a.Type == immich.IMAGE && !a.IsTrashed && !a.IsArchived {
			keep = append(keep, a)
		}
	}
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].FileCreatedAt.After(keep[j].FileCreatedAt) })
	if len(keep) > frameMax {
		keep = keep[:frameMax]
	}
	s := &frameSnap{at: h.now(), by: map[string]uuid.UUID{}}
	for _, a := range keep {
		id := frameID(f.Token, a.Id)
		s.items = append(s.items, FrameItem{ID: id})
		s.by[id] = a.Id
	}
	if h.frames.snaps == nil {
		h.frames.snaps = map[string]*frameSnap{}
	}
	h.frames.snaps[f.ID] = s
	return s, nil
}

// FrameFeed lists the frame's photos, newest first: what a frame page asks for now and then.
func (h *Hub) FrameFeed(ctx context.Context, f store.Frame) ([]FrameItem, error) {
	s, err := h.frameSnapshot(ctx, f)
	if err != nil {
		return nil, err
	}
	return s.items, nil
}

// FrameImage fetches the picture of one photo of a frame, large enough for a screen, with the frame's key. Only a photo that is
// in the album right now can be asked for, by its opaque id.
func (h *Hub) FrameImage(ctx context.Context, f store.Frame, id string) ([]byte, string, error) {
	s, err := h.frameSnapshot(ctx, f)
	if err != nil {
		return nil, "", err
	}
	asset, ok := s.by[id]
	if !ok {
		return nil, "", ErrDropGone
	}
	key := "frame/" + f.ID + "/" + asset.String()
	if p, ok := h.wall.get(key); ok {
		return p.data, p.kind, nil
	}
	api, err := h.Connect(f.Key)
	if err != nil {
		return nil, "", err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, kind, err := api.Thumbnail(cctx, asset, true)
	if err != nil {
		if immich.IsNotFound(err) {
			return nil, "", ErrDropGone
		}
		return nil, "", err
	}
	h.wall.put(key, wallPic{data, kind})
	return data, kind, nil
}
