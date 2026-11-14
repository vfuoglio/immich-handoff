package hub_test

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
)

func phoneEnv(t *testing.T) *env {
	e := setup(t, true)
	e.h.PhoneDir = filepath.Join(t.TempDir(), "phone")
	return e
}

// send puts data in pieces of the given size, the way a phone does, and returns the last offset.
func send(t *testing.T, e *env, user uuid.UUID, id string, data []byte, piece int) {
	sendFrom(t, e, user, id, data, 0, piece)
}

func sendFrom(t *testing.T, e *env, user uuid.UUID, id string, data []byte, from, piece int) {
	t.Helper()
	for off := from; off < len(data); {
		end := min(off+piece, len(data))
		got, err := e.h.PhoneChunk(user, id, int64(off), bytes.NewReader(data[off:end]))
		if err != nil || got != int64(end) {
			t.Fatalf("piece at %d: offset %d, %v", off, got, err)
		}
		off = end
	}
}

func TestAFileSentInPiecesWithTheConnectionCutArrivesWholeAndTagged(t *testing.T) {
	e, ctx := phoneEnv(t), context.Background()
	user := bobID
	data := append(append([]byte{}, jpeg...), bytes.Repeat([]byte("x"), 5000)...)
	u, err := e.h.PhoneStart(user, "IMG 1.jpeg", int64(len(data)), "family", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// the first piece is cut off in the middle: what arrived is kept, and the phone asks where it stands
	cut := data[:1500]
	if _, err := e.h.PhoneChunk(user, u.ID, 0, &failing{r: bytes.NewReader(cut), after: 1000}); err == nil {
		t.Fatal("a cut piece is an error")
	}
	_, have, err := e.h.PhoneStatus(user, u.ID)
	if err != nil || have != 1000 {
		t.Fatalf("status: %d %v", have, err)
	}
	// a piece that does not follow is refused with where to carry on
	_, err = e.h.PhoneChunk(user, u.ID, 4000, bytes.NewReader(data[4000:4100]))
	var off *hub.PhoneOffsetError
	if !errors.As(err, &off) || off.At != 1000 || !hub.IsUserError(err) {
		t.Fatalf("a gap: %v", err)
	}
	// finishing too early says so
	if _, _, err := e.h.PhoneFinish(ctx, e.sess, user, u.ID, "share", ""); !errors.As(err, &off) {
		t.Fatalf("finish early: %v", err)
	}
	sendFrom(t, e, user, u.ID, data, 1000, 2048) // carry on from where the server stands
	if _, err = e.h.PhoneChunk(user, u.ID, 1000, bytes.NewReader(nil)); err == nil {
		t.Fatal("an old offset after the file is whole is refused")
	}
	sum := sha1.Sum(data)
	id, dup, err := e.h.PhoneFinish(ctx, e.sess, user, u.ID, "share", hex.EncodeToString(sum[:]))
	if err != nil || dup || id == "" {
		t.Fatalf("finish: %q %v %v", id, dup, err)
	}
	e.f.mu.Lock()
	up := e.f.uploads
	e.f.mu.Unlock()
	if len(up) != 1 || up[0].size != len(data) || up[0].name != "IMG 1.jpg" {
		t.Fatalf("what Immich got: %+v", up)
	}
	if len(e.f.tagged) != 1 || e.f.tagged[0] != id {
		t.Errorf("tagged: %v", e.f.tagged)
	}
	if _, err := os.Stat(filepath.Join(e.h.PhoneDir, user.String(), u.ID+".part")); !os.IsNotExist(err) {
		t.Errorf("the pieces are deleted: %v", err)
	}
	// a phone that never heard the answer asks again and gets the same one, with nothing sent twice
	id2, _, err := e.h.PhoneFinish(ctx, e.sess, user, u.ID, "share", "")
	if err != nil || id2 != id {
		t.Errorf("repeat: %q %v", id2, err)
	}
	if len(e.f.uploads) != 1 {
		t.Errorf("sent twice: %+v", e.f.uploads)
	}
}

type failing struct {
	r     *bytes.Reader
	after int
	n     int
}

func (f *failing) Read(p []byte) (int, error) {
	if f.n >= f.after {
		return 0, errors.New("connection reset")
	}
	if len(p) > f.after-f.n {
		p = p[:f.after-f.n]
	}
	n, err := f.r.Read(p)
	f.n += n
	return n, err
}

func TestLibraryOnlyIsNotTaggedAndADuplicateIsReported(t *testing.T) {
	e, ctx := phoneEnv(t), context.Background()
	u, _ := e.h.PhoneStart(bobID, "dupe.jpg", int64(len(jpeg)), "", time.Time{})
	send(t, e, bobID, u.ID, jpeg, 700)
	_, dup, err := e.h.PhoneFinish(ctx, e.sess, bobID, u.ID, "", "")
	if err != nil || !dup {
		t.Fatalf("%v %v", dup, err)
	}
	if len(e.f.tagged) != 0 {
		t.Errorf("my library only means no tag: %v", e.f.tagged)
	}
}

func TestWhatIsNotAPhotoOrVideoOrChangedOnTheWayIsRefused(t *testing.T) {
	e, ctx := phoneEnv(t), context.Background()
	page := []byte("<html><script>alert(1)</script></html>")
	u, _ := e.h.PhoneStart(bobID, "x.jpg", int64(len(page)), "family", time.Now())
	send(t, e, bobID, u.ID, page, 100)
	if _, _, err := e.h.PhoneFinish(ctx, e.sess, bobID, u.ID, "share", ""); !errors.Is(err, hub.ErrPhoneKind) || !hub.IsUserError(err) {
		t.Fatalf("a page: %v", err)
	}
	if len(e.f.uploads) != 0 {
		t.Errorf("nothing reached Immich: %+v", e.f.uploads)
	}
	u, _ = e.h.PhoneStart(bobID, "y.jpg", int64(len(jpeg)), "family", time.Now())
	send(t, e, bobID, u.ID, jpeg, 900)
	if _, _, err := e.h.PhoneFinish(ctx, e.sess, bobID, u.ID, "share", strings.Repeat("0", 40)); !errors.Is(err, hub.ErrPhoneChecksum) {
		t.Fatalf("a changed file: %v", err)
	}
	if _, have, _ := e.h.PhoneStatus(bobID, u.ID); have != 0 {
		t.Errorf("a changed file starts again: %d", have)
	}
}

func TestPiecesCannotRunPastTheFileOrBeTooBig(t *testing.T) {
	e := phoneEnv(t)
	u, _ := e.h.PhoneStart(bobID, "a.jpg", 100, "", time.Now())
	if _, err := e.h.PhoneChunk(bobID, u.ID, 0, bytes.NewReader(make([]byte, 101))); !errors.Is(err, hub.ErrPhoneChunk) {
		t.Errorf("past the end: %v", err)
	}
	if _, have, _ := e.h.PhoneStatus(bobID, u.ID); have != 0 {
		t.Errorf("what ran past is not kept: %d", have)
	}
}

func TestPhoneLimitsSayWhy(t *testing.T) {
	e := phoneEnv(t)
	e.h.PhoneCap, e.h.PhoneMaxFile = 10<<20, 6<<20
	if _, err := e.h.PhoneStart(bobID, "a", 7<<20, "", time.Time{}); !errors.Is(err, hub.ErrPhoneTooBig) || !strings.Contains(err.Error(), "6 MB") {
		t.Errorf("one file too big: %v", err)
	}
	if _, err := e.h.PhoneStart(bobID, "a", 0, "", time.Time{}); !errors.Is(err, hub.ErrPhoneEmpty) {
		t.Errorf("empty: %v", err)
	}
	a, err := e.h.PhoneStart(bobID, "a", 6<<20, "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.PhoneStart(bobID, "b", 6<<20, "", time.Time{}); !errors.Is(err, hub.ErrPhoneFull) || !strings.Contains(err.Error(), "10 MB") {
		t.Errorf("what waits is capped for each person: %v", err)
	}
	if _, err := e.h.PhoneStart(annID, "b", 6<<20, "", time.Time{}); err != nil {
		t.Errorf("another person has their own room: %v", err)
	}
	// cancelling frees the room
	if err := e.h.PhoneCancel(bobID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.PhoneStart(bobID, "b", 6<<20, "", time.Time{}); err != nil {
		t.Errorf("after a cancel: %v", err)
	}
	// somebody else's upload, and ids that are not ids
	if _, _, err := e.h.PhoneStatus(annID, a.ID); !errors.Is(err, hub.ErrPhoneNoUpload) {
		t.Errorf("another person's file: %v", err)
	}
	for _, id := range []string{"../../etc/passwd", "", strings.Repeat("g", 32)} {
		if _, _, err := e.h.PhoneStatus(bobID, id); !errors.Is(err, hub.ErrPhoneNoUpload) {
			t.Errorf("%q: %v", id, err)
		}
	}
}

func TestPhonesCannotSendWithoutTheDataFolder(t *testing.T) {
	e := setup(t, true)
	if e.h.PhoneOn() {
		t.Fatal("no folder, no phone upload")
	}
	if _, err := e.h.PhoneStart(bobID, "a", 10, "", time.Time{}); !errors.Is(err, hub.ErrPhoneOff) || !hub.IsUserError(err) {
		t.Errorf("%v", err)
	}
}

func TestUnfinishedFilesAreDeletedAfterTheKeepTimeAndFinishedAnswersAfterAnHour(t *testing.T) {
	e, ctx := phoneEnv(t), context.Background()
	a, _ := e.h.PhoneStart(bobID, "a.jpg", int64(len(jpeg)), "", time.Now())
	send(t, e, bobID, a.ID, jpeg[:500], 500)
	done, _ := e.h.PhoneStart(bobID, "b.jpg", int64(len(jpeg)), "", time.Now())
	send(t, e, bobID, done.ID, jpeg, 900)
	if _, _, err := e.h.PhoneFinish(ctx, e.sess, bobID, done.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if n := e.h.PhoneMaintenance(); n != 0 {
		t.Fatalf("nothing is old yet: %d", n)
	}
	clock := time.Now().Add(47 * time.Hour)
	e.h.Now = func() time.Time { return clock }
	if n := e.h.PhoneMaintenance(); n != 0 {
		t.Fatalf("47 hours: %d", n)
	}
	if _, _, err := e.h.PhoneStatus(bobID, done.ID); err == nil {
		t.Errorf("the answer for a finished file goes after an hour")
	}
	clock = time.Now().Add(49 * time.Hour)
	if n := e.h.PhoneMaintenance(); n != 1 {
		t.Fatalf("49 hours: %d", n)
	}
	if _, _, err := e.h.PhoneStatus(bobID, a.ID); !errors.Is(err, hub.ErrPhoneNoUpload) {
		t.Errorf("abandoned: %v", err)
	}
	if left, _ := os.ReadDir(e.h.PhoneDir); len(left) != 0 {
		t.Errorf("nothing is left, not even the person's folder: %v", left)
	}
}
