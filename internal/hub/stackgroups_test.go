package hub_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
)

var t0 = time.Date(2025, 7, 4, 12, 0, 0, 0, time.UTC)

func sp(path string, at time.Duration, cam ...string) hub.StackPhoto {
	p := hub.StackPhoto{ID: uuid.New(), Path: path, Taken: t0.Add(at)}
	if len(cam) == 2 {
		p.Make, p.Model = cam[0], cam[1]
	}
	return p
}

func paths(g hub.StackGroup) string {
	var out []string
	for _, m := range g.Members {
		out = append(out, m.Path[strings.LastIndex(m.Path, "/")+1:])
	}
	return strings.Join(out, ",")
}

var all = hub.StackSpec{Pair: true, Edit: true, Burst: true}

func TestARawAndAJpegOfTheSameShotAreAPairWhateverTheCase(t *testing.T) {
	g := hub.PlanStacks(all, []hub.StackPhoto{
		sp("/p/IMG_0042.CR2", 0), sp("/p/IMG_0042.JPG", time.Second),
		sp("/p/img_0050.dng", 0), sp("/p/IMG_0050.jpeg", 0),
	})
	if len(g) != 2 || g[0].Signal != hub.SignalPair || paths(g[0]) != "IMG_0042.JPG,IMG_0042.CR2" || paths(g[1]) != "IMG_0050.jpeg,img_0050.dng" {
		t.Fatalf("%+v", g)
	}
	if !g[0].AutoOK() {
		t.Error("a pair may go on by itself")
	}
}

func TestTheSameNameInAnotherFolderOrAtAnotherMomentIsNotTheSameShot(t *testing.T) {
	g := hub.PlanStacks(all, []hub.StackPhoto{
		sp("/a/IMG_1.CR2", 0), sp("/b/IMG_1.JPG", 0), // two folders
		sp("/c/IMG_2.CR2", 0), sp("/c/IMG_2.JPG", 3*time.Hour), // the counter came round: another shot, hours later
		sp("/d/IMG_3.JPG", 0), sp("/d/IMG_3.PNG", 0), // two plain files with one name are not a pair or an edit
	})
	if len(g) != 0 {
		t.Errorf("no group: %+v", g)
	}
}

func TestAnEditIsStackedWithItsOriginalAndAPairKeepsTheEditToo(t *testing.T) {
	g := hub.PlanStacks(all, []hub.StackPhoto{
		sp("/p/IMG_7.jpg", 0), sp("/p/IMG_7-edited.jpg", 0), sp("/p/IMG_7_edit.jpg", 0),
		sp("/p/IMG_8.jpg", 0), sp("/p/IMG_8 (1).jpg", 0),
		sp("/p/IMG_9.CR2", 0), sp("/p/IMG_9.JPG", 0), sp("/p/IMG_9-edited.jpg", 0),
	})
	if len(g) != 3 {
		t.Fatalf("%+v", g)
	}
	if g[0].Signal != hub.SignalEdit || paths(g[0]) != "IMG_7.jpg,IMG_7-edited.jpg,IMG_7_edit.jpg" {
		t.Errorf("edits: %s %s", g[0].Signal, paths(g[0]))
	}
	if g[1].Signal != hub.SignalEdit || paths(g[1]) != "IMG_8.jpg,IMG_8 (1).jpg" {
		t.Errorf("a numbered copy: %s %s", g[1].Signal, paths(g[1]))
	}
	if g[2].Signal != hub.SignalPair || paths(g[2]) != "IMG_9.JPG,IMG_9-edited.jpg,IMG_9.CR2" {
		t.Errorf("a RAW, its JPEG and an edit are one pair: %s %s", g[2].Signal, paths(g[2]))
	}
}

func TestTheSignalsTheRuleAsksForAreTheOnlyOnesUsed(t *testing.T) {
	ps := []hub.StackPhoto{sp("/p/A.CR2", 0), sp("/p/A.JPG", 0), sp("/p/B.jpg", 0), sp("/p/B-edited.jpg", 0)}
	if g := hub.PlanStacks(hub.StackSpec{Pair: true}, ps); len(g) != 1 || g[0].Signal != hub.SignalPair {
		t.Errorf("pairs only: %+v", g)
	}
	if g := hub.PlanStacks(hub.StackSpec{Edit: true}, ps); len(g) != 1 || g[0].Signal != hub.SignalEdit {
		t.Errorf("edits only: %+v", g)
	}
	if g := hub.PlanStacks(hub.StackSpec{}, ps); len(g) != 0 {
		t.Errorf("nothing asked: %+v", g)
	}
}

func TestABurstIsThreeOrMoreShotsFromOneCameraAMomentApartAndNeverGoesByItself(t *testing.T) {
	cam := []string{"Canon", "EOS R5"}
	g := hub.PlanStacks(all, []hub.StackPhoto{
		sp("/b/D1.jpg", 0, cam...), sp("/b/D2.jpg", time.Second, cam...), sp("/b/D3.jpg", 2*time.Second, cam...), // a chain of one-second gaps
		sp("/b/D4.jpg", 10*time.Second, cam...), sp("/b/D5.jpg", 11*time.Second, cam...), // only two: not a burst
		sp("/b/E1.jpg", 0, "Nikon", "Z6"), sp("/b/E2.jpg", time.Second, "Nikon", "Z6"), // another camera, two shots: not a burst
		sp("/b/F1.jpg", 0), sp("/b/F2.jpg", time.Second), sp("/b/F3.jpg", 2*time.Second), // no camera: nothing to tell them apart by
	})
	if len(g) != 1 || g[0].Signal != hub.SignalBurst || paths(g[0]) != "D1.jpg,D2.jpg,D3.jpg" {
		t.Fatalf("one burst: %+v", g)
	}
	if g[0].AutoOK() {
		t.Error("a burst always asks")
	}
	// a pause of two seconds ends the run
	g = hub.PlanStacks(all, []hub.StackPhoto{sp("/b/1.jpg", 0, cam...), sp("/b/2.jpg", time.Second, cam...), sp("/b/3.jpg", 3*time.Second, cam...)})
	if len(g) != 0 {
		t.Errorf("a pause too long: %+v", g)
	}
}

func TestAPhotoInAPairIsNotAlsoInABurst(t *testing.T) {
	cam := []string{"Sony", "A7"}
	g := hub.PlanStacks(all, []hub.StackPhoto{
		sp("/p/S1.ARW", 0, cam...), sp("/p/S1.JPG", 0, cam...), sp("/p/S2.JPG", time.Second, cam...), sp("/p/S3.JPG", 2*time.Second, cam...),
	})
	if len(g) != 1 || g[0].Signal != hub.SignalPair {
		t.Errorf("S2 and S3 are two shots, not a burst, once S1 is in its pair: %+v", g)
	}
}

func TestAGroupWithAStackedMemberIsLeftAloneAndNothingIsMerged(t *testing.T) {
	a, b := sp("/p/X.CR2", 0), sp("/p/X.JPG", 0)
	b.Stacked = true
	c, d := sp("/p/Y.CR2", 0), sp("/p/Y.JPG", 0)
	g := hub.PlanStacks(all, []hub.StackPhoto{a, b, c, d})
	if len(g) != 1 || paths(g[0]) != "Y.JPG,Y.CR2" {
		t.Errorf("only the group with no stacked member: %+v", g)
	}
}

func TestTheCoverIsTheJpegByDefaultAndTheRawOrTheEditWhenAsked(t *testing.T) {
	ps := func() []hub.StackPhoto {
		return []hub.StackPhoto{sp("/p/Z.CR2", 0), sp("/p/Z.JPG", 0), sp("/p/Z-edited.jpg", 0)}
	}
	for cover, want := range map[string]string{"": "Z.JPG,Z-edited.jpg,Z.CR2", hub.CoverJPEG: "Z.JPG,Z-edited.jpg,Z.CR2", hub.CoverRAW: "Z.CR2,Z.JPG,Z-edited.jpg", hub.CoverEdited: "Z-edited.jpg,Z.JPG,Z.CR2"} {
		g := hub.PlanStacks(hub.StackSpec{Pair: true, Edit: true, Cover: cover}, ps())
		if len(g) != 1 || paths(g[0]) != want {
			t.Errorf("cover %q: %s, want %s", cover, paths(g[0]), want)
		}
	}
}

func TestAFolderPrefixLimitsWhatIsGroupedAndASiblingFolderIsNotInIt(t *testing.T) {
	ps := []hub.StackPhoto{sp("/lib/a/1.CR2", 0), sp("/lib/a/1.JPG", 0), sp("/lib-old/a/2.CR2", 0), sp("/lib-old/a/2.JPG", 0)}
	g := hub.PlanStacks(hub.StackSpec{Pair: true, Prefix: "/lib/"}, ps)
	if len(g) != 1 || paths(g[0]) != "1.JPG,1.CR2" {
		t.Errorf("%+v", g)
	}
	if g := hub.PlanStacks(hub.StackSpec{Pair: true}, ps); len(g) != 2 {
		t.Errorf("no prefix: both: %d", len(g))
	}
}

func TestAPhotoGivenTwiceIsInAGroupOnce(t *testing.T) {
	a, b := sp("/p/Q.CR2", 0), sp("/p/Q.JPG", 0)
	g := hub.PlanStacks(all, []hub.StackPhoto{a, b, a, b})
	if len(g) != 1 || len(g[0].Members) != 2 {
		t.Errorf("%+v", g)
	}
}
