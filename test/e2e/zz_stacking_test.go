//go:build e2e

// Named to sort last, like the other zz_ tests: it leaves an external library and its photos in the shared Immich.
package e2e

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
)

// stackLibrary is bob's external library over the sample photos of test/external-photos/stacks (eleven files).
func stackLibrary(t *testing.T, bob user) string {
	t.Helper()
	return externalLibraryAt(t, bob, []string{"/external/stacks"}, 11)
}

// TestStackingSpike: what Immich gives the grouping, as docs/design/stacks-design.md says. The groups the read-only run finds
// among the sample photos, how a search treats a photo that is in a stack, and what a key of the planned permissions can do.
func TestStackingSpike(t *testing.T) {
	_, bob, _, _ := bootstrap(t)
	lib := stackLibrary(t, bob)
	api, err := immich.New(baseURL, immich.Auth{APIKey: bob.key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	me, err := api.Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var h hub.Hub
	ctx := context.Background()
	photos, err := h.StackPhotos(ctx, api, uuid.UUID(me.ID), "/external/stacks", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]hub.StackPhoto{}
	for _, p := range photos {
		t.Logf("%-28s taken %s  %s %s  stacked=%v", p.Path[strings.LastIndex(p.Path, "/")+1:], p.Taken.UTC().Format("15:04:05"), p.Make, p.Model, p.Stacked)
		byName[p.Path[strings.LastIndex(p.Path, "/")+1:]] = p
	}
	if len(photos) != 11 {
		t.Fatalf("the eleven sample photos, with their EXIF: %d", len(photos))
	}
	if p := byName["IMG_1001.DNG"]; p.Make != "Canon" || p.Model != "EOS R5" || p.Taken.UTC().Format("15:04:05") != "12:00:00" {
		t.Fatalf("Immich read the EXIF of the RAW-named file: %+v", p)
	}

	spec := hub.StackSpec{Pair: true, Edit: true, Burst: true}
	groups, err := h.DryRunStacks(ctx, api, uuid.UUID(me.ID), hub.StackSpec{Pair: true, Edit: true, Burst: true, Prefix: "/external/stacks"})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		t.Logf("the read-only run: %s", g)
	}
	want := []string{"burst: DSC_0101.jpg, DSC_0102.jpg, DSC_0103.jpg", "pair: IMG_1001.JPG, IMG_1001.DNG", "edit: IMG_1002.jpg, IMG_1002-edited.jpg"}
	if len(groups) != 3 {
		t.Fatalf("three groups among eleven photos (the same name in another folder, two shots a second apart and a photo alone are not groups): %d", len(groups))
	}
	got := map[string]bool{}
	for _, g := range groups {
		got[g.String()] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing the group %q", w)
		}
	}
	_ = spec

	// how a search treats a photo in a stack: make one of the pair by hand (with the person's own key)
	jpg, dng := byName["IMG_1001.JPG"], byName["IMG_1001.DNG"]
	var stack struct {
		ID string `json:"id"`
	}
	mustOK(t, call(t, "POST", "/stacks", keyAuth(bob), map[string]any{"assetIds": []string{jpg.ID.String(), dng.ID.String()}}, &stack), "make a stack by hand")
	defer call(t, "DELETE", "/stacks/"+stack.ID, keyAuth(bob), nil, nil)
	after, _ := h.StackPhotos(ctx, api, uuid.UUID(me.ID), "/external/stacks", time.Time{})
	stacked := 0
	for _, p := range after {
		if p.Stacked {
			stacked++
		}
	}
	t.Logf("a search of the folder with withStacked set lists %d photos, and %d of them are left out when it is not set (they are stacked)", len(after), stacked)
	if len(after) != 11 || stacked != 2 {
		t.Errorf("a search with withStacked lists every photo and says which are stacked: %d, %d", len(after), stacked)
	}
	again, _ := h.DryRunStacks(ctx, api, uuid.UUID(me.ID), hub.StackSpec{Pair: true, Edit: true, Burst: true, Prefix: "/external/stacks"})
	if len(again) != 2 {
		t.Errorf("the pair that is already stacked is left alone: %d groups", len(again))
	}

	// the whole library, no folder: the other photos in the shared Immich (uploads without EXIF, the folder albums' library) add no group
	if all, err := h.DryRunStacks(ctx, api, uuid.UUID(me.ID), hub.StackSpec{Pair: true, Edit: true, Burst: true}); err == nil {
		t.Logf("the whole library of bob in this Immich: %d groups (the three above, when the sample photos are all there is)", len(all))
	}

	// the permissions the key will have: it can search, read and make a stack and take it apart again, and nothing is asked of anything else
	var key struct {
		Secret string `json:"secret"`
	}
	mustOK(t, call(t, "POST", "/api-keys", map[string]string{"Authorization": "Bearer " + bob.token},
		map[string]any{"name": "stacks-spike", "permissions": []string{"user.read", "asset.read", "stack.read", "stack.create", "stack.update", "stack.delete"}}, &key), "the planned key")
	k := map[string]string{"x-api-key": key.Secret}
	mustOK(t, call(t, "DELETE", "/stacks/"+stack.ID, k, nil, nil), "the key takes the stack apart")
	burst := []string{}
	for _, n := range []string{"DSC_0101.jpg", "DSC_0102.jpg", "DSC_0103.jpg"} {
		burst = append(burst, byName[n].ID.String())
	}
	var made struct {
		ID             string `json:"id"`
		PrimaryAssetID string `json:"primaryAssetId"`
	}
	mustOK(t, call(t, "POST", "/stacks", k, map[string]any{"assetIds": burst}, &made), "the key makes a stack")
	if made.PrimaryAssetID != burst[0] {
		t.Errorf("the first photo given is the cover: %s, want %s", made.PrimaryAssetID, burst[0])
	}
	mustOK(t, call(t, "GET", "/stacks/"+made.ID, k, nil, nil), "the key reads it")
	mustOK(t, call(t, "DELETE", "/stacks/"+made.ID, k, nil, nil), "the key takes it apart")
	if c := call(t, "DELETE", "/assets", k, map[string]any{"ids": burst}, nil); c < 400 {
		t.Errorf("the key cannot delete a photo: %d", c)
	}
	_ = lib
}

// TestStacking: a person stacks the photos that belong together on a real Immich. The form says what it finds, the rule offers the
// groups, nothing is stacked before the answer, Send makes the stacks (the JPEG is the cover of a pair, a photo of another folder with
// the same name is left alone), Undo takes the app's stacks apart again, and stopping the rule deletes the key.
func TestStacking(t *testing.T) {
	_, bob, _, _ := bootstrap(t)
	stackLibrary(t, bob)
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 97)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir(), "RULES_CHECK_MINUTES=0.1", "RULES_SETTLE_MINUTES=0.02")
	cookie := loginCookie(t, "bob@e2e.test")
	form := func(path string, v url.Values) {
		t.Helper()
		if code, body := postBody(t, port, path, cookie, v); code != 204 {
			t.Fatalf("%s: HTTP %d %.300s", path, code, body)
		}
	}
	asset := func(path string) (id string, stackCount int, primary string) {
		var res struct {
			Assets struct {
				Items []struct {
					ID string `json:"id"`
				} `json:"items"`
			} `json:"assets"`
		}
		call(t, "POST", "/search/metadata", keyAuth(bob), map[string]any{"originalPath": path}, &res)
		if len(res.Assets.Items) != 1 {
			t.Fatalf("the photo %s: %d found", path, len(res.Assets.Items))
		}
		var a struct {
			Stack *struct {
				AssetCount     int    `json:"assetCount"`
				PrimaryAssetID string `json:"primaryAssetId"`
			} `json:"stack"`
		}
		call(t, "GET", "/assets/"+res.Assets.Items[0].ID, keyAuth(bob), nil, &a)
		if a.Stack != nil {
			return res.Assets.Items[0].ID, a.Stack.AssetCount, a.Stack.PrimaryAssetID
		}
		return res.Assets.Items[0].ID, 0, ""
	}

	code, body := getBody(t, port, "/fragments/stack-check?pair=1&edit=1&burst=1&cover=jpeg&folder="+url.QueryEscape("/external/stacks"), cookie)
	if code != 200 || !strings.Contains(body, "Looked at 11 of your photos") || !strings.Contains(body, "1 RAW with its JPEG") || !strings.Contains(body, "1 edit with its original") || !strings.Contains(body, "1 burst") {
		t.Fatalf("the form's check: %d %.400s", code, body)
	}
	form("/rules/stack", url.Values{"pair": {"1"}, "edit": {"1"}, "burst": {"1"}, "cover": {"jpeg"}, "folder": {"/external/stacks"}})
	if !hasName(keyNames(t, bob), "handoff: stacks") {
		t.Fatalf("the key the app made is in bob's Immich: %v", keyNames(t, bob))
	}
	var rule string
	eventually(t, "the groups are offered", 90*time.Second, func() bool {
		body := page(t, port, cookie)
		m := regexp.MustCompile(`rules/review/([0-9a-f-]{36})`).FindStringSubmatch(body)
		if m == nil || !strings.Contains(body, "3 groups of photos to stack") {
			return false
		}
		rule = m[1]
		return true
	})
	if _, n, _ := asset("/external/stacks/trip/IMG_1001.JPG"); n != 0 {
		t.Fatal("nothing is stacked before the answer")
	}
	if _, review := getBody(t, port, "/rules/review/"+rule, cookie); !strings.Contains(review, "A RAW with its JPEG") || !strings.Contains(review, "A burst") || !strings.Contains(review, "An edit with its original") {
		t.Errorf("the review page shows the three groups")
	}
	form("/rules/answer", url.Values{"rule": {rule}, "do": {"send"}})
	eventually(t, "the stacks exist", 60*time.Second, func() bool {
		_, n, _ := asset("/external/stacks/trip/IMG_1001.JPG")
		return n == 2
	})
	jpg, _, primary := asset("/external/stacks/trip/IMG_1001.JPG")
	if primary != jpg {
		t.Errorf("the JPEG is the cover of the pair")
	}
	if _, n, _ := asset("/external/stacks/trip/IMG_1002-edited.jpg"); n != 2 {
		t.Errorf("the edit is stacked with its original: %d", n)
	}
	if _, n, _ := asset("/external/stacks/burst/DSC_0102.jpg"); n != 3 {
		t.Errorf("the burst is stacked: %d", n)
	}
	for _, p := range []string{"/external/stacks/other/IMG_1001.JPG", "/external/stacks/pairs/DSC_0200.jpg", "/external/stacks/alone/IMG_2000.jpg"} {
		if _, n, _ := asset(p); n != 0 {
			t.Errorf("%s is not in any group: %d", p, n)
		}
	}
	if !strings.Contains(page(t, port, cookie), "Undo the 3 stacks just made") {
		t.Error("the page offers to take them apart")
	}
	form("/rules/stack-undo", url.Values{"rule": {rule}})
	eventually(t, "the stacks are taken apart", 30*time.Second, func() bool {
		_, a, _ := asset("/external/stacks/trip/IMG_1001.JPG")
		_, b, _ := asset("/external/stacks/burst/DSC_0102.jpg")
		return a == 0 && b == 0
	})

	form("/rules/delete", url.Values{"rule": {rule}})
	eventually(t, "the key is gone", 20*time.Second, func() bool { return !hasName(keyNames(t, bob), "handoff: stacks") })
}
