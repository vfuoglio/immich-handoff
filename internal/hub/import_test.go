package hub_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/net/webdav"

	"github.com/vfuoglio/immich-handoff/internal/dest"
	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// importSetup is a hub over the small Immich of the library tests with a destination "drop" the administrator opened for importing, and the
// memory the destination holds (the same one the hub reads).
func importSetup(t *testing.T) (*hub.Hub, *libFake, *store.Store, *dest.Memory, string) {
	t.Helper()
	h, f, _, st := followSetup(t)
	root := "/imports/" + t.Name()
	h.AdminDests = map[string]dest.Spec{
		"drop":    {Name: "drop", Kind: "folder", Settings: map[string]string{"root": root, "import": "true"}},
		"private": {Name: "private", Kind: "folder", Settings: map[string]string{"root": root + "-private"}},
	}
	m := folders[root]
	if m == nil {
		m = &dest.Memory{}
		folders[root] = m
	}
	return h, f, st, m, root
}

func putFile(t *testing.T, m *dest.Memory, name, body string) {
	t.Helper()
	if _, err := m.Put(context.Background(), name, strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
}

func TestAPersonMayFollowTheirOwnNetworkDestinationsAndOnlyTheAdministratorsThatAreOpenedToIt(t *testing.T) {
	h, _, st, _, _ := importSetup(t)
	ctx := context.Background()
	own, _ := st.AddOwnDestination(ctx, store.OwnDestination{OwnerID: bobID, Name: "Mine", Kind: "folder", Settings: map[string]string{"root": "/imports/mine"}})
	other, _ := st.AddOwnDestination(ctx, store.OwnDestination{OwnerID: annID, Name: "Hers", Kind: "folder", Settings: map[string]string{"root": "/imports/hers"}})
	got := h.ImportSources(ctx, bobID)
	if len(got) != 2 || got[0].Name != "drop" || got[1].Name != "Mine" {
		t.Fatalf("the opened administrator's destination and their own: %+v", got)
	}
	sess := mustSess(t, h)
	for name, p := range map[string]hub.ImportParams{
		"closed":          {Source: "admin:private"},
		"unknown":         {Source: "admin:nope"},
		"somebody else's": {Source: "own:" + other.ID},
		"a bad history":   {Source: "admin:drop", History: "later"},
	} {
		if _, err := h.FollowDestination(ctx, sess, bob, p); err == nil || !hub.IsUserError(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := h.FollowDestination(ctx, sess, bob, hub.ImportParams{Source: "admin:private"}); !errors.Is(err, hub.ErrImportNotAllowed) {
		t.Errorf("%v", err)
	}
	if _, err := h.FollowDestination(ctx, sess, bob, hub.ImportParams{Source: "own:" + own.ID, Dest: hub.LibraryDest{Kind: hub.LibraryPlain}}); err != nil {
		t.Errorf("their own: %v", err)
	}
}

func TestANewFileIsOfferedASeenOneIsNotAndNoSidecarRecordOrDeliveryOfThisAppIs(t *testing.T) {
	h, f, st, m, _ := importSetup(t)
	ctx := context.Background()
	putFile(t, m, "old.jpg", "was there before")
	r, err := h.FollowDestination(ctx, mustSess(t, h), bob, hub.ImportParams{Source: "admin:drop", Dest: hub.LibraryDest{Kind: hub.LibraryPlain}})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := h.LookRule(ctx, r); err != nil || n != 0 {
		t.Fatalf("what was there is only remembered: %d %v", n, err)
	}
	putFile(t, m, "trip/new.jpg", "new photo")
	putFile(t, m, "trip/new.jpg.xmp", "<x/>")
	putFile(t, m, "trip/notes.txt", "not a photo")
	putFile(t, m, "delivered/IMG_9.jpg", "a hand-over of this app")
	putFile(t, m, "delivered/IMG_9.jpg.handoff.json", "{}")
	putFile(t, m, "trip/.handoff-tmp-123", "half")
	putFile(t, m, "trip/clip.MOV", "a video")
	got, _ := st.RuleByID(ctx, r.ID)
	if n, err := h.LookRule(ctx, got); err != nil || n != 2 {
		t.Fatalf("the new photo and the video, not the sidecar, the text, the record's file or the temporary one: %d %v", n, err)
	}
	sg, _ := h.SuggestionOf(ctx, bobID, r.ID)
	var names []string
	for _, p := range sg.Photos {
		names = append(names, p.AssetID+"|"+p.Why)
	}
	if len(sg.Photos) != 2 || !strings.Contains(strings.Join(names, ","), "trip/new.jpg|new.jpg (9 B)") {
		t.Errorf("each says its name and size: %v", names)
	}
	if n, _ := h.LookRule(ctx, got); n != 0 {
		t.Errorf("a file already offered is not offered again: %d", n)
	}
	if len(f.uploads) != 0 {
		t.Error("nothing is added before a yes")
	}
}

func TestSayingYesAddsTheFileWithItsSidecarToTheLibraryAndChangesNothingAtTheDestination(t *testing.T) {
	h, f, st, m, _ := importSetup(t)
	ctx := context.Background()
	r, _ := h.FollowDestination(ctx, mustSess(t, h), bob, hub.ImportParams{Source: "admin:drop", Dest: hub.LibraryDest{Kind: hub.LibraryTag}})
	putFile(t, m, "trip/new.jpg", "new photo")
	putFile(t, m, "trip/new.jpg.xmp", "<x/>")
	putFile(t, m, "trip/other.jpg", "another")
	got, _ := st.RuleByID(ctx, r.ID)
	h.LookRule(ctx, got)
	before := m.Refs()
	if err := h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerSend, []string{"trip/new.jpg"}); err != nil {
		t.Fatal(err)
	}
	var up *libUpload
	for i := range f.uploads {
		if f.uploads[i].name == "new.jpg" {
			up = &f.uploads[i]
		}
	}
	if up == nil || up.person != "bob" || !up.sidecar || up.status != "created" {
		t.Fatalf("uploaded with the sidecar, as bob: %+v", f.uploads)
	}
	if len(f.bobCopies()) != 1 || len(f.bobCopies()[0].tags) != 1 || f.bobCopies()[0].tags[0] != "from/drop" {
		t.Errorf("tagged with where it came from: %+v", f.bobCopies())
	}
	if after := m.Refs(); strings.Join(after, "|") != strings.Join(before, "|") {
		t.Errorf("nothing at the destination changed:\n%v\n%v", before, after)
	}
	// the one that was not ticked is left out, and neither is offered again
	if w, _ := st.RulePhotos(ctx, r.ID, store.RuleSuggested); len(w) != 0 {
		t.Errorf("answered: %+v", w)
	}
	if n, _ := h.LookRule(ctx, got); n != 0 {
		t.Errorf("not again: %d", n)
	}
	// a file Immich already has is not added twice
	putFile(t, m, "again/new.jpg", "new photo")
	h.LookRule(ctx, got)
	if err := h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerSend, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.bobCopies()) != 1 {
		t.Errorf("a duplicate is Immich's to refuse: %d copies", len(f.bobCopies()))
	}
}

func TestAfterThreeAlwaysAFollowedDestinationAddsWhatAppearsByItselfAndNeverBeyondItsFolder(t *testing.T) {
	h, f, st, m, _ := importSetup(t)
	ctx := context.Background()
	r, _ := h.FollowDestination(ctx, mustSess(t, h), bob, hub.ImportParams{Source: "admin:drop", Prefix: "/phone/", Dest: hub.LibraryDest{Kind: hub.LibraryPlain}})
	if r.Cond.Prefix != "phone" {
		t.Fatalf("the folder is cleaned: %q", r.Cond.Prefix)
	}
	if err := h.SetRuleAuto(ctx, bob, r.ID, true); !errors.Is(err, hub.ErrAutoTooSoon) {
		t.Fatalf("not before three Always: %v", err)
	}
	putFile(t, m, "elsewhere/not-mine.jpg", "outside the followed folder")
	for i := 0; i < 3; i++ {
		putFile(t, m, "phone/p"+string(rune('a'+i))+".jpg", "photo-"+string(rune('a'+i)))
		got, _ := st.RuleByID(ctx, r.ID)
		h.LookRule(ctx, got)
		if err := h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerAlways, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.bobCopies()) != 3 {
		t.Fatalf("three Always, three photos: %d", len(f.bobCopies()))
	}
	if err := h.SetRuleAuto(ctx, bob, r.ID, true); err != nil {
		t.Fatal(err)
	}
	putFile(t, m, "phone/auto.jpg", "added-after")
	got, _ := st.RuleByID(ctx, r.ID)
	if n, err := h.LookRule(ctx, got); err != nil || n != 1 || len(f.bobCopies()) != 4 {
		t.Fatalf("added without asking: %d %v %d", n, err, len(f.bobCopies()))
	}
	for _, c := range f.bobCopies() {
		if c.name == "not-mine.jpg" {
			t.Error("a file outside the followed folder was added")
		}
	}
}

func TestAFollowedDestinationThatTheAdministratorClosedSaysSoAndAddsNothing(t *testing.T) {
	h, f, st, m, _ := importSetup(t)
	ctx := context.Background()
	r, _ := h.FollowDestination(ctx, mustSess(t, h), bob, hub.ImportParams{Source: "admin:drop", Dest: hub.LibraryDest{Kind: hub.LibraryPlain}})
	putFile(t, m, "a.jpg", "x")
	got, _ := st.RuleByID(ctx, r.ID)
	h.LookRule(ctx, got)
	delete(h.AdminDests["drop"].Settings, "import")
	if _, err := h.LookRule(ctx, got); !errors.Is(err, hub.ErrImportNotAllowed) {
		t.Fatalf("%v", err)
	}
	if again, _ := st.RuleByID(ctx, r.ID); !strings.Contains(again.Problem, "not allowed importing") {
		t.Errorf("the rule says why: %q", again.Problem)
	}
	if err := h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerSend, nil); err == nil {
		t.Error("a yes does not read from a destination that was closed")
	}
	if len(f.uploads) != 0 {
		t.Error("nothing was added")
	}
}

func TestAPictureOfAnOfferedFileIsScaledAndNothingElseIsEverRead(t *testing.T) {
	h, _, st, m, _ := importSetup(t)
	ctx := context.Background()
	r, _ := h.FollowDestination(ctx, mustSess(t, h), bob, hub.ImportParams{Source: "admin:drop", Dest: hub.LibraryDest{Kind: hub.LibraryPlain}})
	img := image.NewRGBA(image.Rect(0, 0, 1200, 800))
	for x := 0; x < 1200; x++ {
		for y := 0; y < 800; y++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	putFile(t, m, "a.png", buf.String())
	putFile(t, m, "clip.mp4", "video")
	putFile(t, m, "secret/never-offered.png", buf.String())
	got, _ := st.RuleByID(ctx, r.ID)
	// only the first two are in what the rule offers: the third is outside the follow's listing once it is declined
	h.LookRule(ctx, got)
	b, err := h.ImportThumbnail(ctx, bobID, r.ID, hub.EncodeImportRef("a.png"))
	if err != nil || len(b) == 0 || len(b) > 100<<10 || !bytes.HasPrefix(b, []byte{0xff, 0xd8}) {
		t.Fatalf("a small JPEG: %d bytes, %v", len(b), err)
	}
	if _, err := h.ImportThumbnail(ctx, bobID, r.ID, hub.EncodeImportRef("clip.mp4")); !errors.Is(err, hub.ErrNoThumbnail) {
		t.Errorf("a video has no picture: %v", err)
	}
	if _, err := h.ImportThumbnail(ctx, bobID, r.ID, hub.EncodeImportRef("never/offered.png")); !errors.Is(err, hub.ErrNoThumbnail) {
		t.Errorf("a file the rule never offered is not read: %v", err)
	}
	if _, err := h.ImportThumbnail(ctx, bobID, r.ID, hub.EncodeImportRef("../etc/passwd")); err == nil {
		t.Error("a name that is not an offered one")
	}
	if _, err := h.ImportThumbnail(ctx, annID, r.ID, hub.EncodeImportRef("a.png")); !errors.Is(err, hub.ErrNoRule) {
		t.Errorf("another person cannot see them: %v", err)
	}
	if _, err := h.ImportThumbnail(ctx, bobID, r.ID, "!!not base64"); err == nil {
		t.Error("bad encoding")
	}
}

func mustSess(t *testing.T, h *hub.Hub) *immich.API {
	t.Helper()
	api, err := h.Connect("")
	if err != nil {
		t.Fatal(err)
	}
	return api
}

// ---- a real network kind: WebDAV against a server in the test ----

type davRig struct {
	srv  *httptest.Server
	down atomic.Bool
	url  string
}

func newDavRig(t *testing.T) *davRig {
	t.Helper()
	h := &webdav.Handler{Prefix: "/dav", FileSystem: webdav.Dir(t.TempDir()), LockSystem: webdav.NewMemLS()}
	rig := &davRig{}
	rig.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rig.down.Load() {
			http.Error(w, "down", 503)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(rig.srv.Close)
	rig.url = rig.srv.URL + "/dav"
	return rig
}

func (r *davRig) put(t *testing.T, name, body string) {
	t.Helper()
	d, err := dest.NewWebDAV(r.url, "", "", dest.Net{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Put(context.Background(), name, strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
}

func TestAWebDAVDestinationIsFollowedOfferedAddedAndAFailingServerLosesNothing(t *testing.T) {
	h, f, st, _, _ := importSetup(t)
	ctx := context.Background()
	rig := newDavRig(t)
	h.DestNet.AllowPrivate = true // the test server is on 127.0.0.1
	h.AdminDests["cloud"] = dest.Spec{Name: "cloud", Kind: "webdav", Settings: map[string]string{"url": rig.url, "import": "true"}}
	rig.put(t, "old/before.jpg", "was there")
	r, err := h.FollowDestination(ctx, mustSess(t, h), bob, hub.ImportParams{Source: "admin:cloud", Dest: hub.LibraryDest{Kind: hub.LibraryPlain}})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := h.LookRule(ctx, r); err != nil || n != 0 {
		t.Fatalf("what was there is not offered: %d %v", n, err)
	}
	rig.put(t, "phone/new.jpg", "a new photo")
	rig.put(t, "phone/new.jpg.xmp", "<x/>")
	rig.put(t, "phone/also.jpg", "another one")
	got, _ := st.RuleByID(ctx, r.ID)
	if n, err := h.LookRule(ctx, got); err != nil || n != 2 {
		t.Fatalf("two new photos, not the sidecar: %d %v", n, err)
	}
	if n, _ := h.LookRule(ctx, got); n != 0 {
		t.Errorf("a seen one is not offered again: %d", n)
	}
	// the server stops answering: the look fails in words, and nothing that was offered is lost or marked
	rig.down.Store(true)
	before, _ := st.RulePhotoStates(ctx, r.ID)
	if _, err := h.LookRule(ctx, got); err == nil {
		t.Fatal("it cannot be listed")
	}
	if again, _ := st.RuleByID(ctx, r.ID); !strings.Contains(again.Problem, "could not be listed") || strings.Contains(again.Problem, rig.srv.Listener.Addr().String()) {
		t.Errorf("the rule says so, without the address: %q", again.Problem)
	}
	if after, _ := st.RulePhotoStates(ctx, r.ID); len(after) != len(before) {
		t.Errorf("nothing is marked on a failure: %v %v", before, after)
	}
	if err := h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerSend, nil); err == nil {
		t.Error("a yes while the server is down fails, and keeps what is offered")
	}
	if w, _ := st.RulePhotos(ctx, r.ID, store.RuleSuggested); len(w) != 2 {
		t.Errorf("both are still waiting: %+v", w)
	}
	// it is back: the yes adds both, the first with its sidecar, and the destination is as it was
	rig.down.Store(false)
	if err := h.AnswerSuggestion(ctx, bob, r.ID, hub.AnswerSend, nil); err != nil {
		t.Fatal(err)
	}
	var withSidecar, without int
	for _, u := range f.uploads {
		if u.name == "new.jpg" && u.sidecar {
			withSidecar++
		}
		if u.name == "also.jpg" && !u.sidecar {
			without++
		}
	}
	if withSidecar != 1 || without != 1 || len(f.bobCopies()) != 2 {
		t.Errorf("added, the first with its sidecar: %+v", f.uploads)
	}
	d, _ := dest.NewWebDAV(rig.url, "", "", dest.Net{AllowPrivate: true})
	if all, _ := d.List(ctx, ""); len(all) != 4 {
		t.Errorf("nothing at the destination changed: %+v", all)
	}
}

func TestADestinationWithTooManyFilesIsFollowedByAFolderInsideIt(t *testing.T) {
	h, _, _, _, _ := importSetup(t)
	ctx := context.Background()
	rig := newDavRig(t)
	h.DestNet.AllowPrivate = true
	h.AdminDests["cloud"] = dest.Spec{Name: "cloud", Kind: "webdav", Settings: map[string]string{"url": rig.url, "import": "true"}}
	for i := 0; i < 4; i++ {
		rig.put(t, "big/f"+string(rune('a'+i))+".jpg", "x")
	}
	rig.put(t, "small/only.jpg", "y")
	dest.ListLimit = 3
	defer func() { dest.ListLimit = 200000 }()
	if _, err := h.FollowDestination(ctx, mustSess(t, h), bob, hub.ImportParams{Source: "admin:cloud", Dest: hub.LibraryDest{Kind: hub.LibraryPlain}}); !errors.Is(err, hub.ErrImportTooBig) || !hub.IsUserError(err) {
		t.Fatalf("all of it is too much: %v", err)
	}
	if _, err := h.FollowDestination(ctx, mustSess(t, h), bob, hub.ImportParams{Source: "admin:cloud", Prefix: "small", Dest: hub.LibraryDest{Kind: hub.LibraryPlain}}); err != nil {
		t.Errorf("a folder inside it is fine: %v", err)
	}
}
