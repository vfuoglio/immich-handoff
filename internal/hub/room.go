package hub

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/mark"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// A client room is a link for a client to look at a photographer's album, choose some photos and, when the photographer
// releases them, take the originals. This file is the room itself: making it, listing its photos (the album as it is
// read, like a frame, never kept), and ending it. The pictures a client sees are made in roomimage.go.

var (
	ErrBadRoomName    = errors.New("give the room a name of one to sixty characters")
	ErrBadRoomTitle   = errors.New("the title the client sees can be at most sixty characters")
	ErrBadRoomEnd     = errors.New("a room can end after 1 to 730 days, or never")
	ErrTooManyRooms   = errors.New("you already have 20 rooms; remove one first")
	ErrNoRoom         = errors.New("that room is not yours")
	ErrBadRoomPIN     = errors.New("a PIN is 4 to 40 characters")
	ErrRoomTooBig     = errors.New("that album has more photos than a room can show")
	ErrRoomNotActive  = errors.New("that room has ended")
	ErrBadRoomMark    = errors.New("choose how the pictures are marked: text, a logo, or both")
	ErrRoomMarkText   = errors.New("give the text to put on the pictures (up to 60 characters)")
	ErrRoomLogo       = errors.New("a logo is needed for that, as a JPEG, PNG or WebP of at most 300 KB")
	ErrBadReleaseDays = errors.New("the originals can be downloaded for 1 to 730 days, or as long as the room works")
	ErrRoomMarking    = errors.New("this picture could not be marked, so it is not shown")
)

const (
	maxRooms        = 20
	roomRefresh     = 2 * time.Minute // how long the list of an album is kept before it is read again
	roomMaxPhotos   = 2000            // the most photos one room shows
	roomMinPINChars = 4
)

// RoomParams is what a photographer chooses when making a room.
type RoomParams struct {
	Name, Title string
	AlbumID     uuid.UUID
	PIN         string // empty: none
	EndsInDays  int    // 0: never
	Mark        string // how the pictures are marked: "text", "logo" or "both" (empty: text)
	MarkText    string // the text; empty: the photographer's name
	Logo        []byte // the logo, for "logo" and "both"
	PickMode    string // who chooses: "shared", "visitor" or "named" (empty: shared)
	PickLimit   int    // the most photos a selection may hold; 0: no limit
	AutoRelease bool   // release the originals the moment a client sends a choice
	NotesMode   string // notes on photos: "none", "note" or "replies" (empty: none)
	ReleaseDays int    // how many days the originals can be downloaded after a release (0: as long as the room works; empty form: 30)
}

// CreateRoom makes a room of one of the photographer's own albums. It has a key of its own, made with their session, that can
// only look at pictures and list an album's photos.
func (h *Hub) CreateRoom(ctx context.Context, sess *immich.API, owner immich.User, p RoomParams) (store.Room, error) {
	if !h.SelfService() {
		return store.Room{}, ErrNotJoinable
	}
	name, title := strings.TrimSpace(p.Name), strings.TrimSpace(p.Title)
	if n := utf8.RuneCountInString(name); n < 1 || n > 60 || strings.ContainsAny(name, "\r\n\x00") {
		return store.Room{}, ErrBadRoomName
	}
	if utf8.RuneCountInString(title) > 60 || strings.ContainsAny(title, "\r\n\x00") {
		return store.Room{}, ErrBadRoomTitle
	}
	if p.EndsInDays < 0 || p.EndsInDays > 730 {
		return store.Room{}, ErrBadRoomEnd
	}
	if p.ReleaseDays < 0 || p.ReleaseDays > 730 {
		return store.Room{}, ErrBadReleaseDays
	}
	if p.PIN != "" {
		if n := utf8.RuneCountInString(p.PIN); n < roomMinPINChars || n > 40 {
			return store.Room{}, ErrBadRoomPIN
		}
	}
	markMode, markText, logoKind, err := checkRoomMark(p, owner)
	if err != nil {
		return store.Room{}, err
	}
	pickMode, pickLimit, err := checkRoomPicks(p)
	if err != nil {
		return store.Room{}, err
	}
	notesMode, err := checkNotesMode(p)
	if err != nil {
		return store.Room{}, err
	}
	if n, err := h.Store.CountRooms(ctx, owner.ID); err != nil {
		return store.Room{}, err
	} else if n >= maxRooms {
		return store.Room{}, ErrTooManyRooms
	}
	albums, err := sess.OwnedAlbums(ctx)
	if err != nil {
		return store.Room{}, err
	}
	var album *immich.AlbumInfo
	for i := range albums {
		if uuid.UUID(albums[i].ID) == p.AlbumID {
			album = &albums[i]
		}
	}
	if album == nil {
		return store.Room{}, ErrNoAlbum
	}
	perms := immich.RoomPermissions
	if p.AutoRelease {
		perms = immich.RoomPermissionsAuto
	}
	keyID, secret, err := sess.CreateAPIKey(ctx, keyName("room "+name), perms)
	if err != nil {
		return store.Room{}, err
	}
	undo := func() {
		if err := sess.DeleteAPIKey(context.WithoutCancel(ctx), keyID); err != nil {
			h.Log.Error("could not remove the key made for a failed room", "err", err)
		}
	}
	api, err := h.Connect(secret)
	if err != nil {
		undo()
		return store.Room{}, err
	}
	list, err := api.AlbumAssets(ctx, p.AlbumID) // the key can list the album
	if err != nil {
		undo()
		return store.Room{}, err
	}
	if len(list) > roomMaxPhotos {
		undo()
		return store.Room{}, ErrRoomTooBig
	}
	r := store.Room{Token: newToken() + newToken(), OwnerID: owner.ID, OwnerName: owner.Name, Name: name, Title: title,
		AlbumID: p.AlbumID, AlbumName: album.Name, KeyID: keyID, Key: secret, Mark: markMode, MarkText: markText, PickMode: pickMode, PickLimit: pickLimit, AutoRelease: p.AutoRelease, ReleaseDays: p.ReleaseDays, NotesMode: notesMode}
	if p.PIN != "" {
		if r.Password, err = HashDropPassword(p.PIN); err != nil {
			undo()
			return store.Room{}, err
		}
	}
	if p.EndsInDays > 0 {
		r.EndsAt = h.now().Add(time.Duration(p.EndsInDays) * 24 * time.Hour)
	}
	if r, err = h.Store.AddRoom(ctx, r); err != nil {
		undo()
		return store.Room{}, err
	}
	if logoKind != "" {
		if err := h.Store.SetRoomLogo(ctx, r.ID, logoKind, p.Logo); err != nil {
			h.Store.DeleteRoom(ctx, r.ID)
			undo()
			return store.Room{}, err
		}
	}
	h.Log.Info("room made", "owner", owner.Email, "album", album.Name)
	return r, nil
}

// EndRoom removes a room: its link stops working at once and its key is deleted from the photographer's account.
func (h *Hub) EndRoom(ctx context.Context, sess *immich.API, owner immich.User, id string) error {
	if !h.SelfService() {
		return ErrNotJoinable
	}
	r, err := h.Store.RoomByID(ctx, owner.ID, id)
	if err != nil {
		return ErrNoRoom
	}
	if err := h.Store.DeleteRoom(ctx, r.ID); err != nil {
		return err
	}
	h.rooms.forget(r.ID)
	if err := sess.DeleteAPIKey(ctx, r.KeyID); err != nil {
		h.Log.Warn("end a room: could not delete its key (it may already be gone)", "err", err)
	}
	return nil
}

// PauseRoom stops a room's link from showing anything, or lets it again.
func (h *Hub) PauseRoom(ctx context.Context, owner immich.User, id string, paused bool) error {
	if !h.SelfService() {
		return ErrNotJoinable
	}
	r, err := h.Store.RoomByID(ctx, owner.ID, id)
	if err != nil {
		return ErrNoRoom
	}
	if r.State == store.RoomEnded {
		return ErrRoomNotActive
	}
	state := store.RoomActive
	if paused {
		state = store.RoomPaused
	}
	return h.Store.SetRoomState(ctx, r.ID, state)
}

// Rooms lists a person's rooms.
func (h *Hub) Rooms(ctx context.Context, owner uuid.UUID) ([]store.Room, error) {
	if h.Store == nil {
		return nil, nil
	}
	return h.Store.RoomsOf(ctx, owner)
}

// RoomFor finds the room a link opens. One that is unknown or ended gives ErrDropGone, always the same answer, so a link says
// nothing about why it does not work. A paused room is found: the caller shows nothing of it.
func (h *Hub) RoomFor(ctx context.Context, token string) (store.Room, error) {
	if h.Store == nil || len(token) < 32 || len(token) > 128 {
		return store.Room{}, ErrDropGone
	}
	r, err := h.Store.RoomByToken(ctx, token)
	if err != nil || (!r.EndsAt.IsZero() && !r.EndsAt.After(h.now())) {
		return store.Room{}, ErrDropGone
	}
	return r, nil
}

// RoomMaintenance ends the rooms whose end date has come (their links stop; the photographer removes them, which deletes
// their keys: a key cannot be deleted without the owner's session).
func (h *Hub) RoomMaintenance(ctx context.Context) {
	if h.Store == nil {
		return
	}
	if n, err := h.Store.EndRoomsDue(ctx, h.now()); err != nil {
		h.Log.Warn("could not end rooms", "err", err)
	} else if n > 0 {
		h.Log.Info("rooms ended", "n", n)
	}
}

// RoomPhoto is a photo of a room: an opaque id, made from the room's link and the photo, so a client never learns an Immich
// id and an id from one room means nothing in another. The place in the list is what the client sees.
type RoomPhoto struct {
	ID    string
	Video bool
	Name  string // the file's name, for the photographer only
	Taken time.Time
}

type roomSnap struct {
	at     time.Time
	photos []RoomPhoto
	by     map[string]uuid.UUID
}

type roomCache struct {
	mu    sync.Mutex
	snaps map[string]*roomSnap
}

func (c *roomCache) forget(id string) {
	c.mu.Lock()
	delete(c.snaps, id)
	c.mu.Unlock()
}

func roomPhotoID(token string, asset uuid.UUID) string {
	m := hmac.New(sha256.New, []byte("room:"+token))
	m.Write(asset[:])
	return hex.EncodeToString(m.Sum(nil))[:16]
}

// roomSnapshot is the room's album as it was read in the last two minutes, read again when older, with the room's own key:
// photos and videos, not archived, not trashed, oldest first (the order of a shoot), at most roomMaxPhotos.
func (h *Hub) roomSnapshot(ctx context.Context, r store.Room) (*roomSnap, error) {
	h.rooms.mu.Lock()
	defer h.rooms.mu.Unlock()
	if s := h.rooms.snaps[r.ID]; s != nil && h.now().Sub(s.at) < roomRefresh {
		return s, nil
	}
	api, err := h.Connect(r.Key)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	list, err := api.AlbumAssets(cctx, r.AlbumID)
	if err != nil {
		return nil, err
	}
	var keep []immich.AssetResponseDto
	for _, a := range list {
		if (a.Type == immich.IMAGE || a.Type == immich.VIDEO) && !a.IsTrashed && !a.IsArchived {
			keep = append(keep, a)
		}
	}
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].FileCreatedAt.Before(keep[j].FileCreatedAt) })
	if len(keep) > roomMaxPhotos {
		keep = keep[:roomMaxPhotos]
	}
	s := &roomSnap{at: h.now(), by: map[string]uuid.UUID{}}
	for _, a := range keep {
		id := roomPhotoID(r.Token, a.Id)
		s.photos = append(s.photos, RoomPhoto{ID: id, Video: a.Type == immich.VIDEO, Name: a.OriginalFileName, Taken: a.FileCreatedAt})
		s.by[id] = a.Id
	}
	if h.rooms.snaps == nil {
		h.rooms.snaps = map[string]*roomSnap{}
	}
	h.rooms.snaps[r.ID] = s
	return s, nil
}

// RoomPhotos lists the room's photos in the order of the shoot.
func (h *Hub) RoomPhotos(ctx context.Context, r store.Room) ([]RoomPhoto, error) {
	s, err := h.roomSnapshot(ctx, r)
	if err != nil {
		return nil, err
	}
	return s.photos, nil
}

// checkRoomMark checks how the photographer wants the pictures marked and says what to keep: the mode, the text (their name if
// they gave none) and the logo's content type.
func checkRoomMark(p RoomParams, owner immich.User) (mode, text, logoKind string, err error) {
	mode = p.Mark
	if mode == "" {
		mode = mark.Text
	}
	if mode != mark.Text && mode != mark.Logo && mode != mark.Both {
		return "", "", "", ErrBadRoomMark
	}
	if mode != mark.Logo {
		text = strings.TrimSpace(p.MarkText)
		if text == "" {
			text = strings.TrimSpace(owner.Name)
		}
		if n := utf8.RuneCountInString(text); n < 1 || n > 60 || strings.ContainsAny(text, "\r\n\x00") {
			return "", "", "", ErrRoomMarkText
		}
	}
	if mode != mark.Text {
		if len(p.Logo) == 0 || len(p.Logo) > maxLogo {
			return "", "", "", ErrRoomLogo
		}
		switch ct := http.DetectContentType(p.Logo); ct {
		case "image/jpeg", "image/png", "image/webp":
			logoKind = ct
		default:
			return "", "", "", ErrRoomLogo
		}
	}
	return mode, text, logoKind, nil
}

// markSig says what marks a room's pictures, so a changed mark is not served from the cache of an old one.
func markSig(r store.Room, logo []byte) string {
	m := sha256.New()
	m.Write([]byte(r.Mark + "\x00" + r.MarkText + "\x00"))
	m.Write(logo)
	return hex.EncodeToString(m.Sum(nil))[:12]
}

// RoomImage is the picture of one photo of a room as a client sees it: Immich's preview, fetched with the room's key, marked
// and encoded as a JPEG. It is the only way a picture leaves a room, and a picture that cannot be marked is an error, never
// the plain preview; the cache holds marked pictures only.
func (h *Hub) RoomImage(ctx context.Context, r store.Room, id string) ([]byte, string, error) {
	s, err := h.roomSnapshot(ctx, r)
	if err != nil {
		return nil, "", err
	}
	asset, ok := s.by[id]
	if !ok {
		return nil, "", ErrDropGone
	}
	var logo []byte
	if r.Mark != mark.Text {
		if _, logo, err = h.Store.RoomLogo(ctx, r.ID); err != nil {
			return nil, "", err
		}
	}
	key := "room/" + r.ID + "/" + asset.String() + "/" + markSig(r, logo)
	if p, ok := h.wall.get(key); ok {
		return p.data, p.kind, nil
	}
	api, err := h.Connect(r.Key)
	if err != nil {
		return nil, "", err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	plain, _, err := api.Thumbnail(cctx, asset, true)
	if err != nil {
		if immich.IsNotFound(err) {
			return nil, "", ErrDropGone
		}
		return nil, "", err
	}
	marked, err := mark.Preview(plain, mark.Options{Mode: r.Mark, Text: r.MarkText, Logo: logo})
	if err != nil {
		h.Log.Warn("a room's picture could not be marked, so it is not shown", "room", r.ID, "err", err)
		return nil, "", ErrRoomMarking
	}
	h.wall.put(key, wallPic{marked, "image/jpeg"})
	return marked, "image/jpeg", nil
}
