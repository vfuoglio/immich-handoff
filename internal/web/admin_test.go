package web_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/backup"
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

// withBackups gives the page a backup manager over the store withHub made, and says what it told.
func withDownload(st **store.Store, download bool, gap time.Duration, told *[]string, mu *sync.Mutex, dir string) func(*web.Server) {
	return func(srv *web.Server) {
		srv.BackupDownload, srv.DownloadGap = download, gap
		srv.Backups = &backup.Manager{Dir: dir, Source: *st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Keep: 7, Every: 24 * time.Hour,
			Tell: func(s string) { mu.Lock(); *told = append(*told, s); mu.Unlock() },
			Event: func(kind, text string) {
				(*st).AddEvent(context.Background(), store.Event{Service: "club", Kind: kind, Text: text})
			}}
	}
}

func TestTheBackupDownloadIsOffUnlessSwitchedOn(t *testing.T) {
	var st *store.Store
	var told []string
	var mu sync.Mutex
	app, _ := setup(t, withHub(t, &st), func(srv *web.Server) { withDownload(&st, false, 0, &told, &mu, t.TempDir())(srv) })
	if code, _ := do(t, "POST", app.URL+"/admin/backup/download", "sess=admin", change); code != 404 {
		t.Errorf("download while switched off: %d, want 404", code)
	}
	code, body := do(t, "GET", app.URL+"/admin", "sess=admin", nil)
	if code != 200 || strings.Contains(body, "data-backup-download") || !strings.Contains(body, "Downloading a backup from this page is off") {
		t.Errorf("the page must say the download is off and show no button: %d", code)
	}
	if !strings.Contains(body, "Automatic backups: every 24h, keeping the newest 7") {
		t.Error("the page does not show the automatic backup schedule")
	}
	if len(told) != 0 {
		t.Errorf("a refused download told someone: %v", told)
	}
}

func TestTheBackupDownloadIsLimitedLoggedAndLeavesNothingBehind(t *testing.T) {
	var st *store.Store
	var told []string
	var mu sync.Mutex
	dir := t.TempDir()
	app, _ := setup(t, withHub(t, &st), func(srv *web.Server) { withDownload(&st, true, 300*time.Millisecond, &told, &mu, dir)(srv) })
	url := app.URL + "/admin/backup/download"

	if code, _ := do(t, "POST", url, "sess=admin", nil); code != 400 {
		t.Errorf("without the CSRF header: %d, want 400", code)
	}
	if code, _ := do(t, "GET", url, "sess=admin", change); code != 405 {
		t.Errorf("as a plain link (GET): %d, want 405", code)
	}
	for _, c := range []string{"", "sess=alice"} {
		if code, _ := do(t, "POST", url, c, change); code != 401 && code != 404 {
			t.Errorf("as %q: %d, want it refused", c, code)
		}
	}

	req, _ := http.NewRequest("POST", url, nil)
	req.Header.Set("Cookie", "sess=admin")
	req.Header.Set("X-Requested-With", "immich-handoff")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	file, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.HasPrefix(string(file), "SQLite format 3") {
		t.Fatalf("download: %d, %d bytes", res.StatusCode, len(file))
	}
	if cd := res.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment; filename=\"handoff-") {
		t.Errorf("Content-Disposition %q", cd)
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q, want no-store", res.Header.Get("Cache-Control"))
	}
	mu.Lock()
	if len(told) != 1 || !strings.Contains(told[0], "downloaded by") {
		t.Errorf("whoever runs the app was not told: %v", told)
	}
	mu.Unlock()
	if _, body := do(t, "GET", app.URL+"/admin", "sess=admin", nil); !strings.Contains(body, "A backup was downloaded by") {
		t.Error("the download is not in the recent events")
	}

	// the next one is refused for a while, and is not counted as a failure
	if code, _ := do(t, "POST", url, "sess=admin", change); code != 429 {
		t.Errorf("a second download at once: %d, want 429", code)
	}
	time.Sleep(350 * time.Millisecond)
	if code, _ := do(t, "POST", url, "sess=admin", change); code != 200 {
		t.Errorf("a download after the gap: %d, want 200", code)
	}
	// the app removes its copy when the handler returns, a moment after the last byte reached us
	var entries []os.DirEntry
	for i := 0; i < 100; i++ {
		if entries, _ = os.ReadDir(dir); len(entries) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, e := range entries {
		t.Errorf("a download left %s in the backup folder", filepath.Join(dir, e.Name()))
	}
}

func TestTheAdminPageOffersTheDownloadWhenItIsOn(t *testing.T) {
	var st *store.Store
	var told []string
	var mu sync.Mutex
	app, _ := setup(t, withHub(t, &st), func(srv *web.Server) { withDownload(&st, true, 0, &told, &mu, t.TempDir())(srv) })
	_, body := do(t, "GET", app.URL+"/admin", "sess=admin", nil)
	if !strings.Contains(body, `data-backup-download="admin/backup/download"`) || strings.Contains(body, "Downloading a backup from this page is off") {
		t.Error("the page does not offer the download")
	}
	if !strings.Contains(body, "No backup has been made yet.") {
		t.Error("the page does not say that no backup exists yet")
	}
}
