package hub_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	imgjpeg "image/jpeg"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/notify"
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

// realJPEG is a picture that can be decoded (the fake Immich's other previews are only words).
func realJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(40 + x*150/w), uint8(60 + y*150/h), 120, 255})
		}
	}
	var b bytes.Buffer
	imgjpeg.Encode(&b, img, &imgjpeg.Options{Quality: 92})
	return b.Bytes()
}

func realLogo() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 60, 30))
	for y := 0; y < 30; y++ {
		for x := 0; x < 60; x++ {
			img.Set(x, y, color.RGBA{255, 30, 30, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func TestAClientOnlyEverGetsAMarkedPictureAndTheCacheHoldsNothingElse(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album, _ := holiday(t, e, "one.jpg", "two.jpg")
	plain := realJPEG(900, 600)
	e.f.thumbs["one.jpg"], e.f.thumbs["two.jpg"] = plain, plain
	r := makeRoom(t, e, album, func(p *hub.RoomParams) { p.MarkText = "Chiara Bianchi" })
	if r.Mark != "text" || r.MarkText != "Chiara Bianchi" {
		t.Fatalf("%+v", r)
	}
	photos, _ := e.h.RoomPhotos(ctx, r)
	data, kind, err := e.h.RoomImage(ctx, r, photos[0].ID)
	if err != nil || kind != "image/jpeg" {
		t.Fatal(kind, err)
	}
	if bytes.Equal(data, plain) {
		t.Fatal("the plain preview went out")
	}
	in, _ := imgjpeg.Decode(bytes.NewReader(plain))
	out, err := imgjpeg.Decode(bytes.NewReader(data))
	if err != nil || out.Bounds() != in.Bounds() {
		t.Fatalf("a JPEG of the same size: %v", err)
	}
	// asked again it comes from the cache, and it is the marked one
	again, _, _ := e.h.RoomImage(ctx, r, photos[0].ID)
	if !bytes.Equal(again, data) {
		t.Error("the cache gives the marked picture")
	}
	if _, _, err := e.h.RoomImage(ctx, r, "0000000000000000"); err != hub.ErrDropGone {
		t.Errorf("an id that is not in the album: %v", err)
	}
	other := makeRoom(t, e, album, nil)
	if _, _, err := e.h.RoomImage(ctx, other, photos[0].ID); err != hub.ErrDropGone {
		t.Errorf("an id from another room: %v", err)
	}
	if got, _, _ := e.h.RoomImage(ctx, r, photos[1].ID); bytes.Equal(got, plain) {
		t.Error("the second photo is marked too")
	}
}

func TestAPreviewThatCannotBeMarkedIsNotShownAtAll(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album, _ := holiday(t, e, "bad.jpg")
	// the fake's default preview is only words: it cannot be read as a picture
	r := makeRoom(t, e, album, nil)
	photos, _ := e.h.RoomPhotos(ctx, r)
	data, _, err := e.h.RoomImage(ctx, r, photos[0].ID)
	if err != hub.ErrRoomMarking || data != nil {
		t.Fatalf("an unreadable preview is an error and no bytes: %v %q", err, data)
	}
	if string(e.f.thumbs["bad.jpg"]) == "" {
		t.Fatal("setup")
	}
	if data != nil {
		t.Error("nothing went out")
	}
}

func TestTheWayAPictureIsMarkedIsCheckedAndTheLogoIsKept(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album, _ := holiday(t, e, "one.jpg")
	e.f.thumbs["one.jpg"] = realJPEG(900, 600)
	bad := func(mod func(*hub.RoomParams), want error) {
		t.Helper()
		p := hub.RoomParams{Name: "x", AlbumID: uuid.MustParse(album)}
		mod(&p)
		if _, err := e.h.CreateRoom(ctx, e.sess, bob, p); err != want {
			t.Errorf("got %v, want %v", err, want)
		}
	}
	bad(func(p *hub.RoomParams) { p.Mark = "stripes" }, hub.ErrBadRoomMark)
	bad(func(p *hub.RoomParams) { p.Mark = "logo" }, hub.ErrRoomLogo)
	bad(func(p *hub.RoomParams) { p.Mark = "both"; p.Logo = []byte("<html>") }, hub.ErrRoomLogo)
	bad(func(p *hub.RoomParams) { p.Mark = "logo"; p.Logo = make([]byte, 400<<10) }, hub.ErrRoomLogo)
	bad(func(p *hub.RoomParams) { p.MarkText = strings.Repeat("x", 61) }, hub.ErrRoomMarkText)
	// no text: the photographer's name
	r := makeRoom(t, e, album, nil)
	if r.MarkText != "Bob" {
		t.Errorf("the photographer's name by default: %q", r.MarkText)
	}
	both := makeRoom(t, e, album, func(p *hub.RoomParams) { p.Mark = "both"; p.Logo = realLogo() })
	if kind, data, err := e.h.Store.RoomLogo(ctx, both.ID); err != nil || kind != "image/png" || !bytes.Equal(data, realLogo()) {
		t.Fatalf("the logo is kept: %q %v", kind, err)
	}
	photos, _ := e.h.RoomPhotos(ctx, both)
	out, _, err := e.h.RoomImage(ctx, both, photos[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	img, _ := imgjpeg.Decode(bytes.NewReader(out))
	rd, g, b, _ := img.At(800, 540).RGBA() // inside the corner logo, which is red
	if rd>>8 < 150 || g>>8 > 140 || b>>8 > 140 {
		t.Errorf("the logo is in the corner: %d %d %d", rd>>8, g>>8, b>>8)
	}
	logoOnly := makeRoom(t, e, album, func(p *hub.RoomParams) { p.Mark = "logo"; p.Logo = realLogo() })
	if logoOnly.MarkText != "" {
		t.Errorf("a logo-only room has no text: %q", logoOnly.MarkText)
	}
}

func pickIDs(t *testing.T, e *env, r store.Room) []string {
	t.Helper()
	photos, err := e.h.RoomPhotos(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range photos {
		ids = append(ids, p.ID)
	}
	return ids
}

func TestAClientChoosesUpToTheLimitAndDoneLocksTheChoiceAndTellsThePhotographer(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	var told []string
	e.h.Notify = func(m notify.Message) { told = append(told, m.Text) }
	album, _ := holiday(t, e, "a.jpg", "b.jpg", "c.jpg")
	r := makeRoom(t, e, album, func(p *hub.RoomParams) { p.PickLimit = 2 })
	ids := pickIDs(t, e, r)
	v := hub.RoomViewer{Who: "visitor-1", Name: "Chiara"}

	sel, err := e.h.RoomPick(ctx, r, v, ids[0])
	if err != nil || sel.Count != 1 || !sel.Picked[ids[0]] || !sel.CanPick || sel.Limit != 2 {
		t.Fatalf("%+v %v", sel, err)
	}
	if sel, err = e.h.RoomPick(ctx, r, v, ids[1]); err != nil || sel.Count != 2 {
		t.Fatalf("%+v %v", sel, err)
	}
	if _, err := e.h.RoomPick(ctx, r, v, ids[2]); err != hub.ErrRoomLimit {
		t.Errorf("the limit: %v", err)
	}
	if sel, err = e.h.RoomPick(ctx, r, v, ids[1]); err != nil || sel.Count != 1 || sel.Picked[ids[1]] {
		t.Errorf("a second press takes the pick away: %+v %v", sel, err)
	}
	if _, err := e.h.RoomPick(ctx, r, v, "0000000000000000"); err != hub.ErrDropGone {
		t.Errorf("a photo that is not in the room: %v", err)
	}
	if sel, err := e.h.RoomDone(ctx, r, v); err != nil || !sel.Done || sel.Count != 1 {
		t.Fatalf("%+v %v", sel, err)
	}
	if len(told) != 1 || !strings.Contains(told[0], "Rossi wedding") || !strings.Contains(told[0], "Chiara") || !strings.Contains(told[0], "1 photos") {
		t.Fatalf("the photographer is told once: %q", told)
	}
	if _, err := e.h.RoomPick(ctx, r, v, ids[2]); err != hub.ErrRoomLocked {
		t.Errorf("a finished choice cannot change: %v", err)
	}
	if sel, _ = e.h.RoomSelectionOf(ctx, r, v); !sel.Done || sel.CanPick {
		t.Errorf("%+v", sel)
	}
	if _, err := e.h.RoomDone(ctx, r, v); err != nil || len(told) != 1 {
		t.Errorf("a second Done tells nobody again: %v %q", err, told)
	}
}

func TestDoneNeedsAtLeastOnePick(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album, _ := holiday(t, e, "a.jpg")
	r := makeRoom(t, e, album, nil)
	if _, err := e.h.RoomDone(ctx, r, hub.RoomViewer{Who: "v"}); err != hub.ErrRoomNoPicks {
		t.Errorf("%v", err)
	}
}

func TestEveryoneWhoHasTheLinkSharesOneSelectionInASharedRoomAndEachHasTheirOwnInAnotherKind(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album, _ := holiday(t, e, "a.jpg", "b.jpg", "c.jpg")
	shared := makeRoom(t, e, album, func(p *hub.RoomParams) { p.PickMode = store.RoomShared })
	each := makeRoom(t, e, album, func(p *hub.RoomParams) { p.PickMode = store.RoomVisitor })
	ann, luca := hub.RoomViewer{Who: "ann", Name: "Ann"}, hub.RoomViewer{Who: "luca", Name: "Luca"}

	sids := pickIDs(t, e, shared)
	e.h.RoomPick(ctx, shared, ann, sids[0])
	if sel, _ := e.h.RoomSelectionOf(ctx, shared, luca); sel.Count != 1 || !sel.Picked[sids[0]] {
		t.Errorf("in a shared room Luca sees Ann's pick: %+v", sel)
	}
	e.h.RoomPick(ctx, shared, luca, sids[1])
	picks, _ := e.h.Store.Picks(ctx, shared.ID, 1, nil)
	names := map[string]bool{}
	for _, p := range picks {
		names[p.Name] = true
	}
	if len(picks) != 2 || !names["Ann"] || !names["Luca"] {
		t.Errorf("who chose each is kept: %+v", picks)
	}

	eids := pickIDs(t, e, each)
	e.h.RoomPick(ctx, each, ann, eids[0])
	if sel, _ := e.h.RoomSelectionOf(ctx, each, luca); sel.Count != 0 {
		t.Errorf("each visitor has their own: %+v", sel)
	}
	if _, err := e.h.RoomDone(ctx, each, ann); err != nil {
		t.Fatal(err)
	}
	if sel, err := e.h.RoomPick(ctx, each, luca, eids[1]); err != nil || sel.Count != 1 || !sel.CanPick {
		t.Errorf("Ann's Done does not lock Luca: %+v %v", sel, err)
	}
}

func TestInANamedRoomOnlyTheFirstToGiveANameChoosesAndOthersLook(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album, _ := holiday(t, e, "a.jpg", "b.jpg")
	r := makeRoom(t, e, album, func(p *hub.RoomParams) { p.PickMode = store.RoomNamed })
	ids := pickIDs(t, e, r)
	ann, luca := hub.RoomViewer{Who: "ann", Name: " Ann  Rossi "}, hub.RoomViewer{Who: "luca", Name: "Luca"}
	if sel, _ := e.h.RoomSelectionOf(ctx, r, ann); !sel.NeedsName || sel.CanPick {
		t.Fatalf("nobody has given a name: %+v", sel)
	}
	if _, err := e.h.RoomPick(ctx, r, ann, ids[0]); err != hub.ErrRoomName {
		t.Errorf("a pick needs a name first: %v", err)
	}
	if err := e.h.RoomClaim(ctx, r, hub.RoomViewer{Who: "x"}); err != hub.ErrRoomName {
		t.Errorf("a claim needs a name: %v", err)
	}
	if err := e.h.RoomClaim(ctx, r, ann); err != nil {
		t.Fatal(err)
	}
	r, _ = e.h.Store.RoomByIDAny(ctx, r.ID)
	if r.ClaimWho != "ann" || r.ClaimName != "Ann Rossi" {
		t.Fatalf("%+v", r)
	}
	if err := e.h.RoomClaim(ctx, r, luca); err != hub.ErrRoomClaimed {
		t.Errorf("the second to claim: %v", err)
	}
	if err := e.h.RoomClaim(ctx, r, ann); err != nil {
		t.Errorf("the same person claiming again is fine: %v", err)
	}
	if sel, _ := e.h.RoomSelectionOf(ctx, r, luca); sel.CanPick || sel.Chooser != "Ann Rossi" {
		t.Errorf("Luca can look: %+v", sel)
	}
	if _, err := e.h.RoomPick(ctx, r, luca, ids[0]); err != hub.ErrRoomClaimed {
		t.Errorf("%v", err)
	}
	if sel, err := e.h.RoomPick(ctx, r, ann, ids[0]); err != nil || sel.Count != 1 {
		t.Errorf("%+v %v", sel, err)
	}
	if sel, _ := e.h.RoomSelectionOf(ctx, r, luca); sel.Count != 1 {
		t.Errorf("the others see the choice: %+v", sel)
	}
}

func TestThePickSettingsAreChecked(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album, _ := holiday(t, e, "a.jpg")
	for _, c := range []struct {
		mode  string
		limit int
		want  error
	}{{"everyone", 0, hub.ErrBadPickMode}, {"", -1, hub.ErrBadPickLim}, {"", 5001, hub.ErrBadPickLim}} {
		if _, err := e.h.CreateRoom(ctx, e.sess, bob, hub.RoomParams{Name: "x", AlbumID: uuid.MustParse(album), PickMode: c.mode, PickLimit: c.limit}); err != c.want {
			t.Errorf("%v %v: %v", c.mode, c.limit, err)
		}
	}
	if r := makeRoom(t, e, album, nil); r.PickMode != store.RoomShared || r.PickLimit != 0 {
		t.Errorf("shared and no limit by default: %+v", r)
	}
}

func TestWhenAClientFinishesThePhotographerGetsALineInTheirActivityAndCanGiveTheChoiceBack(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album, _ := holiday(t, e, "a.jpg", "b.jpg")
	r := makeRoom(t, e, album, nil)
	ids := pickIDs(t, e, r)
	v := hub.RoomViewer{Who: "v1", Name: "Chiara"}
	e.h.RoomPick(ctx, r, v, ids[0])
	e.h.RoomPick(ctx, r, v, ids[1])
	if _, err := e.h.RoomDone(ctx, r, v); err != nil {
		t.Fatal(err)
	}
	acts, _ := e.h.Activity(ctx, bobID, 10)
	if len(acts) == 0 || acts[0].Kind != "choice" || !strings.Contains(acts[0].Text, "Chiara") {
		t.Fatalf("a line for the photographer: %+v", acts)
	}
	if got, _ := e.h.Activity(ctx, annID, 10); len(got) != 0 {
		t.Errorf("nobody else is told: %+v", got)
	}
	sels, _ := e.h.RoomSelections(ctx, r)
	if len(sels) != 1 || !sels[0].Done || sels[0].Count != 2 || sels[0].Name != "Chiara" {
		t.Fatalf("%+v", sels)
	}
	if err := e.h.ReopenRoomSelection(ctx, ann, r.ID, ""); err != hub.ErrNoRoom {
		t.Errorf("another person: %v", err)
	}
	if err := e.h.ReopenRoomSelection(ctx, bob, r.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.RoomPick(ctx, r, v, ids[0]); err != nil {
		t.Errorf("the client can change it again: %v", err)
	}
}
