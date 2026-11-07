// Package syncer hands tagged assets from sender accounts to receiver accounts, per service.
package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
)

// Config holds the options that apply to every service.
type Config struct {
	DeleteForce bool // delete originals permanently instead of trashing them
	DryRun      bool
	NotifyURL   string
}

type Counts struct {
	Imported, Duplicate, Refused, DryRun, Errors int
}

func (c *Counts) add(o Counts) {
	c.Imported += o.Imported
	c.Duplicate += o.Duplicate
	c.Refused += o.Refused
	c.DryRun += o.DryRun
	c.Errors += o.Errors
}

// RunInfo is the in-memory record of the last pass (the app is stateless by design).
type RunInfo struct {
	Started, Finished time.Time
	Reason            string
	Counts            Counts
	PerService        map[string]Counts
	Untagged          int
	Error             string
}

type Syncer struct {
	Cfg      Config
	Services []*Service // fixed services (used when Provider is nil)
	Provider Provider   // optional: services that can change while running
	Log      *slog.Logger

	mu          sync.Mutex
	provMu      sync.Mutex
	hintMu      sync.Mutex
	hints       map[uuid.UUID]time.Time // asset id -> first seen
	retryQueued atomic.Bool
	rerun       atomic.Bool
	last        atomic.Pointer[RunInfo]

	subMu sync.Mutex
	subs  map[chan struct{}]struct{}

	totMu  sync.Mutex
	totals Totals

	recentMu sync.Mutex
	recent   map[uuid.UUID]time.Time // photos handled a moment ago

	albumMu     sync.Mutex
	albumBroken map[string]bool // album problems already reported, so each is reported once, not every pass
	lastPublish atomic.Int64    // when the open pages were last told something changed (see publishSoon)
}

// Totals are cumulative since the app started (kept in memory only).
type Totals struct {
	Passes int
	Counts Counts
}

// Totals returns a snapshot of the cumulative counters.
func (s *Syncer) Totals() Totals {
	s.totMu.Lock()
	defer s.totMu.Unlock()
	return s.totals
}

// Subscribe returns a channel that receives a signal whenever a pass finishes, and a function that
// ends the subscription. Signals are not queued: a slow reader just sees the latest state.
func (s *Syncer) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.subMu.Lock()
	if s.subs == nil {
		s.subs = map[chan struct{}]struct{}{}
	}
	s.subs[ch] = struct{}{}
	s.subMu.Unlock()
	return ch, func() {
		s.subMu.Lock()
		delete(s.subs, ch)
		s.subMu.Unlock()
	}
}

func (s *Syncer) publish() {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *Syncer) Last() *RunInfo { return s.last.Load() }

// AddHint records an asset id taken from a webhook payload. A hint is never trusted: the next pass
// fetches the asset with the source account's own key and checks it really carries the share tag.
// Immich can announce a tag before the tag is visible through its API (search and lookups alike), so
// hints that cannot be confirmed yet are retried every few seconds for hintTTL.
func (s *Syncer) AddHint(id uuid.UUID) {
	s.hintMu.Lock()
	defer s.hintMu.Unlock()
	if s.hints == nil {
		s.hints = map[uuid.UUID]time.Time{}
	}
	if _, ok := s.hints[id]; !ok {
		s.hints[id] = time.Now()
	}
}

func (s *Syncer) takeHints() map[uuid.UUID]time.Time {
	s.hintMu.Lock()
	defer s.hintMu.Unlock()
	out := s.hints
	s.hints = nil
	return out
}

// justHandledFor is how long a photo that was just handed over is left alone if Immich's search still
// lists it. Immich's search lags behind tag removals, as it does behind additions, so without this a
// handed-over photo can come back for several passes and be downloaded and uploaded again each time.
const justHandledFor = 2 * time.Minute

func (s *Syncer) justHandled(id uuid.UUID) bool {
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	t, ok := s.recent[id]
	if ok && time.Since(t) > justHandledFor {
		delete(s.recent, id)
		return false
	}
	return ok
}

func (s *Syncer) markJustHandled(id uuid.UUID) {
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	if s.recent == nil {
		s.recent = map[uuid.UUID]time.Time{}
	}
	now := time.Now()
	for k, t := range s.recent { // keep the map small
		if now.Sub(t) > justHandledFor {
			delete(s.recent, k)
		}
	}
	s.recent[id] = now
}

const (
	hintTTL   = 90 * time.Second // after this the daily sweep is the only retry
	hintRetry = 2 * time.Second
)

// retryHints re-queues hints that no source could confirm and schedules one more pass for them.
// Events whose asset never ends up carrying the share tag (untagged quickly, or retagged after a
// refusal) are normal and simply expire quietly.
func (s *Syncer) retryHints(ctx context.Context, hints map[uuid.UUID]time.Time, resolved map[uuid.UUID]bool) {
	pending := false
	s.hintMu.Lock()
	for id, first := range hints {
		age := time.Since(first)
		switch {
		case resolved[id]:
			if age > time.Second {
				s.Log.Info("tag became visible after a delay", "asset", id, "after", age.Round(100*time.Millisecond).String())
			}
		case age > hintTTL:
			s.Log.Debug("event hint expired without the share tag appearing", "asset", id)
		default:
			if s.hints == nil {
				s.hints = map[uuid.UUID]time.Time{}
			}
			s.hints[id] = first
			pending = true
		}
	}
	s.hintMu.Unlock()
	if pending && s.retryQueued.CompareAndSwap(false, true) {
		time.AfterFunc(hintRetry, func() {
			s.retryQueued.Store(false)
			if ctx.Err() == nil {
				s.Run(ctx, "retry: tag not yet visible", false)
			}
		})
	}
}

// Run starts a pass, or, if one is already running, asks it to go round once more.
// Webhooks are only wake-up signals: every pass searches by tag, so lost or duplicated events are harmless.
func (s *Syncer) Run(ctx context.Context, reason string, cleanup bool) {
	if !s.mu.TryLock() {
		s.rerun.Store(true)
		return
	}
	defer s.mu.Unlock()
	for {
		s.rerun.Store(false)
		s.once(ctx, reason, cleanup, s.takeHints())
		if !s.rerun.Load() {
			return
		}
		reason, cleanup = "coalesced event", false
	}
}

// WhenIdle waits until no pass is running and then holds the passes back, so that something that must not
// overlap a hand-over (a backup of the data file) can run. It checks every poll. When wait has passed
// without a quiet moment it gives up waiting and reports forced = true; the caller then goes ahead without the
// lock. release lets the passes go again and runs the one that was asked for meanwhile, so a webhook that
// arrived during the work is not lost. It stops early (forced = true, a no-op release) if ctx ends.
func (s *Syncer) WhenIdle(ctx context.Context, wait, poll time.Duration) (release func(), forced bool) {
	deadline := time.Now().Add(wait)
	for {
		if s.mu.TryLock() {
			return func() {
				pending := s.rerun.Load()
				s.mu.Unlock()
				if pending {
					go s.Run(ctx, "after a backup", false)
				}
			}, false
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return func() {}, true
		}
		select {
		case <-ctx.Done():
		case <-time.After(poll):
		}
	}
}

// Tidy only removes inbox tags from photos that are filed or expired. Immich sends no event when a photo
// is added to an album, so this is called on a short timer. It skips a round when a pass is running (the
// next tick will catch up) and, unlike a pass, leaves the "last run" record alone.
func (s *Syncer) Tidy(ctx context.Context) {
	if !s.mu.TryLock() {
		return
	}
	defer s.mu.Unlock()
	if n, errs := s.cleanupAll(ctx); n > 0 || errs > 0 {
		s.Log.Info("tidy finished", "untagged", n, "errors", errs)
	}
}

func (s *Syncer) once(ctx context.Context, reason string, cleanup bool, hints map[uuid.UUID]time.Time) {
	info := &RunInfo{Started: time.Now(), Reason: reason, PerService: map[string]Counts{}}
	defer func() {
		info.Finished = time.Now()
		s.last.Store(info)
		s.totMu.Lock()
		s.totals.Passes++
		s.totals.Counts.add(info.Counts)
		s.totMu.Unlock()
		s.publish()
		s.Log.Info("pass finished", "reason", reason, "counts", info.Counts, "untagged", info.Untagged, "error", info.Error)
		if reason == "daily sweep" && info.Counts.Imported+info.Counts.Duplicate+info.Counts.Refused > 0 {
			// Photos reached the daily sweep untouched: the tag events never arrived. The usual cause is
			// PUBLIC_WEBHOOK_URL not being reachable from the Immich container (or the workflow being off).
			msg := "photos were found by the daily sweep instead of by webhook: check that PUBLIC_WEBHOOK_URL is reachable from Immich and the workflow is enabled (see workflow logs in Immich)"
			s.Log.Warn(msg)
			s.notify("immich-handoff: " + msg)
		}
		if info.Error != "" || info.Counts.Errors > 0 || info.Counts.Refused > 0 {
			s.notify(fmt.Sprintf("immich-handoff: %+v %s", info.Counts, info.Error))
		}
	}()

	resolved := map[uuid.UUID]bool{}
	for _, svc := range s.Current() {
		c := s.runService(ctx, svc, hints, resolved)
		info.Counts.add(c)
		info.PerService[svc.Label()] = c
	}
	s.retryHints(ctx, hints, resolved)
	if cleanup {
		n, errs := s.cleanupAll(ctx)
		info.Untagged = n
		info.Counts.Errors += errs
	}
}

// receiver is a resolved destination for one pass.
type receiver struct {
	acct   *Account
	userID uuid.UUID
	inbox  uuid.UUID
	album  func(ctx context.Context) (uuid.UUID, error) // the service's album in this account, found or made when first needed; nil without one
}

func (s *Syncer) runService(ctx context.Context, svc *Service, hints map[uuid.UUID]time.Time, resolved map[uuid.UUID]bool) (c Counts) {
	log := s.Log.With("service", svc.Label())
	// Never run without a healthy receiver: with none, an original would be deleted with no copy anywhere.
	if svc.Broken != "" || len(svc.Receivers) == 0 {
		return
	}
	var rs []receiver
	for _, r := range svc.Receivers {
		me, err := r.API.Me(ctx)
		if err != nil {
			if immich.IsAuthError(err) {
				s.rejected(r)
			}
			log.Error("cannot authenticate receiver", "receiver", r.Name, "err", err)
			c.Errors++
			return
		}
		inbox, err := r.API.FindTag(ctx, svc.InboxTag, !s.Cfg.DryRun)
		if err != nil {
			log.Error("cannot ensure inbox tag", "receiver", r.Name, "err", err)
			c.Errors++
			return
		}
		rc := receiver{acct: r, userID: me.ID}
		if inbox != nil {
			rc.inbox = *inbox
		}
		if svc.Album != "" && !s.Cfg.DryRun {
			rc.album = s.albumFinder(r.API, svc.Album)
		}
		rs = append(rs, rc)
	}
	for _, snd := range svc.Senders {
		c.add(s.ingest(ctx, log, svc, snd, rs, hints, resolved))
	}
	return
}

func (s *Syncer) ingest(ctx context.Context, log *slog.Logger, svc *Service, snd *Account, rs []receiver, hints map[uuid.UUID]time.Time, resolved map[uuid.UUID]bool) (c Counts) {
	log = log.With("sender", snd.Name)
	me, err := snd.API.Me(ctx)
	if err != nil {
		if immich.IsAuthError(err) {
			s.rejected(snd)
		}
		log.Error("cannot authenticate sender", "err", err)
		c.Errors++
		return
	}
	for _, r := range rs {
		if r.userID == me.ID {
			log.Error("sender and receiver are the same Immich account; refusing", "receiver", r.acct.Name)
			c.Errors++
			return
		}
	}
	tag, err := snd.API.FindTag(ctx, svc.Tag, false)
	if err != nil {
		log.Error("tag lookup failed", "err", err)
		c.Errors++
		return
	}
	var shareTag uuid.UUID // the zero id: a photo that was not tagged (it came from the watched album)
	var assets []immich.AssetResponseDto
	if tag != nil {
		shareTag = *tag
		assets, err = snd.API.Search(ctx, immich.MetadataSearchDto{TagIds: &[]uuid.UUID{*tag}})
		if err != nil {
			log.Error("search failed", "err", err)
			c.Errors++
			return
		}
		assets = s.withHinted(ctx, log, snd.API, svc.Tag, assets, hints, resolved)
	}
	ids := make([]string, 0, len(assets))
	for _, a := range assets {
		ids = append(ids, a.Id.String())
	}
	if tag != nil {
		log.Info("tagged assets found", "user", me.Email, "count", len(assets), "ids", ids)
	}
	// Photos in the sender's linked album are handed over too, once, without a tag.
	fromAlbum := map[uuid.UUID]bool{}
	if snd.WatchAlbum != uuid.Nil {
		if pending, err := AlbumPending(ctx, svc, snd, me.ID); err != nil {
			s.watchProblem(log, svc, snd, err)
		} else {
			s.watchOK(svc, snd)
			tagged := map[uuid.UUID]bool{}
			for _, a := range assets {
				tagged[a.Id] = true
			}
			for _, a := range pending {
				if !tagged[a.Id] {
					assets = append(assets, a)
					fromAlbum[a.Id] = true
				}
			}
			if len(pending) > 0 {
				log.Info("photos in the linked album", "album", snd.WatchAlbumName, "count", len(pending))
			}
		}
	}
	var fromAlbumDone, fromAlbumRefused, fromAlbumFailed int
	for _, a := range assets {
		if s.justHandled(a.Id) {
			log.Debug("skipping a photo handled a moment ago that Immich's search still lists", "asset", a.Id)
			continue
		}
		tagID := shareTag
		if fromAlbum[a.Id] {
			tagID = uuid.Nil
		}
		switch out, err := s.process(ctx, log, svc, snd, rs, a, tagID); {
		case err != nil:
			c.Errors++
			if fromAlbum[a.Id] {
				fromAlbumFailed++
			}
			log.Error("asset failed; original kept", "asset", a.Id, "file", a.OriginalFileName, "err", err)
		case out == "imported":
			c.Imported++
			s.markJustHandled(a.Id)
			if fromAlbum[a.Id] {
				fromAlbumDone++
			}
		case out == "duplicate":
			c.Duplicate++
			s.markJustHandled(a.Id)
			if fromAlbum[a.Id] {
				fromAlbumDone++
			}
		case out == "refused":
			c.Refused++
			s.markJustHandled(a.Id)
			if fromAlbum[a.Id] {
				fromAlbumRefused++
			}
		case out == "dryrun":
			c.DryRun++
		}
		s.publishSoon() // the page's "waiting" counter counts down while a long hand-over runs
	}
	s.reportAlbum(svc, snd, fromAlbumDone, fromAlbumRefused, fromAlbumFailed)
	return
}

// process hands one asset to every receiver. The original is deleted last, and only after every
// receiver holds a verified copy. Any failure keeps the original; because Immich recognises
// duplicates by checksum, a later retry is safe for receivers that already have their copy.
func (s *Syncer) process(ctx context.Context, log *slog.Logger, svc *Service, snd *Account, rs []receiver, a immich.AssetResponseDto, shareTag uuid.UUID) (string, error) {
	if s.Cfg.DryRun {
		log.Info("[dry-run] would hand over and remove original", "asset", a.Id, "file", a.OriginalFileName)
		return "dryrun", nil
	}
	outcome := "duplicate"
	for _, r := range rs {
		up, sum, err := immich.Transfer(ctx, snd.API, r.acct.API, a)
		if err != nil {
			return "", fmt.Errorf("to %s: %w", r.acct.Name, err)
		}
		copy_, err := r.acct.API.Asset(ctx, up.Id)
		if err != nil {
			return "", fmt.Errorf("to %s: %w", r.acct.Name, err)
		}
		if copy_.IsTrashed {
			log.Warn("a copy exists in a receiver's trash; handover refused, original kept and retagged",
				"file", a.OriginalFileName, "receiver", r.acct.Name, "tag", svc.RefusedTag)
			if err := s.markRefused(ctx, svc, snd.API, a.Id, shareTag); err != nil {
				return "", fmt.Errorf("handover refused but could not retag the original: %w", err)
			}
			return "refused", nil
		}
		if !immich.ChecksumMatches(copy_.Checksum, sum) {
			return "", fmt.Errorf("checksum mismatch on %s's copy %s", r.acct.Name, up.Id)
		}
		if up.Status == immich.AssetMediaStatusCreated {
			// Immich can drop a tag added before it has finished reading a new upload, so wait for that
			// first, then add the tag and check it stuck (a copy without it would arrive unnoticed).
			s.waitProcessed(ctx, r.acct.API, up.Id)
			if !s.fileInAlbum(ctx, log, svc, r, up.Id) {
				if err := s.addTagVerified(ctx, r.acct.API, svc.InboxTag, up.Id); err != nil {
					return "", fmt.Errorf("tag %s's copy %s: %w", r.acct.Name, up.Id, err)
				}
			}
			outcome = "imported"
		}
	}
	if svc.CopiesFor(snd) {
		if err := s.markSent(ctx, svc, snd.API, a.Id, shareTag); err != nil {
			return "", fmt.Errorf("copies verified but the original could not be marked as handed over: %w", err)
		}
	} else if err := snd.API.DeleteAsset(ctx, a.Id, s.Cfg.DeleteForce); err != nil {
		return "", fmt.Errorf("copies verified but original not removed: %w", err)
	}
	log.Info(outcome, "file", a.OriginalFileName, "receivers", len(rs))
	return outcome, nil
}

// albumFinder returns a function that finds the account's own album with this name, or makes it, once, the
// first time a photo needs it. Looking it up by name every pass means an album the person deleted or renamed
// simply comes back under the configured name at the next arrival.
func (s *Syncer) albumFinder(api *immich.API, name string) func(ctx context.Context) (uuid.UUID, error) {
	var once sync.Once
	var id uuid.UUID
	var err error
	return func(ctx context.Context) (uuid.UUID, error) {
		once.Do(func() {
			var found *uuid.UUID
			if found, _, err = api.FindAlbum(ctx, name); err != nil {
				return
			}
			if found != nil {
				id = *found
				return
			}
			id, err = api.CreateAlbum(ctx, name)
		})
		return id, err
	}
}

// fileInAlbum puts a new copy in the service's album and reports whether it did. When the service has no
// album it does nothing (false). When the album cannot be used, for example because the receiver's key may
// not touch albums, it says so (once, until it works again) and returns false, so the copy still gets the
// inbox tag and is not lost: a hand-over never fails because of the album.
func (s *Syncer) fileInAlbum(ctx context.Context, log *slog.Logger, svc *Service, r receiver, asset uuid.UUID) bool {
	if r.album == nil {
		return false
	}
	album, err := r.album(ctx)
	if err == nil {
		err = r.acct.API.AddToAlbum(ctx, album, asset)
	}
	key := svc.Label() + "/" + r.acct.Name
	s.albumMu.Lock()
	defer s.albumMu.Unlock()
	if err != nil {
		log.Warn("could not put the copy in the album; tagging it inbox instead", "receiver", r.acct.Name, "album", svc.Album, "err", err)
		if !s.albumBroken[key] {
			if s.albumBroken == nil {
				s.albumBroken = map[string]bool{}
			}
			s.albumBroken[key] = true
			s.notify(fmt.Sprintf("immich-handoff: could not put photos in the album %q for %s (%v); they are tagged %s instead. The receiver's key needs album.read, album.create and albumAsset.create.", svc.Album, r.acct.Name, err, svc.InboxTag))
		}
		return false
	}
	delete(s.albumBroken, key)
	return true
}

// cleanupAll removes the inbox tag from filed or expired copies in every receiver, once per
// receiver account and tag name. Assets are never deleted.
func (s *Syncer) cleanupAll(ctx context.Context) (untagged, errs int) {
	type key struct{ acct, tag string }
	type job struct {
		acct   *Account
		tag    string
		expiry int
	}
	jobs := map[key]*job{}
	var order []key
	for _, svc := range s.Current() {
		for _, r := range svc.Receivers {
			k := key{r.Name, svc.InboxTag}
			if j, ok := jobs[k]; ok {
				j.expiry = min(j.expiry, svc.ExpiryDays)
				continue
			}
			jobs[k] = &job{r, svc.InboxTag, svc.ExpiryDays}
			order = append(order, k)
		}
	}
	for _, k := range order {
		j := jobs[k]
		n, err := s.cleanup(ctx, j.acct, j.tag, j.expiry)
		untagged += n
		if err != nil {
			errs++
			s.Log.Error("cleanup failed", "receiver", j.acct.Name, "err", err)
		}
	}
	return
}

// cleanup removes the inbox tag from assets filed in an album or unfiled for expiryDays.
func (s *Syncer) cleanup(ctx context.Context, rcv *Account, inboxTag string, expiryDays int) (int, error) {
	inbox, err := rcv.API.FindTag(ctx, inboxTag, false)
	if err != nil || inbox == nil {
		return 0, err
	}
	tags := &[]uuid.UUID{*inbox}
	tagged, err := rcv.API.Search(ctx, immich.MetadataSearchDto{TagIds: tags})
	if err != nil || len(tagged) == 0 {
		return 0, err
	}
	yes := true
	unfiled, err := rcv.API.Search(ctx, immich.MetadataSearchDto{TagIds: tags, IsNotInAlbum: &yes})
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().AddDate(0, 0, -expiryDays)
	expired, err := rcv.API.Search(ctx, immich.MetadataSearchDto{TagIds: tags, IsNotInAlbum: &yes, CreatedBefore: &cutoff})
	if err != nil {
		return 0, err
	}
	unfiledSet := map[uuid.UUID]bool{}
	for _, a := range unfiled {
		unfiledSet[a.Id] = true
	}
	var drop []uuid.UUID
	for _, a := range tagged {
		if !unfiledSet[a.Id] { // filed in at least one album
			drop = append(drop, a.Id)
		}
	}
	for _, a := range expired {
		drop = append(drop, a.Id)
	}
	if len(drop) == 0 {
		return 0, nil
	}
	if s.Cfg.DryRun {
		s.Log.Info("[dry-run] would untag", "receiver", rcv.Name, "count", len(drop))
		return 0, nil
	}
	if err := rcv.API.UntagAssets(ctx, *inbox, drop); err != nil {
		return 0, err
	}
	s.Log.Info("cleanup: removed inbox tag (assets kept)", "receiver", rcv.Name, "count", len(drop))
	return len(drop), nil
}

// withHinted adds assets named by webhook events that the tag search did not return, after checking
// with this account's own key that they exist and carry the service tag. Hints that this account can
// confirm (or that are trashed, so there is nothing to do) are recorded in resolved.
func (s *Syncer) withHinted(ctx context.Context, log *slog.Logger, src *immich.API, tagName string, found []immich.AssetResponseDto, hints map[uuid.UUID]time.Time, resolved map[uuid.UUID]bool) []immich.AssetResponseDto {
	have := map[uuid.UUID]bool{}
	for _, a := range found {
		have[a.Id] = true
		resolved[a.Id] = true
	}
	for id := range hints {
		if have[id] {
			continue
		}
		a, err := src.Asset(ctx, id) // fails for assets owned by another account: simply not ours
		if err != nil {
			continue
		}
		if a.IsTrashed {
			resolved[id] = true
			continue
		}
		if !hasTag(a, tagName) {
			continue // announced by an event but the tag is not visible yet (or belongs to another service); retried
		}
		log.Info("tag search did not list a freshly tagged asset; using the event hint", "asset", id)
		resolved[id] = true
		found = append(found, *a)
	}
	return found
}

func hasTag(a *immich.AssetResponseDto, name string) bool {
	if a.Tags == nil {
		return false
	}
	for _, t := range *a.Tags {
		if t.Name == name || t.Value == name {
			return true
		}
	}
	return false
}

// rejected tells the provider that Immich no longer accepts an account's key.
func (s *Syncer) rejected(a *Account) {
	if w, ok := s.Provider.(Watcher); ok {
		w.KeyRejected(a)
	}
}

// markSent is what ends a hand-over in copy mode: the original keeps existing, gets the sent tag so its
// owner can see it was handed over, and only then loses the service tag so it is not found again.
func (s *Syncer) markSent(ctx context.Context, svc *Service, src *immich.API, id, shareTag uuid.UUID) error {
	if svc.SentTag != "" {
		if err := s.addTagVerified(ctx, src, svc.SentTag, id); err != nil {
			return err
		}
	}
	if shareTag == uuid.Nil { // it came from the linked album and never had the service tag
		return nil
	}
	return s.removeTagVerified(ctx, src, svc.Tag, shareTag, id)
}

// processingWait is how long to wait for Immich to finish reading a new upload, and how often to look.
var (
	processingWait = 30 * time.Second
	processingPoll = 150 * time.Millisecond
)

// waitProcessed returns once Immich has read the new asset's metadata (or the wait runs out; the tag
// check that follows still catches a dropped tag).
func (s *Syncer) waitProcessed(ctx context.Context, api *immich.API, id uuid.UUID) {
	deadline := time.Now().Add(processingWait)
	for time.Now().Before(deadline) {
		if a, err := api.Asset(ctx, id); err == nil && a.HasMetadata && a.ExifInfo != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(processingPoll):
		}
	}
}

// SettleDelay is how long to wait before checking that a tag change took effect.
var SettleDelay = 400 * time.Millisecond

// addTagVerified adds a tag and makes sure it is really there afterwards. Immich occasionally drops a
// tag change made right after another change to the same photo (its own metadata and sidecar jobs race
// with the API), so a single "success" answer is not trusted.
func (s *Syncer) addTagVerified(ctx context.Context, api *immich.API, name string, asset uuid.UUID) error {
	tag, err := api.FindTag(ctx, name, true)
	if err != nil || tag == nil {
		return fmt.Errorf("ensure tag %q: %v", name, err)
	}
	for attempt := 0; attempt < 5; attempt++ {
		if err := api.TagAssets(ctx, *tag, asset); err != nil {
			return err
		}
		time.Sleep(SettleDelay)
		a, err := api.Asset(ctx, asset)
		if err != nil {
			return err
		}
		if hasTag(a, name) {
			return nil
		}
	}
	return fmt.Errorf("tag %q did not stick on %s", name, asset)
}

// removeTagVerified removes a tag and makes sure it is really gone afterwards (see addTagVerified).
func (s *Syncer) removeTagVerified(ctx context.Context, api *immich.API, name string, tagID, asset uuid.UUID) error {
	for attempt := 0; attempt < 5; attempt++ {
		if err := api.UntagAssets(ctx, tagID, []uuid.UUID{asset}); err != nil {
			return err
		}
		time.Sleep(SettleDelay)
		a, err := api.Asset(ctx, asset)
		if err != nil {
			return err
		}
		if !hasTag(a, name) {
			return nil
		}
	}
	return fmt.Errorf("tag %q could not be removed from %s", name, asset)
}

// markRefused swaps the service tag for the refused tag on the sender's original, so the refusal is
// visible to its owner in the Immich app and the photo is not retried (and not reported) on every pass.
// Re-adding the service tag after the receiver clears its trash retries it.
func (s *Syncer) markRefused(ctx context.Context, svc *Service, src *immich.API, id, shareTag uuid.UUID) error {
	if svc.RefusedTag == "" {
		return nil
	}
	if err := s.addTagVerified(ctx, src, svc.RefusedTag, id); err != nil {
		return err
	}
	if shareTag == uuid.Nil {
		return nil
	}
	return s.removeTagVerified(ctx, src, svc.Tag, shareTag, id)
}
