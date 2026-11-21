package hub

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

var placeDay = time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)

func ph(min int, lat, lon *float64, mod string) PlacePhoto {
	t := placeDay.Add(time.Duration(min) * time.Minute)
	return PlacePhoto{ID: uuid.New(), Name: "x", Taken: t, Local: t, Make: "Canon", Model: mod, Lat: lat, Lon: lon}
}

func at(lat, lon float64) (*float64, *float64) { return &lat, &lon }

func TestAPhotoBetweenTwoLocatedOnesOfTheSameCameraGetsTheNearerPlace(t *testing.T) {
	la, lo := at(38.7223, -9.1393)
	lb, lob := at(38.7230, -9.1400) // about 90 m away
	before, after, me := ph(0, la, lo, "R5"), ph(10, lb, lob, "R5"), ph(3, nil, nil, "R5")
	before.City, before.Country = "Lisbon", "Portugal"
	g := GuessPlaces([]PlacePhoto{after, me, before})
	if len(g) != 1 || g[0].Photo.ID != me.ID || !g[0].TwoSided || g[0].Neighbours != 2 {
		t.Fatalf("one two-sided guess: %+v", g)
	}
	if g[0].Near.ID != before.ID || g[0].Minutes != 3 || g[0].Metres < 50 || g[0].Metres > 150 || g[0].Lat != 38.7223 || g[0].Place != "Lisbon, Portugal" {
		t.Fatalf("the nearer neighbour lends the place: %+v", g[0])
	}
	// the other way: nearer to the one after
	me2 := ph(8, nil, nil, "R5")
	if g := GuessPlaces([]PlacePhoto{before, after, me2}); len(g) != 1 || g[0].Near.ID != after.ID || g[0].Lat != 38.7230 {
		t.Fatalf("nearer to the one after: %+v", g)
	}
}

func TestOneNeighbourIsEnoughOnlyInsideTenMinutesAndOnlyForAnAnswer(t *testing.T) {
	la, lo := at(41.9, 12.5)
	loc := ph(0, la, lo, "R5")
	if g := GuessPlaces([]PlacePhoto{loc, ph(9, nil, nil, "R5")}); len(g) != 1 || g[0].TwoSided || g[0].Neighbours != 1 || g[0].Minutes != 9 {
		t.Fatalf("nine minutes after one located photo: %+v", g)
	}
	if g := GuessPlaces([]PlacePhoto{loc, ph(11, nil, nil, "R5")}); len(g) != 0 {
		t.Fatalf("eleven minutes is too far for one side: %+v", g)
	}
	if g := GuessPlaces([]PlacePhoto{ph(-9, nil, nil, "R5"), loc}); len(g) != 1 || g[0].TwoSided {
		t.Fatalf("before the located one: %+v", g)
	}
}

func TestNoGuessWhenTheCameraTravelledOrTheNeighboursAreFarApart(t *testing.T) {
	la, lo := at(38.72, -9.14)
	lb, lob := at(41.15, -8.61) // Porto, 275 km away, 12 minutes later: a flight, or a mistake
	if g := GuessPlaces([]PlacePhoto{ph(0, la, lo, "R5"), ph(6, nil, nil, "R5"), ph(12, lb, lob, "R5")}); len(g) != 0 {
		t.Fatalf("a camera that went 275 km in 12 minutes: %+v", g)
	}
	lc, loc := at(38.74, -9.14) // 2.2 km: too far apart even at a walking pace over 15 minutes
	if g := GuessPlaces([]PlacePhoto{ph(0, la, lo, "R5"), ph(7, nil, nil, "R5"), ph(14, lc, loc, "R5")}); len(g) != 0 {
		t.Fatalf("more than two kilometres apart: %+v", g)
	}
	ld, lod := at(38.7222, -9.1000) // about 3 km in a minute: too fast
	if g := GuessPlaces([]PlacePhoto{ph(0, la, lo, "R5"), ph(1, ld, lod, "R5"), ph(2, nil, nil, "R5")}); len(g) != 1 {
		t.Fatalf("the one at minute 2 has one neighbour one minute away on its side, and the far one is not used: %+v", g)
	}
}

func TestTheCameraTheDayTheZoneAndAnExistingPlaceDecide(t *testing.T) {
	la, lo := at(38.72, -9.14)
	loc := ph(0, la, lo, "R5")
	if g := GuessPlaces([]PlacePhoto{loc, ph(3, nil, nil, "R6")}); len(g) != 0 {
		t.Fatalf("another camera: %+v", g)
	}
	other := ph(3, nil, nil, "R5")
	other.Make = ""
	if g := GuessPlaces([]PlacePhoto{loc, other}); len(g) != 0 {
		t.Fatalf("no camera: %+v", g)
	}
	late := ph(3, nil, nil, "R5")
	late.Local = late.Local.Add(24 * time.Hour)
	if g := GuessPlaces([]PlacePhoto{loc, late}); len(g) != 0 {
		t.Fatalf("another local day: %+v", g)
	}
	zoned, zoned2 := ph(0, la, lo, "R5"), ph(3, nil, nil, "R5")
	zoned.Zone, zoned2.Zone = "Europe/Lisbon", "Europe/Rome"
	if g := GuessPlaces([]PlacePhoto{zoned, zoned2}); len(g) != 0 {
		t.Fatalf("another time zone: %+v", g)
	}
	lens1, lens2 := ph(0, la, lo, "R5"), ph(3, nil, nil, "R5")
	lens1.Lens, lens2.Lens = "RF24-70", "RF100-500"
	if g := GuessPlaces([]PlacePhoto{lens1, lens2}); len(g) != 0 {
		t.Fatalf("another lens: %+v", g)
	}
	lb, lob := at(10, 10)
	if g := GuessPlaces([]PlacePhoto{loc, ph(3, lb, lob, "R5")}); len(g) != 0 {
		t.Fatalf("a photo that has a place is left alone: %+v", g)
	}
	noTime := ph(3, nil, nil, "R5")
	noTime.Taken = time.Time{}
	if g := GuessPlaces([]PlacePhoto{loc, noTime}); len(g) != 0 {
		t.Fatalf("no time: %+v", g)
	}
	// the midnight boundary: 23:58 and 00:02 are not the same local day
	a, b := ph(0, la, lo, "R5"), ph(4, nil, nil, "R5")
	a.Local, b.Local = time.Date(2026, 8, 3, 23, 58, 0, 0, time.UTC), time.Date(2026, 8, 4, 0, 2, 0, 0, time.UTC)
	if g := GuessPlaces([]PlacePhoto{a, b}); len(g) != 0 {
		t.Fatalf("across midnight: %+v", g)
	}
}

func TestAnUnlocatedPhotoBetweenIsSkippedAndTheNearestLocatedOneIsUsed(t *testing.T) {
	la, lo := at(38.72, -9.14)
	loc := ph(0, la, lo, "R5")
	g := GuessPlaces([]PlacePhoto{loc, ph(2, nil, nil, "R5"), ph(4, nil, nil, "R5"), ph(6, nil, nil, "R5")})
	if len(g) != 3 {
		t.Fatalf("each of the three is offered: %d", len(g))
	}
	for i, want := range []int{2, 4, 6} {
		if g[i].Minutes != want || g[i].Near.ID != loc.ID {
			t.Errorf("guess %d: %+v", i, g[i])
		}
	}
}

func TestTheReasonOfAGuessIsKeptAndReadBack(t *testing.T) {
	la, lo := at(38.7223, -9.1393)
	near := ph(0, la, lo, "R5")
	near.City, near.Country = "Lisbon", "Portugal"
	g := GuessPlaces([]PlacePhoto{near, ph(4, nil, nil, "R5")})
	r, ok := ParsePlaceWhy(placeWhy(g[0]))
	if !ok || r.Neighbours != 1 || r.TwoSided() || r.Near != near.ID.String() || r.Minutes != 4 || r.Place != "Lisbon, Portugal" || r.Lat < 38.72 || r.Lat > 38.73 {
		t.Fatalf("read back: %+v %v", r, ok)
	}
	if _, ok := ParsePlaceWhy("a:b"); ok {
		t.Fatal("anything else is refused")
	}
	if _, ok := ParsePlaceWhy("1|99|0|x|1|1|y"); ok {
		t.Fatal("a latitude that is not one is refused")
	}
}
