package web_test

import (
	"strings"
	"testing"
)

func TestTheAlbumTreeShowsAlbumsAsFoldersWithCoversAndTheSharedOnesApart(t *testing.T) {
	app, _ := setup(t)
	if code, _ := do(t, "GET", app.URL+"/albums", "", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	code, page := do(t, "GET", app.URL+"/albums", "sess=tree", nil)
	if code != 200 {
		t.Fatalf("the page: %d", code)
	}
	for _, want := range []string{"Album tree", "My albums", "Trips", "2024", "Rome", "Oslo", "Birthday", "Shared with me", "Beach", "shared by Alice",
		"albums/99999999-9999-4999-8999-999999999951/cover", "albums/99999999-9999-4999-8999-999999999960/cover", "To open an album in Immich"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(page, "albums/99999999-9999-4999-8999-999999999952/cover") {
		t.Error("an album with no cover is shown with one")
	}
	if strings.Contains(page, "Trips/2024/Oslo") || strings.Contains(page, "Trips / 2024 / Rome<") {
		t.Errorf("the names are split into folders, not shown whole")
	}
}

func TestAnAlbumCoverIsServedByAlbumIdOnlyForTheAlbumsOfTheSignedInPerson(t *testing.T) {
	app, _ := setup(t)
	if code, _ := do(t, "GET", app.URL+"/albums/99999999-9999-4999-8999-999999999951/cover", "", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/albums/99999999-9999-4999-8999-999999999951/cover", "sess=tree", nil); code != 200 {
		t.Errorf("an own album's cover: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/albums/99999999-9999-4999-8999-999999999960/cover", "sess=tree", nil); code != 200 {
		t.Errorf("a shared album's cover: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/albums/99999999-9999-4999-8999-999999999952/cover", "sess=tree", nil); code != 404 {
		t.Errorf("no cover: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/albums/99999999-9999-4999-8999-999999999951/cover", "sess=family", nil); code != 404 {
		t.Errorf("somebody else's album: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/albums/not-an-id/cover", "sess=tree", nil); code != 404 {
		t.Errorf("not an id: %d", code)
	}
}

func TestMovingAnAlbumIntoAFolderPreviewsAndRenamesOnlyTheOwnAlbums(t *testing.T) {
	app, _ := setup(t)
	if code, _ := postForm(t, app.URL+"/albums/move", "", "album=99999999-9999-4999-8999-999999999953&folder=Parties"); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, body := do(t, "GET", app.URL+"/fragments/album-move?album=99999999-9999-4999-8999-999999999953", "sess=tree", nil); code != 200 || !strings.Contains(body, "Move into the folder") || !strings.Contains(body, "Trips / 2024") {
		t.Errorf("the form offers the folders there are: %d %s", code, body)
	}
	if _, body := do(t, "GET", app.URL+"/fragments/album-move-preview?album=99999999-9999-4999-8999-999999999953&folder=Parties+%2F+2025", "sess=tree", nil); !strings.Contains(body, "Birthday will be renamed to Parties / 2025 / Birthday.") {
		t.Errorf("the preview: %s", body)
	}
	if code, _ := do(t, "GET", app.URL+"/fragments/album-move?album=99999999-9999-4999-8999-999999999960", "sess=tree", nil); code != 404 {
		t.Errorf("a shared album cannot be moved: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/albums/move", "sess=tree", "album=99999999-9999-4999-8999-999999999960&folder=Mine"); code != 409 {
		t.Errorf("renaming a shared album is refused: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/albums/move", "sess=tree", "album=99999999-9999-4999-8999-999999999953&folder="+strings.Repeat("x", 120)); code != 409 {
		t.Errorf("a name that is too long is refused: %d", code)
	}
	if code, b := postForm(t, app.URL+"/albums/move", "sess=tree", "album=99999999-9999-4999-8999-999999999953&folder=Parties+%2F+2025"); code != 204 {
		t.Fatalf("the move: %d %s", code, b)
	}
	if got, _ := renamed.Load().(string); got != "99999999-9999-4999-8999-999999999953=Parties / 2025 / Birthday" {
		t.Errorf("Immich was asked for %q", got)
	}
}
