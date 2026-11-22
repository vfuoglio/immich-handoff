package hub

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/safeimg"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Choosing the photos of a story (#272): look at the photos of a source once, with the owner's session, through the small picture
// Immich made of each (the same check as a drop box's best-of: too dark, too bright, blurred, or the same moment as a better shot),
// keep about storyTarget of the best spread over time, and cut them into chapters. The result is stored; the public page never
// looks at the library. See docs/contributing/design/stories-design.md.

const (
	storyTarget  = 40  // about how many photos a story shows
	storyLook    = 600 // the most photos looked at: previews are what it costs
	storyMaxList = 3000
)

// analyseAssets looks at each photo, four at a time, through its small picture, and reports how many are done.
func analyseAssets(ctx context.Context, sess *immich.API, assets []immich.AssetResponseDto, progress func(done int)) []lookedAt {
	results := make([]lookedAt, len(assets))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	var done atomic.Int64
	for i := range assets {
		results[i].a = assets[i]
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer func() {
				<-sem
				wg.Done()
				if d := int(done.Add(1)); progress != nil && (d%10 == 0 || d == len(assets)) {
					progress(d)
				}
			}()
			data, err := previewWhenReady(ctx, sess, assets[i].Id)
			if err != nil {
				return
			}
			if img, err := safeimg.Decode(data, safeimg.Preview); err == nil {
				q := analyze(img)
				results[i].q = &q
			}
		}(i)
	}
	wg.Wait()
	return results
}

// sampleSpread keeps at most max of the photos, spread evenly over the time they cover (a year of photos is looked at as 600, not all).
func sampleSpread(list []immich.AssetResponseDto, max int) []immich.AssetResponseDto {
	if len(list) <= max {
		return list
	}
	sorted := append([]immich.AssetResponseDto(nil), list...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].LocalDateTime.Before(sorted[j].LocalDateTime) })
	out := make([]immich.AssetResponseDto, 0, max)
	for i := 0; i < max; i++ {
		out = append(out, sorted[i*len(sorted)/max])
	}
	return out
}

func dayOf(t time.Time) string { return t.Format("2006-01-02") }

// bucketKey says which stretch of time a photo belongs to, for spreading the choice: a month, or, for a span of a few weeks, a day.
func bucketKey(t time.Time, short bool) string {
	if short {
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01")
}

// shortSpan says whether the photos cover under 45 days, which makes a story of a trip rather than of a year.
func shortSpan(times []time.Time) bool {
	if len(times) == 0 {
		return false
	}
	lo, hi := times[0], times[0]
	for _, t := range times {
		if t.Before(lo) {
			lo = t
		}
		if t.After(hi) {
			hi = t
		}
	}
	return hi.Sub(lo) < 45*24*time.Hour
}

// scoreOf ranks a photo that was kept: how sharp it is, with a favourite counting a quarter more.
func scoreOf(r lookedAt) float64 {
	s := 0.0
	if r.q != nil {
		s = r.q.Sharp
	}
	if r.a.IsFavorite {
		s *= 1.25
		s += 5 // a favourite that could not be looked at still stands out
	}
	return s
}

// selectStory chooses about target of the photos the check kept, spread over time: every stretch (month, or day for a short span) gets
// a share in proportion to how many photos it has and at least one, and inside a stretch the best-scoring photos win. The
// result is in time order. keep says which photos the check let through.
func selectStory(rs []lookedAt, keep []bool, target int) []int {
	short := shortSpan(func() []time.Time {
		var ts []time.Time
		for i, r := range rs {
			if keep[i] {
				ts = append(ts, r.a.LocalDateTime)
			}
		}
		return ts
	}())
	buckets := map[string][]int{}
	var keys []string
	for i, r := range rs {
		if !keep[i] {
			continue
		}
		k := bucketKey(r.a.LocalDateTime, short)
		if _, ok := buckets[k]; !ok {
			keys = append(keys, k)
		}
		buckets[k] = append(buckets[k], i)
	}
	sort.Strings(keys)
	total := 0
	for _, k := range keys {
		total += len(buckets[k])
	}
	if total == 0 {
		return nil
	}
	if total <= target {
		var all []int
		for _, k := range keys {
			all = append(all, buckets[k]...)
		}
		sortByTime(rs, all)
		return all
	}
	if len(keys) > target { // more stretches than slots: every few stretches, one photo
		var thinned []string
		for i := 0; i < target; i++ {
			thinned = append(thinned, keys[i*len(keys)/target])
		}
		keys = thinned
		total = 0
		for _, k := range keys {
			total += len(buckets[k])
		}
	}
	// a share for each stretch by the method of the largest remainders, at least one each; the weight of a stretch is the square root of
	// its photos, so a holiday month with most of the photos does not take most of the slots
	weight := 0.0
	for _, k := range keys {
		weight += math.Sqrt(float64(len(buckets[k])))
	}
	quota := map[string]int{}
	type rem struct {
		k string
		f float64
	}
	var rems []rem
	used := 0
	for _, k := range keys {
		exact := float64(target) * math.Sqrt(float64(len(buckets[k]))) / weight // a busy month counts for more, but not in proportion
		q := int(math.Floor(exact))
		if q < 1 {
			q = 1
		}
		if q > len(buckets[k]) {
			q = len(buckets[k])
		}
		quota[k] = q
		used += q
		rems = append(rems, rem{k, exact - math.Floor(exact)})
	}
	sort.SliceStable(rems, func(i, j int) bool { return rems[i].f > rems[j].f })
	for used < target {
		grew := false
		for _, r := range rems {
			if used >= target {
				break
			}
			if quota[r.k] < len(buckets[r.k]) {
				quota[r.k]++
				used++
				grew = true
			}
		}
		if !grew {
			break
		}
	}
	// over target (many stretches with one each): drop from the biggest shares
	for used > target {
		biggest := ""
		for _, k := range keys {
			if quota[k] > 1 && (biggest == "" || quota[k] > quota[biggest]) {
				biggest = k
			}
		}
		if biggest == "" {
			break
		}
		quota[biggest]--
		used--
	}
	var out []int
	for _, k := range keys {
		in := append([]int(nil), buckets[k]...)
		sort.SliceStable(in, func(a, b int) bool { return scoreOf(rs[in[a]]) > scoreOf(rs[in[b]]) })
		out = append(out, in[:quota[k]]...)
	}
	sortByTime(rs, out)
	return out
}

func sortByTime(rs []lookedAt, idx []int) {
	sort.SliceStable(idx, func(a, b int) bool { return rs[idx[a]].a.LocalDateTime.Before(rs[idx[b]].a.LocalDateTime) })
}

// pickOf describes one chosen photo for the story: when, where and who. The place is the city and country Immich knows, never the
// coordinates.
func pickOf(a immich.AssetResponseDto) store.StoryPick {
	p := store.StoryPick{Asset: uuid.UUID(a.Id).String(), Date: dayOf(a.LocalDateTime)}
	if e := a.ExifInfo; e != nil {
		var parts []string
		if e.City != nil && *e.City != "" {
			parts = append(parts, *e.City)
		}
		if e.Country != nil && *e.Country != "" {
			parts = append(parts, *e.Country)
		}
		p.Place = strings.Join(parts, ", ")
	}
	if a.People != nil {
		for _, pr := range *a.People {
			if pr.Name != "" {
				p.People = append(p.People, pr.Name)
			}
		}
	}
	return p
}

func cityOf(p store.StoryPick) string {
	city, _, _ := strings.Cut(p.Place, ",")
	return strings.TrimSpace(city)
}

// chaptersFor cuts photos in time order into chapters and says which chapter each belongs to. A span of 45 days or more is cut by
// month. A shorter one is cut by place, when most photos have a city (a run of photos in the same city is a chapter, a run of one is
// joined to the one before), and otherwise by day, or by week when there are many days. A chapter whose title is empty is shown with
// its dates, in the reader's language.
func chaptersFor(picks []store.StoryPick) []store.StoryChapter {
	if len(picks) == 0 {
		return nil
	}
	var times []time.Time
	withCity := 0
	for _, p := range picks {
		if d, err := time.Parse("2006-01-02", p.Date); err == nil {
			times = append(times, d)
		}
		if cityOf(p) != "" {
			withCity++
		}
	}
	var ch []store.StoryChapter
	assign := func(i, c int) { picks[i].Chapter = c }
	extend := func(c int, date string) {
		if ch[c].From == "" || date < ch[c].From {
			ch[c].From = date
		}
		if date > ch[c].To {
			ch[c].To = date
		}
	}
	switch {
	case !shortSpan(times):
		last := ""
		for i, p := range picks {
			key := ""
			if len(p.Date) >= 7 {
				key = p.Date[:7]
			}
			if key != last || len(ch) == 0 {
				ch = append(ch, store.StoryChapter{})
				last = key
			}
			assign(i, len(ch)-1)
			extend(len(ch)-1, p.Date)
		}
	case withCity*10 >= len(picks)*6:
		lastCity := "\x00"
		for i, p := range picks {
			city := cityOf(p)
			if city == "" {
				city = lastCity
			}
			if city != lastCity || len(ch) == 0 {
				ch = append(ch, store.StoryChapter{Title: city})
				lastCity = city
			}
			assign(i, len(ch)-1)
			extend(len(ch)-1, p.Date)
		}
		// a chapter of a single photo is joined to the one before it
		merged := ch[:0]
		remap := map[int]int{}
		counts := map[int]int{}
		for _, p := range picks {
			counts[p.Chapter]++
		}
		for c := range ch {
			if counts[c] == 1 && len(merged) > 0 {
				remap[c] = len(merged) - 1
				continue
			}
			remap[c] = len(merged)
			merged = append(merged, ch[c])
		}
		for i := range picks {
			picks[i].Chapter = remap[picks[i].Chapter]
			extend2(merged, picks[i].Chapter, picks[i].Date)
		}
		ch = merged
	default:
		days := map[string]bool{}
		for _, p := range picks {
			days[p.Date] = true
		}
		byWeek := len(days) > 12
		last := ""
		for i, p := range picks {
			key := p.Date
			if byWeek {
				if d, err := time.Parse("2006-01-02", p.Date); err == nil {
					back := (int(d.Weekday()) + 6) % 7 // Monday starts the week
					key = d.AddDate(0, 0, -back).Format("2006-01-02")
				}
			}
			if key != last || len(ch) == 0 {
				ch = append(ch, store.StoryChapter{})
				last = key
			}
			assign(i, len(ch)-1)
			extend(len(ch)-1, p.Date)
		}
	}
	return ch
}

func extend2(ch []store.StoryChapter, c int, date string) {
	if ch[c].From == "" || date < ch[c].From {
		ch[c].From = date
	}
	if date > ch[c].To {
		ch[c].To = date
	}
}

// storyFilter keeps what a story may show: photos, not trashed or archived, taken inside the story's dates.
func storyFilter(list []immich.AssetResponseDto, from, to string) []immich.AssetResponseDto {
	var out []immich.AssetResponseDto
	for _, a := range list {
		if a.Type != immich.IMAGE || a.IsTrashed || a.IsArchived {
			continue
		}
		d := dayOf(a.LocalDateTime)
		if (from != "" && d < from) || (to != "" && d > to) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// buildStory chooses the photos and chapters of a story with the owner's session and stores them.
func (h *Hub) buildStory(ctx context.Context, sess *immich.API, s store.Story) error {
	candidates, pinned, err := h.storyCandidates(ctx, sess, s)
	if err != nil {
		return err
	}
	candidates = storyFilter(candidates, s.From, s.To)
	if len(candidates) == 0 {
		return ErrStoryNoPhotos
	}
	sample := sampleSpread(candidates, storyLook)
	h.Store.SetStoryProgress(context.WithoutCancel(ctx), s.ID, 0, len(sample))
	results := analyseAssets(ctx, sess, sample, func(d int) { h.Store.SetStoryProgress(context.WithoutCancel(ctx), s.ID, d, len(sample)) })
	if err := ctx.Err(); err != nil {
		return err
	}
	verdicts := judge(results, StrictnessNamed("normal"))
	keep := make([]bool, len(results))
	for i, v := range verdicts {
		keep[i] = v.verdict == VerdictKept
		if pinned[uuid.UUID(results[i].a.Id).String()] { // the host of a drop box pinned it
			keep[i] = true
			results[i].a.IsFavorite = true
		}
	}
	chosen := selectStory(results, keep, storyTarget)
	if len(chosen) == 0 { // everything was too dark or blurred: show the best there is rather than nothing
		for i := range keep {
			keep[i] = true
		}
		chosen = selectStory(results, keep, storyTarget)
	}
	var picks []store.StoryPick
	for _, i := range chosen {
		picks = append(picks, pickOf(results[i].a))
	}
	chapters := chaptersFor(picks)
	return h.Store.SetStoryBuilt(context.WithoutCancel(ctx), s.ID, store.StoryReady, "", picks, chapters)
}

// storyCandidates reads the photos a story may be made from, with the owner's session. For a drop box it also says which photos the host
// pinned; hidden ones are left out.
func (h *Hub) storyCandidates(ctx context.Context, sess *immich.API, s store.Story) ([]immich.AssetResponseDto, map[string]bool, error) {
	from, to := dayStart(s.From), time.Time{}
	if s.To != "" {
		to = dayStart(s.To).Add(24*time.Hour - time.Second)
	}
	switch s.Kind {
	case store.StoryAlbum:
		album, _ := uuid.Parse(s.SourceID)
		list, err := sess.StoryAssets(ctx, album, uuid.Nil, from, to, storyMaxList)
		return list, nil, err
	case store.StoryPerson:
		person, _ := uuid.Parse(s.SourceID)
		list, err := sess.StoryAssets(ctx, uuid.Nil, person, from, to, storyMaxList)
		return list, nil, err
	case store.StoryRange:
		list, err := sess.StoryAssets(ctx, uuid.Nil, uuid.Nil, from, to, storyMaxList)
		return list, nil, err
	case store.StoryBox:
		items, err := h.Store.DropAssetsAll(ctx, s.SourceID, 0, 300)
		if err != nil {
			return nil, nil, err
		}
		var out []immich.AssetResponseDto
		pinned := map[string]bool{}
		for _, it := range items {
			if it.Hidden {
				continue
			}
			id, err := uuid.Parse(it.AssetID)
			if err != nil {
				continue
			}
			a, err := sess.Asset(ctx, id)
			if err != nil {
				continue
			}
			if it.Pinned {
				pinned[it.AssetID] = true
			}
			out = append(out, *a)
		}
		return out, pinned, nil
	}
	return nil, nil, fmt.Errorf("a story cannot be made from %q", s.Kind)
}
