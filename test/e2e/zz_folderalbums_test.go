//go:build e2e

// Named to sort last, like the other zz_ tests: it leaves an external library, albums and photos in the shared Immich.
package e2e

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
)

// externalLibrary makes bob an external library over the mounted folder tree (test/external-photos, at /external in the
// container), scans it and waits until every file is an asset. It returns the library's id; asked twice it reuses it.
func externalLibrary(t *testing.T, bob user) string {
	return externalLibraryAt(t, bob, []string{"/external/photos", "/external/photos-old"}, 7)
}

// externalLibraryAt is externalLibrary for any import paths: it waits until at least n files are assets.
func externalLibraryAt(t *testing.T, bob user, paths []string, n int) string {
	t.Helper()
	var login struct {
		AccessToken string `json:"accessToken"`
	}
	mustOK(t, call(t, "POST", "/auth/login", nil, map[string]string{"email": "admin@e2e.test", "password": "Passw0rd!x"}, &login), "admin login")
	admin := map[string]string{"Authorization": "Bearer " + login.AccessToken}
	var libs []struct {
		ID          string   `json:"id"`
		OwnerID     string   `json:"ownerId"`
		ImportPaths []string `json:"importPaths"`
	}
	call(t, "GET", "/libraries", admin, nil, &libs)
	id := ""
	for _, l := range libs {
		if l.OwnerID == bob.id && strings.Join(l.ImportPaths, ",") == strings.Join(paths, ",") {
			id = l.ID
		}
	}
	if id == "" {
		var made struct {
			ID string `json:"id"`
		}
		mustOK(t, call(t, "POST", "/libraries", admin, map[string]any{"ownerId": bob.id, "name": "e2e " + paths[0], "importPaths": paths}, &made), "make the library")
		id = made.ID
		mustOK(t, call(t, "POST", "/libraries/"+id+"/scan", admin, nil, nil), "scan the library")
	}
	eventually(t, "the library's files are assets", 120*time.Second, func() bool {
		return len(searchPaths(t, bob, map[string]any{"libraryId": id})) >= n
	})
	return id
}

// searchPaths lists the original paths of the assets a metadata search finds for bob, every page, sorted.
func searchPaths(t *testing.T, u user, body map[string]any) []string {
	t.Helper()
	var out []string
	for page := 1; page < 1000; page++ {
		body["page"], body["size"] = page, 250
		var res struct {
			Assets struct {
				Items []struct {
					OriginalPath string `json:"originalPath"`
				} `json:"items"`
				NextPage *string `json:"nextPage"`
			} `json:"assets"`
		}
		call(t, "POST", "/search/metadata", keyAuth(u), body, &res)
		for _, a := range res.Assets.Items {
			out = append(out, a.OriginalPath)
		}
		if res.Assets.NextPage == nil || *res.Assets.NextPage == "" {
			break
		}
	}
	sort.Strings(out)
	return out
}

// TestFolderAlbumsSpike: what Immich stores for an external library, as the notes in docs/contributing/design/immich-api-notes.md say.
// It checks, on the real stack, the facts the folder albums are built on.
func TestFolderAlbumsSpike(t *testing.T) {
	_, bob, _, _ := bootstrap(t)
	lib := externalLibrary(t, bob)
	all := searchPaths(t, bob, map[string]any{"libraryId": lib})
	t.Logf("the library's assets carry these paths:\n  %s", strings.Join(all, "\n  "))
	for _, p := range all {
		if !strings.HasPrefix(p, "/external/photos") {
			t.Errorf("an asset path outside the import paths: %s", p)
		}
	}
	// the prefix search: a trailing slash keeps /external/photos apart from /external/photos-old
	both := searchPaths(t, bob, map[string]any{"originalPath": "/external/photos"})
	slash := searchPaths(t, bob, map[string]any{"originalPath": "/external/photos/"})
	t.Logf("originalPath=/external/photos: %d assets; with a trailing slash: %d", len(both), len(slash))
	// the dry run: the albums the finder would make from the real library, for the three ways of reading the folders
	api, err := immich.New(baseURL, immich.Auth{APIKey: bob.key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	me, err := api.Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var h hub.Hub
	dry := func(s hub.FolderSpec) hub.FolderPlan {
		plan, err := h.DryRunFolders(context.Background(), api, uuid.UUID(me.ID), s)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("dry run %+v:\n%s", s, plan)
		return plan
	}
	if plan := dry(hub.FolderSpec{Prefix: "/external/photos"}); len(plan.Albums) != 3 || plan.Albums[1].Name != "paris" || len(plan.Albums[1].Photos) != 3 || plan.Loose != 1 {
		t.Errorf("by the folder a photo is in: %+v", plan)
	}
	if plan := dry(hub.FolderSpec{Prefix: "/external/photos", Depth: 1}); len(plan.Albums) != 3 || plan.Albums[0].Name != "2025-02" || len(plan.Albums[0].Photos) != 3 {
		t.Errorf("by the first folder: %+v", plan)
	}
	if plan := dry(hub.FolderSpec{Prefix: "/external/photos", Naming: hub.NameParent}); len(plan.Albums) != 4 {
		t.Errorf("with the parent in the name the two Paris folders are two albums: %+v", plan)
	}
	if plan := dry(hub.FolderSpec{Prefix: "/external/photos-old"}); len(plan.Albums) != 1 || plan.Albums[0].Name != "2019" {
		t.Errorf("the folder next to it is its own: %+v", plan)
	}
	if plan, _ := h.DryRunFolders(context.Background(), api, uuid.UUID(me.ID), hub.FolderSpec{Prefix: "/external/PHOTOS"}); len(plan.Albums) != 0 {
		t.Errorf("the case matters: %+v", plan)
	}

	// the libraries the person's own key can list
	code := call(t, "GET", "/libraries", keyAuth(bob), nil, nil)
	t.Logf("GET /libraries with bob's key (all permissions): HTTP %d", code)
	var limited struct {
		Secret string `json:"secret"`
	}
	call(t, "POST", "/api-keys", map[string]string{"Authorization": "Bearer " + bob.token}, map[string]any{"name": "spike-" + fmt.Sprint(time.Now().UnixNano()), "permissions": []string{"library.read", "asset.read"}}, &limited)
	code = call(t, "GET", "/libraries", map[string]string{"x-api-key": limited.Secret}, nil, nil)
	t.Logf("GET /libraries with a key of library.read and asset.read: HTTP %d", code)
}

// TestFolderAlbums: a person makes albums from the folders of their external library on a real Immich. The form says what it
// finds, the rule offers the albums with their names, nothing is made before the answer, Send makes them (an album of that name
// the person already has is used, not made again), the photo directly in the folder is left alone, and stopping the rule
// deletes the key the app made.
func TestFolderAlbums(t *testing.T) {
	_, bob, _, _ := bootstrap(t)
	externalLibrary(t, bob)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 95)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir(), "RULES_CHECK_MINUTES=0.1", "RULES_SETTLE_MINUTES=0.02")
	cookie := loginCookie(t, "bob@e2e.test")
	form := func(path string, v url.Values) {
		t.Helper()
		if code, body := postBody(t, port, path, cookie, v); code != 204 {
			t.Fatalf("%s: HTTP %d %.300s", path, code, body)
		}
	}
	albums := func() map[string]int {
		var list []struct {
			Name       string `json:"albumName"`
			AssetCount int    `json:"assetCount"`
		}
		call(t, "GET", "/albums", keyAuth(bob), nil, &list)
		out := map[string]int{}
		for _, a := range list {
			out[a.Name] += a.AssetCount
		}
		return out
	}

	// an album of that name that bob already has
	var rome struct {
		ID string `json:"id"`
	}
	mustOK(t, call(t, "POST", "/albums", keyAuth(bob), map[string]any{"albumName": "rome"}, &rome), "bob's own album named rome")

	// the form says what it finds as the folder is typed
	code, body := getBody(t, port, "/fragments/folder-check?folder="+url.QueryEscape("/external/photos")+"&level=0&naming=last", cookie)
	if code != 200 || !strings.Contains(body, "Found 6 of your photos") || !strings.Contains(body, "making 3 albums") || !strings.Contains(body, "paris (3 photos)") {
		t.Fatalf("the form's check: %d %.400s", code, body)
	}
	if _, body := getBody(t, port, "/fragments/folder-check?folder="+url.QueryEscape("/external/photos-old")+"&level=0&naming=last", cookie); !strings.Contains(body, "Found 1 of your photos") {
		t.Errorf("the folder next to it is its own: %.300s", body)
	}

	form("/rules/folder", url.Values{"folder": {"/external/photos"}, "level": {"0"}, "naming": {"last"}})
	if !hasName(keyNames(t, bob), "handoff: folders") {
		t.Fatalf("the key the app made is in bob's Immich: %v", keyNames(t, bob))
	}
	var rule string
	eventually(t, "the albums are offered", 90*time.Second, func() bool {
		body := page(t, port, cookie)
		m := regexp.MustCompile(`rules/review/([0-9a-f-]{36})`).FindStringSubmatch(body)
		if m == nil || !strings.Contains(body, "5 photos for 3 albums from your folders") {
			return false
		}
		rule = m[1]
		return true
	})
	offered := page(t, port, cookie)
	for _, want := range []string{"Albums: Summer trip, paris, rome", "Make the albums"} {
		if !strings.Contains(offered, want) {
			t.Errorf("the card lacks %q", want)
		}
	}
	if got := albums(); got["paris"] != 0 || got["Summer trip"] != 0 {
		t.Errorf("nothing is made before the answer: %v", got)
	}
	form("/rules/answer", url.Values{"rule": {rule}, "do": {"send"}})
	eventually(t, "the albums have their photos", 60*time.Second, func() bool {
		got := albums()
		return got["paris"] == 3 && got["rome"] == 1 && got["Summer trip"] == 1
	})
	var list []struct {
		Name string `json:"albumName"`
	}
	call(t, "GET", "/albums", keyAuth(bob), nil, &list)
	n := 0
	for _, a := range list {
		if a.Name == "rome" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the album bob already had is used, not made again: %d albums named rome", n)
	}
	if got := albums(); got["photos"] != 0 { // other tests leave albums of their own in the shared Immich: only the folder's would matter
		t.Errorf("the photo directly in the folder has no album: %v", got)
	}

	// stopping the rule deletes the key
	form("/rules/delete", url.Values{"rule": {rule}})
	eventually(t, "the key is gone", 20*time.Second, func() bool { return !hasName(keyNames(t, bob), "handoff: folders") })
}
