package web_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

const followedAlbum = "99999999-9999-4999-8999-999999999990"

func TestAnAlbumSharedWithThePersonIsListedAndFollowedFromTheirPage(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	code, body := do(t, "GET", app.URL+"/fragments/follow-albums", "sess=other", nil)
	if code != 200 || !strings.Contains(body, "Trip") || !strings.Contains(body, "Alice") || !strings.Contains(body, followedAlbum) {
		t.Fatalf("the albums shared with the person, and who shared them: %d %s", code, body)
	}
	if code, _ := do(t, "GET", app.URL+"/fragments/follow-albums", "", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if _, page := do(t, "GET", app.URL+"/", "sess=other", nil); !strings.Contains(page, "Follow an album shared with me") || strings.Contains(page, "Stop following this album") {
		t.Error("the page offers to follow an album, and none is followed yet")
	}
	if code, b := postForm(t, app.URL+"/rules/follow", "sess=other", "album_id="+followedAlbum+"&history=new&dest=plain"); code != 204 {
		t.Fatalf("follow: %d %s", code, b)
	}
	_, page := do(t, "GET", app.URL+"/", "sess=other", nil)
	for _, want := range []string{"Trip, shared by Alice", "Asks you first", "Stop following"} {
		if !strings.Contains(page, want) {
			t.Errorf("the followed album on the page lacks %q", want)
		}
	}
	if code, _ := postForm(t, app.URL+"/rules/follow", "sess=other", "album_id="+followedAlbum+"&history=new&dest=plain"); code != 409 {
		t.Errorf("following it twice: %d, want 409", code)
	}
	if code, _ := postForm(t, app.URL+"/rules/follow", "sess=other", "album_id=99999999-9999-4999-8999-999999999991&dest=plain"); code != 409 {
		t.Errorf("an album that is not shared with them: %d, want 409", code)
	}
	if code, _ := postForm(t, app.URL+"/rules/follow", "", "album_id="+followedAlbum+"&dest=plain"); code != 401 {
		t.Errorf("signed out: %d", code)
	}
}

func TestWhatAFollowedAlbumOffersIsShownAndAnsweredLikeAnyRulesSuggestion(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	if code, b := postForm(t, app.URL+"/rules/follow", "sess=other", "album_id="+followedAlbum+"&history=new&dest=plain"); code != 204 {
		t.Fatalf("follow: %d %s", code, b)
	}
	rules, _ := st.RulesOf(context.Background(), mustUUID(otherID))
	var id string
	for _, r := range rules {
		if r.Kind == store.RuleKindLibrary {
			id = r.ID
		}
	}
	if id == "" {
		t.Fatal("the rule is stored")
	}
	st.PutRulePhoto(context.Background(), id, myPhoto, store.RuleSuggested, "added to the album Trip, shared by Alice")
	_, page := do(t, "GET", app.URL+"/", "sess=other", nil)
	for _, want := range []string{"1 new photo in Trip", "Shared by Alice. Add them to your library?", "always"} {
		if !strings.Contains(page, want) {
			t.Errorf("the card lacks %q", want)
		}
	}
	if _, review := do(t, "GET", app.URL+"/rules/review/"+id, "sess=other", nil); !strings.Contains(review, "New in Trip") || !strings.Contains(review, "Add the ticked photos to my library") {
		t.Error("the review page speaks of the album and of the library")
	}
	if code, b := postForm(t, app.URL+"/rules/answer", "sess=other", "rule="+id+"&do=send"); code != 204 {
		t.Fatalf("answer: %d %s", code, b)
	}
	if states, _ := st.RulePhotoStates(context.Background(), id); states[myPhoto] != store.RuleSent {
		t.Errorf("the photo is sent: %v", states)
	}
	// stopping the album
	if code, _ := postForm(t, app.URL+"/rules/delete", "sess=other", "rule="+id); code != 204 {
		t.Fatalf("stop following: %d", code)
	}
	if _, page := do(t, "GET", app.URL+"/", "sess=other", nil); strings.Contains(page, id) {
		t.Error("the album is not followed any more")
	}
}

var _ = web.ButtonService{}

func mustUUID(s string) uuid.UUID { return uuid.MustParse(s) }
