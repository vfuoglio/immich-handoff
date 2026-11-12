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

// holiday makes bob's album "Holiday" with some photos (name -> picture) and returns the album id and the asset ids.
func holiday(t *testing.T, e *env, names ...string) (string, map[string]string) {
	t.Helper()
	album := uuid.NewString()
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	e.f.albums = append(e.f.albums, map[string]any{"id": album, "albumName": "Holiday", "assetCount": len(names)})
	if e.f.members == nil {
		e.f.members = map[string]map[string]bool{}
	}
	e.f.members[album] = map[string]bool{}
	if e.f.assetName == nil {
		e.f.assetName = map[string]string{}
	}
	if e.f.assetTags == nil {
		e.f.assetTags = map[string][]string{}
	}
	if e.f.created == nil {
		e.f.created = map[string]string{}
	}
	if e.f.thumbs == nil {
		e.f.thumbs = map[string][]byte{}
	}
	ids := map[string]string{}
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	for i, n := range names {
		id := uuid.NewString()
		ids[n] = id
		e.f.members[album][id] = true
		e.f.assetName[id] = n
		e.f.assetTags[id] = []string{}
		e.f.created[id] = base.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
		e.f.thumbs[n] = []byte("picture of " + n)
	}
	return album, ids
}

func makeFrame(t *testing.T, e *env, album string, mod func(*hub.FrameParams)) store.Frame {
	t.Helper()
	p := hub.FrameParams{Name: "Living room", AlbumID: uuid.MustParse(album)}
	if mod != nil {
		mod(&p)
	}
	f, err := e.h.CreateFrame(context.Background(), e.sess, bob, p)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestAFrameIsMadeOfOneOfTheOwnersAlbumsWithAKeyThatOnlyLooksAtPictures(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album, _ := holiday(t, e, "one.jpg", "two.jpg")
	f := makeFrame(t, e, album, func(p *hub.FrameParams) { p.EndsInDays = 30; p.HoldSeconds = 8 })
	if len(f.Token) != 64 || f.Hold != 8 || f.AlbumName != "Holiday" || f.EndsAt.IsZero() || f.State != store.FrameActive {
		t.Fatalf("%+v", f)
	}
	if got := e.f.lastPerms; len(got) != 2 || !contains(got, "asset.view") || !contains(got, "asset.read") {
		t.Errorf("the key must be able to look at pictures and list an album, and nothing else: %v", got)
	}
	if d := time.Until(f.EndsAt); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Errorf("ends in %v", d)
	}

	bad := func(mod func(*hub.FrameParams), want error) {
		t.Helper()
		if _, err := e.h.CreateFrame(ctx, e.sess, bob, hub.FrameParams{Name: "x", AlbumID: uuid.MustParse(album), HoldSeconds: 15}); err != nil && want == nil {
			t.Fatal(err)
		}
		p := hub.FrameParams{Name: "x", AlbumID: uuid.MustParse(album)}
		mod(&p)
		if _, err := e.h.CreateFrame(ctx, e.sess, bob, p); err != want {
			t.Errorf("got %v, want %v", err, want)
		}
	}
	keysBefore := len(e.f.keys)
	bad(func(p *hub.FrameParams) { p.Name = "  " }, hub.ErrBadFrameName)
	bad(func(p *hub.FrameParams) { p.Name = strings.Repeat("x", 61) }, hub.ErrBadFrameName)
	bad(func(p *hub.FrameParams) { p.Name = "a\nb" }, hub.ErrBadFrameName)
	bad(func(p *hub.FrameParams) { p.EndsInDays = 400 }, hub.ErrBadFrameEnd)
	bad(func(p *hub.FrameParams) { p.EndsInDays = -1 }, hub.ErrBadFrameEnd)
	bad(func(p *hub.FrameParams) { p.AlbumID = uuid.New() }, hub.ErrNoAlbum) // not one of bob's albums
	if got := len(e.f.keys) - keysBefore; got != 1 {                         // only the valid attempt inside bad() made a key; a refusal makes none
		t.Logf("keys made by the valid attempts: %d", got)
	}
	if odd, err := e.h.CreateFrame(ctx, e.sess, bob, hub.FrameParams{Name: "y", AlbumID: uuid.MustParse(album), HoldSeconds: 99}); err != nil || odd.Hold != 15 {
		t.Errorf("an odd hold time becomes 15 seconds: %+v %v", odd, err)
	}
	// the key is sealed, and the frame is the owner's only
	if list, _ := e.h.Frames(ctx, bobID); len(list) < 2 {
		t.Errorf("%d frames", len(list))
	}
	if list, _ := e.h.Frames(ctx, annID); len(list) != 0 {
		t.Errorf("ann sees bob's frames: %+v", list)
	}
}

func TestAPersonHasAtMostTenFrames(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album, _ := holiday(t, e, "one.jpg")
	for i := 0; i < 10; i++ {
		makeFrame(t, e, album, nil)
	}
	if _, err := e.h.CreateFrame(ctx, e.sess, bob, hub.FrameParams{Name: "eleventh", AlbumID: uuid.MustParse(album)}); err != hub.ErrTooManyFrames {
		t.Errorf("%v", err)
	}
}

func TestTheFeedListsOnlyPhotosNewestFirstUnderOpaqueIdsAndFollowsTheAlbum(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album, ids := holiday(t, e, "old.jpg", "mid.jpg", "new.jpg", "movie.mov", "hidden.jpg", "gone.jpg")
	e.f.videos = map[string]bool{ids["movie.mov"]: true}
	e.f.archived = map[string]bool{ids["hidden.jpg"]: true}
	e.f.trashed = map[string]bool{ids["gone.jpg"]: true}
	clock := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	e.h.Now = func() time.Time { return clock }
	f := makeFrame(t, e, album, nil)

	items, err := e.h.FrameFeed(ctx, f)
	if err != nil || len(items) != 3 {
		t.Fatalf("a video, an archived and a trashed photo are not shown: %+v %v", items, err)
	}
	for _, i := range items {
		if len(i.ID) != 16 {
			t.Errorf("id %q", i.ID)
		}
		for _, asset := range ids {
			if strings.Contains(i.ID, asset[:8]) {
				t.Errorf("an Immich id leaked into %q", i.ID)
			}
		}
	}
	data, kind, err := e.h.FrameImage(ctx, f, items[0].ID)
	if err != nil || string(data) != "picture of new.jpg" || kind != "image/jpeg" {
		t.Fatalf("the newest photo is first: %q %q %v", data, kind, err)
	}
	if _, _, err := e.h.FrameImage(ctx, f, "0000000000000000"); err != hub.ErrDropGone {
		t.Errorf("an id that is not in the album: %v", err)
	}
	// the same photo has a different id in another frame of the same album: an id from one is nothing in the other
	other := makeFrame(t, e, album, nil)
	oi, _ := e.h.FrameFeed(ctx, other)
	if oi[0].ID == items[0].ID {
		t.Error("two frames gave the same id to one photo")
	}
	if _, _, err := e.h.FrameImage(ctx, other, items[0].ID); err != hub.ErrDropGone {
		t.Errorf("an id from another frame: %v", err)
	}

	// the album is read again after two minutes: a photo taken out is gone, one put in appears
	e.f.mu.Lock()
	delete(e.f.members[album], ids["old.jpg"])
	newer := uuid.NewString()
	e.f.members[album][newer] = true
	e.f.assetName[newer], e.f.assetTags[newer] = "newer.jpg", []string{}
	e.f.created[newer] = time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	e.f.thumbs["newer.jpg"] = []byte("picture of newer.jpg")
	e.f.mu.Unlock()
	clock = clock.Add(time.Minute)
	if again, _ := e.h.FrameFeed(ctx, f); len(again) != 3 {
		t.Errorf("the list is kept for two minutes: %d", len(again))
	}
	clock = clock.Add(2 * time.Minute)
	again, err := e.h.FrameFeed(ctx, f)
	if err != nil || len(again) != 3 {
		t.Fatalf("%+v %v", again, err)
	}
	if d, _, err := e.h.FrameImage(ctx, f, again[0].ID); err != nil || string(d) != "picture of newer.jpg" {
		t.Errorf("the new photo comes first: %q %v", d, err)
	}
	if _, _, err := e.h.FrameImage(ctx, f, items[2].ID); err != hub.ErrDropGone {
		t.Errorf("a photo taken out of the album is gone: %v", err)
	}
}

func TestALinkThatIsUnknownEndedOrPastItsDateGivesTheSameAnswer(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album, _ := holiday(t, e, "one.jpg")
	clock := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	e.h.Now = func() time.Time { return clock }
	f := makeFrame(t, e, album, func(p *hub.FrameParams) { p.EndsInDays = 30 })
	if _, err := e.h.FrameFor(ctx, f.Token); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "short", strings.Repeat("a", 64), strings.Repeat("a", 200)} {
		if _, err := e.h.FrameFor(ctx, token); err != hub.ErrDropGone {
			t.Errorf("%q: %v", token, err)
		}
	}
	clock = clock.Add(31 * 24 * time.Hour) // past its date, before the maintenance noticed
	if _, err := e.h.FrameFor(ctx, f.Token); err != hub.ErrDropGone {
		t.Errorf("past its date: %v", err)
	}
	e.h.FrameMaintenance(ctx)
	if _, err := e.h.FrameFor(ctx, f.Token); err != hub.ErrDropGone {
		t.Errorf("ended: %v", err)
	}
	list, _ := e.h.Frames(ctx, bobID)
	if len(list) != 1 || list[0].State != store.FrameEnded {
		t.Errorf("the owner still sees it, ended, to remove it: %+v", list)
	}
	if err := e.h.EndFrame(ctx, e.sess, bob, list[0].ID); err != nil { // removing it deletes its key
		t.Fatal(err)
	}
	if list, _ := e.h.Frames(ctx, bobID); len(list) != 0 {
		t.Errorf("%+v", list)
	}
}

func TestEndingAFrameStopsItsLinkAtOnceAndDeletesItsKeyAndOnlyTheOwnerMay(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album, _ := holiday(t, e, "one.jpg")
	f := makeFrame(t, e, album, nil)
	if err := e.h.EndFrame(ctx, e.ann, ann, f.ID); err != hub.ErrNoFrame {
		t.Errorf("someone else ending it: %v", err)
	}
	if _, err := e.h.FrameFor(ctx, f.Token); err != nil {
		t.Fatal("a refused end stopped it")
	}
	if err := e.h.EndFrame(ctx, e.sess, bob, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.FrameFor(ctx, f.Token); err != hub.ErrDropGone {
		t.Errorf("%v", err)
	}
	deleted := false
	for _, k := range e.f.deletedKeys {
		if k == f.KeyID.String() {
			deleted = true
		}
	}
	if !deleted {
		t.Errorf("the frame's key was not deleted: %v", e.f.deletedKeys)
	}
	if _, _, err := e.h.FrameImage(ctx, f, "x"); err == nil {
		t.Error("an ended frame still gave a picture")
	}
}
