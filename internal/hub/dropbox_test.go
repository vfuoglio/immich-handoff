package hub_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
