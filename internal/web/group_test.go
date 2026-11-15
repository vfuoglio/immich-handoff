package web_test

import (
	"context"
	"strings"
	"testing"
)

// Three servers share one album through its creator: Alice's app is paired with each of the other two, which never connect to each
// other.
func threeApps(t *testing.T) (a, b, c *anApp, sb, sc, sa1, sa2 string) {
	t.Helper()
	a = startApp(t, "sess=alice", true, true)
	b = startApp(t, "sess=family", true, true)
	c = startApp(t, "sess=family", true, true)
	pairApps(t, a, b)
	pairApps(t, a, c)
	ctx := context.Background()
	for i, name := range []string{"Ben", "Cara"} {
		p := peersOf(t, a, aliceID)[i]
		if err := a.st.SetPeerTheirID(ctx, p.ID, p.TheirID, name, p.Version); err != nil {
			t.Fatal(err)
		}
	}
	a.fake.addPhoto(aliceID, tripAlbum, "a0.jpg", []byte("alice one"))
	a.fake.addPhoto(aliceID, tripAlbum, "a1.jpg", []byte("alice two"))
	for i, m := range []*anApp{b, c} {
		pa := peersOf(t, a, aliceID)[i]
		if code, body := a.as(t, "POST", "/peers/share", "peer="+pa.ID+"&name=Trip&album="+tripAlbum+"&history=all&deletion=nothing"); code != 204 {
			t.Fatalf("propose to %d: %d %s", i, code, body)
		}
		if code, body := m.as(t, "POST", "/shared/accept", "shared="+sharedOf(t, m, familyID)[0].ID+"&album=new&history=new"); code != 204 {
			t.Fatalf("accept %d: %d %s", i, code, body)
		}
	}
	ra := sharedOf(t, a, aliceID)
	return a, b, c, sharedOf(t, b, familyID)[0].ID, sharedOf(t, c, familyID)[0].ID, ra[0].ID, ra[1].ID
}

func TestThreeServersShareOneAlbumThroughTheCreatorWithNoLoopOrDuplicate(t *testing.T) {
	a, b, c, sb, sc, ab, ac := threeApps(t)
	ctx := context.Background()
	album := func(app *anApp, id string) string {
		x, _ := app.st.SharedAlbumByID(ctx, id)
		return x.AlbumID.String()
	}
	pass := func() {
		for _, f := range []func() error{
			func() error { return a.hub.SyncShared(ctx, ab) }, func() error { return a.hub.SyncShared(ctx, ac) },
			func() error { return b.hub.SyncShared(ctx, sb) }, func() error { return c.hub.SyncShared(ctx, sc) },
			func() error { return a.hub.SyncShared(ctx, ab) }, func() error { return a.hub.SyncShared(ctx, ac) },
		} {
			if err := f(); err != nil {
				t.Fatal(err)
			}
		}
	}
	pass()
	if len(b.fake.inAlbum(album(b, sb))) != 2 || len(c.fake.inAlbum(album(c, sc))) != 2 {
		t.Fatalf("both members have Alice's photos: %d %d", len(b.fake.inAlbum(album(b, sb))), len(c.fake.inAlbum(album(c, sc))))
	}
	// Ben adds a photo: it reaches Cara through Alice, and goes nowhere twice
	b.fake.addPhoto(familyID, album(b, sb), "b0.jpg", []byte("ben one"))
	pass()
	if got := c.fake.inAlbum(album(c, sc)); len(got) != 3 || got[2].name != "b0.jpg" {
		t.Fatalf("Ben's photo reached Cara through Alice: %d", len(got))
	}
	// Ben and Cara add the same photo at the same moment: it is one photo in every library
	same := []byte("the same photo")
	b.fake.addPhoto(familyID, album(b, sb), "same-b.jpg", same)
	c.fake.addPhoto(familyID, album(c, sc), "same-c.jpg", same)
	pass()
	pass()
	for name, app := range map[string]*anApp{"alice": a, "ben": b, "cara": c} {
		owner := map[string]string{"alice": aliceID, "ben": familyID, "cara": familyID}[name]
		for _, sum := range []string{sumOf(same), sumOf([]byte("ben one")), sumOf([]byte("alice one"))} {
			if n := app.fake.countByChecksum(owner, sum); n != 1 {
				t.Errorf("%s has %d copies of a photo", name, n)
			}
		}
	}
	if n := len(a.fake.inAlbum(tripAlbum)); n != 4 {
		t.Errorf("the creator's album holds each photo once: %d", n)
	}
	// nothing moves any more
	ub, uc, ua := b.fake.uploads, c.fake.uploads, a.fake.uploads
	pass()
	if b.fake.uploads != ub || c.fake.uploads != uc || a.fake.uploads != ua {
		t.Error("a quiet pass moves nothing")
	}
	// the members are told each other's names, and the creator sees whom it relays between
	_, pageB := b.as(t, "GET", "/", "")
	_, pageA := a.as(t, "GET", "/", "")
	if !strings.Contains(pageB, "Also in this album, through Alice: Cara") || !strings.Contains(pageA, "You also share this album with Cara") {
		t.Error("names are shown to the members and the creator")
	}
	if strings.Contains(pageB, "Cara's") {
		t.Error("only names")
	}
	// a member leaving ends only their link
	if code, body := c.as(t, "POST", "/shared/leave", "shared="+sc); code != 204 {
		t.Fatalf("leave: %d %s", code, body)
	}
	a.fake.addPhoto(aliceID, tripAlbum, "a2.jpg", []byte("alice three"))
	if err := a.hub.SyncShared(ctx, ac); err == nil {
		t.Error("Cara's link has ended")
	}
	if err := a.hub.SyncShared(ctx, ab); err != nil {
		t.Fatal(err)
	}
	if b.fake.countByChecksum(familyID, sumOf([]byte("alice three"))) != 1 || c.fake.countByChecksum(familyID, sumOf([]byte("alice three"))) != 0 {
		t.Error("Ben still gets photos, Cara no longer does")
	}
	b.hub.SyncShared(ctx, sb)
	a.hub.SyncShared(ctx, ab)
	if x, _ := a.st.SharedAlbumByID(ctx, ab); x.Problem != "" {
		t.Errorf("Ben's link is fine: %q", x.Problem)
	}
	_, pageB = b.as(t, "GET", "/", "")
	if strings.Contains(pageB, "Also in this album, through Alice: Cara") {
		t.Error("Cara is no longer listed once the creator tells it")
	}
}

func TestThePhotosOfAMemberWaitWhileTheCreatorIsOffAndTheGroupHasALimit(t *testing.T) {
	a, b, _, sb, _, _, _ := threeApps(t)
	ctx := context.Background()
	x, _ := b.st.SharedAlbumByID(ctx, sb)
	b.fake.addPhoto(familyID, x.AlbumID.String(), "waiting.jpg", []byte("it waits"))
	a.srv.Close()
	if err := b.hub.SyncShared(ctx, sb); err == nil {
		t.Fatal("the creator is off")
	}
	if y, _ := b.st.SharedAlbumByID(ctx, sb); !strings.Contains(y.Problem, "could not be reached") || y.Sent != 0 {
		t.Errorf("said plainly, nothing lost: %+v", y)
	}
	if _, order, _ := b.st.LedgerUnsent(ctx, sb); len(order) != 1 {
		t.Errorf("the photo is still waiting to be sent: %v", order)
	}

	// the limit: with room for two, a third person is refused
	a2, _, _, _, _, _, _ := threeApps(t)
	a2.hub.GroupMax = 2
	p := peersOf(t, a2, aliceID)[0]
	if code, body := a2.as(t, "POST", "/peers/share", "peer="+p.ID+"&name=Trip&album="+tripAlbum+"&history=new&deletion=nothing"); code != 409 || !strings.Contains(body, "as many people") {
		t.Errorf("a third member is refused: %d %s", code, body)
	}
}
