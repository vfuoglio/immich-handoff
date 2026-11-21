package albumtree

import (
	"strings"
	"testing"
)

func TestANameIsSplitOnASlashWhateverTheSpaces(t *testing.T) {
	for _, n := range []string{"Trips/2024/Rome", "Trips / 2024 / Rome", " Trips /2024/  Rome ", "Trips//2024 / / Rome"} {
		f, title := Split(n)
		if len(f) != 2 || f[0] != "Trips" || f[1] != "2024" || title != "Rome" {
			t.Errorf("%q: %v %q", n, f, title)
		}
	}
	if f, title := Split("Holiday"); len(f) != 0 || title != "Holiday" {
		t.Errorf("no folders: %v %q", f, title)
	}
	if f, title := Split(" / / "); len(f) != 0 || title != "/ /" {
		t.Errorf("only slashes stays as it is: %v %q", f, title)
	}
	deep := strings.Repeat("a/", 12) + "z"
	if f, title := Split(deep); len(f) != MaxDepth || !strings.HasSuffix(title, "z") {
		t.Errorf("the depth is capped: %d %q", len(f), title)
	}
}

func TestTheTreeNestsOrdersAndCounts(t *testing.T) {
	root := Build([]Entry{
		{ID: "1", Name: "Trips / 2024 / Rome", Count: 10},
		{ID: "2", Name: "trips/2024/Oslo", Count: 5},
		{ID: "3", Name: "Trips / 2023", Count: 2},
		{ID: "4", Name: "Birthday", Count: 7},
		{ID: "5", Name: "Trips / 2024 / Rome", Count: 1}, // the same path twice: both are shown
	})
	if len(root.Albums) != 1 || root.Albums[0].Title != "Birthday" {
		t.Fatalf("top albums %+v", root.Albums)
	}
	if len(root.Children) != 1 || root.Children[0].Name != "Trips" {
		t.Fatalf("folders %+v (Trips and trips are one folder)", root.Children)
	}
	trips := root.Children[0]
	if trips.AlbumCount != 4 || trips.PhotoCount != 18 || root.AlbumCount != 5 || root.PhotoCount != 25 {
		t.Errorf("counts: trips %d/%d, all %d/%d", trips.AlbumCount, trips.PhotoCount, root.AlbumCount, root.PhotoCount)
	}
	if len(trips.Albums) != 1 || trips.Albums[0].Title != "2023" || len(trips.Children) != 1 || trips.Children[0].Name != "2024" {
		t.Errorf("trips: %+v", trips)
	}
	y := trips.Children[0]
	if len(y.Albums) != 3 || y.Albums[0].Title != "Oslo" || y.Albums[1].Title != "Rome" || y.Albums[2].Title != "Rome" {
		t.Errorf("2024 albums are ordered, the same path shown twice: %+v", y.Albums)
	}
	if got := strings.Join(root.Folders(), "|"); got != "Trips|Trips / 2024" {
		t.Errorf("folders for the menu: %s", got)
	}
}

func TestRenamingMovesAnAlbumIntoAFolderKeepingItsTitle(t *testing.T) {
	cases := []struct{ old, folder, want string }{
		{"Rome", "Trips / 2024", "Trips / 2024 / Rome"},
		{"Old / Rome", "Trips/2024", "Trips / 2024 / Rome"},
		{"Trips / Rome", "", "Rome"},
		{"Rome", "  Trips  //  2024 ", "Trips / 2024 / Rome"},
	}
	for _, c := range cases {
		if got, ok := Rename(c.old, c.folder); !ok || got != c.want {
			t.Errorf("%q into %q: %q %v, want %q", c.old, c.folder, got, ok, c.want)
		}
	}
	if _, ok := Rename("Rome", strings.Repeat("x", 120)); ok {
		t.Error("a name over 100 characters is refused")
	}
	if _, ok := Rename("/ /", "Trips"); ok {
		t.Error("an album with no title is refused")
	}
}
