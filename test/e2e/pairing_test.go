//go:build e2e

package e2e

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestPairing: two people share photos of named people both ways (docs/design/pairing-design.md). Alice offers Nora, Bob says yes
// and sends Leo back. A photo of Nora in Alice's library reaches Bob's, a photo of Leo in Bob's reaches Alice's, a photo of
// someone else moves nowhere, the pieces cannot be changed by hand, Bob's automatic switch sends only what arrives after it, and
// stopping leaves every copy where it is. Faces are made by hand (the test Immich has no machine learning).
func TestPairing(t *testing.T) {
	alice, bob, _, _ := bootstrap(t)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 72)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir(), "RULES_CHECK_MINUTES=0.1", "RULES_SETTLE_MINUTES=0.02")
	owner, other := loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test")
	form := func(path, cookie string, v url.Values) string {
		t.Helper()
		code, body := postBody(t, port, path, cookie, v)
		if code != 204 {
			t.Fatalf("%s: HTTP %d %.300s", path, code, body)
		}
		return body
	}
	person := func(u user, name string, seed int) string {
		t.Helper()
		var p struct {
			ID string `json:"id"`
		}
		mustOK(t, call(t, "POST", "/people", keyAuth(u), map[string]string{"name": name}, &p), "person")
		asset, _ := upload(t, u, "seed-"+name+".jpg", jpegBytes(seed))
		pairFace(t, u, asset, p.ID)
		return p.ID
	}
	nora := person(alice, "Nora-pair", 72001)
	zed := person(alice, "Zed-pair", 72002)
	leo := person(bob, "Leo-pair", 72003)

	// the offer, the link, the yes
	form("/pairings", owner, url.Values{"email": {"bob@e2e.test"}, "person": {nora}})
	idRe := regexp.MustCompile(`pair/([0-9a-f-]{36})`)
	m := idRe.FindStringSubmatch(page(t, port, owner))
	if m == nil {
		t.Fatal("the starter's card has the link")
	}
	pairing := m[1]
	if code, b := postBody(t, port, "/pairings/accept", owner, url.Values{"pairing": {pairing}, "person": {leo}}); code == 204 {
		t.Errorf("the starter cannot say yes for the other person: %s", b)
	}
	if code, _ := postBody(t, port, "/pairings/accept", other, url.Values{"pairing": {pairing}, "person": {nora}}); code == 204 {
		t.Error("the other person can send back only people of their own library")
	}
	form("/pairings/accept", other, url.Values{"pairing": {pairing}, "person": {leo}})
	for who, want := range map[string]string{owner: "bob sends photos of Leo-pair.", other: "alice sends photos of Nora-pair."} {
		if p := page(t, port, who); !strings.Contains(p, want) || regexp.MustCompile(`pair-[0-9a-f]{8}`).MatchString(p) {
			t.Errorf("each card says who sends what, and never the pool's generated name (%q)", want)
		}
	}

	// a photo of Nora (and one of Zed) in Alice's library: only Nora's is offered, and a yes sends it to Bob
	cardRe := regexp.MustCompile(`rules/photo/([0-9a-f-]{36})/([0-9a-f-]{36})`)
	a1, _ := upload(t, alice, "pair-a1.jpg", jpegBytes(72011))
	z1, _ := upload(t, alice, "pair-z1.jpg", jpegBytes(72012))
	pairFace(t, alice, a1, nora)
	pairFace(t, alice, z1, zed)
	var aliceRule string
	eventually(t, "Alice's suggestion", 120*time.Second, func() bool {
		mm := cardRe.FindStringSubmatch(page(t, port, owner))
		if mm == nil {
			return false
		}
		aliceRule = mm[1]
		return true
	})
	if got := cardRe.FindAllStringSubmatch(page(t, port, owner), -1); len(got) != 1 || got[0][2] != a1 {
		t.Errorf("only the photo of Nora is offered, not the one of Zed: %v", got)
	}
	atBob := len(searchTag(t, bob, "from/alice", false))
	form("/rules/answer", owner, url.Values{"rule": {aliceRule}, "do": {"send"}})
	eventually(t, "the photo reached Bob", 120*time.Second, func() bool { return len(searchTag(t, bob, "from/alice", false)) == atBob+1 })

	// the pieces cannot be changed by hand
	if code, _ := postBody(t, port, "/rules/delete", owner, url.Values{"rule": {aliceRule}}); code == 204 {
		t.Error("a rule of a sharing cannot be deleted by hand")
	}
	if code, _ := postBody(t, port, "/rules/pause", owner, url.Values{"rule": {aliceRule}, "paused": {"1"}}); code == 204 {
		t.Error("nor paused")
	}

	// the other way, with Bob's switch: a photo that was waiting stays a suggestion, one that arrives afterwards is sent by itself
	l1, _ := upload(t, bob, "pair-l1.jpg", jpegBytes(72021))
	pairFace(t, bob, l1, leo)
	eventually(t, "Bob's suggestion", 120*time.Second, func() bool { return cardRe.MatchString(page(t, port, other)) })
	form("/pairings/auto", other, url.Values{"pairing": {pairing}, "on": {"1"}})
	atAlice := len(searchTag(t, alice, "from/bob", false))
	l2, _ := upload(t, bob, "pair-l2.jpg", jpegBytes(72022))
	pairFace(t, bob, l2, leo)
	eventually(t, "the later photo went by itself", 180*time.Second, func() bool { return len(searchTag(t, alice, "from/bob", false)) == atAlice+1 })
	time.Sleep(20 * time.Second)
	if n := len(searchTag(t, alice, "from/bob", false)); n != atAlice+1 {
		t.Errorf("the photo that was waiting before the switch is still only a suggestion: %d copies", n-atAlice)
	}
	if !cardRe.MatchString(page(t, port, other)) {
		t.Error("and it is still offered")
	}

	// Stop: the starter takes it apart at once, every copy stays
	form("/pairings/stop", owner, url.Values{"pairing": {pairing}})
	if p := page(t, port, owner); strings.Contains(p, "You and bob") {
		t.Error("the sharing is gone from the starter's page")
	}
	if n := len(searchTag(t, bob, "from/alice", false)); n != atBob+1 {
		t.Errorf("Bob keeps what arrived: %d", n-atBob)
	}
	if n := len(searchTag(t, alice, "from/bob", false)); n != atAlice+1 {
		t.Errorf("Alice keeps what arrived: %d", n-atAlice)
	}
	if p := page(t, port, other); !strings.Contains(p, "Removed by its owner") {
		t.Error("Bob is left able to clean up")
	}
}

// pairFace gives a photo a face of a named person, by hand.
func pairFace(t *testing.T, u user, asset, person string) {
	t.Helper()
	mustOK(t, call(t, "POST", "/faces", keyAuth(u), map[string]any{"assetId": asset, "personId": person, "imageWidth": 64, "imageHeight": 64, "x": 10, "y": 10, "width": 30, "height": 30}, nil), "face")
}
