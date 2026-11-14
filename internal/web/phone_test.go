package web_test

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

type phoneImmich struct {
	mu      sync.Mutex
	uploads []string // who uploaded what: "cookie:file"
	tags    map[string]string
	tagged  map[string][]string
}

func newPhone(t *testing.T) (app *httptest.Server, im *phoneImmich, h *hub.Hub) {
	t.Helper()
	im = &phoneImmich{tags: map[string]string{}, tagged: map[string][]string{}}
	base := fakeImmich()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/assets", func(w http.ResponseWriter, r *http.Request) {
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "no", 400)
			return
		}
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FormName() == "assetData" {
				n, _ := io.Copy(io.Discard, p)
				im.mu.Lock()
				im.uploads = append(im.uploads, strings.TrimSpace(r.Header.Get("Cookie"))+":"+p.FileName()+":"+itoa(n))
				im.mu.Unlock()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"id": "11111111-1111-1111-1111-111111111111", "status": "created"})
	})
	mux.HandleFunc("PUT /api/tags", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Tags []string }
		json.NewDecoder(r.Body).Decode(&body)
		out := []any{}
		im.mu.Lock()
		for _, n := range body.Tags {
			id := uuid.NewString()
			im.tags[id] = n
			out = append(out, map[string]any{"id": id, "name": n, "value": n, "createdAt": ts, "updatedAt": ts})
		}
		im.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("PUT /api/tags/{id}/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Ids []string }
		json.NewDecoder(r.Body).Decode(&body)
		im.mu.Lock()
		im.tagged[im.tags[r.PathValue("id")]] = append(im.tagged[im.tags[r.PathValue("id")]], body.Ids...)
		im.mu.Unlock()
		out := []any{}
		for _, id := range body.Ids {
			out = append(out, map[string]any{"id": id, "success": true})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})
	mux.Handle("/", base)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mk := func(name, key string) *syncer.Account {
		a, _ := immich.New(srv.URL, immich.Auth{APIKey: key}, nil)
		return &syncer.Account{Name: name, API: a}
	}
	svc := &syncer.Service{Name: "family", Tag: "share", InboxTag: "inbox", ExpiryDays: 30,
		Senders: []*syncer.Account{mk("alice", "k-alice")}, Receivers: []*syncer.Account{mk("family", "k-family")}}
	s := &syncer.Syncer{Services: []*syncer.Service{svc}, Cfg: syncer.Config{DryRun: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	syncer.ResolveUsers(context.Background(), s.Services)
	h = &hub.Hub{Static: s.Services, Log: s.Log}
	h.PhoneDir = t.TempDir()
	return startPhone(t, srv.URL, s, h), im, h
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

var phoneHdr = map[string]string{"X-Requested-With": "immich-handoff"}

func phoneCall(t *testing.T, method, url, cookie string, body []byte, hdr map[string]string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, url, bytes.NewReader(body))
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
	return res.StatusCode, b
}

func TestAPhoneSendsAFileInPiecesAndItLandsTaggedInTheSendersOwnImmich(t *testing.T) {
	app, im, _ := newPhone(t)
	data := append(append([]byte{}, jpegBytes...), bytes.Repeat([]byte("z"), 3000)...)
	code, b := phoneCall(t, "POST", app.URL+"/send/start", "sess=alice", []byte(`{"name":"IMG_1.jpg","size":`+itoa(int64(len(data)))+`,"service":"family","taken":1700000000000}`), phoneHdr)
	var st struct {
		ID    string
		Chunk int
	}
	json.Unmarshal(b, &st)
	if code != 201 || st.ID == "" || st.Chunk <= 0 {
		t.Fatalf("start: %d %s", code, b)
	}
	url := app.URL + "/send/" + st.ID
	if c, b := phoneCall(t, "PUT", url+"/chunk?offset=0", "sess=alice", data[:1200], phoneHdr); c != 200 || !strings.Contains(string(b), `"offset":1200`) {
		t.Fatalf("first piece: %d %s", c, b)
	}
	// a piece sent twice (the answer was lost) is refused with where to carry on
	if c, b := phoneCall(t, "PUT", url+"/chunk?offset=0", "sess=alice", data[:1200], phoneHdr); c != 409 || !strings.Contains(string(b), `"offset":1200`) {
		t.Fatalf("again: %d %s", c, b)
	}
	if c, b := phoneCall(t, "GET", url, "sess=alice", nil, phoneHdr); c != 200 || !strings.Contains(string(b), `"offset":1200`) {
		t.Fatalf("status: %d %s", c, b)
	}
	if c, b := phoneCall(t, "POST", url+"/done", "sess=alice", nil, phoneHdr); c != 409 || !strings.Contains(string(b), `"offset":1200`) {
		t.Fatalf("early: %d %s", c, b)
	}
	if c, b := phoneCall(t, "PUT", url+"/chunk?offset=1200", "sess=alice", data[1200:], phoneHdr); c != 200 || !strings.Contains(string(b), `"offset":`+itoa(int64(len(data)))) {
		t.Fatalf("rest: %d %s", c, b)
	}
	sum := sha1.Sum(data)
	if c, b := phoneCall(t, "POST", url+"/done", "sess=alice", []byte(`{"sum":"`+hex.EncodeToString(sum[:])+`"}`), phoneHdr); c != 200 || !strings.Contains(string(b), `"duplicate":false`) {
		t.Fatalf("done: %d %s", c, b)
	}
	im.mu.Lock()
	defer im.mu.Unlock()
	if len(im.uploads) != 1 || !strings.HasPrefix(im.uploads[0], "sess=alice:IMG_1.jpg:"+itoa(int64(len(data)))) {
		t.Errorf("it is stored with the sender's own session: %v", im.uploads)
	}
	if len(im.tagged["share"]) != 1 {
		t.Errorf("tagged for the service: %v", im.tagged)
	}
}

func TestThePhoneEndpointsAreForSignedInPeopleWithTheHeaderAndTheirOwnFilesOnly(t *testing.T) {
	app, _, _ := newPhone(t)
	start := []byte(`{"name":"a.jpg","size":100}`)
	if c, _ := phoneCall(t, "POST", app.URL+"/send/start", "sess=alice", start, nil); c != 400 {
		t.Errorf("without the header: %d", c)
	}
	if c, _ := phoneCall(t, "POST", app.URL+"/send/start", "", start, phoneHdr); c != 401 {
		t.Errorf("signed out: %d", c)
	}
	if c, _ := phoneCall(t, "POST", app.URL+"/send/start", "sess=bogus", start, phoneHdr); c != 401 {
		t.Errorf("unknown session: %d", c)
	}
	// anyone signed in may send to their own library
	c, b := phoneCall(t, "POST", app.URL+"/send/start", "sess=other", start, phoneHdr)
	if c != 201 {
		t.Fatalf("library only: %d %s", c, b)
	}
	var st struct{ ID string }
	json.Unmarshal(b, &st)
	if c, _ := phoneCall(t, "GET", app.URL+"/send/"+st.ID, "sess=alice", nil, phoneHdr); c != 404 {
		t.Errorf("another person's upload: %d", c)
	}
	if c, _ := phoneCall(t, "PUT", app.URL+"/send/"+st.ID+"/chunk?offset=0", "sess=alice", []byte("x"), phoneHdr); c != 404 {
		t.Errorf("another person's piece: %d", c)
	}
	// a service the person does not send to
	if c, b := phoneCall(t, "POST", app.URL+"/send/start", "sess=other", []byte(`{"name":"a.jpg","size":100,"service":"family"}`), phoneHdr); c != 422 || !strings.Contains(string(b), "do not send to that service") {
		t.Errorf("not a sender: %d %s", c, b)
	}
	if c, _ := phoneCall(t, "POST", app.URL+"/send/start", "sess=alice", []byte(`{"name":"a.jpg","size":100,"service":"nope"}`), phoneHdr); c != 422 {
		t.Errorf("no such service: %d", c)
	}
	if c, _ := phoneCall(t, "POST", app.URL+"/send/start", "sess=alice", []byte(`not json`), phoneHdr); c != 400 {
		t.Errorf("bad body: %d", c)
	}
	if c, _ := phoneCall(t, "PUT", app.URL+"/send/"+st.ID+"/chunk?offset=-3", "sess=other", nil, phoneHdr); c != 400 {
		t.Errorf("bad offset: %d", c)
	}
}

func TestWithoutADataFolderPhonesAreToldSo(t *testing.T) {
	app, _, h := newPhone(t)
	h.PhoneDir = ""
	c, b := phoneCall(t, "POST", app.URL+"/send/start", "sess=alice", []byte(`{"name":"a.jpg","size":100}`), phoneHdr)
	if c != 404 || !strings.Contains(string(b), "data folder") {
		t.Errorf("%d %s", c, b)
	}
}

func TestAFileThatIsNotAPhotoIsRefusedWithAReason(t *testing.T) {
	app, im, _ := newPhone(t)
	page := []byte("<html>hi</html>")
	_, b := phoneCall(t, "POST", app.URL+"/send/start", "sess=alice", []byte(`{"name":"a.jpg","size":`+itoa(int64(len(page)))+`}`), phoneHdr)
	var st struct{ ID string }
	json.Unmarshal(b, &st)
	phoneCall(t, "PUT", app.URL+"/send/"+st.ID+"/chunk?offset=0", "sess=alice", page, phoneHdr)
	if c, b := phoneCall(t, "POST", app.URL+"/send/"+st.ID+"/done", "sess=alice", nil, phoneHdr); c != 422 || !strings.Contains(string(b), "only photos and videos") {
		t.Errorf("%d %s", c, b)
	}
	if len(im.uploads) != 0 {
		t.Errorf("nothing was stored: %v", im.uploads)
	}
}

func startPhone(t *testing.T, immichURL string, s *syncer.Syncer, h *hub.Hub) *httptest.Server {
	t.Helper()
	srv := &web.Server{S: s, Hub: h, BaseURL: immichURL, WebhookHeader: "X-Webhook-Secret", WebhookSecret: "s3cret", Log: s.Log, BackgroundCtx: context.Background()}
	app := httptest.NewServer(srv.Handler())
	t.Cleanup(app.Close)
	return app
}

func TestTheSendPageListsWhereAPersonSendsAndAnyoneSignedInMayOpenIt(t *testing.T) {
	app, _, h := newPhone(t)
	if c, _ := phoneCall(t, "GET", app.URL+"/send", "", nil, nil); c != 401 {
		t.Errorf("signed out: %d", c)
	}
	_, b := phoneCall(t, "GET", app.URL+"/send", "sess=alice", nil, nil)
	page := string(b)
	for _, want := range []string{`id="send"`, `value="family"`, "My library only", `id="send-files"`, `accept="image/*,video/*`} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if !strings.Contains(page, `data-file-mb="2048"`) || !strings.Contains(page, `data-wait-mb="4096"`) {
		t.Errorf("the limits are on the page for the script")
	}
	// someone who sends to no service has only the library
	_, b = phoneCall(t, "GET", app.URL+"/send", "sess=other", nil, nil)
	if strings.Contains(string(b), `value="family"`) || !strings.Contains(string(b), "My library only") {
		t.Errorf("a person who sends to nothing sees only their library")
	}
	h.PhoneDir = ""
	_, b = phoneCall(t, "GET", app.URL+"/send", "sess=alice", nil, nil)
	if strings.Contains(string(b), `id="send-files"`) || !strings.Contains(string(b), "data folder") {
		t.Errorf("without the data folder the page says so and offers nothing")
	}
	if c, b := phoneCall(t, "GET", app.URL+"/", "sess=alice", nil, nil); c != 200 || !strings.Contains(string(b), `class="send-tab"`) || !strings.Contains(string(b), `href="send"`) {
		t.Errorf("the tab bar leads to it: %d", c)
	}
}

func TestAFileChosenByAnotherPersonIsNotSentAsThisOne(t *testing.T) {
	app, _, _ := newPhone(t)
	start := func(owner string) int {
		c, _ := phoneCall(t, "POST", app.URL+"/send/start", "sess=alice", []byte(`{"name":"a.jpg","size":100,"owner":"`+owner+`"}`), phoneHdr)
		return c
	}
	if c := start(otherID); c != 403 {
		t.Errorf("someone else's file: %d", c)
	}
	if c := start(aliceID); c != 201 {
		t.Errorf("one's own: %d", c)
	}
	if c := start(""); c != 201 {
		t.Errorf("no owner named (an older page): %d", c)
	}
}
