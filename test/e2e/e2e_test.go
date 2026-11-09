//go:build e2e

// End-to-end tests against a real, disposable Immich (see test/docker-compose.yml and test/run.sh).
// They build and run the actual binary, so the generated client, the workflow provisioning and the
// webhook path are all exercised for real.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
)

const (
	secret = "e2e-secret-0123456789"
)

var appPort = envOr("E2E_APP_PORT", "18080")

var baseURL = envOr("IMMICH_E2E_URL", "http://localhost:2284")

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// --- tiny Immich REST helper (independent of the app's generated client on purpose) ---

// key plays the user (full access, like their own session); appKey is the restricted key given to the app.
type user struct{ id, token, key, appKey string }

func call(t *testing.T, method, path string, auth map[string]string, body any, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, baseURL+"/api"+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range auth {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: bad JSON %q", method, path, b)
		}
	}
	if res.StatusCode >= 300 && out == nil {
		t.Logf("%s %s -> %d %s", method, path, res.StatusCode, b)
	}
	return res.StatusCode
}

// The exact API key permissions documented in the README. The suite runs with these (not "all"),
// so a permission the app really needs but the README omits fails the tests.
var (
	sourcePermissions = []string{"user.read", "tag.read", "tag.create", "tag.asset", "asset.read", "asset.download", "asset.delete", "stack.read", "workflow.read", "workflow.create", "workflow.update", "workflow.logs"}
	destPermissions   = []string{"user.read", "tag.read", "tag.create", "tag.asset", "asset.read", "asset.upload", "stack.create"}
)

func keyAuth(u user) map[string]string { return map[string]string{"x-api-key": u.key} }

func mustOK(t *testing.T, code int, what string) {
	t.Helper()
	if code >= 300 {
		t.Fatalf("%s failed with HTTP %d", what, code)
	}
}

func bootstrap(t *testing.T) (alice, bob, family, grandma user) {
	t.Helper()
	// wait for the API
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if res, err := http.Get(baseURL + "/api/server/ping"); err == nil && res.StatusCode == 200 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	pw := "Passw0rd!x"
	call(t, "POST", "/auth/admin-sign-up", nil, map[string]string{"email": "admin@e2e.test", "name": "admin", "password": pw}, nil) // 400 if already done
	var login struct {
		AccessToken string `json:"accessToken"`
	}
	mustOK(t, call(t, "POST", "/auth/login", nil, map[string]string{"email": "admin@e2e.test", "password": pw}, &login), "admin login")
	admin := map[string]string{"Authorization": "Bearer " + login.AccessToken}

	mk := func(name string, perms []string) user {
		email := name + "@e2e.test"
		call(t, "POST", "/admin/users", admin, map[string]any{"email": email, "name": name, "password": pw, "shouldChangePassword": false}, nil) // 400 if exists
		var l struct {
			AccessToken string `json:"accessToken"`
			UserID      string `json:"userId"`
		}
		mustOK(t, call(t, "POST", "/auth/login", nil, map[string]string{"email": email, "password": pw}, &l), "login "+name)
		newKey := func(label string, perms []string) string {
			var k struct {
				Secret string `json:"secret"`
			}
			mustOK(t, call(t, "POST", "/api-keys", map[string]string{"Authorization": "Bearer " + l.AccessToken},
				map[string]any{"name": label + fmt.Sprint(time.Now().UnixNano()), "permissions": perms}, &k), "api key "+name)
			return k.Secret
		}
		u := user{id: l.UserID, token: l.AccessToken, key: newKey("e2e-user-", []string{"all"}), appKey: newKey("e2e-app-", perms)}
		call(t, "PUT", "/users/me/preferences", keyAuth(u), map[string]any{"tags": map[string]any{"enabled": true}}, nil)
		return u
	}
	return mk("alice", sourcePermissions), mk("bob", sourcePermissions), mk("family", destPermissions), mk("grandma", destPermissions)
}

// jpeg returns distinct, valid JPEG bytes per seed.
func jpegBytes(seed int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for x := 0; x < 64; x++ {
		for y := 0; y < 64; y++ {
			img.Set(x, y, color.RGBA{uint8(seed * 37), uint8(x*4) + uint8(seed>>8)*61, uint8(y * 4), 255}) // seed>>8: seeds 256 apart must not make the same file
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

func upload(t *testing.T, u user, name string, data []byte) (id, status string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	now := time.Now().UTC().Format(time.RFC3339)
	mw.WriteField("fileCreatedAt", now)
	mw.WriteField("fileModifiedAt", now)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="assetData"; filename=%q`, name))
	h.Set("Content-Type", "image/jpeg")
	p, _ := mw.CreatePart(h)
	p.Write(data)
	mw.Close()
	req, _ := http.NewRequest("POST", baseURL+"/api/assets", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("x-api-key", u.key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct{ ID, Status string }
	b, _ := io.ReadAll(res.Body)
	json.Unmarshal(b, &out)
	if res.StatusCode >= 300 || out.ID == "" {
		t.Fatalf("upload %s: HTTP %d %s", name, res.StatusCode, b)
	}
	waitProcessed(t, u, out.ID)
	return out.ID, out.Status
}

// waitProcessed returns once Immich has finished reading the uploaded photo's metadata. A tag added
// before that point can be overwritten by Immich's own metadata job, so a user (who taps the photo
// seconds after it appeared) never meets that window but a script that tags instantly does.
func waitProcessed(t *testing.T, u user, id string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		var a struct {
			HasMetadata bool           `json:"hasMetadata"`
			ExifInfo    map[string]any `json:"exifInfo"`
		}
		if call(t, "GET", "/assets/"+id, keyAuth(u), nil, &a) == 200 && a.HasMetadata && a.ExifInfo != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Immich did not finish processing %s", id)
}

type asset struct {
	ID        string `json:"id"`
	IsTrashed bool   `json:"isTrashed"`
	Tags      []struct {
		Name string `json:"name"`
	} `json:"tags"`
}

func getAsset(t *testing.T, u user, id string) (asset, int) {
	var a asset
	code := call(t, "GET", "/assets/"+id, keyAuth(u), nil, &a)
	return a, code
}

func tagAsset(t *testing.T, u user, tag string, ids ...string) {
	t.Helper()
	var tags []struct{ ID string }
	mustOK(t, call(t, "PUT", "/tags", keyAuth(u), map[string]any{"tags": []string{tag}}, &tags), "upsert tag")
	apply := func(ids []string) {
		var results []struct {
			ID      string `json:"id"`
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		mustOK(t, call(t, "PUT", "/tags/"+tags[0].ID+"/assets", keyAuth(u), map[string]any{"ids": ids}, &results), "tag asset")
		for _, r := range results {
			if !r.Success && r.Error != "duplicate" {
				t.Fatalf("tagging %s with %q failed: %q", r.ID, tag, r.Error)
			}
		}
	}
	apply(ids)
	keepTagged(t, u, ids, apply)
}

// keepTagged makes sure a tag that was just applied stuck, and applies it again when it did not.
//
// Immich itself occasionally drops a tag applied right after an upload (about 1 in 50, seen with no
// handoff app running at all; see docs/immich-api-notes.md). A person would notice and tag again, so the
// test helpers do the same, and the tests then measure this app and not that behaviour of Immich.
//
// "Lost" means the photo has no tags at all and is not in the trash: a photo the app has already handled
// carries a marker tag or is trashed, and must not have the tag put back on it. Both tagging helpers use
// this, so a different way of tagging cannot reintroduce the flake.
func keepTagged(t *testing.T, u user, ids []string, apply func(ids []string)) {
	t.Helper()
	for attempt := 0; attempt < 6; attempt++ {
		time.Sleep(700 * time.Millisecond)
		var missing []string
		for _, id := range ids {
			x, _ := getAsset(t, u, id)
			if len(x.Tags) == 0 && !x.IsTrashed {
				missing = append(missing, id)
			}
		}
		if len(missing) == 0 {
			return
		}
		t.Logf("tag was dropped by Immich on %d photo(s); applying it again (attempt %d)", len(missing), attempt+1)
		apply(missing)
	}
}

// tagLikeThePhone tags the way the Immich mobile app does: look the tag up (creating it), then add it to
// the photos with the bulk call (PUT /tags/assets), which is a different endpoint from tagAsset's.
func tagLikeThePhone(t *testing.T, u user, tag string, ids ...string) {
	t.Helper()
	var tags []struct{ ID string }
	mustOK(t, call(t, "PUT", "/tags", keyAuth(u), map[string]any{"tags": []string{tag}}, &tags), "upsert tag")
	apply := func(ids []string) {
		var out struct {
			Count int `json:"count"`
		}
		mustOK(t, call(t, "PUT", "/tags/assets", keyAuth(u), map[string]any{"assetIds": ids, "tagIds": []string{tags[0].ID}}, &out), "bulk tag")
		if out.Count != len(ids) {
			t.Fatalf("bulk tagging reported %d of %d", out.Count, len(ids))
		}
	}
	apply(ids)
	keepTagged(t, u, ids, apply)
}

// search returns the ids of assets with a tag, optionally including trashed ones.
func searchTag(t *testing.T, u user, tag string, withTrashed bool) []string {
	t.Helper()
	var tags []struct{ ID, Name, Value string }
	call(t, "GET", "/tags", keyAuth(u), nil, &tags)
	var tid string
	for _, x := range tags {
		if x.Name == tag || x.Value == tag {
			tid = x.ID
		}
	}
	if tid == "" {
		return nil
	}
	var res struct {
		Assets struct{ Items []struct{ ID string } } `json:"assets"`
	}
	call(t, "POST", "/search/metadata", keyAuth(u), map[string]any{"tagIds": []string{tid}, "withDeleted": withTrashed}, &res)
	var ids []string
	for _, i := range res.Assets.Items {
		ids = append(ids, i.ID)
	}
	return ids
}

func eventually(t *testing.T, what string, within time.Duration, f func() bool) {
	t.Helper()
	end := time.Now().Add(within)
	for time.Now().Before(end) {
		if f() {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// --- the app under test ---

type app struct {
	cmd *exec.Cmd
	log *bytes.Buffer
}

func startApp(t *testing.T, bin string, alice, bob, family user) *app {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"IMMICH_BASE_URL="+baseURL,
		"IMMICH_SOURCE_API_KEYS="+alice.appKey+","+bob.appKey,
		"IMMICH_DEST_API_KEY="+family.appKey,
		"WEBHOOK_SECRET="+secret,
		// Immich runs in a container; the app runs on the host.
		"PUBLIC_WEBHOOK_URL=http://host.docker.internal:"+appPort+"/webhook/immich",
		"LISTEN_ADDR=:"+appPort,
		"LOG_LEVEL=debug",
		"HANDOFF_MODE=move", "SWEEP_INTERVAL_HOURS=1", // so anything imported after startup arrived through the webhook
		"CLEANUP_INTERVAL_MINUTES=0.05", // the inbox tidy-up runs every few seconds
	)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	a := &app{cmd: cmd, log: &buf}
	t.Cleanup(a.stop)
	eventually(t, "app healthy", 30*time.Second, func() bool {
		res, err := http.Get("http://localhost:" + appPort + "/healthz")
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == 200
	})
	return a
}

func (a *app) stop() {
	if a.cmd.Process != nil {
		a.cmd.Process.Signal(os.Interrupt)
		a.cmd.Wait()
	}
}

func buildApp(t *testing.T) string {
	bin := filepath.Join(t.TempDir(), "immich-handoff")
	out, err := exec.Command("go", "build", "-o", bin, "../../cmd/immich-handoff").CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func TestEndToEnd(t *testing.T) {
	alice, bob, family, _ := bootstrap(t)
	bin := buildApp(t)
	a := startApp(t, bin, alice, bob, family)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("app log:\n%s", a.log.String())
		}
		for _, l := range strings.Split(a.log.String(), "\n") {
			if strings.Contains(l, "event hint") || strings.Contains(l, "became visible") || strings.Contains(l, "still not visible") {
				t.Logf("NOTE tag visibility lag: %s", l)
			}
		}
	})

	t.Run("startup creates the share tag and workflow for each source", func(t *testing.T) {
		// Immich is "healthy" before its workflow plugins load, so provisioning may need a few retries.
		for _, u := range []user{alice, bob} {
			eventually(t, "workflow for "+u.id, 4*time.Minute, func() bool {
				var wfs []struct {
					Name    string `json:"name"`
					Enabled bool   `json:"enabled"`
					Trigger string `json:"trigger"`
				}
				call(t, "GET", "/workflows", keyAuth(u), nil, &wfs)
				for _, w := range wfs {
					if w.Name == "immich-handoff" && w.Enabled && w.Trigger == "AssetTagged" {
						return true
					}
				}
				return false
			})
		}
		if !strings.Contains(a.log.String(), "workflow ready") {
			t.Errorf("no 'workflow ready' in app log")
		}
	})

	t.Run("the dashboard opens with a real Immich login cookie", func(t *testing.T) {
		cookieFor := func(email string) string {
			body, _ := json.Marshal(map[string]string{"email": email, "password": "Passw0rd!x"})
			res, err := http.Post(baseURL+"/api/auth/login", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			var parts []string
			for _, c := range res.Cookies() {
				parts = append(parts, c.Name+"="+c.Value)
			}
			return strings.Join(parts, "; ")
		}
		get := func(cookie string) (int, string) {
			req, _ := http.NewRequest("GET", "http://localhost:"+appPort+"/", nil)
			req.Header.Set("Cookie", cookie)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			b, _ := io.ReadAll(res.Body)
			return res.StatusCode, string(b)
		}
		if code, body := get(cookieFor("alice@e2e.test")); code != 200 || !strings.Contains(body, "You send") {
			t.Errorf("sender dashboard: %d %.200s", code, body)
		}
		if code, body := get(cookieFor("family@e2e.test")); code != 200 || !strings.Contains(body, "You receive") {
			t.Errorf("receiver dashboard: %d %.200s", code, body)
		}
		if code, body := get(cookieFor("admin@e2e.test")); code != 200 || !strings.Contains(body, "do not take part in any service") || strings.Contains(body, "Last run") {
			t.Errorf("an account outside every service sees an empty page without activity: %d %.200s", code, body)
		}
	})

	imgA := jpegBytes(1)
	var aliceOrig string

	t.Run("a person with Tags off is offered the switch, and it works through a real Immich session", func(t *testing.T) {
		cookie := loginCookie(t, "admin@e2e.test") // the setup switches Tags on for the other users, not for the admin
		page := func() string {
			req, _ := http.NewRequest("GET", "http://localhost:"+appPort+"/", nil)
			req.Header.Set("Cookie", cookie)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			b, _ := io.ReadAll(res.Body)
			return string(b)
		}
		if before := page(); !strings.Contains(before, "Tags are switched off") || !strings.Contains(before, "inert") {
			t.Fatal("a person with Tags off must be told, offered the switch, and have the rest of the page switched off")
		}
		if res := post(t, appPort, "/preferences/tags", cookie, nil); res.StatusCode != 200 {
			t.Fatalf("switching Tags on: status %d", res.StatusCode)
		}
		if after := page(); strings.Contains(after, "Tags are switched off") || strings.Contains(after, "inert") {
			t.Error("the notice and the lock must go once Tags are on")
		}
		var prefs struct {
			Tags struct{ Enabled, SidebarWeb bool } `json:"tags"`
		}
		req, _ := http.NewRequest("GET", baseURL+"/api/users/me/preferences", nil)
		req.Header.Set("Cookie", cookie)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		json.NewDecoder(res.Body).Decode(&prefs)
		if !prefs.Tags.Enabled || !prefs.Tags.SidebarWeb {
			t.Errorf("Immich must now have Tags on and in the sidebar, has %+v", prefs.Tags)
		}
	})

	t.Run("the built-in health check passes against the running app and fails when it is not there", func(t *testing.T) {
		run := func(addr string) error {
			cmd := exec.Command(bin, "--healthcheck")
			cmd.Env = append(os.Environ(), "LISTEN_ADDR="+addr)
			return cmd.Run()
		}
		if err := run(":" + appPort); err != nil {
			t.Errorf("--healthcheck against the running app: %v", err)
		}
		if err := run(":" + fmt.Sprint(atoi(appPort)+5000)); err == nil {
			t.Error("--healthcheck must fail when nothing listens")
		}
	})

	t.Run("tagging share imports via webhook, tags inbox, trashes the original", func(t *testing.T) {
		aliceOrig, _ = upload(t, alice, "alice-1.jpg", imgA)
		tagAsset(t, alice, "share", aliceOrig)
		eventually(t, "photo to appear in family with inbox tag", 90*time.Second, func() bool {
			return len(searchTag(t, family, "inbox", false)) == 1
		})
		eventually(t, "original to be trashed", 30*time.Second, func() bool {
			x, code := getAsset(t, alice, aliceOrig)
			return code == 200 && x.IsTrashed
		})
	})

	t.Run("doctor is quiet on a healthy setup and names what is wrong otherwise", func(t *testing.T) {
		doctor := func(extra ...string) (string, int) {
			cmd := exec.Command(bin, "doctor")
			cmd.Env = append(os.Environ(),
				"IMMICH_BASE_URL="+baseURL,
				"IMMICH_SOURCE_API_KEYS="+alice.appKey+","+bob.appKey,
				"IMMICH_DEST_API_KEY="+family.appKey,
				"WEBHOOK_SECRET="+secret,
				"PUBLIC_WEBHOOK_URL=http://host.docker.internal:"+appPort+"/webhook/immich",
				"HANDOFF_MODE=move")
			cmd.Env = append(cmd.Env, extra...)
			out, err := cmd.CombinedOutput()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			return string(out), code
		}
		// Immich ran alice's workflow when she tagged the photo above, and the app answered it.
		out, code := doctor()
		if code != 0 || !strings.Contains(out, "Immich's last") || strings.Contains(out, "FAIL") {
			t.Errorf("healthy setup: exit %d\n%s", code, out)
		}
		if out, code := doctor("WEBHOOK_SECRET=" + secret + "-changed"); code != 1 || !strings.Contains(out, "does not match the current settings") {
			t.Errorf("a changed secret: exit %d\n%s", code, out)
		}
		if out, code := doctor("PUBLIC_WEBHOOK_URL=http://localhost:" + appPort + "/webhook/immich"); code != 1 || !strings.Contains(out, "Immich itself") {
			t.Errorf("a loopback address: exit %d\n%s", code, out)
		}
		if out, code := doctor("IMMICH_BASE_URL=http://127.0.0.1:1"); code != 1 || !strings.Contains(out, "cannot reach Immich") {
			t.Errorf("Immich not reachable: exit %d\n%s", code, out)
		}
		// it only reads: the real workflow is still the one the app made
		if out, code := doctor(); code != 0 {
			t.Errorf("after the failing checks the workflow must be untouched: exit %d\n%s", code, out)
		}
	})

	t.Run("the trashed original is still reported as already uploaded (phone backup will skip it)", func(t *testing.T) {
		sum := sha1.Sum(imgA)
		var out struct {
			Results []struct {
				Action, Reason string
				IsTrashed      bool
			} `json:"results"`
		}
		mustOK(t, call(t, "POST", "/assets/bulk-upload-check", keyAuth(alice), map[string]any{
			"assets": []map[string]string{{"id": "x", "checksum": hex.EncodeToString(sum[:])}}}, &out), "bulk-upload-check")
		if len(out.Results) != 1 || out.Results[0].Action != "reject" || out.Results[0].Reason != "duplicate" || !out.Results[0].IsTrashed {
			t.Errorf("upload check = %+v", out.Results)
		}
	})

	t.Run("search pagination returns every tagged asset", func(t *testing.T) {
		var ids []string
		for i := 10; i < 15; i++ {
			id, st := upload(t, alice, fmt.Sprintf("page-%d.jpg", i), jpegBytes(i))
			t.Logf("uploaded page-%d.jpg -> %s (%s)", i, id, st)
			ids = append(ids, id)
		}
		tagAsset(t, alice, "pagination-check", ids...)
		var tags []struct{ ID, Name string }
		call(t, "GET", "/tags", keyAuth(alice), nil, &tags)
		var tid string
		for _, x := range tags {
			if x.Name == "pagination-check" {
				tid = x.ID
			}
		}
		// Immich itself occasionally drops a tag applied right after an upload (seen without this app
		// running at all), so, like a user would, re-apply the tag to anything that lost it.
		eventually(t, "all five tagged assets visible in search", time.Minute, func() bool {
			seen := map[string]bool{}
			for _, id := range searchTag(t, alice, "pagination-check", false) {
				seen[id] = true
			}
			var missing []string
			for _, id := range ids {
				if !seen[id] {
					missing = append(missing, id)
				}
			}
			if len(missing) > 0 {
				tagAsset(t, alice, "pagination-check", missing...)
			}
			return len(missing) == 0
		})
		api, err := immich.New(baseURL, immich.Auth{APIKey: alice.key}, nil)
		if err != nil {
			t.Fatal(err)
		}
		api.PageSize = 2 // 5 assets over 3 pages
		got, err := api.Search(context.Background(), immich.MetadataSearchDto{TagIds: &[]uuid.UUID{uuid.MustParse(tid)}})
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, a := range got {
			seen[a.Id.String()] = true
		}
		if len(got) != 5 || len(seen) != 5 {
			t.Errorf("got %d results, %d distinct; want 5", len(got), len(seen))
		}
	})

	t.Run("duplicate is removed from source but not tagged again", func(t *testing.T) {
		before := len(searchTag(t, family, "inbox", false))
		bobOrig, _ := upload(t, bob, "bob-same.jpg", imgA) // identical bytes
		tagAsset(t, bob, "share", bobOrig)
		eventually(t, "bob's original trashed", 90*time.Second, func() bool {
			x, code := getAsset(t, bob, bobOrig)
			return code == 200 && x.IsTrashed
		})
		if got := len(searchTag(t, family, "inbox", false)); got != before {
			t.Errorf("inbox count changed %d -> %d", before, got)
		}
	})

	t.Run("a copy in the destination trash refuses the import and keeps the original", func(t *testing.T) {
		imgB := jpegBytes(2)
		bobB, _ := upload(t, bob, "bob-2.jpg", imgB)
		tagAsset(t, bob, "share", bobB)
		var famID string
		eventually(t, "second photo imported", 90*time.Second, func() bool {
			for _, id := range searchTag(t, family, "inbox", false) {
				if id != "" {
					famID = id
				}
			}
			x, code := getAsset(t, bob, bobB)
			return code == 200 && x.IsTrashed && famID != ""
		})
		// the family trashes every inbox photo, then alice sends the same bytes as image B
		for _, id := range searchTag(t, family, "inbox", false) {
			call(t, "DELETE", "/assets", keyAuth(family), map[string]any{"ids": []string{id}}, nil)
		}
		aliceB, _ := upload(t, alice, "alice-2.jpg", imgB)
		tagAsset(t, alice, "share", aliceB)
		eventually(t, "refusal logged", 90*time.Second, func() bool {
			return strings.Contains(a.log.String(), "receiver's trash")
		})
		x, _ := getAsset(t, alice, aliceB)
		if x.IsTrashed {
			t.Errorf("original was removed although the destination copy is trashed")
		}
		eventually(t, "refused original retagged share-duplicate instead of share", 30*time.Second, func() bool {
			y, _ := getAsset(t, alice, aliceB)
			names := map[string]bool{}
			for _, tg := range y.Tags {
				names[tg.Name] = true
			}
			return names["share-duplicate"] && !names["share"]
		})
	})

	t.Run("restart cleans the inbox tag from photos filed in an album", func(t *testing.T) {
		imgC := jpegBytes(3)
		cID, _ := upload(t, alice, "alice-3.jpg", imgC)
		tagAsset(t, alice, "share", cID)
		var famID string
		eventually(t, "third photo imported", 90*time.Second, func() bool {
			ids := searchTag(t, family, "inbox", false)
			if len(ids) == 0 {
				return false
			}
			famID = ids[0]
			return true
		})
		mustOK(t, call(t, "POST", "/albums", keyAuth(family), map[string]any{"albumName": "Filed", "assetIds": []string{famID}}, nil), "create album")
		a.stop()
		startApp(t, bin, alice, bob, family)
		eventually(t, "inbox tag removed after filing", 60*time.Second, func() bool {
			return len(searchTag(t, family, "inbox", false)) == 0
		})
		if x, code := getAsset(t, family, famID); code != 200 || x.IsTrashed {
			t.Errorf("filed photo must be kept (code %d)", code)
		}
	})

	t.Run("the timer cleans the inbox tag soon after a photo is filed, without a restart", func(t *testing.T) {
		// the app started by the previous subtest stopped with it, so run one for this subtest
		startApp(t, bin, alice, bob, family)
		cID, _ := upload(t, alice, "alice-4.jpg", jpegBytes(4))
		tagAsset(t, alice, "share", cID)
		var famID string
		eventually(t, "fourth photo imported", 90*time.Second, func() bool {
			ids := searchTag(t, family, "inbox", false)
			if len(ids) == 0 {
				return false
			}
			famID = ids[0]
			return true
		})
		mustOK(t, call(t, "POST", "/albums", keyAuth(family), map[string]any{"albumName": "Filed later", "assetIds": []string{famID}}, nil), "create album")
		eventually(t, "inbox tag removed by the timer", 30*time.Second, func() bool {
			return len(searchTag(t, family, "inbox", false)) == 0
		})
		if x, code := getAsset(t, family, famID); code != 200 || x.IsTrashed {
			t.Errorf("filed photo must be kept (code %d)", code)
		}
	})
}

// TestAlbum runs fixed services that put arrivals in an album: a receiver whose key may use albums, and one
// whose key may not (the photos must still arrive, tagged inbox).
func TestAlbum(t *testing.T) {
	alice, _, family, grandma := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 7)

	// a receiver key with exactly the album permissions the app asks people for
	var withAlbum struct {
		Secret string `json:"secret"`
	}
	albumPerms := append(append([]string{}, destPermissions...), "album.read", "album.create", "albumAsset.create")
	mustOK(t, call(t, "POST", "/api-keys", map[string]string{"Authorization": "Bearer " + family.token},
		map[string]any{"name": "e2e-album-" + fmt.Sprint(time.Now().UnixNano()), "permissions": albumPerms}, &withAlbum), "album key")

	routes := filepath.Join(t.TempDir(), "routes.yaml")
	if err := os.WriteFile(routes, []byte(`
accounts:
  alice:   { key: ${E2E_ALICE} }
  family:  { key: ${E2E_FAMILY} }
  grandma: { key: ${E2E_GRANDMA} }
services:
  - name: filed
    tag: for-album
    senders: [alice]
    receivers: [family]
    album: Arrivals
  - name: nopermission
    tag: for-noperm
    senders: [alice]
    receivers: [grandma]
    album: Nope
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"IMMICH_BASE_URL="+baseURL, "ROUTES_FILE="+routes,
		"E2E_ALICE="+alice.appKey, "E2E_FAMILY="+withAlbum.Secret, "E2E_GRANDMA="+grandma.appKey, // grandma's key has no album permissions
		"WEBHOOK_SECRET="+secret,
		"PUBLIC_WEBHOOK_URL=http://host.docker.internal:"+port+"/webhook/immich",
		"LISTEN_ADDR=:"+port, "SWEEP_INTERVAL_HOURS=1",
	)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(os.Interrupt)
		cmd.Wait()
		if t.Failed() {
			t.Logf("app log:\n%s", logs.String())
		}
	})
	eventually(t, "both workflows exist in alice's account", 4*time.Minute, func() bool {
		var wfs []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		}
		call(t, "GET", "/workflows", keyAuth(alice), nil, &wfs)
		n := 0
		for _, w := range wfs {
			if (w.Name == "immich-handoff: filed" || w.Name == "immich-handoff: nopermission") && w.Enabled {
				n++
			}
		}
		return n == 2
	})

	type album struct {
		ID         string `json:"id"`
		AlbumName  string `json:"albumName"`
		AssetCount int    `json:"assetCount"`
	}
	owned := func(u user, name string) []album {
		var out []album
		call(t, "GET", "/albums?isOwned=true&name="+url.QueryEscape(name), keyAuth(u), nil, &out)
		return out
	}
	send := func(who user, tag, file string, seed int) {
		id, _ := upload(t, who, file, jpegBytes(seed))
		tagAsset(t, who, tag, id)
	}

	t.Run("a photo is put in a new album, and is not tagged inbox", func(t *testing.T) {
		inboxBefore := len(searchTag(t, family, "inbox", false))
		send(alice, "for-album", "album-1.jpg", 200)
		eventually(t, "the album exists with the photo in it", 90*time.Second, func() bool {
			al := owned(family, "Arrivals")
			return len(al) == 1 && al[0].AssetCount == 1
		})
		if got := len(searchTag(t, family, "inbox", false)); got != inboxBefore {
			t.Errorf("a photo that is in the album must not also be tagged inbox: %d -> %d", inboxBefore, got)
		}
	})

	t.Run("the next photo goes to the same album, not a second one", func(t *testing.T) {
		send(alice, "for-album", "album-2.jpg", 201)
		eventually(t, "two photos in the one album", 90*time.Second, func() bool {
			al := owned(family, "Arrivals")
			return len(al) == 1 && al[0].AssetCount == 2
		})
	})

	t.Run("an album the receiver deleted comes back at the next arrival", func(t *testing.T) {
		al := owned(family, "Arrivals")
		if len(al) != 1 {
			t.Fatalf("albums: %+v", al)
		}
		mustOK(t, call(t, "DELETE", "/albums/"+al[0].ID, keyAuth(family), nil, nil), "delete album")
		send(alice, "for-album", "album-3.jpg", 202)
		eventually(t, "a new album holding the new photo", 90*time.Second, func() bool {
			now := owned(family, "Arrivals")
			return len(now) == 1 && now[0].ID != al[0].ID && now[0].AssetCount == 1
		})
	})

	t.Run("when the key may not use albums the photo still arrives, tagged inbox", func(t *testing.T) {
		before := len(searchTag(t, grandma, "inbox", false))
		send(alice, "for-noperm", "album-4.jpg", 203)
		eventually(t, "the copy is tagged inbox in grandma's account", 90*time.Second, func() bool {
			return len(searchTag(t, grandma, "inbox", false)) == before+1
		})
		if len(owned(grandma, "Nope")) != 0 {
			t.Error("no album could have been made with that key")
		}
		eventually(t, "the problem is in the log, once", 30*time.Second, func() bool {
			return strings.Contains(logs.String(), "could not put the copy in the album")
		})
	})
}

// TestAlbumWatch links a sender's album to a service from the page: everything in it, now and later, is handed
// over without a tag, once.
func TestAlbumWatch(t *testing.T) {
	_, bob, family, _ := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 8)
	dir := t.TempDir()
	routes := filepath.Join(dir, "routes.yaml")
	if err := os.WriteFile(routes, []byte(`
accounts:
  family: { key: ${E2E_FAMILY} }
services:
  - name: watch
    tag: join-watch
    receivers: [family]
    allow_join: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"IMMICH_BASE_URL="+baseURL, "ROUTES_FILE="+routes, "E2E_FAMILY="+family.appKey,
		"WEBHOOK_SECRET="+secret, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+dir,
		"PUBLIC_WEBHOOK_URL=http://host.docker.internal:"+port+"/webhook/immich", "LISTEN_ADDR=:"+port,
		"SWEEP_INTERVAL_HOURS=1", "ALBUM_CHECK_MINUTES=0.05") // look at linked albums every few seconds
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(os.Interrupt)
		cmd.Wait()
		if t.Failed() {
			t.Logf("app log:\n%s", logs.String())
		}
	})
	eventually(t, "app healthy", 30*time.Second, func() bool {
		res, err := http.Get("http://localhost:" + port + "/healthz")
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == 200
	})

	cookie := loginCookie(t, "bob@e2e.test")
	if res := post(t, port, "/join", cookie, url.Values{"service": {"watch"}, "mode": {"copy"}}); res.StatusCode != 204 {
		t.Fatalf("join: HTTP %d", res.StatusCode)
	}
	var album struct {
		ID string `json:"id"`
	}
	mustOK(t, call(t, "POST", "/albums", keyAuth(bob), map[string]any{"albumName": "To send"}, &album), "create album")
	page := func(path string) string {
		req, _ := http.NewRequest("GET", "http://localhost:"+port+path, nil)
		req.Header.Set("Cookie", cookie)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	inbox := func() int { return len(searchTag(t, family, "inbox", false)) }
	addToAlbum := func(name string, seed int) string {
		id, _ := upload(t, bob, name, jpegBytes(seed))
		mustOK(t, call(t, "PUT", "/albums/"+album.ID+"/assets", keyAuth(bob), map[string]any{"ids": []string{id}}, nil), "add to album")
		return id
	}
	before := inbox()

	t.Run("the page offers bob's albums, and linking one gives the key album.read", func(t *testing.T) {
		if body := page("/fragments/albums?service=watch"); !strings.Contains(body, "To send") {
			t.Fatalf("the album list: %.300s", body)
		}
		if res := post(t, port, "/services/album", cookie, url.Values{"service": {"watch"}, "album": {album.ID}}); res.StatusCode != 204 {
			t.Fatalf("link: HTTP %d", res.StatusCode)
		}
		var keys []struct {
			Name        string   `json:"name"`
			Permissions []string `json:"permissions"`
		}
		call(t, "GET", "/api-keys", keyAuth(bob), nil, &keys)
		ok := false
		for _, k := range keys {
			if k.Name == "handoff: watch" {
				for _, p := range k.Permissions {
					ok = ok || p == "album.read"
				}
			}
		}
		if !ok {
			t.Errorf("the key must now be allowed to read albums: %+v", keys)
		}
		if body := page("/"); !strings.Contains(body, "Everything in your album") || !strings.Contains(body, "To send") {
			t.Errorf("the card must say which album is linked: %.300s", body)
		}
	})

	t.Run("a photo put in the album is handed over without a tag, and the original is kept and marked", func(t *testing.T) {
		id := addToAlbum("watch-1.jpg", 210)
		eventually(t, "the receiver has the copy", 90*time.Second, func() bool { return inbox() == before+1 })
		eventually(t, "the original is marked as handed over", 30*time.Second, func() bool {
			x, code := getAsset(t, bob, id)
			if code != 200 || x.IsTrashed {
				return false
			}
			for _, tg := range x.Tags {
				if tg.Name == "handed-over" {
					return true
				}
			}
			return false
		})
	})

	t.Run("it is not handed over again at the next checks", func(t *testing.T) {
		time.Sleep(10 * time.Second) // several checks
		if got := inbox(); got != before+1 {
			t.Errorf("the receiver must still have exactly one copy: %d -> %d", before, got)
		}
	})

	t.Run("a photo added later is handed over too, and the page reports it", func(t *testing.T) {
		addToAlbum("watch-2.jpg", 211)
		eventually(t, "the second copy", 90*time.Second, func() bool { return inbox() == before+2 })
		eventually(t, "a line in recent events", 30*time.Second, func() bool {
			return strings.Contains(page("/"), "from the album To send handed over to watch")
		})
	})

	t.Run("after unlinking, new photos in the album stay where they are", func(t *testing.T) {
		if res := post(t, port, "/services/album/stop", cookie, url.Values{"service": {"watch"}}); res.StatusCode != 204 {
			t.Fatalf("unlink: HTTP %d", res.StatusCode)
		}
		addToAlbum("watch-3.jpg", 212)
		time.Sleep(10 * time.Second)
		if got := inbox(); got != before+2 {
			t.Errorf("nothing may be handed over after unlinking: %d", got-before)
		}
	})
}

// TestRoutes runs a routes file with a one-to-many service: one tag, two receivers.
func TestRoutes(t *testing.T) {
	alice, _, family, grandma := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 1)

	dir := t.TempDir()
	routes := filepath.Join(dir, "routes.yaml")
	if err := os.WriteFile(routes, []byte(`
accounts:
  alice:   { key: ${E2E_ALICE} }
  family:  { key: ${E2E_FAMILY} }
  grandma: { key: ${E2E_GRANDMA} }
services:
  - name: grandma
    tag: for-grandma
    senders: [alice]
    receivers: [grandma, family]
    mode: move
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"IMMICH_BASE_URL="+baseURL,
		"ROUTES_FILE="+routes,
		"E2E_ALICE="+alice.appKey, "E2E_FAMILY="+family.appKey, "E2E_GRANDMA="+grandma.appKey,
		"WEBHOOK_SECRET="+secret,
		"PUBLIC_WEBHOOK_URL=http://host.docker.internal:"+port+"/webhook/immich",
		"LISTEN_ADDR=:"+port,
		"SWEEP_INTERVAL_HOURS=1",
	)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(os.Interrupt)
		cmd.Wait()
		if t.Failed() {
			t.Logf("app log:\n%s", logs.String())
		}
	})

	eventually(t, "named workflow exists in alice's account", 4*time.Minute, func() bool {
		var wfs []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		}
		call(t, "GET", "/workflows", keyAuth(alice), nil, &wfs)
		for _, w := range wfs {
			if w.Name == "immich-handoff: grandma" && w.Enabled {
				return true
			}
		}
		return false
	})

	familyBefore := len(searchTag(t, family, "inbox", false))
	grandmaBefore := len(searchTag(t, grandma, "inbox", false)) // other tests may have left copies there
	id, _ := upload(t, alice, "to-both.jpg", jpegBytes(70))
	tagAsset(t, alice, "for-grandma", id)

	eventually(t, "both receivers hold a tagged copy", 90*time.Second, func() bool {
		return len(searchTag(t, grandma, "inbox", false)) == grandmaBefore+1 && len(searchTag(t, family, "inbox", false)) == familyBefore+1
	})
	eventually(t, "original removed from alice only after both copies exist", 30*time.Second, func() bool {
		x, code := getAsset(t, alice, id)
		return code == 200 && x.IsTrashed
	})
}

func atoi(s string) int {
	n := 0
	fmt.Sscanf(s, "%d", &n)
	return n
}

// loginCookie signs in to Immich like a browser and returns the Cookie header value.
func loginCookie(t *testing.T, email string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": "Passw0rd!x"})
	res, err := http.Post(baseURL+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var parts []string
	for _, c := range res.Cookies() {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// post sends a state-changing request to the app as a browser would (cookie plus the CSRF header).
func post(t *testing.T, port, path, cookie string, form url.Values) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", "http://localhost:"+port+path, strings.NewReader(form.Encode()))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-Requested-With", "immich-handoff")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

func keyNames(t *testing.T, u user) []string {
	var keys []struct{ Name string }
	call(t, "GET", "/api-keys", keyAuth(u), nil, &keys)
	var out []string
	for _, k := range keys {
		out = append(out, k.Name)
	}
	return out
}

func hasName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// TestSelfService: a person joins an open service from the page, hands photos over with the key the
// app created for them, survives an app restart, and leaves again.
func TestSelfService(t *testing.T) {
	_, bob, family, _ := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 2)
	dir := t.TempDir()
	routes := filepath.Join(dir, "routes.yaml")
	if err := os.WriteFile(routes, []byte(`
accounts:
  family: { key: ${E2E_FAMILY} }
services:
  - name: open
    tag: join-me
    receivers: [family]
    allow_join: true
    mode: move
`), 0o600); err != nil {
		t.Fatal(err)
	}
	launch := func() *app {
		cmd := exec.Command(bin)
		cmd.Env = append(os.Environ(),
			"IMMICH_BASE_URL="+baseURL, "ROUTES_FILE="+routes, "E2E_FAMILY="+family.appKey,
			"WEBHOOK_SECRET="+secret, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+dir,
			"PUBLIC_WEBHOOK_URL=http://host.docker.internal:"+port+"/webhook/immich", "LISTEN_ADDR=:"+port, "SWEEP_INTERVAL_HOURS=1")
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		a := &app{cmd: cmd, log: &buf}
		t.Cleanup(a.stop)
		eventually(t, "app healthy", 30*time.Second, func() bool {
			res, err := http.Get("http://localhost:" + port + "/healthz")
			if err != nil {
				return false
			}
			res.Body.Close()
			return res.StatusCode == 200
		})
		return a
	}
	a := launch()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("app log:\n%s", a.log.String())
		}
	})

	cookie := loginCookie(t, "bob@e2e.test")
	handedOver := func(label string, seed int) {
		id, _ := upload(t, bob, label, jpegBytes(seed))
		tagAsset(t, bob, "join-me", id)
		eventually(t, label+" handed over", 90*time.Second, func() bool {
			x, code := getAsset(t, bob, id)
			return code == 200 && x.IsTrashed
		})
	}
	notHandedOver := func(label string, seed int) {
		id, _ := upload(t, bob, label, jpegBytes(seed))
		tagAsset(t, bob, "join-me", id)
		time.Sleep(10 * time.Second)
		if x, _ := getAsset(t, bob, id); x.IsTrashed {
			t.Errorf("%s was handed over although bob has left", label)
		}
	}

	t.Run("a stranger is offered the service and joins", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "http://localhost:"+port+"/", nil)
		req.Header.Set("Cookie", cookie)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if !strings.Contains(string(b), "You can join") || !strings.Contains(string(b), "open") {
			t.Fatalf("join offer missing: %.300s", b)
		}
		if res := post(t, port, "/join", cookie, url.Values{"service": {"open"}, "mode": {"move"}}); res.StatusCode != 204 {
			t.Fatalf("join: HTTP %d", res.StatusCode)
		}
		if names := keyNames(t, bob); !hasName(names, "handoff: open") {
			t.Errorf("the app must have created a key named 'handoff: open', keys: %v", names)
		}
		eventually(t, "bob's workflow exists", 60*time.Second, func() bool {
			var wfs []struct{ Name string }
			call(t, "GET", "/workflows", keyAuth(bob), nil, &wfs)
			for _, w := range wfs {
				if w.Name == "immich-handoff: open" {
					return true
				}
			}
			return false
		})
	})

	t.Run("the new member hands photos over, also after a restart", func(t *testing.T) {
		handedOver("before-restart.jpg", 80)
		a.stop()
		a = launch()
		handedOver("after-restart.jpg", 81)
	})

	t.Run("leaving removes the key and the workflow and stops the hand-over", func(t *testing.T) {
		if res := post(t, port, "/leave", cookie, url.Values{"service": {"open"}}); res.StatusCode != 204 {
			t.Fatalf("leave: HTTP %d", res.StatusCode)
		}
		if names := keyNames(t, bob); hasName(names, "handoff: open") {
			t.Errorf("the key must be deleted from Immich, keys: %v", names)
		}
		var wfs []struct{ Name string }
		call(t, "GET", "/workflows", keyAuth(bob), nil, &wfs)
		for _, w := range wfs {
			if w.Name == "immich-handoff: open" {
				t.Errorf("the workflow must be deleted")
			}
		}
		notHandedOver("after-leaving.jpg", 82)
	})
}

// launchApp starts the built binary with the given extra environment and waits until it answers.
func launchApp(t *testing.T, bin, port string, extra ...string) *app {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), append([]string{
		"IMMICH_BASE_URL=" + baseURL, "WEBHOOK_SECRET=" + secret, "LISTEN_ADDR=:" + port, "SWEEP_INTERVAL_HOURS=1",
		"PUBLIC_WEBHOOK_URL=http://host.docker.internal:" + port + "/webhook/immich",
	}, extra...)...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	a := &app{cmd: cmd, log: &buf}
	t.Cleanup(func() {
		a.stop()
		if t.Failed() {
			t.Logf("app log:\n%s", buf.String())
		}
	})
	eventually(t, "app healthy", 30*time.Second, func() bool {
		res, err := http.Get("http://localhost:" + port + "/healthz")
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == 200
	})
	return a
}

func page(t *testing.T, port, cookie string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://localhost:"+port+"/", nil)
	req.Header.Set("Cookie", cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return string(b)
}

// TestHubServices: a person creates a service, another asks, the owner approves, the sender activates,
// photos flow, and when the owner deletes the service the sender can clean up.
func TestHubServices(t *testing.T) {
	alice, bob, _, _ := bootstrap(t) // alice owns the service, bob sends
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 3)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	ownerCookie, senderCookie := loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test")

	expect := func(res *http.Response, want int, what string) {
		t.Helper()
		if res.StatusCode != want {
			t.Fatalf("%s: HTTP %d, want %d", what, res.StatusCode, want)
		}
	}
	expect(post(t, port, "/services", ownerCookie, url.Values{"name": {"holiday"}, "tag": {"hol"}, "policy": {"approval"}}), 204, "create service")
	if names := keyNames(t, alice); !hasName(names, "handoff: holiday (receiving)") {
		t.Fatalf("the receiver key must be made in the owner's account, keys: %v", names)
	}
	expect(post(t, port, "/services", ownerCookie, url.Values{"name": {"holiday"}, "tag": {"x"}}), 409, "duplicate name")

	if body := page(t, port, senderCookie); !strings.Contains(body, "Needs approval") || !strings.Contains(body, "Ask to join") {
		t.Fatalf("bob should be offered the service: %.400s", body)
	}
	expect(post(t, port, "/join", senderCookie, url.Values{"service": {"holiday"}}), 409, "activating before approval")
	expect(post(t, port, "/ask", senderCookie, url.Values{"service": {"holiday"}}), 204, "ask")
	if body := page(t, port, senderCookie); !strings.Contains(body, "Waiting for approval") {
		t.Errorf("bob should see the request is pending")
	}

	body := page(t, port, ownerCookie)
	m := regexp.MustCompile(`request(?:"|&#34;|&quot;):(?:"|&#34;|&quot;)([0-9a-f-]{36})`).FindStringSubmatch(body)
	if m == nil || !strings.Contains(body, "bob@e2e.test") {
		t.Fatalf("the owner should see bob's request: %.600s", body)
	}
	expect(post(t, port, "/decide", senderCookie, url.Values{"service": {"holiday"}, "request": {m[1]}, "decision": {"approve"}}), 409, "a non-owner deciding")
	expect(post(t, port, "/decide", ownerCookie, url.Values{"service": {"holiday"}, "request": {m[1]}, "decision": {"approve"}}), 204, "approve")

	if body := page(t, port, senderCookie); !strings.Contains(body, "Activate") {
		t.Fatalf("bob should be able to activate: %.400s", body)
	}
	expect(post(t, port, "/join", senderCookie, url.Values{"service": {"holiday"}, "mode": {"move"}}), 204, "activate")
	if names := keyNames(t, bob); !hasName(names, "handoff: holiday") {
		t.Errorf("bob's sender key must exist, keys: %v", names)
	}

	before := len(searchTag(t, alice, "inbox/holiday", false))
	id, _ := upload(t, bob, "holiday.jpg", jpegBytes(90))
	tagAsset(t, bob, "hol", id)
	eventually(t, "photo reaches the owner and the original is removed", 90*time.Second, func() bool {
		x, code := getAsset(t, bob, id)
		return code == 200 && x.IsTrashed && len(searchTag(t, alice, "inbox/holiday", false)) == before+1
	})

	expect(post(t, port, "/services/delete", senderCookie, url.Values{"service": {"holiday"}}), 409, "a non-owner deleting")
	expect(post(t, port, "/services/delete", ownerCookie, url.Values{"service": {"holiday"}}), 204, "delete service")
	if names := keyNames(t, alice); hasName(names, "handoff: holiday (receiving)") {
		t.Errorf("the receiver key must be deleted, keys: %v", names)
	}
	if body := page(t, port, senderCookie); !strings.Contains(body, "Removed by its owner") {
		t.Fatalf("bob must be told the service is gone: %.400s", body)
	}
	expect(post(t, port, "/leave", senderCookie, url.Values{"service": {"holiday"}}), 204, "clean up")
	if names := keyNames(t, bob); hasName(names, "handoff: holiday") {
		t.Errorf("bob's key must be deleted after cleaning up, keys: %v", names)
	}
}

// deleteKeyNamed deletes one of the user's Immich API keys by name, like a person would in Immich's settings.
func deleteKeyNamed(t *testing.T, u user, name string) {
	t.Helper()
	var keys []struct{ ID, Name string }
	call(t, "GET", "/api-keys", keyAuth(u), nil, &keys)
	for _, k := range keys {
		if k.Name == name {
			mustOK(t, call(t, "DELETE", "/api-keys/"+k.ID, keyAuth(u), nil, nil), "delete key "+name)
			return
		}
	}
	t.Fatalf("no key named %q in %v", name, keys)
}

// TestLinkHealth: when either side deletes its key in Immich the connection pauses, both sides are
// told, no photo is touched, and everything resumes after clean up / reconnect.
func TestLinkHealth(t *testing.T) {
	alice, bob, _, _ := bootstrap(t) // alice owns the service, bob sends
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 4)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	owner, sender := loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test")
	must := func(res *http.Response, want int, what string) {
		t.Helper()
		if res.StatusCode != want {
			t.Fatalf("%s: HTTP %d, want %d", what, res.StatusCode, want)
		}
	}
	must(post(t, port, "/services", owner, url.Values{"name": {"pause"}, "tag": {"pause-tag"}, "policy": {"open"}}), 204, "create")
	must(post(t, port, "/join", sender, url.Values{"service": {"pause"}, "mode": {"move"}}), 204, "join")
	eventually(t, "bob's workflow exists", 90*time.Second, func() bool {
		var wfs []struct{ Name string }
		call(t, "GET", "/workflows", keyAuth(bob), nil, &wfs)
		for _, w := range wfs {
			if w.Name == "immich-handoff: pause" {
				return true
			}
		}
		return false
	})

	send := func(label string, seed int) string {
		id, _ := upload(t, bob, label, jpegBytes(seed))
		tagAsset(t, bob, "pause-tag", id)
		return id
	}
	trashed := func(id string) bool { x, code := getAsset(t, bob, id); return code == 200 && x.IsTrashed }
	inbox := func() int { return len(searchTag(t, alice, "inbox/pause", false)) }

	base := inbox()
	one := send("one.jpg", 100)
	eventually(t, "a healthy link hands over", 90*time.Second, func() bool { return trashed(one) && inbox() == base+1 })

	// --- the sender deletes their key ---
	deleteKeyNamed(t, bob, "handoff: pause")
	two := send("two.jpg", 101)
	eventually(t, "bob is told the connection was lost", 90*time.Second, func() bool {
		return strings.Contains(page(t, port, sender), "Connection lost")
	})
	if trashed(two) {
		t.Fatal("a photo must never be removed while the connection is broken")
	}
	if body := page(t, port, owner); !strings.Contains(body, "disconnected") {
		t.Errorf("the owner must see that bob is disconnected: %.500s", body)
	}
	must(post(t, port, "/leave", sender, url.Values{"service": {"pause"}}), 204, "clean up")
	must(post(t, port, "/join", sender, url.Values{"service": {"pause"}, "mode": {"move"}}), 204, "join again")
	eventually(t, "the waiting photo is handed over after rejoining", 90*time.Second, func() bool { return trashed(two) && inbox() == base+2 })

	// --- the owner deletes the receiver key ---
	deleteKeyNamed(t, alice, "handoff: pause (receiving)")
	three := send("three.jpg", 102)
	eventually(t, "the owner is offered a reconnect and the sender sees a pause", 90*time.Second, func() bool {
		return strings.Contains(page(t, port, owner), "Reconnect") && strings.Contains(page(t, port, sender), "Paused")
	})
	if trashed(three) {
		t.Fatal("a photo must never be removed while the service has no receiver")
	}
	must(post(t, port, "/reconnect", sender, url.Values{"service": {"pause"}}), 409, "a sender cannot reconnect")
	must(post(t, port, "/reconnect", owner, url.Values{"service": {"pause"}}), 204, "reconnect")
	eventually(t, "photos flow again after reconnecting", 90*time.Second, func() bool { return trashed(three) && inbox() == base+3 })
}

// TestInvitations: an invitation-only service is invisible in the directory, reachable only through the
// owner's link, and a new link makes the old one useless.
func TestInvitations(t *testing.T) {
	_, bob, _, _ := bootstrap(t) // alice owns the service, bob is invited
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 5)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	owner, guest := loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test")
	status := func(path, cookie string) (int, string) {
		req, _ := http.NewRequest("GET", "http://localhost:"+port+path, nil)
		req.Header.Set("Cookie", cookie)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	want := func(res *http.Response, code int, what string) {
		t.Helper()
		if res.StatusCode != code {
			t.Fatalf("%s: HTTP %d, want %d", what, res.StatusCode, code)
		}
	}
	token := func() string {
		_, body := status("/", owner)
		m := regexp.MustCompile(`data-invite="([0-9a-f]{32})"`).FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("the owner's page must show an invitation link: %.500s", body)
		}
		return m[1]
	}

	want(post(t, port, "/services", owner, url.Values{"name": {"club"}, "tag": {"club-tag"}, "policy": {"approval"}, "visibility": {"invite"}}), 204, "create")
	if _, body := status("/", guest); strings.Contains(body, ">club<") {
		t.Fatalf("an invitation-only service must not be in the directory: %.400s", body)
	}
	want(post(t, port, "/ask", guest, url.Values{"service": {"club"}}), 409, "asking by name without the link")

	link := token()
	if code, _ := status("/invite/not-a-real-token", guest); code != 404 {
		t.Errorf("a wrong link: HTTP %d", code)
	}
	code, body := status("/invite/"+link, guest)
	if code != 200 || !strings.Contains(body, "You are invited") || !strings.Contains(body, "Ask to join") {
		t.Fatalf("the link must lead to the service: %d %.500s", code, body)
	}
	want(post(t, port, "/ask", guest, url.Values{"service": {"club"}, "invite": {link}}), 204, "asking with the link")

	_, ownerPage := status("/", owner)
	m := regexp.MustCompile(`request(?:"|&#34;|&quot;):(?:"|&#34;|&quot;)([0-9a-f-]{36})`).FindStringSubmatch(ownerPage)
	if m == nil {
		t.Fatalf("the owner should see the request: %.500s", ownerPage)
	}
	want(post(t, port, "/decide", owner, url.Values{"service": {"club"}, "request": {m[1]}, "decision": {"approve"}}), 204, "approve")

	want(post(t, port, "/services/invite", guest, url.Values{"service": {"club"}}), 409, "a guest making links")
	want(post(t, port, "/services/invite", owner, url.Values{"service": {"club"}}), 204, "new link")
	if fresh := token(); fresh == link {
		t.Error("a new link must differ from the old one")
	}
	if code, _ := status("/invite/"+link, loginCookie(t, "admin@e2e.test")); code != 404 {
		t.Errorf("the old link must stop working: HTTP %d", code)
	}

	// approval already given: no link needed to activate
	want(post(t, port, "/join", guest, url.Values{"service": {"club"}, "mode": {"move"}}), 204, "activate")
	eventually(t, "bob's workflow exists", 90*time.Second, func() bool {
		var wfs []struct{ Name string }
		call(t, "GET", "/workflows", keyAuth(bob), nil, &wfs)
		for _, w := range wfs {
			if w.Name == "immich-handoff: club" {
				return true
			}
		}
		return false
	})
	id, _ := upload(t, bob, "club.jpg", jpegBytes(110))
	tagAsset(t, bob, "club-tag", id)
	eventually(t, "the invited sender hands over", 90*time.Second, func() bool {
		x, code := getAsset(t, bob, id)
		return code == 200 && x.IsTrashed
	})
}

// TestCopyMode: with a copy service the sender keeps the original (marked as handed over), the receiver
// still gets a tagged copy, and the sender's key has no delete permission at all.
func TestCopyMode(t *testing.T) {
	alice, bob, _, _ := bootstrap(t) // alice owns the service, bob sends
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 6)
	a := launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	owner, sender := loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test")
	must := func(res *http.Response, want int, what string) {
		t.Helper()
		if res.StatusCode != want {
			t.Fatalf("%s: HTTP %d, want %d", what, res.StatusCode, want)
		}
	}
	must(post(t, port, "/services", owner, url.Values{"name": {"keep"}, "tag": {"keep-tag"}, "policy": {"open"}}), 204, "create")
	if body := page(t, port, sender); !strings.Contains(body, "Join and keep my originals") || !strings.Contains(body, "Join and move my originals to my trash") {
		t.Errorf("the sender must be offered both choices: %.400s", body)
	}
	must(post(t, port, "/join", sender, url.Values{"service": {"keep"}, "mode": {"copy"}}), 204, "join")

	var keys []struct {
		Name        string
		Permissions []string
	}
	call(t, "GET", "/api-keys", keyAuth(bob), nil, &keys)
	found := false
	for _, k := range keys {
		if k.Name == "handoff: keep" {
			found = true
			for _, p := range k.Permissions {
				if p == "asset.delete" || p == "all" {
					t.Errorf("a copy-mode sender key must not be able to delete, has %v", k.Permissions)
				}
			}
		}
	}
	if !found {
		t.Fatalf("bob's key is missing: %v", keys)
	}
	eventually(t, "bob's workflow exists", 90*time.Second, func() bool {
		var wfs []struct{ Name string }
		call(t, "GET", "/workflows", keyAuth(bob), nil, &wfs)
		for _, w := range wfs {
			if w.Name == "immich-handoff: keep" {
				return true
			}
		}
		return false
	})

	before := len(searchTag(t, alice, "inbox/keep", false))
	id, _ := upload(t, bob, "keeper.jpg", jpegBytes(120))
	tagLikeThePhone(t, bob, "keep-tag", id) // the endpoint the mobile app uses
	eventually(t, "the receiver gets a tagged copy", 90*time.Second, func() bool {
		return len(searchTag(t, alice, "inbox/keep", false)) == before+1
	})
	var names map[string]bool
	eventually(t, "the original is marked as handed over", 60*time.Second, func() bool {
		x, _ := getAsset(t, bob, id)
		names = map[string]bool{}
		for _, tg := range x.Tags {
			names[tg.Name] = true
		}
		return names["handed-over"] && !names["keep-tag"]
	})
	if x, code := getAsset(t, bob, id); code != 200 || x.IsTrashed {
		t.Errorf("the original must stay (code %d, trashed %v)", code, x.IsTrashed)
	}
	time.Sleep(8 * time.Second)
	if n := len(searchTag(t, alice, "inbox/keep", false)); n != before+1 {
		t.Errorf("a copied photo must not be handed over again, inbox grew to %d", n-before)
	}
	if strings.Contains(a.log.String(), `"msg":"duplicate"`) {
		t.Errorf("Immich's search kept listing the photo and it was processed again:\n%s", a.log.String())
	}
}

// TestAdmin: Immich says who is an administrator; only they see every service and member, and they can
// remove one. Immich lets only a key's owner delete it, so the person's key stays until they clean up.
func TestAdmin(t *testing.T) {
	alice, bob, _, _ := bootstrap(t) // alice owns a service, bob sends to it
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 9)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	admin, owner, sender := loginCookie(t, "admin@e2e.test"), loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test")
	must := func(res *http.Response, want int, what string) {
		t.Helper()
		if res.StatusCode != want {
			t.Fatalf("%s: HTTP %d, want %d", what, res.StatusCode, want)
		}
	}
	get := func(cookie, path string) (int, string) {
		req, _ := http.NewRequest("GET", "http://localhost:"+port+path, nil)
		req.Header.Set("Cookie", cookie)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	must(post(t, port, "/services", owner, url.Values{"name": {"admin-svc"}, "tag": {"admin-tag"}, "policy": {"open"}}), 204, "create")
	must(post(t, port, "/join", sender, url.Values{"service": {"admin-svc"}, "mode": {"copy"}}), 204, "join")

	for name, c := range map[string]string{"owner": owner, "sender": sender, "signed out": ""} {
		if code, _ := get(c, "/admin"); code != 404 {
			t.Errorf("%s: GET /admin = %d, want 404", name, code)
		}
	}
	code, body := get(admin, "/admin")
	if code != 200 || !strings.Contains(body, "admin-svc") || !strings.Contains(body, "bob@e2e.test") || !strings.Contains(body, "alice@e2e.test") {
		t.Fatalf("the administrator's page: %d %.600s", code, body)
	}
	if _, home := get(admin, "/"); !strings.Contains(home, `href="admin"`) {
		t.Error("the administrator is not offered the admin page")
	}
	if res := post(t, port, "/admin/members/remove", owner, url.Values{"link": {"x"}}); res.StatusCode != 404 {
		t.Errorf("a non-admin removing a member: %d, want 404", res.StatusCode)
	}

	req, _ := http.NewRequest("POST", "http://localhost:"+port+"/admin/check", nil) // post() discards the body, and this one is read
	req.Header.Set("Cookie", admin)
	req.Header.Set("X-Requested-With", "immich-handoff")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), "Settings") || !strings.Contains(string(b), "reached Immich") {
		t.Errorf("Check now: %d %.400s", res.StatusCode, b)
	}

	m := regexp.MustCompile(`link&#34;:&#34;([0-9a-f-]+)&#34;`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no Remove button for bob in the page: %.800s", body)
	}
	must(post(t, port, "/admin/members/remove", admin, url.Values{"link": {m[1]}}), 204, "admin removes bob")

	// the app stopped using bob's key, but cannot delete it: it is still in bob's account, and bob is told
	if !hasName(keyNames(t, bob), "handoff: admin-svc") {
		t.Error("Immich lets only the owner delete a key; it must still be there")
	}
	if p := page(t, port, sender); !strings.Contains(p, "Connection lost") || !strings.Contains(p, "still valid in your Immich account") {
		t.Errorf("bob must be told on his page: %.800s", p)
	}
	// bob cleans up, which deletes what the app made in his account
	must(post(t, port, "/leave", sender, url.Values{"service": {"admin-svc"}}), 204, "bob cleans up")
	if hasName(keyNames(t, bob), "handoff: admin-svc") {
		t.Error("cleaning up must delete bob's key")
	}

	// removing the whole service: the owner's receiver key stays too, and the owner is told
	_, body = get(admin, "/admin")
	if !strings.Contains(body, "Remove this service") {
		t.Fatalf("no Remove this service button: %.600s", body)
	}
	must(post(t, port, "/admin/services/remove", admin, url.Values{"service": {"admin-svc"}}), 204, "admin removes the service")
	if !hasName(keyNames(t, alice), "handoff: admin-svc (receiving)") {
		t.Error("the owner's receiver key must stay: only the owner can delete it")
	}
	if p := page(t, port, owner); !strings.Contains(p, "was removed by an administrator") {
		t.Errorf("the owner must be told: %.800s", p)
	}
	if _, body := get(admin, "/admin"); strings.Contains(body, "Remove this service") {
		t.Error("the removed service is still listed")
	}
}

// TestBackupAndRestore: back up while the app runs, move the data to a fresh folder on another port, and the same
// people and services are back and still hand photos over.
func TestBackupAndRestore(t *testing.T) {
	alice, bob, _, _ := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 10)
	data := t.TempDir()
	envOf := func(port, data string) []string {
		return []string{"IMMICH_BASE_URL=" + baseURL, "WEBHOOK_SECRET=" + secret, "LISTEN_ADDR=:" + port, "DATA_DIR=" + data,
			"HANDOFF_STORE_KEY=an-e2e-store-key-0123456789",
			"PUBLIC_WEBHOOK_URL=http://host.docker.internal:" + port + "/webhook/immich"}
	}
	cli := func(env []string, args ...string) (int, string) {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return code, string(out)
	}
	a := launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+data)
	owner, sender := loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test")
	must := func(res *http.Response, want int, what string) {
		t.Helper()
		if res.StatusCode != want {
			t.Fatalf("%s: HTTP %d, want %d", what, res.StatusCode, want)
		}
	}
	must(post(t, port, "/services", owner, url.Values{"name": {"moved"}, "tag": {"moved-tag"}, "policy": {"open"}}), 204, "create")
	must(post(t, port, "/join", sender, url.Values{"service": {"moved"}, "mode": {"move"}}), 204, "join")

	// the command works against the running app, and the file appears
	code, out := cli(envOf(port, data), "backup", "--wait")
	if code != 0 || !strings.Contains(out, "Backup ready") {
		t.Fatalf("backup --wait: exit %d\n%s", code, out)
	}
	files, _ := filepath.Glob(filepath.Join(data, "backups", "handoff-*.db"))
	if len(files) != 1 || strings.Contains(files[0], "forced") {
		t.Fatalf("backup files: %v\n%s", files, out)
	}
	if code, out := cli(envOf(port, data), "backup", "--list"); code != 0 || !strings.Contains(out, filepath.Base(files[0])) {
		t.Errorf("--list: exit %d\n%s", code, out)
	}
	// replacing the data of a running app is refused
	if code, out := cli(envOf(port, data), "restore", "--force", files[0]); code != 1 || !strings.Contains(out, "running") {
		t.Errorf("restore under a running app: exit %d\n%s", code, out)
	}

	// move to a fresh folder and another port
	a.stop()
	data2, port2 := t.TempDir(), fmt.Sprint(atoi(appPort)+11)
	if code, out := cli(envOf(port2, data2), "restore", files[0]); code != 0 {
		t.Fatalf("restore: exit %d\n%s", code, out)
	}
	launchApp(t, bin, port2, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+data2)
	if p := page(t, port2, owner); !strings.Contains(p, "moved") || !strings.Contains(p, "bob") && !strings.Contains(p, "Bob") {
		t.Errorf("the service and its member must be back: %.600s", p)
	}
	// the keys still work and the workflow was repaired to point at the new address
	eventually(t, "bob's workflow points at the new app", 90*time.Second, func() bool {
		var wfs []struct {
			Name  string
			Steps []struct {
				Config map[string]any
			}
		}
		call(t, "GET", "/workflows", keyAuth(bob), nil, &wfs)
		for _, w := range wfs {
			if w.Name == "immich-handoff: moved" {
				for _, s := range w.Steps {
					if u, _ := s.Config["url"].(string); strings.Contains(u, ":"+port2+"/") {
						return true
					}
				}
			}
		}
		return false
	})
	id, _ := upload(t, bob, "after-restore.jpg", jpegBytes(9200))
	tagAsset(t, bob, "moved-tag", id)
	eventually(t, "a photo is handed over by the restored app", 90*time.Second, func() bool {
		x, code := getAsset(t, bob, id)
		return code == 200 && x.IsTrashed && len(searchTag(t, alice, "inbox/moved", false)) >= 1
	})
}

// TestPauseAndLimits: a paused sender's tagged photos stay tagged and go when they resume; a daily limit holds the
// rest back, says so, and a higher limit lets them go.
func TestPauseAndLimits(t *testing.T) {
	alice, bob, _, _ := bootstrap(t) // alice owns the service, bob sends
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 12)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	owner, sender := loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test")
	must := func(res *http.Response, want int, what string) {
		t.Helper()
		if res.StatusCode != want {
			t.Fatalf("%s: HTTP %d, want %d", what, res.StatusCode, want)
		}
	}
	must(post(t, port, "/services", owner, url.Values{"name": {"calm"}, "tag": {"calm-tag"}, "policy": {"open"}}), 204, "create")
	must(post(t, port, "/join", sender, url.Values{"service": {"calm"}, "mode": {"move"}}), 204, "join")
	eventually(t, "bob's workflow exists", 90*time.Second, func() bool {
		var wfs []struct{ Name string }
		call(t, "GET", "/workflows", keyAuth(bob), nil, &wfs)
		for _, w := range wfs {
			if w.Name == "immich-handoff: calm" {
				return true
			}
		}
		return false
	})
	trashed := func(id string) bool { x, code := getAsset(t, bob, id); return code == 200 && x.IsTrashed }
	inbox := func() int { return len(searchTag(t, alice, "inbox/calm", false)) }
	stays := func(what string, id string) {
		t.Helper()
		time.Sleep(6 * time.Second) // long enough for the webhook, which arrives within a second or two
		if trashed(id) {
			t.Fatalf("%s: the photo was handed over", what)
		}
	}

	// --- bob pauses: a photo tagged meanwhile stays tagged, and goes when he resumes ---
	must(post(t, port, "/pause", sender, url.Values{"service": {"calm"}, "paused": {"1"}}), 204, "bob pauses")
	if p := page(t, port, sender); !strings.Contains(p, "You paused sending") {
		t.Errorf("bob's page: %.600s", p)
	}
	if p := page(t, port, owner); !strings.Contains(p, "(paused)") {
		t.Errorf("the owner must see bob paused: %.600s", p)
	}
	base := inbox()
	one, _ := upload(t, bob, "paused-1.jpg", jpegBytes(9301))
	tagAsset(t, bob, "calm-tag", one)
	stays("sender paused", one)
	must(post(t, port, "/pause", sender, url.Values{"service": {"calm"}, "paused": {"0"}}), 204, "bob resumes")
	eventually(t, "the waiting photo is handed over after bob resumes", 90*time.Second, func() bool { return trashed(one) && inbox() == base+1 })

	// --- the owner pauses the whole service ---
	must(post(t, port, "/services/pause", owner, url.Values{"service": {"calm"}, "paused": {"1"}}), 204, "owner pauses")
	if p := page(t, port, sender); !strings.Contains(p, "The owner paused this service.") {
		t.Errorf("bob must be told: %.600s", p)
	}
	two, _ := upload(t, bob, "paused-2.jpg", jpegBytes(9302))
	tagAsset(t, bob, "calm-tag", two)
	stays("service paused", two)
	must(post(t, port, "/services/pause", owner, url.Values{"service": {"calm"}, "paused": {"0"}}), 204, "owner resumes")
	eventually(t, "the waiting photo is handed over after the owner resumes", 90*time.Second, func() bool { return trashed(two) && inbox() == base+2 })

	// --- a daily limit of three photos, two of which went today already: one more goes, the next waits, and a
	// higher limit lets it go ---
	must(post(t, port, "/services/limits", owner, url.Values{"service": {"calm"}, "photos": {"3"}}), 204, "limit three photos")
	three, _ := upload(t, bob, "limit-3.jpg", jpegBytes(9303))
	tagAsset(t, bob, "calm-tag", three)
	eventually(t, "the third photo of the day goes", 90*time.Second, func() bool { return trashed(three) && inbox() == base+3 })
	// three photos handed over today: the limit is reached
	four, _ := upload(t, bob, "limit-4.jpg", jpegBytes(9304))
	tagAsset(t, bob, "calm-tag", four)
	eventually(t, "bob is told the limit is reached", 90*time.Second, func() bool {
		return strings.Contains(page(t, port, sender), "The daily limit is reached")
	})
	if trashed(four) {
		t.Fatal("a photo over the limit must not be handed over")
	}
	if p := page(t, port, owner); !strings.Contains(p, "reached the daily limit") {
		t.Errorf("the owner must see the event: %.800s", p)
	}
	must(post(t, port, "/services/limits", owner, url.Values{"service": {"calm"}, "photos": {"50"}}), 204, "raise the limit")
	eventually(t, "the waiting photo goes once the limit is raised", 90*time.Second, func() bool { return trashed(four) && inbox() == base+4 })
	if p := page(t, port, sender); strings.Contains(p, "The daily limit is reached") {
		t.Error("the waiting notice must be gone once the photos went")
	}

	// the Overview's numbers: what moved this week, counted from what the app recorded
	req, _ := http.NewRequest("GET", "http://localhost:"+port+"/fragments/numbers", nil)
	req.Header.Set("Cookie", owner)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	nb, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(nb), "Handed over, last 7 days") || !strings.Contains(string(nb), ">4<") || !strings.Contains(string(nb), "In your inbox") {
		t.Errorf("numbers for the owner: %d %.600s", res.StatusCode, nb)
	}
	if p := page(t, port, owner); !strings.Contains(p, "Needs you") || !strings.Contains(p, "Nothing needs you right now.") {
		t.Errorf("the owner's Overview must lead with the strip, all clear here: %.500s", p)
	}
}

// openStream listens to the page's live stream as a signed-in person, and returns the events it carries.
func openStream(t *testing.T, port, cookie string) (status int, events <-chan map[string]any) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://localhost:"+port+"/events", nil)
	req.Header.Set("Cookie", cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan map[string]any, 64)
	if res.StatusCode != 200 {
		res.Body.Close()
		return res.StatusCode, out
	}
	go func() {
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if l := sc.Text(); strings.HasPrefix(l, "data: {") {
				var m map[string]any
				if json.Unmarshal([]byte(strings.TrimPrefix(l, "data: ")), &m) == nil {
					out <- m
				}
			}
		}
	}()
	return 200, out
}

func waitEvent(t *testing.T, who string, events <-chan map[string]any, within time.Duration, match func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case ev := <-events:
			if match(ev) {
				return ev
			}
		case <-deadline:
			t.Fatalf("%s never heard the expected live event", who)
		}
	}
}

// TestLiveEvents: a real hand-over, a pause and a daily limit are told to the page as they happen, to the sender, the
// owner and the administrator, and to nobody else.
func TestLiveEvents(t *testing.T) {
	alice, bob, _, grandma := bootstrap(t) // alice owns the service, bob sends, grandma takes no part
	_ = grandma
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 13)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir())
	owner, sender, admin, stranger := loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test"), loginCookie(t, "admin@e2e.test"), loginCookie(t, "grandma@e2e.test")
	must := func(res *http.Response, want int, what string) {
		t.Helper()
		if res.StatusCode != want {
			t.Fatalf("%s: HTTP %d, want %d", what, res.StatusCode, want)
		}
	}
	must(post(t, port, "/services", owner, url.Values{"name": {"live"}, "tag": {"live-tag"}, "policy": {"open"}}), 204, "create")
	must(post(t, port, "/join", sender, url.Values{"service": {"live"}, "mode": {"move"}}), 204, "join")
	eventually(t, "bob's workflow exists", 90*time.Second, func() bool {
		var wfs []struct{ Name string }
		call(t, "GET", "/workflows", keyAuth(bob), nil, &wfs)
		for _, w := range wfs {
			if w.Name == "immich-handoff: live" {
				return true
			}
		}
		return false
	})

	if code, _ := openStream(t, port, stranger); code != 403 {
		t.Errorf("someone who takes part in nothing must not listen: %d", code)
	}
	_, ownerFeed := openStream(t, port, owner)
	_, senderFeed := openStream(t, port, sender)
	_, adminFeed := openStream(t, port, admin)
	time.Sleep(500 * time.Millisecond) // the streams are open

	// a real hand-over
	id, _ := upload(t, bob, "live-1.jpg", jpegBytes(9401))
	tagAsset(t, bob, "live-tag", id)
	for who, feed := range map[string]<-chan map[string]any{"owner": ownerFeed, "sender": senderFeed, "administrator": adminFeed} {
		ev := waitEvent(t, who, feed, 90*time.Second, func(e map[string]any) bool { return e["kind"] == "handed" })
		if ev["service"] != "live" || ev["sender"] == nil || ev["count"] != float64(1) {
			t.Errorf("%s heard %v", who, ev)
		}
		for k := range ev {
			switch k {
			case "kind", "service", "sender", "reason", "count", "until", "at":
			default:
				t.Errorf("%s: unexpected field %q in %v", who, k, ev)
			}
		}
		if b, _ := json.Marshal(ev); strings.Contains(string(b), "live-1.jpg") || strings.Contains(string(b), id) {
			t.Errorf("%s: an event must name no photo: %s", who, b)
		}
	}
	_ = alice

	// the live scene on the page: a stage for the service with a lane for bob, which follows what really happens
	if p := page(t, port, owner); !strings.Contains(p, `data-service="live"`) || !strings.Contains(p, `data-state="flow"`) || !strings.Contains(p, "arrived while you watch") {
		t.Errorf("the owner's page must have the live scene with bob's lane: %.600s", p)
	}

	// the sender pauses
	must(post(t, port, "/pause", sender, url.Values{"service": {"live"}, "paused": {"1"}}), 204, "bob pauses")
	if p := page(t, port, owner); !strings.Contains(p, `data-state="paused" data-paused="sender"`) || !strings.Contains(p, "Paused by the sender") {
		t.Errorf("bob's lane must show his pause: %.800s", p)
	}
	for who, feed := range map[string]<-chan map[string]any{"owner": ownerFeed, "sender": senderFeed} {
		waitEvent(t, who, feed, 10*time.Second, func(e map[string]any) bool { return e["kind"] == "paused" && e["reason"] == "sender" })
	}
	must(post(t, port, "/pause", sender, url.Values{"service": {"live"}, "paused": {"0"}}), 204, "bob resumes")
	waitEvent(t, "owner", ownerFeed, 10*time.Second, func(e map[string]any) bool { return e["kind"] == "resumed" })
	if p := page(t, port, owner); strings.Contains(p, `data-paused="sender"`) {
		t.Errorf("after resuming the lane must not say paused: %.400s", p)
	}

	// the daily limit: one photo went today, so a limit of one holds the next
	must(post(t, port, "/services/limits", owner, url.Values{"service": {"live"}, "photos": {"1"}}), 204, "limit")
	id2, _ := upload(t, bob, "live-2.jpg", jpegBytes(9402))
	tagAsset(t, bob, "live-tag", id2)
	held := waitEvent(t, "owner", ownerFeed, 90*time.Second, func(e map[string]any) bool { return e["kind"] == "held" })
	if held["reason"] != "limit" || held["count"] != float64(1) || held["until"] == nil {
		t.Errorf("held: %v", held)
	}
	waitEvent(t, "sender", senderFeed, 10*time.Second, func(e map[string]any) bool { return e["kind"] == "limit" })
	if p := page(t, port, owner); !strings.Contains(p, `data-state="capped"`) || !strings.Contains(p, "Daily limit reached") {
		t.Errorf("bob's lane must show the limit: %.800s", p)
	}
}
