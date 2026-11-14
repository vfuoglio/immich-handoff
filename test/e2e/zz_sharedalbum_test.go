//go:build e2e

// What a shared album across servers (#131) relies on in Immich, asked of two real servers (docs/shared-albums-design.md
// has the findings). It started as the spike of #141 and stays as the check that Immich still behaves this way: the
// permissions a key needs, the checksum as a photo's identity across servers, a duplicate upload as the answer to an echo,
// and a trashed copy that a re-send leaves alone. It starts the second Immich, so it runs in the bridge group.
package e2e

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

type side struct {
	name, base string
	token, key string
}

func (s side) api(t *testing.T, method, path string, key string, body any, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.base+"/api"+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if out != nil && len(b) > 0 {
		json.Unmarshal(b, out)
	}
	return res.StatusCode
}

func (s side) makeKey(t *testing.T, perms []string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"name": fmt.Sprintf("spike-%d", time.Now().UnixNano()), "permissions": perms})
	req, _ := http.NewRequest("POST", s.base+"/api/api-keys", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var k struct{ Secret string }
	json.NewDecoder(res.Body).Decode(&k)
	if k.Secret == "" {
		t.Fatalf("a key with %v was not made: HTTP %d", perms, res.StatusCode)
	}
	return k.Secret
}

// put uploads a stream the way the app does (multipart through a pipe, nothing on disk) and reports the status and the id.
func (s side) put(t *testing.T, key, name string, r io.Reader, at time.Time) (status int, id, state string) {
	t.Helper()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		mw.WriteField("fileCreatedAt", at.UTC().Format(time.RFC3339Nano))
		mw.WriteField("fileModifiedAt", at.UTC().Format(time.RFC3339Nano))
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="assetData"; filename=%q`, name))
		h.Set("Content-Type", "application/octet-stream")
		part, _ := mw.CreatePart(h)
		_, err := io.Copy(part, r)
		mw.Close()
		pw.CloseWithError(err)
	}()
	req, _ := http.NewRequest("POST", s.base+"/api/assets", pr)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("x-api-key", key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct{ ID, Status string }
	b, _ := io.ReadAll(res.Body)
	json.Unmarshal(b, &out)
	return res.StatusCode, out.ID, out.Status
}

func (s side) original(t *testing.T, key, id string) (*http.Response, int) {
	t.Helper()
	req, _ := http.NewRequest("GET", s.base+"/api/assets/"+id+"/original", nil)
	req.Header.Set("x-api-key", key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res, res.StatusCode
}

type listed struct {
	ID               string `json:"id"`
	Checksum         string `json:"checksum"`
	OriginalFileName string `json:"originalFileName"`
	FileCreatedAt    string `json:"fileCreatedAt"`
	Type             string `json:"type"`
	DeviceAssetID    string `json:"deviceAssetId"`
	LibraryID        string `json:"libraryId"`
	IsTrashed        bool   `json:"isTrashed"`
	OwnerID          string `json:"ownerId"`
}

func (s side) album(t *testing.T, key, album string) ([]listed, int) {
	t.Helper()
	var res struct {
		Assets struct{ Items []listed } `json:"assets"`
	}
	code := s.api(t, "POST", "/search/metadata", key, map[string]any{"albumIds": []string{album}, "size": 1000}, &res)
	return res.Assets.Items, code
}

func TestSharedAlbumImmichRules(t *testing.T) {
	alice, _, _, _ := bootstrap(t)
	startFarImmich(t)
	lia := farBootstrap(t)
	a := side{name: "A (near)", base: baseURL, token: alice.token, key: alice.key}
	b := side{name: "B (far)", base: farURL(), token: lia.token, key: lia.key}
	finding := func(f string, args ...any) { t.Logf("FINDING: "+f, args...) }

	// 1. the key each side makes for its own app, with the permissions the design wants
	want := []string{"user.read", "asset.read", "asset.download", "asset.upload", "album.read", "album.create", "albumAsset.create", "albumAsset.delete"}
	ka, kb := a.makeKey(t, want), b.makeKey(t, want)
	finding("a key with %v is accepted by Immich on both servers", want)

	// 2. A has an album with photos; B has an empty album of its own
	var ids []string
	var sums []string
	for i := 0; i < 3; i++ {
		data := noiseJPEG(uint32(time.Now().UnixNano())+uint32(i), 40, 220)
		sum := sha1.Sum(data)
		sums = append(sums, base64.StdEncoding.EncodeToString(sum[:]))
		st, id, state := a.put(t, ka, fmt.Sprintf("spike-%d.jpg", i), bytes.NewReader(data), time.Date(2026, 6, 14, 15, i, 0, 0, time.UTC))
		if id == "" || state != "created" {
			t.Fatalf("upload to A: %d %s", st, state)
		}
		ids = append(ids, id)
	}
	var albA, albB struct{ ID string }
	if c := a.api(t, "POST", "/albums", ka, map[string]any{"albumName": "Shared A", "assetIds": ids}, &albA); c != 201 {
		t.Fatalf("make the album on A with the app's key: %d", c)
	}
	if c := b.api(t, "POST", "/albums", kb, map[string]any{"albumName": "Shared B"}, &albB); c != 201 {
		t.Fatalf("make the album on B with the app's key: %d", c)
	}
	finding("each app can make an album on its own server with album.create alone (no asset.read needed to make it)")
	list, code := a.album(t, ka, albA.ID)
	if code != 200 || len(list) != 3 {
		t.Fatalf("list A's album: %d %d", code, len(list))
	}
	for _, x := range list {
		if x.Checksum == "" {
			t.Fatal("the album listing carries no checksum")
		}
	}
	finding("listing an album (search with albumIds) answers each photo's checksum, so what is new can be told without downloading anything")
	for i, x := range list {
		found := false
		for _, s := range sums {
			found = found || x.Checksum == s
		}
		if !found {
			t.Errorf("photo %d: the checksum Immich lists is not the SHA-1 of the file the app sent", i)
		}
	}
	finding("the checksum Immich lists is the base64 SHA-1 of the uploaded bytes (so the same file on two servers has the same checksum)")

	// 3. stream A -> B with nothing on disk, and put it in B's album
	type moved struct{ srcID, dstID string }
	var arrived []moved
	for _, x := range list {
		res, code := a.original(t, ka, x.ID)
		if code != 200 {
			t.Fatalf("download from A with asset.download: %d", code)
		}
		at, _ := time.Parse(time.RFC3339Nano, x.FileCreatedAt)
		st, id, state := b.put(t, kb, x.OriginalFileName, res.Body, at)
		res.Body.Close()
		if id == "" || state != "created" || st != 201 {
			t.Fatalf("upload to B: %d %s", st, state)
		}
		arrived = append(arrived, moved{x.ID, id})
	}
	finding("an original streams from one server into the other through a pipe (download body to upload body, no file on disk) and is created (201)")
	var bids []string
	for _, m := range arrived {
		bids = append(bids, m.dstID)
	}
	if c := b.api(t, "PUT", "/albums/"+albB.ID+"/assets", kb, map[string]any{"ids": bids}, nil); c != 200 {
		t.Fatalf("add to B's album with albumAsset.create: %d", c)
	}
	got, _ := b.album(t, kb, albB.ID)
	if len(got) != 3 {
		t.Fatalf("B's album after the add: %d", len(got))
	}
	for i := range got {
		match := false
		for _, s := range sums {
			match = match || got[i].Checksum == s
		}
		if !match {
			t.Error("B's copy has another checksum than A's original")
		}
	}
	finding("the copy on B has the same checksum as the original on A, and the album on B lists it: the checksum is the identity of a photo across servers")

	// 4. the echo: send B's copies back to A, as a naive two-way sync would
	for _, m := range arrived {
		res, code := b.original(t, kb, m.dstID)
		if code != 200 {
			t.Fatalf("download from B: %d", code)
		}
		st, id, state := a.put(t, ka, "echo.jpg", res.Body, time.Now())
		res.Body.Close()
		if st != 200 || state != "duplicate" || id != m.srcID {
			t.Errorf("an echo to A: %d %s %s (want 200 duplicate %s)", st, state, id, m.srcID)
		}
	}
	finding("sending B's copy back to A answers 200 duplicate with A's own asset id: an echo creates nothing, and the id needed to put it in an album is known")

	// 5. what a key without one permission cannot do (which are really needed)
	for _, drop := range want {
		var keep []string
		for _, p := range want {
			if p != drop {
				keep = append(keep, p)
			}
		}
		k := a.makeKey(t, keep)
		var result []string
		if _, c := a.album(t, k, albA.ID); c == 200 {
			result = append(result, "lists")
		} else {
			result = append(result, fmt.Sprintf("list=%d", c))
		}
		if res, c := a.original(t, k, ids[0]); c == 200 {
			res.Body.Close()
			result = append(result, "downloads")
		} else {
			res.Body.Close()
			result = append(result, fmt.Sprintf("download=%d", c))
		}
		c := a.api(t, "PUT", "/albums/"+albA.ID+"/assets", k, map[string]any{"ids": []string{ids[0]}}, nil)
		result = append(result, fmt.Sprintf("album-add=%d", c))
		c = a.api(t, "GET", "/albums/"+albA.ID, k, nil, nil)
		result = append(result, fmt.Sprintf("album-get=%d", c))
		finding("without %-18s: %s", drop, strings.Join(result, ", "))
		needed := map[string]string{"asset.read": "list=403", "asset.download": "download=403", "albumAsset.create": "album-add=403", "album.read": "album-get=403"}
		if n, ok := needed[drop]; ok && !strings.Contains(strings.Join(result, ", "), n) {
			t.Errorf("a key without %s should fail with %s: %v", drop, n, result)
		}
		if _, ok := needed[drop]; !ok && strings.Contains(strings.Join(result, ", "), "=403") {
			t.Errorf("%s is not needed for listing, downloading, adding or reading an album: %v", drop, result)
		}
	}

	// 6. removal from an album, and what a key can see of the rest of the library
	if c := a.api(t, "DELETE", "/albums/"+albA.ID+"/assets", ka, map[string]any{"ids": []string{ids[2]}}, nil); c != 200 {
		t.Errorf("remove from an album with albumAsset.delete: %d", c)
	}
	after, _ := a.album(t, ka, albA.ID)
	finding("removing a photo from an album needs albumAsset.delete; the listing then shows %d of 3 (the photo itself stays in the library)", len(after))
	var all struct {
		Assets struct{ Items []listed } `json:"assets"`
	}
	a.api(t, "POST", "/search/metadata", ka, map[string]any{"size": 1000}, &all)
	finding("the same key can list the whole library without an album filter (%d photos): the app, not the key, limits it to one album", len(all.Assets.Items))
	var meA, meB struct{ ID, Email string }
	a.api(t, "GET", "/users/me", ka, nil, &meA)
	b.api(t, "GET", "/users/me", kb, nil, &meB)
	finding("each key says whose it is (user.read): %s and %s", meA.Email, meB.Email)

	// 7. how fast the album can be read
	t0 := time.Now()
	for i := 0; i < 5; i++ {
		a.album(t, ka, albA.ID)
	}
	finding("reading a three-photo album takes about %v (five reads)", time.Since(t0)/5)

	// 8. what a copy that the other person trashed does when it is sent again (their deleting must not be undone silently)
	victim := arrived[0]
	if c := b.api(t, "DELETE", "/assets", b.key, map[string]any{"ids": []string{victim.dstID}, "force": false}, nil); c != 204 && c != 200 {
		t.Fatalf("trash on B: %d", c)
	}
	res, code2 := a.original(t, ka, victim.srcID)
	if code2 != 200 {
		t.Fatal(code2)
	}
	st, id, state := b.put(t, kb, "again.jpg", res.Body, time.Now())
	res.Body.Close()
	var now struct {
		IsTrashed bool `json:"isTrashed"`
		ID        string
	}
	b.api(t, "GET", "/assets/"+id, b.key, nil, &now)
	if st != 200 || state != "duplicate" || id != victim.dstID || !now.IsTrashed {
		t.Errorf("a re-send of a trashed copy: %d %s %v %v", st, state, id == victim.dstID, now.IsTrashed)
	}
	finding("sending a photo again that the other person put in their trash: HTTP %d %s, answered id is the trashed one (%v), still in the trash (%v): a re-send neither restores nor duplicates it, so the person's deleting is respected", st, state, id == victim.dstID, now.IsTrashed)
}
