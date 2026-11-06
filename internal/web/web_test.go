package web_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

const (
	aliceID  = "aaaaaaaa-0000-0000-0000-000000000001"
	familyID = "aaaaaaaa-0000-0000-0000-000000000002"
	otherID  = "aaaaaaaa-0000-0000-0000-000000000009"
	ts       = "2025-01-02T03:04:05.000Z"
)

// fakeImmich answers just enough for sign-in checks, counts and a dry-run pass.
func fakeImmich() http.Handler {
	j := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/users/me", func(w http.ResponseWriter, r *http.Request) {
		who := map[string][2]string{
			"sess=alice": {aliceID, "Alice"}, "sess=family": {familyID, "Family"}, "sess=other": {otherID, "Other"},
			"k-alice": {aliceID, "Alice"}, "k-family": {familyID, "Family"},
		}
		key := r.Header.Get("x-api-key")
		if key == "" {
			key = strings.TrimSpace(r.Header.Get("Cookie"))
		}
		u, ok := who[key]
		if !ok {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		j(w, map[string]any{"id": u[0], "name": u[1], "email": strings.ToLower(u[1]) + "@x.test"})
	})
	// Tags are off by default in Immich; the family account has them on, "sess=other" cannot be changed.
	var prefMu sync.Mutex
	tagsOn := map[string]bool{"sess=family": true}
	cookie := func(r *http.Request) string { return strings.TrimSpace(r.Header.Get("Cookie")) }
	prefs := func(on bool) map[string]any {
		return map[string]any{"tags": map[string]any{"enabled": on, "sidebarWeb": on}}
	}
	mux.HandleFunc("GET /api/users/me/preferences", func(w http.ResponseWriter, r *http.Request) {
		prefMu.Lock()
		defer prefMu.Unlock()
		j(w, prefs(tagsOn[cookie(r)]))
	})
	mux.HandleFunc("PUT /api/users/me/preferences", func(w http.ResponseWriter, r *http.Request) {
		if cookie(r) == "sess=other" {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		var body struct{ Tags struct{ Enabled bool } }
		json.NewDecoder(r.Body).Decode(&body)
		prefMu.Lock()
		defer prefMu.Unlock()
		tagsOn[cookie(r)] = body.Tags.Enabled
		j(w, prefs(tagsOn[cookie(r)]))
	})
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		tag := func(id, n string) map[string]any {
			return map[string]any{"id": id, "name": n, "value": n, "createdAt": ts, "updatedAt": ts}
		}
		j(w, []any{tag("33333333-3333-3333-3333-333333333333", "share"), tag("44444444-4444-4444-4444-444444444444", "inbox")})
	})
	mux.HandleFunc("POST /api/search/metadata", func(w http.ResponseWriter, r *http.Request) {
		a := func(id string) map[string]any {
			return map[string]any{"id": id, "originalFileName": "x.jpg", "fileCreatedAt": ts, "fileModifiedAt": ts}
		}
		j(w, map[string]any{"assets": map[string]any{"items": []any{a("11111111-1111-1111-1111-111111111111"), a("22222222-2222-2222-2222-222222222222")}, "count": 2, "total": 2, "facets": []any{}}})
	})
	return mux
}

func setup(t *testing.T, opts ...func(*web.Server)) (*httptest.Server, *syncer.Syncer) {
	t.Helper()
	im := httptest.NewServer(fakeImmich())
	t.Cleanup(im.Close)
	mk := func(name, key string) *syncer.Account {
		a, err := immich.New(im.URL, immich.Auth{APIKey: key}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return &syncer.Account{Name: name, API: a}
	}
	svc := &syncer.Service{Name: "family", Tag: "share", InboxTag: "inbox", RefusedTag: "share-duplicate", ExpiryDays: 30,
		Senders: []*syncer.Account{mk("alice", "k-alice")}, Receivers: []*syncer.Account{mk("family", "k-family")}}
	s := &syncer.Syncer{Services: []*syncer.Service{svc}, Cfg: syncer.Config{DryRun: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	syncer.ResolveUsers(context.Background(), s.Services)
	srv := &web.Server{S: s, BaseURL: im.URL, WebhookHeader: "X-Webhook-Secret", WebhookSecret: "s3cret", Log: s.Log, BackgroundCtx: context.Background()}
	for _, o := range opts {
		o(srv)
	}
	app := httptest.NewServer(srv.Handler())
	t.Cleanup(app.Close)
	return app, s
}

func do(t *testing.T, method, url, cookie string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestSignInStates(t *testing.T) {
	app, _ := setup(t)
	if code, body := do(t, "GET", app.URL+"/", "", nil); code != 401 || !strings.Contains(body, "Sign in to Immich first") {
		t.Errorf("no cookie: %d %.80s", code, body)
	}
	// any signed-in Immich user may open the page; without a service it is simply empty
	if code, body := do(t, "GET", app.URL+"/", "sess=other", nil); code != 200 || !strings.Contains(body, "do not take part in any service") || strings.Contains(body, "Last run") {
		t.Errorf("stranger: %d %.120s", code, body)
	}
	if code, _ := do(t, "GET", app.URL+"/fragments/lastrun", "sess=other", nil); code != 403 {
		t.Errorf("activity must stay hidden from non-members, got %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/events", "sess=other", nil); code != 403 {
		t.Errorf("the live stream must stay hidden from non-members, got %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/", "sess=bogus", nil); code != 401 {
		t.Errorf("unknown session must be 401, got %d", code)
	}
}

func TestDashboardShowsMyServices(t *testing.T) {
	app, _ := setup(t)
	code, body := do(t, "GET", app.URL+"/", "sess=alice", nil)
	for _, want := range []string{"Your services", "family", "You send", "share", "Alice", `hx-post="sync"`} {
		if code != 200 || !strings.Contains(body, want) {
			t.Fatalf("dashboard missing %q (code %d)", want, code)
		}
	}
	if _, body := do(t, "GET", app.URL+"/", "sess=family", nil); !strings.Contains(body, "You receive") || !strings.Contains(body, "inbox") {
		t.Errorf("receiver view wrong: %.200s", body)
	}
}

func TestCountFragment(t *testing.T) {
	app, _ := setup(t)
	code, body := do(t, "GET", app.URL+"/fragments/count?service=family&role=sender", "sess=alice", nil)
	if code != 200 || !strings.Contains(body, "2") || !strings.Contains(body, "waiting to be handed over") {
		t.Errorf("count: %d %q", code, body)
	}
	if code, _ := do(t, "GET", app.URL+"/fragments/count?service=family&role=receiver", "sess=alice", nil); code != 404 {
		t.Errorf("a sender must not read the receiver count, got %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/fragments/count?service=family&role=sender", "", nil); code != 401 {
		t.Errorf("fragment needs sign-in, got %d", code)
	}
}

func TestSyncNeedsHeaderAndSignIn(t *testing.T) {
	app, _ := setup(t)
	if code, _ := do(t, "POST", app.URL+"/sync", "sess=alice", nil); code != 400 {
		t.Errorf("without the CSRF header: %d", code)
	}
	if code, _ := do(t, "POST", app.URL+"/sync", "", map[string]string{"X-Requested-With": "immich-handoff"}); code != 401 {
		t.Errorf("without sign-in: %d", code)
	}
	if code, _ := do(t, "POST", app.URL+"/sync", "sess=other", map[string]string{"X-Requested-With": "immich-handoff"}); code != 403 {
		t.Errorf("a non-member must not trigger syncs: %d", code)
	}
	if code, body := do(t, "POST", app.URL+"/sync", "sess=alice", map[string]string{"X-Requested-With": "immich-handoff"}); code != 202 || body != "Sync started" {
		t.Errorf("sync: %d %q", code, body)
	}
}

func TestWebhookSecret(t *testing.T) {
	app, _ := setup(t)
	if code, _ := do(t, "POST", app.URL+"/webhook/immich", "", nil); code != 401 {
		t.Errorf("no secret: %d", code)
	}
	if code, _ := do(t, "POST", app.URL+"/webhook/immich", "", map[string]string{"X-Webhook-Secret": "s3cret"}); code != 202 {
		t.Errorf("good secret: %d", code)
	}
}

func TestStaticAssets(t *testing.T) {
	app, _ := setup(t)
	for _, p := range []string{"app.css", "app.js", "htmx.min.js", "favicon.svg"} {
		if code, body := do(t, "GET", app.URL+"/static/"+p, "", nil); code != 200 || len(body) < 50 {
			t.Errorf("static %s: %d (%d bytes)", p, code, len(body))
		}
	}
}

func TestEventsAnnounceFinishedRuns(t *testing.T) {
	app, s := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", app.URL+"/events", nil)
	req.Header.Set("Cookie", "sess=alice")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	lines := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	if l := <-lines; !strings.HasPrefix(l, ": connected") {
		t.Fatalf("first line %q", l)
	}
	go s.Run(context.Background(), "test", false)
	deadline := time.After(4 * time.Second)
	for {
		select {
		case l := <-lines:
			if l == "event: run" {
				return
			}
		case <-deadline:
			t.Fatal("no run event after a pass finished")
		}
	}
}

func TestJoinAndLeaveAreOffWithoutSelfService(t *testing.T) {
	app, _ := setup(t)
	h := map[string]string{"X-Requested-With": "immich-handoff"}
	if code, _ := do(t, "POST", app.URL+"/join", "sess=other", h); code != 404 {
		t.Errorf("join with self-service off: %d", code)
	}
	if code, _ := do(t, "POST", app.URL+"/leave", "sess=alice", h); code != 404 {
		t.Errorf("leave with self-service off: %d", code)
	}
	if code, _ := do(t, "POST", app.URL+"/join", "sess=other", nil); code != 400 {
		t.Errorf("join without the CSRF header: %d", code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	app, _ := setup(t, func(s *web.Server) { s.PublicImmichURL = "https://photos.example.com/immich" })
	for _, path := range []string{"/", "/static/app.css", "/healthz"} {
		res, err := http.Get(app.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		csp := res.Header.Get("Content-Security-Policy")
		for _, want := range []string{"frame-ancestors 'none'", "script-src 'self'", "img-src 'self' data: https://photos.example.com"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP %q lacks %q", path, csp, want)
			}
		}
		if res.Header.Get("X-Content-Type-Options") != "nosniff" || res.Header.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: missing hardening headers: %v", path, res.Header)
		}
	}
}

func TestMetricsNeedTheSecret(t *testing.T) {
	app, s := setup(t)
	s.Run(context.Background(), "test", false)
	if code, _ := do(t, "GET", app.URL+"/metrics", "", nil); code != 401 {
		t.Errorf("metrics without the secret: %d", code)
	}
	code, body := do(t, "GET", app.URL+"/metrics", "", map[string]string{"X-Webhook-Secret": "s3cret"})
	if code != 200 || !strings.Contains(body, "handoff_passes_total 1") || !strings.Contains(body, `handoff_photos_total{result="imported"}`) {
		t.Errorf("metrics: %d %q", code, body)
	}
}

func TestChangesAreRateLimitedPerPerson(t *testing.T) {
	app, _ := setup(t)
	h := map[string]string{"X-Requested-With": "immich-handoff"}
	got429 := false
	for i := 0; i < 40; i++ {
		if code, _ := do(t, "POST", app.URL+"/sync", "sess=alice", h); code == 429 {
			got429 = true
			if i < 30 {
				t.Fatalf("limited too early, after %d changes", i)
			}
			break
		}
	}
	if !got429 {
		t.Error("a person flooding changes must be limited")
	}
	if code, _ := do(t, "POST", app.URL+"/sync", "sess=family", h); code == 429 {
		t.Error("the limit is per person, not global")
	}
}

func TestThumbnailsOnlyWhenTheImmichAddressIsKnown(t *testing.T) {
	without, _ := setup(t)
	_, body := do(t, "GET", without.URL+"/fragments/count?service=family&role=sender", "sess=alice", nil)
	if strings.Contains(body, "<img") {
		t.Errorf("no thumbnails without PUBLIC_IMMICH_URL: %q", body)
	}
	with, _ := setup(t, func(s *web.Server) { s.PublicImmichURL = "https://photos.example.com/" })
	_, body = do(t, "GET", with.URL+"/fragments/count?service=family&role=sender", "sess=alice", nil)
	if !strings.Contains(body, "https://photos.example.com/api/assets/11111111-1111-1111-1111-111111111111/thumbnail?size=thumbnail") {
		t.Errorf("thumbnails expected: %q", body)
	}
	_, body = do(t, "GET", with.URL+"/fragments/count?service=family&role=receiver", "sess=family", nil)
	if strings.Contains(body, "<img") {
		t.Errorf("receivers see counts only: %q", body)
	}
}

// The invitation page lives one level down (/invite/<token>); its relative links to the stylesheet,
// scripts and actions must still resolve to the app root, also behind a path prefix.
func TestInvitePageLinksPointAtTheRoot(t *testing.T) {
	app, _ := setup(t)
	_, body := do(t, "GET", app.URL+"/invite/abc", "sess=alice", nil)
	if !strings.Contains(body, `<base href="../"`) || !strings.Contains(body, `href="static/app.css"`) {
		t.Errorf("invite page must point relative links at the root: %.400s", body)
	}
	if _, body := do(t, "GET", app.URL+"/", "sess=alice", nil); strings.Contains(body, "<base") {
		t.Errorf("the dashboard needs no base: %.200s", body)
	}
}

func TestTagsNoticeAndSwitch(t *testing.T) {
	app, _ := setup(t)
	hdr := map[string]string{"X-Requested-With": "immich-handoff"}
	if _, body := do(t, "GET", app.URL+"/", "sess=alice", nil); !strings.Contains(body, "Tags are switched off") ||
		!strings.Contains(body, `id="gated"`) || !strings.Contains(body, "inert") {
		t.Errorf("a person with Tags off must be told, and the rest of the page switched off: %.300s", body)
	}
	if _, body := do(t, "GET", app.URL+"/", "sess=family", nil); strings.Contains(body, "Tags are switched off") || strings.Contains(body, "inert") {
		t.Errorf("a person with Tags on must not be nagged or locked out: %.300s", body)
	}
	if code, _ := do(t, "POST", app.URL+"/preferences/tags", "sess=alice", nil); code != 400 {
		t.Errorf("switching Tags on needs the cross-site header, got %d", code)
	}
	if code, _ := do(t, "POST", app.URL+"/preferences/tags", "", hdr); code != 401 {
		t.Errorf("switching Tags on needs a sign-in, got %d", code)
	}
	ok, _ := http.NewRequest("POST", app.URL+"/preferences/tags", nil)
	ok.Header.Set("Cookie", "sess=alice")
	ok.Header.Set("X-Requested-With", "immich-handoff")
	res0, err := http.DefaultClient.Do(ok)
	if err != nil {
		t.Fatal(err)
	}
	b0, _ := io.ReadAll(res0.Body)
	res0.Body.Close()
	if res0.StatusCode != 200 || !strings.Contains(string(b0), "refresh the Immich tab") || res0.Header.Get("HX-Trigger") != "tags-on" {
		t.Errorf("switching on: %d %q, trigger %q (the page unlocks itself on it)", res0.StatusCode, b0, res0.Header.Get("HX-Trigger"))
	}
	if _, body := do(t, "GET", app.URL+"/", "sess=alice", nil); strings.Contains(body, "Tags are switched off") || strings.Contains(body, "inert") {
		t.Errorf("the notice and the lock must go once Tags are on: %.300s", body)
	}
	// when Immich refuses, say so in the message line and keep the button
	req, _ := http.NewRequest("POST", app.URL+"/preferences/tags", nil)
	req.Header.Set("Cookie", "sess=other")
	req.Header.Set("X-Requested-With", "immich-handoff")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadGateway || res.Header.Get("HX-Retarget") != "#flash" {
		t.Errorf("a refusal from Immich: %d, retarget %q", res.StatusCode, res.Header.Get("HX-Retarget"))
	}
}
