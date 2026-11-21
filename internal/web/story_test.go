package web_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/vault"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

const storyAsset1, storyAsset2 = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"

// withStory gives the page a store and a hub whose story key reads framePhotos, and one ready story owned by "other".
func withStory(t *testing.T, st **store.Store, story *store.Story, appURL string) func(*web.Server) {
	return func(srv *web.Server) {
		v, _ := vault.New("0123456789abcdef0123")
		s, err := store.Open(t.TempDir()+"/h.db", v)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		*st = s
		im := framePhotos(t)
		h := &hub.Hub{Static: srv.S.Services, Store: s, Log: srv.Log, Options: hub.Options{InboxTag: "inbox"}, AppURL: appURL,
			Connect: func(key string) (*immich.API, error) { return immich.New(im.URL, immich.Auth{APIKey: key}, nil) }}
		ctx := context.Background()
		story.OwnerID, story.OwnerName, story.KeyID, story.Key, story.Token = uuid.MustParse(otherID), "Other", uuid.New(), "k-story", strings.Repeat("cd", 32)
		story.Title, story.Kind, story.SourceName, story.ShowDates, story.ShowPlaces = "Summer 2026", store.StoryAlbum, "Beach", true, true
		added, err := s.AddStory(ctx, *story)
		if err != nil {
			t.Fatal(err)
		}
		*story = added
		picks := []store.StoryPick{{Asset: storyAsset1, Chapter: 0, Date: "2026-07-01", Place: "Lisbon, Portugal", People: []string{"Nora"}}, {Asset: storyAsset2, Chapter: 1, Date: "2026-08-02"}}
		chapters := []store.StoryChapter{{Title: "Lisbon", From: "2026-07-01", To: "2026-07-01"}, {From: "2026-08-02", To: "2026-08-02"}}
		if err := s.SetStoryBuilt(ctx, added.ID, store.StoryReady, "", picks, chapters); err != nil {
			t.Fatal(err)
		}
		story.Picks, story.Chapters, story.State = picks, chapters, store.StoryReady
		if err := h.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		srv.Hub = h
		srv.S.Provider = h
	}
}

func TestAStoryPageNeedsNoSignInAndGivesOnlyWhatTheSwitchesAllow(t *testing.T) {
	var st *store.Store
	var story store.Story
	app, _ := setup(t, withStory(t, &st, &story, "https://photos.example.test"))
	base := app.URL + "/story/" + story.Token
	code, page := do(t, "GET", base, "", nil)
	if code != 200 || !strings.Contains(page, `id="story"`) || !strings.Contains(page, `data-doc="story/`+story.Token+`/data"`) || !strings.Contains(page, `<base href="../">`) || !strings.Contains(page, `name="robots" content="noindex"`) || strings.Contains(page, "og:image") {
		t.Fatalf("the page: %d %.500s", code, page)
	}
	code, data := do(t, "GET", base+"/data", "", nil)
	var doc struct {
		Title  string `json:"title"`
		Scenes []struct {
			ID, Date, Place string
			People          []string
		} `json:"scenes"`
	}
	if code != 200 || json.Unmarshal([]byte(data), &doc) != nil || doc.Title != "Summer 2026" || len(doc.Scenes) != 2 || len(doc.Scenes[0].ID) != 16 || doc.Scenes[0].Place != "Lisbon, Portugal" || doc.Scenes[0].Date != "2026-07-01" || len(doc.Scenes[0].People) != 0 {
		t.Fatalf("the data: %d %s", code, data)
	}
	if strings.Contains(data, storyAsset1) || strings.Contains(data, "Nora") {
		t.Errorf("no Immich id and no names: %s", data)
	}
	if code, pic := do(t, "GET", base+"/photo/"+doc.Scenes[0].ID, "", nil); code != 200 || pic != "a picture" {
		t.Errorf("a picture: %d %q", code, pic)
	}
	for _, p := range []string{"/story/" + story.Token + "/photo/" + storyAsset1, "/story/" + strings.Repeat("0", 64), "/story/short/data", "/story/" + story.Token + "/cover"} {
		if code, _ := do(t, "GET", app.URL+p, "", nil); code != 404 {
			t.Errorf("%s: %d (unknown, ended, a preview that is off and an Immich id all answer alike)", p, code)
		}
	}
	// the owner switches the preview on: the tags, and the cover
	story.Preview, story.ShowPeople = true, true
	if err := st.UpdateStory(context.Background(), story); err != nil {
		t.Fatal(err)
	}
	_, page = do(t, "GET", base, "", nil)
	for _, want := range []string{`property="og:title" content="Summer 2026"`, `property="og:image" content="https://photos.example.test/story/` + story.Token + `/cover"`, `property="og:url"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the preview tags lack %q", want)
		}
	}
	if code, pic := do(t, "GET", base+"/cover", "", nil); code != 200 || pic != "a picture" {
		t.Errorf("the cover: %d %q", code, pic)
	}
	if _, data = do(t, "GET", base+"/data", "", nil); !strings.Contains(data, "Nora") {
		t.Errorf("names on: %s", data)
	}
	// ended: the same 404 as unknown
	story.EndsAt = time.Now().Add(-time.Minute)
	st.UpdateStory(context.Background(), story)
	for _, p := range []string{"", "/data", "/cover"} {
		if code, _ := do(t, "GET", base+p, "", nil); code != 404 {
			t.Errorf("ended %s: %d", p, code)
		}
	}
}

func TestTheOwnersSideOfStoriesNeedsSignInAndOnlyTheOwnerCanChangeAStory(t *testing.T) {
	var st *store.Store
	var story store.Story
	app, _ := setup(t, withStory(t, &st, &story, ""))
	if code, _ := postForm(t, app.URL+"/stories", "", "kind=range"); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/stories/"+story.ID, "", nil); code != 401 {
		t.Errorf("the editor signed out: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/stories/"+story.ID, "sess=family", nil); code != 404 {
		t.Errorf("the editor of somebody else's story: %d", code)
	}
	if code, body := do(t, "GET", app.URL+"/fragments/story-form", "sess=other", nil); code != 200 || !strings.Contains(body, "Made from") || !strings.Contains(body, "The link lasts") {
		t.Errorf("the form: %d %.200s", code, body)
	}
	_, page := do(t, "GET", app.URL+"/", "sess=other", nil)
	for _, want := range []string{"Stories", "Make a story", "Summer 2026", "From the album Beach", "data-link=\"story/" + story.Token + "\"", "Share"} {
		if !strings.Contains(page, want) {
			t.Errorf("the card lacks %q", want)
		}
	}
	code, edit := do(t, "GET", app.URL+"/stories/"+story.ID, "sess=other", nil)
	if code != 200 || !strings.Contains(edit, "What the page shows") || !strings.Contains(edit, "stories/"+story.ID+"/thumb/"+storyAsset1) || !strings.Contains(edit, "chapter0") || !strings.Contains(edit, "PUBLIC_APP_URL") {
		t.Fatalf("the editor: %d %.600s", code, edit)
	}
	if code, _ := do(t, "GET", app.URL+"/stories/"+story.ID+"/thumb/"+storyAsset1, "sess=other", nil); code != 200 {
		t.Errorf("a thumbnail of a photo in the story: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/stories/"+story.ID+"/thumb/33333333-3333-4333-8333-333333333333", "sess=other", nil); code != 404 {
		t.Errorf("a photo that is not in the story: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/stories/"+story.ID+"/thumb/"+storyAsset1, "sess=family", nil); code != 404 {
		t.Errorf("somebody else's thumbnail: %d", code)
	}
	// edits
	if code, _ := postForm(t, app.URL+"/stories/edit", "sess=family", "story="+story.ID+"&title=Mine"); code != 409 {
		t.Errorf("somebody else's story: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/stories/edit", "sess=other", "story="+story.ID+"&title="); code != 409 {
		t.Errorf("no title: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/stories/edit", "sess=other", "story="+story.ID+"&title=Our+summer&dates=1&preview=1&ends=keep"); code != 409 {
		t.Errorf("a preview needs the app's address: %d", code)
	}
	if code, b := postForm(t, app.URL+"/stories/edit", "sess=other", "story="+story.ID+"&title=Our+summer&dates=1&places=1&people=1&ends=30&chapter1=Beach+days"); code != 204 {
		t.Fatalf("an edit: %d %s", code, b)
	}
	got, _ := st.StoryByID(context.Background(), mustUUID(otherID), story.ID)
	if got.Title != "Our summer" || !got.ShowPeople || got.EndsAt.IsZero() || got.Chapters[1].Title != "Beach days" {
		t.Fatalf("kept: %+v", got)
	}
	if code, _ := postForm(t, app.URL+"/stories/photo/remove", "sess=other", "story="+story.ID+"&asset="+storyAsset1); code != 204 {
		t.Errorf("take a photo out: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/stories/photo/remove", "sess=other", "story="+story.ID+"&asset="+storyAsset2); code != 409 {
		t.Errorf("the last photo stays: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/stories", "sess=other", "kind=album&album="+uuid.NewString()); code != 409 {
		t.Errorf("an album that is not theirs: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/stories/end", "sess=family", "story="+story.ID); code != 409 {
		t.Errorf("somebody else's story cannot be removed: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/stories/end", "sess=other", "story="+story.ID); code != 204 {
		t.Errorf("remove: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/story/"+story.Token, "", nil); code != 404 {
		t.Errorf("a removed story: %d", code)
	}
}

func TestTheStoryScriptsAreBuilt(t *testing.T) {
	app, _ := setup(t)
	_, js := do(t, "GET", app.URL+"/static/app.js", "", nil)
	for _, want := range []string{"st-bar", "st-cardbox", "The end"} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
}
