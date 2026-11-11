//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// This file is last in the suite's alphabet on purpose (see zz_dropbox_test.go): it needs a second Immich, which the
// test starts itself.

// The second Immich: the receiver's own server. It is a second compose project of the same file on another port.
var farPort = envOr("E2E_FAR_PORT", fmt.Sprint(atoi(envOr("E2E_PORT", "2284"))+5))

func farURL() string { return "http://localhost:" + farPort }

func farProject() string { return envOr("COMPOSE_PROJECT_NAME", "immich-handoff-e2e") + "-far" }

func farCompose(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("docker", append([]string{"compose", "-p", farProject(), "-f", "../docker-compose.yml"}, args...)...)
	cmd.Env = append(os.Environ(), "E2E_PORT="+farPort)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker compose %v: %v\n%s", args, err, out)
	}
}

func farCall(t *testing.T, method, path string, auth map[string]string, body any, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, farURL()+"/api"+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range auth {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("far %s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if out != nil && len(b) > 0 {
		json.Unmarshal(b, out)
	}
	return res.StatusCode
}

func farUp(t *testing.T) bool {
	res, err := http.Get(farURL() + "/api/server/ping")
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == 200
}

func startFarImmich(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is needed to start a second Immich")
	}
	t.Cleanup(func() {
		cmd := exec.Command("docker", "compose", "-p", farProject(), "-f", "../docker-compose.yml", "down", "-v")
		cmd.Env = append(os.Environ(), "E2E_PORT="+farPort)
		cmd.Run()
	})
	farCompose(t, "up", "-d", "--wait", "--wait-timeout", "300")
}

// farUser is the person on the second server: key is their own full-access key (to look at their library as they would),
// bridge is the key they make for the app, with exactly the four permissions.
type farUser struct{ token, key, bridge string }

func farBootstrap(t *testing.T) farUser {
	t.Helper()
	pw := "Passw0rd!x"
	farCall(t, "POST", "/auth/admin-sign-up", nil, map[string]string{"email": "admin@far.test", "name": "admin", "password": pw}, nil)
	var login struct {
		AccessToken string `json:"accessToken"`
	}
	mustOK(t, farCall(t, "POST", "/auth/login", nil, map[string]string{"email": "admin@far.test", "password": pw}, &login), "far admin login")
	admin := map[string]string{"Authorization": "Bearer " + login.AccessToken}
	farCall(t, "POST", "/admin/users", admin, map[string]any{"email": "lia@far.test", "name": "lia", "password": pw, "shouldChangePassword": false}, nil)
	var l struct {
		AccessToken string `json:"accessToken"`
	}
	mustOK(t, farCall(t, "POST", "/auth/login", nil, map[string]string{"email": "lia@far.test", "password": pw}, &l), "far login")
	u := farUser{token: l.AccessToken}
	u.key = farKey(t, u, "lia-full", []string{"all"})
	u.bridge = farKey(t, u, "lia-handoff", []string{"user.read", "tag.create", "tag.asset", "asset.upload"})
	farCall(t, "PUT", "/users/me/preferences", map[string]string{"x-api-key": u.key}, map[string]any{"tags": map[string]any{"enabled": true}}, nil)
	return u
}

func farKey(t *testing.T, u farUser, name string, perms []string) string {
	t.Helper()
	var k struct {
		Secret string `json:"secret"`
	}
	mustOK(t, farCall(t, "POST", "/api-keys", map[string]string{"Authorization": "Bearer " + u.token},
		map[string]any{"name": fmt.Sprintf("%s-%d", name, time.Now().UnixNano()), "permissions": perms}, &k), "far api key")
	return k.Secret
}

// farAssetsTagged lists the photos in the second library that carry the tag, read with the person's own full key.
func farAssetsTagged(t *testing.T, u farUser, tag string) []string {
	t.Helper()
	auth := map[string]string{"x-api-key": u.key}
	var tags []struct{ ID, Name, Value string }
	farCall(t, "GET", "/tags", auth, nil, &tags)
	for _, x := range tags {
		if x.Value == tag || x.Name == tag {
			var res struct {
				Assets struct{ Items []struct{ ID string } } `json:"assets"`
			}
			farCall(t, "POST", "/search/metadata", auth, map[string]any{"tagIds": []string{x.ID}}, &res)
			var ids []string
			for _, i := range res.Assets.Items {
				ids = append(ids, i.ID)
			}
			return ids
		}
	}
	return nil
}

var bridgeLink = regexp.MustCompile(`data-link="bridge/([0-9a-f]+)"`)

// TestBridge: a service hands photos to someone on another Immich server with a key that can only upload and tag; a lost
// connection holds the photos without loss (for the owner's own receiver too) and they go when it returns; a revoked key
// stops everything, once told, and a new invitation reconnects.
func TestBridge(t *testing.T) {
	alice, bob, _, _ := bootstrap(t) // alice owns the service, bob sends
	startFarImmich(t)
	lia := farBootstrap(t)
	bin := buildApp(t)

	var mu sync.Mutex
	var told []string
	notify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		told = append(told, string(b))
		mu.Unlock()
	}))
	defer notify.Close()
	toldAbout := func(word string) (n int) {
		mu.Lock()
		defer mu.Unlock()
		for _, m := range told {
			if strings.Contains(m, word) {
				n++
			}
		}
		return
	}

	port := fmt.Sprint(atoi(appPort) + 50)
	a := launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir(), "CLEANUP_INTERVAL_MINUTES=0.05",
		"BRIDGE_ENABLE=true", "BRIDGE_ALLOW_PRIVATE=true", "BRIDGE_RETRY_MINUTES=0.05", "NOTIFY_URL="+notify.URL)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("app log:\n%s", a.log.String())
		}
	})
	owner, sender := loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test")
	must := func(code, want int, what string) {
		t.Helper()
		if code != want {
			t.Fatalf("%s: HTTP %d, want %d", what, code, want)
		}
	}
	code, _ := postBody(t, port, "/services", owner, url.Values{"name": {"far"}, "tag": {"far-tag"}, "policy": {"open"}})
	must(code, 204, "create")
	code, _ = postBody(t, port, "/join", sender, url.Values{"service": {"far"}, "mode": {"move"}})
	must(code, 204, "join")
	eventually(t, "bob's workflow exists", 90*time.Second, func() bool {
		var wfs []struct{ Name string }
		call(t, "GET", "/workflows", keyAuth(bob), nil, &wfs)
		for _, w := range wfs {
			if w.Name == "immich-handoff: far" {
				return true
			}
		}
		return false
	})

	// --- the invitation, from the owner's page to a working connection ---
	invite := func() string {
		t.Helper()
		code, body := postBody(t, port, "/services/bridge-invite", owner, url.Values{"service": {"far"}})
		must(code, 204, "invite: "+body)
		m := bridgeLink.FindStringSubmatch(page(t, port, owner))
		if m == nil {
			t.Fatal("the owner's page does not show the invitation link")
		}
		return m[1]
	}
	connect := func(token, address, key string) (int, string) {
		t.Helper()
		res, err := http.PostForm("http://localhost:"+port+"/bridge/"+token, url.Values{"address": {address}, "key": {key}, "name": {"Lia"}})
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	token := invite()
	if res, _ := http.Get("http://localhost:" + port + "/bridge/" + token); res == nil || res.StatusCode != 200 {
		t.Fatal("the invitation page must open without a login")
	}
	// a key that can read the library is refused, and nothing is kept
	if code, body := connect(token, farURL(), lia.key); code != 409 || !strings.Contains(body, "can read your library") {
		t.Fatalf("a full-access key: %d %.300s", code, body)
	}
	if code, body := connect(token, farURL(), lia.bridge); code != 200 || !strings.Contains(body, "Connected") {
		t.Fatalf("the four-permission key: %d %.300s", code, body)
	}
	if p := page(t, port, owner); !strings.Contains(p, "Lia") || !strings.Contains(p, "Connected") {
		t.Errorf("the owner's page must show the connection: %.300s", p)
	}

	// the key can do what it was made for, and nothing else
	if c := farCall(t, "GET", "/albums", map[string]string{"x-api-key": lia.bridge}, nil, nil); c != 403 {
		t.Errorf("the bridge key can list albums (HTTP %d)", c)
	}

	inbox := func() []string { return searchTag(t, alice, "inbox/far", false) }
	handOver := func(label string, seed int) string {
		t.Helper()
		original, _ := upload(t, bob, label, jpegBytes(seed))
		tagAsset(t, bob, "far-tag", original)
		return original
	}
	trashed := func(id string) bool { x, c := getAsset(t, bob, id); return c == 200 && x.IsTrashed }

	// --- a photo goes to the owner and to the other server ---
	first := handOver("bridge-1.jpg", 12001)
	eventually(t, "the first photo reached both libraries", 120*time.Second, func() bool {
		return trashed(first) && len(inbox()) == 1 && len(farAssetsTagged(t, lia, "from/bob")) == 1
	})
	eventually(t, "the tag stays on the other server", 30*time.Second, func() bool { return len(farAssetsTagged(t, lia, "from/bob")) == 1 })

	// --- the other server goes away: nothing is sent, nothing is deleted, not even for the owner ---
	farCompose(t, "stop", "immich-server")
	second := handOver("bridge-2.jpg", 12002)
	eventually(t, "the owner's page says the other server cannot be reached", 90*time.Second, func() bool {
		return strings.Contains(page(t, port, owner), "Cannot be reached")
	})
	time.Sleep(10 * time.Second)
	if trashed(second) {
		t.Fatal("the original was removed although the other server has no copy")
	}
	if n := len(inbox()); n != 1 {
		t.Fatalf("the owner received a copy while the other server was away: %d in the inbox", n)
	}
	eventually(t, "the owner was told once", 30*time.Second, func() bool { return toldAbout("cannot reach") >= 1 })
	if n := toldAbout("cannot reach"); n != 1 {
		t.Errorf("told %d times about an unreachable server", n)
	}

	// --- it comes back: the waiting photo goes, by itself ---
	farCompose(t, "start", "immich-server")
	eventually(t, "the other server answers again", 3*time.Minute, farUpFn(t))
	eventually(t, "the waiting photo reached both libraries", 3*time.Minute, func() bool {
		return trashed(second) && len(inbox()) == 2 && len(farAssetsTagged(t, lia, "from/bob")) == 2
	})
	if !strings.Contains(page(t, port, owner), "Connected") {
		t.Error("the page must show it connected again")
	}
	if toldAbout("again") < 1 {
		t.Error("the owner must be told it works again")
	}

	// --- the receiver revokes the key: stopped, told once, nothing touched ---
	var keys []struct{ ID, Name string }
	farCall(t, "GET", "/api-keys", map[string]string{"x-api-key": lia.key}, nil, &keys)
	for _, k := range keys {
		if strings.HasPrefix(k.Name, "lia-handoff") {
			farCall(t, "DELETE", "/api-keys/"+k.ID, map[string]string{"Authorization": "Bearer " + lia.token}, nil, nil)
		}
	}
	third := handOver("bridge-3.jpg", 12003)
	eventually(t, "the owner's page says the key was revoked", 90*time.Second, func() bool {
		return strings.Contains(page(t, port, owner), "Key revoked")
	})
	time.Sleep(10 * time.Second)
	if trashed(third) || len(inbox()) != 2 {
		t.Fatalf("with a revoked key nothing may be handed over (original trashed: %v, inbox %d)", trashed(third), len(inbox()))
	}
	if n := toldAbout("revoked"); n != 1 {
		t.Errorf("told %d times about a revoked key, want once", n)
	}

	// --- a new key and a new invitation reconnect it, and the waiting photo goes ---
	lia.bridge = farKey(t, lia, "lia-handoff", []string{"user.read", "tag.create", "tag.asset", "asset.upload"})
	if code, body := connect(invite(), farURL(), lia.bridge); code != 200 {
		t.Fatalf("reconnecting: %d %.300s", code, body)
	}
	eventually(t, "the waiting photo reached both libraries after reconnecting", 3*time.Minute, func() bool {
		return trashed(third) && len(inbox()) == 3 && len(farAssetsTagged(t, lia, "from/bob")) == 3
	})

	// --- taking a hand-over back reaches the other server only if its key was given asset.delete ---
	farIDs := func() map[string]bool {
		m := map[string]bool{}
		for _, id := range farAssetsTagged(t, lia, "from/bob") {
			m[id] = true
		}
		return m
	}
	before := farIDs()
	fifth := handOver("bridge-5.jpg", 12005)
	eventually(t, "the fifth photo reached both libraries", 120*time.Second, func() bool {
		return trashed(fifth) && len(inbox()) == 4 && len(farIDs()) == len(before)+1
	})
	if p := page(t, port, owner); !strings.Contains(p, "does not reach them") {
		t.Errorf("the owner must be told that taking back does not reach a key without asset.delete: %.200s", p)
	}
	code, body := postBody(t, port, "/services/takeback", sender, url.Values{"service": {"far"}})
	if code != 200 || !strings.Contains(body, "on another server stayed there") {
		t.Fatalf("taking back with a key that cannot delete: %d %s", code, body)
	}
	eventually(t, "the owner's copy went and the original came back", 60*time.Second, func() bool { return len(inbox()) == 3 && !trashed(fifth) })
	if n := len(farIDs()); n != len(before)+1 {
		t.Fatalf("the other server lost photos (%d) although its key cannot delete", n)
	}

	// the receiver adds the optional permission to a new key and connects again
	lia.bridge = farKey(t, lia, "lia-handoff", []string{"user.read", "tag.create", "tag.asset", "asset.upload", "asset.delete"})
	if code, body := connect(invite(), farURL(), lia.bridge); code != 200 {
		t.Fatalf("connecting with asset.delete: %d %.300s", code, body)
	}
	if p := page(t, port, owner); !strings.Contains(p, "Taking a hand-over back reaches them") {
		t.Errorf("the owner must be told that taking back reaches the receiver: %.200s", p)
	}
	before = farIDs()
	sixth := handOver("bridge-6.jpg", 12006)
	eventually(t, "the sixth photo reached both libraries", 120*time.Second, func() bool {
		return trashed(sixth) && len(inbox()) == 4 && len(farIDs()) == len(before)+1
	})
	var added string
	for id := range farIDs() {
		if !before[id] {
			added = id
		}
	}
	code, body = postBody(t, port, "/services/takeback", sender, url.Values{"service": {"far"}})
	if code != 204 {
		t.Fatalf("taking back with asset.delete: %d %s", code, body)
	}
	eventually(t, "the copy on the other server went to its trash and the rest came back", 60*time.Second, func() bool {
		return len(farIDs()) == len(before) && len(inbox()) == 3 && !trashed(sixth)
	})
	var gone struct {
		IsTrashed bool `json:"isTrashed"`
	}
	if c := farCall(t, "GET", "/assets/"+added, map[string]string{"x-api-key": lia.key}, nil, &gone); c != 200 || !gone.IsTrashed {
		t.Errorf("the copy must be in the receiver's trash, not deleted for good (HTTP %d, trashed %v)", c, gone.IsTrashed)
	}

	// --- the owner removes the connection: later photos go to the owner alone ---
	code, body = postBody(t, port, "/services/bridge/remove", owner, url.Values{"service": {"far"}})
	must(code, 204, "remove: "+body)
	fourth := handOver("bridge-4.jpg", 12004)
	eventually(t, "the next photo reached the owner alone", 120*time.Second, func() bool { return trashed(fourth) && len(inbox()) == 4 })
	if n := len(farIDs()); n != len(before) {
		t.Errorf("the other server holds %d photos, want %d: a removed connection must receive nothing", n, len(before))
	}
}

func farUpFn(t *testing.T) func() bool { return func() bool { return farUp(t) } }
