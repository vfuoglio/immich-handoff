package syncer_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/vfuoglio/immich-sync-console/internal/immich"
	"github.com/vfuoglio/immich-sync-console/internal/syncer"
)

// fakeWorkflows stores one workflow per fake server, mimicking create/search/update.
type fakeWorkflows struct {
	mu      sync.Mutex
	current map[string]any
	creates int
	updates int
}

func (f *fakeWorkflows) handler() http.Handler {
	const id = "55555555-5555-5555-5555-555555555555"
	ts := "2025-01-02T03:04:05.000Z"
	j := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	store := func(body map[string]any) map[string]any {
		out := map[string]any{"id": id, "createdAt": ts, "updatedAt": ts, "description": "", "logging": true}
		for k, v := range f.current {
			out[k] = v
		}
		for k, v := range body {
			out[k] = v
		}
		f.current = out
		return out
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		j(w, 200, []any{map[string]any{"id": shareTag, "name": "share", "value": "share", "createdAt": ts, "updatedAt": ts}})
	})
	mux.HandleFunc("GET /api/workflows", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.current == nil {
			j(w, 200, []any{})
			return
		}
		j(w, 200, []any{f.current})
	})
	mux.HandleFunc("POST /api/workflows", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.creates++
		j(w, 201, store(body))
	})
	mux.HandleFunc("PUT /api/workflows/"+id, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.updates++
		j(w, 200, store(body))
	})
	return mux
}

func provision(t *testing.T, f *fakeWorkflows, url string) int {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	src, err := immich.New(srv.URL, immich.Auth{APIKey: "src"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &syncer.Syncer{Cfg: syncer.Config{ShareTag: "share"}, Sources: []*immich.API{src}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return s.Provision(context.Background(), syncer.Webhook{URL: url, Header: "X-Webhook-Secret", Secret: "s3cret"})
}

func TestProvisionCreatesThenIsIdempotentThenRepairs(t *testing.T) {
	f := &fakeWorkflows{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	src, _ := immich.New(srv.URL, immich.Auth{APIKey: "src"}, nil)
	s := &syncer.Syncer{Cfg: syncer.Config{ShareTag: "share"}, Sources: []*immich.API{src}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := context.Background()
	wh := syncer.Webhook{URL: "http://app:8080/webhook/immich", Header: "X-Webhook-Secret", Secret: "s3cret"}

	if n := s.Provision(ctx, wh); n != 0 || f.creates != 1 {
		t.Fatalf("first run: failures=%d creates=%d", n, f.creates)
	}
	steps := f.current["steps"].([]any)
	if f.current["trigger"] != "AssetTagged" || len(steps) != 2 {
		t.Fatalf("unexpected workflow: %v", f.current)
	}
	if got := steps[1].(map[string]any)["method"]; got != "immich-plugin-core#webhook" {
		t.Errorf("step 2 = %v", got)
	}

	if s.Provision(ctx, wh); f.creates != 1 || f.updates != 0 {
		t.Errorf("second run should change nothing: creates=%d updates=%d", f.creates, f.updates)
	}

	wh.URL = "http://elsewhere:9000/webhook/immich"
	if s.Provision(ctx, wh); f.creates != 1 || f.updates != 1 {
		t.Errorf("changed URL should update once: creates=%d updates=%d", f.creates, f.updates)
	}
}

func TestProvisionSkippedWithoutPublicURL(t *testing.T) {
	f := &fakeWorkflows{}
	if n := provision(t, f, ""); n != 0 || f.creates != 0 {
		t.Errorf("failures=%d creates=%d", n, f.creates)
	}
}
