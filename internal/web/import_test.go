package web_test

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

func TestAPersonFollowsADestinationSeesItsFilesWithPicturesAndAddsThemOrNot(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withDestinations(t, &st, false), func(s *web.Server) {
		s.Hub.AdminDests["attic"].Settings["import"] = "true" // opened for importing by the administrator
	})
	ctx := context.Background()
	_, page := do(t, "GET", app.URL+"/", "sess=other", nil)
	for _, want := range []string{"Destinations you follow", "Follow a destination"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	code, form := do(t, "GET", app.URL+"/fragments/import-sources", "sess=other", nil)
	if code != 200 || !strings.Contains(form, `value="admin:attic"`) || !strings.Contains(form, "attic (folder)") || !strings.Contains(form, "nothing in it is changed or deleted") {
		t.Fatalf("the form: %d %s", code, form)
	}
	if code, _ := do(t, "GET", app.URL+"/fragments/import-sources", "", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, b := postForm(t, app.URL+"/rules/import", "sess=other", "source=admin:nope&dest=plain"); code != 409 {
		t.Errorf("an unknown destination: %d %s", code, b)
	}
	if code, b := postForm(t, app.URL+"/rules/import", "sess=other", "source=admin:attic&prefix=phone&history=new&dest=plain"); code != 204 {
		t.Fatalf("followed: %d %s", code, b)
	}
	if code, b := postForm(t, app.URL+"/rules/import", "sess=other", "source=admin:attic&prefix=phone&history=new&dest=plain"); code != 409 || !strings.Contains(b, "already follow that destination") {
		t.Errorf("twice: %d %s", code, b)
	}
	rules, _ := st.RulesOf(ctx, mustUUID(otherID))
	var rule store.Rule
	for _, r := range rules {
		if r.Kind == store.RuleKindImport {
			rule = r
		}
	}
	if rule.ID == "" || rule.Cond.Source != "admin:attic" || rule.Cond.Prefix != "phone" {
		t.Fatalf("%+v", rules)
	}
	_, page = do(t, "GET", app.URL+"/", "sess=other", nil)
	for _, want := range []string{"attic, the folder phone", "Asks you first", "In my library", "Stop following this destination?"} {
		if !strings.Contains(page, want) {
			t.Errorf("the followed destination's row lacks %q", want)
		}
	}
	// a file offered: the card, the picture, the review page
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	var buf bytes.Buffer
	png.Encode(&buf, img)
	folderAt("/d/attic").Put(ctx, "phone/a.png", bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	st.PutRulePhoto(ctx, rule.ID, "phone/a.png", store.RuleSuggested, "a.png (1 KB)")
	_, page = do(t, "GET", app.URL+"/", "sess=other", nil)
	enc := hub.EncodeImportRef("phone/a.png")
	for _, want := range []string{"1 new file in attic", "Add them to your library? Nothing in the destination is changed.", "rules/import-photo/" + rule.ID + "/" + enc} {
		if !strings.Contains(page, want) {
			t.Errorf("the card lacks %q", want)
		}
	}
	if code, _ := do(t, "GET", app.URL+"/rules/import-photo/"+rule.ID+"/"+enc, "sess=other", nil); code != 200 {
		t.Errorf("the picture: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/rules/import-photo/"+rule.ID+"/"+hub.EncodeImportRef("phone/never.png"), "sess=other", nil); code != 404 {
		t.Errorf("a file that was not offered: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/rules/import-photo/"+rule.ID+"/"+enc, "sess=family", nil); code != 404 {
		t.Errorf("somebody else: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/rules/import-photo/"+rule.ID+"/"+enc, "", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	code, review := do(t, "GET", app.URL+"/rules/review/"+rule.ID, "sess=other", nil)
	for _, want := range []string{"New in attic", "Files that appeared in the destination attic", "Add the ticked files to my library", "Add the ticked files, and always", "a.png (1 KB)"} {
		if code != 200 || !strings.Contains(review, want) {
			t.Errorf("the review page lacks %q (%d)", want, code)
		}
	}
	// Not these: nothing is added, nothing at the destination changes
	if code, b := postForm(t, app.URL+"/rules/answer", "sess=other", "rule="+rule.ID+"&do=decline"); code != 204 {
		t.Fatalf("%d %s", code, b)
	}
	if refs := folderAt("/d/attic").Refs(); len(refs) != 1 || refs[0] != "phone/a.png" {
		t.Errorf("the destination is as it was: %v", refs)
	}
	if code, _ := postForm(t, app.URL+"/rules/delete", "sess=other", "rule="+rule.ID); code != 204 {
		t.Errorf("stop following: %d", code)
	}
}

func TestADestinationThatIsNotOpenedForImportingIsNotOfferedAndCannotBeFollowed(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withDestinations(t, &st, false))
	_, page := do(t, "GET", app.URL+"/", "sess=other", nil)
	if strings.Contains(page, "Follow a destination") {
		t.Error("nothing to follow, so no form")
	}
	if code, b := postForm(t, app.URL+"/rules/import", "sess=other", "source=admin:attic&dest=plain"); code != 409 || !strings.Contains(b, "has not allowed importing") {
		t.Errorf("%d %s", code, b)
	}
}
