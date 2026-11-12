package hub_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	imgjpeg "image/jpeg"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/notify"
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
	e.h.Notify = func(m notify.Message) { told = append(told, m.Text) }

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
	e.h.Notify = func(m notify.Message) { told = append(told, m.Text) }
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

// --- templates, more ways in, the look of the page, and guests taking back what they sent ---

func TestTemplatesKeepSettingsAndStartNewBoxes(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Wedding 2026", Message: "Hi!", Days: 10, MaxFileMB: 50, MaxMB: 900, MaxFiles: 40, PhotosOnly: true, Album: "Wedding", ExtraTag: "wed", Accent: "#AA3355", Lang: "it", MaxDim: 2000, Password: "secret"})
	if err := e.h.SaveDropTemplate(ctx, bob, b.ID, "Every wedding"); err != nil {
		t.Fatal(err)
	}
	all, _ := e.h.DropTemplates(ctx, bobID)
	if len(all) != 4 || !all[0].Builtin || all[3].Name != "Every wedding" {
		t.Fatalf("templates: %+v", all)
	}
	// what is copied: the settings. What is not: the name, the link, the password, the guests
	p, err := e.h.TemplateParams(ctx, bobID, all[3].ID, "Wedding 2027")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Wedding 2027" || p.Message != "Hi!" || p.Days != 10 || p.MaxFileMB != 50 || p.MaxMB != 900 || p.MaxFiles != 40 || !p.PhotosOnly ||
		p.Album != "Wedding" || p.ExtraTag != "wed" || p.Accent != "#aa3355" || p.Lang != "it" || p.MaxDim != 2000 || p.Password != "" {
		t.Errorf("params: %+v", p)
	}
	n := newBox(t, e, p)
	if n.Token == b.Token || n.Album != "Wedding" || n.Password != "" {
		t.Errorf("the new box: %+v", n)
	}
	// duplicating a box needs no template
	d, err := e.h.DuplicateParams(ctx, bobID, b.ID, "Copy")
	if err != nil || d.Name != "Copy" || d.MaxFiles != 40 {
		t.Errorf("duplicate: %+v %v", d, err)
	}
	// the built-in one that needs approval does not where files cannot wait
	if sp, _ := e.h.TemplateParams(ctx, bobID, "builtin:school", "Trip"); sp.Approve || !sp.PhotosOnly {
		t.Errorf("school trip without a staging folder: %+v", sp)
	}
	e.h.StageDir = t.TempDir()
	if sp, _ := e.h.TemplateParams(ctx, bobID, "builtin:school", "Trip"); !sp.Approve {
		t.Errorf("school trip with one: %+v", sp)
	}
	// templates belong to their owner
	if mine, _ := e.h.DropTemplates(ctx, annID); len(mine) != 3 {
		t.Errorf("another person sees %d templates", len(mine))
	}
	if _, err := e.h.TemplateParams(ctx, annID, all[3].ID, "x"); !errors.Is(err, hub.ErrNoTemplate) {
		t.Errorf("another person's template: %v", err)
	}
	if err := e.h.SaveDropTemplate(ctx, ann, b.ID, "Mine now"); !errors.Is(err, hub.ErrNoDropBox) {
		t.Errorf("saving from a box that is not yours: %v", err)
	}
	if err := e.h.SaveDropTemplate(ctx, bob, b.ID, "x"); !errors.Is(err, hub.ErrBadTemplate) {
		t.Errorf("a one-letter name: %v", err)
	}
	e.h.DeleteDropTemplate(ctx, ann, all[3].ID) // not hers: nothing happens
	if again, _ := e.h.DropTemplates(ctx, bobID); len(again) != 4 {
		t.Error("another person deleted a template")
	}
	e.h.DeleteDropTemplate(ctx, bob, all[3].ID)
	if again, _ := e.h.DropTemplates(ctx, bobID); len(again) != 3 {
		t.Error("deleting a template")
	}
}

func TestEachWayInIsEndedOnItsOwn(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Ways"})
	if err := e.h.SetDropShort(ctx, bob, b.ID, true); err != nil {
		t.Fatal(err)
	}
	got, _ := e.st.DropBox(ctx, b.ID)
	if len(got.Short) != 10 || got.Short == b.Token {
		t.Fatalf("short code: %q", got.Short)
	}
	for _, way := range []string{b.Token, got.Short} {
		if _, err := e.h.OpenDropBox(ctx, way); err != nil {
			t.Errorf("%s does not open the box: %v", way, err)
		}
	}
	// ending the short one leaves the long one
	e.h.SetDropShort(ctx, bob, b.ID, false)
	if _, err := e.h.OpenDropBox(ctx, got.Short); err == nil {
		t.Error("the short link must stop")
	}
	if _, err := e.h.OpenDropBox(ctx, b.Token); err != nil {
		t.Errorf("the long link must stay: %v", err)
	}
	// a new long link leaves the short one, and stops the old long one
	e.h.SetDropShort(ctx, bob, b.ID, true)
	short, _ := e.st.DropBox(ctx, b.ID)
	if err := e.h.RenewDropToken(ctx, bob, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.OpenDropBox(ctx, b.Token); err == nil {
		t.Error("the old long link must stop")
	}
	if _, err := e.h.OpenDropBox(ctx, short.Short); err != nil {
		t.Errorf("the short link must stay: %v", err)
	}
	// who may: a co-host who manages makes a short link, not a new long one
	e.h.AddDropHost(ctx, e.sess, bob, b.ID, "ann@x.test", store.HostManage)
	if err := e.h.SetDropShort(ctx, ann, b.ID, false); err != nil {
		t.Errorf("a manager removing the short link: %v", err)
	}
	if err := e.h.RenewDropToken(ctx, ann, b.ID); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a manager renewing the long link: %v", err)
	}
	// two boxes never share a code, and a revoked box gives its code up
	e.h.SetDropShort(ctx, bob, b.ID, true)
	other := newBox(t, e, hub.DropParams{Name: "Other"})
	e.h.SetDropShort(ctx, bob, other.ID, true)
	a, _ := e.st.DropBox(ctx, b.ID)
	o, _ := e.st.DropBox(ctx, other.ID)
	if a.Short == "" || o.Short == "" || a.Short == o.Short {
		t.Errorf("codes: %q %q", a.Short, o.Short)
	}
	e.h.RevokeDropBox(ctx, e.sess, bob, b.ID)
	if _, err := e.h.OpenDropBox(ctx, a.Short); err == nil {
		t.Error("a revoked box's short link must stop")
	}
}

func TestTheLookOfTheGuestPageIsChecked(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Pretty"})
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, make([]byte, 100)...)
	if err := e.h.SetDropLook(ctx, bob, b.ID, "#112233", "de", 2400, png, false); err != nil {
		t.Fatal(err)
	}
	got, _ := e.st.DropBox(ctx, b.ID)
	if got.Accent != "#112233" || got.Lang != "de" || got.MaxDim != 2400 || !e.st.HasDropCover(ctx, b.ID) {
		t.Errorf("look: %+v", got)
	}
	for _, c := range []struct {
		accent, lang string
		dim          int
		cover        []byte
	}{{"red", "", 0, nil}, {"#12345", "", 0, nil}, {"", "xx", 0, nil}, {"", "", 100, nil}, {"", "", 9000, nil}, {"", "", 0, page}, {"", "", 0, make([]byte, 1<<20)}} {
		if err := e.h.SetDropLook(ctx, bob, b.ID, c.accent, c.lang, c.dim, c.cover, false); err == nil {
			t.Errorf("%+v was taken", c)
		}
	}
	// leaving the cover alone keeps it; asking removes it
	e.h.SetDropLook(ctx, bob, b.ID, "", "", 0, nil, false)
	if !e.st.HasDropCover(ctx, b.ID) {
		t.Error("the cover must stay when nothing is said about it")
	}
	e.h.SetDropLook(ctx, bob, b.ID, "", "", 0, nil, true)
	if e.st.HasDropCover(ctx, b.ID) {
		t.Error("the cover must go when asked")
	}
	e.h.AddDropHost(ctx, e.sess, bob, b.ID, "ann@x.test", store.HostView)
	if err := e.h.SetDropLook(ctx, ann, b.ID, "#000000", "", 0, nil, false); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a viewer changing the look: %v", err)
	}
}

func TestGuestsTakeBackWhatTheySentWhileItIsInTheInbox(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Undo", GuestDelete: true})
	if !has(e.f.lastPerms, "asset.delete") || !has(e.f.lastPerms, "asset.read") {
		t.Fatalf("a box that lets guests delete needs asset.read and asset.delete: %v", e.f.lastPerms)
	}
	plain := newBox(t, e, hub.DropParams{Name: "Plain"})
	if has(e.f.lastPerms, "asset.delete") || has(e.f.lastPerms, "asset.read") {
		t.Errorf("an ordinary box must not be able to read or delete: %v", e.f.lastPerms)
	}
	mine, theirs := hub.NewGuestSecret(), hub.NewGuestSecret()
	for _, n := range []string{"one.jpg", "two.jpg", "three.jpg"} {
		if _, err := e.h.ReceiveDropFileFor(ctx, b.ID, n, bytes.NewReader(jpeg), "mark", mine); err != nil {
			t.Fatal(err)
		}
	}
	e.h.ReceiveDropFileFor(ctx, b.ID, "other.jpg", bytes.NewReader(jpeg), "mark2", theirs)
	list, err := e.h.GuestUploads(ctx, b.ID, mine)
	if err != nil || len(list) != 3 {
		t.Fatalf("my files: %d %v", len(list), err)
	}
	if o, _ := e.h.GuestUploads(ctx, b.ID, theirs); len(o) != 1 {
		t.Errorf("their files: %d", len(o))
	}
	if _, err := e.h.GuestUploads(ctx, b.ID, "nobody"); err != nil && len(list) == 0 {
		t.Error("an unknown guest sees no files")
	}
	if none, _ := e.h.GuestUploads(ctx, b.ID, "nobody"); len(none) != 0 {
		t.Errorf("a stranger sees files: %v", none)
	}
	// a guest cannot delete somebody else's file
	if err := e.h.DeleteGuestUpload(ctx, b.ID, theirs, list[0].ID); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("deleting another guest's file: %v", err)
	}
	// deleting moves it to the owner's trash and gives the room back
	before, _ := e.st.DropBox(ctx, b.ID)
	if err := e.h.DeleteGuestUpload(ctx, b.ID, mine, list[0].ID); err != nil {
		t.Fatal(err)
	}
	after, _ := e.st.DropBox(ctx, b.ID)
	trashed := 0
	for _, v := range e.f.trashed {
		if v {
			trashed++
		}
	}
	if trashed != 1 || after.Files != before.Files-1 || after.Bytes >= before.Bytes || after.Removed != 1 {
		t.Errorf("trashed %d, files %d->%d, bytes %d->%d, removed %d", trashed, before.Files, after.Files, before.Bytes, after.Bytes, after.Removed)
	}
	if again, _ := e.h.GuestUploads(ctx, b.ID, mine); len(again) != 2 {
		t.Errorf("after deleting: %d", len(again))
	}
	// once the owner takes the inbox tag off, the photo is theirs: the guest is told to ask
	for id, names := range e.f.assetTags {
		for _, n := range names {
			if n == "inbox/Undo" {
				e.f.assetTags[id] = nil
			}
		}
	}
	if err := e.h.DeleteGuestUpload(ctx, b.ID, mine, list[1].ID); !errors.Is(err, hub.ErrDropKept) {
		t.Errorf("a filed photo: %v", err)
	}
	// a box that does not let guests delete records nothing and refuses
	e.h.ReceiveDropFileFor(ctx, plain.ID, "p.jpg", bytes.NewReader(jpeg), "m", mine)
	if _, err := e.h.GuestUploads(ctx, plain.ID, mine); err == nil {
		t.Error("an ordinary box must not list guests' files")
	}
	if err := e.h.DeleteGuestUpload(ctx, plain.ID, mine, "x"); !errors.Is(err, hub.ErrDropDeleteOff) {
		t.Errorf("deleting where it is off: %v", err)
	}
}

func TestAGuestTakesBackAFileThatStillWaitsForApproval(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	e.h.StageDir = t.TempDir()
	b := newBox(t, e, hub.DropParams{Name: "Wait", Approve: true, GuestDelete: true})
	secret := hub.NewGuestSecret()
	e.h.ReceiveDropFileFor(ctx, b.ID, "a.jpg", bytes.NewReader(jpeg), "m", secret)
	e.h.ReceiveDropFileFor(ctx, b.ID, "b.jpg", bytes.NewReader(jpeg), "m", secret)
	list, _ := e.h.GuestUploads(ctx, b.ID, secret)
	if len(list) != 2 || !list[0].Waiting {
		t.Fatalf("files: %+v", list)
	}
	pend, _, _ := e.h.DropPending(ctx, bobID, b.ID, 10)
	if err := e.h.DeleteGuestUpload(ctx, b.ID, secret, list[0].ID); err != nil {
		t.Fatal(err)
	}
	left, n, _ := e.h.DropPending(ctx, bobID, b.ID, 10)
	if n != 1 || len(left) != 1 {
		t.Errorf("one must be gone from the queue: %d", n)
	}
	for _, p := range pend {
		if p.ID != left[0].ID {
			if _, err := os.Stat(p.Path); !os.IsNotExist(err) {
				t.Errorf("the file must leave the disk: %v", err)
			}
		}
	}
	if len(e.f.uploads) != 0 {
		t.Error("nothing was ever stored")
	}
	// approving turns the other record into a file in the library, which the guest can still take back
	e.h.DecideDrop(ctx, bob, b.ID, nil, true)
	now, _ := e.h.GuestUploads(ctx, b.ID, secret)
	if len(now) != 1 || now[0].Waiting {
		t.Fatalf("after approval: %+v", now)
	}
	if err := e.h.DeleteGuestUpload(ctx, b.ID, secret, now[0].ID); err != nil {
		t.Errorf("taking back an approved file: %v", err)
	}
}

// --- the shot list, the guest book and sharing back ---

func TestAShotListTagsPhotosByPromptAndCountsWhatIsCovered(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Hunt", Prompts: []string{"First dance", " The oldest guest ", "", "The best hat"}, PromptRequired: true})
	if len(b.Prompts) != 3 || b.Prompts[0].ID != "p1" || b.Prompts[1].Text != "The oldest guest" || !b.PromptRequired {
		t.Fatalf("prompts: %+v", b.Prompts)
	}
	// a photo must say what it is for, and what it says must be on the list
	for _, prompt := range []string{"", "p9"} {
		if _, err := e.h.ReceiveDropFileWith(ctx, b.ID, "a.jpg", bytes.NewReader(jpeg), "m", hub.DropMeta{Prompt: prompt}); !errors.Is(err, hub.ErrDropPrompt) {
			t.Errorf("prompt %q: %v", prompt, err)
		}
	}
	if got, _ := e.st.DropBox(ctx, b.ID); got.Files != 0 {
		t.Errorf("a refused photo uses no room: %+v", got)
	}
	if _, err := e.h.ReceiveDropFileWith(ctx, b.ID, "a.jpg", bytes.NewReader(jpeg), "m", hub.DropMeta{Prompt: "p1"}); err != nil {
		t.Fatal(err)
	}
	e.h.ReceiveDropFileWith(ctx, b.ID, "b.jpg", bytes.NewReader(jpeg), "m", hub.DropMeta{Prompt: "p1"})
	if !has(e.f.tagNames, "inbox/Hunt") || !has(e.f.tagNames, "inbox/Hunt/First dance") {
		t.Errorf("tags: %v", e.f.tagNames)
	}
	got, _ := e.st.DropBox(ctx, b.ID)
	var covered []string
	for _, ps := range e.h.PromptStatuses(ctx, got) {
		covered = append(covered, fmt.Sprint(ps.Text, "=", ps.Files))
	}
	if strings.Join(covered, ",") != "First dance=2,The oldest guest=0,The best hat=0" {
		t.Errorf("what is covered: %v", covered)
	}

	// editing keeps the ids of prompts that stay, gives new ones new ids, and a list that is not allowed changes nothing
	if err := e.h.SetDropExtras(ctx, bob, b.ID, []string{"The best hat", "first dance", "Sunset"}, false, false); err != nil {
		t.Fatal(err)
	}
	got, _ = e.st.DropBox(ctx, b.ID)
	if len(got.Prompts) != 3 || got.Prompts[0].ID != "p3" || got.Prompts[1].ID != "p1" || got.Prompts[2].ID != "p4" || got.PromptRequired {
		t.Errorf("after editing: %+v", got.Prompts)
	}
	if c, _ := e.st.PromptCounts(ctx, b.ID); c["p1"] != 2 {
		t.Errorf("a prompt that stayed keeps its count: %v", c)
	}
	long := strings.Repeat("x", 61)
	tooMany := make([]string, 21)
	for i := range tooMany {
		tooMany[i] = fmt.Sprint("prompt ", i)
	}
	for _, list := range [][]string{{"a", "A"}, {"a/b"}, {"a,b"}, {long}, tooMany} {
		if err := e.h.SetDropExtras(ctx, bob, b.ID, list, false, false); !errors.Is(err, hub.ErrBadPrompts) {
			t.Errorf("%v: %v", list, err)
		}
	}
	if _, err := e.h.CreateDropBox(ctx, e.sess, bob, hub.DropParams{Name: "Bad", Prompts: []string{"a", "a"}}); !errors.Is(err, hub.ErrBadPrompts) {
		t.Errorf("a box with a bad list: %v", err)
	}
	// with no list nothing is required, and a prompt a guest names that is not there is ignored
	if _, err := e.h.ReceiveDropFileWith(ctx, newBox(t, e, hub.DropParams{Name: "Plain"}).ID, "a.jpg", bytes.NewReader(jpeg), "m", hub.DropMeta{Prompt: "zzz"}); err != nil {
		t.Errorf("no list: %v", err)
	}
	e.h.AddDropHost(ctx, e.sess, bob, b.ID, "ann@x.test", store.HostView)
	if err := e.h.SetDropExtras(ctx, ann, b.ID, []string{"x"}, false, false); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a viewer editing the list: %v", err)
	}
}

func TestAPromptSurvivesApprovalAndATakeBack(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	e.h.StageDir = t.TempDir()
	b := newBox(t, e, hub.DropParams{Name: "Careful hunt", Approve: true, GuestDelete: true, Prompts: []string{"The cake"}})
	secret := hub.NewGuestSecret()
	e.h.ReceiveDropFileWith(ctx, b.ID, "a.jpg", bytes.NewReader(jpeg), "m", hub.DropMeta{Prompt: "p1", Secret: secret})
	e.h.ReceiveDropFileWith(ctx, b.ID, "b.jpg", bytes.NewReader(jpeg), "m", hub.DropMeta{Prompt: "p1", Secret: secret})
	if c, _ := e.st.PromptCounts(ctx, b.ID); c["p1"] != 0 {
		t.Errorf("a waiting photo is not counted yet: %v", c)
	}
	e.h.DecideDrop(ctx, bob, b.ID, nil, true)
	if c, _ := e.st.PromptCounts(ctx, b.ID); c["p1"] != 2 {
		t.Errorf("approved photos are counted: %v", c)
	}
	if !has(e.f.tagNames, "inbox/Careful hunt/The cake") {
		t.Errorf("approved photos get the prompt's tag: %v", e.f.tagNames)
	}
	list, _ := e.h.GuestUploads(ctx, b.ID, secret)
	if err := e.h.DeleteGuestUpload(ctx, b.ID, secret, list[0].ID); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.st.PromptCounts(ctx, b.ID); c["p1"] != 1 {
		t.Errorf("a photo taken back is not counted: %v", c)
	}
}

func TestMessagesToTheHostAreKeptWithAVoiceNoteForTheHostsOnly(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	closed := newBox(t, e, hub.DropParams{Name: "No book"})
	if err := e.h.LeaveMessage(ctx, closed, "m", "Eve", "Hello", nil); !errors.Is(err, hub.ErrGuestBookOff) {
		t.Errorf("a box that takes no messages: %v", err)
	}
	b := newBox(t, e, hub.DropParams{Name: "Book", GuestBook: true})
	webm := append([]byte{0x1A, 0x45, 0xDF, 0xA3}, make([]byte, 500)...)
	if err := e.h.LeaveMessage(ctx, b, "m1", " Eve ", " Congratulations! ", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.h.LeaveMessage(ctx, b, "m2", "Zoe", "", webm); err != nil {
		t.Fatalf("a voice note alone: %v", err)
	}
	if err := e.h.LeaveMessage(ctx, b, "m3", "Bo", "Words and voice", webm); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		name, text string
		voice      []byte
		want       error
	}{
		"nothing":      {"Eve", "", nil, hub.ErrBadMessage},
		"no name":      {"", "Hello", nil, hub.ErrMessageNeedsName},
		"blank name":   {"   ", "Hello", nil, hub.ErrMessageNeedsName},
		"long text":    {"Eve", strings.Repeat("x", 1001), nil, hub.ErrBadMessage},
		"long name":    {strings.Repeat("n", 61), "hi", nil, hub.ErrBadMessage},
		"a page":       {"Eve", "", page, hub.ErrBadVoice},
		"a huge voice": {"Eve", "", append([]byte{0x1A, 0x45, 0xDF, 0xA3}, make([]byte, 3<<20)...), hub.ErrBadVoice},
	} {
		if err := e.h.LeaveMessage(ctx, b, "m", c.name, c.text, c.voice); !errors.Is(err, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	msgs, err := e.h.GuestBook(ctx, bobID, b.ID)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("the messages: %d %v", len(msgs), err)
	}
	if msgs[2].Name != "Eve" || msgs[2].Text != "Congratulations!" || msgs[2].HasAudio || !msgs[0].HasAudio {
		t.Errorf("messages: %+v", msgs)
	}
	// nobody but the hosts can read it, and a viewer cannot either
	if _, err := e.h.GuestBook(ctx, annID, b.ID); !errors.Is(err, hub.ErrNoDropBox) {
		t.Errorf("a stranger reads it: %v", err)
	}
	e.h.AddDropHost(ctx, e.sess, bob, b.ID, "ann@x.test", store.HostView)
	if _, err := e.h.GuestBook(ctx, annID, b.ID); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a viewer reads it: %v", err)
	}
	if _, _, err := e.h.GuestBookAudio(ctx, annID, msgs[0].ID); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a viewer plays a voice note: %v", err)
	}
	kind, data, err := e.h.GuestBookAudio(ctx, bobID, msgs[0].ID)
	if err != nil || kind != "audio/webm" || len(data) != len(webm) {
		t.Errorf("the voice note: %s %d %v", kind, len(data), err)
	}
	if _, _, err := e.h.GuestBookAudio(ctx, bobID, msgs[2].ID); err == nil {
		t.Error("a message without a voice note has none to play")
	}
	// the export is a zip with the messages and the voice notes
	var buf bytes.Buffer
	if err := e.h.ExportGuestBook(ctx, bobID, b.ID, &buf); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
	}
	if len(files) != 3 || !strings.Contains(files["messages.txt"], "Eve") || !strings.Contains(files["messages.txt"], "Congratulations!") || !strings.Contains(files["messages.txt"], "voice-001.webm") {
		t.Errorf("export: %v", files)
	}
	if strings.Index(files["messages.txt"], "Eve") > strings.Index(files["messages.txt"], "Bo") {
		t.Error("the export reads oldest first, like a guest book")
	}
	// removing a message removes its voice note
	if err := e.h.DeleteGuestMessage(ctx, bob, msgs[0].ID); err != nil {
		t.Fatal(err)
	}
	if after, _ := e.h.GuestBook(ctx, bobID, b.ID); len(after) != 2 {
		t.Errorf("after removing: %d", len(after))
	}
	if err := e.h.DeleteGuestMessage(ctx, ann, msgs[1].ID); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a viewer removing: %v", err)
	}
	// the book has a size
	for i := 0; i < 600; i++ {
		if err := e.h.LeaveMessage(ctx, b, "m", "Eve", "x", nil); err != nil {
			if !errors.Is(err, hub.ErrGuestBookFull) || i < 400 {
				t.Fatalf("message %d: %v", i, err)
			}
			return
		}
	}
	t.Error("the messages must have a limit")
}

func TestSharingTheResultBackWithTheGuests(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Gala night"})
	if _, err := e.h.ShareBack(ctx, e.sess, bob, b.ID, 30, "", ""); !errors.Is(err, hub.ErrNothingToShare) {
		t.Errorf("nothing to share yet: %v", err)
	}
	for i := 0; i < 3; i++ {
		e.h.ReceiveDropFile(ctx, b.ID, fmt.Sprint(i, ".jpg"), bytes.NewReader(jpeg), "m")
	}
	sh, err := e.h.ShareBack(ctx, e.sess, bob, b.ID, 14, "swordfish", "Thank you all!")
	if err != nil {
		t.Fatal(err)
	}
	// the album is named after the box and holds what arrived; the link is view-only with an end, a password and the thanks
	if len(e.f.albums) != 1 || e.f.albums[0]["albumName"] != "Gala night" || len(e.f.albumAdds) != 3 {
		t.Errorf("albums %v, adds %v", e.f.albums, e.f.albumAdds)
	}
	var link map[string]any
	for _, l := range e.f.links {
		link = l
	}
	if link["type"] != "ALBUM" || link["allowDownload"] != true || link["allowUpload"] != false || link["password"] != "swordfish" || link["description"] != "Thank you all!" || link["expiresAt"] == nil {
		t.Errorf("link: %v", link)
	}
	if got, ok := e.h.DropShareFor(ctx, b.ID); !ok || got.Key != sh.Key || !strings.HasPrefix(got.Key, "key-") || got.Message != "Thank you all!" {
		t.Errorf("the box's share: %+v %v", got, ok)
	}
	// sharing again adds what came since, and replaces the link
	e.h.ReceiveDropFile(ctx, b.ID, "late.jpg", bytes.NewReader(jpeg), "m")
	sh2, err := e.h.ShareBack(ctx, e.sess, bob, b.ID, 30, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if sh2.Key == sh.Key || len(e.f.links) != 1 || len(e.f.removedLinks) != 1 || len(e.f.albums) != 1 {
		t.Errorf("replaced: links %d removed %d albums %d", len(e.f.links), len(e.f.removedLinks), len(e.f.albums))
	}
	// only the owner may, and the limits are checked
	e.h.AddDropHost(ctx, e.sess, bob, b.ID, "ann@x.test", store.HostManage)
	if _, err := e.h.ShareBack(ctx, e.ann, ann, b.ID, 30, "", ""); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a co-host sharing: %v", err)
	}
	for _, c := range []struct {
		days int
		pw   string
		msg  string
	}{{0, "", ""}, {366, "", ""}, {5, strings.Repeat("p", 65), ""}, {5, "", strings.Repeat("m", 301)}} {
		if _, err := e.h.ShareBack(ctx, e.sess, bob, b.ID, c.days, c.pw, c.msg); !errors.Is(err, hub.ErrBadShare) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	// stopping ends the link and leaves the photos
	if err := e.h.UnshareBack(ctx, e.sess, bob, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.h.DropShareFor(ctx, b.ID); ok || len(e.f.links) != 0 {
		t.Error("the share must be gone")
	}
	if err := e.h.UnshareBack(ctx, e.sess, bob, b.ID); !errors.Is(err, hub.ErrNoShare) {
		t.Errorf("stopping twice: %v", err)
	}
	if len(e.f.trashed) != 0 {
		t.Error("stopping must not touch the photos")
	}
}

// --- the wall, the best-of and handing the set onward ---

func TestTheWallShowsWhatArrivesToAScreenWithNoSignIn(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	e.f.thumbs = map[string][]byte{"": jpeg}
	b := newBox(t, e, hub.DropParams{Name: "Wall", Prompts: []string{"The cake"}, GuestBook: true, GuestDelete: true})
	if _, err := e.h.WallBox(ctx, strings.Repeat("a", 64)); err == nil {
		t.Fatal("a wall nobody started shows nothing")
	}
	secret := hub.NewGuestSecret()
	for i, n := range []string{"a.jpg", "b.jpg", "c.jpg"} {
		prompt := ""
		if i == 1 {
			prompt = "p1"
		}
		if _, err := e.h.ReceiveDropFileWith(ctx, b.ID, n, bytes.NewReader(jpeg), "m", hub.DropMeta{Prompt: prompt, Secret: secret}); err != nil {
			t.Fatal(err)
		}
	}

	// only the owner starts it, and the key is given the one permission it needs to show pictures
	e.h.AddDropHost(ctx, e.sess, bob, b.ID, "ann@x.test", store.HostManage)
	if _, err := e.h.StartWall(ctx, e.ann, ann, b.ID, false); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a co-host starting the wall: %v", err)
	}
	token, err := e.h.StartWall(ctx, e.sess, bob, b.ID, false)
	if err != nil || len(token) < 64 {
		t.Fatalf("start: %q %v", token, err)
	}
	if !has(e.f.keyPerms[b.KeyID.String()], "asset.view") {
		t.Errorf("the key must be allowed to fetch thumbnails: %v", e.f.keyPerms[b.KeyID.String()])
	}
	if again, _ := e.h.StartWall(ctx, e.sess, bob, b.ID, false); again != token {
		t.Error("starting twice keeps the link")
	}
	wb, err := e.h.WallBox(ctx, token)
	if err != nil || wb.ID != b.ID {
		t.Fatalf("the wall's box: %v", err)
	}

	// a screen with none yet gets the latest, oldest first; one that has some gets only what is newer
	items, _, err := e.h.WallFeed(ctx, wb, 0)
	if err != nil || len(items) != 3 || items[1].Caption != "The cake" || items[0].Caption != "" || items[0].ID >= items[2].ID {
		t.Fatalf("first feed: %+v %v", items, err)
	}
	e.h.ReceiveDropFile(ctx, b.ID, "d.jpg", bytes.NewReader(jpeg), "m")
	newer, _, _ := e.h.WallFeed(ctx, wb, items[2].ID)
	if len(newer) != 1 || newer[0].ID <= items[2].ID {
		t.Errorf("what is newer: %+v", newer)
	}
	if none, _, _ := e.h.WallFeed(ctx, wb, newer[0].ID); len(none) != 0 {
		t.Errorf("nothing newer: %+v", none)
	}
	// the picture comes by the photo's place in the box's list, with the box's own key
	data, kind, err := e.h.WallImage(ctx, wb, items[0].ID)
	if err != nil || kind != "image/jpeg" || len(data) != len(jpeg) {
		t.Errorf("picture: %s %d %v", kind, len(data), err)
	}
	if _, _, err := e.h.WallImage(ctx, wb, 9999); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("a photo that is not in the box: %v", err)
	}
	other := newBox(t, e, hub.DropParams{Name: "Other wall"})
	e.h.ReceiveDropFile(ctx, other.ID, "o.jpg", bytes.NewReader(jpeg), "m")
	otherAssets, _ := e.st.DropAssetsLatest(ctx, other.ID, 1)
	if _, _, err := e.h.WallImage(ctx, wb, otherAssets[0].ID); !errors.Is(err, hub.ErrDropGone) {
		t.Errorf("another box's photo through this wall: %v", err)
	}

	// a photo a guest takes back leaves the wall
	list, _ := e.h.GuestUploads(ctx, b.ID, secret)
	if err := e.h.DeleteGuestUpload(ctx, b.ID, secret, list[len(list)-1].ID); err != nil {
		t.Fatal(err)
	}
	if after, _, _ := e.h.WallFeed(ctx, wb, 0); len(after) != 3 {
		t.Errorf("after a take-back: %d photos", len(after))
	}

	// messages are shown only when the owner says so
	e.h.LeaveMessage(ctx, wb, "m", "Eve", "Cheers!", nil)
	msgs, _ := e.h.GuestBook(ctx, bobID, b.ID)
	if _, m, _ := e.h.WallFeed(ctx, wb, 0); len(m) != 0 {
		t.Error("a message is not on the wall until chosen")
	}
	if err := e.h.SetMessageOnWall(ctx, bob, msgs[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if _, m, _ := e.h.WallFeed(ctx, wb, 0); len(m) != 1 || m[0].Text != "Cheers!" || m[0].Name != "Eve" {
		t.Errorf("a chosen message: %+v", m)
	}
	e.h.SetMessageOnWall(ctx, bob, msgs[0].ID, false)
	if _, m, _ := e.h.WallFeed(ctx, wb, 0); len(m) != 0 {
		t.Error("a message taken off the wall")
	}

	// a new link ends the old one; stopping ends the wall and takes the permission back
	token2, err := e.h.StartWall(ctx, e.sess, bob, b.ID, true)
	if err != nil || token2 == token {
		t.Fatalf("renew: %v", err)
	}
	if _, err := e.h.WallBox(ctx, token); err == nil {
		t.Error("the old wall link must stop")
	}
	if err := e.h.StopWall(ctx, e.sess, bob, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.WallBox(ctx, token2); err == nil {
		t.Error("a stopped wall shows nothing")
	}
	if has(e.f.keyPerms[b.KeyID.String()], "asset.view") {
		t.Error("stopping the wall must take the permission off the key")
	}
	if got, _ := e.st.DropBox(ctx, b.ID); got.Wall != "" {
		t.Error("the link is gone")
	}
}

// pictures for the best-of: a scene full of detail, a lighter copy of it, a blurred one, a dark one, a glaring one, another scene
func synth(kind string) []byte {
	var img image.Image
	switch kind {
	case "sharp":
		img = noiseImage(1, 40, 220)
	case "sharp-copy":
		img = noiseImage(1, 46, 226)
	case "scene":
		img = noiseImage(9, 40, 220)
	case "dark":
		img = noiseImage(2, 0, 30)
	case "bright":
		img = noiseImage(3, 235, 255)
	default: // blurred
		g := image.NewRGBA(image.Rect(0, 0, 400, 300))
		for y := 0; y < 300; y++ {
			for x := 0; x < 400; x++ {
				v := uint8(80 + x*80/400)
				g.Set(x, y, color.RGBA{v, v, v, 255})
			}
		}
		img = g
	}
	var b bytes.Buffer
	imgjpeg.Encode(&b, img, nil)
	return b.Bytes()
}

func noiseImage(seed uint32, lo, hi uint8) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	state := seed*2654435761 + 1
	for y := 0; y < 300; y += 3 {
		for x := 0; x < 400; x += 3 {
			state = state*1664525 + 1013904223
			v := lo + uint8((state>>16)%uint32(hi-lo+1))
			for dy := 0; dy < 3; dy++ {
				for dx := 0; dx < 3; dx++ {
					img.Set(x+dx, y+dy, color.RGBA{v, v, v, 255})
				}
			}
		}
	}
	return img
}

func idOfName(e *env, name string) string {
	for id, n := range e.f.assetName {
		if n == name {
			return id
		}
	}
	return ""
}

func waitHighlight(t *testing.T, e *env, boxID string) store.DropHighlight {
	t.Helper()
	for i := 0; i < 200; i++ {
		if h, err := e.st.DropHighlightOf(context.Background(), boxID); err == nil && h.Status != store.HighlightRunning {
			return h
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("the best-of did not finish")
	return store.DropHighlight{}
}

func TestTheBestOfLeavesOutTheDarkTheBlurredAndTheRepeatsAndSaysWhy(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	e.f.thumbs = map[string][]byte{"sharp1": synth("sharp"), "sharp2": synth("sharp-copy"), "scene": synth("scene"), "blur": synth("blur"), "dark": synth("dark"), "bright": synth("bright")}
	b := newBox(t, e, hub.DropParams{Name: "Gala"})
	for _, n := range []string{"sharp1.jpg", "sharp2.jpg", "scene.jpg", "blur.jpg", "dark.jpg", "bright.jpg"} {
		if _, err := e.h.ReceiveDropFile(ctx, b.ID, n, bytes.NewReader(jpeg), "m"); err != nil {
			t.Fatal(err)
		}
	}
	e.h.AddDropHost(ctx, e.sess, bob, b.ID, "ann@x.test", store.HostManage)
	if err := e.h.StartHighlights(ctx, e.ann, ann, b.ID, "normal"); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a co-host making a best-of: %v", err)
	}
	if err := e.h.StartHighlights(ctx, e.sess, bob, b.ID, "normal"); err != nil {
		t.Fatal(err)
	}
	h := waitHighlight(t, e, b.ID)
	if h.Status != store.HighlightDone || h.Total != 6 || h.Kept != 2 || h.Album != "Gala best of" || h.Strictness != "normal" {
		t.Fatalf("the best-of: %+v", h)
	}
	why := map[string]string{}
	kept := map[string]bool{}
	all, _ := e.st.DropVerdicts(ctx, b.ID, true, 100)
	for _, v := range all {
		why[v.Name] = v.Verdict
	}
	ks, _ := e.st.DropVerdicts(ctx, b.ID, false, 100)
	for _, v := range ks {
		kept[v.Name] = true
	}
	if why["blur.jpg"] != hub.VerdictBlurry || why["dark.jpg"] != hub.VerdictDark || why["bright.jpg"] != hub.VerdictBright || len(why) != 4 {
		t.Errorf("left out: %v", why)
	}
	if len(kept) != 2 || !kept["scene.jpg"] || (kept["sharp1.jpg"] == kept["sharp2.jpg"]) {
		t.Errorf("kept: %v, left out: %v", kept, why)
	}
	for n, v := range why {
		if v == hub.VerdictDuplicate && !strings.HasPrefix(n, "sharp") {
			t.Errorf("the repeat must be one of the two sharp shots: %s", n)
		}
	}
	// the album holds exactly the kept photos; nothing was deleted
	if len(e.f.albums) != 1 || e.f.albums[0]["albumName"] != "Gala best of" {
		t.Fatalf("albums: %v", e.f.albums)
	}
	albumID := e.f.albums[0]["id"].(string)
	if len(e.f.members[albumID]) != 2 || !e.f.members[albumID][idOfName(e, "scene.jpg")] {
		t.Errorf("the album: %v", e.f.members[albumID])
	}
	if len(e.f.trashed) != 0 {
		t.Errorf("a best-of must never delete: %v", e.f.trashed)
	}

	// running it again corrects the album: what is no longer kept goes, and what the owner put in it themselves stays
	foreign := uuid.NewString()
	e.f.members[albumID][foreign] = true
	e.f.members[albumID][idOfName(e, "dark.jpg")] = true
	if err := e.h.StartHighlights(ctx, e.sess, bob, b.ID, "strict"); err != nil {
		t.Fatal(err)
	}
	again := waitHighlight(t, e, b.ID)
	if again.Strictness != "strict" || again.Status != store.HighlightDone {
		t.Fatalf("again: %+v", again)
	}
	if e.f.members[albumID][idOfName(e, "dark.jpg")] || !e.f.members[albumID][foreign] || len(e.f.members[albumID]) != 3 {
		t.Errorf("the corrected album: %v", e.f.members[albumID])
	}
}

func TestABestOfOfNothingSaysSo(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	b := newBox(t, e, hub.DropParams{Name: "Empty"})
	if err := e.h.StartHighlights(ctx, e.sess, bob, b.ID, ""); err != nil {
		t.Fatal(err)
	}
	h := waitHighlight(t, e, b.ID)
	if h.Status != store.HighlightFailed || !strings.Contains(h.Note, "no photos") || h.Strictness != "normal" {
		t.Errorf("an empty box: %+v", h)
	}
}

func TestAClosedBoxHandsItsPhotosOnThroughTheHandOverThatExists(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	if err := e.h.Join(ctx, e.sess, bob, "family", "", "copy"); err != nil {
		t.Fatal(err)
	}
	if opts := e.h.OnwardOptions(ctx, bobID); len(opts) != 1 || opts[0].Service != "family" || !opts[0].Copy {
		t.Fatalf("services to hand on to: %+v", opts)
	}
	if opts := e.h.OnwardOptions(ctx, annID); len(opts) != 0 {
		t.Errorf("a person who sends to nothing: %+v", opts)
	}
	b := newBox(t, e, hub.DropParams{Name: "Onward"})
	for _, n := range []string{"a.jpg", "b.jpg", "c.jpg"} {
		e.h.ReceiveDropFile(ctx, b.ID, n, bytes.NewReader(jpeg), "m")
	}
	if _, err := e.h.HandOnward(ctx, bob, b.ID, "all"); !errors.Is(err, hub.ErrNoOnward) {
		t.Errorf("no service chosen: %v", err)
	}
	if err := e.h.SetDropOnward(ctx, bob, b.ID, "nowhere", false); !errors.Is(err, hub.ErrOnwardService) {
		t.Errorf("a service the person does not send to: %v", err)
	}
	e.h.AddDropHost(ctx, e.sess, bob, b.ID, "ann@x.test", store.HostManage)
	if err := e.h.SetDropOnward(ctx, ann, b.ID, "family", false); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a co-host choosing: %v", err)
	}
	if err := e.h.SetDropOnward(ctx, bob, b.ID, "family", false); err != nil {
		t.Fatal(err)
	}

	// everything: the photos get the service's tag, which is what hands them over, and a pass is woken
	before := len(e.f.tagged)
	n, err := e.h.HandOnward(ctx, bob, b.ID, "all")
	if err != nil || n != 3 || len(e.f.tagged)-before != 3 {
		t.Fatalf("hand on: %d %v, tagged %d", n, err, len(e.f.tagged)-before)
	}
	if !has(e.woken, "a drop box handed on to family") {
		t.Errorf("a pass must be woken: %v", e.woken)
	}
	if log, _ := e.st.DropOnwardLog(ctx, b.ID, 5); len(log) != 1 || log[0].Service != "family" || log[0].Count != 3 || log[0].Scope != "all" || log[0].By != "Bob" {
		t.Errorf("the log: %+v", log)
	}
	// the best-of needs one first
	if _, err := e.h.HandOnward(ctx, bob, b.ID, "best"); !errors.Is(err, hub.ErrNoHighlights) {
		t.Errorf("best-of with none: %v", err)
	}
	if _, err := e.h.HandOnward(ctx, bob, b.ID, "some"); !errors.Is(err, hub.ErrBadScope) {
		t.Errorf("an unknown scope: %v", err)
	}
	ids := []string{}
	for id := range e.f.assetName {
		ids = append(ids, id)
	}
	e.st.ReplaceDropVerdicts(ctx, b.ID, []store.Verdict{{AssetID: ids[0], Verdict: "kept", Name: "a"}, {AssetID: ids[1], Verdict: "kept", Name: "b"}, {AssetID: ids[2], Verdict: "blurry", Name: "c"}})
	before = len(e.f.tagged)
	if n, err := e.h.HandOnward(ctx, bob, b.ID, "best"); err != nil || n != 2 || len(e.f.tagged)-before != 2 {
		t.Errorf("the best-of: %d %v, tagged %d", n, err, len(e.f.tagged)-before)
	}
	if _, err := e.h.HandOnward(ctx, ann, b.ID, "all"); !errors.Is(err, hub.ErrDropRole) {
		t.Errorf("a co-host handing on: %v", err)
	}
}

func TestABoxThatHandsOnByItselfDoesItOnceWhenItClosesAndTheSummaryTellsHow(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	e.h.Join(ctx, e.sess, bob, "family", "", "copy")
	b := newBox(t, e, hub.DropParams{Name: "Auto", Closes: time.Now().Add(time.Hour)})
	for _, n := range []string{"a.jpg", "b.jpg"} {
		e.h.ReceiveDropFile(ctx, b.ID, n, bytes.NewReader(jpeg), "m")
	}
	if err := e.h.SetDropOnward(ctx, bob, b.ID, "family", true); err != nil {
		t.Fatal(err)
	}
	var told []string
	e.h.Notify = func(m notify.Message) { told = append(told, m.Text) }
	e.h.DropMaintenance(ctx) // still open: nothing is handed on
	if log, _ := e.st.DropOnwardLog(ctx, b.ID, 5); len(log) != 0 {
		t.Fatal("an open box hands nothing on")
	}
	e.st.SetDropSchedule(ctx, b.ID, time.Time{}, time.Now().Add(-time.Minute)) // it closes
	before := len(e.f.tagged)
	e.h.DropMaintenance(ctx)
	e.h.DropMaintenance(ctx)
	if len(e.f.tagged)-before != 2 {
		t.Errorf("handed on %d photos, want 2, once", len(e.f.tagged)-before)
	}
	if log, _ := e.st.DropOnwardLog(ctx, b.ID, 5); len(log) != 1 || log[0].Count != 2 {
		t.Errorf("the log: %+v", log)
	}
	closing := ""
	for _, m := range told {
		if strings.Contains(m, "closed") {
			closing = m
		}
	}
	if !strings.Contains(closing, "Handed 2 photos on to family") {
		t.Errorf("the summary must say it: %v", told)
	}
	ev, _ := e.h.DropEvents(ctx, bobID, b.ID, 20)
	said := false
	for _, x := range ev {
		said = said || (x.Kind == store.DropExpired && strings.Contains(x.Text, "Handed 2 photos on to family"))
	}
	if !said {
		t.Error("the box's log must say it too")
	}
	// a service the owner has left cannot receive: the summary says so instead of failing silently
	c := newBox(t, e, hub.DropParams{Name: "Orphan", Closes: time.Now().Add(time.Hour)})
	e.h.ReceiveDropFile(ctx, c.ID, "a.jpg", bytes.NewReader(jpeg), "m")
	e.h.SetDropOnward(ctx, bob, c.ID, "family", true)
	e.h.Leave(ctx, e.sess, bob, "family")
	e.st.SetDropSchedule(ctx, c.ID, time.Time{}, time.Now().Add(-time.Minute))
	told = nil
	e.h.DropMaintenance(ctx)
	failed := false
	for _, m := range told {
		failed = failed || strings.Contains(m, "Could not hand the photos on to family")
	}
	if !failed {
		t.Errorf("a failed hand-on must be in the summary: %v", told)
	}
}

// What a password guards is chosen when the box is made. With a password and no choice it guards the upload, as it
// always did; a choice without a password is refused; and the token that opens a box guarding its whole page stops
// working when the password changes.
func TestWhatAPasswordGuardsIsChosenWhenTheBoxIsMade(t *testing.T) {
	e := setup(t, true)
	for name, c := range map[string]struct {
		scope, password, want string
		err                   error
	}{
		"password only":       {"", "swordfish", store.DropScopeUpload, nil},
		"upload":              {"upload", "swordfish", store.DropScopeUpload, nil},
		"the whole page":      {"page", "swordfish", store.DropScopePage, nil},
		"no password":         {"", "", "", nil},
		"a choice, no word":   {"page", "", "", hub.ErrDropNeedsPassword},
		"a choice nobody has": {"sideways", "swordfish", "", hub.ErrBadDropSecret},
	} {
		b, err := e.h.CreateDropBox(context.Background(), e.sess, bob, hub.DropParams{Name: "Box " + strings.ReplaceAll(name, ",", ""), Password: c.password, PasswordScope: c.scope})
		if !errors.Is(err, c.err) {
			t.Errorf("%s: %v, want %v", name, err, c.err)
			continue
		}
		if err == nil && c.password != "" && b.PasswordScope != c.want {
			t.Errorf("%s: scope %q, want %q", name, b.PasswordScope, c.want)
		}
	}

	page := newBox(t, e, hub.DropParams{Name: "Hidden", Password: "swordfish", PasswordScope: "page"})
	upload := newBox(t, e, hub.DropParams{Name: "Visible", Password: "swordfish", PasswordScope: "upload"})
	token := hub.DropUnlockToken(page)
	if !hub.DropPageLocked(page, "") || !hub.DropPageLocked(page, "made-up") || hub.DropPageLocked(page, token) {
		t.Error("a page-wide password keeps out a guest with no token or a made-up one, and lets in the right one")
	}
	if hub.DropPageLocked(upload, "") {
		t.Error("a password on the upload does not hide the page")
	}
	changed := page
	changed.Password, _ = hub.HashDropPassword("another")
	if !hub.DropPageLocked(changed, token) {
		t.Error("the token of the old password must not open the box with a new one")
	}
	if tok := hub.DropUnlockToken(upload); tok == token {
		t.Error("two boxes must not share a token")
	}
}
