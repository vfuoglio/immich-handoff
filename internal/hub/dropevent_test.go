package hub_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/notify"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

func TestAnEventKeepsWhatGuestsLeaveForTheDaysTheOwnerChose(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	end := time.Now().Add(48 * time.Hour)
	b := newBox(t, e, hub.DropParams{Name: "Wedding", Closes: end, KeepDays: 30})
	if b.KeepDays != 30 {
		t.Fatalf("keep days: %d", b.KeepDays)
	}
	if got := b.KeepUntil(); got.Sub(end.AddDate(0, 0, 30)).Abs() > time.Second {
		t.Errorf("kept until %v, want 30 days after %v", got, end)
	}
	if s := hub.SettingsOf(b); s.KeepDays != 30 || s.Params("Copy").KeepDays != 30 {
		t.Errorf("a template keeps the retention: %+v", s)
	}
	// a box that never ends, or that keeps nothing past its end, has no date
	if until := newBox(t, e, hub.DropParams{Name: "Open ended", KeepDays: 30}).KeepUntil(); !until.IsZero() {
		t.Errorf("a box with no end has no date: %v", until)
	}
	if until := newBox(t, e, hub.DropParams{Name: "Until revoked", Closes: end}).KeepUntil(); !until.IsZero() {
		t.Errorf("0 keeps it until the box is revoked: %v", until)
	}
	for _, days := range []int{-1, 3651} {
		if _, err := e.h.CreateDropBox(ctx, e.sess, bob, hub.DropParams{Name: "Bad", KeepDays: days}); !errors.Is(err, hub.ErrBadDropLimit) {
			t.Errorf("%d days: %v", days, err)
		}
	}
}

func TestOnlyTheOwnerChangesWhatIsKeptAfterTheEvent(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Party", Days: 3, KeepDays: 7})
	if err := e.h.AddDropHost(ctx, e.sess, bob, b.ID, "ann@x.test", store.HostManage); err != nil {
		t.Fatal(err)
	}
	ann := immich.User{ID: annID, Name: "Ann", Email: "ann@x.test"}
	if err := e.h.SetDropEvent(ctx, e.sess, ann, b.ID, hub.EventSettings{KeepDays: 365}); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a co-host who manages must not decide what is kept of other people: %v", err)
	}
	if err := e.h.SetDropEvent(ctx, e.sess, immich.User{ID: uuid.New()}, b.ID, hub.EventSettings{KeepDays: 365}); !errors.Is(err, hub.ErrNoDropBox) {
		t.Errorf("a stranger: %v", err)
	}
	if err := e.h.SetDropEvent(ctx, e.sess, bob, b.ID, hub.EventSettings{KeepDays: -2}); !errors.Is(err, hub.ErrBadDropLimit) {
		t.Errorf("a negative number: %v", err)
	}
	if err := e.h.SetDropEvent(ctx, e.sess, bob, b.ID, hub.EventSettings{KeepDays: 90}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.st.DropBox(ctx, b.ID); got.KeepDays != 90 {
		t.Errorf("keep days after the change: %d", got.KeepDays)
	}
	if ev, _ := e.h.DropEvents(ctx, bob.ID, b.ID, 5); len(ev) == 0 || ev[0].Text == "" {
		t.Errorf("the change is in the log: %+v", ev)
	}
}

func TestGuestNamesAreCleanedBeforeTheyAreShown(t *testing.T) {
	for in, want := range map[string]string{
		"  Ann   Lee ":           "Ann Lee",
		"<b>Eve</b>":             "bEve/b", // the angle brackets are dropped, the letters stay
		"Zoë\x00\n\tRossi":       "Zoë Rossi",
		"":                       "",
		"​​":                     "",
		strings.Repeat("é", 60):  strings.Repeat("é", 40),
		"Mary \"the cat\" Jones": "Mary the cat Jones",
	} {
		if got := hub.CleanGuestName(in); got != want {
			t.Errorf("CleanGuestName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAGalleryShowsOnlyWhatTheBoxKeptAndOnlyWhileItIsKept(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	e.h.StageDir = t.TempDir()
	e.f.thumbs = map[string][]byte{"": jpeg}
	b := newBox(t, e, hub.DropParams{Name: "Party", Closes: time.Now().Add(24 * time.Hour), KeepDays: 7, Gallery: true, GalleryNames: true, Approve: true})
	if !has(e.f.lastPerms, "asset.view") || has(e.f.lastPerms, "asset.download") {
		t.Errorf("a gallery's key may look at pictures, and download only if the owner allows it: %v", e.f.lastPerms)
	}
	plain := newBox(t, e, hub.DropParams{Name: "Plain"})
	if has(e.f.lastPerms, "asset.view") {
		t.Errorf("a box with no gallery has a key that cannot view: %v", e.f.lastPerms)
	}
	if _, err := e.h.GalleryBox(ctx, plain.Token); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("a box with no gallery shows none: %v", err)
	}

	// what waits for approval is not in it; what is approved is, with the name the guest gave
	secret := hub.NewGuestSecret()
	for _, n := range []string{"a.jpg", "b.jpg", "c.jpg"} {
		if out, err := e.h.ReceiveDropFileWith(ctx, b.ID, n, bytes.NewReader(jpeg), "g", hub.DropMeta{Secret: secret, Name: " Ann  <Lee> "}); err != nil || out != hub.DropHeld {
			t.Fatalf("%s: %v %v", n, out, err)
		}
	}
	gb, err := e.h.GalleryBox(ctx, b.Token)
	if err != nil {
		t.Fatal(err)
	}
	if items, _ := e.h.GalleryFeed(ctx, gb, secret, 0, 0, 0); len(items) != 0 {
		t.Fatalf("nothing is kept yet: %+v", items)
	}
	pending, _, _ := e.h.DropPending(ctx, bobID, b.ID, 10)
	if done, _, err := e.h.DecideDrop(ctx, bob, b.ID, []string{pending[0].ID, pending[1].ID}, true); err != nil || done != 2 {
		t.Fatalf("approve: %d %v", done, err)
	}
	items, err := e.h.GalleryFeed(ctx, gb, secret, 0, 0, 0)
	if err != nil || len(items) != 2 || items[0].ID <= items[1].ID || items[0].Name != "Ann Lee" {
		t.Fatalf("what was kept, newest first, with the name: %+v %v", items, err)
	}
	// the name is shown only when the box shows names
	e.st.SetDropGallery(ctx, b.ID, true, false, false)
	gb, _ = e.h.GalleryBox(ctx, b.Token)
	if items, _ := e.h.GalleryFeed(ctx, gb, secret, 0, 0, 0); items[0].Name != "" {
		t.Errorf("a box that shows no names shows none: %+v", items[0])
	}

	// hearts: one a guest, taken back by the same tap, counted for everyone, and only mine are mine
	if _, _, err := e.h.GalleryHeart(ctx, gb, "", items[0].ID); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("no secret, no heart: %v", err)
	}
	if h, n, err := e.h.GalleryHeart(ctx, gb, secret, items[0].ID); err != nil || !h || n != 1 {
		t.Fatalf("heart: %v %d %v", h, n, err)
	}
	other := hub.NewGuestSecret()
	e.h.GalleryHeart(ctx, gb, other, items[0].ID)
	mine, _ := e.h.GalleryFeed(ctx, gb, secret, 0, 0, 0)
	stranger, _ := e.h.GalleryFeed(ctx, gb, hub.NewGuestSecret(), 0, 0, 0)
	if mine[0].Hearts != 2 || !mine[0].Mine || mine[1].Mine || stranger[0].Mine || stranger[0].Hearts != 2 {
		t.Errorf("hearts: mine %+v stranger %+v", mine, stranger)
	}
	if h, n, _ := e.h.GalleryHeart(ctx, gb, secret, items[0].ID); h || n != 1 {
		t.Errorf("taking it back: %v %d", h, n)
	}
	if _, _, err := e.h.GalleryHeart(ctx, gb, secret, 99999); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("a photo that is not there: %v", err)
	}
	// what came after the newest the page has
	if newer, _ := e.h.GalleryFeed(ctx, gb, secret, 0, items[0].ID, 0); len(newer) != 0 {
		t.Errorf("nothing newer: %+v", newer)
	}
	if older, _ := e.h.GalleryFeed(ctx, gb, secret, items[0].ID, 0, 0); len(older) != 1 || older[0].ID != items[1].ID {
		t.Errorf("older: %+v", older)
	}
	// the picture comes with the box's key; a photo of another box does not
	if data, kind, err := e.h.GalleryImage(ctx, gb, items[0].ID); err != nil || kind != "image/jpeg" || len(data) != len(jpeg) {
		t.Errorf("picture: %s %d %v", kind, len(data), err)
	}
	if _, _, err := e.h.GalleryImage(ctx, gb, 99999); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("a photo that is not in the box: %v", err)
	}

	// paused, it shows nothing; after the end it shows until the retention is over, then nothing
	e.st.SetDropBoxState(ctx, b.ID, store.DropPaused, "")
	if _, err := e.h.GalleryBox(ctx, b.Token); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("paused: %v", err)
	}
	e.st.SetDropBoxState(ctx, b.ID, store.DropActive, "")
	e.st.SetDropSchedule(ctx, b.ID, time.Time{}, time.Now().Add(-24*time.Hour))
	if _, err := e.h.GalleryBox(ctx, b.Token); err != nil {
		t.Errorf("a day after the end, inside the retention: %v", err)
	}
	e.st.SetDropSchedule(ctx, b.ID, time.Time{}, time.Now().Add(-8*24*time.Hour))
	if _, err := e.h.GalleryBox(ctx, b.Token); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("past the retention: %v", err)
	}
	e.st.SetDropKeep(ctx, b.ID, 0)
	if _, err := e.h.GalleryBox(ctx, b.Token); err != nil {
		t.Errorf("0 keeps it until the box is revoked: %v", err)
	}
	e.st.SetDropBoxState(ctx, b.ID, store.DropRevoked, "")
	if _, err := e.h.GalleryBox(ctx, b.Token); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("revoked: %v", err)
	}
}

func TestOnlyTheOwnerOpensTheGalleryAndTheKeyGetsExactlyWhatItNeeds(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Open house", Days: 3})
	perms := func() []string { return e.f.keyPerms[b.KeyID.String()] }
	if err := e.h.AddDropHost(ctx, e.sess, bob, b.ID, "ann@x.test", store.HostManage); err != nil {
		t.Fatal(err)
	}
	if err := e.h.SetDropEvent(ctx, e.sess, ann, b.ID, hub.EventSettings{Gallery: true}); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a co-host opening the gallery: %v", err)
	}
	if err := e.h.SetDropEvent(ctx, e.sess, bob, b.ID, hub.EventSettings{Gallery: true, Download: true, Names: true}); err != nil {
		t.Fatal(err)
	}
	if !has(perms(), "asset.view") || !has(perms(), "asset.download") {
		t.Errorf("the key can look and download: %v", perms())
	}
	got, _ := e.st.DropBox(ctx, b.ID)
	if !got.Gallery || !got.GalleryDownload || !got.GalleryNames {
		t.Errorf("settings: %+v", got)
	}
	// download off: the key loses it, keeps the right to look
	if err := e.h.SetDropEvent(ctx, e.sess, bob, b.ID, hub.EventSettings{Gallery: true}); err != nil {
		t.Fatal(err)
	}
	if !has(perms(), "asset.view") || has(perms(), "asset.download") {
		t.Errorf("download off: %v", perms())
	}
	// a wall needs the right to look too, so switching the gallery off leaves it while a wall runs
	if _, err := e.h.StartWall(ctx, e.sess, bob, b.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := e.h.SetDropEvent(ctx, e.sess, bob, b.ID, hub.EventSettings{}); err != nil {
		t.Fatal(err)
	}
	if !has(perms(), "asset.view") {
		t.Errorf("the wall still needs to look: %v", perms())
	}
	// and stopping the wall with the gallery on leaves it as well
	e.h.SetDropEvent(ctx, e.sess, bob, b.ID, hub.EventSettings{Gallery: true})
	if err := e.h.StopWall(ctx, e.sess, bob, b.ID); err != nil {
		t.Fatal(err)
	}
	if !has(perms(), "asset.view") {
		t.Errorf("the gallery still needs to look: %v", perms())
	}
	e.h.SetDropEvent(ctx, e.sess, bob, b.ID, hub.EventSettings{})
	if has(perms(), "asset.view") || has(perms(), "asset.download") {
		t.Errorf("nothing needs it any more: %v", perms())
	}
}

func TestAnOriginalIsDownloadedOnlyWhereTheBoxAllowsIt(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	e.f.thumbs = map[string][]byte{"": jpeg}
	b := newBox(t, e, hub.DropParams{Name: "Save", Gallery: true, GalleryDownload: true})
	closed := newBox(t, e, hub.DropParams{Name: "Look only", Gallery: true})
	for _, box := range []store.DropBox{b, closed} {
		if _, err := e.h.ReceiveDropFile(ctx, box.ID, "IMG_1.jpg", bytes.NewReader(jpeg), "g"); err != nil {
			t.Fatal(err)
		}
	}
	gb, _ := e.h.GalleryBox(ctx, b.Token)
	items, _ := e.h.GalleryFeed(ctx, gb, "", 0, 0, 0)
	resp, err := e.h.GalleryOriginal(ctx, gb, items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(data) != len(jpeg) || !strings.Contains(resp.Header.Get("Content-Disposition"), "IMG_1.jpg") {
		t.Errorf("original: %d bytes, %q", len(data), resp.Header.Get("Content-Disposition"))
	}
	lb, _ := e.h.GalleryBox(ctx, closed.Token)
	litems, _ := e.h.GalleryFeed(ctx, lb, "", 0, 0, 0)
	if _, err := e.h.GalleryOriginal(ctx, lb, litems[0].ID); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("a box that does not allow downloads: %v", err)
	}
	if _, err := e.h.GalleryOriginal(ctx, gb, litems[0].ID); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("another box's photo: %v", err)
	}
}

func TestAModeratorLinkLetsAHelperWithNoAccountDecideOnWhatWaits(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	e.h.StageDir = t.TempDir()
	b := newBox(t, e, hub.DropParams{Name: "Wedding", Approve: true, Days: 3})
	other := newBox(t, e, hub.DropParams{Name: "Other", Approve: true, Days: 3})
	if _, err := e.h.ModBox(ctx, strings.Repeat("a", 64)); !errors.Is(err, hub.ErrDropGone) {
		t.Fatalf("no link yet: %v", err)
	}

	// only the owner makes it
	e.h.AddDropHost(ctx, e.sess, bob, b.ID, "ann@x.test", store.HostManage)
	if _, err := e.h.SetDropModerator(ctx, ann, b.ID, true); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a co-host making the link: %v", err)
	}
	token, err := e.h.SetDropModerator(ctx, bob, b.ID, true)
	if err != nil || len(token) < 64 {
		t.Fatalf("make: %q %v", token, err)
	}
	mb, err := e.h.ModBox(ctx, token)
	if err != nil || mb.ID != b.ID || mb.Key != "" {
		t.Fatalf("the moderator's box, never with its key: %+v %v", mb, err)
	}
	if _, err := e.h.ModBox(ctx, b.Token); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("the guests' link is not a moderator link: %v", err)
	}

	for _, n := range []string{"a.jpg", "b.jpg", "c.jpg"} {
		if _, err := e.h.ReceiveDropFile(ctx, b.ID, n, bytes.NewReader(jpeg), "g"); err != nil {
			t.Fatal(err)
		}
	}
	e.h.ReceiveDropFile(ctx, other.ID, "o.jpg", bytes.NewReader(jpeg), "g")
	list, total, err := e.h.ModPending(ctx, mb, 10)
	if err != nil || total != 3 || len(list) != 3 {
		t.Fatalf("what waits: %d %v", total, err)
	}
	otherList, _, _ := e.h.ModPending(ctx, other, 10)
	if _, err := e.h.ModPendingFile(ctx, mb, otherList[0].ID); !errors.Is(err, hub.ErrNoDropBox) {
		t.Errorf("another box's file: %v", err)
	}
	if p, err := e.h.ModPendingFile(ctx, mb, list[0].ID); err != nil || p.ID != list[0].ID {
		t.Errorf("a file of the box: %v", err)
	}

	done, failed, err := e.h.ModDecide(ctx, mb, []string{list[0].ID}, true)
	if err != nil || done != 1 || failed != 0 || len(e.f.uploads) != 1 {
		t.Fatalf("approve one: %d %d %v (%d uploads)", done, failed, err, len(e.f.uploads))
	}
	if done, _, _ = e.h.ModDecide(ctx, mb, []string{list[1].ID}, false); done != 1 {
		t.Errorf("reject one: %d", done)
	}
	if done, _, _ = e.h.ModDecide(ctx, mb, nil, true); done != 1 || len(e.f.uploads) != 2 {
		t.Errorf("all that are left: %d (%d uploads)", done, len(e.f.uploads))
	}
	if left, _, _ := e.h.ModPending(ctx, other, 10); len(left) != 1 {
		t.Errorf("another box is not touched: %d", len(left))
	}
	ev, _ := e.h.DropEvents(ctx, bobID, b.ID, 20)
	text := ""
	for _, x := range ev {
		text += x.Text + "\n"
	}
	if !strings.Contains(text, "A moderator approved") || !strings.Contains(text, "A moderator rejected") {
		t.Errorf("the log says a moderator did it: %s", text)
	}

	// a new link replaces the old; ending it ends it; a revoked box has none
	again, _ := e.h.SetDropModerator(ctx, bob, b.ID, true)
	if again == token {
		t.Error("a new link is a new link")
	}
	if _, err := e.h.ModBox(ctx, token); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("the old link must stop: %v", err)
	}
	if _, err := e.h.SetDropModerator(ctx, bob, b.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.ModBox(ctx, again); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("the ended link: %v", err)
	}
	t2, _ := e.h.SetDropModerator(ctx, bob, b.ID, true)
	e.h.RevokeDropBox(ctx, e.sess, bob, b.ID)
	if _, err := e.h.ModBox(ctx, t2); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("a revoked box: %v", err)
	}
}

func TestTheHostIsToldOnceThatFilesWaitAndAgainWhenTheQueueWasEmptied(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	e.h.StageDir = t.TempDir()
	var told []string
	e.h.Notify = func(m notify.Message) { told = append(told, m.Text) }
	b := newBox(t, e, hub.DropParams{Name: "Careful", Approve: true})
	for _, n := range []string{"a.jpg", "b.jpg", "c.jpg"} {
		e.h.ReceiveDropFile(ctx, b.ID, n, bytes.NewReader(jpeg), "g")
	}
	if len(told) != 1 || !strings.Contains(told[0], "wait for approval") || !strings.Contains(told[0], "Careful") {
		t.Fatalf("one message for a hundred files: %v", told)
	}
	// deciding on some leaves the host told; deciding on all lets the next file tell again
	pending, _, _ := e.h.DropPending(ctx, bobID, b.ID, 10)
	e.h.DecideDrop(ctx, bob, b.ID, []string{pending[0].ID}, false)
	e.h.ReceiveDropFile(ctx, b.ID, "d.jpg", bytes.NewReader(jpeg), "g")
	if len(told) != 1 {
		t.Errorf("still waiting, no new message: %v", told)
	}
	e.h.DecideDrop(ctx, bob, b.ID, nil, false)
	e.h.ReceiveDropFile(ctx, b.ID, "e.jpg", bytes.NewReader(jpeg), "g")
	if len(told) != 2 {
		t.Errorf("a new file after the queue was emptied tells again: %v", told)
	}
}

func TestTheWallShowsWhoSentAPhotoOnlyWhereTheBoxShowsNames(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	e.f.thumbs = map[string][]byte{"": jpeg}
	named := newBox(t, e, hub.DropParams{Name: "Named", Gallery: true, GalleryNames: true, Prompts: []string{"The cake"}})
	plain := newBox(t, e, hub.DropParams{Name: "Plain", Gallery: true})
	for _, b := range []store.DropBox{named, plain} {
		if _, err := e.h.ReceiveDropFileWith(ctx, b.ID, "a.jpg", bytes.NewReader(jpeg), "g", hub.DropMeta{Name: "Ann", Prompt: "p1"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		b    store.DropBox
		want string
	}{{named, "Ann"}, {plain, ""}} {
		full, _ := e.st.DropBox(ctx, c.b.ID)
		items, _, err := e.h.WallFeed(ctx, full, 0)
		if err != nil || len(items) != 1 || items[0].By != c.want {
			t.Errorf("%s: %+v %v", c.b.Name, items, err)
		}
	}
	if full, _ := e.st.DropBox(ctx, named.ID); true {
		if items, _, _ := e.h.WallFeed(ctx, full, 0); items[0].Caption != "The cake" {
			t.Errorf("the prompt stays: %+v", items[0])
		}
	}
}

func TestWhatGuestsLeaveWithTheBoxGoesWhenItsRetentionIsOverAndThePhotosStay(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	e.f.thumbs = map[string][]byte{"": jpeg}
	var told []string
	e.h.Notify = func(m notify.Message) { told = append(told, m.Text) }
	b := newBox(t, e, hub.DropParams{Name: "Wedding", Closes: time.Now().Add(time.Hour), KeepDays: 7, Gallery: true, GalleryNames: true})
	keep0 := newBox(t, e, hub.DropParams{Name: "Forever", Closes: time.Now().Add(time.Hour), Gallery: true, GalleryNames: true})
	secret := hub.NewGuestSecret()
	for _, box := range []store.DropBox{b, keep0} {
		e.h.ReceiveDropFileWith(ctx, box.ID, "a.jpg", bytes.NewReader(jpeg), "g", hub.DropMeta{Name: "Ann"})
		items, _ := e.st.DropAssetsLatest(ctx, box.ID, 1)
		e.st.ToggleHeart(ctx, box.ID, items[0].ID, hub.GuestKey(secret))
	}
	names := func(box store.DropBox) (string, int) {
		a, _ := e.st.DropAssetsLatest(ctx, box.ID, 1)
		return a[0].GuestName, e.st.HeartTotal(ctx, box.ID)
	}

	// the box ends: the summary counts the hearts; nothing is forgotten yet
	e.st.SetDropSchedule(ctx, b.ID, time.Time{}, time.Now().Add(-time.Hour))
	e.st.SetDropSchedule(ctx, keep0.ID, time.Time{}, time.Now().Add(-time.Hour))
	e.h.DropMaintenance(ctx)
	if n, h := names(b); n != "Ann" || h != 1 {
		t.Errorf("inside the retention: %q %d", n, h)
	}
	found := false
	for _, m := range told {
		found = found || strings.Contains(m, "1 hearts given")
	}
	if !found {
		t.Errorf("the closing message counts the hearts: %v", told)
	}

	// past it: names and hearts go, once; the photo and its place stay; 0 keeps everything
	e.st.SetDropSchedule(ctx, b.ID, time.Time{}, time.Now().Add(-8*24*time.Hour))
	e.st.SetDropSchedule(ctx, keep0.ID, time.Time{}, time.Now().Add(-400*24*time.Hour))
	e.h.DropMaintenance(ctx)
	e.h.DropMaintenance(ctx)
	if n, h := names(b); n != "" || h != 0 {
		t.Errorf("past the retention: %q %d", n, h)
	}
	if ids, _ := e.st.DropAssetIDs(ctx, b.ID); len(ids) != 1 {
		t.Errorf("the photo stays in the box's list: %v", ids)
	}
	if n, h := names(keep0); n != "Ann" || h != 1 {
		t.Errorf("0 days keeps it until the box is revoked: %q %d", n, h)
	}
	ev, _ := e.h.DropEvents(ctx, bobID, b.ID, 30)
	count := 0
	for _, x := range ev {
		if strings.Contains(x.Text, "was deleted; the photos are in the library") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("the log says it once: %d", count)
	}

	// revoking forgets at once
	e.h.RevokeDropBox(ctx, e.sess, bob, keep0.ID)
	if n, h := names(keep0); n != "" || h != 0 {
		t.Errorf("a revoked box keeps nothing guests left: %q %d", n, h)
	}
}

func TestTheOwnerDownloadsEveryPhotoOfTheBoxAsOneZip(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	e.f.thumbs = map[string][]byte{"": jpeg}
	b := newBox(t, e, hub.DropParams{Name: "Trip"})
	for _, n := range []string{"IMG_1.jpg", "IMG_1.jpg", "IMG_2.jpg"} {
		if _, err := e.h.ReceiveDropFile(ctx, b.ID, n, bytes.NewReader(jpeg), "g"); err != nil {
			t.Fatal(err)
		}
	}
	e.h.AddDropHost(ctx, e.sess, bob, b.ID, "ann@x.test", store.HostManage)
	var buf bytes.Buffer
	if _, err := e.h.ZipPhotos(ctx, e.ann, ann, b.ID, &buf); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a co-host downloading everything: %v", err)
	}
	n, err := e.h.ZipPhotos(ctx, e.sess, bob, b.ID, &buf)
	if err != nil || n != 3 {
		t.Fatalf("zip: %d %v", n, err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil || len(zr.File) != 3 {
		t.Fatalf("the archive: %v %d files", err, len(zr.File))
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		if len(data) != len(jpeg) || f.Method != zip.Store {
			t.Errorf("%s: %d bytes, method %d", f.Name, len(data), f.Method)
		}
	}
	if strings.Join(names, ",") != "IMG_1.jpg,IMG_1 (2).jpg,IMG_2.jpg" {
		t.Errorf("names, with the repeat numbered: %v", names)
	}

	// one ZIP of a box at a time
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := e.h.ZipPhotos(ctx, e.sess, bob, b.ID, blockingWriter{started, release})
		done <- err
	}()
	<-started
	if _, err := e.h.ZipPhotos(ctx, e.sess, bob, b.ID, io.Discard); !errors.Is(err, hub.ErrZipBusy) {
		t.Errorf("a second ZIP while one is made: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Errorf("the first one finishes: %v", err)
	}
	if _, err := e.h.ZipPhotos(ctx, e.sess, bob, b.ID, io.Discard); err != nil {
		t.Errorf("when it has finished the next may start: %v", err)
	}
}

// blockingWriter holds its first write until released, to keep a ZIP in the making.
type blockingWriter struct{ started, release chan struct{} }

func (w blockingWriter) Write(p []byte) (int, error) {
	select {
	case <-w.started:
	default:
		close(w.started)
		<-w.release
	}
	return len(p), nil
}

func TestGuestsHaveAFewZipsAtATimeAndOnlyWhereDownloadsAreAllowed(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	e.f.thumbs = map[string][]byte{"": jpeg}
	b := newBox(t, e, hub.DropParams{Name: "Trip", Gallery: true, GalleryDownload: true})
	plain := newBox(t, e, hub.DropParams{Name: "Look only", Gallery: true})
	e.h.ReceiveDropFile(ctx, b.ID, "a.jpg", bytes.NewReader(jpeg), "g")
	gb, _ := e.h.GalleryBox(ctx, b.Token)
	var buf bytes.Buffer
	if n, err := e.h.GalleryZip(ctx, gb, "ann", &buf); err != nil || n != 1 {
		t.Fatalf("zip: %d %v", n, err)
	}
	pb, _ := e.h.GalleryBox(ctx, plain.Token)
	if _, err := e.h.GalleryZip(ctx, pb, "ann", io.Discard); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("a box that does not allow downloads: %v", err)
	}

	// a guest has one at a time; a box has three
	var releases []chan struct{}
	var dones []chan error
	hold := func(who string) {
		started, release := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			_, err := e.h.GalleryZip(ctx, gb, who, blockingWriter{started, release})
			done <- err
		}()
		<-started
		releases, dones = append(releases, release), append(dones, done)
	}
	hold("ann")
	if _, err := e.h.GalleryZip(ctx, gb, "ann", io.Discard); !errors.Is(err, hub.ErrZipBusy) {
		t.Errorf("a second ZIP for the same guest: %v", err)
	}
	hold("bob")
	hold("eve")
	if _, err := e.h.GalleryZip(ctx, gb, "zoe", io.Discard); !errors.Is(err, hub.ErrZipBusy) {
		t.Errorf("a fourth guest at the same moment: %v", err)
	}
	for _, r := range releases {
		close(r)
	}
	for _, d := range dones {
		if err := <-d; err != nil {
			t.Errorf("a held ZIP: %v", err)
		}
	}
	if _, err := e.h.GalleryZip(ctx, gb, "zoe", io.Discard); err != nil {
		t.Errorf("when they are done: %v", err)
	}
}

// rowOf is a box's photo (its place in the box's list) by the file name it was sent under.
func rowOf(t *testing.T, e *env, boxID, name string) store.DropAsset {
	t.Helper()
	want := idOfName(e, name)
	all, _ := e.st.DropAssetsAll(context.Background(), boxID, 0, 1000)
	for _, a := range all {
		if a.AssetID == want {
			return a
		}
	}
	t.Fatalf("no photo %s in the box", name)
	return store.DropAsset{}
}

func TestThePhotosAModeratorPinsOrHidesAreTreatedAsTheyWereChosen(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	e.f.thumbs = map[string][]byte{"a": synth("sharp"), "b": synth("scene"), "c": synth("dark")}
	b := newBox(t, e, hub.DropParams{Name: "Gala", Gallery: true, GalleryDownload: true, Days: 3})
	for _, n := range []string{"a.jpg", "b.jpg", "c.jpg"} {
		if _, err := e.h.ReceiveDropFile(ctx, b.ID, n, bytes.NewReader(jpeg), "m"); err != nil {
			t.Fatal(err)
		}
	}
	token, _ := e.h.SetDropModerator(ctx, bob, b.ID, true)
	mb, _ := e.h.ModBox(ctx, token)
	a, bb, c := rowOf(t, e, b.ID, "a.jpg"), rowOf(t, e, b.ID, "b.jpg"), rowOf(t, e, b.ID, "c.jpg")

	for _, bad := range []string{"", "delete", "pin; drop"} {
		if err := e.h.ModMark(ctx, mb, a.ID, bad); !errors.Is(err, hub.ErrBadMark) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if err := e.h.ModMark(ctx, mb, 99999, "hide"); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("a photo that is not there: %v", err)
	}
	other := newBox(t, e, hub.DropParams{Name: "Other"})
	e.h.ReceiveDropFile(ctx, other.ID, "o.jpg", bytes.NewReader(jpeg), "m")
	oa, _ := e.st.DropAssetsLatest(ctx, other.ID, 1)
	if err := e.h.ModMark(ctx, mb, oa[0].ID, "hide"); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("another box's photo: %v", err)
	}

	// hidden: the moderator still sees it (marked), guests do not (gallery, picture, original, heart, wall, ZIP)
	if err := e.h.ModMark(ctx, mb, bb.ID, "hide"); err != nil {
		t.Fatal(err)
	}
	gb, _ := e.h.GalleryBox(ctx, b.Token)
	items, _ := e.h.GalleryFeed(ctx, gb, "", 0, 0, 0)
	if len(items) != 2 {
		t.Errorf("guests see %d photos", len(items))
	}
	for _, it := range items {
		if it.ID == bb.ID {
			t.Error("a hidden photo is in the gallery")
		}
	}
	if _, _, err := e.h.GalleryImage(ctx, gb, bb.ID); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("a hidden photo's picture: %v", err)
	}
	if _, err := e.h.GalleryOriginal(ctx, gb, bb.ID); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("a hidden photo's original: %v", err)
	}
	if _, _, err := e.h.GalleryHeart(ctx, gb, hub.NewGuestSecret(), bb.ID); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("a heart on a hidden photo: %v", err)
	}
	full, _ := e.st.DropBox(ctx, b.ID)
	if wall, _, _ := e.h.WallFeed(ctx, full, 0); len(wall) != 2 {
		t.Errorf("the wall shows %d photos", len(wall))
	}
	if _, _, err := e.h.WallImage(ctx, full, bb.ID); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("a hidden photo on the wall: %v", err)
	}
	var zipped bytes.Buffer
	if n, err := e.h.GalleryZip(ctx, gb, "ann", &zipped); err != nil || n != 2 {
		t.Errorf("guests' ZIP: %d %v", n, err)
	}
	if n, err := e.h.ZipPhotos(ctx, e.sess, bob, b.ID, io.Discard); err != nil || n != 3 {
		t.Errorf("the owner's ZIP keeps everything: %d %v", n, err)
	}
	mod, _ := e.h.ModPhotos(ctx, mb, 0, 10)
	if len(mod) != 3 || !mod[1].Hidden || mod[0].Hidden || mod[2].Hidden {
		t.Errorf("the moderator sees all three, one hidden: %+v", mod)
	}
	if data, _, err := e.h.ModImage(ctx, mb, bb.ID); err != nil || len(data) == 0 {
		t.Errorf("the moderator sees a hidden photo's picture: %v", err)
	}

	// pinned and hidden win over the looks of a photo in the best-of: the dark one is kept, the nice one is out
	if err := e.h.ModMark(ctx, mb, c.ID, "pin"); err != nil {
		t.Fatal(err)
	}
	if err := e.h.StartHighlights(ctx, e.sess, bob, b.ID, "normal"); err != nil {
		t.Fatal(err)
	}
	h := waitHighlight(t, e, b.ID)
	why := map[string]string{}
	all, _ := e.st.DropVerdicts(ctx, b.ID, true, 100)
	for _, v := range all {
		why[v.Name] = v.Verdict
	}
	if h.Status != store.HighlightDone || why["b.jpg"] != hub.VerdictHidden || why["c.jpg"] != "" {
		t.Errorf("left out: %v (%+v)", why, h)
	}
	ks, _ := e.st.DropVerdicts(ctx, b.ID, false, 100)
	kept := map[string]bool{}
	for _, v := range ks {
		kept[v.Name] = true
	}
	if !kept["c.jpg"] || kept["b.jpg"] || !kept["a.jpg"] {
		t.Errorf("kept: %v", kept)
	}

	// and taking the picks back puts everything as it was
	e.h.ModMark(ctx, mb, bb.ID, "show")
	e.h.ModMark(ctx, mb, c.ID, "unpin")
	if items, _ := e.h.GalleryFeed(ctx, gb, "", 0, 0, 0); len(items) != 3 {
		t.Errorf("after showing it again: %d photos", len(items))
	}
}

func TestTheAlbumOfLovedPhotosHoldsTheHeartedAndThePinnedAndNeverTheHidden(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Wedding", Gallery: true, Days: 3})
	for _, n := range []string{"a.jpg", "b.jpg", "c.jpg", "d.jpg", "e.jpg"} {
		if _, err := e.h.ReceiveDropFile(ctx, b.ID, n, bytes.NewReader(jpeg), "m"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := e.h.MakeLovedAlbum(ctx, e.sess, bob, b.ID); !errors.Is(err, hub.ErrNoLoved) {
		t.Fatalf("nothing is loved yet: %v", err)
	}
	row := func(n string) int64 { return rowOf(t, e, b.ID, n).ID }
	for _, who := range []string{"g1", "g2", "g3"} {
		e.st.ToggleHeart(ctx, b.ID, row("a.jpg"), who) // three hearts
	}
	e.st.ToggleHeart(ctx, b.ID, row("b.jpg"), "g1") // one
	e.st.ToggleHeart(ctx, b.ID, row("c.jpg"), "g1") // one, but hidden afterwards
	e.st.MarkAsset(ctx, b.ID, row("c.jpg"), "hidden", true)
	e.st.MarkAsset(ctx, b.ID, row("e.jpg"), "pinned", true) // no hearts, pinned

	e.h.AddDropHost(ctx, e.sess, bob, b.ID, "ann@x.test", store.HostManage)
	if _, _, err := e.h.MakeLovedAlbum(ctx, e.ann, ann, b.ID); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a co-host: %v", err)
	}
	name, n, err := e.h.MakeLovedAlbum(ctx, e.sess, bob, b.ID)
	if err != nil || name != "Wedding loved by guests" || n != 3 {
		t.Fatalf("album: %q %d %v", name, n, err)
	}
	album := ""
	for _, a := range e.f.albums {
		if a["albumName"] == name {
			album = a["id"].(string)
		}
	}
	in := e.f.members[album]
	if len(in) != 3 || !in[idOfName(e, "a.jpg")] || !in[idOfName(e, "b.jpg")] || !in[idOfName(e, "e.jpg")] || in[idOfName(e, "c.jpg")] || in[idOfName(e, "d.jpg")] {
		t.Errorf("the album: %v", in)
	}
	// run again after a change: the hidden one stays out, a new heart brings one in, what the owner added stays
	foreign := uuid.NewString()
	in[foreign] = true
	e.st.ToggleHeart(ctx, b.ID, row("d.jpg"), "g2")
	e.st.MarkAsset(ctx, b.ID, row("b.jpg"), "hidden", true)
	if _, n, err = e.h.MakeLovedAlbum(ctx, e.sess, bob, b.ID); err != nil || n != 3 {
		t.Fatalf("again: %d %v", n, err)
	}
	in = e.f.members[album]
	if in[idOfName(e, "b.jpg")] || !in[idOfName(e, "d.jpg")] || !in[foreign] || len(in) != 4 {
		t.Errorf("the corrected album: %v", in)
	}
}
