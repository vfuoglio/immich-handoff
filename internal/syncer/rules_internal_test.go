package syncer

import (
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/immich"
)

func at(h, m int) time.Time { return time.Date(2026, 11, 8, h, m, 0, 0, time.Local) }

func TestAWindowThatEndsBeforeItStartsCrossesMidnight(t *testing.T) {
	q, err := ParseQuiet("22:00-06:00")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		h, m int
		open bool
	}{{21, 59, false}, {22, 0, true}, {23, 30, true}, {0, 0, true}, {5, 59, true}, {6, 0, false}, {12, 0, false}} {
		if got := q.Open(at(c.h, c.m)); got != c.open {
			t.Errorf("%02d:%02d open = %v, want %v", c.h, c.m, got, c.open)
		}
	}
	if got := q.NextOpen(at(12, 0)); got.Hour() != 22 || got.Day() != 8 {
		t.Errorf("next opening from noon: %v", got)
	}
	if got := q.NextOpen(at(23, 0)); got.Hour() != 22 || got.Day() != 9 {
		t.Errorf("next opening from 23:00 (it is open: the next is tomorrow's): %v", got)
	}
}

func TestNoWindowIsAlwaysOpenAndSizeDecidesWhatWaits(t *testing.T) {
	if (Quiet{}).Set() || !(Quiet{}).Open(at(3, 0)) || (Quiet{}).Holds(1<<40, at(3, 0)) {
		t.Error("no window means nothing waits")
	}
	q := Quiet{From: 60, To: 360}
	if !q.Holds(1, at(12, 0)) {
		t.Error("with no size threshold everything waits outside the window")
	}
	if q.Holds(1, at(2, 0)) {
		t.Error("nothing waits inside the window")
	}
	q.LargeMB = 200
	if q.Holds(100<<20, at(12, 0)) || !q.Holds(201<<20, at(12, 0)) {
		t.Error("only files over the threshold wait")
	}
}

func TestQuietHoursAreWrittenHHMM(t *testing.T) {
	if q, err := ParseQuiet(" 01:30 - 06:05 "); err != nil || q.From != 90 || q.To != 365 {
		t.Errorf("%+v %v", q, err)
	}
	if q, err := ParseQuiet(""); err != nil || q.Set() {
		t.Errorf("empty is no window: %+v %v", q, err)
	}
	for _, bad := range []string{"1:00", "25:00-06:00", "01:00-06:60", "night", "01:00-"} {
		if _, err := ParseQuiet(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	if Clock(365) != "06:05" {
		t.Errorf("clock: %s", Clock(365))
	}
}

func photo(typ immich.AssetTypeEnum, rating *int, w, h *int, orientation *string, size *int) immich.AssetResponseDto {
	return immich.AssetResponseDto{Type: typ, ExifInfo: &immich.ExifResponseDto{Rating: rating, ExifImageWidth: w, ExifImageHeight: h, Orientation: orientation, FileSizeInByte: size}}
}

func TestWhatAServiceAcceptsIsJudgedByEachRule(t *testing.T) {
	i := func(n int) *int { return &n }
	o := func(s string) *string { return &s }
	cases := []struct {
		name string
		rule Accept
		p    immich.AssetResponseDto
		ok   bool
	}{
		{"no rules take everything", Accept{}, photo(immich.IMAGE, nil, nil, nil, nil, nil), true},
		{"rating high enough", Accept{MinRating: 4}, photo(immich.IMAGE, i(4), nil, nil, nil, nil), true},
		{"rating too low", Accept{MinRating: 4}, photo(immich.IMAGE, i(3), nil, nil, nil, nil), false},
		{"unrated is below any minimum", Accept{MinRating: 1}, photo(immich.IMAGE, nil, nil, nil, nil, nil), false},
		{"photos only refuses a video", Accept{PhotosOnly: true}, photo(immich.VIDEO, nil, nil, nil, nil, nil), false},
		{"photos only takes a photo", Accept{PhotosOnly: true}, photo(immich.IMAGE, nil, nil, nil, nil, nil), true},
		{"landscape takes a wide picture", Accept{Orientation: "landscape"}, photo(immich.IMAGE, nil, i(4000), i(3000), nil, nil), true},
		{"landscape refuses a tall picture", Accept{Orientation: "landscape"}, photo(immich.IMAGE, nil, i(3000), i(4000), nil, nil), false},
		{"a turned camera swaps the sides", Accept{Orientation: "landscape"}, photo(immich.IMAGE, nil, i(3000), i(4000), o("6"), nil), true},
		{"portrait refuses a wide picture", Accept{Orientation: "portrait"}, photo(immich.IMAGE, nil, i(4000), i(3000), nil, nil), false},
		{"a square is both", Accept{Orientation: "portrait"}, photo(immich.IMAGE, nil, i(3000), i(3000), nil, nil), true},
		{"unknown dimensions let it through", Accept{Orientation: "landscape"}, photo(immich.IMAGE, nil, nil, nil, nil, nil), true},
		{"size within", Accept{MaxMB: 20}, photo(immich.IMAGE, nil, nil, nil, nil, i(20<<20)), true},
		{"size over", Accept{MaxMB: 20}, photo(immich.IMAGE, nil, nil, nil, nil, i(20<<20+1)), false},
		{"unknown size lets it through", Accept{MaxMB: 20}, photo(immich.IMAGE, nil, nil, nil, nil, nil), true},
	}
	for _, c := range cases {
		if got := c.rule.Check(c.p); got != c.ok {
			t.Errorf("%s: %v, want %v", c.name, got, c.ok)
		}
	}
	if (Accept{}).Active() || !(Accept{MaxMB: 1}).Active() {
		t.Error("Active")
	}
}

func TestADateInATagIsRecognisedWhateverSeparatesIt(t *testing.T) {
	for _, v := range []string{"share/2027-06-01", "share 2027-06-01", "share-2027-06-01", "share_2027-06-01"} {
		d, ok := scheduledFor(v, "share")
		if !ok || d.Year() != 2027 || d.Month() != 6 || d.Day() != 1 {
			t.Errorf("%q: %v %v", v, d, ok)
		}
	}
	for _, v := range []string{"share", "share/2027-6-1", "share/2027-13-01", "share/2027-06-01x", "holiday/2027-06-01", "share2027-06-01", "sharing/2027-06-01"} {
		if _, ok := scheduledFor(v, "share"); ok {
			t.Errorf("%q must not be taken for a date", v)
		}
	}
}
