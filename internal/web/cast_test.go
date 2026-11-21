package web_test

import (
	"context"
	"encoding/json"
	"io"
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

// withCast gives the page a store and a hub whose key reads framePhotos (two photos in any source), and one TV paired by "other".
func withCast(t *testing.T, st **store.Store, tv *store.CastSession) func(*web.Server) {
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
		ctx := context.Background()
		if err := s.SetCastKey(ctx, uuid.MustParse(otherID), store.CastKey{KeyID: uuid.New(), Key: "k-cast"}); err != nil {
			t.Fatal(err)
		}
		tv.ID, tv.UserID, tv.Kind, tv.SourceName, tv.Seconds, tv.FX = strings.Repeat("ab", 32), uuid.MustParse(otherID), store.CastNew, "", 15, "fade"
		if _, err := s.AddCastSession(ctx, *tv); err != nil {
			t.Fatal(err)
		}
		if err := h.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		srv.Hub = h
		srv.S.Provider = h
	}
}

func TestATVPageNeedsNoSignInAndAnswersOnlyForItsOwnSession(t *testing.T) {
	var st *store.Store
	var tv store.CastSession
	app, _ := setup(t, withCast(t, &st, &tv))
	for _, p := range []string{"/tv", "/tv/legacy"} {
		code, body := do(t, "GET", app.URL+p, "", nil)
		if code != 200 || !strings.Contains(body, "tv-boot.js") || !strings.Contains(body, `class="tv-code"`) || !strings.Contains(body, `name="robots" content="noindex"`) {
			t.Errorf("%s: %d %.300s", p, code, body)
		}
	}
	if _, body := do(t, "GET", app.URL+"/tv/legacy", "", nil); !strings.Contains(body, `data-legacy="1"`) || !strings.Contains(body, `<base href="../">`) || strings.Contains(body, "app.css") {
		t.Errorf("the legacy page forces itself, finds its files one level up and loads no dashboard styles: %.400s", body)
	}
	if _, body := do(t, "GET", app.URL+"/tv", "", nil); !strings.Contains(body, `data-legacy="0"`) {
		t.Errorf("the usual page does not force legacy")
	}
	// a TV asks for a code
	res, body := postNoHeader(t, app.URL+"/tv/new")
	var made struct {
		S    string `json:"s"`
		Code string `json:"code"`
	}
	if res != 200 || json.Unmarshal([]byte(body), &made) != nil || len(made.Code) != 6 || len(made.S) != 64 {
		t.Fatalf("a new TV: %d %s", res, body)
	}
	if _, st := do(t, "GET", app.URL+"/tv/state?s="+made.S, "", nil); !strings.Contains(st, `"state":"waiting"`) {
		t.Errorf("waiting: %s", st)
	}
	// the paired TV's state, feed and pictures
	if _, st := do(t, "GET", app.URL+"/tv/state?s="+tv.ID, "", nil); !strings.Contains(st, `"state":"live"`) || !strings.Contains(st, `"seconds":15`) {
		t.Errorf("live: %s", st)
	}
	code, feed := do(t, "GET", app.URL+"/tv/feed?s="+tv.ID, "", nil)
	var f struct {
		Items []struct{ ID string } `json:"items"`
	}
	if code != 200 || json.Unmarshal([]byte(feed), &f) != nil || len(f.Items) != 2 || len(f.Items[0].ID) != 16 {
		t.Fatalf("the feed: %d %s", code, feed)
	}
	if code, pic := do(t, "GET", app.URL+"/tv/photo?s="+tv.ID+"&id="+f.Items[0].ID, "", nil); code != 200 || pic != "a picture" {
		t.Errorf("a picture: %d %q", code, pic)
	}
	for _, p := range []string{"/tv/photo?s=" + tv.ID + "&id=11111111-1111-4111-8111-111111111111", "/tv/feed?s=" + strings.Repeat("0", 64), "/tv/photo?s=x&id=y", "/tv/feed"} {
		if code, _ := do(t, "GET", app.URL+p, "", nil); code != 404 {
			t.Errorf("%s: %d (unknown, ended and malformed answer alike)", p, code)
		}
	}
}

func TestThePhoneSideOfCastingNeedsSignInAndLetsAPersonControlTheirTV(t *testing.T) {
	var st *store.Store
	var tv store.CastSession
	app, _ := setup(t, withCast(t, &st, &tv))
	if code, _ := postForm(t, app.URL+"/cast/claim", "", "code=abcdef&kind=new"); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/fragments/cast-form", "", nil); code != 401 {
		t.Errorf("form signed out: %d", code)
	}
	if code, body := do(t, "GET", app.URL+"/fragments/cast-form", "sess=other", nil); code != 200 || !strings.Contains(body, "The code on the TV") || !strings.Contains(body, "Everything new in the last 30 days") {
		t.Errorf("the form: %d %.300s", code, body)
	}
	if _, page := do(t, "GET", app.URL+"/", "sess=other", nil); !strings.Contains(page, "Cast to a TV") || !strings.Contains(page, `data-link="tv"`) || !strings.Contains(page, `data-link="tv/legacy"`) ||
		!strings.Contains(page, "Everything new in the last 30 days") || !strings.Contains(page, "Pause") || !strings.Contains(page, "Show something else") {
		t.Errorf("the card lists the TV with its remote")
	}
	if code, _ := postForm(t, app.URL+"/cast/claim", "sess=other", "code=zzzzzz&kind=new&seconds=15&fx=fade"); code != 409 {
		t.Errorf("a code no TV shows: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/cast/control", "sess=family", "tv="+tv.ID+"&do=stop"); code != 409 {
		t.Errorf("somebody else's TV: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/cast/control", "sess=other", "tv="+tv.ID+"&do=explode"); code != 409 {
		t.Errorf("a command that does not exist: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/cast/control", "sess=other", "tv="+tv.ID+"&do=pause"); code != 204 {
		t.Fatalf("pause: %d", code)
	}
	if _, s := do(t, "GET", app.URL+"/tv/state?s="+tv.ID, "", nil); !strings.Contains(s, `"paused":true`) {
		t.Errorf("the TV is told: %s", s)
	}
	if code, _ := postForm(t, app.URL+"/cast/control", "sess=other", "tv="+tv.ID+"&do=next"); code != 204 {
		t.Fatalf("next: %d", code)
	}
	if _, s := do(t, "GET", app.URL+"/tv/state?s="+tv.ID, "", nil); !strings.Contains(s, `"cmd":"next"`) || !strings.Contains(s, `"seq":1`) {
		t.Errorf("the command reaches the TV once: %s", s)
	}
	if code, _ := postForm(t, app.URL+"/cast/control", "sess=other", "tv="+tv.ID+"&do=stop"); code != 204 {
		t.Fatalf("stop: %d", code)
	}
	if _, s := do(t, "GET", app.URL+"/tv/state?s="+tv.ID, "", nil); !strings.Contains(s, `"state":"ended"`) {
		t.Errorf("stopped: %s", s)
	}
	if code, _ := do(t, "GET", app.URL+"/tv/feed?s="+tv.ID, "", nil); code != 404 {
		t.Errorf("an ended TV gets nothing: %d", code)
	}
}

func TestTheTVScriptsAreBuiltAndTheLegacyOneUsesNoNewSyntax(t *testing.T) {
	app, _ := setup(t)
	for _, p := range []string{"/static/tv-boot.js", "/static/tv.js", "/static/tv-legacy.js", "/static/tv.css"} {
		if code, _ := do(t, "GET", app.URL+p, "", nil); code != 200 {
			t.Errorf("%s: %d", p, code)
		}
	}
	for _, p := range []string{"/static/tv-boot.js", "/static/tv-legacy.js"} {
		_, js := do(t, "GET", app.URL+p, "", nil)
		for _, bad := range []string{"=>", "?.", "??", "async ", "await ", "class ", "`", "const ", "let "} {
			if strings.Contains(js, bad) {
				t.Errorf("%s contains %q, which a browser from 2016 cannot read", p, bad)
			}
		}
	}
}

// postNoHeader posts with no body and no cross-site header, as a TV's script does.
func postNoHeader(t *testing.T, url string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}
