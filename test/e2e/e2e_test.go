//go:build e2e

// End-to-end tests against a real, disposable Immich (see test/docker-compose.yml and test/run.sh).
// They build and run the actual binary, so the generated client, the workflow provisioning and the
// webhook path are all exercised for real.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	secret = "e2e-secret"
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

type user struct{ id, token, key string }

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

func keyAuth(u user) map[string]string { return map[string]string{"x-api-key": u.key} }

func mustOK(t *testing.T, code int, what string) {
	t.Helper()
	if code >= 300 {
		t.Fatalf("%s failed with HTTP %d", what, code)
	}
}

func bootstrap(t *testing.T) (alice, bob, family user) {
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

	mk := func(name string) user {
		email := name + "@e2e.test"
		call(t, "POST", "/admin/users", admin, map[string]any{"email": email, "name": name, "password": pw, "shouldChangePassword": false}, nil) // 400 if exists
		var l struct {
			AccessToken string `json:"accessToken"`
			UserID      string `json:"userId"`
		}
		mustOK(t, call(t, "POST", "/auth/login", nil, map[string]string{"email": email, "password": pw}, &l), "login "+name)
		var k struct {
			Secret string `json:"secret"`
		}
		mustOK(t, call(t, "POST", "/api-keys", map[string]string{"Authorization": "Bearer " + l.AccessToken},
			map[string]any{"name": "e2e-" + fmt.Sprint(time.Now().UnixNano()), "permissions": []string{"all"}}, &k), "api key "+name)
		u := user{id: l.UserID, token: l.AccessToken, key: k.Secret}
		call(t, "PUT", "/users/me/preferences", keyAuth(u), map[string]any{"tags": map[string]any{"enabled": true}}, nil)
		return u
	}
	return mk("alice"), mk("bob"), mk("family")
}

// jpeg returns distinct, valid JPEG bytes per seed.
func jpegBytes(seed int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for x := 0; x < 64; x++ {
		for y := 0; y < 64; y++ {
			img.Set(x, y, color.RGBA{uint8(seed * 37), uint8(x * 4), uint8(y * 4), 255})
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
	return out.ID, out.Status
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
	mustOK(t, call(t, "PUT", "/tags/"+tags[0].ID+"/assets", keyAuth(u), map[string]any{"ids": ids}, nil), "tag asset")
}

// search returns the ids of assets with a tag, optionally including trashed ones.
func searchTag(t *testing.T, u user, tag string, withTrashed bool) []string {
	t.Helper()
	var tags []struct{ ID, Name string }
	call(t, "GET", "/tags", keyAuth(u), nil, &tags)
	var tid string
	for _, x := range tags {
		if x.Name == tag {
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
		"IMMICH_SOURCE_API_KEYS="+alice.key+","+bob.key,
		"IMMICH_DEST_API_KEY="+family.key,
		"WEBHOOK_SECRET="+secret,
		// Immich runs in a container; the app runs on the host.
		"PUBLIC_WEBHOOK_URL=http://host.docker.internal:"+appPort+"/webhook/immich",
		"LISTEN_ADDR=:"+appPort,
		"LOG_LEVEL=debug",
		"SWEEP_INTERVAL_HOURS=1", // so anything imported after startup arrived through the webhook
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
	alice, bob, family := bootstrap(t)
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

	imgA := jpegBytes(1)
	var aliceOrig string

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
			return strings.Contains(a.log.String(), "destination trash")
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
}
