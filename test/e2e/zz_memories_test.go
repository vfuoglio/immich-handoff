//go:build e2e

// Named to sort last, like the other zz_ tests: it leaves a photo and a memory in the shared Immich.
package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestOnThisDay: a person turns on the daily message from their page on a real Immich. The app makes a key that can read
// memories and look at pictures and nothing else; a memory made for today (POST /memories) reaches them by email with its
// thumbnail inside the mail and the links in it; the page the link leads to shows the photo; and turning it off deletes the key.
func TestOnThisDay(t *testing.T) {
	_, bob, _, _ := bootstrap(t)
	sink := newMailSink(t)
	notifyFile := filepath.Join(t.TempDir(), "notify.yaml")
	if err := os.WriteFile(notifyFile, []byte(fmt.Sprintf(`destinations:
  - name: people
    format: email
    host: 127.0.0.1
    port: %d
    tls: none
    from: Immich Handoff <handoff@e2e.test>
    people: true
    group: 2s
`, sink.port())), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 90)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir(), "NOTIFY_FILE="+notifyFile,
		"PUBLIC_APP_URL=http://handoff.test", "PUBLIC_IMMICH_URL=http://photos.test", "MEMORIES_CHECK_MINUTES=0.1")
	cookie := loginCookie(t, "bob@e2e.test")

	// a photo of bob's, processed, in a memory for today
	seed := uint32(time.Now().UnixNano())
	id, _ := upload(t, bob, fmt.Sprintf("memory-%d.jpg", seed), noiseJPEG(seed, 40, 220))
	waitProcessed(t, bob, id)
	eventually(t, "Immich has made the thumbnail", 60*time.Second, func() bool { // a memory is made from old photos, whose thumbnails exist
		req, _ := http.NewRequest("GET", baseURL+"/api/assets/"+id+"/thumbnail?size=thumbnail", nil)
		req.Header.Set("Cookie", cookie)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == 200
	})
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	session := map[string]string{"Cookie": cookie}
	mustOK(t, call(t, "POST", "/memories", session, map[string]any{
		"type": "on_this_day", "memoryAt": start.AddDate(-3, 0, 0).Format(time.RFC3339), "data": map[string]any{"year": now.Year() - 3},
		"assetIds": []string{id}, "showAt": start.Format(time.RFC3339), "hideAt": start.Add(24*time.Hour - time.Second).Format(time.RFC3339),
	}, nil), "a memory for today")

	if body := page(t, port, cookie); !strings.Contains(body, "On this day") || !strings.Contains(body, "Turn on") {
		t.Fatalf("the card is missing: %.300s", body)
	}
	if res := post(t, port, "/me/memories", cookie, url.Values{"action": {"on"}, "at": {"00:00"}, "tz": {"UTC"}, "email": {"1"}, "pictures": {"1"}}); res.StatusCode != 204 {
		t.Fatalf("turning it on: HTTP %d", res.StatusCode)
	}

	// the key: named for it, with exactly two permissions
	var keys []struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
	}
	call(t, "GET", "/api-keys", session, nil, &keys)
	found := false
	for _, k := range keys {
		if k.Name == "handoff: memories" {
			found = true
			if len(k.Permissions) != 2 || !(contains(k.Permissions, "memory.read") && contains(k.Permissions, "asset.view")) {
				t.Errorf("the key can read memories and look at pictures and nothing else: %v", k.Permissions)
			}
		}
	}
	if !found {
		t.Fatalf("the app's key is in bob's Immich: %v", keys)
	}

	// the message: in his mail, with the picture inside it and the links
	eventually(t, "the mail arrives", 90*time.Second, func() bool { return len(sink.to("bob@e2e.test")) >= 1 })
	text := sink.to("bob@e2e.test")[0]
	day := now.Format("2006-01-02")
	for _, want := range []string{"3 years ago", "http://photos.test/memory?id=", "http://handoff.test/memories/" + day} {
		if !strings.Contains(text, want) {
			t.Errorf("the mail lacks %q: %q", want, text)
		}
	}
	sink.mu.Lock()
	raw := sink.mails[len(sink.mails)-1].data
	sink.mu.Unlock()
	if !strings.Contains(raw, "multipart/related") || !strings.Contains(strings.ToLower(raw), "content-id: <memory-1@handoff>") {
		t.Errorf("the thumbnail is inside the mail")
	}

	// the page the link leads to shows the photo, as bob's own Immich has it, and no other
	if code, body := getBody(t, port, "/memories/"+day, cookie); code != 200 || !strings.Contains(body, "memories/"+day+"/photo/"+id) || !strings.Contains(body, "3 years ago") {
		t.Fatalf("the page of the day: %d %.300s", code, body)
	}
	if code, _ := getBody(t, port, "/memories/"+day+"/photo/"+id+"?size=large", cookie); code != 200 {
		t.Errorf("the picture: %d", code)
	}
	if code, _ := getBody(t, port, "/memories/"+day+"/photo/00000000-0000-4000-8000-000000000000", cookie); code != 404 {
		t.Errorf("a photo that is in no memory: %d", code)
	}

	// it is sent once a day: the pass looks again every few seconds and finds the day done
	time.Sleep(15 * time.Second)
	if n := len(sink.to("bob@e2e.test")); n != 1 {
		t.Errorf("one mail a day, got %d", n)
	}

	// turning it off deletes the key
	if res := post(t, port, "/me/memories", cookie, url.Values{"action": {"off"}}); res.StatusCode != 204 {
		t.Fatalf("turning it off: HTTP %d", res.StatusCode)
	}
	if hasName(keyNames(t, bob), "handoff: memories") {
		t.Error("the key is gone from Immich")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
