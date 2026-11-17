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
	status   int      // when not zero, every call answers with it
}

func (f *memFake) failWith(code int) { f.mu.Lock(); f.status = code; f.mu.Unlock() }

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
		if f.status != 0 {
			http.Error(w, "unwell", f.status)
			return
		}
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
	clk  *testClock
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
	clk := &testClock{t: time.Date(2026, 11, 16, 6, 0, 0, 0, time.UTC)}
	svc := &fakePushService{fail: map[string]error{}}
	h := &hub.Hub{Store: st, Connect: connect, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: clk.now}
	h.Push = &hub.Push{Store: st, Sender: svc, Key: "PUBLIC-KEY", Window: 10 * time.Millisecond, Now: clk.now}
	t.Cleanup(h.Push.Close)
	return &memEnv{h: h, f: f, sess: sess, st: st, push: svc, clk: clk}
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

// --- the daily pass ---

func (e *memEnv) at(t time.Time) { e.clk.set(t) }

func (e *memEnv) memory(year int, seen bool, photos ...string) {
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	ids := make([]string, len(photos))
	for i, n := range photos {
		ids[i] = uuid.NewSHA1(uuid.NameSpaceOID, []byte(n)).String() // a photo is named by a real id
	}
	e.f.memories = append(e.f.memories, memFakeMemory{id: uuid.NewString(), year: year, seen: seen, photo: ids})
}

func (e *memEnv) asked() []string {
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	return append([]string(nil), e.f.asks...)
}

// bobOn turns the messages on for bob, at 08:00 UTC unless the input says otherwise, with a phone to push to.
func (e *memEnv) bobOn(t *testing.T, in hub.MemoryInput) {
	t.Helper()
	if _, err := e.h.PushSubscribe(context.Background(), bobID, device(t, bobPhone, "en", store.MailAll)); err != nil {
		t.Fatal(err)
	}
	if err := e.h.SetMemories(context.Background(), e.sess, bob, in); err != nil {
		t.Fatal(err)
	}
}

func (e *memEnv) pushes() []sentPush { return e.push.to(bobPhone) }

func memAt(h, m int) time.Time { return time.Date(2026, 11, 16, h, m, 0, 0, time.UTC) }

func TestAMessageIsSentOnceADayWhenTheTimeHasComeAndNotBefore(t *testing.T) {
	e, ctx := memSetup(t), context.Background()
	e.h.AppURL, e.h.ImmichURL = "https://handoff.example", "https://photos.example"
	e.bobOn(t, goodMemoryInput())
	e.memory(2023, false, "p1", "p2")

	e.at(memAt(7, 59))
	e.h.MemoriesMaintenance(ctx)
	if len(e.asked()) != 0 {
		t.Fatalf("Immich is not even asked before the person's time: %v", e.asked())
	}
	e.at(memAt(8, 5))
	e.h.MemoriesMaintenance(ctx)
	waitFor(t, "the push", func() bool { return len(e.pushes()) == 1 })
	body := e.pushes()[0].body
	if !strings.Contains(body, "On this day") || !strings.Contains(body, "3 years ago") {
		t.Errorf("the years: %q", body)
	}
	if got := e.asked(); len(got) != 1 || got[0] != "2026-11-16" {
		t.Errorf("asked for today only: %v", got)
	}
	acts, _ := e.pushes()[0].raw["actions"].([]any)
	urls, _ := e.pushes()[0].raw["urls"].(map[string]any)
	if len(acts) != 2 || urls["a0"] == nil || !strings.HasPrefix(urls["a0"].(string), "https://photos.example/memory?id=") || urls["a1"] != "https://handoff.example/memories/2026-11-16" {
		t.Errorf("two buttons, the memory in Immich and Send to someone: %v %v", acts, urls)
	}
	// later the same day, nothing more, and Immich is not asked again
	e.at(memAt(18, 0))
	e.h.MemoriesMaintenance(ctx)
	e.h.MemoriesMaintenance(ctx)
	time.Sleep(60 * time.Millisecond)
	if len(e.pushes()) != 1 || len(e.asked()) != 1 {
		t.Errorf("once a day: %d pushes, asked %v", len(e.pushes()), e.asked())
	}
	// the next day it is sent again
	e.at(memAt(8, 1).Add(24 * time.Hour))
	e.h.MemoriesMaintenance(ctx)
	waitFor(t, "the next day's push", func() bool { return len(e.pushes()) == 2 })
	if acts, _ := e.h.Activity(ctx, bobID, 10); len(acts) == 0 {
		t.Error("a line in Activity")
	}
}

func TestADayWithNoMemoryOrOnlyWatchedOnesSendsNothingAndIsNotAskedAgain(t *testing.T) {
	e, ctx := memSetup(t), context.Background()
	e.bobOn(t, goodMemoryInput())
	e.at(memAt(9, 0))
	e.h.MemoriesMaintenance(ctx) // nothing at all
	e.memory(2021, true, "seen1")
	e.memory(2020, false) // a memory with no photo
	e.h.MemoriesMaintenance(ctx)
	time.Sleep(60 * time.Millisecond)
	if len(e.pushes()) != 0 || len(e.asked()) != 1 {
		t.Fatalf("nothing sent, and the day was done at the first look: %d pushes, asked %v", len(e.pushes()), e.asked())
	}
	// the next day one memory is new and one is watched: only the new one is told
	e.at(memAt(9, 0).Add(24 * time.Hour))
	e.f.mu.Lock()
	e.f.memories = nil
	e.f.mu.Unlock()
	e.memory(2021, true, "seen1")
	e.memory(2024, false, "new1")
	e.h.MemoriesMaintenance(ctx)
	waitFor(t, "one push", func() bool { return len(e.pushes()) == 1 })
	if b := e.pushes()[0].body; !strings.Contains(b, "2 years ago") || strings.Contains(b, "5 years ago") {
		t.Errorf("only the memory that was not watched: %q", b)
	}
}

func TestSeveralYearsOnOneDayMakeOneMessageWithTheYearsListedOldestFirst(t *testing.T) {
	e, ctx := memSetup(t), context.Background()
	e.bobOn(t, goodMemoryInput())
	e.memory(2021, false, "a")
	e.memory(2018, false, "b")
	e.memory(2025, false, "c")
	e.at(memAt(8, 30))
	e.h.MemoriesMaintenance(ctx)
	waitFor(t, "the push", func() bool { return len(e.pushes()) >= 1 })
	time.Sleep(60 * time.Millisecond)
	if len(e.pushes()) != 1 {
		t.Fatalf("one message: %d", len(e.pushes()))
	}
	if b := e.pushes()[0].body; !strings.Contains(b, "8 years ago, 5 years ago, 1 year ago") {
		t.Errorf("the years, oldest first: %q", b)
	}
}

func TestTheDayIsTheDayInThePersonsTimeZone(t *testing.T) {
	e, ctx := memSetup(t), context.Background()
	in := goodMemoryInput()
	in.TZ, in.At = "Pacific/Auckland", "00:30" // 01:00 on the 17th there when it is 12:00 UTC on the 16th
	e.bobOn(t, in)
	e.memory(2020, false, "a")
	e.at(memAt(12, 0))
	e.h.MemoriesMaintenance(ctx)
	if got := e.asked(); len(got) != 1 || got[0] != "2026-11-17" {
		t.Fatalf("their day, not the server's: %v", got)
	}
	waitFor(t, "the push", func() bool { return len(e.pushes()) == 1 })
	if !strings.Contains(e.pushes()[0].body, "6 years ago") {
		t.Errorf("years counted in their day: %q", e.pushes()[0].body)
	}
}

func TestADayThatFailedIsTriedAgainAndNotMarkedDone(t *testing.T) {
	e, ctx := memSetup(t), context.Background()
	e.bobOn(t, goodMemoryInput())
	e.memory(2022, false, "a")
	e.at(memAt(8, 10))
	e.f.failWith(503) // Immich is unwell: that says nothing about the key
	e.h.MemoriesMaintenance(ctx)
	time.Sleep(60 * time.Millisecond)
	if len(e.pushes()) != 0 {
		t.Fatal("nothing sent while Immich is down")
	}
	if p, _, _ := e.st.MemoryPref(ctx, bobID); p.LastDay != "" || p.Broken {
		t.Fatalf("the day is not done and the key is not blamed: %+v", p)
	}
	e.f.failWith(0)
	e.at(memAt(8, 20))
	e.h.MemoriesMaintenance(ctx)
	waitFor(t, "the push after Immich is back", func() bool { return len(e.pushes()) == 1 })
}

func TestAKeyThatWasDeletedIsSaidOnceAndThenLeftAlone(t *testing.T) {
	e, ctx := memSetup(t), context.Background()
	e.bobOn(t, goodMemoryInput())
	e.memory(2022, false, "a")
	p, _, _ := e.st.MemoryPref(ctx, bobID)
	e.sess.DeleteAPIKey(ctx, p.KeyID) // bob deleted it in Immich
	e.at(memAt(8, 10))
	e.h.MemoriesMaintenance(ctx)
	waitFor(t, "the notice", func() bool { return len(e.pushes()) == 1 })
	if b := e.pushes()[0].body; !strings.Contains(b, "stopped") {
		t.Errorf("the notice: %q", b)
	}
	if q, _, _ := e.st.MemoryPref(ctx, bobID); !q.Broken {
		t.Fatal("marked broken")
	}
	asks := len(e.asked())
	e.at(memAt(8, 10).Add(24 * time.Hour))
	e.h.MemoriesMaintenance(ctx)
	e.h.MemoriesMaintenance(ctx)
	time.Sleep(60 * time.Millisecond)
	if len(e.pushes()) != 1 || len(e.asked()) != asks {
		t.Errorf("left alone: %d pushes, %d asks (was %d)", len(e.pushes()), len(e.asked()), asks)
	}
}

func TestTheEmailCarriesUpToThreePicturesInsideItWhenAskedForAndNoneWhenNot(t *testing.T) {
	e, ctx := memSetup(t), context.Background()
	v, _ := vault.New("a-test-store-key-0123456789")
	m := &fakeMailer{ready: true}
	e.h.People = &hub.People{Store: e.st, Sender: m, Vault: v, AppURL: "https://app.example", Window: time.Hour}
	e.h.ImmichURL, e.h.AppURL = "https://photos.example", "https://handoff.example"
	t.Cleanup(e.h.People.Close)
	in := goodMemoryInput()
	in.Push, in.Email, in.Lang = false, true, "de"
	if err := e.h.SetMemories(ctx, e.sess, hubUser("Bob", "bob@x.test"), in); err != nil {
		t.Fatal(err)
	}
	e.memory(2020, false, "a1", "a2", "a3")
	e.memory(2022, false, "b1", "b2")
	e.at(memAt(8, 1))
	e.h.MemoriesMaintenance(ctx)
	mails := m.to("bob@x.test")
	if len(mails) != 1 {
		t.Fatalf("one email: %d", len(mails))
	}
	got := mails[0]
	if len(got.Inline) != 3 || e.f.thumbs != 3 {
		t.Errorf("three pictures at most, one from each year first: %d inline, %d fetched", len(got.Inline), e.f.thumbs)
	}
	for _, in := range got.Inline {
		if !strings.Contains(got.HTML, "cid:"+in.CID) || in.Type != "image/jpeg" {
			t.Errorf("the page shows %s: %s", in.CID, in.Type)
		}
	}
	if !strings.Contains(got.Subject, "An diesem Tag") || !strings.Contains(got.Subject, "vor 6 Jahren") || !strings.Contains(got.Text, "Hallo") && !strings.Contains(got.Text, "Hi") {
		t.Errorf("in the person's language: %q", got.Subject)
	}
	if !strings.Contains(got.Text, "https://photos.example/memory?id=") || !strings.Contains(got.HTML, "https://photos.example/memory?id=") {
		t.Errorf("a link into the oldest memory: %s", got.Text)
	}
	if !strings.Contains(got.Text, "https://handoff.example/memories/2026-11-16") || !strings.Contains(got.HTML, "An jemanden senden") {
		t.Errorf("and one to send the photos on, in German: %s", got.Text)
	}

	// without pictures
	e.at(memAt(8, 1).Add(24 * time.Hour))
	in.Pictures = false
	if err := e.h.SetMemories(ctx, e.sess, hubUser("Bob", "bob@x.test"), in); err != nil {
		t.Fatal(err)
	}
	before := e.f.thumbs
	e.h.MemoriesMaintenance(ctx)
	if mails = m.to("bob@x.test"); len(mails) != 2 || len(mails[1].Inline) != 0 || e.f.thumbs != before {
		t.Errorf("no pictures: %d mails, %d fetched more", len(mails), e.f.thumbs-before)
	}
}

func TestAnEmailThatCannotBeSentIsTriedAgainOnTheNextPassWhenItIsTheOnlyChannel(t *testing.T) {
	e, ctx := memSetup(t), context.Background()
	v, _ := vault.New("a-test-store-key-0123456789")
	m := &fakeMailer{ready: true, err: errors.New("smtp down")}
	e.h.People = &hub.People{Store: e.st, Sender: m, Vault: v, AppURL: "https://app.example", Window: time.Hour}
	t.Cleanup(e.h.People.Close)
	in := goodMemoryInput()
	in.Push, in.Email = false, true
	if err := e.h.SetMemories(ctx, e.sess, hubUser("Bob", "bob@x.test"), in); err != nil {
		t.Fatal(err)
	}
	e.memory(2020, false, "a")
	e.at(memAt(8, 1))
	e.h.MemoriesMaintenance(ctx)
	if p, _, _ := e.st.MemoryPref(ctx, bobID); p.LastDay != "" {
		t.Fatal("not done: nothing was sent")
	}
	m.mu.Lock()
	m.err = nil
	m.mu.Unlock()
	e.h.MemoriesMaintenance(ctx)
	if len(m.to("bob@x.test")) != 1 {
		t.Error("sent on the next pass")
	}
}

func hubUser(name, email string) immich.User { return immich.User{ID: bobID, Name: name, Email: email} }

func TestWithNoDeviceToPushToTheDayIsNotUsedUpSoALaterDeviceStillGetsIt(t *testing.T) {
	e, ctx := memSetup(t), context.Background()
	if err := e.h.SetMemories(ctx, e.sess, bob, goodMemoryInput()); err != nil { // push chosen, but no device yet
		t.Fatal(err)
	}
	e.memory(2022, false, "a")
	e.at(memAt(8, 5))
	e.h.MemoriesMaintenance(ctx)
	if p, _, _ := e.st.MemoryPref(ctx, bobID); p.LastDay != "" {
		t.Fatal("nothing could be sent, so the day is not done")
	}
	if _, err := e.h.PushSubscribe(ctx, bobID, device(t, bobPhone, "en", store.MailAll)); err != nil {
		t.Fatal(err)
	}
	e.at(memAt(9, 0))
	e.h.MemoriesMaintenance(ctx)
	waitFor(t, "the push to the new device", func() bool { return len(e.pushes()) == 1 })
}

// testClock is the clock of a test: the push goroutines read it while the test moves it.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }
