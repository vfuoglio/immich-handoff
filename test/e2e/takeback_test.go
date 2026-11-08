//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// keyPermissions reads the permissions of the account's API key with this name.
func keyPermissions(t *testing.T, u user, name string) []string {
	t.Helper()
	var keys []struct {
		Name        string
		Permissions []string
	}
	call(t, "GET", "/api-keys", keyAuth(u), nil, &keys)
	for _, k := range keys {
		if k.Name == name {
			return k.Permissions
		}
	}
	t.Fatalf("no key named %q in the account", name)
	return nil
}

func has(list []string, want string) bool {
	for _, x := range list {
		if x == want {
			return true
		}
	}
	return false
}

// backdate makes Immich think a photo was uploaded days ago: copies reach the end of their lifetime by the date they
// were uploaded, and a test cannot wait for that. It writes to the throwaway Immich's database.
func backdate(t *testing.T, id string, days int) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is needed to age a photo in the throwaway Immich's database")
	}
	project := envOr("COMPOSE_PROJECT_NAME", "immich-handoff-e2e")
	sql := fmt.Sprintf(`UPDATE asset SET "createdAt" = now() - interval '%d days' WHERE id = '%s'`, days, id)
	out, err := exec.Command("docker", "compose", "-p", project, "-f", "../docker-compose.yml", "exec", "-T", "database", "psql", "-U", "postgres", "-d", "immich", "-c", sql).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "UPDATE 1") {
		t.Fatalf("could not change the upload date in Immich's database: %v %s", err, out)
	}
}

// postBody is post that also returns the answer.
func postBody(t *testing.T, port, path, cookie string, form url.Values) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", "http://localhost:"+port+path, strings.NewReader(form.Encode()))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-Requested-With", "immich-handoff")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// TestTakeBackAndExpiry: a sender takes their last hand-over back (the copy goes to the receiver's trash, the original
// returns from theirs), a copy the receiver filed in an album is theirs and stays, and copies that outlive the
// service's lifetime go to the receiver's trash, all with the receiver key the app made.
func TestTakeBackAndExpiry(t *testing.T) {
	alice, bob, _, _ := bootstrap(t) // alice owns the service, bob sends
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 40)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir(), "CLEANUP_INTERVAL_MINUTES=0.05")
	owner, sender := loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test")
	must := func(code int, want int, what string) {
		t.Helper()
		if code != want {
			t.Fatalf("%s: HTTP %d, want %d", what, code, want)
		}
	}
	code, _ := postBody(t, port, "/services", owner, url.Values{"name": {"frame"}, "tag": {"frame-tag"}, "policy": {"open"}})
	must(code, 204, "create")
	if !has(keyPermissions(t, alice, "handoff: frame (receiving)"), "asset.delete") {
		t.Fatal("a receiver key made while deleting is on (the default) must be able to move copies to the trash")
	}
	code, _ = postBody(t, port, "/join", sender, url.Values{"service": {"frame"}, "mode": {"move"}})
	must(code, 204, "join")
	eventually(t, "bob's workflow exists", 90*time.Second, func() bool {
		var wfs []struct{ Name string }
		call(t, "GET", "/workflows", keyAuth(bob), nil, &wfs)
		for _, w := range wfs {
			if w.Name == "immich-handoff: frame" {
				return true
			}
		}
		return false
	})
	inbox := func() []string { return searchTag(t, alice, "inbox/frame", false) }
	handOver := func(label string, seed int) (original, copyID string) {
		t.Helper()
		before := map[string]bool{}
		for _, x := range inbox() {
			before[x] = true
		}
		original, _ = upload(t, bob, label, jpegBytes(seed))
		tagAsset(t, bob, "frame-tag", original)
		eventually(t, label+" handed over", 90*time.Second, func() bool {
			x, c := getAsset(t, bob, original)
			if c != 200 || !x.IsTrashed {
				return false
			}
			for _, id := range inbox() {
				if !before[id] {
					copyID = id
				}
			}
			return copyID != ""
		})
		return
	}
	trashedAt := func(u user, id string) bool { x, c := getAsset(t, u, id); return c == 200 && x.IsTrashed }

	// --- bob takes his last hand-over back ---
	a, aCopy := handOver("take-back-1.jpg", 9801)
	if p := page(t, port, sender); !strings.Contains(p, "Take back the last hand-over") || !strings.Contains(p, "1 photo handed over") {
		t.Errorf("bob must be offered to take it back: %.300s", p)
	}
	code, body := postBody(t, port, "/services/takeback", sender, url.Values{"service": {"frame"}})
	must(code, 204, "take back: "+body)
	eventually(t, "the receiver's copy is in the receiver's trash", 30*time.Second, func() bool { return trashedAt(alice, aCopy) })
	x, _ := getAsset(t, bob, a)
	if x.IsTrashed {
		t.Error("bob's original must come back from his trash")
	}
	for _, tg := range x.Tags {
		if tg.Name == "frame-tag" {
			t.Error("the restored original must not carry the service tag, or it would be handed over again")
		}
	}
	time.Sleep(6 * time.Second)
	if trashedAt(bob, a) {
		t.Error("the restored original was handed over again")
	}
	if code, body := postBody(t, port, "/services/takeback", sender, url.Values{"service": {"frame"}}); code != 409 || !strings.Contains(body, "nothing to take back") {
		t.Errorf("a second take back has nothing to take: %d %s", code, body)
	}

	// --- a copy the receiver filed in an album stays ---
	b, bCopy := handOver("take-back-2.jpg", 9802)
	mustOK(t, call(t, "POST", "/albums", keyAuth(alice), map[string]any{"albumName": "Kept", "assetIds": []string{bCopy}}, nil), "alice files the copy")
	code, body = postBody(t, port, "/services/takeback", sender, url.Values{"service": {"frame"}})
	if code != 200 || !strings.Contains(body, "stayed with the receiver") {
		t.Fatalf("the filed copy stays and the page says so: %d %s", code, body)
	}
	if trashedAt(alice, bCopy) {
		t.Error("a copy the receiver filed in an album must not be trashed")
	}
	if !trashedAt(bob, b) {
		t.Error("the original of a photo that stays handed over stays in the trash")
	}

	// --- copies live for their lifetime, then go to the trash unless filed ---
	code, _ = postBody(t, port, "/services/lifetime", owner, url.Values{"service": {"frame"}, "days": {"2"}})
	must(code, 204, "lifetime")
	_, young := handOver("young.jpg", 9803)
	_, old := handOver("old.jpg", 9804)
	_, filed := handOver("filed.jpg", 9805)
	mustOK(t, call(t, "POST", "/albums", keyAuth(alice), map[string]any{"albumName": "Also kept", "assetIds": []string{filed}}, nil), "alice files a copy")
	backdate(t, old, 5)
	backdate(t, filed, 5)
	eventually(t, "the old unfiled copy goes to the trash", 60*time.Second, func() bool { return trashedAt(alice, old) })
	if trashedAt(alice, young) {
		t.Error("a copy inside its lifetime must stay")
	}
	if trashedAt(alice, filed) {
		t.Error("a copy filed in an album must stay, however old")
	}
	if p := page(t, port, owner); !strings.Contains(p, "reached the end of its lifetime") {
		t.Errorf("the owner's activity must say that a copy expired: %.200s", p)
	}
}

// TestReceiverDeleteSwitch: RECEIVER_DELETE decides whether receiver keys may delete. A key made while it is off has no
// delete permission; after the administrator turns it on and restarts, its owner opening the page gives it the
// permission; turned off again, the next visit takes it away.
func TestReceiverDeleteSwitch(t *testing.T) {
	alice, _, _, _ := bootstrap(t)
	bin := buildApp(t)
	dir := t.TempDir()
	storeKey := "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789"
	owner := loginCookie(t, "alice@e2e.test")

	off := launchApp(t, bin, fmt.Sprint(atoi(appPort)+41), storeKey, "DATA_DIR="+dir, "RECEIVER_DELETE=off")
	code, _ := postBody(t, fmt.Sprint(atoi(appPort)+41), "/services", owner, url.Values{"name": {"lent"}, "tag": {"lent-tag"}, "policy": {"open"}})
	if code != 204 {
		t.Fatalf("create: %d", code)
	}
	key := "handoff: lent (receiving)"
	if has(keyPermissions(t, alice, key), "asset.delete") {
		t.Fatal("with RECEIVER_DELETE=off the receiver key is made without asset.delete")
	}
	if code, body := postBody(t, fmt.Sprint(atoi(appPort)+41), "/services/lifetime", owner, url.Values{"service": {"lent"}, "days": {"3"}}); code != 409 || !strings.Contains(body, "switched off by the administrator") {
		t.Errorf("a lifetime cannot be set while it is off: %d %s", code, body)
	}
	off.stop()

	// on again (the default): the owner opening the page gives the key what it lacks
	on := launchApp(t, bin, fmt.Sprint(atoi(appPort)+42), storeKey, "DATA_DIR="+dir)
	page(t, fmt.Sprint(atoi(appPort)+42), owner)
	eventually(t, "the key gets asset.delete when its owner opens the page", 30*time.Second, func() bool { return has(keyPermissions(t, alice, key), "asset.delete") })
	on.stop()

	// and off once more: the permission is taken away again, the rest of the key stays
	launchApp(t, bin, fmt.Sprint(atoi(appPort)+43), storeKey, "DATA_DIR="+dir, "RECEIVER_DELETE=off")
	page(t, fmt.Sprint(atoi(appPort)+43), owner)
	eventually(t, "the key loses asset.delete when its owner opens the page", 30*time.Second, func() bool {
		p := keyPermissions(t, alice, key)
		return !has(p, "asset.delete") && has(p, "asset.upload")
	})
}
