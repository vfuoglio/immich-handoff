package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/bridge"
	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/vault"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

// farServer is the Immich of the person who is invited: it answers the two questions a check asks.
func farServer(t *testing.T, perms string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server/version":
			w.Write([]byte(`{"major":3,"minor":2,"patch":0}`))
		case "/api/api-keys/me":
			if r.Header.Get("x-api-key") != "FAR-KEY" {
				http.Error(w, "no", 401)
				return
			}
			w.Write([]byte(`{"permissions":` + perms + `}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

const fourPerms = `["user.read","tag.create","tag.asset","asset.upload"]`

// withBridges gives the page a store with a service "holiday" owned by the family user, an open invitation to it, and a hub
// with bridges switched on.
func withBridges(t *testing.T, st **store.Store, token string, p bridge.Policy) func(*web.Server) {
	return func(srv *web.Server) {
		v, _ := vault.New("0123456789abcdef0123")
		s, err := store.Open(t.TempDir()+"/h.db", v)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		*st = s
		ctx := context.Background()
		if _, err := s.AddService(ctx, store.Service{Name: "holiday", Tag: "hol", InboxTag: "inbox", Refused: "r", ExpiryDays: 30, OwnerID: uuid.MustParse(familyID),
			OwnerName: "Family", OwnerEmail: "f@x", KeyID: uuid.New(), Key: "k-family", Policy: store.PolicyApproval}); err != nil {
			t.Fatal(err)
		}
		if token != "" {
			if err := s.SetBridgeInvite(ctx, "holiday", token); err != nil {
				t.Fatal(err)
			}
		}
		h := &hub.Hub{Static: srv.S.Services, Store: s, Log: srv.Log, Options: hub.Options{InboxTag: "inbox"},
			Bridge:  bridge.Settings{Enabled: true, Policy: p},
			Connect: func(key string) (*immich.API, error) { return immich.New(srv.BaseURL, immich.Auth{APIKey: key}, nil) }}
		if err := h.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		srv.Hub = h
		srv.S.Provider = h
	}
}

func connectForm(address, key, name string) string {
	return url.Values{"address": {address}, "key": {key}, "name": {name}}.Encode()
}

func TestTheInvitationPageNeedsNoLoginAndSaysOnlyWhatItMust(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("ab", 16)
	app, _ := setup(t, withBridges(t, &st, token, bridge.Policy{AllowPrivate: true}))
	code, body := do(t, "GET", app.URL+"/bridge/"+token, "", nil)
	if code != 200 || !strings.Contains(body, "holiday") || !strings.Contains(body, "Family") {
		t.Fatalf("%d %.300s", code, body)
	}
	for _, p := range bridge.RequiredPermissions {
		if !strings.Contains(body, "<code>"+p+"</code>") {
			t.Errorf("the page must list %s", p)
		}
	}
	for _, want := range []string{`<base href="../"`, `action="bridge/` + token + `"`, "do not work across servers", `type="password"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, "Photos you tag in Immich") || strings.Contains(body, `class="tabs"`) {
		t.Error("a guest sees none of the account parts of the page")
	}
	if strings.Contains(body, "FAR-KEY") || strings.Contains(body, "k-family") {
		t.Error("no key on the page")
	}
}

func TestEveryClosedInvitationAnswersTheSame(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("cd", 16)
	app, _ := setup(t, withBridges(t, &st, token, bridge.Policy{AllowPrivate: true}))
	_, unknown := do(t, "GET", app.URL+"/bridge/"+strings.Repeat("ef", 16), "", nil)
	_, short := do(t, "GET", app.URL+"/bridge/x", "", nil)
	st.DeleteBridgeInvite(context.Background(), token)
	code, used := do(t, "GET", app.URL+"/bridge/"+token, "", nil)
	if code != 404 || unknown != short || short != used || !strings.Contains(used, "This invitation is not open") {
		t.Errorf("used, unknown and malformed links must look alike (%d):\n%.300s\n%.300s", code, used, unknown)
	}
	if code, _ := postForm(t, app.URL+"/bridge/"+token, "", connectForm("https://x.example.org", "k", "n")); code != 404 {
		t.Errorf("posting to a closed link: %d", code)
	}
}

func TestSomeoneOnAnotherServerConnectsAndTheLinkIsUsedUp(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("ab", 16)
	far := farServer(t, fourPerms)
	app, _ := setup(t, withBridges(t, &st, token, bridge.Policy{AllowPrivate: true}))

	code, body := postForm(t, app.URL+"/bridge/"+token, "", connectForm(far.URL, "FAR-KEY", "Aunt Lia"))
	if code != 200 || !strings.Contains(body, "Connected") {
		t.Fatalf("%d %.400s", code, body)
	}
	b, err := st.Bridge(context.Background(), "holiday")
	if err != nil || b.DisplayName != "Aunt Lia" || b.Key != "FAR-KEY" || b.Version != "v3.2.0" {
		t.Fatalf("%+v %v", b, err)
	}
	if strings.Contains(body, "FAR-KEY") {
		t.Error("the key must not be echoed")
	}
	if code, _ := do(t, "GET", app.URL+"/bridge/"+token, "", nil); code != 404 {
		t.Errorf("the link works once: %d", code)
	}
}

func TestAMistakeIsExplainedAndCanBeCorrectedOnTheSamePage(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("ab", 16)
	broad := farServer(t, `["user.read","tag.create","tag.asset","asset.upload","asset.read"]`)
	far := farServer(t, fourPerms)
	app, _ := setup(t, withBridges(t, &st, token, bridge.Policy{AllowPrivate: true}))

	for name, c := range map[string]struct{ address, key, want string }{
		"a key that can read":  {broad.URL, "FAR-KEY", "can read your library"},
		"a wrong key":          {far.URL, "WRONG", "does not accept this key"},
		"not an address":       {"ftp://x", "FAR-KEY", "not an address the app may connect to"},
		"a server that is off": {"http://127.0.0.1:1", "FAR-KEY", "cannot reach that server"},
	} {
		code, body := postForm(t, app.URL+"/bridge/"+token, "", connectForm(c.address, c.key, "Lia"))
		if code != 409 || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d, want %q in %.600s", name, code, c.want, body)
		}
		if !strings.Contains(body, `value="Lia"`) {
			t.Errorf("%s: what they typed must still be there", name)
		}
		if strings.Contains(body, `name="key" type="password" required autocomplete="off" value`) {
			t.Errorf("%s: the key must not be put back in the form", name)
		}
	}
	if _, err := st.Bridge(context.Background(), "holiday"); err == nil {
		t.Fatal("a failed attempt stored a bridge")
	}
	if code, _ := postForm(t, app.URL+"/bridge/"+token, "", connectForm(far.URL, "FAR-KEY", "Lia")); code != 200 {
		t.Fatalf("the correction: %d", code)
	}
}

func TestAPrivateAddressIsRefusedWithoutTheOptInAndNothingIsSaidAboutWhatIsThere(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("ab", 16)
	far := farServer(t, fourPerms) // on 127.0.0.1
	app, _ := setup(t, withBridges(t, &st, token, bridge.Policy{}))
	code, body := postForm(t, app.URL+"/bridge/"+token, "", connectForm(far.URL, "FAR-KEY", "Lia"))
	if code != 409 || !strings.Contains(body, "not an address the app may connect to") {
		t.Fatalf("%d %.400s", code, body)
	}
	// the same answer whether or not something is listening there
	_, other := postForm(t, app.URL+"/bridge/"+token, "", connectForm("http://127.0.0.1:1", "FAR-KEY", "Lia"))
	if !strings.Contains(other, "not an address the app may connect to") {
		t.Errorf("a closed private port answered differently: %.400s", other)
	}
	if strings.Contains(body, "inside a private network") {
		t.Error("the page must not describe the app's own network")
	}
}

func TestTooManyAttemptsAreTurnedAway(t *testing.T) {
	var st *store.Store
	token := strings.Repeat("ab", 16)
	app, _ := setup(t, withBridges(t, &st, token, bridge.Policy{}))
	var last int
	for i := 0; i < 12; i++ {
		last, _ = postForm(t, app.URL+"/bridge/"+token, "", connectForm("ftp://x", "k", "n"))
	}
	if last != 429 {
		t.Fatalf("the twelfth attempt: %d, want 429", last)
	}
}

func TestOnlyTheOwnerMakesTheLinkAndItAppearsOnTheirPage(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withBridges(t, &st, "", bridge.Policy{AllowPrivate: true}))
	if code, _ := postForm(t, app.URL+"/services/bridge-invite", "", "service=holiday"); code != 401 {
		t.Errorf("not signed in: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/services/bridge-invite", "sess=alice", "service=holiday"); code != 409 {
		t.Errorf("someone who does not own it: %d", code)
	}
	if code, body := postForm(t, app.URL+"/services/bridge-invite", "sess=family", "service=holiday"); code != 204 {
		t.Fatalf("the owner: %d %s", code, body)
	}
	i, err := st.BridgeInviteOf(context.Background(), "holiday")
	if err != nil {
		t.Fatal(err)
	}
	_, page := do(t, "GET", app.URL+"/", "sess=family", nil)
	if !strings.Contains(page, `data-link="bridge/`+i.Token+`"`) {
		t.Error("the owner's page must show the link")
	}
}

func TestTheOwnerSeesTheStateOfTheBridgeAndCanRemoveIt(t *testing.T) {
	var st *store.Store
	far := farServer(t, fourPerms)
	app, _ := setup(t, withBridges(t, &st, "", bridge.Policy{AllowPrivate: true}))
	ctx := context.Background()
	if err := st.SetBridge(ctx, store.Bridge{Service: "holiday", Address: far.URL, Key: "FAR-KEY", DisplayName: "Aunt Lia", Version: "v3.2.0"}); err != nil {
		t.Fatal(err)
	}
	// a state that needs the owner shows on the page and in "Needs you", with the way to remove it
	st.SetBridgeState(ctx, "holiday", store.BridgeRevoked, "The other server does not accept the key any more.", "", 0, time.Time{})
	hub0 := app.URL + "/"
	_, page := do(t, "GET", hub0, "sess=family", nil)
	for _, want := range []string{"Aunt Lia", "Key revoked", "v3.2.0", "does not accept the key any more", "Photos wait, still tagged", "services/bridge/remove"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if code, _ := postForm(t, app.URL+"/services/bridge/remove", "sess=alice", "service=holiday"); code != 409 {
		t.Errorf("someone who does not own it: %d", code)
	}
	if code, body := postForm(t, app.URL+"/services/bridge/remove", "sess=family", "service=holiday"); code != 204 {
		t.Fatalf("%d %s", code, body)
	}
	if _, err := st.Bridge(ctx, "holiday"); err == nil {
		t.Fatal("not removed")
	}
	if _, page := do(t, "GET", hub0, "sess=family", nil); strings.Contains(page, "Key revoked") {
		t.Error("the page still shows it")
	}
}
