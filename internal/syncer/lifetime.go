package syncer

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
)

// Waiting is why a photo was not finished: receivers outside this Immich that were not ready, so it stays tagged and the original is
// not touched.
type Waiting struct{ For []string }

func (w *Waiting) Error() string { return "waiting for " + strings.Join(w.For, ", ") }

// RemoteLedger is implemented by a Provider that remembers what each receiver outside this Immich holds, by the reference it gave
// (a path, a key or an id there), so a hand-over can be taken back and a retry delivers only to the receivers that lack the photo.
type RemoteLedger interface {
	RememberRemote(ctx context.Context, snd *Account, batch string, senderAsset uuid.UUID, dest, ref string, clip bool)
	// Delivered says whether dest holds the photo (not taken back since).
	Delivered(ctx context.Context, senderAsset uuid.UUID, dest string) bool
}

func (s *Syncer) rememberRemote(ctx context.Context, snd *Account, batch string, senderAsset uuid.UUID, dest, ref string, clip bool) {
	if l, ok := s.Provider.(RemoteLedger); ok {
		l.RememberRemote(ctx, snd, batch, senderAsset, dest, ref, clip)
		return
	}
	if l, ok := s.Provider.(Ledger); ok { // an older provider remembers only ids that are Immich's
		if id, err := uuid.Parse(ref); err == nil {
			l.Remember(ctx, snd, batch, senderAsset, id, clip, true)
		}
	}
}

func (s *Syncer) delivered(ctx context.Context, senderAsset uuid.UUID, dest string) bool {
	if l, ok := s.Provider.(RemoteLedger); ok {
		return l.Delivered(ctx, senderAsset, dest)
	}
	return false
}

// remember tells a Provider that keeps a ledger which copy came from which photo.
func (s *Syncer) remember(ctx context.Context, snd *Account, batch string, senderAsset, receiverAsset uuid.UUID, clip, remote bool) {
	if l, ok := s.Provider.(Ledger); ok {
		l.Remember(ctx, snd, batch, senderAsset, receiverAsset, clip, remote)
	}
}

// expire moves copies that outlived their service's lifetime to the receiver's trash (never deleted for good: the
// receiver's trash keeps them for as long as Immich does). They are copies nobody filed in an album and nobody took
// the inbox tag off, which is how a person keeps one. A key that may not delete is said so once.
func (s *Syncer) expire(ctx context.Context, rcv *Account, service string, ended []immich.AssetResponseDto) {
	if len(ended) == 0 {
		return
	}
	ids := make([]uuid.UUID, len(ended))
	for i, a := range ended {
		ids[i] = a.Id
	}
	if s.Cfg.DryRun {
		s.Log.Info("[dry-run] would move expired copies to the trash", "receiver", rcv.Name, "count", len(ids))
		return
	}
	if err := rcv.API.DeleteAssets(ctx, ids, false); err != nil {
		if immich.IsForbidden(err) {
			s.warnOnce("lifetime/"+rcv.Name, s.Log, "the receiver's key may not delete, so copies that reached the end of their lifetime stay; their owner gets the permission by opening the page, or add asset.delete to a key made by hand",
				rcv.Name+"'s key may not move photos to the trash, so copies that reached the end of their lifetime stay. The key needs asset.delete: its owner gets it by opening the page once, or add it to a key made by hand.")
		} else {
			s.Log.Warn("could not move expired copies to the trash", "receiver", rcv.Name, "err", err)
		}
		return
	}
	s.Log.Info("cleanup: expired copies moved to the trash", "receiver", rcv.Name, "service", service, "count", len(ids))
	if r, ok := s.Provider.(ExpiryReporter); ok {
		r.Expired(service, rcv, len(ids))
	}
}
