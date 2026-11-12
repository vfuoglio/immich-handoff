package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

var (
	// ErrDigestOff: the service has no digest turned on.
	ErrDigestOff = errors.New("turn the weekly digest on for this service first")
	// ErrDigestRunning: a digest is being made for the service right now.
	ErrDigestRunning = errors.New("a digest is already being made for this service")
)

// The digest keeps at most this many photos, and its shared link ends after this long.
const (
	digestMax      = 40
	digestLinkLife = 30 * 24 * time.Hour
	digestHour     = 8 // the digest is made on Monday at about this hour, in the server's time zone
)

// VerdictMore marks a photo the digest left out only because the week had more good ones than fit.
const VerdictMore = "more"

// DigestLeft is a photo a digest left out, and why (a code the page turns into words, and a detail).
type DigestLeft struct {
	Name   string `json:"n"`
	Reason string `json:"r"`
	Detail string `json:"d,omitempty"`
}

// digestWeekStart is the Monday, at digestHour in the server's time zone, on or before t. A week's digest is made once,
// from then on.
func digestWeekStart(t time.Time) time.Time {
	t = t.In(time.Local)
	day := time.Date(t.Year(), t.Month(), t.Day(), digestHour, 0, 0, 0, time.Local)
	back := (int(day.Weekday()) + 6) % 7 // days since Monday
	start := day.AddDate(0, 0, -back)
	if start.After(t) {
		start = start.AddDate(0, 0, -7)
	}
	return start
}

func weekKey(start time.Time) string { return start.Format("2006-01-02") }

// EnableDigest turns on the weekly digest of a service: the service's receiver key is given what it needs to look at
// pictures, make an album and a shared link (with the owner's own session; a key cannot change itself), and the first
// digest is made next Monday. Only the owner may.
func (h *Hub) EnableDigest(ctx context.Context, sess *immich.API, owner immich.User, service string) error {
	v, err := h.ownedService(ctx, owner, service)
	if err != nil {
		return err
	}
	have, err := sess.KeyPermissions(ctx, v.KeyID)
	if err != nil {
		return fmt.Errorf("could not read the service's key: %w", err)
	}
	has := map[immich.Permission]bool{}
	for _, p := range have {
		has[p] = true
	}
	d, _, err := h.Store.DigestFor(ctx, service)
	if err != nil {
		return err
	}
	added := map[string]bool{}
	for _, p := range strings.Split(d.Added, ",") {
		if p != "" {
			added[p] = true
		}
	}
	for _, p := range immich.DigestPermissions {
		if has[p] || has["all"] {
			continue
		}
		if err := sess.AddKeyPermission(ctx, v.KeyID, p); err != nil {
			return fmt.Errorf("could not let the service's key make the digest: %w", err)
		}
		added[string(p)] = true
	}
	d.Service, d.Enabled, d.Added = service, true, joinSet(added)
	d.Week = weekKey(digestWeekStart(h.now())) // the first digest is next Monday's
	if err := h.Store.SetDigest(ctx, d); err != nil {
		return err
	}
	h.event(ctx, service, owner.ID, "digest-on", i18n.Pack("{name} turned on the weekly digest of {service}", "name", owner.Name, "service", service))
	return nil
}

// DisableDigest turns the digest off: its shared link ends and what it added to the key is taken away. The albums it made
// stay, they are the owner's.
func (h *Hub) DisableDigest(ctx context.Context, sess *immich.API, owner immich.User, service string) error {
	v, err := h.ownedService(ctx, owner, service)
	if err != nil {
		return err
	}
	d, ok, err := h.Store.DigestFor(ctx, service)
	if err != nil || !ok {
		return err
	}
	h.endDigestLink(ctx, v, d)
	for _, p := range strings.Split(d.Added, ",") {
		if p == "" {
			continue
		}
		if err := sess.RemoveKeyPermission(ctx, v.KeyID, immich.Permission(p)); err != nil {
			h.Log.Warn("turn off the digest: could not take a permission off the key", "permission", p, "err", err)
		}
	}
	d.Enabled, d.Added, d.LinkID, d.LinkKey, d.LinkEnds = false, "", "", "", time.Time{}
	if err := h.Store.SetDigest(ctx, d); err != nil {
		return err
	}
	h.event(ctx, service, owner.ID, "digest-off", i18n.Pack("{name} turned off the weekly digest of {service}", "name", owner.Name, "service", service))
	return nil
}

func joinSet(m map[string]bool) string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// endDigestLink ends the shared link of the last digest, if there is one (best effort: it also ends by itself).
func (h *Hub) endDigestLink(ctx context.Context, v *store.Service, d store.Digest) {
	if d.LinkID == "" {
		return
	}
	id, err := uuid.Parse(d.LinkID)
	if err != nil {
		return
	}
	api, err := h.Connect(v.Key)
	if err != nil {
		return
	}
	if err := api.RemoveLink(ctx, id); err != nil {
		h.Log.Debug("could not end a digest's shared link", "service", v.Name, "err", err)
	}
}

// MakeDigestNow makes a digest of the last seven days at once, in the background. Only the owner may, and only with the
// digest on.
func (h *Hub) MakeDigestNow(ctx context.Context, owner immich.User, service string) error {
	v, err := h.ownedService(ctx, owner, service)
	if err != nil {
		return err
	}
	d, ok, err := h.Store.DigestFor(ctx, service)
	if err != nil {
		return err
	}
	if !ok || !d.Enabled {
		return ErrDigestOff
	}
	return h.startDigest(ctx, v, d, h.now().Add(-7*24*time.Hour), "")
}

// DigestMaintenance makes the digests that are due: those turned on whose week has no digest yet. It is called by the
// same ticker as the drop boxes' reminders, and starts each in the background.
func (h *Hub) DigestMaintenance(ctx context.Context) {
	if h.Store == nil {
		return
	}
	on, err := h.Store.DigestsEnabled(ctx)
	if err != nil || len(on) == 0 {
		return
	}
	start := digestWeekStart(h.now())
	week := weekKey(start)
	services, err := h.Store.Services(ctx)
	if err != nil {
		return
	}
	for _, d := range on {
		if d.Week == week {
			continue
		}
		for i := range services {
			if services[i].Name == d.Service {
				if err := h.startDigest(ctx, &services[i], d, start.AddDate(0, 0, -7), week); err != nil && !errors.Is(err, ErrDigestRunning) {
					h.Log.Warn("could not start a digest", "service", d.Service, "err", err)
				}
			}
		}
	}
}

func (h *Hub) startDigest(ctx context.Context, v *store.Service, d store.Digest, since time.Time, week string) error {
	h.mu.Lock()
	if h.digestRun == nil {
		h.digestRun = map[string]bool{}
	}
	if h.digestRun[v.Name] {
		h.mu.Unlock()
		return ErrDigestRunning
	}
	h.digestRun[v.Name] = true
	h.mu.Unlock()
	vc := *v
	go func() {
		defer func() {
			h.mu.Lock()
			delete(h.digestRun, vc.Name)
			h.mu.Unlock()
		}()
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
		defer cancel()
		if err := h.runDigest(rctx, &vc, d, since, week); err != nil {
			h.Log.Warn("a digest failed", "service", vc.Name, "err", err)
			cur, _, _ := h.Store.DigestFor(context.WithoutCancel(rctx), vc.Name)
			cur.Service, cur.Note = vc.Name, "The digest could not be made: "+err.Error()
			if week != "" {
				cur.Week = week // tried once this week; the owner can make it again by hand
			}
			_ = h.Store.SetDigest(context.WithoutCancel(rctx), cur)
		}
	}()
	return nil
}

// DigestRunning says whether a digest is being made for the service now.
func (h *Hub) DigestRunning(service string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.digestRun[service]
}

type arrived struct {
	a    immich.AssetResponseDto
	from string
}

// runDigest makes one digest: it looks at what arrived in the service since the given time (from the hand-over ledger,
// not from a search), leaves out what the best-of rules reject, keeps the best forty, puts them in a new album of the
// owner's and makes a shared link that ends after a month. It changes no photo.
func (h *Hub) runDigest(ctx context.Context, v *store.Service, prev store.Digest, since time.Time, week string) error {
	api, err := h.Connect(v.Key)
	if err != nil {
		return err
	}
	links, err := h.Store.Links(ctx)
	if err != nil {
		return err
	}
	var ids []string
	names := map[string]string{}
	for _, l := range links {
		if l.Service == v.Name && l.Role == store.RoleSender {
			ids = append(ids, l.ID)
			names[l.ID] = l.Name
		}
	}
	items, err := h.Store.HandoverItemsSince(ctx, ids, since)
	if err != nil {
		return err
	}
	d := prev
	d.Service, d.MadeAt, d.Note = v.Name, h.now(), ""
	if week != "" {
		d.Week = week
	}
	seen := map[string]bool{}
	var got []arrived
	for _, it := range items {
		if seen[it.ReceiverAsset] {
			continue
		}
		seen[it.ReceiverAsset] = true
		id, err := uuid.Parse(it.ReceiverAsset)
		if err != nil {
			continue
		}
		a, err := api.Asset(ctx, id)
		if err != nil {
			if immich.IsAuthError(err) {
				return err
			}
			continue // gone, or not readable: it is not in the digest
		}
		if a == nil || a.IsTrashed {
			continue
		}
		got = append(got, arrived{*a, names[it.LinkID]})
	}
	if len(got) == 0 {
		d.Photos, d.People, d.LeftOut, d.LeftJSON, d.Note = 0, 0, 0, "", "Nothing arrived this week."
		return h.Store.SetDigest(ctx, d)
	}

	results := make([]lookedAt, len(got))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	var done atomic.Int64
	for i := range got {
		results[i].a = got[i].a
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer func() { <-sem; wg.Done(); done.Add(1) }()
			data, err := previewWhenReady(ctx, api, got[i].a.Id)
			if err != nil {
				return
			}
			if img, _, err := image.Decode(bytes.NewReader(data)); err == nil {
				q := analyze(img)
				results[i].q = &q
			}
		}(i)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}

	verdicts := judge(results, StrictnessNamed("normal"))
	var kept []int
	for i, vd := range verdicts {
		if vd.verdict == VerdictKept {
			kept = append(kept, i)
		}
	}
	if len(kept) > digestMax { // the sharpest of what is left; a photo that could not be looked at counts as the least sharp
		sharp := func(i int) float64 {
			if results[i].q == nil {
				return -1
			}
			return results[i].q.Sharp
		}
		sort.SliceStable(kept, func(x, y int) bool { return sharp(kept[x]) > sharp(kept[y]) })
		for _, i := range kept[digestMax:] {
			verdicts[i] = judgement{VerdictMore, fmt.Sprintf("more than the %d that fit", digestMax)}
		}
		kept = kept[:digestMax]
	}
	keep := map[uuid.UUID]bool{}
	people := map[string]bool{}
	for _, i := range kept {
		keep[results[i].a.Id] = true
		people[got[i].from] = true
	}
	var left []DigestLeft
	for i, vd := range verdicts {
		if vd.verdict != VerdictKept {
			left = append(left, DigestLeft{Name: results[i].a.OriginalFileName, Reason: vd.verdict, Detail: vd.detail})
		}
	}
	d.LeftOut = len(left)
	if len(left) > 100 {
		left = left[:100]
	}
	lj, _ := json.Marshal(left)
	d.LeftJSON = string(lj)
	if len(kept) == 0 {
		d.Photos, d.People, d.Note = 0, 0, "Every photo of the week was left out (see why below)."
		return h.Store.SetDigest(ctx, d)
	}

	label := since.In(time.Local).Format("2006-01-02")
	album := fmt.Sprintf("%s week of %s", v.Name, label)
	all := make([]immich.AssetResponseDto, len(got))
	for i := range got {
		all[i] = got[i].a
	}
	if err := h.fillAlbum(ctx, api, album, all, keep); err != nil {
		return fmt.Errorf("could not fill the album %q: %w", album, err)
	}
	albumID, _, err := api.FindAlbum(ctx, album)
	if err != nil || albumID == nil {
		return fmt.Errorf("could not find the album %q: %v", album, err)
	}
	h.endDigestLink(ctx, v, prev) // last week's link ends: a person has one to follow
	ends := h.now().Add(digestLinkLife)
	linkID, linkKey, err := api.CreateAlbumLink(ctx, *albumID, ends, "", fmt.Sprintf("%s, week of %s", v.Name, label))
	if err != nil {
		return fmt.Errorf("could not make the shared link: %w", err)
	}
	d.Album, d.LinkID, d.LinkKey, d.LinkEnds = album, linkID.String(), linkKey, ends
	d.Photos, d.People = len(kept), len(people)
	if err := h.Store.SetDigest(ctx, d); err != nil {
		return err
	}
	h.ownerEvent(ctx, v.OwnerID, "digest", i18n.PackN("The weekly digest of {service} is ready: {n} photo of the week", "The weekly digest of {service} is ready: {n} photos of the week", len(kept), "service", v.Name))
	h.Log.Info("digest made", "service", v.Name, "photos", len(kept), "left out", d.LeftOut)
	return nil
}

// ownerEvent writes a line only the owner sees in their Activity (it names no service, so the service's members do not
// get it) and tells them through the channels they asked for.
func (h *Hub) ownerEvent(ctx context.Context, owner uuid.UUID, kind, text string) {
	if err := h.Store.AddEvent(context.WithoutCancel(ctx), store.Event{UserID: owner, Kind: kind, Text: text}); err != nil {
		h.Log.Warn("could not write the activity log", "err", err)
	}
	if h.People != nil {
		h.People.Tell(ctx, []uuid.UUID{owner}, kind, text)
	}
	if h.Push != nil {
		h.Push.Tell(ctx, []uuid.UUID{owner}, kind, text)
	}
}

// DigestInfo is what the owner's card shows about a service's digest.
type DigestInfo struct {
	On                   bool
	Running              bool
	Made                 time.Time
	Album                string
	LinkKey              string
	Ends                 time.Time
	Photos, People, Left int
	LeftList             []DigestLeft
	Note                 string
	Next                 time.Time // when the next one is made
}

// DigestFor describes a service's digest for its owner.
func (h *Hub) DigestFor(ctx context.Context, service string) DigestInfo {
	var info DigestInfo
	if h.Store == nil {
		return info
	}
	d, ok, err := h.Store.DigestFor(ctx, service)
	if err != nil || !ok {
		return info
	}
	info = DigestInfo{On: d.Enabled, Running: h.DigestRunning(service), Made: d.MadeAt, Album: d.Album, LinkKey: d.LinkKey, Ends: d.LinkEnds,
		Photos: d.Photos, People: d.People, Left: d.LeftOut, Note: d.Note}
	if d.LeftJSON != "" {
		_ = json.Unmarshal([]byte(d.LeftJSON), &info.LeftList)
	}
	if !d.LinkEnds.IsZero() && d.LinkEnds.Before(h.now()) {
		info.LinkKey = "" // it has ended
	}
	if d.Enabled {
		info.Next = digestWeekStart(h.now()).AddDate(0, 0, 7)
		if d.Week != weekKey(digestWeekStart(h.now())) {
			info.Next = digestWeekStart(h.now()) // due now: the next ticker makes it
		}
	}
	return info
}
