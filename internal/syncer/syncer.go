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

	"github.com/vfuoglio/immich-sync-console/internal/immich"
)

type Config struct {
	ShareTag, InboxTag string
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

	mu    sync.Mutex
	rerun atomic.Bool
	last  atomic.Pointer[RunInfo]
}

func (s *Syncer) Last() *RunInfo { return s.last.Load() }

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
		s.once(ctx, reason, cleanup)
		if !s.rerun.Load() {
			return
		}
		reason, cleanup = "coalesced event", false
	}
}

func (s *Syncer) once(ctx context.Context, reason string, cleanup bool) {
	info := &RunInfo{Started: time.Now(), Reason: reason}
	defer func() {
		info.Finished = time.Now()
		s.last.Store(info)
		s.Log.Info("pass finished", "reason", reason, "counts", info.Counts, "untagged", info.Untagged, "error", info.Error)
		if info.Error != "" || info.Counts.Errors > 0 || info.Counts.Refused > 0 {
			s.notify(fmt.Sprintf("immich-sync-console: %+v %s", info.Counts, info.Error))
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
	for i, src := range s.Sources {
		c := s.ingest(ctx, i+1, src, destUser.ID, inboxID)
		info.Counts.add(c)
	}
	if cleanup && inbox != nil {
		n, err := s.cleanup(ctx, inboxID)
		info.Untagged = n
		if err != nil {
			info.Counts.Errors++
			s.Log.Error("cleanup failed", "err", err)
		}
	}
}

func (s *Syncer) ingest(ctx context.Context, n int, src *immich.API, destUser uuid.UUID, inbox uuid.UUID) (c Counts) {
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
	log.Info("tagged assets found", "user", me.Email, "count", len(assets))
	for _, a := range assets {
		switch out, err := s.process(ctx, src, a, inbox); {
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
func (s *Syncer) process(ctx context.Context, src *immich.API, a immich.AssetResponseDto, inbox uuid.UUID) (string, error) {
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
		s.Log.Warn("a copy exists in the destination trash; import refused, original kept", "file", a.OriginalFileName)
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
