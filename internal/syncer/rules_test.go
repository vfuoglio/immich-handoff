package syncer_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

const shotID = "b3000000-0000-4000-8000-0000000000%02d"

func shot(n int, size int, tags ...string) *fakeAsset {
	a := &fakeAsset{id: fmt.Sprintf(shotID, n), name: fmt.Sprintf("p%d.jpg", n), data: []byte(strings.Repeat("x", size)), tags: map[string]bool{}}
	for _, t := range tags {
		a.tags[t] = true
	}
	return a
}

// closedNow is a window that is shut at this moment (it opens in two hours), openNow one that is open.
func closedNow() syncer.Quiet {
	m := time.Now().Hour()*60 + time.Now().Minute()
	return syncer.Quiet{From: (m + 120) % 1440, To: (m + 180) % 1440}
}
func openNow() syncer.Quiet {
	m := time.Now().Hour()*60 + time.Now().Minute()
	return syncer.Quiet{From: (m + 1440 - 60) % 1440, To: (m + 60) % 1440}
}

func (f *twoAccounts) arrived() int { return len(f.assets["dest"]) }
func (f *twoAccounts) tagged(a *fakeAsset, tag string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return a.tags[tag]
}

func TestOutsideTheQuietHoursAPhotoWaitsStillTaggedAndInsideThemItGoes(t *testing.T) {
	f := newTwoAccounts()
	a := shot(1, 10, "share")
	f.addSender(a)
	f.hand(t, func(s *syncer.Service) { s.Quiet = closedNow() })
	if f.arrived() != 0 || !f.tagged(a, "share") {
		t.Fatalf("outside the window the photo must wait, tagged: arrived %d, tagged %v", f.arrived(), a.tags)
	}
	f.hand(t, func(s *syncer.Service) { s.Quiet = openNow() })
	if f.arrived() != 1 {
		t.Errorf("inside the window it goes: %d", f.arrived())
	}
}

func TestOnlyFilesOverTheThresholdWaitForTheQuietHours(t *testing.T) {
	f := newTwoAccounts()
	small, large := shot(1, 100, "share"), shot(2, 2<<20, "share")
	f.addSender(small)
	f.addSender(large)
	f.hand(t, func(s *syncer.Service) { q := closedNow(); q.LargeMB = 1; s.Quiet = q })
	if f.arrived() != 1 || f.tagged(small, "share") && !small.trashed || !f.tagged(large, "share") {
		t.Errorf("the small photo goes at once and the large one waits: arrived %d, small %v, large %v", f.arrived(), small.tags, large.tags)
	}
}

func TestTheAppWideQuietHoursApplyUnlessTheServiceSetsItsOwn(t *testing.T) {
	f := newTwoAccounts()
	a := shot(1, 10, "share")
	f.addSender(a)
	f.handWith(t, nil, func(c *syncer.Config) { c.Quiet = closedNow() })
	if f.arrived() != 0 {
		t.Fatalf("the app-wide window holds the photo: %d", f.arrived())
	}
	f.handWith(t, func(s *syncer.Service) { s.Quiet = openNow() }, func(c *syncer.Config) { c.Quiet = closedNow() })
	if f.arrived() != 1 {
		t.Errorf("the service's own window wins: %d", f.arrived())
	}
}

func rated(n int) map[string]any { return map[string]any{"rating": n} }

func TestAPhotoBelowTheMinimumRatingIsNotHandedOverAndGetsTheRefusedTag(t *testing.T) {
	f := newTwoAccounts()
	low, high := shot(1, 10, "share"), shot(2, 10, "share")
	low.exif, high.exif = rated(3), rated(5)
	f.addSender(low)
	f.addSender(high)
	c := f.hand(t, func(s *syncer.Service) { s.Accept = syncer.Accept{MinRating: 4} })
	if f.arrived() != 1 || c.NotAccepted != 1 {
		t.Fatalf("one goes and one is not accepted: arrived %d, counts %+v", f.arrived(), c)
	}
	if f.tagged(low, "share") || !f.tagged(low, "share-duplicate") || low.trashed {
		t.Errorf("the refused photo loses the service tag, gets the refused tag and stays: %v trashed=%v", low.tags, low.trashed)
	}
	if !high.trashed {
		t.Error("the accepted photo was moved as usual")
	}
}

func TestAServiceThatTakesPhotosOnlyRefusesAVideo(t *testing.T) {
	f := newTwoAccounts()
	v, p := shot(1, 10, "share"), shot(2, 10, "share")
	v.kind = "VIDEO"
	f.addSender(v)
	f.addSender(p)
	f.hand(t, func(s *syncer.Service) { s.Accept = syncer.Accept{PhotosOnly: true} })
	if f.arrived() != 1 || !f.tagged(v, "share-duplicate") {
		t.Errorf("the video is refused and the photo goes: arrived %d, %v", f.arrived(), v.tags)
	}
}

func TestOrientationAndSizeRulesAreApplied(t *testing.T) {
	f := newTwoAccounts()
	wide, tall, big := shot(1, 10, "share"), shot(2, 10, "share"), shot(3, 3<<20, "share")
	wide.exif = map[string]any{"exifImageWidth": 4000, "exifImageHeight": 3000}
	tall.exif = map[string]any{"exifImageWidth": 3000, "exifImageHeight": 4000}
	big.exif = map[string]any{"exifImageWidth": 4000, "exifImageHeight": 3000}
	for _, a := range []*fakeAsset{wide, tall, big} {
		f.addSender(a)
	}
	f.hand(t, func(s *syncer.Service) { s.Accept = syncer.Accept{Orientation: "landscape", MaxMB: 2} })
	if f.arrived() != 1 || !wide.trashed || !f.tagged(tall, "share-duplicate") || !f.tagged(big, "share-duplicate") {
		t.Errorf("only the wide, small photo goes: arrived %d, tall %v, big %v", f.arrived(), tall.tags, big.tags)
	}
}

func TestAPhotoTaggedForAPastDateIsHandedOverAndTheDatedTagComesOff(t *testing.T) {
	f := newTwoAccounts()
	a := shot(1, 10, "share/2000-01-01")
	f.tags["src"]["share/2000-01-01"] = "c1000000-0000-4000-8000-000000000001"
	f.addSender(a)
	f.hand(t, func(s *syncer.Service) { s.Mode = syncer.ModeCopy; s.SentTag = "handed-over" })
	if f.arrived() != 1 {
		t.Fatalf("a date that has come: the photo goes: %d", f.arrived())
	}
	if f.tagged(a, "share/2000-01-01") || !f.tagged(a, "handed-over") {
		t.Errorf("the dated tag comes off and the sent tag goes on: %v", a.tags)
	}
}

func TestAPhotoTaggedForAFutureDateWaitsEvenWhenItAlsoHasThePlainTag(t *testing.T) {
	f := newTwoAccounts()
	only, both := shot(1, 10, "share/2999-01-01"), shot(2, 10, "share/2999-01-01", "share")
	f.tags["src"]["share/2999-01-01"] = "c1000000-0000-4000-8000-000000000002"
	f.addSender(only)
	f.addSender(both)
	f.hand(t, nil)
	if f.arrived() != 0 || !f.tagged(only, "share/2999-01-01") || !f.tagged(both, "share") {
		t.Errorf("both wait for their day: arrived %d, %v %v", f.arrived(), only.tags, both.tags)
	}
}

func TestAPlainTagGoesAtOnceWhateverElseIsScheduled(t *testing.T) {
	f := newTwoAccounts()
	now, later := shot(1, 10, "share"), shot(2, 10, "share/2999-01-01")
	f.tags["src"]["share/2999-01-01"] = "c1000000-0000-4000-8000-000000000003"
	f.addSender(now)
	f.addSender(later)
	f.hand(t, nil)
	if f.arrived() != 1 || !now.trashed || later.trashed {
		t.Errorf("only the plain-tagged photo goes: arrived %d", f.arrived())
	}
}
