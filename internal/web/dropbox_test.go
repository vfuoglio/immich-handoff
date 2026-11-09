package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/vault"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

var jpegBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0}, make([]byte, 1000)...)

// storing is an Immich that takes uploads and tags, and remembers what it was given.
type storing struct {
	mu        sync.Mutex
	names     []string
	tags      map[string]string   // tag id -> name
	assetTags map[string][]string // asset id -> tag names
	trashed   map[string]bool
}

func (s *storing) handler() http.Handler {
	mux := http.NewServeMux()
	j := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST /api/assets", func(w http.ResponseWriter, r *http.Request) {
		mr, _ := r.MultipartReader()
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FormName() == "assetData" {
				io.Copy(io.Discard, p)
				id := uuid.NewString()
				s.mu.Lock()
				s.names = append(s.names, p.FileName())
				if s.assetTags == nil {
					s.assetTags = map[string][]string{}
				}
				s.assetTags[id] = nil
				s.mu.Unlock()
				j(w, 201, map[string]any{"id": id, "status": "created"})
				return
			}
		}
		http.Error(w, "no", 400)
	})
	mux.HandleFunc("PUT /api/tags", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Tags []string }
		json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.tags == nil {
			s.tags = map[string]string{}
		}
		out := []any{}
		for _, n := range body.Tags {
			id := uuid.NewString()
			s.tags[id] = n
			out = append(out, map[string]any{"id": id, "name": n, "value": n, "createdAt": "2025-01-02T03:04:05.000Z", "updatedAt": "2025-01-02T03:04:05.000Z"})
		}
		j(w, 200, out)
	})
	mux.HandleFunc("GET /api/assets/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		names, ok := s.assetTags[r.PathValue("id")]
		if !ok {
			http.Error(w, "no", 404)
			return
		}
		tags := []any{}
		for _, n := range names {
			tags = append(tags, map[string]any{"id": uuid.NewString(), "name": n, "value": n, "createdAt": "2025-01-02T03:04:05.000Z", "updatedAt": "2025-01-02T03:04:05.000Z"})
		}
		j(w, 200, map[string]any{"id": r.PathValue("id"), "isTrashed": s.trashed[r.PathValue("id")], "tags": tags, "originalFileName": "x.jpg", "fileCreatedAt": "2025-01-02T03:04:05.000Z", "fileModifiedAt": "2025-01-02T03:04:05.000Z"})
	})
	mux.HandleFunc("DELETE /api/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Ids   []string
			Force bool
		}
		json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		if s.trashed == nil {
			s.trashed = map[string]bool{}
		}
		for _, id := range body.Ids {
			s.trashed[id] = !body.Force
		}
		s.mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) { j(w, 200, []any{}) })
	mux.HandleFunc("PUT /api/tags/{id}/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Ids []string }
		json.NewDecoder(r.Body).Decode(&body)
		out := []any{}
		s.mu.Lock()
		for _, id := range body.Ids {
			s.assetTags[id] = append(s.assetTags[id], s.tags[r.PathValue("id")])
			out = append(out, map[string]any{"id": id, "success": true})
		}
		s.mu.Unlock()
		j(w, 200, out)
	})
	return mux
}

// withDrop gives the page a store, a hub whose keys reach a storing Immich, and one drop box owned by the "family" user.
func withDrop(t *testing.T, st **store.Store, im *storing, box store.DropBox) func(*web.Server) {
	return func(srv *web.Server) {
		v, _ := vault.New("0123456789abcdef0123")
		s, err := store.Open(t.TempDir()+"/h.db", v)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		*st = s
		store := httptest.NewServer(im.handler())
		t.Cleanup(store.Close)
		if box.Name != "" {
			box.OwnerID, box.OwnerName, box.KeyID, box.Key = uuid.MustParse(familyID), "Family", uuid.New(), "k-drop"
			if _, err := s.AddDropBox(context.Background(), box); err != nil {
				t.Fatal(err)
			}
		}
		h := &hub.Hub{Static: srv.S.Services, Store: s, Log: srv.Log, Options: hub.Options{InboxTag: "inbox"},
			Connect: func(key string) (*immich.API, error) { return immich.New(srv.BaseURL, immich.Auth{APIKey: key}, nil) }}
		h.Connect = func(key string) (*immich.API, error) { return immich.New(store.URL, immich.Auth{APIKey: key}, nil) }
		h.StageDir = t.TempDir()
		if err := h.Reload(context.Background()); err != nil {
			t.Fatal(err)
		}
		srv.Hub = h
		srv.S.Provider = h
	}
}

func sendFilesAs(t *testing.T, url string, header map[string]string, files map[string][]byte) (int, string, []*http.Cookie) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, data := range files {
		fw, _ := mw.CreateFormFile("files", name)
		fw.Write(data)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", url, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Cookies()
}

func sendFiles(t *testing.T, url string, fields map[string]string, files map[string][]byte) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields { // fields first: a password has to come before the files
		mw.WriteField(k, v)
	}
	for name, data := range files {
		fw, _ := mw.CreateFormFile("files", name)
		fw.Write(data)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", url, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestAGuestSendsPhotosThroughTheLinkWithoutSigningIn(t *testing.T) {
	var st *store.Store
	im := &storing{}
	token := strings.Repeat("ab", 16)
	app, _ := setup(t, withDrop(t, &st, im, store.DropBox{Token: token, Name: "Wedding", Message: "Send us your photos", InboxTag: "inbox/Wedding"}))

	// the page needs no login, shows the name and the message, and nothing from the library
	code, body := do(t, "GET", app.URL+"/drop/"+token, "", nil)
	if code != 200 || !strings.Contains(body, "Wedding") || !strings.Contains(body, "Send us your photos") || !strings.Contains(body, `type="file"`) {
		t.Fatalf("guest page: %d %.200s", code, body)
	}
	if strings.Contains(body, "Photos you tag in Immich") || strings.Contains(body, `id="live"`) {
		t.Error("a guest must not see the parts of the page that are for people with an account")
	}
	if !strings.Contains(body, `<base href="../"`) || !strings.Contains(body, `action="drop/`+token+`/upload"`) {
		t.Error("the page's links must work from /drop/<token>")
	}

	code, body = sendFiles(t, app.URL+"/drop/"+token+"/upload", nil, map[string][]byte{"a.jpg": jpegBytes, "bad.jpg": []byte("<html>not a photo</html>")})
	if code != 200 || !strings.Contains(body, "Received") || !strings.Contains(body, "Not a photo or video this box takes") {
		t.Fatalf("upload: %d %s", code, body)
	}
	if len(im.names) != 1 || im.names[0] != "a.jpg" {
		t.Errorf("stored: %v", im.names)
	}
	if got, _ := st.DropBoxByToken(context.Background(), token); got.Files != 1 {
		t.Errorf("counter: %+v", got)
	}
}

func TestEveryClosedLinkAnswersTheSame(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("cd", 16)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Gone", InboxTag: "inbox/Gone"}))
	postForm(t, app.URL+"/dropbox/pause", "sess=family", "service="+firstBox(t, st)+"&paused=1")
	_, paused := do(t, "GET", app.URL+"/drop/"+token, "", nil)
	_, unknown := do(t, "GET", app.URL+"/drop/"+strings.Repeat("ef", 16), "", nil)
	_, short := do(t, "GET", app.URL+"/drop/x", "", nil)
	if paused != unknown || unknown != short || !strings.Contains(paused, "This link is not open") {
		t.Errorf("paused, unknown and malformed links must look alike:\n%.300s\n%.300s", paused, unknown)
	}
	if code, _ := sendFiles(t, app.URL+"/drop/"+token+"/upload", nil, map[string][]byte{"a.jpg": jpegBytes}); code != 404 {
		t.Errorf("upload to a paused box: %d", code)
	}
}

func firstBox(t *testing.T, st *store.Store) string {
	t.Helper()
	all, err := st.DropBoxes(context.Background())
	if err != nil || len(all) == 0 {
		t.Fatal("no drop box", err)
	}
	return all[0].ID
}

func TestAPasswordBoxTakesNothingWithoutIt(t *testing.T) {
	var st *store.Store
	im := &storing{}
	token := strings.Repeat("12", 16)
	pw, _ := hubPassword()
	app, _ := setup(t, withDrop(t, &st, im, store.DropBox{Token: token, Name: "Locked", InboxTag: "inbox/Locked", Password: pw}))
	if _, body := do(t, "GET", app.URL+"/drop/"+token, "", nil); !strings.Contains(body, `type="password"`) {
		t.Error("the page must ask for the password")
	}
	for _, f := range []map[string]string{nil, {"password": "wrong"}} {
		if code, _ := sendFiles(t, app.URL+"/drop/"+token+"/upload", f, map[string][]byte{"a.jpg": jpegBytes}); code != 403 {
			t.Errorf("%v: %d, want 403", f, code)
		}
	}
	if len(im.names) != 0 {
		t.Fatalf("nothing may be stored without the password: %v", im.names)
	}
	if code, _ := sendFiles(t, app.URL+"/drop/"+token+"/upload", map[string]string{"password": "swordfish"}, map[string][]byte{"a.jpg": jpegBytes}); code != 200 || len(im.names) != 1 {
		t.Errorf("right password: %d %v", code, im.names)
	}
}

func TestTheOwnerSeesTheirBoxAndOthersDoNot(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("34", 16)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Reunion", InboxTag: "inbox/Reunion"}))
	owner := get(t, app.URL, "sess=family")
	if !strings.Contains(owner, "Reunion") || !strings.Contains(owner, `data-link="drop/`+token+`"`) || !strings.Contains(owner, "Revoke drop box") {
		t.Error("the owner must see the box with its link")
	}
	if other := get(t, app.URL, "sess=other"); strings.Contains(other, token) || strings.Contains(other, "Reunion") {
		t.Error("another person must not see the box or its link")
	}
	id := firstBox(t, st)
	if code, _ := postForm(t, app.URL+"/dropbox/revoke", "sess=other", "service="+id); code != 409 {
		t.Errorf("another person revoking: %d, want 409", code)
	}
	if code, _ := postForm(t, app.URL+"/dropbox/revoke", "sess=family", "service="+id); code != 204 {
		t.Errorf("owner revoking: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/drop/"+token, "", nil); code != 404 {
		t.Errorf("a revoked link: %d", code)
	}
	if owner := get(t, app.URL, "sess=family"); !strings.Contains(owner, "Revoked") || strings.Contains(owner, `data-link="drop/`) {
		t.Error("a revoked box keeps its log but loses its link")
	}
}

func TestGuestRoutesAreOpenButTheOwnersAreNot(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{}))
	for _, path := range []string{"/dropbox", "/dropbox/pause", "/dropbox/revoke"} {
		if code, _ := postForm(t, app.URL+path, "", "service=x"); code != 401 {
			t.Errorf("%s without signing in: %d, want 401", path, code)
		}
	}
	// a form posted from another site cannot make a box
	if code, _ := do(t, "POST", app.URL+"/dropbox", "sess=family", nil); code != 400 {
		t.Errorf("without the CSRF header: %d, want 400", code)
	}
}

// hubPassword is the stored form of the password "swordfish", made the way a drop box makes it.
func hubPassword() (string, error) { return hub.HashDropPassword("swordfish") }

func TestAnApprovalBoxHoldsFilesAndOnlyItsHostsSeeThem(t *testing.T) {
	var st *store.Store
	im := &storing{}
	token := strings.Repeat("56", 16)
	app, _ := setup(t, withDrop(t, &st, im, store.DropBox{Token: token, Name: "Careful", InboxTag: "inbox/Careful", Approve: true}))
	id := firstBox(t, st)

	if _, page := do(t, "GET", app.URL+"/drop/"+token, "", nil); !strings.Contains(page, "looked at before it is kept") {
		t.Error("guests are told their files are looked at first")
	}
	code, body := sendFiles(t, app.URL+"/drop/"+token+"/upload", nil, map[string][]byte{"a.jpg": jpegBytes})
	if code != 200 || !strings.Contains(body, "waiting for approval") || len(im.names) != 0 {
		t.Fatalf("upload: %d %.200s stored %v", code, body, im.names)
	}

	owner := get(t, app.URL, "sess=family")
	m := regexp.MustCompile(`src="dropbox/pending/([0-9a-f-]{36})"`).FindStringSubmatch(owner)
	if m == nil || !strings.Contains(owner, "Waiting for approval") || !strings.Contains(owner, "Approve all") {
		t.Fatalf("the owner must see the file waiting: %.300s", owner)
	}
	// the file is shown to the people who decide and to nobody else
	for who, want := range map[string]int{"sess=family": 200, "sess=other": 404, "": 401} {
		if code, _ := do(t, "GET", app.URL+"/dropbox/pending/"+m[1], who, nil); code != want {
			t.Errorf("waiting file as %q: %d, want %d", who, code, want)
		}
	}
	// a co-host who only views sees that something waits, not what
	st.SetDropHost(context.Background(), id, store.DropHost{UserID: uuid.MustParse(otherID), Name: "Other", Email: "other@x.test", Role: store.HostView})
	view := get(t, app.URL, "sess=other")
	if !strings.Contains(view, "Careful") || !strings.Contains(view, "You can see this drop box") || strings.Contains(view, "dropbox/pending/") || strings.Contains(view, `data-link="drop/`) || strings.Contains(view, "Revoke drop box") {
		t.Errorf("what a viewer sees: %.600s", view)
	}
	if code, _ := postForm(t, app.URL+"/dropbox/approve", "sess=other", "service="+id+"&all=1"); code != 409 {
		t.Errorf("a viewer approving: %d, want 409", code)
	}
	// nothing chosen is an error, not "approve everything"
	if code, _ := postForm(t, app.URL+"/dropbox/approve", "sess=family", "service="+id); code != 409 {
		t.Errorf("approving nothing: %d, want 409", code)
	}
	// a manager approves; the file is stored and the queue empties
	st.SetDropHost(context.Background(), id, store.DropHost{UserID: uuid.MustParse(otherID), Name: "Other", Email: "other@x.test", Role: store.HostManage})
	if code, b := postForm(t, app.URL+"/dropbox/approve", "sess=other", "service="+id+"&item="+m[1]); code != 204 {
		t.Fatalf("approve: %d %s", code, b)
	}
	if len(im.names) != 1 {
		t.Errorf("stored: %v", im.names)
	}
	if after := get(t, app.URL, "sess=family"); strings.Contains(after, "dropbox/pending/") {
		t.Error("the queue must be empty")
	}
	// a manager may pause but not revoke
	if code, _ := postForm(t, app.URL+"/dropbox/pause", "sess=other", "service="+id+"&paused=1"); code != 204 {
		t.Errorf("a manager pausing: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/dropbox/revoke", "sess=other", "service="+id); code != 409 {
		t.Errorf("a manager revoking: %d, want 409", code)
	}
}

var pngBytes = append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, make([]byte, 200)...)

func TestTheGuestPageHasItsOwnLookNameAndWaysIn(t *testing.T) {
	var st *store.Store
	token, short := strings.Repeat("78", 16), "abcdefghjk"
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Short: short, Name: "Gala", Message: "Hello guests", InboxTag: "inbox/Gala", Accent: "#aa3355", Lang: "it", MaxDim: 2400}))
	st.SetDropCover(context.Background(), firstBox(t, st), "image/png", pngBytes)

	// the short link opens the same page, in the box's language, with its colour, cover, manifest and shrinking size
	for _, way := range []string{"/d/" + short, "/drop/" + token} {
		code, p := do(t, "GET", app.URL+way, "", nil)
		if code != 200 || !strings.Contains(p, "Hello guests") || !strings.Contains(p, "Invia") {
			t.Fatalf("%s: %d %.300s", way, code, p)
		}
		id := short
		if way == "/drop/"+token {
			id = token
		}
		for _, want := range []string{`href="drop/` + id + `/manifest.webmanifest"`, `href="drop/` + id + `/theme.css"`, `src="drop/` + id + `/cover"`, `data-maxdim="2400"`, `capture="environment"`, `<base href="../"`} {
			if !strings.Contains(p, want) {
				t.Errorf("%s lacks %s", way, want)
			}
		}
	}
	// a guest who chose English gets English, whatever the box says
	if _, p := do(t, "GET", app.URL+"/d/"+short, "", map[string]string{"Cookie": "handoff-lang=en"}); !strings.Contains(p, "Choose your photos") {
		t.Error("the guest's own language choice wins")
	}
	res, _ := http.Get(app.URL + "/drop/" + short + "/theme.css")
	css, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/css") || !strings.Contains(string(css), "--accent:#aa3355") || !strings.Contains(string(css), "--on-accent:#ffffff") {
		t.Errorf("theme: %d %s", res.StatusCode, css)
	}
	res, _ = http.Get(app.URL + "/drop/" + token + "/cover")
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/png" {
		t.Errorf("cover: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	res, _ = http.Get(app.URL + "/drop/" + token + "/manifest.webmanifest")
	var m struct {
		Name     string `json:"name"`
		StartURL string `json:"start_url"`
		Scope    string `json:"scope"`
		Theme    string `json:"theme_color"`
	}
	json.NewDecoder(res.Body).Decode(&m)
	if res.Header.Get("Content-Type") != "application/manifest+json" || m.Name != "Gala" || m.StartURL != "../"+token || m.Scope != "../" || m.Theme != "#aa3355" {
		t.Errorf("manifest: %+v", m)
	}
	// an upload through the short link works, and the log marks the guest the same way as the long link would
	if code, _, _ := sendFilesAs(t, app.URL+"/d/"+short+"/upload", nil, map[string][]byte{"a.jpg": jpegBytes}); code != 200 {
		t.Errorf("upload by the short link: %d", code)
	}
	// none of it exists once the box is closed
	postForm(t, app.URL+"/dropbox/pause", "sess=family", "service="+firstBox(t, st)+"&paused=1")
	for _, path := range []string{"/drop/" + token + "/theme.css", "/drop/" + token + "/cover", "/drop/" + token + "/manifest.webmanifest", "/d/" + short} {
		if code, _ := do(t, "GET", app.URL+path, "", nil); code != 404 {
			t.Errorf("%s while paused: %d", path, code)
		}
	}
}

func TestAGuestSeesAndTakesBackOnlyTheirOwnUploads(t *testing.T) {
	var st *store.Store
	im := &storing{}
	token := strings.Repeat("9a", 16)
	app, _ := setup(t, withDrop(t, &st, im, store.DropBox{Token: token, Name: "Undo", InboxTag: "inbox/Undo", GuestDelete: true}))
	asJSON := map[string]string{"Accept": "application/json"}

	code, body, cookies := sendFilesAs(t, app.URL+"/drop/"+token+"/upload", asJSON, map[string][]byte{"mine.jpg": jpegBytes})
	var answer struct {
		Files []struct {
			Name string
			OK   bool
		}
	}
	if err := json.Unmarshal([]byte(body), &answer); err != nil || code != 200 || len(answer.Files) != 1 || !answer.Files[0].OK {
		t.Fatalf("a script upload is answered in JSON: %d %s", code, body)
	}
	var secret *http.Cookie
	for _, c := range cookies {
		if strings.HasPrefix(c.Name, "handoff-drop-") {
			secret = c
		}
	}
	if secret == nil || !secret.HttpOnly || secret.SameSite != http.SameSiteLaxMode {
		t.Fatalf("the guest must be given a secret cookie that scripts cannot read: %+v", cookies)
	}
	// someone else uploads too, with a secret of their own
	sendFilesAs(t, app.URL+"/drop/"+token+"/upload", asJSON, map[string][]byte{"theirs.jpg": jpegBytes})

	mine := func(cookie string) string {
		_, p := do(t, "GET", app.URL+"/drop/"+token+"/mine", "", map[string]string{"Cookie": cookie})
		return p
	}
	c := secret.Name + "=" + secret.Value
	p := mine(c)
	if !strings.Contains(p, "mine.jpg") || strings.Contains(p, "theirs.jpg") || !strings.Contains(p, "Take back") {
		t.Fatalf("a guest sees only their own: %.500s", p)
	}
	if strings.Contains(mine(""), "mine.jpg") {
		t.Error("a browser with no secret sees nothing")
	}
	id := regexp.MustCompile(`name="id" value="([0-9a-f-]{36})"`).FindStringSubmatch(p)[1]

	// deleting: another browser cannot; the owner of the secret can, and is sent back to the list
	post := func(cookie string) (int, string) {
		req, _ := http.NewRequest("POST", app.URL+"/drop/"+token+"/mine/delete", strings.NewReader("id="+id))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode, res.Header.Get("Location")
	}
	if code, loc := post("handoff-drop-" + token[:8] + "=" + strings.Repeat("0", 32)); code != 303 || !strings.Contains(loc, "failed") {
		t.Errorf("another browser deleting: %d %s", code, loc)
	}
	if len(im.trashed) != 0 {
		t.Fatal("nothing may be deleted by someone else")
	}
	if code, loc := post(c); code != 303 || loc != "../mine?msg=deleted" {
		t.Errorf("deleting: %d %s", code, loc)
	}
	trashed := 0
	for _, v := range im.trashed {
		if v {
			trashed++
		}
	}
	if trashed != 1 {
		t.Errorf("the photo must be in the owner's trash: %v", im.trashed)
	}
	if after := mine(c); strings.Contains(after, "mine.jpg") {
		t.Error("a deleted file leaves the list")
	}
	if _, p := do(t, "GET", app.URL+"/drop/"+token+"/mine?msg=kept", "", map[string]string{"Cookie": c}); !strings.Contains(p, "already filed") {
		t.Error("the page must say when the owner has filed a photo")
	}
}

func TestOwnersPrintCardsAndOnlyOwnersSeeThem(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("bc", 16)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Short: "mnpqrstuvw", Name: "Reunion", Message: "See you", InboxTag: "inbox/Reunion"}))
	id := firstBox(t, st)
	for who, want := range map[string]int{"sess=family": 200, "sess=other": 404, "": 401} {
		if code, _ := do(t, "GET", app.URL+"/dropbox/card/"+id, who, nil); code != want {
			t.Errorf("card as %q: %d, want %d", who, code, want)
		}
	}
	_, card := do(t, "GET", app.URL+"/dropbox/card/"+id, "sess=family", nil)
	if !strings.Contains(card, `data-card-link="drop/`+token+`"`) || !strings.Contains(card, "Reunion") || !strings.Contains(card, `<base href="../../"`) {
		t.Errorf("long card: %.400s", card)
	}
	if _, short := do(t, "GET", app.URL+"/dropbox/card/"+id+"?way=short", "sess=family", nil); !strings.Contains(short, `data-card-link="d/mnpqrstuvw"`) {
		t.Error("the short card must carry the short link")
	}
	// the owner's page shows both ways in
	if owner := get(t, app.URL, "sess=family"); !strings.Contains(owner, `data-link="d/mnpqrstuvw"`) || !strings.Contains(owner, "Printable card") {
		t.Error("the owner must see the short link and the card")
	}
	// the owner's look form takes a cover; a bad one is refused
	form := func(fields map[string]string, cover []byte) int {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("service", id)
		for k, v := range fields {
			mw.WriteField(k, v)
		}
		if cover != nil {
			fw, _ := mw.CreateFormFile("cover", "c.png")
			fw.Write(cover)
		}
		mw.Close()
		req, _ := http.NewRequest("POST", app.URL+"/dropbox/look", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Cookie", "sess=family")
		req.Header.Set("X-Requested-With", "immich-handoff")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := form(map[string]string{"accent": "#112233", "lang": "fr", "maxdim": "2400"}, pngBytes); code != 204 {
		t.Errorf("look: %d", code)
	}
	if code := form(map[string]string{"accent": "javascript:alert(1)"}, nil); code != 409 {
		t.Errorf("a bad colour: %d, want 409", code)
	}
	if code := form(nil, []byte("<html>not an image</html>")); code != 409 {
		t.Errorf("a bad cover: %d, want 409", code)
	}
	if got, _ := st.DropBox(context.Background(), id); got.Accent != "#112233" || got.Lang != "fr" || got.MaxDim != 2400 || !st.HasDropCover(context.Background(), id) {
		t.Errorf("saved: %+v", got)
	}
	for _, path := range []string{"/dropbox/short", "/dropbox/renew", "/dropbox/template", "/dropbox/duplicate", "/dropbox/fromtemplate"} {
		if code, _ := postForm(t, app.URL+path, "", "service=x"); code != 401 {
			t.Errorf("%s without signing in: %d, want 401", path, code)
		}
	}
}
