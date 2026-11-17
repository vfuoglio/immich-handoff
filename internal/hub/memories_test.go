package hub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	imgjpeg "image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/vault"
)

// memFake is a small Immich for the "On this day" tests: one person (bob) with keys, and the memories made for him.
type memFake struct {
	mu       sync.Mutex
	keys     map[string][]string // key id -> permissions
	secrets  map[string]string   // secret -> key id
	memories []memFakeMemory
	asks     []string // the days asked for
	thumbs   int      // thumbnails served
	down     bool     // every call fails
}

type memFakeMemory struct {
	id    string
	year  int
	seen  bool
	photo []string
}

func (f *memFake) handler() http.Handler {
	j := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	// who may do what: a session (the cookie) may do everything; a key only what it was given
	allowed := func(r *http.Request, perm string) bool {
		if strings.HasPrefix(r.Header.Get("Cookie"), "sess=") {
			return true
		}
		id, ok := f.secrets[r.Header.Get("x-api-key")]
		if !ok {
			return false
		}
		for _, p := range f.keys[id] {
			if p == perm {
				return true
			}
		}
		return false
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/api-keys", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body struct {
			Name        string   `json:"name"`
			Permissions []string `json:"permissions"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		id, secret := uuid.NewString(), "secret-"+uuid.NewString()
		f.keys[id], f.secrets[secret] = body.Permissions, id
		j(w, 201, map[string]any{"id": id, "name": body.Name, "secret": secret, "permissions": body.Permissions, "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"})
	})
	mux.HandleFunc("DELETE /api/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := r.PathValue("id")
		if _, ok := f.keys[id]; !ok {
			http.Error(w, "gone", 404)
			return
		}
		delete(f.keys, id)
		for s, k := range f.secrets {
			if k == id {
				delete(f.secrets, s)
			}
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /api/memories", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.down || !allowed(r, "memory.read") {
			http.Error(w, "no", 401)
			return
		}
		f.asks = append(f.asks, r.URL.Query().Get("for"))
		out := []any{}
		for _, m := range f.memories {
			as := []any{}
			for _, p := range m.photo {
				as = append(as, map[string]any{"id": p})
			}
			mm := map[string]any{"id": m.id, "type": "on_this_day", "data": map[string]any{"year": m.year}, "assets": as,
				"memoryAt": "2026-11-16T00:00:00Z", "createdAt": "2026-11-16T00:00:00Z", "isSaved": false, "ownerId": uuid.NewString()}
			if m.seen {
				mm["seenAt"] = "2026-11-16T05:00:00Z"
			}
			out = append(out, mm)
		}
		j(w, 200, out)
	})
	mux.HandleFunc("GET /api/assets/{id}/thumbnail", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.down || !allowed(r, "asset.view") {
			http.Error(w, "no", 401)
			return
		}
		f.thumbs++
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(tinyJPEG(r.PathValue("id")))
	})
	return mux
}

// tinyJPEG is a real, small picture.
func tinyJPEG(seed string) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	c := color.RGBA{R: uint8(len(seed) * 7), G: 90, B: 160, A: 255}
	for x := 0; x < 8; x++ {
		for y := 0; y < 8; y++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = imgjpeg.Encode(&b, img, nil)
	return b.Bytes()
}

type memEnv struct {
	h    *hub.Hub
	f    *memFake
	sess *immich.API
	st   *store.Store
	push *fakePushService
	clk  *time.Time
}

// memSetup is a hub with a store, push, and an Immich that has memories; the person is bob.
func memSetup(t *testing.T) *memEnv {
	t.Helper()
	f := &memFake{keys: map[string][]string{}, secrets: map[string]string{}}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	connect := func(key string) (*immich.API, error) { return immich.New(srv.URL, immich.Auth{APIKey: key}, nil) }
	sess, _ := immich.New(srv.URL, immich.Auth{Cookie: "sess=bob"}, nil)
	v, _ := vault.New("a-test-store-key-0123456789")
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clk := time.Date(2026, 11, 16, 6, 0, 0, 0, time.UTC)
	svc := &fakePushService{fail: map[string]error{}}
	h := &hub.Hub{Store: st, Connect: connect, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return clk }}
	h.Push = &hub.Push{Store: st, Sender: svc, Key: "PUBLIC-KEY", Window: 10 * time.Millisecond, Now: func() time.Time { return clk }}
	t.Cleanup(h.Push.Close)
	return &memEnv{h: h, f: f, sess: sess, st: st, push: svc, clk: &clk}
}

func goodMemoryInput() hub.MemoryInput {
	return hub.MemoryInput{At: "08:00", TZ: "UTC", Lang: "en", Push: true, Pictures: true}
}

func TestOnThisDayNeedsATimeAZoneAndAChannelAndTheAppNeedsSomethingToSendWith(t *testing.T) {
	e, ctx := memSetup(t), context.Background()
	for name, in := range map[string]hub.MemoryInput{
		"no time":    {At: "", Push: true},
		"bad time":   {At: "8:00", Push: true},
		"past 23:59": {At: "24:00", Push: true},
		"bad zone":   {At: "08:00", TZ: "Mars/Olympus", Push: true},
		"no channel": {At: "08:00"},
	} {
		if err := e.h.SetMemories(ctx, e.sess, bob, in); err == nil || !hub.IsUserError(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(e.f.keys) != 0 {
		t.Error("a refused form makes no key")
	}
	e.h.Push = nil
	if err := e.h.SetMemories(ctx, e.sess, bob, goodMemoryInput()); !errors.Is(err, hub.ErrMemoriesNoSender) {
		t.Errorf("with nothing to send with: %v", err)
	}
	if c := e.h.MemoriesCard(ctx, bobID); c.Available {
		t.Error("the card is not offered without push or email")
	}
}

func TestTurningOnMakesAKeyThatCanReadMemoriesAndLookAtPicturesAndNothingElse_AndTurningOffDeletesIt(t *testing.T) {
	e, ctx := memSetup(t), context.Background()
	if c := e.h.MemoriesCard(ctx, bobID); !c.Available || c.On || !c.Push || c.Email {
		t.Fatalf("offered, not on: %+v", c)
	}
	if err := e.h.SetMemories(ctx, e.sess, bob, goodMemoryInput()); err != nil {
		t.Fatal(err)
	}
	p, ok, _ := e.st.MemoryPref(ctx, bobID)
	if !ok || p.At != "08:00" || p.TZ != "UTC" || !p.Push || p.Email || !p.Pictures || p.Name != "Bob" {
		t.Fatalf("saved: %+v %v", p, ok)
	}
	perms := e.f.keys[p.KeyID.String()]
	if len(perms) != 2 || !(perms[0] == "memory.read" && perms[1] == "asset.view" || perms[1] == "memory.read" && perms[0] == "asset.view") {
		t.Fatalf("the key can read memories and look at pictures, and nothing else: %v", perms)
	}
	if c := e.h.MemoriesCard(ctx, bobID); !c.On || !c.KeyOK || c.Settings.At != "08:00" {
		t.Errorf("on: %+v", c)
	}

	// changing the settings keeps the key
	in := goodMemoryInput()
	in.At, in.Email = "07:15", true
	if err := e.h.SetMemories(ctx, e.sess, bob, in); err != nil {
		t.Fatal(err)
	}
	q, _, _ := e.st.MemoryPref(ctx, bobID)
	if q.KeyID != p.KeyID || q.At != "07:15" || !q.Email || len(e.f.keys) != 1 {
		t.Fatalf("same key, new settings: %+v keys=%d", q, len(e.f.keys))
	}

	if err := e.h.StopMemories(ctx, e.sess, bob); err != nil {
		t.Fatal(err)
	}
	if len(e.f.keys) != 0 {
		t.Error("the key is deleted in the person's Immich")
	}
	if _, ok, _ := e.st.MemoryPref(ctx, bobID); ok {
		t.Error("and forgotten")
	}
	if err := e.h.StopMemories(ctx, e.sess, bob); !errors.Is(err, hub.ErrMemoriesNotOn) {
		t.Errorf("turning off what is off: %v", err)
	}
}

func TestAKeyThatStoppedWorkingIsReplacedWhenTheSettingsAreSavedAgain(t *testing.T) {
	e, ctx := memSetup(t), context.Background()
	if err := e.h.SetMemories(ctx, e.sess, bob, goodMemoryInput()); err != nil {
		t.Fatal(err)
	}
	p, _, _ := e.st.MemoryPref(ctx, bobID)
	e.st.MarkMemoryBroken(ctx, bobID, true)
	if err := e.h.SetMemories(ctx, e.sess, bob, goodMemoryInput()); err != nil {
		t.Fatal(err)
	}
	q, _, _ := e.st.MemoryPref(ctx, bobID)
	if q.KeyID == p.KeyID || q.Broken || len(e.f.keys) != 1 {
		t.Fatalf("a new key, the old one gone: %+v keys=%d", q, len(e.f.keys))
	}
}
