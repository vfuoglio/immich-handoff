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
	mu    sync.Mutex
	names []string
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
				s.mu.Lock()
				s.names = append(s.names, p.FileName())
				s.mu.Unlock()
				j(w, 201, map[string]any{"id": uuid.NewString(), "status": "created"})
				return
			}
		}
		http.Error(w, "no", 400)
	})
	mux.HandleFunc("PUT /api/tags", func(w http.ResponseWriter, r *http.Request) {
		j(w, 200, []any{map[string]any{"id": uuid.NewString(), "name": "inbox/Wedding", "value": "inbox/Wedding", "createdAt": "2025-01-02T03:04:05.000Z", "updatedAt": "2025-01-02T03:04:05.000Z"}})
	})
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) { j(w, 200, []any{}) })
	mux.HandleFunc("PUT /api/tags/{id}/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Ids []string }
		json.NewDecoder(r.Body).Decode(&body)
		out := []any{}
		for _, id := range body.Ids {
			out = append(out, map[string]any{"id": id, "success": true})
		}
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
