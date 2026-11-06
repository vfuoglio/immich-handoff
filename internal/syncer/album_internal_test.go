package syncer

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOnlyABigAlbumHandOverIsNotified(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
	}))
	defer srv.Close()
	s := &Syncer{Cfg: Config{NotifyURL: srv.URL}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	svc := &Service{Name: "Holiday"}
	snd := &Account{Name: "alice", WatchAlbum: uuid.New(), WatchAlbumName: "To send"}

	s.reportAlbum(svc, snd, bigBatch-1, 0, 0) // a small one: no message
	s.reportAlbum(svc, &Account{Name: "bob"}, 500, 0, 0)
	s.reportAlbum(svc, snd, 0, 0, 0)
	mu.Lock()
	n := len(got)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("small hand-overs, and senders without an album, must not notify: %v", got)
	}
	s.reportAlbum(svc, snd, bigBatch+5, 2, 1)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || !strings.Contains(got[0], "25 photos") || !strings.Contains(got[0], "To send") || !strings.Contains(got[0], "2 refused") || !strings.Contains(got[0], "1 failed") {
		t.Errorf("a big hand-over is reported once, with what happened: %q", got)
	}
}

func TestThePageIsToldAtMostOnceASecond(t *testing.T) {
	s := &Syncer{}
	ch, cancel := s.Subscribe()
	defer cancel()
	s.publishSoon()
	s.publishSoon() // within the same second: swallowed
	s.publishSoon()
	n := 0
	for len(ch) > 0 {
		<-ch
		n++
	}
	if n != 1 {
		t.Errorf("signals = %d, want 1", n)
	}
	s.lastPublish.Store(time.Now().Add(-2 * time.Second).UnixNano())
	s.publishSoon()
	if len(ch) != 1 {
		t.Error("after a second the next change must get through")
	}
}
