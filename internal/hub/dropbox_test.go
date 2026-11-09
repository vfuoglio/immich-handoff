package hub_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// a few bytes that start like a real JPEG, and like a web page
var (
	jpeg = append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0}, make([]byte, 2000)...)
	page = []byte("<!DOCTYPE html><html><script>alert(1)</script></html>")
	heic = append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c', 0, 0, 0, 0}, make([]byte, 100)...)
	mp4  = append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'm', 'p', '4', '2', 0, 0, 0, 0}, make([]byte, 100)...)
)

func newBox(t *testing.T, e *env, p hub.DropParams) store.DropBox {
	t.Helper()
	b, err := e.h.CreateDropBox(context.Background(), e.sess, bob, p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestADropBoxStoresWhatGuestsSendAndTagsIt(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Wedding", Message: "Send us your photos"})

	// the key can store and tag, and nothing else
	if want := "asset.upload"; !has(e.f.lastPerms, want) || has(e.f.lastPerms, "asset.read") || has(e.f.lastPerms, "asset.delete") || has(e.f.lastPerms, "album.read") {
		t.Errorf("drop box key permissions: %v", e.f.lastPerms)
	}
	if b.InboxTag != "inbox/Wedding" || len(b.Token) < 32 {
		t.Errorf("tag %q, token %q", b.InboxTag, b.Token)
	}
	box, err := e.h.OpenDropBox(ctx, b.Token)
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.h.ReceiveDropFile(ctx, box.ID, "../../IMG 001?.png", bytes.NewReader(jpeg), "guest1")
	if err != nil || out != hub.DropStored {
		t.Fatalf("receive: %v %v", out, err)
	}
	if len(e.f.uploads) != 1 || e.f.uploads[0].person != "bob" || e.f.uploads[0].size != len(jpeg) {
		t.Fatalf("uploads: %+v", e.f.uploads)
	}
	// the stored name has the extension of what the file is, not of what the guest called it, and no path
	if got := e.f.uploads[0].name; got != "IMG 001_.jpg" {
		t.Errorf("stored name: %q", got)
	}
	if len(e.f.tagged) != 1 {
		t.Errorf("tagged: %v", e.f.tagged)
	}
	if got, _ := e.st.DropBox(ctx, b.ID); got.Files != 1 || got.Bytes != int64(len(jpeg)) {
		t.Errorf("counters: %+v", got)
	}
	// the log says what arrived and holds a mark, not an address
	ev, _ := e.h.DropEvents(ctx, bob.ID, b.ID, 10)
	if len(ev) < 2 || ev[0].Kind != store.DropUploaded || ev[0].Who != "guest1" {
		t.Errorf("log: %+v", ev)
	}
}

func TestADropBoxTurnsAwayWhatIsNotAPhotoOrVideo(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Party", PhotosOnly: false})
	for name, data := range map[string][]byte{"page.jpg": page, "empty.jpg": {}, "text.png": []byte("just words")} {
		if _, err := e.h.ReceiveDropFile(ctx, b.ID, name, bytes.NewReader(data), "g"); err == nil {
			t.Errorf("%s was taken", name)
		}
	}
	for name, data := range map[string][]byte{"a.heic": heic, "b.mov": mp4} {
		if _, err := e.h.ReceiveDropFile(ctx, b.ID, name, bytes.NewReader(data), "g"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a box for photos only refuses a video, though it is a real one
	p := newBox(t, e, hub.DropParams{Name: "Photos", PhotosOnly: true})
	if _, err := e.h.ReceiveDropFile(ctx, p.ID, "v.mp4", bytes.NewReader(mp4), "g"); !errors.Is(err, hub.ErrDropType) {
		t.Errorf("video in a photos-only box: %v", err)
	}
	if got, _ := e.st.DropBox(ctx, b.ID); got.Files != 2 {
		t.Errorf("refused files must not use up room: %+v", got)
	}
	if len(e.f.uploads) != 2 {
		t.Errorf("only two files should have reached Immich: %+v", e.f.uploads)
	}
}

func TestADropBoxHonoursItsLimits(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	big := append(append([]byte{}, jpeg...), make([]byte, 1<<20)...)

	one := newBox(t, e, hub.DropParams{Name: "Small", MaxFileMB: 1})
	if _, err := e.h.ReceiveDropFile(ctx, one.ID, "big.jpg", bytes.NewReader(big), "g"); !errors.Is(err, hub.ErrDropTooBig) {
		t.Errorf("file over the per-file limit: %v", err)
	}
	if got, _ := e.st.DropBox(ctx, one.ID); got.Files != 0 || got.Bytes != 0 {
		t.Errorf("a refused file leaves no trace in the counters: %+v", got)
	}

	two := newBox(t, e, hub.DropParams{Name: "Few", MaxFiles: 2})
	for i := 0; i < 2; i++ {
		if _, err := e.h.ReceiveDropFile(ctx, two.ID, "a.jpg", bytes.NewReader(jpeg), "g"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.h.ReceiveDropFile(ctx, two.ID, "a.jpg", bytes.NewReader(jpeg), "g"); !errors.Is(err, hub.ErrDropFull) {
		t.Errorf("third file: %v", err)
	}

	tot := newBox(t, e, hub.DropParams{Name: "Total", MaxMB: 1})
	if _, err := e.h.ReceiveDropFile(ctx, tot.ID, "a.jpg", bytes.NewReader(big[:700<<10]), "g"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.ReceiveDropFile(ctx, tot.ID, "b.jpg", bytes.NewReader(big[:700<<10]), "g"); !errors.Is(err, hub.ErrDropTooBig) {
		t.Errorf("a file that would go past the total: %v", err)
	}
}

func TestPausedExpiredAndRevokedBoxesAnswerAlike(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Closing"})
	if err := e.h.SetDropBoxPaused(ctx, bob, b.ID, true); err != nil {
		t.Fatal(err)
	}
	_, paused := e.h.OpenDropBox(ctx, b.Token)
	e.h.SetDropBoxPaused(ctx, bob, b.ID, false)
	if _, err := e.h.OpenDropBox(ctx, b.Token); err != nil {
		t.Fatalf("resumed: %v", err)
	}
	if err := e.h.RevokeDropBox(ctx, e.sess, bob, b.ID); err != nil {
		t.Fatal(err)
	}
	_, revoked := e.h.OpenDropBox(ctx, b.Token)
	_, unknown := e.h.OpenDropBox(ctx, strings.Repeat("a", 32))
	if paused == nil || paused != revoked || revoked != unknown {
		t.Errorf("answers differ: paused %v, revoked %v, unknown %v", paused, revoked, unknown)
	}
	if len(e.f.deletedKeys) != 1 {
		t.Errorf("the key must be deleted from the owner's account: %v", e.f.deletedKeys)
	}
	// only the owner may touch a box
	other := newBox(t, e, hub.DropParams{Name: "Mine"})
	if err := e.h.SetDropBoxPaused(ctx, ann, other.ID, true); !errors.Is(err, hub.ErrNoDropBox) {
		t.Errorf("another person paused it: %v", err)
	}
	if err := e.h.RevokeDropBox(ctx, e.ann, ann, other.ID); !errors.Is(err, hub.ErrNoDropBox) {
		t.Errorf("another person revoked it: %v", err)
	}
	// a box made with an end date stops by itself
	short := newBox(t, e, hub.DropParams{Name: "Short", Days: 1})
	if short.ExpiresAt.Before(time.Now().Add(23 * time.Hour)) {
		t.Errorf("end: %v", short.ExpiresAt)
	}
}

func TestDropBoxPasswordAndOptionsAreChecked(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Secret", Password: "swordfish"})
	if b.Password == "" || strings.Contains(b.Password, "swordfish") {
		t.Errorf("the password must be kept hashed: %q", b.Password)
	}
	if !e.h.CheckDropPassword(ctx, b, "g", "swordfish") || e.h.CheckDropPassword(ctx, b, "g", "swordfisH") {
		t.Error("password check")
	}
	for _, p := range []hub.DropParams{{Name: "x"}, {Name: "a/b"}, {Name: "ok", Days: 400}, {Name: "ok", MaxMB: -1}, {Name: "ok", Message: strings.Repeat("x", 301)}} {
		if _, err := e.h.CreateDropBox(ctx, e.sess, bob, p); !hub.IsUserError(err) {
			t.Errorf("%+v: %v", p, err)
		}
	}
	// a failed creation leaves no key behind
	if len(e.f.keys) != 1 { // only the one of the box above
		t.Errorf("keys: %d", len(e.f.keys))
	}
}

func TestAGuestWhoAsksTooOftenIsTurnedAwayAndTheBoxPausesInAFlood(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Flood"})
	var told []string
	e.h.Notify = func(m string) { told = append(told, m) }

	// one guest: thirty requests in ten minutes, then silence
	n := 0
	for i := 0; i < 40; i++ {
		if e.h.AllowDrop(ctx, b, "noisy") == nil {
			n++
			e.h.DoneDrop(b)
		}
	}
	if n != 30 {
		t.Errorf("allowed %d requests", n)
	}
	// another guest is not affected
	if err := e.h.AllowDrop(ctx, b, "quiet"); err != nil {
		t.Errorf("a different guest: %v", err)
	}
	e.h.DoneDrop(b)
	if ev, _ := e.h.DropEvents(ctx, bob.ID, b.ID, 50); countKind(ev, store.DropBlocked) != 1 {
		t.Errorf("the log should say once that the guest was turned away: %+v", ev)
	}

	// four uploads at once is the most one box takes
	var held int
	for i := 0; i < 6; i++ {
		if e.h.AllowDrop(ctx, b, "g"+string(rune('a'+i))) == nil {
			held++
		}
	}
	if held != 4 {
		t.Errorf("concurrent uploads allowed: %d", held)
	}

	// a flood of refusals pauses the box and tells the administrator
	for i := 0; i < 210 && len(told) == 0; i++ {
		e.h.RejectDrop(ctx, b, "bot", "x.exe", hub.ErrDropType)
	}
	if got, _ := e.st.DropBox(ctx, b.ID); got.State != store.DropPaused || !strings.Contains(got.Note, "Paused by itself") || len(told) != 1 {
		t.Errorf("state %q note %q told %v", got.State, got.Note, told)
	}
	if _, err := e.h.OpenDropBox(ctx, b.Token); err == nil {
		t.Error("a paused box must not take uploads")
	}
}

func TestWrongPasswordsSilenceAGuest(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Locked", Password: "right"})
	for i := 0; i < 5; i++ {
		if err := e.h.AllowDrop(ctx, b, "guesser"); err != nil {
			t.Fatal(err)
		}
		e.h.CheckDropPassword(ctx, b, "guesser", "wrong")
		e.h.DoneDrop(b)
	}
	if err := e.h.AllowDrop(ctx, b, "guesser"); !errors.Is(err, hub.ErrDropBusy) {
		t.Errorf("after five wrong passwords: %v", err)
	}
}

func countKind(ev []store.DropEvent, kind string) (n int) {
	for _, e := range ev {
		if e.Kind == kind {
			n++
		}
	}
	return
}

// --- filing, approval, co-hosts and time ---

func TestADropBoxFilesWhatArrivesInItsAlbumAndTag(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Gala", Album: "Gala photos", ExtraTag: "gala"})
	if !has(e.f.lastPerms, "album.create") || !has(e.f.lastPerms, "albumAsset.create") || has(e.f.lastPerms, "album.delete") || has(e.f.lastPerms, "asset.read") {
		t.Errorf("a box with an album may find, make and add to albums and nothing more: %v", e.f.lastPerms)
	}
	if _, err := e.h.ReceiveDropFile(ctx, b.ID, "a.jpg", bytes.NewReader(jpeg), "g"); err != nil {
		t.Fatal(err)
	}
	if !has(e.f.tagNames, "inbox/Gala") || !has(e.f.tagNames, "gala") || len(e.f.tagged) != 2 {
		t.Errorf("tags made %v, photos tagged %d times", e.f.tagNames, len(e.f.tagged))
	}
	if len(e.f.albumAdds) != 1 || !strings.HasPrefix(e.f.albumAdds[0], "Gala photos: ") {
		t.Errorf("album: %v", e.f.albumAdds)
	}
	// the second photo goes in the same album, which is not made again
	e.h.ReceiveDropFile(ctx, b.ID, "b.jpg", bytes.NewReader(jpeg), "g")
	if len(e.f.albumAdds) != 2 || len(e.f.albums) != 1 {
		t.Errorf("albums %d, adds %v", len(e.f.albums), e.f.albumAdds)
	}
	// a plain box asks for no album permission
	newBox(t, e, hub.DropParams{Name: "Plain"})
	if has(e.f.lastPerms, "album.create") {
		t.Error("a box with no album must not be given album permissions")
	}
	// a duplicate is told apart, and not filed again
	before := len(e.f.tagged)
	if out, err := e.h.ReceiveDropFile(ctx, b.ID, "dupe.jpg", bytes.NewReader(jpeg), "g"); err != nil || out != hub.DropDuplicate || len(e.f.tagged) != before {
		t.Errorf("duplicate: %v %v tagged %d->%d", out, err, before, len(e.f.tagged))
	}
	if _, err := e.h.CreateDropBox(ctx, e.sess, bob, hub.DropParams{Name: "Bad", Album: "a/b"}); !errors.Is(err, hub.ErrBadDropAlbum) {
		t.Errorf("a slash in an album name: %v", err)
	}
}

func TestFilesWaitForApprovalAndOnlyHostsDecide(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	if _, err := e.h.CreateDropBox(ctx, e.sess, bob, hub.DropParams{Name: "Careful", Approve: true}); !errors.Is(err, hub.ErrNoStaging) {
		t.Fatalf("with no place to keep files: %v", err)
	}
	e.h.StageDir = t.TempDir()
	b := newBox(t, e, hub.DropParams{Name: "Careful", Approve: true})
	out, err := e.h.ReceiveDropFile(ctx, b.ID, "a.jpg", bytes.NewReader(jpeg), "g1")
	if err != nil || out != hub.DropHeld {
		t.Fatalf("receive: %v %v", out, err)
	}
	e.h.ReceiveDropFile(ctx, b.ID, "b.jpg", bytes.NewReader(jpeg), "g2")
	e.h.ReceiveDropFile(ctx, b.ID, "c.jpg", bytes.NewReader(jpeg), "g2")
	if _, err := e.h.ReceiveDropFile(ctx, b.ID, "page.jpg", bytes.NewReader(page), "g2"); err == nil {
		t.Error("a page must be refused before it waits")
	}
	if len(e.f.uploads) != 0 {
		t.Fatalf("nothing may reach the library before approval: %v", e.f.uploads)
	}
	pend, n, err := e.h.DropPending(ctx, bobID, b.ID, 10)
	if err != nil || n != 3 || len(pend) != 3 {
		t.Fatalf("pending: %d %d %v", n, len(pend), err)
	}
	if got, _ := e.st.DropBox(ctx, b.ID); got.Files != 3 || got.Bytes != int64(3*len(jpeg)) {
		t.Errorf("waiting files count against the box: %+v", got)
	}

	// a stranger cannot see or decide; a co-host who only views cannot decide either
	if _, _, err := e.h.DropPending(ctx, annID, b.ID, 10); !errors.Is(err, hub.ErrNoDropBox) {
		t.Errorf("a stranger sees the queue: %v", err)
	}
	if err := e.h.AddDropHost(ctx, e.sess, bob, b.ID, "ann@x.test", store.HostView); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.h.DecideDrop(ctx, ann, b.ID, nil, true); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a viewer decides: %v", err)
	}
	if _, err := e.h.DropPendingFile(ctx, annID, pend[0].ID); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a viewer opens a waiting file: %v", err)
	}

	// approve one by name; the file goes to the library, is filed, and its place on the disk is freed
	done, failed, err := e.h.DecideDrop(ctx, bob, b.ID, []string{pend[0].ID}, true)
	if err != nil || done != 1 || failed != 0 {
		t.Fatalf("approve: %d %d %v", done, failed, err)
	}
	if len(e.f.uploads) != 1 || len(e.f.tagged) != 1 {
		t.Errorf("uploads %v tagged %v", e.f.uploads, e.f.tagged)
	}
	if _, err := os.Stat(pend[0].Path); !os.IsNotExist(err) {
		t.Errorf("an approved file must leave the disk: %v", err)
	}
	// the same file cannot be approved twice
	if done, _, _ := e.h.DecideDrop(ctx, bob, b.ID, []string{pend[0].ID}, true); done != 0 || len(e.f.uploads) != 1 {
		t.Errorf("approved twice: %d, uploads %d", done, len(e.f.uploads))
	}
	// reject all: deleted from the disk, the room given back
	done, _, err = e.h.DecideDrop(ctx, bob, b.ID, nil, false)
	if err != nil || done != 2 {
		t.Fatalf("reject: %d %v", done, err)
	}
	if got, _ := e.st.DropBox(ctx, b.ID); got.Files != 1 || got.Bytes != int64(len(jpeg)) {
		t.Errorf("rejected files give their room back: %+v", got)
	}
	for _, p := range pend[1:] {
		if _, err := os.Stat(p.Path); !os.IsNotExist(err) {
			t.Errorf("a rejected file must leave the disk: %v", err)
		}
	}
	if len(e.f.uploads) != 1 {
		t.Errorf("rejected files must never be stored: %v", e.f.uploads)
	}
	// revoking deletes what still waits
	e.h.ReceiveDropFile(ctx, b.ID, "d.jpg", bytes.NewReader(jpeg), "g")
	left, _, _ := e.h.DropPending(ctx, bobID, b.ID, 10)
	e.h.RevokeDropBox(ctx, e.sess, bob, b.ID)
	if _, err := os.Stat(left[0].Path); !os.IsNotExist(err) {
		t.Errorf("revoking must delete waiting files: %v", err)
	}
}

func TestCoHostsSeeTheBoxAndNothingElse(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Shared", Days: 3})
	for email, want := range map[string]error{"nobody@x.test": hub.ErrNoSuchPerson, "bob@x.test": hub.ErrHostSelf} {
		if err := e.h.AddDropHost(ctx, e.sess, bob, b.ID, email, store.HostManage); !errors.Is(err, want) {
			t.Errorf("%s: %v", email, err)
		}
	}
	if err := e.h.AddDropHost(ctx, e.sess, bob, b.ID, "ann@x.test", "boss"); !errors.Is(err, hub.ErrBadHostRole) {
		t.Errorf("an unknown role: %v", err)
	}
	if err := e.h.AddDropHost(ctx, e.sess, bob, b.ID, "ANN@x.test", store.HostManage); err != nil {
		t.Fatal(err)
	}
	// Ann finds the box on her page, without its key, and without knowing who else helps
	boxes, _ := e.h.DropBoxes(ctx, annID)
	if len(boxes) != 1 || boxes[0].Role != "manage" || boxes[0].Box.Key != "" || boxes[0].Hosts != nil {
		t.Fatalf("what a co-host sees: %+v", boxes)
	}
	if mine, _ := e.h.DropBoxes(ctx, bobID); len(mine) != 1 || mine[0].Role != "owner" || len(mine[0].Hosts) != 1 {
		t.Errorf("the owner sees the co-hosts: %+v", mine)
	}
	// she may pause, resume and extend; she may not end it, choose co-hosts, or see the log of a box she does not help with
	if err := e.h.SetDropBoxPaused(ctx, ann, b.ID, true); err != nil {
		t.Errorf("a co-host pauses: %v", err)
	}
	if err := e.h.SetDropBoxPaused(ctx, ann, b.ID, false); err != nil {
		t.Error(err)
	}
	if err := e.h.ExtendDropBox(ctx, ann, b.ID, 2); err != nil {
		t.Errorf("a co-host extends: %v", err)
	}
	if err := e.h.RevokeDropBox(ctx, e.ann, ann, b.ID); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a co-host revokes: %v", err)
	}
	if err := e.h.AddDropHost(ctx, e.ann, ann, b.ID, "bob@x.test", store.HostView); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a co-host chooses co-hosts: %v", err)
	}
	if err := e.h.RemoveDropHost(ctx, ann, b.ID, bobID); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a co-host removes the owner: %v", err)
	}
	if _, err := e.h.DropEvents(ctx, uuid.New(), b.ID, 5); !errors.Is(err, hub.ErrNoDropBox) {
		t.Errorf("a stranger reads the log: %v", err)
	}
	// she can stop helping; then the box is gone from her page
	if err := e.h.RemoveDropHost(ctx, ann, b.ID, annID); err != nil {
		t.Fatal(err)
	}
	if boxes, _ := e.h.DropBoxes(ctx, annID); len(boxes) != 0 {
		t.Errorf("after leaving: %+v", boxes)
	}
	if err := e.h.SetDropBoxPaused(ctx, ann, b.ID, true); !errors.Is(err, hub.ErrNoDropBox) {
		t.Errorf("a former co-host pauses: %v", err)
	}
}

func TestADropBoxOpensAndClosesOnSchedule(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	soon := newBox(t, e, hub.DropParams{Name: "Later", Opens: time.Now().Add(2 * time.Hour), Days: 1})
	if _, err := e.h.OpenDropBox(ctx, soon.Token); err == nil {
		t.Error("a box that has not opened takes nothing")
	}
	if soon.ExpiresAt.Before(time.Now().Add(25 * time.Hour)) {
		t.Errorf("the days count from the opening: %v", soon.ExpiresAt)
	}
	for _, p := range []hub.DropParams{
		{Name: "Back", Closes: time.Now().Add(-time.Hour)},
		{Name: "Inverted", Opens: time.Now().Add(5 * time.Hour), Closes: time.Now().Add(time.Hour)},
		{Name: "Far", Closes: time.Now().AddDate(2, 0, 0)},
	} {
		if _, err := e.h.CreateDropBox(ctx, e.sess, bob, p); !errors.Is(err, hub.ErrBadDropWhen) {
			t.Errorf("%s: %v", p.Name, err)
		}
	}

	// the reminder and the closing message are each sent once; extending lets them go again
	var told []string
	e.h.Notify = func(m string) { told = append(told, m) }
	b := newBox(t, e, hub.DropParams{Name: "Party", Closes: time.Now().Add(2 * time.Hour)})
	e.h.DropMaintenance(ctx)
	e.h.DropMaintenance(ctx)
	if len(told) != 1 || !strings.Contains(told[0], "closes at") {
		t.Fatalf("reminder: %v", told)
	}
	e.st.SetDropSchedule(ctx, b.ID, time.Time{}, time.Now().Add(-time.Minute)) // it is over
	e.h.ReceiveDropFile(ctx, b.ID, "late.jpg", bytes.NewReader(jpeg), "g")
	e.h.DropMaintenance(ctx)
	e.h.DropMaintenance(ctx)
	if len(told) != 2 || !strings.Contains(told[1], "closed") || !strings.Contains(told[1], "0 files") {
		t.Fatalf("closing message: %v", told)
	}
	if _, err := e.h.OpenDropBox(ctx, b.Token); err == nil {
		t.Error("a box that has closed takes nothing")
	}
	// extend by a day: open again, and it will remind again
	if err := e.h.ExtendDropBox(ctx, bob, b.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.OpenDropBox(ctx, b.Token); err != nil {
		t.Errorf("after extending: %v", err)
	}
	got, _ := e.st.DropBox(ctx, b.ID)
	if got.Notified != 0 || got.ExpiresAt.Before(time.Now().Add(23*time.Hour)) {
		t.Errorf("extend: %+v", got)
	}
	if err := e.h.ExtendDropBox(ctx, bob, b.ID, 0); !errors.Is(err, hub.ErrBadDropLimit) {
		t.Errorf("extend by nothing: %v", err)
	}
	noEnd := newBox(t, e, hub.DropParams{Name: "Endless"})
	if err := e.h.ExtendDropBox(ctx, bob, noEnd.ID, 1); !errors.Is(err, hub.ErrNoEnd) {
		t.Errorf("a box with no end: %v", err)
	}
}

func TestTheSummaryCountsGuestsAndTheBusiestHour(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Count"})
	for i, who := range []string{"a", "a", "b"} {
		if _, err := e.h.ReceiveDropFile(ctx, b.ID, fmt.Sprint(i, ".jpg"), bytes.NewReader(jpeg), who); err != nil {
			t.Fatal(err)
		}
	}
	sum, _ := e.st.DropSummaryOf(ctx, b.ID)
	if sum.Guests != 2 || sum.BusiestFiles != 3 || sum.BusiestHour == "" {
		t.Errorf("summary: %+v", sum)
	}
}
