package hub_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/vault"
)

// followSetup is a hub with a store, over the small Immich of the library tests: bob is the person, ann shares an album.
func followSetup(t *testing.T) (*hub.Hub, *libFake, *immich.API, *store.Store) {
	t.Helper()
	f := newLibFake()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	connect := func(string) (*immich.API, error) { return immich.New(srv.URL, immich.Auth{Cookie: "sess=bob"}, nil) } // the fake knows people by cookie
	sess, _ := connect("")
	v, _ := vault.New("a-test-store-key-0123456789")
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hub.LibrarySettle, hub.LibraryPoll, hub.LibraryWait = time.Millisecond, time.Millisecond, time.Second
	h := &hub.Hub{Store: st, Connect: connect, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), RulesSettle: time.Millisecond}
	return h, f, sess, st
}

// sharedAlbum is an album of ann's, shared with bob, holding the photos named.
func (f *libFake) sharedAlbum(name string, photos ...*libAsset) string {
	id := uuid.NewString()
	al := &libAlbum{owner: "ann", name: name, members: map[string]bool{}, sharedWith: map[string]bool{"bob": true}}
	for _, p := range photos {
		al.members[p.id] = true
		f.visible[p.id] = map[string]bool{"bob": true}
	}
	f.albums[id] = al
	return id
}

func TestAnAlbumSharedWithThePersonCanBeFollowedAndOnlyThatOne(t *testing.T) {
	h, f, sess, st := followSetup(t)
	ctx := context.Background()
	a := f.shared("a.jpg", "one")
	trip := uuid.MustParse(f.sharedAlbum("Trip", a))
	mineOnly := uuid.New()
	f.albums[mineOnly.String()] = &libAlbum{owner: "bob", name: "Mine", members: map[string]bool{}}

	list, err := h.SharedAlbumsFor(ctx, sess)
	if err != nil || len(list) != 1 || list[0].Name != "Trip" || list[0].Owner != "Ann" || list[0].Count != 1 {
		t.Fatalf("the albums shared with bob, and who shared them: %+v %v", list, err)
	}
	if _, err := h.FollowAlbum(ctx, sess, bob, hub.FollowParams{Album: mineOnly, Dest: hub.LibraryDest{Kind: hub.LibraryPlain}}); !errors.Is(err, hub.ErrAlbumNotShared) {
		t.Errorf("an album that is bob's own: %v", err)
	}
	r, err := h.FollowAlbum(ctx, sess, bob, hub.FollowParams{Album: trip, Dest: hub.LibraryDest{Kind: hub.LibraryTag}})
	if err != nil || r.Kind != store.RuleKindLibrary || r.Dest != "tag" || r.Cond.Album.Name != "Trip" || r.Cond.AlbumOwner != "Ann" || r.Service != "" || r.Mode != store.RuleSuggest {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := h.FollowAlbum(ctx, sess, bob, hub.FollowParams{Album: trip, Dest: hub.LibraryDest{Kind: hub.LibraryTag}}); !errors.Is(err, hub.ErrAlreadyFollowed) || !hub.IsUserError(err) {
		t.Errorf("following it twice: %v", err)
	}
	if _, ok, _ := st.LibraryKey(ctx, bobID); !ok {
		t.Error("the key for following albums was made")
	}
	if _, err := h.FollowAlbum(ctx, sess, bob, hub.FollowParams{Album: trip, Dest: hub.LibraryDest{Kind: "elsewhere"}}); !errors.Is(err, hub.ErrBadLibrary) {
		t.Errorf("a destination that does not exist: %v", err)
	}
	if _, err := h.FollowAlbum(ctx, sess, bob, hub.FollowParams{Album: trip, Dest: hub.LibraryDest{Kind: hub.LibraryTag}, History: "sometimes"}); !errors.Is(err, hub.ErrBadFollowHistory) {
		t.Errorf("a history that does not exist: %v", err)
	}
}

func TestWhatIsInTheAlbumWhenItIsFollowedIsRememberedNotOffered_AndWhatIsAddedLaterIsOffered(t *testing.T) {
	h, f, sess, st := followSetup(t)
	ctx := context.Background()
	old := f.shared("old.jpg", "old")
	own := f.own("bob", "own.jpg", "own", time.Hour)
	album := f.sharedAlbum("Trip", old)
	f.albums[album].members[own.id] = true // bob adds one of his own too
	r, err := h.FollowAlbum(ctx, sess, bob, hub.FollowParams{Album: uuid.MustParse(album), Dest: hub.LibraryDest{Kind: hub.LibraryPlain}})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := h.LookRule(ctx, r); err != nil || n != 0 {
		t.Fatalf("nothing new yet: %d %v", n, err)
	}
	fresh := f.shared("fresh.jpg", "fresh")
	f.albums[album].members[fresh.id] = true
	f.visible[fresh.id] = map[string]bool{"bob": true}
	if n, err := h.LookRule(ctx, r); err != nil || n != 1 {
		t.Fatalf("one photo was added: %d %v", n, err)
	}
	waiting, _ := st.RulePhotos(ctx, r.ID, store.RuleSuggested)
	if len(waiting) != 1 || waiting[0].AssetID != fresh.id || waiting[0].Why != "added to the album Trip, shared by Ann" {
		t.Fatalf("the suggestion and why: %+v", waiting)
	}
	if n, _ := h.LookRule(ctx, r); n != 0 {
		t.Errorf("a photo already waiting is not offered again: %d", n)
	}
	if len(f.bobCopies()) != 0 {
		t.Error("nothing is copied until the person says so")
	}
}

func TestFollowingWithEverythingOffersWhatIsAlreadyInTheAlbum(t *testing.T) {
	h, f, sess, st := followSetup(t)
	ctx := context.Background()
	album := f.sharedAlbum("Trip", f.shared("a.jpg", "a"), f.shared("b.jpg", "b"))
	r, err := h.FollowAlbum(ctx, sess, bob, hub.FollowParams{Album: uuid.MustParse(album), Dest: hub.LibraryDest{Kind: hub.LibraryPlain}, History: hub.FollowAll})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := h.LookRule(ctx, r); err != nil || n != 2 {
		t.Fatalf("both photos are offered: %d %v", n, err)
	}
	if w, _ := st.RulePhotos(ctx, r.ID, store.RuleSuggested); len(w) != 2 {
		t.Errorf("waiting: %d", len(w))
	}
}

func TestARuleSaysSoWhenTheAlbumIsNotSharedAnyMore(t *testing.T) {
	h, f, sess, st := followSetup(t)
	ctx := context.Background()
	album := f.sharedAlbum("Trip", f.shared("a.jpg", "a"))
	r, err := h.FollowAlbum(ctx, sess, bob, hub.FollowParams{Album: uuid.MustParse(album), Dest: hub.LibraryDest{Kind: hub.LibraryPlain}})
	if err != nil {
		t.Fatal(err)
	}
	delete(f.albums, album) // ann stops sharing it (Immich answers no such album)
	if _, err := h.LookRule(ctx, r); err == nil {
		t.Fatal("a look at an album that is gone must say so")
	}
	if got, _ := st.RuleByID(ctx, r.ID); got.Problem == "" {
		t.Error("the rule shows what is wrong")
	}
}
