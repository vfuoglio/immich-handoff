package hub_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
)

func pa(path string) hub.PathAsset { return hub.PathAsset{ID: uuid.New(), Path: path} }

func names(p hub.FolderPlan) []string {
	var out []string
	for _, a := range p.Albums {
		out = append(out, a.Name)
	}
	return out
}

func TestTheFolderAPhotoIsInMakesTheAlbumByDefault(t *testing.T) {
	assets := []hub.PathAsset{
		pa("/external/photos/2025-02/paris/a.jpg"), pa("/external/photos/2025-02/paris/b.jpg"), pa("/external/photos/2025-02/rome/c.jpg"),
		pa("/external/photos/Summer trip/e.jpg"), pa("/external/photos/loose.jpg"),
	}
	plan, err := hub.PlanFolders(hub.FolderSpec{Prefix: "/external/photos"}, assets)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(plan), "|"); got != "Summer trip|paris|rome" {
		t.Errorf("one album for each folder a photo is in, named after it: %s", got)
	}
	if len(plan.Albums[1].Photos) != 2 || plan.Loose != 1 {
		t.Errorf("paris holds two photos and the photo directly in the root is loose: %+v", plan)
	}
	if !strings.Contains(plan.String(), "3 album(s), 4 photo(s), 1 photo(s) with no folder") {
		t.Errorf("the dry run's words: %s", plan)
	}
}

func TestADepthPicksTheFolderBelowThePrefixAndAShallowPhotoHasNone(t *testing.T) {
	assets := []hub.PathAsset{
		pa("/external/photos/2025-02/paris/a.jpg"), pa("/external/photos/2025-02/rome/c.jpg"), pa("/external/photos/2025-03/paris/d.jpg"),
		pa("/external/photos/Summer trip/e.jpg"),
	}
	plan, _ := hub.PlanFolders(hub.FolderSpec{Prefix: "/external/photos", Depth: 1}, assets)
	if got := strings.Join(names(plan), "|"); got != "2025-02|2025-03|Summer trip" {
		t.Errorf("the first folder below the prefix: %s", got)
	}
	plan, _ = hub.PlanFolders(hub.FolderSpec{Prefix: "/external/photos", Depth: 2}, assets)
	if got := strings.Join(names(plan), "|"); got != "paris|rome" || plan.Loose != 1 {
		t.Errorf("the second level: %s, loose %d (the photo in Summer trip has no second folder)", got, plan.Loose)
	}
}

func TestSameNamedFoldersMakeOneAlbumUnlessTheParentIsInTheName(t *testing.T) {
	assets := []hub.PathAsset{pa("/e/p/2025-02/paris/a.jpg"), pa("/e/p/2025-03/paris/d.jpg")}
	plan, _ := hub.PlanFolders(hub.FolderSpec{Prefix: "/e/p"}, assets)
	if len(plan.Albums) != 1 || plan.Albums[0].Name != "paris" || len(plan.Albums[0].Folders) != 2 || len(plan.Albums[0].Photos) != 2 {
		t.Errorf("merged by name: %+v", plan)
	}
	plan, _ = hub.PlanFolders(hub.FolderSpec{Prefix: "/e/p", Naming: hub.NameParent}, assets)
	if got := strings.Join(names(plan), "|"); got != "2025-02 / paris|2025-03 / paris" {
		t.Errorf("the parent keeps them apart: %s", got)
	}
	// a folder directly under the prefix takes the prefix's own last folder as its parent
	plan, _ = hub.PlanFolders(hub.FolderSpec{Prefix: "/e/p", Depth: 1, Naming: hub.NameParent}, []hub.PathAsset{pa("/e/p/trips/x.jpg")})
	if got := strings.Join(names(plan), "|"); got != "p / trips" {
		t.Errorf("parent of a first-level folder: %s", got)
	}
}

func TestOnlyPhotosReallyUnderTheFolderCountAndASiblingFolderIsNotOne(t *testing.T) {
	assets := []hub.PathAsset{
		pa("/external/photos/a/1.jpg"),
		pa("/external/photos-old/2019/old.jpg"), // a sibling: the folder with a slash does not include it
		pa("/mnt/external/photos/a/2.jpg"),      // Immich matches anywhere in the path
		pa("/EXTERNAL/photos/a/3.jpg"),          // and the case matters
		pa("/external/photosX/a/4.jpg"),
	}
	plan, _ := hub.PlanFolders(hub.FolderSpec{Prefix: "/external/photos/"}, assets) // a trailing slash is allowed
	if len(plan.Albums) != 1 || len(plan.Albums[0].Photos) != 1 || plan.Loose != 0 {
		t.Errorf("one photo is under the folder: %+v", plan)
	}
}

func TestAPhotoAppearingTwiceIsInTheAlbumOnce_AndNamesAreMadeToFit(t *testing.T) {
	a := pa("/e/p/x\ty  z/1.jpg")
	plan, _ := hub.PlanFolders(hub.FolderSpec{Prefix: "/e/p"}, []hub.PathAsset{a, a})
	if len(plan.Albums) != 1 || len(plan.Albums[0].Photos) != 1 || plan.Albums[0].Name != "x y z" {
		t.Errorf("once, with the control character and the double space gone: %+v", plan.Albums)
	}
	long := strings.Repeat("é", 150)
	plan, _ = hub.PlanFolders(hub.FolderSpec{Prefix: "/e/p"}, []hub.PathAsset{pa("/e/p/" + long + "/1.jpg")})
	if n := len([]rune(plan.Albums[0].Name)); n != 100 {
		t.Errorf("at most 100 characters, whole: %d", n)
	}
}

func TestASpecThatIsNotAFolderOrALevelOrANamingIsRefused(t *testing.T) {
	for name, s := range map[string]hub.FolderSpec{
		"empty": {}, "relative": {Prefix: "photos"}, "the whole disk": {Prefix: "/"}, "dots": {Prefix: "/a/../b"}, "double slash": {Prefix: "/a//b"},
		"negative depth": {Prefix: "/a", Depth: -1}, "too deep": {Prefix: "/a", Depth: 9}, "naming": {Prefix: "/a", Naming: "title"},
	} {
		if _, err := hub.PlanFolders(s, nil); err == nil || !hub.IsUserError(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	got, err := hub.FolderSpec{Prefix: " /a/b/ "}.Clean()
	if err != nil || got.Prefix != "/a/b" || got.Naming != hub.NameLast {
		t.Errorf("cleaned: %+v %v", got, err)
	}
}
