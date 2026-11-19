package dest_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/dest"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// immichWith is a minimal Immich that serves the originals of the photos it is given.
func immichWith(t *testing.T, files map[string][]byte, slow bool) *immich.API {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/assets/{id}/original", func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.PathValue("id")]
		if !ok {
			http.Error(w, "no", 404)
			return
		}
		if slow {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done() // never sends a byte
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(b)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	api, err := immich.New(srv.URL, immich.Auth{APIKey: "k"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return api
}

func photo(name string) (immich.AssetResponseDto, string) {
	id := uuid.New()
	return immich.AssetResponseDto{Id: id, OriginalFileName: name}, id.String()
}

func TestAPhotoIsStreamedIntoTheDestinationAndHeldOnlyWhenItsOwnLookAgrees(t *testing.T) {
	a, id := photo("IMG_1.jpg")
	clip, cid := photo("IMG_1.mov")
	api := immichWith(t, map[string][]byte{id: []byte("the still"), cid: []byte("the clip")}, false)
	m := &dest.Memory{}
	r := &dest.Remote{Ident: "nas", Name: "the NAS", D: m}
	cp, err := r.Deliver(context.Background(), api, a, &clip, syncer.RemoteJob{Sender: "alice"})
	if err != nil || cp.Ref != "IMG_1.jpg" || !cp.Created || cp.Clip != "IMG_1.mov" || !cp.ClipCreated {
		t.Fatalf("%+v %v", cp, err)
	}
	if string(m.Content("IMG_1.jpg")) != "the still" || string(m.Content("IMG_1.mov")) != "the clip" {
		t.Errorf("the bytes arrived whole: %v", m.Refs())
	}
	// the same photo again is already there: held, nothing new, nothing to take back
	cp, err = r.Deliver(context.Background(), api, a, nil, syncer.RemoteJob{Sender: "alice"})
	if err != nil || cp.Created || cp.Ref != "IMG_1.jpg" || m.Puts != 2 {
		t.Fatalf("a repeat: %+v %v puts=%d", cp, err, m.Puts)
	}
	// take-back deletes the files
	if err := r.TakeBackRefs(context.Background(), []string{"IMG_1.jpg", "IMG_1.mov"}); err != nil || len(m.Refs()) != 0 {
		t.Errorf("take-back: %v %v", err, m.Refs())
	}
}

// a destination that says it stored something other than what it was sent
type liar struct {
	dest.Memory
	size int64
	sum  string
	gone bool
}

func (l *liar) Put(ctx context.Context, name string, r io.Reader, size int64) (dest.Stored, error) {
	st, err := l.Memory.Put(ctx, name, r, size)
	if l.size != 0 {
		st.Size = l.size
	}
	if l.sum != "" {
		st.Sum = l.sum
	}
	return st, err
}
func (l *liar) Stat(ctx context.Context, ref string) (dest.Info, error) {
	if l.gone {
		return dest.Info{}, dest.ErrNotFound
	}
	return l.Memory.Stat(ctx, ref)
}

func TestACopyThatTheDestinationDoesNotHoldAsSentIsNotHeld(t *testing.T) {
	a, id := photo("IMG_2.jpg")
	api := immichWith(t, map[string][]byte{id: []byte("0123456789")}, false)
	for name, l := range map[string]*liar{"a wrong size": {size: 3}, "a wrong checksum": {sum: strings.Repeat("0", 40)}, "gone when asked again": {gone: true}} {
		r := &dest.Remote{Ident: "x", Name: "x", D: l}
		if _, err := r.Deliver(context.Background(), api, a, nil, syncer.RemoteJob{}); !errors.Is(err, dest.ErrNotHeld) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a destination that is down fails the delivery with its own words, and Ready says so too
	down := &dest.Memory{Fail: errors.New("disk full")}
	r := &dest.Remote{Ident: "x", Name: "x", D: down}
	if _, err := r.Deliver(context.Background(), api, a, nil, syncer.RemoteJob{}); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("down: %v", err)
	}
	if err := r.Ready(context.Background()); err == nil {
		t.Error("not ready")
	}
	if _, err := r.Diagnose(context.Background()); err == nil {
		t.Error("diagnosis says why")
	}
}

func TestATransferThatMovesNoByteIsGivenUp(t *testing.T) {
	a, id := photo("IMG_3.jpg")
	api := immichWith(t, map[string][]byte{id: []byte("x")}, true)
	r := &dest.Remote{Ident: "x", Name: "x", D: &dest.Memory{}, Stall: 150 * time.Millisecond}
	start := time.Now()
	_, err := r.Deliver(context.Background(), api, a, nil, syncer.RemoteJob{})
	if err == nil || !strings.Contains(err.Error(), "stalled") || time.Since(start) > 5*time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}

func TestTheMemoryDestinationSuffixesADifferentFileAndSkipsTheSame(t *testing.T) {
	m := &dest.Memory{}
	put := func(name, body string) dest.Stored {
		st, err := m.Put(context.Background(), name, bytes.NewReader([]byte(body)), int64(len(body)))
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	if a := put("a/IMG.jpg", "one"); a.Ref != "a/IMG.jpg" || a.Existed {
		t.Errorf("%+v", a)
	}
	if b := put("a/IMG.jpg", "one"); !b.Existed || b.Ref != "a/IMG.jpg" {
		t.Errorf("the same: %+v", b)
	}
	if c := put("a/IMG.jpg", "two"); c.Ref != "a/IMG (2).jpg" || c.Existed {
		t.Errorf("a different one: %+v", c)
	}
}
