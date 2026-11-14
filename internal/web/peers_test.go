package web_test

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
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

// sharing is a fake Immich for a Handoff that shares albums: what the base fake lacks (keys that can be made and deleted, albums
// that can be made, an album's photos with their checksums), kept so the test can look at what the app did.
type sharing struct {
	mu      sync.Mutex
	keys    map[string]sharedKey // by secret
	deleted []string
	albums  map[string][]map[string]any // by owner id
	photos  map[string]*fakePhoto       // by asset id
	members map[string][]string         // album id -> asset ids
	uploads int                         // photos uploaded with a key (what a peer sent)
}

type fakePhoto struct {
	owner, name, sum string
	data             []byte
	trashed          bool
}

func sumOf(data []byte) string {
	s := sha1.Sum(data)
	return base64.StdEncoding.EncodeToString(s[:])
}

// addPhoto puts a photo in an owner's library and, if album is not empty, in that album.
func (f *sharing) addPhoto(owner, album, name string, data []byte) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := uuid.NewString()
	f.photos[id] = &fakePhoto{owner: owner, name: name, data: data, sum: sumOf(data)}
	if album != "" {
		f.members[album] = append(f.members[album], id)
	}
	return id
}

func (f *sharing) inAlbum(album string) []*fakePhoto {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*fakePhoto
	for _, id := range f.members[album] {
		out = append(out, f.photos[id])
	}
	return out
}

func (f *sharing) countByChecksum(owner, sum string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.photos {
		if p.owner == owner && p.sum == sum {
			n++
		}
	}
	return n
}

type sharedKey struct {
	id, owner string
	perms     []string
}

func (f *sharing) handler(base http.Handler) http.Handler {
	j := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	owner := func(r *http.Request) string {
		if k, ok := f.keys[r.Header.Get("x-api-key")]; ok {
			return k.owner
		}
		switch {
		case strings.Contains(r.Header.Get("Cookie"), "sess=alice"):
			return aliceID
		case strings.Contains(r.Header.Get("Cookie"), "sess=family"):
			return familyID
		}
		return ""
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/api-keys", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name        string   `json:"name"`
			Permissions []string `json:"permissions"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		id, secret := uuid.NewString(), "shk-"+uuid.NewString()
		f.keys[secret] = sharedKey{id: id, owner: owner(r), perms: body.Permissions}
		j(w, 201, map[string]any{"id": id, "name": body.Name, "secret": secret, "permissions": body.Permissions, "createdAt": ts, "updatedAt": ts})
	})
	mux.HandleFunc("DELETE /api/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.deleted = append(f.deleted, r.PathValue("id"))
		for s, k := range f.keys {
			if k.id == r.PathValue("id") {
				delete(f.keys, s)
			}
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /api/users/me", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		k, ok := f.keys[r.Header.Get("x-api-key")]
		f.mu.Unlock()
		if !ok {
			base.ServeHTTP(w, r)
			return
		}
		name := map[string]string{aliceID: "Alice", familyID: "Family"}[k.owner]
		j(w, 200, map[string]any{"id": k.owner, "name": name, "email": strings.ToLower(name) + "@x.test", "isAdmin": false})
	})
	mux.HandleFunc("GET /api/albums", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Query().Get("isOwned") != "true" || r.URL.Query().Get("name") != "" {
			base.ServeHTTP(w, r)
			return
		}
		out := []map[string]any{{"id": "99999999-9999-4999-8999-999999999992", "albumName": "Trip", "assetCount": 2}}
		out = append(out, f.albums[owner(r)]...)
		j(w, 200, out)
	})
	mux.HandleFunc("POST /api/albums", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AlbumName string `json:"albumName"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		id := uuid.NewString()
		who := owner(r)
		f.albums[who] = append(f.albums[who], map[string]any{"id": id, "albumName": body.AlbumName, "assetCount": 0})
		j(w, 201, map[string]any{"id": id, "albumName": body.AlbumName, "assetCount": 0, "createdAt": ts, "updatedAt": ts, "ownerId": who, "owner": map[string]any{"id": who, "name": "x", "email": "x@x.test", "avatarColor": "primary", "profileImagePath": "", "profileChangedAt": ts},
			"albumUsers": []any{}, "hasSharedLink": false, "shared": false, "isActivityEnabled": true, "order": "desc", "assets": []any{}, "albumThumbnailAssetId": nil, "description": ""})
	})
	dto := func(id string, p *fakePhoto) map[string]any {
		return map[string]any{"id": id, "originalFileName": p.name, "fileCreatedAt": ts, "fileModifiedAt": ts, "type": "IMAGE", "checksum": p.sum, "isTrashed": p.trashed, "isArchived": false}
	}
	mux.HandleFunc("POST /api/search/metadata", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AlbumIds []string `json:"albumIds"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		items := []any{}
		for _, album := range body.AlbumIds {
			for _, id := range f.members[album] {
				if p := f.photos[id]; p != nil && !p.trashed {
					items = append(items, dto(id, p))
				}
			}
		}
		j(w, 200, map[string]any{"assets": map[string]any{"items": items, "count": len(items), "total": len(items), "facets": []any{}}})
	})
	mux.HandleFunc("GET /api/assets/{id}/original", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		p := f.photos[r.PathValue("id")]
		who := owner(r)
		f.mu.Unlock()
		if p == nil || p.owner != who {
			http.Error(w, "no", 404)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Disposition", `attachment; filename="`+p.name+`"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(p.data)))
		w.Write(p.data)
	})
	mux.HandleFunc("GET /api/assets/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		p := f.photos[r.PathValue("id")]
		who := owner(r)
		f.mu.Unlock()
		if p == nil || p.owner != who {
			http.Error(w, "no", 404)
			return
		}
		j(w, 200, dto(r.PathValue("id"), p))
	})
	mux.HandleFunc("POST /api/assets", func(w http.ResponseWriter, r *http.Request) {
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "no", 400)
			return
		}
		var name string
		var data []byte
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			if part.FormName() == "assetData" {
				name = part.FileName()
				data, _ = io.ReadAll(part)
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		who := owner(r)
		sum := sumOf(data)
		for id, p := range f.photos {
			if p.owner == who && p.sum == sum {
				j(w, 200, map[string]any{"id": id, "status": "duplicate"})
				return
			}
		}
		id := uuid.NewString()
		f.photos[id] = &fakePhoto{owner: who, name: name, data: data, sum: sum}
		f.uploads++
		j(w, 201, map[string]any{"id": id, "status": "created"})
	})
	mux.HandleFunc("PUT /api/albums/{id}/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Ids []string `json:"ids"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []any{}
		for _, id := range body.Ids {
			have := false
			for _, m := range f.members[r.PathValue("id")] {
				have = have || m == id
			}
			if !have {
				f.members[r.PathValue("id")] = append(f.members[r.PathValue("id")], id)
			}
			out = append(out, map[string]any{"id": id, "success": true})
		}
		j(w, 200, out)
	})
	mux.Handle("/", base)
	return mux
}

// anApp is one running Handoff, with its own data file, for tests of two apps that talk to each other.
type anApp struct {
	url  string
	hub  *hub.Hub
	st   *store.Store
	srv  *httptest.Server
	fake *sharing
	user string // the cookie of the person who uses it
}

func startApp(t *testing.T, user string, share bool, allowPrivate bool) *anApp {
	t.Helper()
	fake := &sharing{keys: map[string]sharedKey{}, albums: map[string][]map[string]any{}, photos: map[string]*fakePhoto{}, members: map[string][]string{}}
	im := httptest.NewServer(fake.handler(fakeImmich()))
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
	return &anApp{url: srv.URL, hub: h, st: st, srv: srv, fake: fake, user: user}
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

func sharedOf(t *testing.T, a *anApp, owner string) []store.SharedAlbum {
	t.Helper()
	list, err := a.st.SharedAlbumsOf(context.Background(), uuid.MustParse(owner))
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func (a *anApp) keyPermsOf(t *testing.T, secret string) []string {
	a.fake.mu.Lock()
	defer a.fake.mu.Unlock()
	return a.fake.keys[secret].perms
}

func TestAnAlbumIsProposedAnsweredAndSharedWithTheChoicesOfBothSides(t *testing.T) {
	a := startApp(t, "sess=alice", true, true)
	b := startApp(t, "sess=family", true, true)
	pairApps(t, a, b)
	pa, pb := peersOf(t, a, aliceID)[0], peersOf(t, b, familyID)[0]
	p1, p2 := []byte("photo one bytes"), []byte("photo two bytes")
	a.fake.addPhoto(aliceID, tripAlbum, "one.jpg", p1)
	a.fake.addPhoto(aliceID, tripAlbum, "two.jpg", p2)
	code, body := a.as(t, "POST", "/peers/share", "peer="+pa.ID+"&name=Summer+2026&album="+tripAlbum+"&history=new&deletion=follow")
	if code != 204 {
		t.Fatalf("propose: %d %s", code, body)
	}
	sa := sharedOf(t, a, aliceID)
	sb := sharedOf(t, b, familyID)
	if len(sa) != 1 || len(sb) != 1 {
		t.Fatalf("a proposal on each side: %d %d", len(sa), len(sb))
	}
	if x := sa[0]; x.State != store.ShareProposed || !x.Proposer || x.Name != "Summer 2026" || x.AlbumName != "Trip" || x.AlbumID == uuid.Nil || x.History != store.HistoryNew || x.Deletion != store.DeleteFollow || x.Key == "" {
		t.Errorf("the proposer's record: %+v", x)
	}
	if x := sb[0]; x.State != store.ShareInvited || x.Proposer || x.TheirID != sa[0].ID || x.TheirHistory != store.HistoryNew || x.Deletion != store.DeleteFollow || x.Key != "" {
		t.Errorf("the receiver's record: %+v", x)
	}
	if got := a.keyPermsOf(t, sa[0].Key); strings.Join(got, ",") != strings.Join(immichPerms(), ",") {
		t.Errorf("the app's key for the proposer's Immich has exactly the shared-album permissions: %v", got)
	}
	// "only what is new": the photos there now are remembered so they are never sent
	if n, _ := a.st.LedgerCount(context.Background(), sa[0].ID); n != 2 {
		t.Errorf("the baseline of the proposer's album: %d", n)
	}
	if o, sent, ok, _ := a.st.LedgerHas(context.Background(), sa[0].ID, sumOf(p1)); !ok || o != store.OriginBaseline || sent {
		t.Errorf("a baseline photo: %q %v %v", o, sent, ok)
	}
	_, page := b.as(t, "GET", "/", "")
	for _, want := range []string{"Summer 2026", "Waiting for your answer", "proposes to share this album with you", `hx-post="shared/accept"`, "Decline"} {
		if !strings.Contains(page, want) {
			t.Errorf("B's page lacks %q", want)
		}
	}
	_, page = a.as(t, "GET", "/", "")
	if !strings.Contains(page, "Waiting for Family") {
		t.Error("A's page says it waits")
	}
	// somebody else cannot answer it
	req, _ := http.NewRequest("POST", b.url+"/shared/accept", strings.NewReader("shared="+sb[0].ID+"&album=new&history=all"))
	req.Header.Set("Cookie", "sess=other")
	req.Header.Set("X-Requested-With", "immich-handoff")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if res, err := http.DefaultClient.Do(req); err == nil {
		res.Body.Close()
		if res.StatusCode == 204 {
			t.Error("another person accepted")
		}
	}
	if code, body := b.as(t, "POST", "/shared/accept", "shared="+sb[0].ID+"&album=new&history=all"); code != 204 {
		t.Fatalf("accept: %d %s", code, body)
	}
	sa, sb = sharedOf(t, a, aliceID), sharedOf(t, b, familyID)
	if sa[0].State != store.ShareActive || sb[0].State != store.ShareActive {
		t.Fatalf("both active: %s %s", sa[0].State, sb[0].State)
	}
	if sa[0].TheirID != sb[0].ID || sb[0].TheirID != sa[0].ID || sa[0].TheirHistory != store.HistoryAll || sb[0].History != store.HistoryAll || sb[0].TheirHistory != store.HistoryNew {
		t.Errorf("each knows the other's id and choice: %+v %+v", sa[0], sb[0])
	}
	if sb[0].AlbumName != "Summer 2026" || sb[0].AlbumID == uuid.Nil || sb[0].Key == "" {
		t.Errorf("the receiver's own album and key: %+v", sb[0])
	}
	if n, _ := b.st.LedgerCount(context.Background(), sb[0].ID); n != 0 {
		t.Errorf("sharing everything remembers nothing as baseline: %d", n)
	}
	if code, _ := b.as(t, "POST", "/shared/accept", "shared="+sb[0].ID+"&album=new&history=all"); code == 204 {
		t.Error("answering twice")
	}
	_, page = a.as(t, "GET", "/", "")
	for _, want := range []string{"Agreed", "You share: only what is added from now on", "Family shares: everything in the album now"} {
		if !strings.Contains(page, want) {
			t.Errorf("A's page lacks %q", want)
		}
	}
	// ending it from one side ends it for the other and deletes the key
	if code, _ := a.as(t, "POST", "/shared/leave", "shared="+sa[0].ID); code != 204 {
		t.Fatalf("leave: %d", code)
	}
	if len(sharedOf(t, a, aliceID)) != 0 {
		t.Error("A forgot it")
	}
	if got := sharedOf(t, b, familyID); len(got) != 1 || got[0].State != store.ShareLeft {
		t.Fatalf("B is told: %+v", got)
	}
	if len(a.fake.deleted) != 1 {
		t.Errorf("A's key is deleted from A's Immich: %v", a.fake.deleted)
	}
	if code, _ := b.as(t, "POST", "/shared/leave", "shared="+sb[0].ID); code != 204 || len(b.fake.deleted) != 1 || len(sharedOf(t, b, familyID)) != 0 {
		t.Errorf("B removes it, and its key goes: %d %v", code, b.fake.deleted)
	}
	_ = pb
}

func immichPerms() []string {
	out := []string{}
	for _, p := range immich.SharedAlbumPermissions {
		out = append(out, string(p))
	}
	return out
}

func TestAProposalCanBeDeclinedAndTheChoicesAreChecked(t *testing.T) {
	a := startApp(t, "sess=alice", true, true)
	b := startApp(t, "sess=family", true, true)
	pairApps(t, a, b)
	pa := peersOf(t, a, aliceID)[0]
	for name, form := range map[string]string{
		"no name":         "peer=" + pa.ID + "&name=&album=new&history=new",
		"a long name":     "peer=" + pa.ID + "&name=" + strings.Repeat("x", 61) + "&album=new&history=new",
		"no history":      "peer=" + pa.ID + "&name=x&album=new",
		"another history": "peer=" + pa.ID + "&name=x&album=new&history=some",
		"a deletion rule": "peer=" + pa.ID + "&name=x&album=new&history=new&deletion=everything",
		"not a peer":      "peer=" + uuid.NewString() + "&name=x&album=new&history=new",
	} {
		if code, _ := a.as(t, "POST", "/peers/share", form); code != 409 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if len(sharedOf(t, a, aliceID)) != 0 || len(a.fake.keys) != 0 {
		t.Errorf("a refused proposal leaves nothing behind: %d shared, %d keys", len(sharedOf(t, a, aliceID)), len(a.fake.keys))
	}
	if code, body := a.as(t, "POST", "/peers/share", "peer="+pa.ID+"&name=Trip&album=99999999-9999-4999-8999-999999999992&history=all&deletion=nothing"); code != 204 {
		t.Fatalf("an existing album: %d %s", code, body)
	}
	if got := sharedOf(t, a, aliceID); len(got) != 1 || got[0].AlbumName != "Trip" {
		t.Errorf("their own album: %+v", got)
	}
	if code, _ := a.as(t, "POST", "/peers/share", "peer="+pa.ID+"&name=Other&album=88888888-8888-4888-8888-888888888888&history=all"); code != 409 {
		t.Errorf("an album that is not theirs: %d", code)
	}
	sb := sharedOf(t, b, familyID)
	if code, _ := b.as(t, "POST", "/shared/decline", "shared="+sb[0].ID); code != 204 {
		t.Fatalf("decline: %d", code)
	}
	if len(sharedOf(t, b, familyID)) != 0 {
		t.Error("B forgot it")
	}
	if got := sharedOf(t, a, aliceID); got[0].State != store.ShareDeclined {
		t.Errorf("A is told: %+v", got)
	}
	// a paused connection takes no proposals
	b.as(t, "POST", "/peers/pause", "peer="+peersOf(t, b, familyID)[0].ID+"&paused=1")
	if code, _ := a.as(t, "POST", "/peers/share", "peer="+pa.ID+"&name=Late&album=new&history=new"); code != 409 {
		t.Errorf("while the other side has paused: %d", code)
	}
}

func TestEndingAConnectionEndsItsSharedAlbumsAndDeletesTheirKeys(t *testing.T) {
	a := startApp(t, "sess=alice", true, true)
	b := startApp(t, "sess=family", true, true)
	pairApps(t, a, b)
	pa := peersOf(t, a, aliceID)[0]
	a.as(t, "POST", "/peers/share", "peer="+pa.ID+"&name=One&album=new&history=new")
	b.as(t, "POST", "/shared/accept", "shared="+sharedOf(t, b, familyID)[0].ID+"&album=new&history=new")
	if code, _ := a.as(t, "POST", "/peers/remove", "peer="+pa.ID); code != 204 {
		t.Fatal(code)
	}
	if len(sharedOf(t, a, aliceID)) != 0 || len(a.fake.deleted) != 1 {
		t.Errorf("A forgets the shared albums and deletes their keys: %d %v", len(sharedOf(t, a, aliceID)), a.fake.deleted)
	}
	got := sharedOf(t, b, familyID)
	if len(got) != 1 || got[0].State != store.ShareLeft {
		t.Errorf("B sees the sharing ended: %+v", got)
	}
}

const tripAlbum = "99999999-9999-4999-8999-999999999992"

// sharedPair makes two paired apps with one agreed album (A's Trip, B's new one) and the photos given to A's Trip.
func sharedPair(t *testing.T, aHistory, bHistory string, photos ...[]byte) (a, b *anApp, sa, sb store.SharedAlbum) {
	t.Helper()
	a = startApp(t, "sess=alice", true, true)
	b = startApp(t, "sess=family", true, true)
	pairApps(t, a, b)
	for i, p := range photos {
		a.fake.addPhoto(aliceID, tripAlbum, "a"+strconv.Itoa(i)+".jpg", p)
	}
	pa := peersOf(t, a, aliceID)[0]
	if code, body := a.as(t, "POST", "/peers/share", "peer="+pa.ID+"&name=Trip&album="+tripAlbum+"&history="+aHistory+"&deletion=nothing"); code != 204 {
		t.Fatalf("propose: %d %s", code, body)
	}
	if code, body := b.as(t, "POST", "/shared/accept", "shared="+sharedOf(t, b, familyID)[0].ID+"&album=new&history="+bHistory); code != 204 {
		t.Fatalf("accept: %d %s", code, body)
	}
	return a, b, sharedOf(t, a, aliceID)[0], sharedOf(t, b, familyID)[0]
}

func TestPhotosCrossInBothDirectionsOnceAndNeverComeBack(t *testing.T) {
	one, two := []byte("the first photo"), []byte("the second photo")
	a, b, sa, sb := sharedPair(t, "all", "new", one, two)
	ctx := context.Background()
	if err := a.hub.SyncShared(ctx, sa.ID); err != nil {
		t.Fatal(err)
	}
	got := b.fake.inAlbum(sb.AlbumID.String())
	if len(got) != 2 || got[0].sum != sumOf(one) || got[1].sum != sumOf(two) || string(got[0].data) != "the first photo" {
		t.Fatalf("both photos reached B's album, whole: %+v", got)
	}
	if got[0].name != "a0.jpg" {
		t.Errorf("with their names: %q", got[0].name)
	}
	if x, _ := a.st.SharedAlbumByID(ctx, sa.ID); x.Sent != 2 || x.LastSync.IsZero() || x.Problem != "" {
		t.Errorf("A counts what it sent: %+v", x)
	}
	if x, _ := b.st.SharedAlbumByID(ctx, sb.ID); x.Received != 2 {
		t.Errorf("B counts what it received: %+v", x)
	}
	// B's pass finds the two photos in its album, knows they arrived and sends nothing back
	before := a.fake.uploads
	if err := b.hub.SyncShared(ctx, sb.ID); err != nil {
		t.Fatal(err)
	}
	if a.fake.uploads != before {
		t.Errorf("nothing came back to A: %d uploads", a.fake.uploads-before)
	}
	if x, _ := b.st.SharedAlbumByID(ctx, sb.ID); x.Sent != 0 {
		t.Errorf("B sent %d", x.Sent)
	}
	// a photo B adds goes to A
	three := []byte("a photo from the other server")
	b.fake.addPhoto(familyID, sb.AlbumID.String(), "b0.jpg", three)
	if err := b.hub.SyncShared(ctx, sb.ID); err != nil {
		t.Fatal(err)
	}
	inA := a.fake.inAlbum(tripAlbum)
	if len(inA) != 3 || inA[2].sum != sumOf(three) {
		t.Fatalf("A's album has the photo from B: %d", len(inA))
	}
	// and a pass again changes nothing, on either side
	a.hub.SyncShared(ctx, sa.ID)
	b.hub.SyncShared(ctx, sb.ID)
	if len(a.fake.inAlbum(tripAlbum)) != 3 || len(b.fake.inAlbum(sb.AlbumID.String())) != 3 || a.fake.countByChecksum(aliceID, sumOf(three)) != 1 || b.fake.countByChecksum(familyID, sumOf(one)) != 1 {
		t.Error("the albums stay equal, with no duplicates")
	}
	_, page := a.as(t, "GET", "/", "")
	for _, want := range []string{"2 photos sent", "1 photo received", "Last looked at", "Sync now"} {
		if !strings.Contains(page, want) {
			t.Errorf("A's page lacks %q", want)
		}
	}
}

func TestOnlyWhatIsNewIsSentWhenTheSideChoseThat(t *testing.T) {
	old := []byte("an old photo")
	a, b, sa, sb := sharedPair(t, "new", "new", old)
	ctx := context.Background()
	a.hub.SyncShared(ctx, sa.ID)
	if len(b.fake.inAlbum(sb.AlbumID.String())) != 0 {
		t.Fatal("what was there before sharing is not sent")
	}
	fresh := []byte("a new photo")
	a.fake.addPhoto(aliceID, tripAlbum, "new.jpg", fresh)
	a.hub.SyncShared(ctx, sa.ID)
	got := b.fake.inAlbum(sb.AlbumID.String())
	if len(got) != 1 || got[0].sum != sumOf(fresh) {
		t.Errorf("only the new photo: %+v", got)
	}
}

func TestTheSamePhotoOnBothSidesIsNotDuplicatedAndATrashedCopyStaysInTheTrash(t *testing.T) {
	same, gone := []byte("a photo both have"), []byte("a photo B deleted")
	a, b, sa, sb := sharedPair(t, "all", "new", same, gone)
	ctx := context.Background()
	// B already has `same` in its library (not in the album) and has put `gone` in its trash
	b.fake.addPhoto(familyID, "", "mine.jpg", same)
	id := b.fake.addPhoto(familyID, "", "deleted.jpg", gone)
	b.fake.mu.Lock()
	b.fake.photos[id].trashed = true
	b.fake.mu.Unlock()
	if err := a.hub.SyncShared(ctx, sa.ID); err != nil {
		t.Fatal(err)
	}
	if n := b.fake.countByChecksum(familyID, sumOf(same)); n != 1 {
		t.Errorf("the photo B already had is not stored again: %d", n)
	}
	inB := b.fake.inAlbum(sb.AlbumID.String())
	if len(inB) != 1 || inB[0].sum != sumOf(same) {
		t.Errorf("it is in B's album, and the one B deleted is not: %+v", inB)
	}
	b.fake.mu.Lock()
	still := b.fake.photos[id].trashed
	b.fake.mu.Unlock()
	if !still {
		t.Error("the copy B deleted is still in the trash")
	}
	if o, sent, ok, _ := b.st.LedgerHas(ctx, sb.ID, sumOf(gone)); !ok || o != store.OriginArrived || !sent {
		t.Errorf("it is remembered so it is not sent back: %q %v %v", o, sent, ok)
	}
}

func TestNothingMovesWhileEitherSideHasPausedOrForAFileThatIsTooLarge(t *testing.T) {
	a, b, sa, sb := sharedPair(t, "all", "new", []byte("one photo"))
	ctx := context.Background()
	pb := peersOf(t, b, familyID)[0]
	b.as(t, "POST", "/peers/pause", "peer="+pb.ID+"&paused=1")
	a.hub.SyncShared(ctx, sa.ID)
	if len(b.fake.inAlbum(sb.AlbumID.String())) != 0 {
		t.Fatal("B paused: nothing is sent to it")
	}
	b.as(t, "POST", "/peers/pause", "peer="+pb.ID+"&paused=0")
	pa := peersOf(t, a, aliceID)[0]
	a.as(t, "POST", "/peers/pause", "peer="+pa.ID+"&paused=1")
	a.hub.SyncShared(ctx, sa.ID)
	if len(b.fake.inAlbum(sb.AlbumID.String())) != 0 {
		t.Fatal("A paused: nothing is sent")
	}
	a.as(t, "POST", "/peers/pause", "peer="+pa.ID+"&paused=0")
	if err := a.hub.SyncShared(ctx, sa.ID); err != nil {
		t.Fatal(err)
	}
	if len(b.fake.inAlbum(sb.AlbumID.String())) != 1 {
		t.Error("resumed: it goes")
	}
}

func TestAFileLargerThanTheLimitIsLeftOutWithAReasonAndNotTriedAgain(t *testing.T) {
	small, big := []byte("small"), []byte(strings.Repeat("big!", 1000))
	a, b, sa, sb := sharedPair(t, "all", "new", small, big)
	a.hub.SharedMaxFile = 2000
	ctx := context.Background()
	err := a.hub.SyncShared(ctx, sa.ID)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("a reason: %v", err)
	}
	got := b.fake.inAlbum(sb.AlbumID.String())
	if len(got) != 1 || got[0].sum != sumOf(small) {
		t.Errorf("the small one went: %+v", got)
	}
	if o, sent, _, _ := a.st.LedgerHas(ctx, sa.ID, sumOf(big)); o != store.OriginSkipped || !sent {
		t.Errorf("remembered as left out: %q %v", o, sent)
	}
	if err := a.hub.SyncShared(ctx, sa.ID); err != nil {
		t.Errorf("not tried again: %v", err)
	}
}

func TestThePhotoEndpointNeedsASignatureAndTheRightChecksum(t *testing.T) {
	a, b, _, sb := sharedPair(t, "all", "new")
	ctx := context.Background()
	pa := peersOf(t, a, aliceID)[0]
	data := []byte("some bytes of a photo")
	q := func(sum string) string {
		return "/peer/v1/album/photo?to=" + sb.ID + "&checksum=" + url.QueryEscape(sum) + "&name=x.jpg&taken=2026-06-14T10:00:00Z&size=" + strconv.Itoa(len(data))
	}
	// unsigned
	res, err := http.Post(b.url+q(sumOf(data)), "application/octet-stream", strings.NewReader(string(data)))
	if err != nil || res.StatusCode != 401 {
		t.Fatalf("unsigned: %v %v", res, err)
	}
	res.Body.Close()
	stream := func(path string, secret string) int {
		code, _, err := peer.Stream(ctx, http.DefaultClient, b.url, pa.TheirID, secret, "POST", path, strings.NewReader(string(data)), int64(len(data)), nil)
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	if code := stream(q(sumOf(data)), "wrong-secret-wrong-secret-wrong-secret-wrong"); code != 401 {
		t.Errorf("wrong secret: %d", code)
	}
	if code := stream(q("not-the-checksum"), pa.Secret); code != 400 {
		t.Errorf("a checksum that is not the photo's: %d", code)
	}
	if n := len(b.fake.inAlbum(sb.AlbumID.String())); n != 0 {
		t.Errorf("a photo with the wrong checksum is not put in the album: %d", n)
	}
	if code := stream(strings.Replace(q(sumOf(data)), sb.ID, uuid.NewString(), 1), pa.Secret); code != 404 {
		t.Errorf("an album that does not exist: %d", code)
	}
	if code := stream(q(sumOf(data)), pa.Secret); code != 200 {
		t.Errorf("a right one: %d", code)
	}
	if n := len(b.fake.inAlbum(sb.AlbumID.String())); n != 1 {
		t.Errorf("it is in the album: %d", n)
	}
	// big bodies pass the general size limit of a page
	big := strings.NewReader(strings.Repeat("x", 3<<20))
	code, _, err := peer.Stream(ctx, http.DefaultClient, b.url, pa.TheirID, pa.Secret, "POST", "/peer/v1/album/photo?to="+sb.ID+"&checksum=zzz&name=big.jpg&size=3145728", big, 3<<20, nil)
	if err != nil || code == 413 {
		t.Errorf("a 3 MB photo is not cut by the page's limit: %d %v", code, err)
	}
}

func TestSyncNowLooksAtOnceAndOnlyForTheOwner(t *testing.T) {
	a, b, sa, sb := sharedPair(t, "all", "new", []byte("a photo to send now"))
	if code, _ := a.as(t, "POST", "/shared/sync", "shared="+sa.ID); code != 204 {
		t.Fatalf("sync now: %d", code)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(b.fake.inAlbum(sb.AlbumID.String())) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if len(b.fake.inAlbum(sb.AlbumID.String())) != 1 {
		t.Error("it went")
	}
	req, _ := http.NewRequest("POST", a.url+"/shared/sync", strings.NewReader("shared="+sa.ID))
	req.Header.Set("Cookie", "sess=other")
	req.Header.Set("X-Requested-With", "immich-handoff")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if res, err := http.DefaultClient.Do(req); err == nil {
		res.Body.Close()
		if res.StatusCode == 204 {
			t.Error("another person looked")
		}
	}
}

func TestThePersonIsToldWhenPhotosArriveAndOnceWhenSharingStopsWorkingAndWhenItWorksAgain(t *testing.T) {
	a, b, sa, sb := sharedPair(t, "all", "new", []byte("a photo"), []byte("another photo"))
	ctx := context.Background()
	if err := a.hub.SyncShared(ctx, sa.ID); err != nil {
		t.Fatal(err)
	}
	acts, _ := b.hub.Activity(ctx, uuid.MustParse(familyID), 10)
	found := false
	for _, x := range acts {
		found = found || (x.Kind == "share" && strings.Contains(x.Text, "photos arrived from") && strings.Contains(x.Text, `"n":2`) && strings.Contains(x.Text, `"other":"Alice"`))
	}
	if !found {
		t.Errorf("B's person is told, once for the whole pass: %+v", acts)
	}
	// B stops answering (its sharing is switched off): A says so once, not at every look
	b.hub.Share = false
	a.fake.addPhoto(aliceID, tripAlbum, "late.jpg", []byte("a late photo"))
	if err := a.hub.SyncShared(ctx, sa.ID); err == nil {
		t.Fatal("a problem")
	}
	a.hub.SyncShared(ctx, sa.ID)
	count := func(kind, word string) int {
		acts, _ := a.hub.Activity(ctx, uuid.MustParse(aliceID), 50)
		n := 0
		for _, x := range acts {
			if x.Kind == kind && strings.Contains(x.Text, word) {
				n++
			}
		}
		return n
	}
	if n := count("sharing_problem", "ran into a problem"); n != 1 {
		t.Errorf("told once: %d", n)
	}
	_, page := a.as(t, "GET", "/", "")
	for _, want := range []string{"The last look found a problem", "Problem"} {
		if !strings.Contains(page, want) {
			t.Errorf("A's page lacks %q", want)
		}
	}
	b.hub.Share = true
	if err := a.hub.SyncShared(ctx, sa.ID); err != nil {
		t.Fatal(err)
	}
	if n := count("share", "works again"); n != 1 {
		t.Errorf("told when it works again: %d", n)
	}
	_ = sb
}

func TestAProposalAndAnUnreachableConnectionAreOnTheOverview(t *testing.T) {
	a := startApp(t, "sess=alice", true, true)
	b := startApp(t, "sess=family", true, true)
	pairApps(t, a, b)
	a.as(t, "POST", "/peers/share", "peer="+peersOf(t, a, aliceID)[0].ID+"&name=Summer&album=new&history=new")
	_, page := b.as(t, "GET", "/", "")
	for _, want := range []string{"Alice proposes to share Summer", "Open the Services tab to look at it and answer."} {
		if !strings.Contains(page, want) {
			t.Errorf("B's overview lacks %q", want)
		}
	}
	b.srv.Close()
	a.hub.PeerMaintenance(context.Background())
	_, page = a.as(t, "GET", "/", "")
	if !strings.Contains(page, "Shared albums wait until it can") {
		t.Errorf("A's overview says the other app is not reachable")
	}
}
