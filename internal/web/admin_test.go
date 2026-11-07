package web_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/vault"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

var change = map[string]string{"X-Requested-With": "immich-handoff"}

// withHub gives the page a store, with one person who joined the configured "family" service and one service
// a person created on the page.
func withHub(t *testing.T, st **store.Store) func(*web.Server) {
	return func(srv *web.Server) {
		v, err := vault.New("0123456789abcdef0123")
		if err != nil {
			t.Fatal(err)
		}
		s, err := store.Open(t.TempDir()+"/h.db", v)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		*st = s
		ctx := context.Background()
		if _, err := s.AddLink(ctx, store.Link{Service: "family", Role: store.RoleSender, ImmichUserID: uuid.New(), Name: "Bob", Email: "bob@x.test", KeyID: uuid.New(), Key: "k-bob-secret"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddService(ctx, store.Service{Name: "club", Tag: "club-tag", InboxTag: "inbox/club", Refused: "dup", ExpiryDays: 30, OwnerID: uuid.New(), OwnerName: "Carol", OwnerEmail: "carol@x.test", KeyID: uuid.New(), Key: "k-family", Policy: store.PolicyApproval, Visibility: store.VisListed}); err != nil {
			t.Fatal(err)
		}
		h := &hub.Hub{Static: srv.S.Services, Store: s, Log: srv.Log,
			Connect: func(key string) (*immich.API, error) { return immich.New(srv.BaseURL, immich.Auth{APIKey: key}, nil) }}
		if err := h.Reload(context.Background()); err != nil {
			t.Fatal(err)
		}
		srv.Hub = h
		srv.S.Provider = h
	}
}

func TestOnlyAnImmichAdministratorSeesTheAdminPage(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withHub(t, &st))
	for _, c := range []string{"", "sess=bogus", "sess=alice", "sess=other"} {
		if code, _ := do(t, "GET", app.URL+"/admin", c, nil); code != 404 {
			t.Errorf("GET /admin as %q: %d, want 404 (the page must not show itself)", c, code)
		}
	}
	code, body := do(t, "GET", app.URL+"/admin", "sess=admin", nil)
	if code != 200 {
		t.Fatalf("admin: %d %.200s", code, body)
	}
	for _, want := range []string{"family", "From configuration", "Bob", "bob@x.test", "club", "Carol (carol@x.test)", "Check now"} {
		if !strings.Contains(body, want) {
			t.Errorf("the admin page lacks %q", want)
		}
	}
	for _, secret := range []string{"k-bob-secret", "k-family", "k-alice"} {
		if strings.Contains(body, secret) {
			t.Errorf("the admin page must never show a key (%q)", secret)
		}
	}
	// the link to it appears for admins only
	if _, body := do(t, "GET", app.URL+"/", "sess=admin", nil); !strings.Contains(body, `href="admin"`) {
		t.Error("the admin sees no link to the admin page")
	}
	if _, body := do(t, "GET", app.URL+"/", "sess=alice", nil); strings.Contains(body, `href="admin"`) {
		t.Error("a normal user must not be offered the admin page")
	}
}

func TestAdminChangesNeedAnAdministratorAndTheCSRFHeader(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withHub(t, &st))
	links, _ := st.Links(context.Background())
	url := app.URL + "/admin/members/remove?link=" + links[0].ID
	if code, _ := do(t, "POST", url, "sess=admin", nil); code != 400 {
		t.Errorf("without the CSRF header: %d", code)
	}
	for _, c := range []string{"", "sess=alice"} {
		for _, path := range []string{"/admin/members/remove", "/admin/services/remove", "/admin/check"} {
			if code, _ := do(t, "POST", app.URL+path, c, change); code != 401 && code != 404 {
				t.Errorf("POST %s as %q: %d, want it refused", path, c, code)
			}
		}
	}
	if code, _ := do(t, "POST", app.URL+"/admin/members/remove", "sess=alice", change); code != 404 {
		t.Errorf("a non-admin removing a member: %d, want 404", code)
	}
	if got, _ := st.Links(context.Background()); got[0].State != store.StateActive {
		t.Error("a refused request changed a member")
	}
}

func postForm(t *testing.T, url, cookie, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-Requested-With", "immich-handoff")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b := new(strings.Builder)
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return res.StatusCode, b.String()
}

func TestAdminRemovesAMemberAndTheKeyIsForgotten(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withHub(t, &st))
	links, _ := st.Links(context.Background())
	if code, body := postForm(t, app.URL+"/admin/members/remove", "sess=admin", "link="+links[0].ID); code != 204 {
		t.Fatalf("remove: %d %s", code, body)
	}
	got, _ := st.Links(context.Background())
	if got[0].State != store.StateBroken || got[0].Key != "" || !strings.Contains(i18n.For(i18n.English).Unpack(got[0].Note), "still valid in your Immich account") || !strings.Contains(i18n.For(i18n.English).Unpack(got[0].Note), "Admin") {
		t.Errorf("the membership after removal: state %q key %q note %q", got[0].State, got[0].Key, got[0].Note)
	}
	if _, body := do(t, "GET", app.URL+"/admin", "sess=admin", nil); !strings.Contains(body, "still valid in your Immich account") {
		t.Error("the admin page does not show what the person was told")
	}
	if code, _ := postForm(t, app.URL+"/admin/members/remove", "sess=admin", "link=no-such-link"); code != 409 {
		t.Errorf("an unknown member: %d, want 409", code)
	}
}

func TestAdminRemovesAServiceButNotOneFromTheConfiguration(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withHub(t, &st))
	if code, body := postForm(t, app.URL+"/admin/services/remove", "sess=admin", "service=family"); code != 409 || !strings.Contains(body, "configuration") {
		t.Errorf("a configured service: %d %q", code, body)
	}
	if code, body := postForm(t, app.URL+"/admin/services/remove", "sess=admin", "service=club"); code != 204 {
		t.Fatalf("remove club: %d %s", code, body)
	}
	if list, _ := st.Services(context.Background()); len(list) != 0 {
		t.Errorf("the service is still stored: %d", len(list))
	}
	if _, body := do(t, "GET", app.URL+"/admin", "sess=admin", nil); strings.Contains(body, "Carol (carol@x.test)") {
		t.Error("the removed service is still listed")
	}
	if code, _ := postForm(t, app.URL+"/admin/services/remove", "sess=admin", "service=club"); code != 409 {
		t.Errorf("removing it twice: %d, want 409", code)
	}
}

func TestAdminConnectionCheckRuns(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withHub(t, &st))
	code, body := postForm(t, app.URL+"/admin/check", "sess=admin", "")
	if code != 200 || !strings.Contains(body, "Settings") || !strings.Contains(body, "Immich") {
		t.Errorf("check: %d %.300s", code, body)
	}
}
