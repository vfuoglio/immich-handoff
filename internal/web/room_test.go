package web_test

import (
	"context"
	"net/http"
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
