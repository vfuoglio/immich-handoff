package hub_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

func makeRoom(t *testing.T, e *env, album string, mod func(*hub.RoomParams)) store.Room {
	t.Helper()
	p := hub.RoomParams{Name: "Rossi wedding", AlbumID: uuid.MustParse(album)}
	if mod != nil {
		mod(&p)
	}
	r, err := e.h.CreateRoom(context.Background(), e.sess, bob, p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestARoomIsMadeOfOneOfTheOwnersAlbumsWithAKeyThatCannotDownload(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album, _ := holiday(t, e, "one.jpg", "two.jpg")
	r := makeRoom(t, e, album, func(p *hub.RoomParams) { p.Title = "Chiara and Luca"; p.PIN = "1234"; p.EndsInDays = 30 })
	if len(r.Token) != 64 || r.AlbumName != "Holiday" || r.ClientTitle() != "Chiara and Luca" || r.EndsAt.IsZero() || r.State != store.RoomActive || r.Password == "" || r.Password == "1234" {
		t.Fatalf("%+v", r)
	}
	if got := e.f.lastPerms; len(got) != 2 || !contains(got, "asset.view") || !contains(got, "asset.read") || contains(got, "asset.download") {
		t.Errorf("the key looks at pictures and lists an album, and cannot download an original: %v", got)
	}
	if plain := makeRoom(t, e, album, func(p *hub.RoomParams) { p.Name = "Plain" }); plain.ClientTitle() != "Plain" || plain.Password != "" {
		t.Errorf("without a title the name is shown, without a PIN there is none: %+v", plain)
	}

	bad := func(mod func(*hub.RoomParams), want error) {
		t.Helper()
		p := hub.RoomParams{Name: "x", AlbumID: uuid.MustParse(album)}
		mod(&p)
		if _, err := e.h.CreateRoom(ctx, e.sess, bob, p); err != want {
			t.Errorf("got %v, want %v", err, want)
		}
	}
	keys := len(e.f.keys)
	bad(func(p *hub.RoomParams) { p.Name = " " }, hub.ErrBadRoomName)
	bad(func(p *hub.RoomParams) { p.Name = strings.Repeat("x", 61) }, hub.ErrBadRoomName)
	bad(func(p *hub.RoomParams) { p.Title = strings.Repeat("x", 61) }, hub.ErrBadRoomTitle)
	bad(func(p *hub.RoomParams) { p.EndsInDays = 800 }, hub.ErrBadRoomEnd)
	bad(func(p *hub.RoomParams) { p.PIN = "123" }, hub.ErrBadRoomPIN)
	bad(func(p *hub.RoomParams) { p.AlbumID = uuid.New() }, hub.ErrNoAlbum)
	if len(e.f.keys) != keys {
		t.Errorf("a refusal makes no key")
	}
	if list, _ := e.h.Rooms(ctx, bobID); len(list) != 2 {
		t.Errorf("%d rooms", len(list))
	}
	if list, _ := e.h.Rooms(ctx, annID); len(list) != 0 {
		t.Errorf("another person sees bob's rooms: %+v", list)
	}
}

func TestAPersonHasAtMostTwentyRooms(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album, _ := holiday(t, e, "one.jpg")
	for i := 0; i < 20; i++ {
		makeRoom(t, e, album, nil)
	}
	if _, err := e.h.CreateRoom(ctx, e.sess, bob, hub.RoomParams{Name: "21st", AlbumID: uuid.MustParse(album)}); err != hub.ErrTooManyRooms {
		t.Errorf("%v", err)
	}
}

func TestARoomListsPhotosAndVideosInTheOrderOfTheShootUnderOpaqueIdsAndFollowsTheAlbum(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album, ids := holiday(t, e, "first.jpg", "second.jpg", "movie.mov", "hidden.jpg", "gone.jpg")
	e.f.videos = map[string]bool{ids["movie.mov"]: true}
	e.f.archived = map[string]bool{ids["hidden.jpg"]: true}
	e.f.trashed = map[string]bool{ids["gone.jpg"]: true}
	clock := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	e.h.Now = func() time.Time { return clock }
	r := makeRoom(t, e, album, nil)
	photos, err := e.h.RoomPhotos(ctx, r)
	if err != nil || len(photos) != 3 {
		t.Fatalf("an archived and a trashed photo are not shown, a video is: %+v %v", photos, err)
	}
	if photos[0].Name != "first.jpg" || !photos[2].Video || photos[0].Video {
		t.Errorf("the shoot's order, videos marked: %+v", photos)
	}
	for _, p := range photos {
		if len(p.ID) != 16 {
			t.Errorf("id %q", p.ID)
		}
		for _, asset := range ids {
			if strings.Contains(p.ID, asset[:8]) {
				t.Errorf("an Immich id leaked into %q", p.ID)
			}
		}
	}
	other := makeRoom(t, e, album, nil)
	if op, _ := e.h.RoomPhotos(ctx, other); op[0].ID == photos[0].ID {
		t.Error("two rooms gave the same id to one photo")
	}
	// a photo put into the album appears after two minutes, not before
	e.f.mu.Lock()
	extra := uuid.NewString()
	e.f.members[album][extra] = true
	e.f.assetName[extra], e.f.assetTags[extra] = "extra.jpg", []string{}
	e.f.created[extra] = time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	e.f.mu.Unlock()
	clock = clock.Add(time.Minute)
	if again, _ := e.h.RoomPhotos(ctx, r); len(again) != 3 {
		t.Errorf("the list is kept for two minutes: %d", len(again))
	}
	clock = clock.Add(2 * time.Minute)
	if again, _ := e.h.RoomPhotos(ctx, r); len(again) != 4 {
		t.Errorf("a new photo appears: %d", len(again))
	}
}

func TestARoomLinkOnlyOpensForALiveRoomAndEndingItDeletesTheKey(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album, _ := holiday(t, e, "one.jpg")
	clock := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	e.h.Now = func() time.Time { return clock }
	r := makeRoom(t, e, album, func(p *hub.RoomParams) { p.EndsInDays = 14 })
	if got, err := e.h.RoomFor(ctx, r.Token); err != nil || got.ID != r.ID {
		t.Fatal(err)
	}
	for _, token := range []string{"", "short", strings.Repeat("a", 64), strings.Repeat("a", 200)} {
		if _, err := e.h.RoomFor(ctx, token); err != hub.ErrDropGone {
			t.Errorf("%q: %v", token, err)
		}
	}
	if err := e.h.PauseRoom(ctx, bob, r.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.h.RoomFor(ctx, r.Token); got.State != store.RoomPaused {
		t.Errorf("a paused room says so, and the page shows nothing: %q", got.State)
	}
	if err := e.h.PauseRoom(ctx, bob, r.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := e.h.PauseRoom(ctx, ann, r.ID, true); err != hub.ErrNoRoom {
		t.Errorf("another person pauses it: %v", err)
	}
	clock = clock.Add(15 * 24 * time.Hour)
	if _, err := e.h.RoomFor(ctx, r.Token); err != hub.ErrDropGone {
		t.Errorf("past its end date: %v", err)
	}
	e.h.RoomMaintenance(ctx)
	if err := e.h.PauseRoom(ctx, bob, r.ID, true); err != nil {
		t.Logf("a room that ended: %v", err) // it is found by its owner until removed
	}
	if err := e.h.EndRoom(ctx, e.ann, ann, r.ID); err != hub.ErrNoRoom {
		t.Errorf("another person ends it: %v", err)
	}
	if err := e.h.EndRoom(ctx, e.sess, bob, r.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.f.deletedKeys) == 0 {
		t.Error("the room's key is deleted from the owner's account")
	}
	if list, _ := e.h.Rooms(ctx, bobID); len(list) != 0 {
		t.Errorf("%+v", list)
	}
}
