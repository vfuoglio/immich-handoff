package syncer

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/immich"
)

// A pass asked for while a tidy is running is not lost: the tidy starts it when it is done. (The timer's tidy holds
// the same lock as a pass; a webhook that arrived meanwhile left a note that only a pass used to read.)
func TestAPassAskedForDuringATidyRunsWhenTheTidyEnds(t *testing.T) {
	var once sync.Once
	inTidy, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			once.Do(func() { close(inTidy) })
			<-release // the tidy is looking at the receiver's tags
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
	}))
	defer srv.Close()
	api, err := immich.New(srv.URL, immich.Auth{APIKey: "k"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Name: "s", Tag: "share", InboxTag: "inbox", ExpiryDays: 30, Receivers: []*Account{{Name: "r", API: api}}}
	s := &Syncer{Services: []*Service{svc}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := context.Background()

	done := make(chan struct{})
	go func() { s.Tidy(ctx); close(done) }()
	<-inTidy
	s.Run(ctx, "webhook", false) // the lock is taken: it can only leave its note
	close(release)
	<-done

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if l := s.Last(); l != nil && l.Reason == "after a tidy" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the pass asked for during the tidy never ran")
}
