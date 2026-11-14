package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
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
	if res.StatusCode != 200 || !strings.Contains(page, "Chiara and Luca") || !strings.Contains(page, "2 photos are ready") {
		t.Errorf("%d %.200s", res.StatusCode, page)
	}
	if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Referrer-Policy") != "no-referrer" || res.Header.Get("X-Robots-Tag") != "noindex" {
		t.Errorf("never cached, indexed or passed on: %v", res.Header)
	}
	if strings.Contains(page, "Wedding") || strings.Contains(page, "Rossi") {
		t.Error("the page carries nothing of the photographer's album or the room's private name")
	}
	var answers []string
	for _, token := range []string{strings.Repeat("0", 64), paused, ended, "short"} {
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
	ids := regexp.MustCompile(`room/`+token+`/photo/([0-9a-f]{16})`).FindAllStringSubmatch(page, -1)
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
