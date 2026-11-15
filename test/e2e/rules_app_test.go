//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestRules: a sender makes a rule for the people in their library, photos of that person that reach Immich afterwards are
// suggested (and one with a person left out is not), the sender sends them, and they reach the receiver through the normal
// hand-over; "Not these" and a photo's reason are checked on the way. Faces are made by hand (the test Immich has no machine
// learning), see TestRulesImmichRules.
func TestRules(t *testing.T) {
	alice, bob, _, _ := bootstrap(t) // alice owns the service, bob sends
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 70)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir(), "RULES_CHECK_MINUTES=0.1", "RULES_SETTLE_MINUTES=0.02")
	owner, sender := loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test")
	form := func(path, cookie string, v url.Values) string {
		t.Helper()
		code, body := postBody(t, port, path, cookie, v)
		if code != 204 {
			t.Fatalf("%s: HTTP %d %.300s", path, code, body)
		}
		return body
	}
	form("/services", owner, url.Values{"name": {"people"}, "tag": {"people-tag"}, "policy": {"open"}})
	form("/join", sender, url.Values{"service": {"people"}, "mode": {"copy"}})

	// Bob's library: two people Immich knows (named by hand), and photos with faces
	var nora, leo struct {
		ID string `json:"id"`
	}
	mustOK(t, call(t, "POST", "/people", keyAuth(bob), map[string]string{"name": "Nora-rule"}, &nora), "person")
	mustOK(t, call(t, "POST", "/people", keyAuth(bob), map[string]string{"name": "Leo-rule"}, &leo), "person")
	face := func(asset, person string) {
		t.Helper()
		mustOK(t, call(t, "POST", "/faces", keyAuth(bob), map[string]any{"assetId": asset, "personId": person, "imageWidth": 64, "imageHeight": 64, "x": 10, "y": 10, "width": 30, "height": 30}, nil), "face")
	}

	// Immich lists a person once it has a face: two older photos give each of them one (the rule never looks at them)
	seedNora, _ := upload(t, bob, "seed-nora.jpg", jpegBytes(40010))
	seedLeo, _ := upload(t, bob, "seed-leo.jpg", jpegBytes(40011))
	face(seedNora, nora.ID)
	face(seedLeo, leo.ID)

	// the rule: photos of Nora, but not with Leo
	body := page(t, port, sender)
	if !strings.Contains(body, "Make a rule") {
		t.Fatalf("the service's card offers rules: %.300s", body)
	}
	if res, _ := postBody(t, port, "/rules", sender, url.Values{"service": {"people"}}); res != 409 {
		t.Errorf("a rule with nothing to look for is refused: %d", res)
	}
	form("/rules", sender, url.Values{"service": {"people"}, "person": {nora.ID}, "notperson": {leo.ID}})
	if p := page(t, port, sender); !strings.Contains(p, "photos of Nora-rule without Leo-rule") || !strings.Contains(p, "Asks you first") {
		t.Fatalf("the rule is shown as a sentence: %.500s", p)
	}

	// photos reach Immich after the rule: one of Nora, one of Nora and Leo, one of nobody
	a, _ := upload(t, bob, "rule-a.jpg", jpegBytes(40001))
	b, _ := upload(t, bob, "rule-b.jpg", jpegBytes(40002))
	upload(t, bob, "rule-c.jpg", jpegBytes(40003))
	face(a, nora.ID)
	face(b, nora.ID)
	face(b, leo.ID)

	cardRe := regexp.MustCompile(`rules/photo/([0-9a-f-]{36})/([0-9a-f-]{36})`)
	var ruleID string
	eventually(t, "the suggestion appears under Needs you", 120*time.Second, func() bool {
		p := page(t, port, sender)
		m := cardRe.FindStringSubmatch(p)
		if m == nil || !strings.Contains(p, "Found by your rule") {
			return false
		}
		ruleID = m[1]
		return true
	})
	p := page(t, port, sender)
	assets := map[string]bool{}
	for _, m := range cardRe.FindAllStringSubmatch(p, -1) {
		assets[m[2]] = true
	}
	if len(assets) != 1 || !assets[a] {
		t.Errorf("only the photo of Nora alone is suggested (the one with Leo is not): %v", assets)
	}
	if res := getStatus(t, port, "/rules/photo/"+ruleID+"/"+a, sender); res != 200 {
		t.Errorf("the picture comes through the person's own sign-in: %d", res)
	}
	if res := getStatus(t, port, "/rules/photo/"+ruleID+"/"+a, owner); res != 404 {
		t.Errorf("not for anyone else: %d", res)
	}

	// Send: the photo reaches the receiver through the normal hand-over, and is not suggested again
	before := len(searchTag(t, alice, "inbox/people", false))
	form("/rules/answer", sender, url.Values{"rule": {ruleID}, "do": {"send"}})
	eventually(t, "the photo reached the owner", 120*time.Second, func() bool {
		return len(searchTag(t, alice, "inbox/people", false)) == before+1
	})
	time.Sleep(15 * time.Second) // a few looks later
	if p := page(t, port, sender); cardRe.MatchString(p) {
		t.Errorf("a photo that was sent is not suggested again")
	}

	// Not these: a later photo is declined for good
	c, _ := upload(t, bob, "rule-d.jpg", jpegBytes(40004))
	face(c, nora.ID)
	eventually(t, "a new suggestion", 120*time.Second, func() bool { return cardRe.MatchString(page(t, port, sender)) })
	form("/rules/answer", sender, url.Values{"rule": {ruleID}, "do": {"decline"}})
	time.Sleep(15 * time.Second)
	if p := page(t, port, sender); cardRe.MatchString(p) {
		t.Errorf("a declined photo is not suggested again")
	}
	if n := len(searchTag(t, alice, "inbox/people", false)); n != before+1 {
		t.Errorf("declining hands nothing over: %d", n)
	}

	// ending the rule
	form("/rules/delete", sender, url.Values{"rule": {ruleID}})
	if p := page(t, port, sender); strings.Contains(p, "Asks you first") {
		t.Errorf("the rule is gone")
	}
}

// getStatus asks the app for a page as a person and says only the status.
func getStatus(t *testing.T, port, path, cookie string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://localhost:"+port+path, nil)
	req.Header.Set("Cookie", cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}
