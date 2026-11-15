//go:build e2e

package e2e

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestSharedAlbumsTwoServers is the whole of #131 on two real Immich servers and two apps: two people pair their apps, share an
// album, photos cross both ways once and never come back, taking a photo out follows under one rule and not under the other, and
// ending the sharing deletes the key the app made. Then the one-app way: a third person with an Immich and no app, with the small
// key and with the bigger one, and a key that stops working.
func TestSharedAlbumsTwoServers(t *testing.T) {
	alice, _, _, _ := bootstrap(t)
	startFarImmich(t)
	lia := farBootstrap(t)
	bin := buildApp(t)

	portA, portB := fmt.Sprint(atoi(appPort)+60), fmt.Sprint(atoi(appPort)+61)
	common := []string{"HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "BRIDGE_ENABLE=true", "BRIDGE_ALLOW_PRIVATE=true", "SHARED_ALBUMS_ENABLE=true", "SHARED_ALBUMS_EVERY_MINUTES=60"}
	launchApp(t, bin, portA, append(common, "DATA_DIR="+t.TempDir(), "PUBLIC_APP_URL=http://localhost:"+portA)...)
	launchApp(t, bin, portB, append(common, "DATA_DIR="+t.TempDir(), "PUBLIC_APP_URL=http://localhost:"+portB, "IMMICH_BASE_URL="+farURL())...)

	near := side{name: "near", base: baseURL, token: alice.token, key: alice.key}
	far := side{name: "far", base: farURL(), token: lia.token, key: lia.key}
	cookieA := loginCookie(t, "alice@e2e.test")
	cookieB := farLoginCookie(t, "lia@far.test")
	form := func(port, path, cookie string, v url.Values) {
		t.Helper()
		code, body := postBody(t, port, path, cookie, v)
		if code != 204 {
			t.Fatalf("%s: HTTP %d %.300s", path, code, body)
		}
	}

	newAlbum := func(s side, name string) string {
		t.Helper()
		var out struct {
			ID string `json:"id"`
		}
		if code := s.api(t, "POST", "/albums", s.key, map[string]any{"albumName": name}, &out); code >= 300 || out.ID == "" {
			t.Fatalf("make album %s: HTTP %d", name, code)
		}
		return out.ID
	}
	addPhoto := func(s side, album string, seed int) string {
		t.Helper()
		code, id, _ := s.put(t, s.key, fmt.Sprintf("p%d.jpg", seed), strings.NewReader(string(jpegBytes(seed))), time.Now())
		if code >= 300 {
			t.Fatalf("upload: HTTP %d", code)
		}
		if c := s.api(t, "PUT", "/albums/"+album+"/assets", s.key, map[string]any{"ids": []string{id}}, nil); c >= 300 {
			t.Fatalf("add to album: HTTP %d", c)
		}
		return id
	}
	sums := func(s side, album string) map[string]bool {
		list, _ := s.album(t, s.key, album)
		out := map[string]bool{}
		for _, x := range list {
			out[x.Checksum] = true
		}
		return out
	}
	waitSums := func(what string, s side, album string, want ...int) {
		t.Helper()
		eventually(t, what, 90*time.Second, func() bool {
			got := sums(s, album)
			if len(got) != len(want) {
				return false
			}
			for _, seed := range want {
				if !got[sumOf(jpegBytes(seed))] {
					return false
				}
			}
			return true
		})
	}
	farAlbumNamed := func(name string) string {
		var list []struct{ ID, AlbumName string }
		far.api(t, "GET", "/albums?isOwned=true", far.key, nil, &list) // owned by lia
		for _, a := range list {
			if a.AlbumName == name {
				return a.ID
			}
		}
		return ""
	}
	sharedIDs := func(port, cookie string) []string {
		var ids []string
		for _, m := range regexp.MustCompile(`(?:shared&#34;:&#34;|name="shared" value=")([0-9a-f-]{36})`).FindAllStringSubmatch(page(t, port, cookie), -1) {
			dup := false
			for _, x := range ids {
				dup = dup || x == m[1]
			}
			if !dup {
				ids = append(ids, m[1])
			}
		}
		return ids
	}
	newShared := func(port, cookie string, seen []string) string {
		t.Helper()
		for _, id := range sharedIDs(port, cookie) {
			known := false
			for _, s := range seen {
				known = known || s == id
			}
			if !known {
				return id
			}
		}
		t.Fatal("no new shared album on the page")
		return ""
	}
	syncNow := func(port, cookie, id string) {
		t.Helper()
		form(port, "/shared/sync", cookie, url.Values{"shared": {id}})
		time.Sleep(time.Second)
	}

	// --- two apps pair through an invitation ---
	_, invite := postBody(t, portA, "/peers/invite", cookieA, nil)
	m := regexp.MustCompile(`value="(http[^"]+/peer/invite/[0-9a-f]{64})"`).FindStringSubmatch(invite)
	if m == nil {
		t.Fatalf("no invitation link: %.300s", invite)
	}
	form(portB, "/peers/accept", cookieB, url.Values{"link": {m[1]}})
	peerRe := regexp.MustCompile(`peer&#34;:&#34;([0-9a-f-]{36})`)
	pm := peerRe.FindStringSubmatch(page(t, portA, cookieA))
	if pm == nil || !strings.Contains(page(t, portB, cookieB), "Connected") {
		t.Fatal("the two apps are connected")
	}

	// --- an album under the rule "follow": photos cross both ways, once ---
	trip := newAlbum(near, "Trip")
	addPhoto(near, trip, 21001)
	addPhoto(near, trip, 21002)
	form(portA, "/peers/share", cookieA, url.Values{"peer": {pm[1]}, "name": {"Trip"}, "album": {trip}, "history": {"all"}, "deletion": {"follow"}})
	idB := newShared(portB, cookieB, nil)
	form(portB, "/shared/accept", cookieB, url.Values{"shared": {idB}, "album": {"new"}, "history": {"new"}})
	idA := newShared(portA, cookieA, nil)
	syncNow(portA, cookieA, idA)
	farTrip := farAlbumNamed("Trip")
	if farTrip == "" {
		t.Fatal("the other person's album was made")
	}
	waitSums("both photos reached the far album", far, farTrip, 21001, 21002)

	addPhoto(far, farTrip, 21003)
	syncNow(portB, cookieB, idB)
	waitSums("her photo reached the near album", near, trip, 21001, 21002, 21003)
	syncNow(portA, cookieA, idA)
	syncNow(portB, cookieB, idB)
	time.Sleep(3 * time.Second)
	waitSums("nothing came back or doubled (near)", near, trip, 21001, 21002, 21003)
	waitSums("nothing came back or doubled (far)", far, farTrip, 21001, 21002, 21003)

	// --- taking a photo out follows under the rule, and the photo stays in the library ---
	list, _ := near.album(t, near.key, trip)
	var gone string
	for _, x := range list {
		if x.Checksum == sumOf(jpegBytes(21001)) {
			gone = x.ID
		}
	}
	if c := near.api(t, "DELETE", "/albums/"+trip+"/assets", near.key, map[string]any{"ids": []string{gone}}, nil); c >= 300 {
		t.Fatalf("take out: HTTP %d", c)
	}
	syncNow(portA, cookieA, idA)
	waitSums("the removal followed", far, farTrip, 21002, 21003)
	if sum := sums(far, farTrip); sum[sumOf(jpegBytes(21001))] {
		t.Error("it is out of the far album")
	}
	var inLibrary struct {
		Assets struct{ Items []listed } `json:"assets"`
	}
	far.api(t, "POST", "/search/metadata", far.key, map[string]any{"checksum": sumOf(jpegBytes(21001))}, &inLibrary)
	if len(inLibrary.Assets.Items) != 1 || inLibrary.Assets.Items[0].IsTrashed {
		t.Errorf("the photo itself stays in the far library: %+v", inLibrary.Assets.Items)
	}

	// --- under the rule "nothing" a removal does not travel ---
	quiet := newAlbum(near, "Quiet")
	addPhoto(near, quiet, 21101)
	addPhoto(near, quiet, 21102)
	form(portA, "/peers/share", cookieA, url.Values{"peer": {pm[1]}, "name": {"Quiet"}, "album": {quiet}, "history": {"all"}, "deletion": {"nothing"}})
	idB2 := newShared(portB, cookieB, []string{idB})
	form(portB, "/shared/accept", cookieB, url.Values{"shared": {idB2}, "album": {"new"}, "history": {"new"}})
	idA2 := newShared(portA, cookieA, []string{idA})
	syncNow(portA, cookieA, idA2)
	farQuiet := farAlbumNamed("Quiet")
	waitSums("the quiet album arrived", far, farQuiet, 21101, 21102)
	list, _ = near.album(t, near.key, quiet)
	near.api(t, "DELETE", "/albums/"+quiet+"/assets", near.key, map[string]any{"ids": []string{list[0].ID}}, nil)
	syncNow(portA, cookieA, idA2)
	time.Sleep(3 * time.Second)
	if n := len(sums(far, farQuiet)); n != 2 {
		t.Errorf("under the rule nothing, the far album keeps both photos: %d", n)
	}

	// --- ending the sharing from one side: the other app is told and the key the app made is deleted ---
	keysNamed := func(s side) (n int) {
		var ks []struct{ Name string }
		req, _ := http.NewRequest("GET", s.base+"/api/api-keys", nil)
		req.Header.Set("Authorization", "Bearer "+s.token)
		if res, err := http.DefaultClient.Do(req); err == nil {
			defer res.Body.Close()
			decodeJSON(res.Body, &ks)
		}
		for _, k := range ks {
			if strings.HasPrefix(k.Name, "handoff: shared album") {
				n++
			}
		}
		return
	}
	if keysNamed(far) != 2 {
		t.Errorf("the far person has one key per shared album: %d", keysNamed(far))
	}
	form(portB, "/shared/leave", cookieB, url.Values{"shared": {idB2}})
	if keysNamed(far) != 1 {
		t.Errorf("ending the sharing deleted the key (left: %d)", keysNamed(far))
	}
	eventually(t, "the near app shows it ended", 30*time.Second, func() bool { return strings.Contains(page(t, portA, cookieA), "Ended by") })
	addPhoto(near, quiet, 21103)
	if code, _ := postBody(t, portA, "/shared/sync", cookieA, url.Values{"shared": {idA2}}); code != 409 {
		t.Errorf("an ended sharing cannot be synced: HTTP %d", code)
	}
	time.Sleep(2 * time.Second)
	if n := len(sums(far, farQuiet)); n != 2 {
		t.Errorf("nothing moves after it ended: %d", n)
	}

	// --- the one-app way: a third person with an Immich and no app ---
	small := farKey(t, lia, "cousin-small", []string{"user.read", "asset.upload", "album.create", "albumAsset.create"})
	broad := farKey(t, lia, "cousin-broad", []string{"user.read", "asset.upload", "album.create", "albumAsset.create", "asset.delete"})
	two := farKey(t, lia, "cousin-two", []string{"user.read", "asset.upload", "album.create", "albumAsset.create", "asset.read", "asset.download", "album.read"})
	alone := newAlbum(near, "Alone")
	addPhoto(near, alone, 21201)
	share := func(name, key string) (int, string) {
		return postBody(t, portA, "/peers/share-immich", cookieA, url.Values{"person": {"Cousin"}, "address": {farURL()}, "key": {key}, "name": {name}, "album": {alone}, "history": {"all"}})
	}
	if code, body := share("Alone", broad); code != 409 || !strings.Contains(body, "can read your library") && !strings.Contains(body, "more than") {
		t.Errorf("a key with asset.delete is refused: %d %.200s", code, body)
	}
	if code, body := share("Alone", small); code != 204 {
		t.Fatalf("the small key: %d %.300s", code, body)
	}
	idA3 := newShared(portA, cookieA, []string{idA, idA2})
	syncNow(portA, cookieA, idA3)
	farAlone := farAlbumNamed("Alone")
	waitSums("the album was sent with the small key", far, farAlone, 21201)
	addPhoto(far, farAlone, 21202)
	syncNow(portA, cookieA, idA3)
	time.Sleep(3 * time.Second)
	if n := len(sums(near, alone)); n != 1 {
		t.Errorf("the small key brings nothing back: %d", n)
	}
	form(portA, "/shared/leave", cookieA, url.Values{"shared": {idA3}})

	// the bigger key: both ways
	if code, body := postBody(t, portA, "/peers/share-immich", cookieA, url.Values{"person": {"Cousin"}, "address": {farURL()}, "key": {two}, "name": {"Alone"}, "album": {alone}, "history": {"all"}}); code != 204 {
		t.Fatalf("the two-way key: %d %.300s", code, body)
	}
	idA4 := newShared(portA, cookieA, []string{idA, idA2, idA3})
	syncNow(portA, cookieA, idA4)
	waitSums("what they had in that album came here, and what was here went there", near, alone, 21201, 21202)
	waitSums("both ways (far)", far, farAlone, 21201, 21202)
	// a key that stops working is said plainly
	keyID := ""
	var keys []struct{ ID, Name string }
	req, _ := http.NewRequest("GET", farURL()+"/api/api-keys", nil)
	req.Header.Set("Authorization", "Bearer "+lia.token)
	if res, err := http.DefaultClient.Do(req); err == nil {
		decodeJSON(res.Body, &keys)
		res.Body.Close()
	}
	for _, k := range keys {
		if strings.HasPrefix(k.Name, "cousin-two") {
			keyID = k.ID
		}
	}
	if keyID == "" {
		t.Fatal("the cousin's key is in their list")
	}
	delReq, _ := http.NewRequest("DELETE", farURL()+"/api/api-keys/"+keyID, nil)
	delReq.Header.Set("Authorization", "Bearer "+lia.token)
	if res, err := http.DefaultClient.Do(delReq); err != nil || res.StatusCode >= 300 {
		t.Fatal("delete the cousin's key")
	}
	addPhoto(near, alone, 21203)
	syncNow(portA, cookieA, idA4)
	eventually(t, "the refused key is said on the page", 30*time.Second, func() bool {
		return strings.Contains(page(t, portA, cookieA), "no longer accepts the key")
	})
}

func decodeJSON(r io.Reader, into any) { json.NewDecoder(r).Decode(into) }

func sumOf(b []byte) string {
	s := sha1.Sum(b)
	return base64.StdEncoding.EncodeToString(s[:])
}

// farLoginCookie signs a person in on the second Immich, as their browser would.
func farLoginCookie(t *testing.T, email string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": "Passw0rd!x"})
	res, err := http.Post(farURL()+"/api/auth/login", "application/json", bytes.NewReader(body))
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
