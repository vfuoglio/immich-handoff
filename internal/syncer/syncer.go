// Package syncer hands tagged assets from sender accounts to receiver accounts, per service.
package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/live"
)

// Config holds the options that apply to every service.
type Config struct {
	DeleteForce bool // delete originals permanently instead of trashing them
	DryRun      bool
	NotifyURL   string
	Quiet       Quiet // the app-wide quiet hours, for services that set none of their own (QUIET_HOURS)
	NoDetails   bool  // do not carry the archived state, description, rating and location (CARRY_DETAILS=off)

	// ReceiverDelete: receiver keys may move copies to their owner's trash (RECEIVER_DELETE, on by default). It is what
	// lets copies expire (a service's lifetime) and lets a sender take their last hand-over back.
	ReceiverDelete bool
}

type Counts struct {
	Imported, Duplicate, Refused, DryRun, Errors int
	NotAccepted                                  int // photos a service does not take in (its rules), left with the refused tag
}

func (c *Counts) add(o Counts) {
	c.Imported += o.Imported
	c.Duplicate += o.Duplicate
	c.Refused += o.Refused
	c.DryRun += o.DryRun
	c.Errors += o.Errors
	c.NotAccepted += o.NotAccepted
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
	Usage    Usage      // optional: records what senders hand over, so a service's daily cap can be applied
	Bus      *live.Bus  // optional: tells the open pages what is happening, as it happens
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

	heldMu sync.Mutex
	held   map[*Account]Held        // senders whose photos wait for the daily cap
	wake   map[*Account]*time.Timer // when to look again, per waiting sender
	quiet  map[*Account]QuietWait   // senders whose photos wait for the quiet hours
	timers map[wakeKey]*time.Timer  // other times to look again (quiet hours open, a scheduled date comes)

	albumMu     sync.Mutex
	albumBroken map[string]bool // album problems already reported, so each is reported once, not every pass
	warned      map[string]bool // other once-only warnings (a key that lacks an optional permission), by key
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
	// A pass asked for while this runs found the lock taken and left a note for "the pass that is running" to go round
	// once more; a tidy is not one, so it must start that pass itself or the event is lost until the next trigger.
	defer func() {
		pending := s.rerun.Load()
		s.mu.Unlock()
		if pending {
			go s.Run(ctx, "after a tidy", false)
		}
	}()
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
	// A paused service hands nothing over and touches nothing: tagged photos stay tagged until it resumes.
	if svc.Paused {
		log.Debug("service is paused; leaving tagged photos alone")
		return
	}
	if svc.Remote != nil {
		// photos wait, still tagged, until the other server can be used; nothing is sent and nothing deleted
		if err := svc.Remote.Ready(ctx); err != nil {
			log.Warn("the receiver on another server is not ready; tagged photos wait", "receiver", svc.Remote.Label(), "err", err)
			return
		}
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
	if snd.Paused {
		log.Debug("sender is paused; leaving their tagged photos alone")
		s.release(snd)
		return
	}
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
	now := time.Now()
	var datedTag map[uuid.UUID]immich.TagInfo // photos tagged for a date that has come, with the tag that says so
	assets, datedTag = s.scheduled(ctx, log, snd, svc, assets, now)
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
	// the daily cap: what this sender has handed over to this service in the last day
	var snap Snapshot
	// what a sender hands over is always recorded, so a limit set later counts what already went today
	track := s.Usage != nil && !s.Cfg.DryRun
	capped := track && (svc.LimitPhotos > 0 || svc.LimitMB > 0)
	if capped {
		var err error
		if snap, err = s.Usage.Handed(ctx, snd, time.Now().Add(-LimitWindow)); err != nil {
			log.Warn("cannot read what was handed over today; applying no cap this pass", "err", err)
			capped = false
		}
	}
	held := false
	quiet := s.quietOf(svc)
	quietWaiting, notAccepted := 0, 0
	batch := uuid.NewString() // what this pass hands over for this sender, so a sender can take it back as one
	for i, a := range assets {
		if capped && Capped(svc, snap) {
			waiting := 0
			for _, rest := range assets[i:] {
				if !s.justHandled(rest.Id) {
					waiting++
				}
			}
			if waiting > 0 {
				s.hold(ctx, log, svc, snd, snap, waiting)
				held = true
			}
			break
		}
		if s.justHandled(a.Id) {
			log.Debug("skipping a photo handled a moment ago that Immich's search still lists", "asset", a.Id)
			continue
		}
		if quiet.Holds(sizeOfPhoto(a), now) {
			quietWaiting++
			continue
		}
		tagID, procSvc := shareTag, svc
		if fromAlbum[a.Id] {
			tagID = uuid.Nil
		}
		if t, ok := datedTag[a.Id]; ok { // tagged for a date: that tag is the one to take off afterwards
			tagID = t.ID
			local := *svc
			local.Tag = t.Value
			procSvc = &local
		}
		if svc.Accept.Active() && !s.accepted(ctx, log, snd, svc, &a) {
			if err := s.markRefused(ctx, procSvc, snd.API, a.Id, tagID); err != nil {
				log.Error("a photo the service does not take could not be marked; it will be looked at again", "asset", a.Id, "err", err)
				c.Errors++
				continue
			}
			log.Info("not handed over: the service does not take it in", "service", svc.Label(), "file", a.OriginalFileName)
			notAccepted++
			c.NotAccepted++
			s.markJustHandled(a.Id)
			continue
		}
		if !s.Cfg.DryRun {
			s.emit(live.Started, svc, snd, "", 0, nil)
		}
		switch out, handed, err := s.process(ctx, log, procSvc, snd, rs, a, tagID, batch); {
		case err != nil:
			c.Errors++
			s.emit(live.Failed, svc, snd, "", 0, nil)
			if fromAlbum[a.Id] {
				fromAlbumFailed++
			}
			log.Error("asset failed; original kept", "asset", a.Id, "file", a.OriginalFileName, "err", err)
		case out == "imported":
			c.Imported++
			s.emit(live.Handed, svc, snd, "", 0, nil)
			s.markJustHandled(a.Id)
			if track {
				for _, m := range handed { // every photo of a stack counts, with its own size
					size := sizeOf(nil)
					if m.ExifInfo != nil {
						size = sizeOf(m.ExifInfo.FileSizeInByte)
					}
					if err := s.Usage.Record(ctx, snd, size); err != nil {
						log.Warn("could not record the hand-over for the daily cap", "err", err)
					}
					snap.Photos++
					snap.Bytes += size
					if snap.Oldest.IsZero() {
						snap.Oldest = time.Now()
					}
				}
			}
			if fromAlbum[a.Id] {
				fromAlbumDone++
			}
		case out == "duplicate":
			c.Duplicate++
			s.emit(live.Handed, svc, snd, "duplicate", 0, nil)
			s.markJustHandled(a.Id)
			if fromAlbum[a.Id] {
				fromAlbumDone++
			}
		case out == "refused":
			c.Refused++
			s.emit(live.Refused, svc, snd, "", 0, nil)
			s.markJustHandled(a.Id)
			if fromAlbum[a.Id] {
				fromAlbumRefused++
			}
		case out == "dryrun":
			c.DryRun++
		}
		s.publishSoon() // the page's "waiting" counter counts down while a long hand-over runs
	}
	if !held {
		s.release(snd)
	}
	s.quietWait(ctx, log, snd, quiet, quietWaiting, now)
	if notAccepted > 0 {
		if r, ok := s.Provider.(AcceptReporter); ok {
			r.NotAccepted(svc, snd, notAccepted)
		}
	}
	s.reportAlbum(svc, snd, fromAlbumDone, fromAlbumRefused, fromAlbumFailed)
	return
}

// process hands a tagged photo to every receiver. When it is part of a stack (a burst, or a RAW and JPEG pair) the
// whole stack goes, the cover first, and the stack is made again on the receiver's side. The originals are deleted
// last, and only after every receiver holds a verified copy of every photo. Any failure keeps the originals; because
// Immich recognises duplicates by checksum, a later retry is safe for receivers that already have their copies. It
// returns what it handed over (the tagged photo, and the rest of its stack).
func (s *Syncer) process(ctx context.Context, log *slog.Logger, svc *Service, snd *Account, rs []receiver, a immich.AssetResponseDto, shareTag uuid.UUID, batch string) (string, []immich.AssetResponseDto, error) {
	if s.Cfg.DryRun {
		log.Info("[dry-run] would hand over and remove original", "asset", a.Id, "file", a.OriginalFileName)
		return "dryrun", nil, nil
	}
	members := s.stackOf(ctx, log, snd, a)
	// A live photo is a still plus a separate hidden motion clip, which goes with it (see handOne).
	clips := map[uuid.UUID]*immich.AssetResponseDto{}
	clipOK := map[uuid.UUID]bool{} // false once a receiver's still is found not to be linked to its clip
	for _, m := range members {
		if clip := s.motionClipOf(ctx, log, snd, m); clip != nil {
			clips[m.Id], clipOK[m.Id] = clip, true
		}
	}
	outcome := "duplicate"
	for _, r := range rs {
		var copies []handedCopy
		for _, m := range members {
			c, err := s.handOne(ctx, log, svc, snd, r, m, clips[m.Id])
			if err != nil {
				return "", nil, err
			}
			if c.refused {
				if err := s.markRefused(ctx, svc, snd.API, a.Id, shareTag); err != nil {
					return "", nil, fmt.Errorf("handover refused but could not retag the original: %w", err)
				}
				return "refused", nil, nil
			}
			if clips[m.Id] != nil && !c.linked {
				clipOK[m.Id] = false
			}
			if c.created {
				outcome = "imported"
				s.remember(ctx, snd, batch, m.Id, c.id, false)
			}
			if c.clipNew && clips[m.Id] != nil {
				s.remember(ctx, snd, batch, clips[m.Id].Id, c.clip, true)
			}
			copies = append(copies, c)
		}
		s.restack(ctx, log, svc, r, copies)
	}
	if svc.Remote != nil {
		for _, m := range members {
			if err := s.deliverRemote(ctx, log, svc, snd, m, clips[m.Id]); err != nil {
				return "", nil, err
			}
		}
	}
	if svc.CopiesFor(snd) {
		if err := s.markSent(ctx, svc, snd.API, a.Id, shareTag); err != nil {
			return "", nil, fmt.Errorf("copies verified but the original could not be marked as handed over: %w", err)
		}
	} else {
		var ids []uuid.UUID
		for _, m := range members {
			ids = append(ids, m.Id)
			if clips[m.Id] != nil && clipOK[m.Id] {
				ids = append(ids, clips[m.Id].Id) // a hidden clip goes with its still, or it would stay behind unseen
			}
		}
		if err := snd.API.DeleteAssets(ctx, ids, s.Cfg.DeleteForce); err != nil {
			return "", nil, fmt.Errorf("copies verified but original not removed: %w", err)
		}
	}
	for _, m := range members { // the others of a stack (the cover too) must not be handed over again by a search that still lists them
		s.markJustHandled(m.Id)
	}
	log.Info(outcome, "file", a.OriginalFileName, "receivers", len(rs), "photos", len(members))
	return outcome, members, nil
}

// deliverRemote hands one photo to the receiver on another server. Like handOne it touches nothing of the original;
// any error keeps it, and because the other server recognises a duplicate, a retry is safe.
func (s *Syncer) deliverRemote(ctx context.Context, log *slog.Logger, svc *Service, snd *Account, a immich.AssetResponseDto, clip *immich.AssetResponseDto) error {
	if e := a.ExifInfo; !s.Cfg.NoDetails && (e == nil || (e.Description == nil && e.Rating == nil && e.Latitude == nil)) {
		if full, err := snd.API.Asset(ctx, a.Id); err == nil && full != nil {
			a = *full
		}
	}
	if err := svc.Remote.Deliver(ctx, snd.API, a, clip, RemoteJob{Sender: snd.Name, Details: !s.Cfg.NoDetails}); err != nil {
		return fmt.Errorf("to %s: %w", svc.Remote.Label(), err)
	}
	log.Debug("handed over to another server", "file", a.OriginalFileName, "receiver", svc.Remote.Label())
	return nil
}

// handedCopy is one photo's copy in one receiver's account.
type handedCopy struct {
	id      uuid.UUID
	created bool      // it is new (not a duplicate of a copy that was already there)
	refused bool      // the receiver has it in their trash: nothing is handed over
	linked  bool      // a live photo's copy is linked to its motion clip (true for a photo without one)
	stack   *string   // the stack the copy is in at the receiver, if any
	clip    uuid.UUID // a live photo: the receiver's copy of the motion clip, and whether it is new
	clipNew bool
}

// handOne gives one photo to one receiver: its motion clip first when it is a live photo (hidden), then the photo
// itself, checks both by checksum, and tags the copy (or files it in the album). Nothing here touches the original.
func (s *Syncer) handOne(ctx context.Context, log *slog.Logger, svc *Service, snd *Account, r receiver, a immich.AssetResponseDto, clip *immich.AssetResponseDto) (handedCopy, error) {
	var motion *uuid.UUID
	var clipCopy uuid.UUID
	var clipNew bool
	if clip != nil {
		cup, csum, err := immich.TransferWith(ctx, snd.API, r.acct.API, *clip, immich.UploadOptions{Hidden: true})
		if err != nil {
			return handedCopy{}, fmt.Errorf("motion clip to %s: %w", r.acct.Name, err)
		}
		cc, err := r.acct.API.Asset(ctx, cup.Id)
		if err != nil {
			return handedCopy{}, fmt.Errorf("motion clip to %s: %w", r.acct.Name, err)
		}
		if cc.IsTrashed {
			log.Warn("the motion clip exists in a receiver's trash; handover refused, original kept and retagged",
				"file", a.OriginalFileName, "receiver", r.acct.Name, "tag", svc.RefusedTag)
			return handedCopy{refused: true}, nil
		}
		if !immich.ChecksumMatches(cc.Checksum, csum) {
			return handedCopy{}, fmt.Errorf("checksum mismatch on %s's copy %s of the motion clip", r.acct.Name, cup.Id)
		}
		id := cup.Id
		motion = &id
		clipCopy, clipNew = cup.Id, cup.Status == immich.AssetMediaStatusCreated
	}
	if e := a.ExifInfo; !s.Cfg.NoDetails && (e == nil || (e.Description == nil && e.Rating == nil && e.Latitude == nil)) {
		// a search and a stack list give only part of what Immich knows; the photo itself has the description,
		// rating and location a person added
		if full, err := snd.API.Asset(ctx, a.Id); err == nil && full != nil {
			a = *full
		}
	}
	up, sum, err := immich.TransferWith(ctx, snd.API, r.acct.API, a, immich.UploadOptions{MotionClip: motion, Details: !s.Cfg.NoDetails})
	if err != nil {
		return handedCopy{}, fmt.Errorf("to %s: %w", r.acct.Name, err)
	}
	copy_, err := r.acct.API.Asset(ctx, up.Id)
	if err != nil {
		return handedCopy{}, fmt.Errorf("to %s: %w", r.acct.Name, err)
	}
	linked := true
	if motion != nil && (copy_.LivePhotoVideoId == nil || !strings.EqualFold(*copy_.LivePhotoVideoId, motion.String())) {
		// The still was already there without its clip (handed over earlier, as a plain photo), so Immich kept
		// that copy as it was. The clip stays with the sender: nothing that is not linked is deleted.
		log.Warn("the receiver's copy of the still is not linked to the motion clip; the clip is not removed from the sender",
			"file", a.OriginalFileName, "receiver", r.acct.Name)
		linked = false
	}
	if copy_.IsTrashed {
		log.Warn("a copy exists in a receiver's trash; handover refused, original kept and retagged",
			"file", a.OriginalFileName, "receiver", r.acct.Name, "tag", svc.RefusedTag)
		return handedCopy{refused: true}, nil
	}
	if !immich.ChecksumMatches(copy_.Checksum, sum) {
		return handedCopy{}, fmt.Errorf("checksum mismatch on %s's copy %s", r.acct.Name, up.Id)
	}
	c := handedCopy{id: up.Id, linked: linked, clip: clipCopy, clipNew: clipNew}
	if copy_.Stack != nil {
		id := copy_.Stack.Id.String()
		c.stack = &id
	}
	if up.Status == immich.AssetMediaStatusCreated {
		// Immich can drop a tag added before it has finished reading a new upload, so wait for that
		// first, then add the tag and check it stuck (a copy without it would arrive unnoticed).
		s.waitProcessed(ctx, r.acct.API, up.Id)
		if !s.fileInAlbum(ctx, log, svc, r, up.Id) {
			if err := s.addTagVerified(ctx, r.acct.API, svc.InboxTag, up.Id); err != nil {
				return handedCopy{}, fmt.Errorf("tag %s's copy %s: %w", r.acct.Name, up.Id, err)
			}
		}
		c.created = true
	}
	return c, nil
}

// stackOf is the photos that go together with a tagged one: itself, or when it is part of a stack the whole stack,
// the cover first. Without the permission to read stacks (a key made before stacks were handed over), or when the
// stack cannot be read, only the tagged photo goes, as it always did, and the log says so.
func (s *Syncer) stackOf(ctx context.Context, log *slog.Logger, snd *Account, a immich.AssetResponseDto) []immich.AssetResponseDto {
	one := []immich.AssetResponseDto{a}
	if a.Stack == nil { // a search does not say whether a photo is in a stack; the photo itself does
		if full, err := snd.API.Asset(ctx, a.Id); err == nil && full != nil {
			a = *full
		}
	}
	if a.Stack == nil || a.Stack.AssetCount < 2 {
		return one
	}
	st, err := snd.API.Stack(ctx, a.Stack.Id)
	if err != nil {
		if immich.IsForbidden(err) {
			s.warnOnce("stack.read/"+snd.Name, log, "this sender's key may not read stacks, so only the tagged photo of a stack is handed over; opening the page gives the key that permission",
				fmt.Sprintf("immich-handoff: %s's key may not read stacks, so only the tagged photo of a stack is handed over. The key needs stack.read: they get it by opening the page once, or add it to a key made by hand.", snd.Name))
		} else {
			log.Warn("could not read the photo's stack; only the tagged photo is handed over", "file", a.OriginalFileName, "err", err)
		}
		return one
	}
	out := make([]immich.AssetResponseDto, 0, len(st.Assets))
	for _, m := range st.Assets { // the cover first
		if m.Id == st.PrimaryAssetId && !m.IsTrashed {
			out = append(out, m)
		}
	}
	for _, m := range st.Assets {
		if m.Id != st.PrimaryAssetId && !m.IsTrashed {
			out = append(out, m)
		}
	}
	found := false
	for _, m := range out {
		found = found || m.Id == a.Id
	}
	if !found || len(out) < 2 { // the tagged photo is not in what Immich lists: do the one thing that is certain
		return one
	}
	return out
}

// restack makes the stack again in a receiver's account: the copies of a stack that arrived together, the cover
// first. It does nothing when they are already one stack (a hand-over that is repeated), and when the receiver's key
// may not create stacks the photos stay separate, which is said once. It never fails a hand-over: the photos are
// already there.
func (s *Syncer) restack(ctx context.Context, log *slog.Logger, svc *Service, r receiver, copies []handedCopy) {
	if len(copies) < 2 {
		return
	}
	same := copies[0].stack != nil
	for _, c := range copies {
		same = same && c.stack != nil && *c.stack == *copies[0].stack
	}
	if same {
		return
	}
	ids := make([]uuid.UUID, len(copies))
	for i, c := range copies {
		ids[i] = c.id
	}
	if _, err := r.acct.API.CreateStack(ctx, ids); err != nil {
		if immich.IsForbidden(err) {
			s.warnOnce("stack.create/"+svc.Label()+"/"+r.acct.Name, log, "the receiver's key may not create stacks, so the photos of a stack arrive as separate photos; opening the page gives the key that permission",
				fmt.Sprintf("immich-handoff: %s's key may not create stacks, so photos that were a stack arrive as separate photos. The key needs stack.create: its owner gets it by opening the page once, or add it to a key made by hand.", r.acct.Name))
		} else {
			log.Warn("could not make the stack again at the receiver; the photos arrived separately", "receiver", r.acct.Name, "err", err)
		}
	}
}

// warnOnce logs a warning and notifies whoever runs the app, once for each key until the process restarts.
func (s *Syncer) warnOnce(key string, log *slog.Logger, logMsg, notifyMsg string) {
	s.albumMu.Lock()
	first := !s.warned[key]
	if first {
		if s.warned == nil {
			s.warned = map[string]bool{}
		}
		s.warned[key] = true
	}
	s.albumMu.Unlock()
	if first {
		log.Warn(logMsg)
		s.notify(notifyMsg)
	}
}

// motionClipOf is the sender's motion clip of a live photo, or nil when the photo has none (or it cannot be read:
// then only the still is handed over, as before, and the log says so).
func (s *Syncer) motionClipOf(ctx context.Context, log *slog.Logger, snd *Account, a immich.AssetResponseDto) *immich.AssetResponseDto {
	if a.LivePhotoVideoId == nil || *a.LivePhotoVideoId == "" {
		return nil
	}
	id, err := uuid.Parse(*a.LivePhotoVideoId)
	if err != nil {
		return nil
	}
	clip, err := snd.API.Asset(ctx, id)
	if err != nil {
		log.Warn("the live photo's motion clip could not be read; only the still is handed over", "file", a.OriginalFileName, "err", err)
		return nil
	}
	if clip.IsTrashed {
		return nil
	}
	return clip
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
		acct     *Account
		tag      string
		expiry   int
		lifetime int // copies unfiled this long are moved to the trash (0: not)
		service  string
	}
	jobs := map[key]*job{}
	var order []key
	for _, svc := range s.Current() {
		for _, r := range svc.Receivers {
			k := key{r.Name, svc.InboxTag}
			j, ok := jobs[k]
			if !ok {
				j = &job{acct: r, tag: svc.InboxTag, expiry: svc.ExpiryDays, service: svc.Label()}
				jobs[k] = j
				order = append(order, k)
			} else {
				j.expiry = min(j.expiry, svc.ExpiryDays)
			}
			if svc.LifetimeDays > 0 && s.Cfg.ReceiverDelete && (j.lifetime == 0 || svc.LifetimeDays < j.lifetime) {
				j.lifetime, j.service = svc.LifetimeDays, svc.Label()
			}
		}
	}
	for _, k := range order {
		j := jobs[k]
		n, err := s.cleanup(ctx, j.acct, j.tag, j.expiry, j.lifetime, j.service)
		untagged += n
		if err != nil {
			errs++
			s.Log.Error("cleanup failed", "receiver", j.acct.Name, "err", err)
		}
	}
	return
}

func (s *Syncer) cleanup(ctx context.Context, rcv *Account, inboxTag string, expiryDays, lifetimeDays int, service string) (int, error) {
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
	var expired []immich.AssetResponseDto
	if lifetimeDays > 0 { // the copies live out their lifetime with the tag; then they go to the trash, not just lose the tag
		lifeCutoff := time.Now().AddDate(0, 0, -lifetimeDays)
		ended, err := rcv.API.Search(ctx, immich.MetadataSearchDto{TagIds: tags, IsNotInAlbum: &yes, CreatedBefore: &lifeCutoff})
		if err != nil {
			return 0, err
		}
		s.expire(ctx, rcv, service, ended)
	} else {
		var err error
		if expired, err = rcv.API.Search(ctx, immich.MetadataSearchDto{TagIds: tags, IsNotInAlbum: &yes, CreatedBefore: &cutoff}); err != nil {
			return 0, err
		}
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
