package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/vfuoglio/immich-handoff/internal/immich"
)

// Quiet is when a service hands photos over. Outside the window, photos wait, still tagged, and go when it opens.
// With LargeMB set only files larger than that wait; smaller ones go at once. From and To are minutes after midnight in
// the app's time zone (TZ); a window that ends before it starts crosses midnight; From == To means no window.
type Quiet struct {
	From, To int
	LargeMB  int
}

// Set reports whether there is a window at all.
func (q Quiet) Set() bool { return q.From != q.To }

// Open reports whether the window is open at the given time.
func (q Quiet) Open(now time.Time) bool {
	if !q.Set() {
		return true
	}
	m := now.Hour()*60 + now.Minute()
	if q.From < q.To {
		return m >= q.From && m < q.To
	}
	return m >= q.From || m < q.To
}

// NextOpen is when the window next opens, strictly after now.
func (q Quiet) NextOpen(now time.Time) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), q.From/60, q.From%60, 0, 0, now.Location())
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// Holds reports whether a file of this size has to wait at this moment.
func (q Quiet) Holds(bytes int64, now time.Time) bool {
	if !q.Set() || q.Open(now) {
		return false
	}
	return q.LargeMB == 0 || bytes > int64(q.LargeMB)<<20
}

// ParseClock reads "HH:MM" (24 hours) as minutes after midnight.
func ParseClock(s string) (int, error) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, fmt.Errorf("a time is written HH:MM, not %q", s)
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("a time is written HH:MM between 00:00 and 23:59, not %q", s)
	}
	return hh*60 + mm, nil
}

// Clock writes minutes after midnight as "HH:MM".
func Clock(minutes int) string { return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60) }

// ParseQuiet reads "HH:MM-HH:MM" (for example "01:00-06:00"); an empty text is no window.
func ParseQuiet(spec string) (Quiet, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Quiet{}, nil
	}
	a, b, ok := strings.Cut(spec, "-")
	if !ok {
		return Quiet{}, fmt.Errorf("quiet hours are written HH:MM-HH:MM, for example 01:00-06:00, not %q", spec)
	}
	from, err := ParseClock(a)
	if err != nil {
		return Quiet{}, err
	}
	to, err := ParseClock(b)
	if err != nil {
		return Quiet{}, err
	}
	return Quiet{From: from, To: to}, nil
}

// Accept is what a service takes in; whatever does not match is not handed over. The tagged photo is the one judged:
// a stack or a live photo goes or stays as one.
type Accept struct {
	MinRating   int    // 0: any; 1 to 5: the photo needs at least this many stars
	PhotosOnly  bool   // no videos
	Orientation string // "": any, "landscape" or "portrait"
	MaxMB       int    // 0: no limit; the largest file
}

// Active reports whether any rule is set.
func (a Accept) Active() bool {
	return a.MinRating > 0 || a.PhotosOnly || a.Orientation != "" || a.MaxMB > 0
}

// NeedsDetail reports whether judging needs what only the photo itself tells (the rating, the real dimensions), which
// a search result may not carry.
func (a Accept) NeedsDetail() bool { return a.MinRating > 0 || a.Orientation != "" }

// Check says whether a photo matches. A rule that cannot be checked because Immich did not say (no dimensions, no
// size) lets the photo through: the app never refuses for something it does not know.
func (a Accept) Check(p immich.AssetResponseDto) bool {
	if a.PhotosOnly && p.Type != immich.IMAGE {
		return false
	}
	e := p.ExifInfo
	if a.MinRating > 0 {
		if e == nil || e.Rating == nil || *e.Rating < a.MinRating {
			return false
		}
	}
	if a.MaxMB > 0 && e != nil && e.FileSizeInByte != nil && int64(*e.FileSizeInByte) > int64(a.MaxMB)<<20 {
		return false
	}
	if a.Orientation != "" && e != nil && e.ExifImageWidth != nil && e.ExifImageHeight != nil {
		w, h := *e.ExifImageWidth, *e.ExifImageHeight
		if e.Orientation != nil {
			switch *e.Orientation { // the camera was turned: the picture is shown the other way round
			case "5", "6", "7", "8":
				w, h = h, w
			}
		}
		if w != h {
			if (a.Orientation == "landscape") != (w > h) {
				return false
			}
		}
	}
	return true
}

// A photo tagged for a date: the service's tag, a separator, and an ISO date (share/2027-06-01, "share 2027-06-01",
// share-2027-06-01 or share_2027-06-01). It is handed over when the day comes.
func scheduledFor(tagValue, base string) (time.Time, bool) {
	rest, ok := strings.CutPrefix(tagValue, base)
	if !ok || len(rest) != 11 || !strings.ContainsRune("/ _-", rune(rest[0])) {
		return time.Time{}, false
	}
	d, err := time.ParseInLocation("2006-01-02", rest[1:], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return d, true
}

// QuietWait says a sender's photos are waiting for the quiet hours.
type QuietWait struct {
	Waiting int
	Until   time.Time // when the window opens
}

type wakeKey struct {
	acct *Account
	what string
}

// quietOf is the quiet hours that apply to a service: its own, else the app-wide default.
func (s *Syncer) quietOf(svc *Service) Quiet {
	if svc.Quiet.Set() {
		return svc.Quiet
	}
	return s.Cfg.Quiet
}

// QuietOf is the quiet hours that apply to a service (its own, else the app-wide ones).
func (s *Syncer) QuietOf(svc *Service) Quiet { return s.quietOf(svc) }

// QuietFor reports whether a sender's photos are waiting for the quiet hours, and until when.
func (s *Syncer) QuietFor(a *Account) (QuietWait, bool) {
	s.heldMu.Lock()
	defer s.heldMu.Unlock()
	w, ok := s.quiet[a]
	return w, ok
}

// wakeAt arranges a pass at the given time for a sender, replacing an earlier one of the same kind (what). A zero time
// cancels it. Callers hold heldMu.
func (s *Syncer) wakeAt(ctx context.Context, snd *Account, what string, at time.Time, reason string) {
	if s.timers == nil {
		s.timers = map[wakeKey]*time.Timer{}
	}
	k := wakeKey{snd, what}
	if t := s.timers[k]; t != nil {
		t.Stop()
		delete(s.timers, k)
	}
	if at.IsZero() {
		return
	}
	d := time.Until(at) + time.Second
	if d < time.Second {
		d = time.Second
	}
	s.timers[k] = time.AfterFunc(d, func() { s.Run(ctx, reason, false) })
}

// quietWait records that waiting photos are held back by the quiet hours (or that none are), and sees to a pass when
// the window opens.
func (s *Syncer) quietWait(ctx context.Context, log *slog.Logger, snd *Account, q Quiet, waiting int, now time.Time) {
	s.heldMu.Lock()
	defer s.heldMu.Unlock()
	if waiting == 0 {
		delete(s.quiet, snd)
		s.wakeAt(ctx, snd, "quiet", time.Time{}, "")
		return
	}
	if s.quiet == nil {
		s.quiet = map[*Account]QuietWait{}
	}
	until := q.NextOpen(now)
	prev, was := s.quiet[snd]
	s.quiet[snd] = QuietWait{Waiting: waiting, Until: until}
	s.wakeAt(ctx, snd, "quiet", until, "quiet hours began")
	if !was || prev.Waiting != waiting {
		log.Info("quiet hours: photos wait", "waiting", waiting, "until", until.Format(time.RFC3339))
	}
}

func sizeOfPhoto(a immich.AssetResponseDto) int64 {
	if a.ExifInfo == nil || a.ExifInfo.FileSizeInByte == nil {
		return 0
	}
	return int64(*a.ExifInfo.FileSizeInByte)
}

// accepted judges a tagged photo by the service's rules. A rating and the real dimensions are only certain on the photo
// itself, which a search result may not carry, so it is read again when a rule needs them.
func (s *Syncer) accepted(ctx context.Context, log *slog.Logger, snd *Account, svc *Service, a *immich.AssetResponseDto) bool {
	if svc.Accept.NeedsDetail() {
		if full, err := snd.API.Asset(ctx, a.Id); err == nil && full != nil {
			*a = *full
		} else if err != nil {
			log.Warn("could not read the photo to judge it; judging what the search gave", "asset", a.Id, "err", err)
		}
	}
	return svc.Accept.Check(*a)
}

// scheduled looks for photos the sender tagged for a date (the service's tag and a date: share/2027-06-01). Those whose
// day has come are added to the photos to hand over, with the tag that says so; the others stay where they are, even
// when a search for the plain tag lists them too, and a pass is arranged for the first day that comes.
func (s *Syncer) scheduled(ctx context.Context, log *slog.Logger, snd *Account, svc *Service, assets []immich.AssetResponseDto, now time.Time) ([]immich.AssetResponseDto, map[uuid.UUID]immich.TagInfo) {
	tags, err := snd.API.Tags(ctx)
	if err != nil {
		log.Warn("could not list the tags to look for photos tagged for a date", "err", err)
		return assets, nil
	}
	due := map[uuid.UUID]immich.TagInfo{}
	future := map[uuid.UUID]bool{}
	var next time.Time
	for _, t := range tags {
		day, ok := scheduledFor(t.Value, svc.Tag)
		if !ok {
			continue
		}
		found, err := snd.API.Search(ctx, immich.MetadataSearchDto{TagIds: &[]uuid.UUID{t.ID}})
		if err != nil {
			log.Warn("could not search a tag for a date", "tag", t.Value, "err", err)
			continue
		}
		if !day.After(now) {
			have := map[uuid.UUID]bool{}
			for _, a := range assets {
				have[a.Id] = true
			}
			for _, a := range found {
				due[a.Id] = t
				if !have[a.Id] {
					assets = append(assets, a)
				}
			}
			continue
		}
		for _, a := range found {
			future[a.Id] = true
		}
		if len(found) > 0 && (next.IsZero() || day.Before(next)) {
			next = day
		}
	}
	if len(future) > 0 {
		kept := assets[:0]
		for _, a := range assets {
			if !future[a.Id] {
				kept = append(kept, a)
			}
		}
		assets = kept
		log.Info("photos tagged for a later date wait", "count", len(future), "first", next.Format("2006-01-02"))
	}
	s.heldMu.Lock()
	s.wakeAt(ctx, snd, "dated", next, "a date photos were tagged for has come")
	s.heldMu.Unlock()
	return assets, due
}

// Validate says what is wrong with a set of rules, if anything.
func (a Accept) Validate() error {
	switch {
	case a.MinRating < 0 || a.MinRating > 5:
		return fmt.Errorf("the minimum rating is from 0 (any) to 5 stars, not %d", a.MinRating)
	case a.Orientation != "" && a.Orientation != "landscape" && a.Orientation != "portrait":
		return fmt.Errorf("the orientation is landscape or portrait (or empty for any), not %q", a.Orientation)
	case a.MaxMB < 0 || a.MaxMB > 1_000_000:
		return fmt.Errorf("the largest file is from 0 (no limit) to 1000000 MB, not %d", a.MaxMB)
	}
	return nil
}

// Validate says what is wrong with quiet hours, if anything.
func (q Quiet) Validate() error {
	if q.From < 0 || q.From >= 1440 || q.To < 0 || q.To >= 1440 {
		return fmt.Errorf("quiet hours are between 00:00 and 23:59")
	}
	if q.LargeMB < 0 || q.LargeMB > 1_000_000 {
		return fmt.Errorf("the size that waits for the quiet hours is from 0 (everything) to 1000000 MB, not %d", q.LargeMB)
	}
	return nil
}
