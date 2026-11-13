package hub_test

import (
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
