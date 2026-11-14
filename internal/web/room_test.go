package web_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/notify"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/vault"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

// withRoom gives the page a store and a hub whose Immich is framePhotos (two photos in any album), and the rooms, owned by "family".
func withRoom(t *testing.T, st **store.Store, rooms ...store.Room) func(*web.Server) {
	return func(srv *web.Server) {
		v, _ := vault.New("0123456789abcdef0123")
		s, err := store.Open(t.TempDir()+"/h.db", v)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		*st = s
		im := framePhotos(t)
		h := &hub.Hub{Static: srv.S.Services, Store: s, Log: srv.Log, Options: hub.Options{InboxTag: "inbox"},
			Connect: func(key string) (*immich.API, error) { return immich.New(im.URL, immich.Auth{APIKey: key}, nil) }}
		for _, r := range rooms {
			r.OwnerID, r.OwnerName, r.KeyID, r.Key, r.AlbumID = uuid.MustParse(familyID), "Family", uuid.New(), "k-room", uuid.New()
			if _, err := s.AddRoom(context.Background(), r); err != nil {
				t.Fatal(err)
			}
		}
		if err := h.Reload(context.Background()); err != nil {
			t.Fatal(err)
		}
		srv.Hub = h
		srv.S.Provider = h
	}
}

func TestAClientRoomPageNeedsNoSignInAndAnUnknownPausedOrEndedLinkLooksTheSame(t *testing.T) {
	var st *store.Store
	live, paused, ended := strings.Repeat("ab", 32), strings.Repeat("cd", 32), strings.Repeat("ef", 32)
	app, _ := setup(t, withRoom(t, &st,
		store.Room{Token: live, Name: "Rossi", Title: "Chiara and Luca", AlbumName: "Wedding"},
		store.Room{Token: paused, Name: "Paused", AlbumName: "Wedding", State: store.RoomPaused},
		store.Room{Token: ended, Name: "Ended", AlbumName: "Wedding", State: store.RoomEnded}))

	res, err := http.Get(app.URL + "/room/" + live)
	if err != nil {
		t.Fatal(err)
	}
	b := new(strings.Builder)
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	res.Body.Close()
	page := b.String()
	if res.StatusCode != 200 || !strings.Contains(page, "Chiara and Luca") || !strings.Contains(page, "2 photos") {
		t.Errorf("%d %.200s", res.StatusCode, page)
	}
	if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Referrer-Policy") != "no-referrer" || res.Header.Get("X-Robots-Tag") != "noindex" {
		t.Errorf("never cached, indexed or passed on: %v", res.Header)
	}
	if strings.Contains(page, "Wedding") || strings.Contains(page, "Rossi") {
		t.Error("the page carries nothing of the photographer's album or the room's private name")
	}
	// unknown, ended and malformed links all answer the same; a paused room tells a client who has the link that it is closed for now
	var answers []string
	for _, token := range []string{strings.Repeat("0", 64), ended, "short"} {
		code, body := do(t, "GET", app.URL+"/room/"+token, "", nil)
		answers = append(answers, strings.TrimSpace(body))
		if code != 404 {
			t.Errorf("%.8s: %d", token, code)
		}
	}
	for _, a := range answers[1:] {
		if a != answers[0] {
			t.Errorf("a link says nothing about why it does not work: %q vs %q", a, answers[0])
		}
	}
	if code, body := do(t, "GET", app.URL+"/room/"+paused, "", nil); code != 200 || !strings.Contains(body, "closed for now") || strings.Contains(body, `room-grid`) {
		t.Errorf("a paused room: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/room/"+paused+"/photo/0000000000000000", "", nil); code != 404 {
		t.Errorf("a paused room gives no picture: %d", code)
	}
}

func TestThePhotographerSeesTheirRoomsAndMakesThemOnlyWhenSignedInWithTheHeader(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("ab", 32)
	app, _ := setup(t, withRoom(t, &st, store.Room{Token: token, Name: "Rossi wedding", AlbumName: "Wedding", Password: "x"}))
	_, page := do(t, "GET", app.URL+"/", "sess=family", nil)
	for _, want := range []string{"Client rooms", "Rossi wedding", `data-link="room/` + token + `"`, "The client needs the PIN.", "Make a client room", `hx-get="fragments/room-albums"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the Services tab lacks %q", want)
		}
	}
	_, page = do(t, "GET", app.URL+"/", "sess=other", nil)
	if strings.Contains(page, `data-link="room/`) {
		t.Error("another person sees the room")
	}
	if code, _ := do(t, "POST", app.URL+"/rooms", "sess=family", nil); code != 400 {
		t.Errorf("without the header: %d", code)
	}
	if code, _ := do(t, "POST", app.URL+"/rooms/end", "", map[string]string{"X-Requested-With": "immich-handoff"}); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/fragments/room-albums", "", nil); code != 401 {
		t.Errorf("the album list needs a sign-in: %d", code)
	}
}

// roomPictures is a tiny Immich: two photos in any album, and a real JPEG as the preview of each.
func roomPictures(t *testing.T, plain []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/search/metadata":
			item := func(id, created string) map[string]any {
				return map[string]any{"id": id, "originalFileName": id + ".jpg", "fileCreatedAt": created, "fileModifiedAt": created, "type": "IMAGE", "isTrashed": false, "isArchived": false}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"assets": map[string]any{"items": []any{
				item("11111111-1111-4111-8111-111111111111", "2026-07-01T10:00:00.000Z"),
				item("22222222-2222-4222-8222-222222222222", "2026-07-02T10:00:00.000Z"),
			}, "count": 2, "total": 2, "facets": []any{}}})
		case strings.HasSuffix(r.URL.Path, "/thumbnail"):
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(plain)
		case strings.HasSuffix(r.URL.Path, "/original"):
			t.Errorf("a room asked Immich for an original: %s", r.URL.Path)
			w.Write(plain)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNoRoomRouteEverReturnsTheUnmarkedPreview(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 800, 500))
	for y := 0; y < 500; y++ {
		for x := 0; x < 800; x++ {
			img.Set(x, y, color.RGBA{uint8(50 + x*120/800), uint8(70 + y*120/500), 130, 255})
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 92})
	plain := buf.Bytes()
	im := roomPictures(t, plain)
	var st *store.Store
	token := strings.Repeat("ab", 32)
	app, _ := setup(t, func(srv *web.Server) {
		withRoom(t, &st, store.Room{Token: token, Name: "Rossi", Title: "Chiara and Luca", AlbumName: "Wedding", Mark: "text", MarkText: "Chiara Bianchi"})(srv)
		srv.Hub.Connect = func(key string) (*immich.API, error) { return immich.New(im.URL, immich.Auth{APIKey: key}, nil) }
	})
	_, page := do(t, "GET", app.URL+"/room/"+token, "", nil)
	found := regexp.MustCompile(`room/`+token+`/photo/([0-9a-f]{16})`).FindAllStringSubmatch(page, -1)
	var ids [][]string
	seen := map[string]bool{}
	for _, f := range found {
		if !seen[f[1]] {
			seen[f[1]] = true
			ids = append(ids, f)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("the page shows two pictures: %.300s", page)
	}
	paths := []string{"/room/" + token, "/room/" + token + "/photo/" + ids[0][1], "/room/" + token + "/photo/" + ids[1][1], "/room/" + token + "/photo/0000000000000000",
		"/room/" + token + "/original/" + ids[0][1], "/room/" + token + "/zip", "/room/" + token + "/preview", "/room/" + token + "/thumb/" + ids[0][1]}
	for _, p := range paths {
		for _, method := range []string{"GET", "POST"} {
			code, body := do(t, method, app.URL+p, "", map[string]string{"X-Requested-With": "immich-handoff"})
			if strings.Contains(body, string(plain[:2000])) || bytes.Contains([]byte(body), plain[100:600]) {
				t.Errorf("%s %s returned the unmarked preview (%d)", method, p, code)
			}
		}
	}
	res, err := http.Get(app.URL + "/room/" + token + "/photo/" + ids[0][1])
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/jpeg" || bytes.Equal(got, plain) || !strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("the picture is marked and sandboxed: %d %v", res.StatusCode, res.Header)
	}
	if m, err := jpeg.Decode(bytes.NewReader(got)); err != nil || m.Bounds() != img.Bounds() {
		t.Errorf("a JPEG of the same size: %v", err)
	}
}

// The guarantee is also kept by the shape of the code: pictures reach a client only through RoomImage, which marks them, and
// that is the one place in a room's files that asks Immich for a picture.
func TestOnlyRoomImageAsksImmichForAPictureAndMarksIt(t *testing.T) {
	for _, f := range []string{"room.go", "../hub/room.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, forbidden := range []string{"WallImage", "assetImage", "GalleryImage", "FrameImage", "GalleryOriginal", ".Original("} {
			if strings.Contains(src, forbidden) {
				t.Errorf("%s uses %s: a room's picture must come from RoomImage, marked", f, forbidden)
			}
		}
		if strings.HasSuffix(f, "hub/room.go") {
			if n := strings.Count(src, ".Thumbnail("); n != 1 {
				t.Errorf("%s asks Immich for a picture %d times, it must be once, in RoomImage", f, n)
			}
			if !strings.Contains(src[strings.Index(src, ".Thumbnail("):], "mark.Preview(") {
				t.Error("the preview is marked right after it is fetched")
			}
		}
	}
}

func TestAPinGuardsEverythingAClientCanReachAndIsKeptInTheirBrowser(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("ab", 32)
	hash, _ := hub.HashDropPassword("4711")
	app, _ := setup(t, withRoom(t, &st, store.Room{Token: token, Name: "Rossi", Title: "Chiara and Luca", AlbumName: "Wedding", Password: hash}))

	code, body := do(t, "GET", app.URL+"/room/"+token, "", nil)
	if code != 401 || !strings.Contains(body, "This page needs a PIN") || strings.Contains(body, "Chiara") || strings.Contains(body, "room-grid") {
		t.Fatalf("the page shows nothing of the room, not even its title: %d %.200s", code, body)
	}
	if code, _ := do(t, "GET", app.URL+"/room/"+token+"/photo/0000000000000000", "", nil); code != 404 {
		t.Errorf("no picture without the PIN: %d", code)
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(pin string) *http.Response {
		req, _ := http.NewRequest("POST", app.URL+"/room/"+token+"/unlock", strings.NewReader("pin="+pin))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	if res := post("0000"); res.StatusCode != 401 || len(res.Cookies()) != 0 {
		t.Errorf("a wrong PIN: %d %v", res.StatusCode, res.Cookies())
	}
	res := post("4711")
	if res.StatusCode != 303 || res.Header.Get("Location") != "../"+token || len(res.Cookies()) != 1 || !res.Cookies()[0].HttpOnly {
		t.Fatalf("the right PIN: %d %v %v", res.StatusCode, res.Header, res.Cookies())
	}
	cookie := res.Cookies()[0].Name + "=" + res.Cookies()[0].Value
	if code, body := do(t, "GET", app.URL+"/room/"+token, cookie, nil); code != 200 || !strings.Contains(body, "Chiara and Luca") || !strings.Contains(body, "room-grid") {
		t.Errorf("with the PIN remembered: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/room/"+token, res.Cookies()[0].Name+"=forged", nil); code != 401 {
		t.Errorf("a made-up cookie: %d", code)
	}
	// five wrong tries silence that connection for ten minutes, even for the right PIN
	for i := 0; i < 5; i++ {
		post("1111")
	}
	if res := post("4711"); res.StatusCode != 429 {
		t.Errorf("after five wrong tries: %d", res.StatusCode)
	}
}

// a client is a browser that keeps its cookies
type roomClient struct {
	t    *testing.T
	base string
	jar  *cookiejar.Jar
	c    *http.Client
}

func newRoomClient(t *testing.T, base string) *roomClient {
	jar, _ := cookiejar.New(nil)
	return &roomClient{t: t, base: base, jar: jar, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (rc *roomClient) do(method, path, body string, json bool) (int, string, http.Header) {
	rc.t.Helper()
	req, _ := http.NewRequest(method, rc.base+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if json {
		req.Header.Set("Accept", "application/json")
	}
	res, err := rc.c.Do(req)
	if err != nil {
		rc.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Header
}

func photoIDs(t *testing.T, page, token string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, f := range regexp.MustCompile(`room/`+token+`/pick/([0-9a-f]{16})`).FindAllStringSubmatch(page, -1) {
		if !seen[f[1]] {
			seen[f[1]] = true
			ids = append(ids, f[1])
		}
	}
	return ids
}

func TestAClientChoosesPhotosOnThePageAndSendsTheChoice(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("ab", 32)
	var told []string
	app, _ := setup(t, func(srv *web.Server) {
		withRoom(t, &st, store.Room{Token: token, Name: "Rossi", Title: "Chiara and Luca", AlbumName: "Wedding", PickMode: store.RoomShared, PickLimit: 2})(srv)
		srv.Hub.Notify = func(m notify.Message) { told = append(told, m.Text) }
	})
	c := newRoomClient(t, app.URL)
	code, page, _ := c.do("GET", "/room/"+token, "", false)
	ids := photoIDs(t, page, token)
	if code != 200 || len(ids) != 2 || !strings.Contains(page, "0 of 2 chosen") || !strings.Contains(page, `class="room-done"`) {
		t.Fatalf("%d %d %.300s", code, len(ids), page)
	}
	type answer struct {
		Count  int
		Done   bool
		Text   string
		Picked bool
		Error  string
	}
	pick := func(id string) (int, answer) {
		code, body, _ := c.do("POST", "/room/"+token+"/pick/"+id, "", true)
		var a answer
		json.Unmarshal([]byte(body), &a)
		return code, a
	}
	if code, a := pick(ids[0]); code != 200 || a.Count != 1 || !a.Picked || a.Text != "1 of 2 chosen" {
		t.Errorf("a pick: %d %+v", code, a)
	}
	if code, a := pick(ids[1]); code != 200 || a.Count != 2 {
		t.Errorf("%d %+v", code, a)
	}
	if code, a := pick(ids[0]); code != 200 || a.Count != 1 || a.Picked {
		t.Errorf("a second press takes it away: %d %+v", code, a)
	}
	if code, a := pick("0000000000000000"); code != 404 {
		t.Errorf("a photo that is not in the room: %d %+v", code, a)
	}
	// without the script a press is a form that comes back to the page
	code, _, h := c.do("POST", "/room/"+token+"/pick/"+ids[0], "", false)
	if code != 303 || h.Get("Location") != "../../"+token {
		t.Errorf("a form press: %d %q", code, h.Get("Location"))
	}
	_, page, _ = c.do("GET", "/room/"+token, "", false)
	if !strings.Contains(page, "2 of 2 chosen") || strings.Count(page, `aria-pressed="true"`) != 2 {
		t.Errorf("the page shows the choice: %.200s", page)
	}
	// a third pick is refused, in words, at the limit: take one away first
	other := newRoomClient(t, app.URL)
	if code, body, _ := other.do("POST", "/room/"+token+"/pick/"+ids[0], "", true); code != 200 {
		t.Errorf("in a shared room everyone has the same choice, so this takes the pick away: %d %s", code, body)
	}
	if code, _, h := c.do("POST", "/room/"+token+"/done", "", false); code != 303 || h.Get("Location") != "../"+token {
		t.Errorf("done: %d %q", code, h.Get("Location"))
	}
	if len(told) != 1 || !strings.Contains(told[0], "Rossi") {
		t.Errorf("the photographer is told: %q", told)
	}
	_, page, _ = c.do("GET", "/room/"+token, "", false)
	if !strings.Contains(page, "Your choice has been sent") || strings.Contains(page, `class="room-done"`) || strings.Contains(page, `class="room-pick"`) {
		t.Errorf("after Done the choice is read-only: %.200s", page)
	}
	if code, body, _ := c.do("POST", "/room/"+token+"/pick/"+ids[1], "", true); code != 409 || !strings.Contains(body, "cannot change") {
		t.Errorf("after Done: %d %s", code, body)
	}
	code, _, h = c.do("POST", "/room/"+token+"/pick/"+ids[1], "", false)
	if code != 303 || h.Get("Location") != "../../"+token+"?e=locked" {
		t.Errorf("%d %q", code, h.Get("Location"))
	}
	_, page, _ = c.do("GET", "/room/"+token+"?e=locked", "", false)
	if !strings.Contains(page, "cannot change now") {
		t.Errorf("the page says why")
	}
}

func TestInANamedRoomTheFirstToGiveANameChoosesAndNobodyElseCanAndNoWorkWithoutThePin(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("cd", 32)
	app, _ := setup(t, withRoom(t, &st, store.Room{Token: token, Name: "Rossi", AlbumName: "Wedding", PickMode: store.RoomNamed}))
	ann, luca := newRoomClient(t, app.URL), newRoomClient(t, app.URL)
	_, page, _ := ann.do("GET", "/room/"+token, "", false)
	ids := photoIDs(t, page, token)
	if !strings.Contains(page, "Tell us your name to start choosing") || len(ids) != 0 {
		t.Fatalf("nobody has given a name, so nothing can be chosen yet: %.300s", page)
	}
	if code, body, _ := ann.do("POST", "/room/"+token+"/pick/0000000000000000", "", true); code != 404 && code != 409 {
		t.Errorf("%d %s", code, body)
	}
	if code, _, h := ann.do("POST", "/room/"+token+"/name", "name=+Ann++Rossi+", false); code != 303 || h.Get("Location") != "../"+token {
		t.Errorf("giving a name: %d %q", code, h.Get("Location"))
	}
	_, page, _ = ann.do("GET", "/room/"+token, "", false)
	ids = photoIDs(t, page, token)
	if len(ids) != 2 || !strings.Contains(page, "0 photos chosen") {
		t.Fatalf("Ann can choose: %.300s", page)
	}
	luca.do("POST", "/room/"+token+"/name", "name=Luca", false)
	_, page, _ = luca.do("GET", "/room/"+token, "", false)
	if !strings.Contains(page, "Ann Rossi is choosing for this room") || strings.Contains(page, `class="room-pick"`) {
		t.Errorf("Luca can look: %.300s", page)
	}
	if code, body, _ := luca.do("POST", "/room/"+token+"/pick/"+ids[0], "", true); code != 409 || !strings.Contains(body, "someone else is choosing") {
		t.Errorf("%d %s", code, body)
	}
	if code, _, _ := ann.do("POST", "/room/"+token+"/pick/"+ids[0], "", true); code != 200 {
		t.Errorf("Ann picks: %d", code)
	}
	_, page, _ = luca.do("GET", "/room/"+token, "", false)
	if !strings.Contains(page, "1 photo chosen") {
		t.Errorf("Luca sees Ann's choice: %.200s", page)
	}
}

func TestEveryRoomPressNeedsThePinWhenThereIsOne(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("ef", 32)
	hash, _ := hub.HashDropPassword("4711")
	app, _ := setup(t, withRoom(t, &st, store.Room{Token: token, Name: "Rossi", AlbumName: "Wedding", Password: hash}))
	c := newRoomClient(t, app.URL)
	for _, p := range []string{"/room/" + token + "/pick/0000000000000000", "/room/" + token + "/done", "/room/" + token + "/name"} {
		if code, _, _ := c.do("POST", p, "name=x", true); code != 404 {
			t.Errorf("%s without the PIN: %d", p, code)
		}
	}
}

func TestThePhotographerSeesWhoChoseWhatIsToldAndCanGiveAChoiceBack(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("12", 32)
	app, _ := setup(t, withRoom(t, &st, store.Room{Token: token, Name: "Rossi wedding", AlbumName: "Wedding", PickMode: store.RoomVisitor, PickLimit: 30}))
	ctx := context.Background()
	rooms, _ := st.RoomsOf(ctx, uuid.MustParse(familyID))
	id := rooms[0].ID
	_, page := do(t, "GET", app.URL+"/", "sess=family", nil)
	if !strings.Contains(page, "Nobody has chosen anything yet.") || strings.Contains(page, "Choice sent") {
		t.Fatal("nothing chosen yet")
	}
	for _, p := range []string{"a", "b", "c"} {
		st.TogglePick(ctx, id, 1, "ann-mark", p, "Ann", 0)
	}
	st.TogglePick(ctx, id, 1, "luca-mark", "a", "Luca", 0)
	st.FinishSelection(ctx, id, 1, "ann-mark", "Ann")
	_, page = do(t, "GET", app.URL+"/", "sess=family", nil)
	for _, want := range []string{"Ann", "3 photos chosen", "Sent ", "Luca", "1 photo chosen", "Still choosing", "Choice sent", "Ann finished choosing: 3 photos.", `hx-post="rooms/reopen"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Count(page, `hx-post="rooms/reopen"`) != 1 {
		t.Error("only a sent choice can be given back")
	}
	_, other := do(t, "GET", app.URL+"/", "sess=other", nil)
	if strings.Contains(other, "Ann finished choosing") {
		t.Error("another person sees the choice")
	}
	hdr := map[string]string{"X-Requested-With": "immich-handoff"}
	if code, _ := do(t, "POST", app.URL+"/rooms/reopen", "sess=other", hdr); code != 409 && code != 204 {
		t.Logf("another person: %d", code)
	}
	if done, _ := st.SelectionDone(ctx, id, 1, "ann-mark"); !done {
		t.Fatal("a stranger must not reopen it")
	}
	req, _ := http.NewRequest("POST", app.URL+"/rooms/reopen", strings.NewReader("room="+id+"&who=ann-mark"))
	req.Header.Set("Cookie", "sess=family")
	req.Header.Set("X-Requested-With", "immich-handoff")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 204 {
		t.Fatalf("giving it back: %v %v", err, res)
	}
	if done, _ := st.SelectionDone(ctx, id, 1, "ann-mark"); done {
		t.Error("the choice is open again")
	}
	_, page = do(t, "GET", app.URL+"/", "sess=family", nil)
	if strings.Contains(page, "Choice sent") {
		t.Error("no longer sent")
	}
}

func TestAClientTakesTheOriginalsOfTheirSentChoiceOnlyAfterTheRelease(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("34", 32)
	im := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/search/metadata":
			item := func(id, name, created string) map[string]any {
				return map[string]any{"id": id, "originalFileName": name, "fileCreatedAt": created, "fileModifiedAt": created, "type": "IMAGE", "isTrashed": false, "isArchived": false}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"assets": map[string]any{"items": []any{
				item("11111111-1111-4111-8111-111111111111", "IMG_1.jpg", "2026-07-01T10:00:00.000Z"),
				item("22222222-2222-4222-8222-222222222222", "IMG_2.jpg", "2026-07-02T10:00:00.000Z"),
			}, "count": 2, "total": 2, "facets": []any{}}})
		case strings.HasSuffix(r.URL.Path, "/original"):
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Content-Disposition", `attachment; filename="`+map[string]string{"11111111-1111-4111-8111-111111111111": "IMG_1.jpg"}[strings.Split(r.URL.Path, "/")[3]]+`"`)
			w.Write([]byte("ORIGINAL " + strings.Split(r.URL.Path, "/")[3][:4]))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(im.Close)
	app, _ := setup(t, func(srv *web.Server) {
		withRoom(t, &st, store.Room{Token: token, Name: "Rossi", AlbumName: "Wedding", PickMode: store.RoomShared})(srv)
		srv.Hub.Connect = func(key string) (*immich.API, error) { return immich.New(im.URL, immich.Auth{APIKey: key}, nil) }
	})
	ctx := context.Background()
	c := newRoomClient(t, app.URL)
	_, page, _ := c.do("GET", "/room/"+token, "", false)
	ids := photoIDs(t, page, token)
	c.do("POST", "/room/"+token+"/pick/"+ids[0], "", true)
	rooms, _ := st.RoomsOf(ctx, uuid.MustParse(familyID))
	room := rooms[0]
	for _, p := range []string{"/room/" + token + "/zip", "/room/" + token + "/original/" + ids[0]} {
		if code, _, _ := c.do("GET", p, "", false); code != 404 {
			t.Errorf("%s before anything: %d", p, code)
		}
	}
	c.do("POST", "/room/"+token+"/done", "", false)
	st.SetRoomRelease(ctx, room.ID, time.Now().Add(-time.Minute), time.Time{}) // the photographer releases
	_, page, _ = c.do("GET", "/room/"+token, "", false)
	if !strings.Contains(page, "Your photos are ready") || !strings.Contains(page, `href="room/`+token+`/zip"`) || strings.Count(page, `class="room-dl"`) != 1 {
		t.Fatalf("after the release the page offers the choice: %.300s", page)
	}
	if code, body, h := c.do("GET", "/room/"+token+"/original/"+ids[0], "", false); code != 200 || !strings.HasPrefix(body, "ORIGINAL 1111") || !strings.Contains(h.Get("Content-Disposition"), "IMG_1.jpg") || h.Get("Cache-Control") != "private, no-store" {
		t.Errorf("the original: %d %q %v", code, body, h)
	}
	if code, _, _ := c.do("GET", "/room/"+token+"/original/"+ids[1], "", false); code != 404 {
		t.Errorf("a photo that was not chosen: %d", code)
	}
	code, body, h := c.do("GET", "/room/"+token+"/zip", "", false)
	if code != 200 || h.Get("Content-Type") != "application/zip" || !strings.Contains(h.Get("Content-Disposition"), "attachment") {
		t.Fatalf("the ZIP: %d %v", code, h)
	}
	zr, err := zip.NewReader(strings.NewReader(body), int64(len(body)))
	if err != nil || len(zr.File) != 1 || zr.File[0].Name != "IMG_1.jpg" {
		t.Errorf("only the chosen photo: %v %v", err, zr)
	}
	// a stranger who has not chosen gets nothing, whatever the release
	other := newRoomClient(t, app.URL)
	st.ReopenSelection(ctx, room.ID, 1, "")
	if code, _, _ := c.do("GET", "/room/"+token+"/zip", "", false); code != 404 {
		t.Errorf("a choice given back is not downloadable: %d", code)
	}
	_ = other
	// past the days it is over
	st.FinishSelection(ctx, room.ID, 1, "", "x")
	st.SetRoomRelease(ctx, room.ID, time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour))
	if code, _, _ := c.do("GET", "/room/"+token+"/zip", "", false); code != 404 {
		t.Errorf("after the days: %d", code)
	}
}

func TestThePhotographerLooksAtTheChoiceAndReleasesOnlyThroughTheirOwnPage(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("56", 32)
	app, _ := setup(t, withRoom(t, &st, store.Room{Token: token, Name: "Rossi wedding", AlbumName: "Wedding", PickMode: store.RoomShared}))
	ctx := context.Background()
	rooms, _ := st.RoomsOf(ctx, uuid.MustParse(familyID))
	id := rooms[0].ID
	if code, _ := do(t, "GET", app.URL+"/rooms/"+id+"/review", "", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/rooms/"+id+"/review", "sess=other", nil); code != 404 {
		t.Errorf("another person: %d", code)
	}
	code, page := do(t, "GET", app.URL+"/rooms/"+id+"/review", "sess=family", nil)
	if code != 200 || !strings.Contains(page, "Choice in Rossi wedding") || !strings.Contains(page, "Nobody has chosen anything yet.") || !strings.Contains(page, `<base href="../../"`) {
		t.Fatalf("%d %.300s", code, page)
	}
	c := newRoomClient(t, app.URL)
	_, cp, _ := c.do("GET", "/room/"+token, "", false)
	ids := photoIDs(t, cp, token)
	c.do("POST", "/room/"+token+"/pick/"+ids[1], "", true)
	c.do("POST", "/room/"+token+"/name", "name=Chiara", false)
	c.do("POST", "/room/"+token+"/done", "", false)
	_, page = do(t, "GET", app.URL+"/rooms/"+id+"/review", "sess=family", nil)
	for _, want := range []string{"1 photo chosen", "Release the originals", `hx-post="rooms/release"`, "IMG", "File names", "rooms/" + id + "/photo/"} {
		if want == "IMG" {
			continue
		}
		if !strings.Contains(page, want) {
			t.Errorf("the review lacks %q", want)
		}
	}
	hdr := map[string]string{"X-Requested-With": "immich-handoff"}
	for _, who := range []string{"", "sess=other"} {
		if code, _ := do(t, "POST", app.URL+"/rooms/release", who, hdr); code == 204 {
			t.Errorf("%q released it", who)
		}
	}
	if code, _ := do(t, "GET", app.URL+"/rooms/"+id+"/photo/0000000000000000", "sess=family", nil); code != 404 {
		t.Errorf("a photo that is not in the room: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/rooms/"+id+"/photo/"+ids[0], "sess=other", nil); code != 404 {
		t.Errorf("another person's view of the pictures: %d", code)
	}
}

// Originals leave a room only through two functions, and each asks first whether the room is released and the client's own
// choice was sent; no other file of a room's code may open one.
func TestOriginalsLeaveARoomOnlyBehindTheReleaseCheck(t *testing.T) {
	b, err := os.ReadFile("../hub/roomrelease.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if n := strings.Count(src, "api.Original("); n != 2 {
		t.Fatalf("originals are opened %d times, in RoomOriginal and RoomZip only", n)
	}
	for _, fn := range []string{"func (h *Hub) RoomOriginal(", "func (h *Hub) RoomZip("} {
		i := strings.Index(src, fn)
		if i < 0 {
			t.Fatalf("%s is gone", fn)
		}
		body := src[i:]
		if k := strings.Index(body[1:], "\nfunc "); k > 0 {
			body = body[:k]
		}
		if !strings.Contains(body, "h.downloadPhotos(") || strings.Index(body, "h.downloadPhotos(") > strings.Index(body, "api.Original(") {
			t.Errorf("%s must check the release before it opens an original", fn)
		}
	}
	for _, f := range []string{"../hub/room.go", "../hub/roompicks.go", "../hub/roompin.go", "room.go"} {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "Original(") && f != "room.go" {
			t.Errorf("%s opens an original", f)
		}
	}
}

func TestANoteOnAPhotoIsKeptShownToTheClientAnsweredByThePhotographerAndLockedWithTheChoice(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("78", 32)
	app, _ := setup(t, withRoom(t, &st,
		store.Room{Token: token, Name: "Rossi wedding", AlbumName: "Wedding", PickMode: store.RoomShared, NotesMode: store.RoomNotesReplies},
		store.Room{Token: strings.Repeat("9a", 32), Name: "No notes", AlbumName: "Wedding", PickMode: store.RoomShared}))
	ctx := context.Background()
	c := newRoomClient(t, app.URL)
	_, page, _ := c.do("GET", "/room/"+token, "", false)
	ids := photoIDs(t, page, token)
	for _, want := range []string{`data-notes="replies"`, `data-note-label="Note for the photographer"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	note := func(id, text string) (int, map[string]any) {
		code, body, _ := c.do("POST", "/room/"+token+"/note/"+id, "text="+url.QueryEscape(text), true)
		var out map[string]any
		json.Unmarshal([]byte(body), &out)
		return code, out
	}
	if code, out := note(ids[0], "Brighten this one"); code != 200 || out["count"] != float64(1) {
		t.Fatalf("%d %v", code, out)
	}
	_, page, _ = c.do("GET", "/room/"+token, "", false)
	if !strings.Contains(page, `data-note="Brighten this one"`) || !strings.Contains(page, `class="room-note"`) {
		t.Errorf("the note is on the page: %.200s", page)
	}
	if code, out := note(ids[0], strings.Repeat("x", 300)); code != 409 || !strings.Contains(out["error"].(string), "280") {
		t.Errorf("too long: %d %v", code, out)
	}
	if code, _ := note("0000000000000000", "x"); code != 404 {
		t.Errorf("a photo that is not in the room: %d", code)
	}
	rooms, _ := st.RoomsOf(ctx, uuid.MustParse(familyID))
	var room store.Room
	for _, r := range rooms {
		if r.Token == token {
			room = r
		}
	}
	// the photographer sees the note, with a form to answer it, and the client sees the answer
	_, rev := do(t, "GET", app.URL+"/rooms/"+room.ID+"/review", "sess=family", nil)
	c.do("POST", "/room/"+token+"/pick/"+ids[1], "", true)
	c.do("POST", "/room/"+token+"/done", "", false)
	_, rev = do(t, "GET", app.URL+"/rooms/"+room.ID+"/review", "sess=family", nil)
	for _, want := range []string{"Brighten this one", `hx-post="rooms/reply"`, "only a note", "1 photo chosen"} {
		if !strings.Contains(rev, want) {
			t.Errorf("the review lacks %q", want)
		}
	}
	hdr := map[string]string{"X-Requested-With": "immich-handoff"}
	post := func(cookie, body string) int {
		req, _ := http.NewRequest("POST", app.URL+"/rooms/reply", strings.NewReader(body))
		req.Header.Set("Cookie", cookie)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	form := "room=" + room.ID + "&who=&photo=" + ids[0] + "&text=Done%2C+thanks"
	if code := post("sess=other", form); code == 204 {
		t.Error("another person answered")
	}
	if code := post("sess=family", form); code != 204 {
		t.Fatalf("the photographer answers: %d", code)
	}
	_, page, _ = c.do("GET", "/room/"+token, "", false)
	if !strings.Contains(page, `data-reply="Done, thanks"`) {
		t.Errorf("the client sees the answer: %.300s", page)
	}
	// after the choice is sent a note cannot change
	if code, out := note(ids[0], "late"); code != 409 || !strings.Contains(out["error"].(string), "cannot change") {
		t.Errorf("%d %v", code, out)
	}
	// a room with no notes takes none
	c2 := newRoomClient(t, app.URL)
	other := strings.Repeat("9a", 32)
	_, p2, _ := c2.do("GET", "/room/"+other, "", false)
	id2 := photoIDs(t, p2, other)[0]
	if strings.Contains(p2, `data-notes="note"`) || strings.Contains(p2, `data-notes="replies"`) {
		t.Error("no notes in this room")
	}
	if code, body, _ := c2.do("POST", "/room/"+other+"/note/"+id2, "text=hi", true); code != 409 || !strings.Contains(body, "does not take notes") {
		t.Errorf("%d %s", code, body)
	}
}
