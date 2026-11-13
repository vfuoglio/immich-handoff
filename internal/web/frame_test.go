package web_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/vault"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

// framePhotos is a tiny Immich that lists two photos in any album and gives a picture for each.
func framePhotos(t *testing.T) *httptest.Server {
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
			w.Write([]byte("a picture"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// withFrame gives the page a store and a hub whose Immich is framePhotos, and a frame owned by "family".
func withFrame(t *testing.T, st **store.Store, frame store.Frame) func(*web.Server) {
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
		frame.OwnerID, frame.OwnerName, frame.KeyID, frame.Key = uuid.MustParse(familyID), "Family", uuid.New(), "k-frame"
		if frame.AlbumID == uuid.Nil {
			frame.AlbumID = uuid.New()
		}
		if _, err := s.AddFrame(context.Background(), frame); err != nil {
			t.Fatal(err)
		}
		if err := h.Reload(context.Background()); err != nil {
			t.Fatal(err)
		}
		srv.Hub = h
		srv.S.Provider = h
	}
}

func TestAFramePageNeedsNoSignInAndShowsOnlyItsAlbumsPhotos(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("ab", 32)
	app, _ := setup(t, withFrame(t, &st, store.Frame{Token: token, Name: "Living room", AlbumName: "Holiday", Hold: 30}))

	res, err := http.Get(app.URL + "/frame/" + token)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	page := string(b)
	for _, want := range []string{`id="frame"`, `data-feed="frame/` + token + `/feed"`, `data-photo="frame/` + token + `/photo/"`, `data-seconds="30"`, `<base href="../"`,
		"Waiting for the first photo", `content="noindex"`, "<title>Living room</title>"} {
		if !strings.Contains(page, want) {
			t.Errorf("the frame page lacks %s", want)
		}
	}
	// a frame has the same buttons for the transition and the backdrop, and none for the layout (it only shows one photo)
	for _, want := range []string{`data-fx="zoom"`, `data-fx="slide"`, `data-bg="black"`, `class="wall-full"`, `data-palette="twilight"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the frame page lacks %s", want)
		}
	}
	if strings.Contains(page, `data-mode=`) {
		t.Error("a frame shows one photo at a time, so it has no layout buttons")
	}
	if res.StatusCode != 200 || res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("%d %v", res.StatusCode, res.Header)
	}
	if strings.Contains(page, "Photos you tag in Immich") || strings.Contains(page, "Holiday") {
		t.Error("the frame page must carry nothing of the account, not even the album's name")
	}

	code, body := do(t, "GET", app.URL+"/frame/"+token+"/feed", "", nil)
	var feed struct {
		Items   []struct{ ID string }
		Refresh int
	}
	if code != 200 || json.Unmarshal([]byte(body), &feed) != nil || len(feed.Items) != 2 || feed.Refresh != 120 {
		t.Fatalf("feed: %d %s", code, body)
	}
	for _, i := range feed.Items {
		if len(i.ID) != 16 || strings.Contains(body, "1111") || strings.Contains(body, "2222") {
			t.Errorf("the feed gives opaque ids and no Immich id: %s", body)
		}
	}
	pic, err := http.Get(app.URL + "/frame/" + token + "/photo/" + feed.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(pic.Body)
	pic.Body.Close()
	if pic.StatusCode != 200 || string(data) != "a picture" || pic.Header.Get("Content-Type") != "image/jpeg" ||
		!strings.Contains(pic.Header.Get("Content-Security-Policy"), "sandbox") || !strings.Contains(pic.Header.Get("Cache-Control"), "private") {
		t.Errorf("%d %q %v", pic.StatusCode, data, pic.Header)
	}
	if code, _ := do(t, "GET", app.URL+"/frame/"+token+"/photo/0000000000000000", "", nil); code != 404 {
		t.Errorf("a photo that is not in the album: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/frame/"+token+"/photo/..%2Ffeed", "", nil); code != 404 {
		t.Errorf("a path trick: %d", code)
	}
}

func TestAFrameLinkThatIsUnknownEndedOrExpiredIsTheSameNotFound(t *testing.T) {
	var st *store.Store
	live, ended, expired := strings.Repeat("a1", 32), strings.Repeat("b2", 32), strings.Repeat("c3", 32)
	app, _ := setup(t, withFrame(t, &st, store.Frame{Token: live, Name: "Live", AlbumName: "A", Hold: 15}), func(srv *web.Server) {
		ctx := context.Background()
		for _, f := range []store.Frame{{Token: ended, Name: "Ended", Hold: 15}, {Token: expired, Name: "Expired", Hold: 15, EndsAt: time.Now().Add(-time.Hour)}} {
			f.OwnerID, f.OwnerName, f.KeyID, f.Key, f.AlbumID = uuid.MustParse(familyID), "Family", uuid.New(), "k", uuid.New()
			saved, err := st.AddFrame(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			if f.Token == ended {
				st.EndFramesDue(ctx, time.Now()) // nothing due yet
				st.DeleteFrame(ctx, saved.ID)    // an ended frame's link is gone
			}
		}
	})
	var bodies []string
	for _, token := range []string{strings.Repeat("0", 64), ended, expired, "short"} {
		for _, path := range []string{"", "/feed", "/photo/abcdef0123456789"} {
			code, body := do(t, "GET", app.URL+"/frame/"+token+path, "", nil)
			if code != 404 {
				t.Errorf("%s%s: %d", token[:4], path, code)
			}
			bodies = append(bodies, body)
		}
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Errorf("a link said why it does not work: %q vs %q", b, bodies[0])
		}
	}
	if code, _ := do(t, "GET", app.URL+"/frame/"+live, "", nil); code != 200 {
		t.Errorf("the live one: %d", code)
	}
}

func TestTheOwnerSeesTheirFramesWithTheLinkAndOthersDoNotAndMakingAndEndingAreGuarded(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("d4", 32)
	app, _ := setup(t, withFrame(t, &st, store.Frame{Token: token, Name: "Kitchen", AlbumName: "Holiday", Hold: 15}))
	ctx := context.Background()
	_, mine := do(t, "GET", app.URL+"/", "sess=family", nil)
	for _, want := range []string{"Kitchen", `data-link="frame/` + token + `"`, "End frame", "Make a frame", "frame of Holiday"} {
		if !strings.Contains(mine, want) {
			t.Errorf("the owner's page lacks %q", want)
		}
	}
	if _, theirs := do(t, "GET", app.URL+"/", "sess=alice", nil); strings.Contains(theirs, token) || strings.Contains(theirs, "Kitchen") {
		t.Error("someone else sees the frame")
	}
	frames, _ := st.FramesOf(ctx, uuid.MustParse(familyID))
	id := frames[0].ID

	if code, _ := postForm(t, app.URL+"/frames/end", "", "frame="+id); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, _ := do(t, "POST", app.URL+"/frames/end", "sess=family", nil); code != 400 {
		t.Errorf("without the CSRF header: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/frames/end", "sess=alice", "frame="+id); code != 409 {
		t.Errorf("someone else ending it: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/frame/"+token, "", nil); code != 200 {
		t.Fatal("a refused end stopped the frame")
	}
	if code, msg := postForm(t, app.URL+"/frames", "sess=family", "name=x&album=not-a-uuid&ends=0&hold=15"); code != 409 {
		t.Errorf("a bad album: %d %q", code, msg)
	}
	if code, msg := postForm(t, app.URL+"/frames", "sess=family", "name=&album="+uuid.NewString()+"&ends=0&hold=15"); code != 409 || !strings.Contains(msg, "name") {
		t.Errorf("no name: %d %q", code, msg)
	}
	if code, _ := postForm(t, app.URL+"/frames/end", "sess=family", "frame="+id); code != 204 {
		t.Fatalf("ending it: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/frame/"+token, "", nil); code != 404 {
		t.Errorf("an ended frame's link: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/fragments/frame-albums", "", nil); code != 401 {
		t.Errorf("the album list needs a sign-in: %d", code)
	}
}
