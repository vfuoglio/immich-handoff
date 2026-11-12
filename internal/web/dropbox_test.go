package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
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

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/mailin"
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
	mux.HandleFunc("GET /api/assets/{id}/thumbnail", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		_, ok := s.assetTags[r.PathValue("id")]
		s.mu.Unlock()
		if !ok {
			http.Error(w, "no", 404)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(jpegBytes)
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

func TestTheShotListIsOnTheGuestPageAndTagsWhatArrives(t *testing.T) {
	var st *store.Store
	im := &storing{}
	token := strings.Repeat("de", 16)
	app, _ := setup(t, withDrop(t, &st, im, store.DropBox{Token: token, Name: "Hunt", InboxTag: "inbox/Hunt", PromptRequired: true,
		Prompts: []store.Prompt{{ID: "p1", Text: "The cake"}, {ID: "p2", Text: "The oldest guest"}}}))

	_, p := do(t, "GET", app.URL+"/drop/"+token, "", nil)
	for _, want := range []string{`name="prompt"`, `required`, "The cake", "The oldest guest", "no photo yet", "Choose one"} {
		if !strings.Contains(p, want) {
			t.Errorf("the guest page lacks %q", want)
		}
	}
	upload := func(fields map[string]string) string {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, v := range fields {
			mw.WriteField(k, v)
		}
		fw, _ := mw.CreateFormFile("files", "a.jpg")
		fw.Write(jpegBytes)
		mw.Close()
		req, _ := http.NewRequest("POST", app.URL+"/drop/"+token+"/upload", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Accept", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	if r := upload(nil); !strings.Contains(r, `"ok":false`) || !strings.Contains(r, "Choose what the photo is for") {
		t.Errorf("no prompt: %s", r)
	}
	if len(im.names) != 0 {
		t.Fatal("a photo that does not say what it is for must not be stored")
	}
	if r := upload(map[string]string{"prompt": "p1"}); !strings.Contains(r, `"ok":true`) {
		t.Errorf("with a prompt: %s", r)
	}
	tagged := false
	for _, n := range im.tags {
		tagged = tagged || n == "inbox/Hunt/The cake"
	}
	if !tagged {
		t.Errorf("tags made: %v", im.tags)
	}
	// the list now shows the cake as covered, to everybody
	if _, p := do(t, "GET", app.URL+"/drop/"+token, "", nil); !strings.Contains(p, "1 photo") {
		t.Error("the checklist must show what has a photo")
	}
	// the owner edits the list; a viewer cannot
	id := firstBox(t, st)
	if code, b := postForm(t, app.URL+"/dropbox/extras", "sess=family", "service="+id+"&prompts=The+cake%0D%0AThe+band&guestbook=1"); code != 204 {
		t.Fatalf("extras: %d %s", code, b)
	}
	if got, _ := st.DropBox(context.Background(), id); len(got.Prompts) != 2 || got.Prompts[0].ID != "p1" || got.Prompts[1].Text != "The band" || got.PromptRequired || !got.GuestBook {
		t.Errorf("saved: %+v", got)
	}
	if code, _ := postForm(t, app.URL+"/dropbox/extras", "sess=family", "service="+id+"&prompts=a%2Fb"); code != 409 {
		t.Errorf("a slash in a prompt: %d, want 409", code)
	}
	st.SetDropHost(context.Background(), id, store.DropHost{UserID: uuid.MustParse(otherID), Name: "Other", Email: "other@x.test", Role: store.HostView})
	if code, _ := postForm(t, app.URL+"/dropbox/extras", "sess=other", "service="+id+"&prompts=x"); code != 409 {
		t.Errorf("a viewer editing the list: %d, want 409", code)
	}
	if owner := get(t, app.URL, "sess=family"); !strings.Contains(owner, "The band") || !strings.Contains(owner, "Shot list") {
		t.Error("the owner sees the list with its counts")
	}
}

func TestMessagesToTheHostAreTakenAndPlayedToTheHostsOnly(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("ef", 16)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Book", InboxTag: "inbox/Book", GuestBook: true}))
	id := firstBox(t, st)
	webm := append([]byte{0x1A, 0x45, 0xDF, 0xA3}, make([]byte, 300)...)

	if _, p := do(t, "GET", app.URL+"/drop/"+token, "", nil); !strings.Contains(p, "Message to the host") || !strings.Contains(p, "voice-record") || !strings.Contains(p, `action="drop/`+token+`/message"`) {
		t.Fatal("the guest page must offer a message to the host")
	}
	send := func(fields map[string]string, voice []byte, json bool) (int, string) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, v := range fields {
			mw.WriteField(k, v)
		}
		if voice != nil {
			fw, _ := mw.CreateFormFile("voice", "voice")
			fw.Write(voice)
		}
		mw.Close()
		req, _ := http.NewRequest("POST", app.URL+"/drop/"+token+"/message", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		if json {
			req.Header.Set("Accept", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, b := send(map[string]string{"name": "Eve", "text": "Congratulations!"}, webm, true); code != 200 || !strings.Contains(b, `"ok":true`) {
		t.Fatalf("a message with a voice note: %d %s", code, b)
	}
	if code, p := send(map[string]string{"name": "Ann", "text": "No script here"}, nil, false); code != 200 || !strings.Contains(p, "Thank you for your message") {
		t.Errorf("a plain form: %d %.200s", code, p)
	}
	for name, c := range map[string]struct {
		fields map[string]string
		voice  []byte
	}{"empty": {nil, nil}, "no name": {map[string]string{"text": "Hello"}, nil}, "a page as a voice note": {map[string]string{"name": "Eve", "text": "x"}, []byte("<!DOCTYPE html><html><script>alert(1)</script></html>")}, "too long": {map[string]string{"name": "Eve", "text": strings.Repeat("x", 1001)}, nil}} {
		if code, b := send(c.fields, c.voice, true); code != 422 || !strings.Contains(b, `"ok":false`) {
			t.Errorf("%s: %d %s", name, code, b)
		}
	}
	msgs, _ := st.DropMessages(context.Background(), id, 10)
	if len(msgs) != 2 {
		t.Fatalf("kept: %+v", msgs)
	}
	var voiced store.DropMessage
	for _, m := range msgs {
		if m.HasAudio {
			voiced = m
		}
	}

	// the owner reads and plays; another person cannot; a viewer sees nothing of it
	owner := get(t, app.URL, "sess=family")
	if !strings.Contains(owner, "Congratulations!") || !strings.Contains(owner, `src="dropbox/voice/`+voiced.ID+`"`) || !strings.Contains(owner, "Download the messages") {
		t.Errorf("the owner's messages: %.300s", owner)
	}
	for who, want := range map[string]int{"sess=family": 200, "sess=other": 404, "": 401} {
		if code, _ := do(t, "GET", app.URL+"/dropbox/voice/"+voiced.ID, who, nil); code != want {
			t.Errorf("voice note as %q: %d, want %d", who, code, want)
		}
	}
	res, _ := http.DefaultClient.Do(func() *http.Request {
		r, _ := http.NewRequest("GET", app.URL+"/dropbox/guestbook/"+id, nil)
		r.Header.Set("Cookie", "sess=family")
		return r
	}())
	zipBytes, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/zip" || !bytes.HasPrefix(zipBytes, []byte("PK")) {
		t.Errorf("export: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if code, _ := do(t, "GET", app.URL+"/dropbox/guestbook/"+id, "sess=other", nil); code != 404 {
		t.Errorf("export by a stranger: %d", code)
	}
	st.SetDropHost(context.Background(), id, store.DropHost{UserID: uuid.MustParse(otherID), Name: "Other", Email: "other@x.test", Role: store.HostView})
	if view := get(t, app.URL, "sess=other"); strings.Contains(view, "Congratulations!") || strings.Contains(view, "dropbox/voice/") {
		t.Error("a viewer must not see the messages")
	}
	if code, _ := postForm(t, app.URL+"/dropbox/message/delete", "sess=other", "service="+voiced.ID); code != 409 {
		t.Errorf("a viewer removing: %d, want 409", code)
	}
	if code, _ := postForm(t, app.URL+"/dropbox/message/delete", "sess=family", "service="+voiced.ID); code != 204 {
		t.Errorf("the owner removing: %d", code)
	}
	if left, _ := st.DropMessages(context.Background(), id, 10); len(left) != 1 {
		t.Errorf("after removing: %d", len(left))
	}
	// a box that takes no messages takes none
	st.SetDropGuestBook(context.Background(), id, false)
	if code, _ := send(map[string]string{"text": "hi"}, nil, true); code != 422 {
		t.Errorf("a box that takes no messages: %d", code)
	}
}

func TestAClosedBoxThatSharedItsPhotosSaysSoAndNoOtherDoes(t *testing.T) {
	var st *store.Store
	token, plain := strings.Repeat("f0", 16), strings.Repeat("f1", 16)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Shared", InboxTag: "inbox/Shared"}), func(s *web.Server) { s.PublicImmichURL = "https://photos.example.org/" })
	// a second box that shared nothing
	owner := uuid.MustParse(familyID)
	st.AddDropBox(context.Background(), store.DropBox{Token: plain, Name: "Quiet", OwnerID: owner, Key: "k", InboxTag: "inbox/Quiet"})
	boxes, _ := st.DropBoxes(context.Background())
	for _, b := range boxes {
		st.SetDropBoxState(context.Background(), b.ID, store.DropPaused, "")
	}
	st.SetDropShare(context.Background(), store.DropShare{BoxID: boxes[0].ID, LinkID: uuid.NewString(), Key: "abc123key", Message: "Thank you all!", ExpiresAt: time.Now().Add(48 * time.Hour)})

	code, p := do(t, "GET", app.URL+"/drop/"+token, "", nil)
	if code != 200 || !strings.Contains(p, "Thank you all!") || !strings.Contains(p, `href="https://photos.example.org/share/abc123key"`) || !strings.Contains(p, "See the photos") {
		t.Fatalf("shared: %d %.400s", code, p)
	}
	if strings.Contains(p, `type="file"`) {
		t.Error("a closed box takes no photos, whatever it shared")
	}
	if code, _ := do(t, "GET", app.URL+"/drop/"+plain, "", nil); code != 404 {
		t.Errorf("a closed box that shared nothing: %d", code)
	}
	// an expired share says nothing
	st.SetDropShare(context.Background(), store.DropShare{BoxID: boxes[0].ID, LinkID: uuid.NewString(), Key: "abc123key", ExpiresAt: time.Now().Add(-time.Hour)})
	if code, _ := do(t, "GET", app.URL+"/drop/"+token, "", nil); code != 404 {
		t.Errorf("an expired share: %d", code)
	}
}

func TestTheWallIsAScreenWithNoSignInThatSeesOnlyItsBoxesPhotos(t *testing.T) {
	var st *store.Store
	wall := strings.Repeat("ab", 32)
	token := strings.Repeat("c1", 16)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Big screen", InboxTag: "inbox/Big", Wall: wall, GuestBook: true,
		Prompts: []store.Prompt{{ID: "p1", Text: "The cake"}}}))
	id := firstBox(t, st)
	for _, name := range []string{"a.jpg", "b.jpg"} {
		if code, _, _ := sendFilesAs(t, app.URL+"/drop/"+token+"/upload", map[string]string{"Accept": "application/json"}, map[string][]byte{name: jpegBytes}); code != 200 {
			t.Fatalf("upload: %d", code)
		}
	}
	// the page is open to anyone with the link and carries what the script needs, and no account's chrome
	code, p := do(t, "GET", app.URL+"/wall/"+wall, "", nil)
	for _, want := range []string{`id="wall"`, `data-feed="wall/` + wall + `/feed"`, `data-photo="wall/` + wall + `/photo/"`, `data-mode="single"`, `<base href="../"`, "Waiting for the first photo", "Collage"} {
		if !strings.Contains(p, want) {
			t.Errorf("the wall lacks %s", want)
		}
	}
	if code != 200 || strings.Contains(p, "Photos you tag in Immich") || strings.Contains(p, token) {
		t.Errorf("the wall page: %d, and it must not carry the box's own link", code)
	}
	if _, p := do(t, "GET", app.URL+"/wall/"+wall+"?mode=grid", "", nil); !strings.Contains(p, `data-mode="grid"`) {
		t.Error("the layout can be chosen in the address")
	}
	// the box's own link is not a wall, and a wall link is not a box
	for _, path := range []string{"/wall/" + token, "/wall/" + strings.Repeat("0", 64), "/drop/" + wall} {
		if code, _ := do(t, "GET", app.URL+path, "", nil); code != 404 {
			t.Errorf("%s: %d, want 404", path, code)
		}
	}
	// what is new, as JSON
	var feed struct {
		Items []struct {
			ID      int64
			Caption string
		}
		Messages []struct{ Name, Text string }
	}
	get := func(path string) int {
		res, err := http.Get(app.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		feed.Items, feed.Messages = nil, nil
		json.NewDecoder(res.Body).Decode(&feed)
		return res.StatusCode
	}
	if c := get("/wall/" + wall + "/feed?after=0"); c != 200 || len(feed.Items) != 2 {
		t.Fatalf("feed: %d %+v", c, feed)
	}
	first, second := feed.Items[0].ID, feed.Items[1].ID
	if get("/wall/" + wall + "/feed?after=" + strconv.FormatInt(first, 10)); len(feed.Items) != 1 || feed.Items[0].ID != second {
		t.Errorf("what is newer: %+v", feed.Items)
	}
	// a picture, by its place; another number gives nothing; nothing can be asked for by an id in Immich
	res, _ := http.Get(app.URL + "/wall/" + wall + "/photo/" + strconv.FormatInt(first, 10))
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/jpeg" || !strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("a picture: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	for _, id := range []string{"999999", "abc", "-1"} {
		if r, _ := http.Get(app.URL + "/wall/" + wall + "/photo/" + id); r.StatusCode != 404 {
			t.Errorf("photo %s: %d", id, r.StatusCode)
		}
	}
	// a message shows only once chosen
	ctx := context.Background()
	st.AddDropMessage(ctx, store.DropMessage{BoxID: id, Name: "Eve", Text: "Cheers!"}, nil)
	get("/wall/" + wall + "/feed?after=0")
	if len(feed.Messages) != 0 {
		t.Error("a message is not on the wall until chosen")
	}
	msgs, _ := st.DropMessages(ctx, id, 5)
	if code, b := postForm(t, app.URL+"/dropbox/message/wall", "sess=family", "service="+msgs[0].ID+"&on=1"); code != 204 {
		t.Fatalf("choosing a message: %d %s", code, b)
	}
	get("/wall/" + wall + "/feed?after=0")
	if len(feed.Messages) != 1 || feed.Messages[0].Text != "Cheers!" || feed.Messages[0].Name != "Eve" {
		t.Errorf("a chosen message: %+v", feed.Messages)
	}
	// a paused box still has its wall; a revoked one has none
	postForm(t, app.URL+"/dropbox/pause", "sess=family", "service="+id+"&paused=1")
	if c := get("/wall/" + wall + "/feed?after=0"); c != 200 {
		t.Errorf("a paused box's wall: %d", c)
	}
	postForm(t, app.URL+"/dropbox/revoke", "sess=family", "service="+id)
	if c := get("/wall/" + wall + "/feed?after=0"); c != 404 {
		t.Errorf("a revoked box's wall: %d", c)
	}
}

func TestTheOwnerSeesTheWallTheBestOfAndWhereTheSetWentAndOthersCannotChangeIt(t *testing.T) {
	var st *store.Store
	wall, token := strings.Repeat("de", 32), strings.Repeat("c2", 16)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Gala", InboxTag: "inbox/Gala", Wall: wall}))
	id := firstBox(t, st)
	ctx := context.Background()
	// the owner sends to a service, so it can be chosen
	svc := "family"
	st.AddLink(ctx, store.Link{Service: svc, Role: store.RoleSender, ImmichUserID: uuid.MustParse(familyID), Name: "Family", Email: "family@x.test", KeyID: uuid.New(), Key: "k-fam", Mode: "copy"})
	started := time.Now().Add(-time.Minute)
	st.SetDropHighlight(ctx, store.DropHighlight{BoxID: id, Strictness: "normal", Status: store.HighlightDone, Total: 5, Done: 5, Kept: 3, Album: "Gala best of", Started: started, Finished: time.Now()})
	st.ReplaceDropVerdicts(ctx, id, []store.Verdict{{AssetID: "a1", Verdict: "kept", Name: "one.jpg"}, {AssetID: "a2", Verdict: "dark", Detail: "brightness 9 of 255", Name: "night.jpg"}, {AssetID: "a3", Verdict: "duplicate", Detail: "same moment as one.jpg", Name: "again.jpg"}})
	st.SetDropOnward(ctx, id, svc, false)
	st.AddDropOnward(ctx, id, store.DropOnward{Service: svc, Scope: "all", Count: 7, By: "Family"})

	owner := get(t, app.URL, "sess=family")
	for _, want := range []string{`data-link="wall/` + wall + `"`, "Open the wall", "Stop the wall", "New wall link", "3 of 5 photos kept in the album Gala best of", "Why 2 photos were left out",
		"night.jpg", "too dark", "the same moment as a better photo", `option value="family" selected`, "you keep your originals", "Hand everything on now", "Hand the best-of on now", "7 photos (everything) to family"} {
		if !strings.Contains(owner, want) {
			t.Errorf("the owner's page lacks %q", want)
		}
	}
	// another person sees nothing of it, and a manager sees the wall link but cannot stop it or hand anything on
	if other := get(t, app.URL, "sess=other"); strings.Contains(other, wall) || strings.Contains(other, "Gala best of") {
		t.Error("a stranger must see nothing")
	}
	st.SetDropHost(ctx, id, store.DropHost{UserID: uuid.MustParse(otherID), Name: "Other", Email: "other@x.test", Role: store.HostManage})
	mgr := get(t, app.URL, "sess=other")
	if !strings.Contains(mgr, `data-link="wall/`+wall+`"`) || strings.Contains(mgr, "Stop the wall") || strings.Contains(mgr, "Hand everything on now") || strings.Contains(mgr, "Make a best-of") {
		t.Error("a manager sees the wall's link and none of the owner's controls")
	}
	for _, c := range []struct{ path, body string }{
		{"/dropbox/wall", "service=" + id}, {"/dropbox/wall/stop", "service=" + id}, {"/dropbox/highlights", "service=" + id + "&strictness=normal"},
		{"/dropbox/onward", "service=" + id + "&target=family"}, {"/dropbox/handon", "service=" + id + "&scope=all"},
	} {
		if code, _ := postForm(t, app.URL+c.path, "sess=other", c.body); code != 409 {
			t.Errorf("%s by a manager: %d, want 409", c.path, code)
		}
		if code, _ := postForm(t, app.URL+c.path, "", c.body); code != 401 {
			t.Errorf("%s with no sign-in: %d, want 401", c.path, code)
		}
	}
	// the owner chooses where to hand on, and only a service they send to
	if code, _ := postForm(t, app.URL+"/dropbox/onward", "sess=family", "service="+id+"&target=nowhere"); code != 409 {
		t.Errorf("a service the owner does not send to: %d, want 409", code)
	}
	if code, b := postForm(t, app.URL+"/dropbox/onward", "sess=family", "service="+id+"&target=family&auto=1"); code != 204 {
		t.Fatalf("choosing: %d %s", code, b)
	}
	if got, _ := st.DropBox(ctx, id); got.OnwardService != "family" || !got.OnwardAuto {
		t.Errorf("saved: %+v", got)
	}
	if code, _ := postForm(t, app.URL+"/dropbox/handon", "sess=family", "service="+id+"&scope=weird"); code != 409 {
		t.Errorf("an unknown scope: %d, want 409", code)
	}
}

func TestAnEmailAddressForABoxIsOfferedOnlyWhereAMailboxIsSetUpAndShownOnItsCard(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("ee", 16)
	var theHub *hub.Hub
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Gala", InboxTag: "inbox/Gala"}), func(s *web.Server) { theHub = s.Hub })
	id := firstBox(t, st)
	owner := "sess=family"

	if _, p := do(t, "GET", app.URL+"/", owner, nil); strings.Contains(p, "Photos by email") || strings.Contains(p, "email address") {
		t.Error("the box offers an address without a mailbox set up")
	}
	theHub.MailIn = &hub.MailIn{Hub: theHub, Settings: mailin.Settings{Address: "drop@example.org"}}
	_, p := do(t, "GET", app.URL+"/", owner, nil)
	if !strings.Contains(p, "Photos by email") || !strings.Contains(p, "Give this box an email address") {
		t.Fatal("with a mailbox the box should offer an address")
	}
	if code, _ := postForm(t, app.URL+"/dropbox/mail", "", "service="+id+"&on=1"); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, _ := do(t, "POST", app.URL+"/dropbox/mail", owner, nil); code != 400 {
		t.Errorf("without the CSRF header: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/dropbox/mail", "sess=alice", "service="+id+"&on=1"); code != 409 {
		t.Errorf("someone with no part in the box: %d", code)
	}
	if got, _ := st.DropBox(context.Background(), id); got.MailCode != "" {
		t.Fatal("a refused request gave the box an address")
	}
	if code, msg := postForm(t, app.URL+"/dropbox/mail", owner, "service="+id+"&on=1"); code != 204 {
		t.Fatalf("giving it an address: %d %q", code, msg)
	}
	got, _ := st.DropBox(context.Background(), id)
	want := "drop+" + got.MailCode + "@example.org"
	_, p = do(t, "GET", app.URL+"/", owner, nil)
	for _, w := range []string{`value="` + want + `"`, "Remove email address", "nobody gets a reply"} {
		if !strings.Contains(p, w) {
			t.Errorf("the box's row lacks %q", w)
		}
	}
	if _, card := do(t, "GET", app.URL+"/dropbox/card/"+id, owner, nil); !strings.Contains(card, "Or send photos by email to "+want) {
		t.Errorf("the printable card should carry the address: %.300s", card)
	}
	if code, _ := postForm(t, app.URL+"/dropbox/mail", owner, "service="+id+"&on=0"); code != 204 {
		t.Fatalf("removing it: %d", code)
	}
	if got, _ := st.DropBox(context.Background(), id); got.MailCode != "" {
		t.Error("the address is still there")
	}
}

// A password on the whole page: a guest who does not have it sees that a password is needed and nothing of the box (not
// its name, its message, its cover or its colour), can fetch none of its parts and can send nothing. The right password
// is remembered in the guest's browser.
func TestAPasswordOnTheWholePageShowsNothingUntilItIsGiven(t *testing.T) {
	var st *store.Store
	im := &storing{}
	token := strings.Repeat("56", 16)
	pw, _ := hubPassword()
	app, _ := setup(t, withDrop(t, &st, im, store.DropBox{Token: token, Name: "Secret wedding", Message: "Hush hush", InboxTag: "inbox/Secret",
		Password: pw, PasswordScope: store.DropScopePage, GuestDelete: true, GuestBook: true, Accent: "#c2185b"}))

	code, body := do(t, "GET", app.URL+"/drop/"+token, "", nil)
	if code != 401 || !strings.Contains(body, "This page needs a password") || !strings.Contains(body, `action="drop/`+token+`/unlock"`) {
		t.Fatalf("the locked page: %d %.200s", code, body)
	}
	for _, secret := range []string{"Secret wedding", "Hush hush", "drop-files", token + "/theme.css", token + "/manifest.webmanifest", token + "/cover"} {
		if strings.Contains(body, secret) {
			t.Errorf("the locked page shows %q", secret)
		}
	}
	for _, part := range []string{"/cover", "/theme.css", "/manifest.webmanifest", "/mine"} {
		if code, _ := do(t, "GET", app.URL+"/drop/"+token+part, "", nil); code != 404 {
			t.Errorf("%s while locked: %d, want 404", part, code)
		}
	}
	if code, _ := sendFiles(t, app.URL+"/drop/"+token+"/upload", nil, map[string][]byte{"a.jpg": jpegBytes}); code != 403 || len(im.names) != 0 {
		t.Errorf("sending while locked: %d %v", code, im.names)
	}
	if code, _ := sendFiles(t, app.URL+"/drop/"+token+"/message", map[string]string{"name": "Eve", "text": "hi"}, nil); code != 403 {
		t.Errorf("a message while locked: %d, want 403", code)
	}
	if code, _ := do(t, "GET", app.URL+"/drop/"+token, "handoff-unlock-"+st0(t, st)+"=made-up", nil); code != 401 {
		t.Errorf("a made-up cookie: %d, want 401", code)
	}

	// the password is checked; a wrong one gives no cookie
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	unlock := func(password string) *http.Response {
		res, err := noFollow.PostForm(app.URL+"/drop/"+token+"/unlock", url.Values{"password": {password}})
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	if res := unlock("wrong"); res.StatusCode != 401 || len(res.Cookies()) != 0 {
		t.Errorf("a wrong password: %d, %d cookies", res.StatusCode, len(res.Cookies()))
	}
	res := unlock("swordfish")
	if res.StatusCode != 303 || res.Header.Get("Location") != "../"+token || len(res.Cookies()) != 1 || !res.Cookies()[0].HttpOnly {
		t.Fatalf("the right password: %d %q %v", res.StatusCode, res.Header.Get("Location"), res.Cookies())
	}
	cookie := res.Cookies()[0].Name + "=" + res.Cookies()[0].Value

	// with the cookie the page is the box's own, with no password field, and it takes photos and messages
	code, body = do(t, "GET", app.URL+"/drop/"+token, cookie, nil)
	if code != 200 || !strings.Contains(body, "Secret wedding") || !strings.Contains(body, "drop-files") || strings.Contains(body, `type="password"`) {
		t.Errorf("the unlocked page: %d %.200s", code, body)
	}
	if code, _ := do(t, "GET", app.URL+"/drop/"+token+"/mine", cookie, nil); code != 200 {
		t.Errorf("a guest's uploads once unlocked: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/drop/"+token+"/cover", cookie, nil); code == 401 || code == 403 {
		t.Errorf("the cover once unlocked: %d", code)
	}
	if code, _, _ := sendFilesAs(t, app.URL+"/drop/"+token+"/upload", map[string]string{"Cookie": cookie}, map[string][]byte{"a.jpg": jpegBytes}); code != 200 || len(im.names) != 1 {
		t.Errorf("sending once unlocked: %d %v", code, im.names)
	}
}

// What a password guards is chosen when the box is made; a box with a password and no choice guards the upload.
func TestAPasswordGuardsTheUploadByDefaultAndThePageOnlyWhenAsked(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("78", 16)
	pw, _ := hubPassword()
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Plain lock", InboxTag: "inbox/Plain", Password: pw}))
	b := all(t, st)[0]
	if b.PasswordScope != store.DropScopeUpload {
		t.Errorf("the default scope: %q", b.PasswordScope)
	}
	if code, body := do(t, "GET", app.URL+"/drop/"+token, "", nil); code != 200 || !strings.Contains(body, "Plain lock") || !strings.Contains(body, `type="password"`) {
		t.Errorf("a box that guards the upload still shows its page: %d", code)
	}
	if code, _ := do(t, "POST", app.URL+"/drop/"+token+"/unlock", "", nil); code != 404 {
		t.Errorf("unlocking a box that does not guard its page: %d, want 404", code)
	}
}

func all(t *testing.T, st *store.Store) []store.DropBox {
	t.Helper()
	boxes, err := st.DropBoxes(context.Background())
	if err != nil || len(boxes) == 0 {
		t.Fatal("no drop box", err)
	}
	return boxes
}

// st0 is the first eight characters of the box's id, which name its cookies.
func st0(t *testing.T, st *store.Store) string { return all(t, st)[0].ID[:8] }

// The owner chooses what a password guards from one menu, and the box says which it is; the message form on the guest
// page asks for a name.
func TestThePasswordMenuAndTheMessageFormSayWhatTheyDo(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("9a", 16)
	pw, _ := hubPassword()
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Hidden", InboxTag: "inbox/Hidden", Password: pw, PasswordScope: store.DropScopePage, GuestBook: true}))
	owner := get(t, app.URL, "sess=family")
	for _, want := range []string{`id="drop-secure"`, "Secure with a password...", `value="upload"`, `value="page"`, "The whole page", "data-secure-password", "Password on the page"} {
		if !strings.Contains(owner, want) {
			t.Errorf("the owner's page lacks %q", want)
		}
	}
}

func TestTheMessageFormAsksForAName(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("bc", 16)
	app, _ := setup(t, withDrop(t, &st, &storing{}, store.DropBox{Token: token, Name: "Book", InboxTag: "inbox/Book", GuestBook: true}))
	_, p := do(t, "GET", app.URL+"/drop/"+token, "", nil)
	if !strings.Contains(p, "Message to the host") || !regexp.MustCompile(`<input id="gb-name" name="name" required`).MatchString(p) || strings.Contains(p, "Your name (optional)") {
		t.Error("the message form must ask for a name")
	}
}
