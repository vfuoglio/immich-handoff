package syncer

import (
	"context"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
)

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
