package web_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/bridge"
	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/peer"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
	"github.com/vfuoglio/immich-handoff/internal/vault"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

// anApp is one running Handoff, with its own data file, for tests of two apps that talk to each other.
type anApp struct {
	url  string
	hub  *hub.Hub
	st   *store.Store
	srv  *httptest.Server
	user string // the cookie of the person who uses it
}

func startApp(t *testing.T, user string, share bool, allowPrivate bool) *anApp {
	t.Helper()
	im := httptest.NewServer(fakeImmich())
	t.Cleanup(im.Close)
	v, _ := vault.New("0123456789abcdef0123")
	st, err := store.Open(t.TempDir()+"/h.db", v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var handler http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(srv.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &syncer.Syncer{Cfg: syncer.Config{DryRun: true}, Log: log}
	h := &hub.Hub{Store: st, Log: log, AppURL: srv.URL, Share: share, Options: hub.Options{InboxTag: "inbox"},
		Connect: func(key string) (*immich.API, error) { return immich.New(im.URL, immich.Auth{APIKey: key}, nil) }}
	h.Bridge.Policy = bridge.Policy{AllowPrivate: allowPrivate}
	if err := h.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Provider = h
	handler = (&web.Server{S: s, Hub: h, BaseURL: im.URL, WebhookHeader: "X-Webhook-Secret", WebhookSecret: "s", Log: log, BackgroundCtx: context.Background()}).Handler()
	return &anApp{url: srv.URL, hub: h, st: st, srv: srv, user: user}
}

func (a *anApp) as(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, a.url+path, strings.NewReader(body))
	req.Header.Set("Cookie", a.user)
	req.Header.Set("X-Requested-With", "immich-handoff")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

var peerLinkRe = regexp.MustCompile(`value="(http[^"]+/peer/invite/[0-9a-f]{64})"`)

func pairApps(t *testing.T, a, b *anApp) {
	t.Helper()
	code, page := a.as(t, "POST", "/peers/invite", "")
	m := peerLinkRe.FindStringSubmatch(page)
	if code != 200 || m == nil {
		t.Fatalf("an invitation: %d %.200s", code, page)
	}
	if code, body := b.as(t, "POST", "/peers/preview", "link="+m[1]); code != 200 || !strings.Contains(body, "invites you to share albums") {
		t.Fatalf("the preview: %d %.300s", code, body)
	}
	if code, body := b.as(t, "POST", "/peers/accept", "link="+m[1]); code != 204 {
		t.Fatalf("accepting: %d %s", code, body)
	}
}

func peersOf(t *testing.T, a *anApp, owner string) []store.Peer {
	t.Helper()
	list, err := a.st.PeersOf(context.Background(), uuid.MustParse(owner))
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestTwoAppsPairThroughAnInvitationAndKnowEachOtherAfterwards(t *testing.T) {
	a := startApp(t, "sess=alice", true, true)
	b := startApp(t, "sess=family", true, true)
	pairApps(t, a, b)
	pa, pb := peersOf(t, a, aliceID), peersOf(t, b, familyID)
	if len(pa) != 1 || len(pb) != 1 {
		t.Fatalf("one connection each: %d %d", len(pa), len(pb))
	}
	if pa[0].Name != "Family" || pa[0].Role != store.PeerInviter || pa[0].Address != b.url || pa[0].TheirID != pb[0].ID {
		t.Errorf("the inviter's record: %+v", pa[0])
	}
	if pb[0].Name != "Alice" || pb[0].Role != store.PeerJoiner || pb[0].Address != a.url || pb[0].TheirID != pa[0].ID {
		t.Errorf("the joiner's record: %+v", pb[0])
	}
	if pa[0].Secret == "" || pa[0].Secret != pb[0].Secret {
		t.Error("both apps hold the same secret")
	}
	if pa[0].SeenAt.IsZero() || pb[0].SeenAt.IsZero() {
		t.Error("each has heard the other answer")
	}
	_, pageA := a.as(t, "GET", "/", "")
	_, pageB := b.as(t, "GET", "/", "")
	for _, want := range []string{"Albums shared with other servers", "Family", "Connected"} {
		if !strings.Contains(pageA, want) {
			t.Errorf("A's page lacks %q", want)
		}
	}
	if !strings.Contains(pageB, "Alice") || !strings.Contains(pageB, "Connected") {
		t.Errorf("B's page lacks the connection")
	}
	// nobody else's page shows the connection
	req, _ := http.NewRequest("GET", a.url+"/", nil)
	req.Header.Set("Cookie", "sess=other")
	res, _ := http.DefaultClient.Do(req)
	other, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(other), "Family") {
		t.Error("another person sees the connection")
	}
	// the invitation works once
	_, page := a.as(t, "POST", "/peers/invite", "")
	link := peerLinkRe.FindStringSubmatch(page)[1]
	if code, _ := b.as(t, "POST", "/peers/accept", "link="+link); code != 204 {
		t.Fatalf("a second invitation: %d", code)
	}
	if code, body := b.as(t, "POST", "/peers/accept", "link="+link); code == 204 || !strings.Contains(body, "expired or was already used") {
		t.Errorf("the same invitation again: %d %s", code, body)
	}
}

func TestRequestsBetweenAppsAreSignedOrRefusedWithTheSameAnswer(t *testing.T) {
	a := startApp(t, "sess=alice", true, true)
	b := startApp(t, "sess=family", true, true)
	pairApps(t, a, b)
	pa := peersOf(t, a, aliceID)[0]
	pb := peersOf(t, b, familyID)[0]
	send := func(headers map[string]string, body string) (int, string) {
		req, _ := http.NewRequest("POST", b.url+"/peer/v1/hello", strings.NewReader(body))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(out)
	}
	sign := func(secret, id, nonce string, at time.Time, body string) map[string]string {
		return map[string]string{peer.HeaderPeer: id, peer.HeaderTime: strconv.FormatInt(at.Unix(), 10), peer.HeaderNonce: nonce,
			peer.HeaderSignature: peer.Sign(secret, "POST", "/peer/v1/hello", at.Unix(), nonce, []byte(body))}
	}
	body := `{"name":"Alice","version":1}`
	if code, out := send(sign(pa.Secret, pa.TheirID, "n-ok", time.Now(), body), body); code != 200 || !strings.Contains(out, `"name":"Family"`) && !strings.Contains(out, `"name":"Alice"`) {
		t.Fatalf("a signed hello: %d %s", code, out)
	}
	good := sign(pa.Secret, pa.TheirID, "n-replay", time.Now(), body)
	send(good, body)
	answers := map[string]string{}
	for name, c := range map[string]struct {
		h    map[string]string
		body string
	}{
		"nothing":        {nil, body},
		"a made-up peer": {sign(pa.Secret, uuid.NewString(), "n1", time.Now(), body), body},
		"another secret": {sign("not-the-secret-not-the-secret-not-the-secret-xx", pa.TheirID, "n2", time.Now(), body), body},
		"a changed body": {sign(pa.Secret, pa.TheirID, "n3", time.Now(), body), `{"name":"Mallory"}`},
		"an old request": {sign(pa.Secret, pa.TheirID, "n4", time.Now().Add(-time.Hour), body), body},
		"a replay":       {good, body},
	} {
		code, out := send(c.h, c.body)
		answers[name] = strings.TrimSpace(out)
		if code != 401 {
			t.Errorf("%s: %d", name, code)
		}
	}
	for name, a := range answers {
		if a != answers["nothing"] {
			t.Errorf("%s answers differently from the others, which tells an outsider something: %q vs %q", name, a, answers["nothing"])
		}
	}
	_ = pb
}

func TestAnAppThatCannotBeReachedIsNotAcceptedAndNothingIsKept(t *testing.T) {
	a := startApp(t, "sess=alice", true, true)
	b := startApp(t, "sess=family", true, true)
	b.hub.AppURL = "http://127.0.0.1:1" // nothing listens there
	if code, body := func() (int, string) {
		_, page := a.as(t, "POST", "/peers/invite", "")
		return b.as(t, "POST", "/peers/accept", "link="+peerLinkRe.FindStringSubmatch(page)[1])
	}(); code == 204 || !strings.Contains(body, "could not be reached") {
		t.Errorf("an unreachable app: %d %s", code, body)
	}
	if len(peersOf(t, a, aliceID)) != 0 || len(peersOf(t, b, familyID)) != 0 {
		t.Error("neither side keeps a connection that was not proved")
	}
}

func TestPausingAndEndingAConnectionReachesTheOtherApp(t *testing.T) {
	a := startApp(t, "sess=alice", true, true)
	b := startApp(t, "sess=family", true, true)
	pairApps(t, a, b)
	pb := peersOf(t, b, familyID)[0]
	if code, body := b.as(t, "POST", "/peers/pause", "peer="+pb.ID+"&paused=1"); code != 204 {
		t.Fatalf("pause: %d %s", code, body)
	}
	if got := peersOf(t, a, aliceID)[0]; !got.TheyPaused || got.IPaused {
		t.Errorf("A sees that B paused: %+v", got)
	}
	_, page := a.as(t, "GET", "/", "")
	if !strings.Contains(page, "has paused sharing") {
		t.Error("A's page says so")
	}
	b.as(t, "POST", "/peers/pause", "peer="+pb.ID+"&paused=0")
	if got := peersOf(t, a, aliceID)[0]; got.TheyPaused {
		t.Errorf("resumed: %+v", got)
	}
	if code, _ := b.as(t, "POST", "/peers/remove", "peer="+pb.ID); code != 204 {
		t.Fatalf("remove: %d", code)
	}
	if len(peersOf(t, b, familyID)) != 0 {
		t.Error("B forgot it")
	}
	got := peersOf(t, a, aliceID)
	if len(got) != 1 || got[0].State != store.PeerLeft {
		t.Fatalf("A is told the other side left: %+v", got)
	}
	_, page = a.as(t, "GET", "/", "")
	if !strings.Contains(page, "ended the connection") {
		t.Error("A's page says so")
	}
	// a left peer can no longer call
	if _, err := a.hub.PeerAuth(context.Background(), got[0].ID, http.Header{}, "POST", "/peer/v1/hello", nil); err == nil {
		t.Error("a connection that ended answers nothing")
	}
}

func TestSharingNeedsTheSwitchTheAddressAndTheSafeAddressRules(t *testing.T) {
	off := startApp(t, "sess=alice", false, true)
	if code, _ := off.as(t, "POST", "/peers/invite", ""); code != 409 {
		t.Errorf("switched off: %d", code)
	}
	for _, p := range []string{"/peer/v1/invite/" + strings.Repeat("a", 64), "/peer/invite/" + strings.Repeat("a", 64)} {
		if res, err := http.Get(off.url + p); err != nil || res.StatusCode != 404 {
			t.Errorf("%s while off: %v", p, res)
		}
	}
	_, page := off.as(t, "GET", "/", "")
	if strings.Contains(page, "Albums shared with other servers") {
		t.Error("the section is not on the page while sharing is off")
	}
	strict := startApp(t, "sess=family", true, false)
	for _, link := range []string{"http://127.0.0.1:9/peer/invite/" + strings.Repeat("a", 64), "https://10.1.2.3/peer/invite/" + strings.Repeat("a", 64), "https://169.254.169.254/peer/invite/" + strings.Repeat("a", 64)} {
		if code, body := strict.as(t, "POST", "/peers/preview", "link="+link); code != 409 {
			t.Errorf("%s: %d %s", link, code, body)
		}
	}
	for _, link := range []string{"", "not a link", "https://example.org/peer/invite/short", "https://example.org/other/path"} {
		if code, _ := strict.as(t, "POST", "/peers/preview", "link="+link); code != 409 {
			t.Errorf("%q: %d", link, code)
		}
	}
	self := startApp(t, "sess=alice", true, true)
	_, p := self.as(t, "POST", "/peers/invite", "")
	if code, body := self.as(t, "POST", "/peers/preview", "link="+peerLinkRe.FindStringSubmatch(p)[1]); code != 409 || !strings.Contains(body, "this very app") {
		t.Errorf("an invitation to oneself: %d %s", code, body)
	}
	// an invitation page shows who invites and nothing else; an unknown one is not found
	a := startApp(t, "sess=alice", true, true)
	_, p = a.as(t, "POST", "/peers/invite", "")
	link := peerLinkRe.FindStringSubmatch(p)[1]
	res, _ := http.Get(link)
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), "Alice invites you to share albums") || res.Header.Get("X-Robots-Tag") != "noindex" {
		t.Errorf("the invitation's page: %d %.200s", res.StatusCode, b)
	}
	res, _ = http.Get(a.url + "/peer/invite/" + strings.Repeat("b", 64))
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Errorf("an unknown invitation: %d", res.StatusCode)
	}
	var info struct {
		Name    string
		Version int
	}
	res, _ = http.Get(strings.Replace(link, "/peer/invite/", "/peer/v1/invite/", 1))
	json.NewDecoder(res.Body).Decode(&info)
	res.Body.Close()
	if info.Name != "Alice" || info.Version != peer.Version {
		t.Errorf("what the other app learns about it: %+v", info)
	}
}
