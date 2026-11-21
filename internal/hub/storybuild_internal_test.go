package hub

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

func shot(day string, sharp float64, hash uint64, city string) lookedAt {
	t, _ := time.Parse("2006-01-02", day)
	a := immich.AssetResponseDto{Id: uuid.New(), LocalDateTime: t.Add(10 * time.Hour), FileCreatedAt: t.Add(10 * time.Hour), Type: immich.IMAGE}
	if city != "" {
		c, k := city, "Portugal"
		a.ExifInfo = &immich.ExifResponseDto{City: &c, Country: &k}
	}
	return lookedAt{a: a, q: &quality{Mean: 120, Sharp: sharp, Hash: hash}}
}

func allKept(n int) []bool {
	k := make([]bool, n)
	for i := range k {
		k[i] = true
	}
	return k
}

func TestAStoryTakesAboutFortyPhotosSpreadOverTheMonthsWhateverThePhotosPerMonth(t *testing.T) {
	var rs []lookedAt
	for m := 1; m <= 12; m++ {
		n := 10 // a quiet month
		if m == 8 {
			n = 300 // a holiday month with most of the photos
		}
		for i := 0; i < n; i++ {
			rs = append(rs, shot(fmt.Sprintf("2026-%02d-%02d", m, 1+i%27), float64(30+i), uint64(i), ""))
		}
	}
	chosen := selectStory(rs, allKept(len(rs)), 40)
	if len(chosen) < 38 || len(chosen) > 40 {
		t.Fatalf("about forty: %d", len(chosen))
	}
	perMonth := map[string]int{}
	for _, i := range chosen {
		perMonth[rs[i].a.LocalDateTime.Format("2006-01")]++
	}
	if len(perMonth) != 12 {
		t.Fatalf("every month has a photo: %v", perMonth)
	}
	for m, n := range perMonth {
		if n > 16 {
			t.Errorf("the busy month does not take every slot: %s has %d", m, n)
		}
	}
	for k := 1; k < len(chosen); k++ {
		if rs[chosen[k]].a.LocalDateTime.Before(rs[chosen[k-1]].a.LocalDateTime) {
			t.Fatal("the choice is in time order")
		}
	}
	// within a month the sharpest win
	best := 0.0
	for _, i := range chosen {
		if rs[i].a.LocalDateTime.Month() == time.August && rs[i].q.Sharp > best {
			best = rs[i].q.Sharp
		}
	}
	if best < 300 {
		t.Errorf("the sharpest of August is in the story: %.0f", best)
	}
}

func TestAFavouriteBeatsASharperPhotoAndAFewPhotosAreAllKept(t *testing.T) {
	a, b := shot("2026-03-02", 100, 1, ""), shot("2026-03-02", 90, 2, "")
	b.a.IsFavorite = true
	rs := []lookedAt{a, b}
	for i := 0; i < 30; i++ {
		rs = append(rs, shot("2026-03-10", 10, uint64(100+i), ""))
	}
	if got := selectStory(rs[:2], allKept(2), 40); len(got) != 2 {
		t.Fatalf("under the target: all %v", got)
	}
	// one slot for the month: the favourite (90 x 1.25 + 5) beats the sharper one (100)
	if got := selectStory(rs[:2], allKept(2), 1); len(got) != 1 || got[0] != 1 {
		t.Fatalf("the favourite: %v", got)
	}
	if got := selectStory(rs, make([]bool, len(rs)), 40); got != nil {
		t.Fatalf("nothing kept: %v", got)
	}
}

func TestChaptersAreMonthsForAYearAndPlacesForATrip(t *testing.T) {
	var year []store.StoryPick
	for _, d := range []string{"2026-01-05", "2026-01-20", "2026-03-02", "2026-03-30", "2026-07-14"} {
		year = append(year, store.StoryPick{Date: d})
	}
	ch := chaptersFor(year)
	if len(ch) != 3 || ch[0].From != "2026-01-05" || ch[0].To != "2026-01-20" || year[2].Chapter != 1 || year[4].Chapter != 2 || ch[0].Title != "" {
		t.Fatalf("months: %+v %+v", ch, year)
	}
	trip := []store.StoryPick{
		{Date: "2026-08-01", Place: "Lisbon, Portugal"}, {Date: "2026-08-01", Place: "Lisbon, Portugal"}, {Date: "2026-08-02", Place: "Lisbon, Portugal"},
		{Date: "2026-08-03", Place: "Sintra, Portugal"}, {Date: "2026-08-03", Place: "Lisbon, Portugal"}, // one in Lisbon between Sintra and Sintra is joined to the run before
		{Date: "2026-08-04", Place: "Porto, Portugal"}, {Date: "2026-08-05", Place: "Porto, Portugal"},
	}
	ch = chaptersFor(trip)
	titles := []string{}
	for _, c := range ch {
		titles = append(titles, c.Title)
	}
	if len(ch) < 2 || ch[0].Title != "Lisbon" || ch[len(ch)-1].Title != "Porto" || ch[0].From != "2026-08-01" {
		t.Fatalf("places: %v %+v", titles, trip)
	}
	for _, p := range trip {
		if p.Chapter < 0 || p.Chapter >= len(ch) {
			t.Fatalf("every photo is in a chapter: %+v", trip)
		}
	}
	days := []store.StoryPick{{Date: "2026-08-01"}, {Date: "2026-08-01"}, {Date: "2026-08-03"}}
	if ch = chaptersFor(days); len(ch) != 2 || days[1].Chapter != 0 || days[2].Chapter != 1 {
		t.Fatalf("days when there are no places: %+v %+v", ch, days)
	}
	if chaptersFor(nil) != nil {
		t.Fatal("no photos, no chapters")
	}
}

func TestASamplingKeepsTheSpreadAndAPickNeverCarriesCoordinates(t *testing.T) {
	var list []immich.AssetResponseDto
	for i := 0; i < 1000; i++ {
		d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i%365)
		list = append(list, immich.AssetResponseDto{Id: uuid.New(), LocalDateTime: d})
	}
	s := sampleSpread(list, 100)
	if len(s) != 100 || s[0].LocalDateTime.After(s[99].LocalDateTime) {
		t.Fatalf("sample %d", len(s))
	}
	lat, lon, city, country := 38.7, -9.1, "Lisbon", "Portugal"
	a := immich.AssetResponseDto{Id: uuid.New(), LocalDateTime: time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC),
		ExifInfo: &immich.ExifResponseDto{Latitude: &lat, Longitude: &lon, City: &city, Country: &country}}
	name := "Nora"
	people := []immich.PersonResponseDto{{Name: name}, {Name: ""}}
	a.People = &people
	p := pickOf(a)
	if p.Place != "Lisbon, Portugal" || p.Date != "2026-05-01" || len(p.People) != 1 || p.People[0] != "Nora" {
		t.Fatalf("pick: %+v", p)
	}
}
