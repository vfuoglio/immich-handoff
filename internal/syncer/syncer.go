// Package syncer moves tagged assets from source accounts to the destination account.
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

type Config struct {
	ShareTag, InboxTag string
	RefusedTag         string // put on an original whose import was refused; replaces ShareTag so it is not retried
	ExpiryDays         int
	DeleteForce        bool
	DryRun             bool
	NotifyURL          string
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
	Untagged          int
	Error             string
}

type Syncer struct {
	Cfg     Config
	Dest    *immich.API
	Sources []*immich.API
	Log     *slog.Logger

	mu     sync.Mutex
	provMu sync.Mutex
	hintMu sync.Mutex
	hints  map[uuid.UUID]int // asset id -> attempts so far
	rerun  atomic.Bool
	last   atomic.Pointer[RunInfo]
}

func (s *Syncer) Last() *RunInfo { return s.last.Load() }

// AddHint records an asset id taken from a webhook payload. A hint is never trusted: the next pass
// fetches the asset with the source account's own key and checks it really carries the share tag.
// Immich can announce a tag before the tag is visible through its API, so unresolved hints are retried
// with a short backoff (see retryHints).
func (s *Syncer) AddHint(id uuid.UUID) {
	s.hintMu.Lock()
	defer s.hintMu.Unlock()
	if s.hints == nil {
		s.hints = map[uuid.UUID]int{}
	}
	if _, ok := s.hints[id]; !ok {
		s.hints[id] = 0
	}
}

func (s *Syncer) takeHints() map[uuid.UUID]int {
	s.hintMu.Lock()
	defer s.hintMu.Unlock()
	out := s.hints
	s.hints = nil
	return out
}

const maxHintAttempts = 6 // backoff 1s,2s,4s,8s,16s,32s: ~1 minute before leaving it to the daily sweep

// retryHints re-queues hints that no source could confirm and schedules another pass for them.
func (s *Syncer) retryHints(ctx context.Context, hints map[uuid.UUID]int, resolved map[uuid.UUID]bool) {
	delay := time.Duration(0)
	s.hintMu.Lock()
	for id, attempts := range hints {
		if resolved[id] {
			if attempts > 0 {
				s.Log.Info("tag became visible after retries", "asset", id, "retries", attempts)
			}
			continue
		}
		if attempts+1 >= maxHintAttempts {
			s.Log.Warn("tag still not visible; leaving the asset to the next sweep", "asset", id)
			continue
		}
		if s.hints == nil {
			s.hints = map[uuid.UUID]int{}
		}
		s.hints[id] = attempts + 1
		if d := time.Second << attempts; d > delay {
			delay = d
		}
	}
	s.hintMu.Unlock()
	if delay > 0 {
		time.AfterFunc(delay, func() {
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

func (s *Syncer) once(ctx context.Context, reason string, cleanup bool, hints map[uuid.UUID]int) {
	info := &RunInfo{Started: time.Now(), Reason: reason}
	defer func() {
		info.Finished = time.Now()
		s.last.Store(info)
		s.Log.Info("pass finished", "reason", reason, "counts", info.Counts, "untagged", info.Untagged, "error", info.Error)
		if info.Error != "" || info.Counts.Errors > 0 || info.Counts.Refused > 0 {
			s.notify(fmt.Sprintf("immich-handoff: %+v %s", info.Counts, info.Error))
		}
	}()

	destUser, err := s.Dest.Me(ctx)
	if err != nil {
		info.Error = err.Error()
		return
	}
	inbox, err := s.Dest.FindTag(ctx, s.Cfg.InboxTag, !s.Cfg.DryRun)
	if err != nil {
		info.Error = err.Error()
		return
	}
	var inboxID uuid.UUID
	if inbox != nil {
		inboxID = *inbox
	}
	resolved := map[uuid.UUID]bool{}
	for i, src := range s.Sources {
		c := s.ingest(ctx, i+1, src, destUser.ID, inboxID, hints, resolved)
		info.Counts.add(c)
	}
	s.retryHints(ctx, hints, resolved)
	if cleanup && inbox != nil {
		n, err := s.cleanup(ctx, inboxID)
		info.Untagged = n
		if err != nil {
			info.Counts.Errors++
			s.Log.Error("cleanup failed", "err", err)
		}
	}
}

func (s *Syncer) ingest(ctx context.Context, n int, src *immich.API, destUser uuid.UUID, inbox uuid.UUID, hints map[uuid.UUID]int, resolved map[uuid.UUID]bool) (c Counts) {
	log := s.Log.With("source", n)
	me, err := src.Me(ctx)
	if err != nil {
		log.Error("cannot authenticate", "err", err)
		c.Errors++
		return
	}
	if me.ID == destUser {
		log.Error("source key belongs to the destination account; refusing")
		c.Errors++
		return
	}
	tag, err := src.FindTag(ctx, s.Cfg.ShareTag, false)
	if err != nil {
		log.Error("tag lookup failed", "err", err)
		c.Errors++
		return
	}
	if tag == nil {
		return
	}
	assets, err := src.Search(ctx, immich.MetadataSearchDto{TagIds: &[]uuid.UUID{*tag}})
	if err != nil {
		log.Error("search failed", "err", err)
		c.Errors++
		return
	}
	assets = s.withHinted(ctx, log, src, assets, hints, resolved)
	ids := make([]string, 0, len(assets))
	for _, a := range assets {
		ids = append(ids, a.Id.String())
	}
	log.Info("tagged assets found", "user", me.Email, "count", len(assets), "ids", ids)
	for _, a := range assets {
		switch out, err := s.process(ctx, src, a, inbox, *tag); {
		case err != nil:
			c.Errors++
			log.Error("asset failed; original kept", "asset", a.Id, "file", a.OriginalFileName, "err", err)
		case out == "imported":
			c.Imported++
		case out == "duplicate":
			c.Duplicate++
		case out == "refused":
			c.Refused++
		case out == "dryrun":
			c.DryRun++
		}
	}
	return
}

// process imports one asset. The original is deleted last, and only after the copy is verified.
func (s *Syncer) process(ctx context.Context, src *immich.API, a immich.AssetResponseDto, inbox, shareTag uuid.UUID) (string, error) {
	if s.Cfg.DryRun {
		s.Log.Info("[dry-run] would import and remove original", "asset", a.Id, "file", a.OriginalFileName)
		return "dryrun", nil
	}
	up, sum, err := immich.Transfer(ctx, src, s.Dest, a)
	if err != nil {
		return "", err
	}
	copy_, err := s.Dest.Asset(ctx, up.Id)
	if err != nil {
		return "", err
	}
	if copy_.IsTrashed {
		s.Log.Warn("a copy exists in the destination trash; import refused, original kept and retagged",
			"file", a.OriginalFileName, "tag", s.Cfg.RefusedTag)
		if err := s.markRefused(ctx, src, a.Id, shareTag); err != nil {
			return "", fmt.Errorf("import refused but could not retag the original: %w", err)
		}
		return "refused", nil
	}
	if !immich.ChecksumMatches(copy_.Checksum, sum) {
		return "", fmt.Errorf("checksum mismatch on destination copy %s", up.Id)
	}
	outcome := "duplicate"
	if up.Status == immich.AssetMediaStatusCreated {
		if err := s.Dest.TagAssets(ctx, inbox, up.Id); err != nil {
			return "", fmt.Errorf("tag copy %s: %w", up.Id, err)
		}
		outcome = "imported"
	}
	if err := src.DeleteAsset(ctx, a.Id, s.Cfg.DeleteForce); err != nil {
		return "", fmt.Errorf("copy verified but original not removed: %w", err)
	}
	s.Log.Info(outcome, "file", a.OriginalFileName, "dest", up.Id)
	return outcome, nil
}

// cleanup removes the inbox tag from assets filed in an album or unfiled for ExpiryDays. Never deletes assets.
func (s *Syncer) cleanup(ctx context.Context, inbox uuid.UUID) (int, error) {
	tags := &[]uuid.UUID{inbox}
	tagged, err := s.Dest.Search(ctx, immich.MetadataSearchDto{TagIds: tags})
	if err != nil || len(tagged) == 0 {
		return 0, err
	}
	yes := true
	unfiled, err := s.Dest.Search(ctx, immich.MetadataSearchDto{TagIds: tags, IsNotInAlbum: &yes})
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().AddDate(0, 0, -s.Cfg.ExpiryDays)
	expired, err := s.Dest.Search(ctx, immich.MetadataSearchDto{TagIds: tags, IsNotInAlbum: &yes, UpdatedBefore: &cutoff})
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
		s.Log.Info("[dry-run] would untag", "count", len(drop))
		return 0, nil
	}
	return len(drop), s.Dest.UntagAssets(ctx, inbox, drop)
}

// withHinted adds assets named by webhook events that the tag search did not return, after checking
// with this account's own key that they exist and carry the share tag. Hints that this account can
// confirm (or that are trashed, so there is nothing to do) are recorded in resolved.
func (s *Syncer) withHinted(ctx context.Context, log *slog.Logger, src *immich.API, found []immich.AssetResponseDto, hints map[uuid.UUID]int, resolved map[uuid.UUID]bool) []immich.AssetResponseDto {
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
		if !hasTag(a, s.Cfg.ShareTag) {
			continue // announced by an event but the tag is not visible yet; retried
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

// markRefused swaps the share tag for the refused tag on the source original, so the refusal is visible
// to its owner in the Immich app and the photo is not retried (and not reported) on every pass.
// Re-adding the share tag after clearing the destination trash retries it.
func (s *Syncer) markRefused(ctx context.Context, src *immich.API, id, shareTag uuid.UUID) error {
	if s.Cfg.RefusedTag == "" {
		return nil
	}
	tag, err := src.FindTag(ctx, s.Cfg.RefusedTag, true)
	if err != nil || tag == nil {
		return fmt.Errorf("ensure tag %q: %v", s.Cfg.RefusedTag, err)
	}
	if err := src.TagAssets(ctx, *tag, id); err != nil {
		return err
	}
	return src.UntagAssets(ctx, shareTag, []uuid.UUID{id})
}
