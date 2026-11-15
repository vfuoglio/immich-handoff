package web_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/bridge"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// A second Immich with no app: the other app's fake stands for it, with a key made on it, and only the first app takes part.

func relativeKey(rel *anApp, perms ...string) string {
	rel.fake.mu.Lock()
	defer rel.fake.mu.Unlock()
	rel.fake.keys["rel-key"] = sharedKey{id: "rel-key-id", owner: familyID, perms: perms}
	return "rel-key"
}

func shareWithImmichForm(rel *anApp, key string) string {
	return "person=Cousin&address=" + url.QueryEscape(rel.immich) + "&key=" + key + "&name=Trip&album=" + tripAlbum + "&history=all"
}

func oneApp(t *testing.T, perms ...string) (a, rel *anApp) {
	t.Helper()
	a = startApp(t, "sess=alice", true, true)
	rel = startApp(t, "sess=family", true, true)
	key := relativeKey(rel, perms...)
	a.fake.addPhoto(aliceID, tripAlbum, "one.jpg", []byte("the first photo"))
	a.fake.addPhoto(aliceID, tripAlbum, "two.jpg", []byte("the second photo"))
	if code, body := a.as(t, "POST", "/peers/share-immich", shareWithImmichForm(rel, key)); code != 204 {
		t.Fatalf("share: %d %s", code, body)
	}
	return a, rel
}

func theirAlbumOf(t *testing.T, a *anApp) store.SharedAlbum {
	t.Helper()
	list := sharedOf(t, a, aliceID)
	if len(list) != 1 {
		t.Fatalf("one shared album: %d", len(list))
	}
	return list[0]
}

func TestAnAlbumCanBeSentToSomeoneWithNoHandoffWithASmallKeyAndNothingComesBack(t *testing.T) {
	a, rel := oneApp(t, bridge.SharedReceivePermissions...)
	sa := theirAlbumOf(t, a)
	if sa.TwoWay || sa.TheirAlbum == "" || sa.State != store.ShareActive || sa.Deletion != store.DeleteNothing {
		t.Fatalf("a receive-only share: %+v", sa)
	}
	pr := peersOf(t, a, aliceID)
	if len(pr) != 1 || pr[0].Role != store.PeerImmich || pr[0].Name != "Cousin" || pr[0].Secret != "rel-key" {
		t.Fatalf("the other person is a peer of kind immich: %+v", pr)
	}
	ctx := context.Background()
	if err := a.hub.SyncShared(ctx, sa.ID); err != nil {
		t.Fatal(err)
	}
	got := rel.fake.inAlbum(sa.TheirAlbum)
	if len(got) != 2 || string(got[0].data) != "the first photo" || got[0].name != "one.jpg" {
		t.Fatalf("both photos reached their album, whole: %+v", got)
	}
	// a photo they add is not brought here, and a pass again sends nothing twice
	rel.fake.addPhoto(familyID, sa.TheirAlbum, "theirs.jpg", []byte("a photo of theirs"))
	before := rel.fake.uploads
	a.hub.SyncShared(ctx, sa.ID)
	if rel.fake.uploads != before || len(a.fake.inAlbum(sa.AlbumID.String())) != 2 {
		t.Errorf("nothing moved on the second pass: %d uploads, %d here", rel.fake.uploads-before, len(a.fake.inAlbum(sa.AlbumID.String())))
	}
	// a new photo of Alice's goes
	a.fake.addPhoto(aliceID, sa.AlbumID.String(), "three.jpg", []byte("a third photo"))
	a.hub.SyncShared(ctx, sa.ID)
	if len(rel.fake.inAlbum(sa.TheirAlbum)) != 4 {
		t.Errorf("the new photo went: %d", len(rel.fake.inAlbum(sa.TheirAlbum)))
	}
	_, page := a.as(t, "GET", "/", "")
	for _, want := range []string{"Cousin", "Their key only receives", "Share an album with someone who has no Handoff", "asset.upload"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// ending it forgets the connection too (their key is theirs to delete)
	if code, body := a.as(t, "POST", "/shared/leave", "shared="+sa.ID); code != 204 {
		t.Fatalf("leave: %d %s", code, body)
	}
	if len(peersOf(t, a, aliceID)) != 0 || len(sharedOf(t, a, aliceID)) != 0 {
		t.Error("the share and the connection made for it are gone")
	}
}

func TestAKeyThatCanDoMoreOrLessThanNeededIsRefusedAndNothingIsMade(t *testing.T) {
	for name, perms := range map[string][]string{
		"too broad":      append(append([]string{}, bridge.SharedReceivePermissions...), "asset.delete"),
		"too small":      {"user.read", "asset.upload"},
		"half a reading": append(append([]string{}, bridge.SharedReceivePermissions...), "asset.read"),
	} {
		a := startApp(t, "sess=alice", true, true)
		rel := startApp(t, "sess=family", true, true)
		key := relativeKey(rel, perms...)
		keysBefore := len(a.fake.keys)
		code, body := a.as(t, "POST", "/peers/share-immich", shareWithImmichForm(rel, key))
		if code != 409 {
			t.Errorf("%s: refused, got %d %s", name, code, body)
		}
		if len(peersOf(t, a, aliceID)) != 0 || len(sharedOf(t, a, aliceID)) != 0 || len(a.fake.keys) != keysBefore || len(rel.fake.albums[familyID]) != 0 {
			t.Errorf("%s: nothing was made", name)
		}
	}
}

func TestATwoWayKeyBringsTheirPhotosHereOnceAndNeverEchoes(t *testing.T) {
	perms := append(append([]string{}, bridge.SharedReceivePermissions...), bridge.SharedTwoWayExtra...)
	a, rel := oneApp(t, perms...)
	sa := theirAlbumOf(t, a)
	if !sa.TwoWay {
		t.Fatalf("a key that can read makes a two-way share: %+v", sa)
	}
	rel.fake.addPhoto(familyID, sa.TheirAlbum, "theirs.jpg", []byte("a photo of theirs"))
	ctx := context.Background()
	if err := a.hub.SyncShared(ctx, sa.ID); err != nil {
		t.Fatal(err)
	}
	if got := a.fake.inAlbum(sa.AlbumID.String()); len(got) != 3 || got[2].name != "theirs.jpg" {
		t.Fatalf("their photo is in my album: %d", len(got))
	}
	if got := rel.fake.inAlbum(sa.TheirAlbum); len(got) != 3 {
		t.Fatalf("my two photos are in theirs: %d", len(got))
	}
	x, _ := a.st.SharedAlbumByID(ctx, sa.ID)
	if x.Sent != 2 || x.Received != 1 || x.Problem != "" {
		t.Errorf("counted: %+v", x)
	}
	// nothing goes back and forth afterwards
	ups, ups2 := a.fake.uploads, rel.fake.uploads
	a.hub.SyncShared(ctx, sa.ID)
	if a.fake.uploads != ups || rel.fake.uploads != ups2 || rel.fake.countByChecksum(familyID, sumOf([]byte("a photo of theirs"))) != 1 || a.fake.countByChecksum(aliceID, sumOf([]byte("a photo of theirs"))) != 1 {
		t.Error("no echo, no duplicates")
	}
	// the other person's key stops working: the problem is said, not hidden
	rel.fake.mu.Lock()
	rel.fake.revoked["rel-key"] = true
	rel.fake.mu.Unlock()
	a.fake.addPhoto(aliceID, sa.AlbumID.String(), "four.jpg", []byte("a fourth photo"))
	if err := a.hub.SyncShared(ctx, sa.ID); err == nil {
		t.Error("a pass with a dead key ends with a problem")
	}
	if x, _ := a.st.SharedAlbumByID(ctx, sa.ID); !strings.Contains(x.Problem, "no longer accepts the key") {
		t.Errorf("said plainly: %q", x.Problem)
	}
	a.hub.PeerMaintenance(ctx)
	if pr := peersOf(t, a, aliceID); len(pr) != 1 || pr[0].Problem == "" {
		t.Errorf("the connection shows the key is refused: %+v", pr)
	}
}
