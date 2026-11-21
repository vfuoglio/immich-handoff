package hub

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Finding the place of photos that have none (#273): a camera with no GPS leaves no place, but a photo taken minutes before or after one
// that has a place, by the same camera on the same day, was almost certainly taken there. This file is the pure part: from photos with
// their EXIF to the places to offer, and why. It is careful on purpose: a wrong place is a change in the person's library. See
// docs/design/places-design.md.

const (
	placeTwoSided   = 15 * time.Minute // a located photo this near on both sides
	placeOneSided   = 10 * time.Minute // or this near on one side
	placeMaxApart   = 2000.0           // metres: the two located photos around a guess must be this close to each other
	placeMaxSpeedKm = 60.0             // km/h between them: walking or driving, never a train or a flight
)

// PlacePhoto is a photo as the finder needs it.
type PlacePhoto struct {
	ID            uuid.UUID
	Name          string
	Taken         time.Time // when it was taken (an instant)
	Local         time.Time // the same moment on the photographer's clock, to tell the day
	Make, Model   string
	Lens, Zone    string
	Lat, Lon      *float64 // nil: no place
	City, Country string
	Created       time.Time // when it reached Immich
}

func (p PlacePhoto) located() bool { return p.Lat != nil && p.Lon != nil }

func (p PlacePhoto) camera() string {
	return strings.ToLower(strings.TrimSpace(p.Make) + "|" + strings.TrimSpace(p.Model))
}

// PlaceGuess is a photo with no place and the place it is offered.
type PlaceGuess struct {
	Photo      PlacePhoto
	Lat, Lon   float64
	Near       PlacePhoto // the located photo the place is taken from: the nearer in time
	Minutes    int        // how far in time that is
	Metres     int        // how far apart the two located photos around it are (0: there was one side only)
	TwoSided   bool       // a located photo on both sides: the only kind that may be written without asking
	Place      string     // the city and country of the neighbour, if Immich has them
	Neighbours int        // 1 or 2
}

func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000.0
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp, dl := (lat2-lat1)*math.Pi/180, (lon2-lon1)*math.Pi/180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(a)))
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// usable says whether a located photo can lend its place to an unlocated one: the same camera (and lens when both have one), the same
// local day, the same time zone when both carry one.
func usable(p, n PlacePhoto) bool {
	if !n.located() || n.camera() != p.camera() || !sameDay(p.Local, n.Local) {
		return false
	}
	if p.Lens != "" && n.Lens != "" && p.Lens != n.Lens {
		return false
	}
	if p.Zone != "" && n.Zone != "" && p.Zone != n.Zone {
		return false
	}
	return true
}

// GuessPlaces finds, for each photo that has no place, the place to offer it: the photos are taken per camera in the order they were
// taken; the nearest located photo before and after (skipping other photos with no place) are the neighbours. With a neighbour on both
// sides inside 15 minutes the guess needs the two to be at most 2 km apart and at most 60 km/h from each other, or there is none (the
// camera travelled); with a neighbour on one side only, it needs to be inside 10 minutes. The place is the nearer neighbour's.
func GuessPlaces(photos []PlacePhoto) []PlaceGuess {
	byCamera := map[string][]PlacePhoto{}
	for _, p := range photos {
		if p.Make == "" || p.Taken.IsZero() {
			continue // no camera, or a time that is only when it was uploaded
		}
		byCamera[p.camera()] = append(byCamera[p.camera()], p)
	}
	var out []PlaceGuess
	for _, list := range byCamera {
		sort.SliceStable(list, func(i, j int) bool { return list[i].Taken.Before(list[j].Taken) })
		for i, p := range list {
			if p.located() {
				continue
			}
			var before, after *PlacePhoto
			for j := i - 1; j >= 0; j-- {
				if list[j].located() {
					before = &list[j]
					break
				}
			}
			for j := i + 1; j < len(list); j++ {
				if list[j].located() {
					after = &list[j]
					break
				}
			}
			near := func(n *PlacePhoto, window time.Duration) *PlacePhoto {
				if n == nil || !usable(p, *n) {
					return nil
				}
				d := n.Taken.Sub(p.Taken)
				if d < 0 {
					d = -d
				}
				if d > window {
					return nil
				}
				return n
			}
			b2, a2 := near(before, placeTwoSided), near(after, placeTwoSided)
			g := PlaceGuess{Photo: p}
			switch {
			case b2 != nil && a2 != nil:
				apart := haversine(*b2.Lat, *b2.Lon, *a2.Lat, *a2.Lon)
				gap := a2.Taken.Sub(b2.Taken).Hours()
				if apart > placeMaxApart || (gap > 0 && apart/1000/gap > placeMaxSpeedKm) {
					continue // the camera travelled: it could have been anywhere in between
				}
				g.TwoSided, g.Neighbours, g.Metres = true, 2, int(apart+0.5)
				g.Near = *b2
				if a2.Taken.Sub(p.Taken) < p.Taken.Sub(b2.Taken) {
					g.Near = *a2
				}
			default:
				one := near(before, placeOneSided)
				if one == nil {
					one = near(after, placeOneSided)
				}
				if one == nil {
					continue
				}
				// a located photo on the other side that is further than the window but plainly elsewhere also makes it a guess
				// worth asking about, not one to write by itself (it is one-sided anyway)
				g.Neighbours, g.Near = 1, *one
			}
			g.Lat, g.Lon = *g.Near.Lat, *g.Near.Lon
			d := g.Near.Taken.Sub(p.Taken)
			if d < 0 {
				d = -d
			}
			g.Minutes = int(d.Minutes() + 0.5)
			g.Place = placeWords(g.Near.City, g.Near.Country)
			out = append(out, g)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Photo.Taken.Before(out[j].Photo.Taken) })
	return out
}

func placeWords(city, country string) string {
	switch {
	case city != "" && country != "":
		return city + ", " + country
	case city != "":
		return city
	}
	return country
}

// placeWhy packs a guess into the reason a rule keeps for a photo: how many sides, the place, the neighbour, how far in time and in
// metres, and the words of the place.
func placeWhy(g PlaceGuess) string {
	words := strings.NewReplacer("|", " ", "\n", " ").Replace(g.Place)
	return fmt.Sprintf("%d|%s|%s|%s|%d|%d|%s", g.Neighbours,
		strconv.FormatFloat(g.Lat, 'f', 6, 64), strconv.FormatFloat(g.Lon, 'f', 6, 64), g.Near.ID, g.Minutes, g.Metres, words)
}

// PlaceReason is what a rule kept about a guess, read back from its photo row.
type PlaceReason struct {
	Neighbours int
	Lat, Lon   float64
	Near       string // the neighbour's id in Immich
	Minutes    int
	Metres     int
	Place      string
}

// ParsePlaceWhy reads what placeWhy wrote; ok is false for anything else.
func ParsePlaceWhy(why string) (PlaceReason, bool) {
	f := strings.SplitN(why, "|", 7)
	if len(f) != 7 {
		return PlaceReason{}, false
	}
	var r PlaceReason
	var err error
	if r.Neighbours, err = strconv.Atoi(f[0]); err != nil {
		return r, false
	}
	if r.Lat, err = strconv.ParseFloat(f[1], 64); err != nil {
		return r, false
	}
	if r.Lon, err = strconv.ParseFloat(f[2], 64); err != nil {
		return r, false
	}
	r.Near = f[3]
	if r.Minutes, err = strconv.Atoi(f[4]); err != nil {
		return r, false
	}
	if r.Metres, err = strconv.Atoi(f[5]); err != nil {
		return r, false
	}
	r.Place = f[6]
	return r, r.Lat >= -90 && r.Lat <= 90 && r.Lon >= -180 && r.Lon <= 180
}

// TwoSided says whether the guess had a located photo on both sides: only such a guess may be written without asking.
func (r PlaceReason) TwoSided() bool { return r.Neighbours == 2 }
