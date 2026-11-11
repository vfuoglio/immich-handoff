package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/notify"
)

// A sender can link one of their own albums to a service: everything in it is handed over, now and later,
// without a tag. Immich sends no event when a photo is added to an album, so linked albums are looked at on
// a timer (see HasWatchers and ALBUM_CHECK_MINUTES) as well as in every pass.

// bigBatch is how many photos from a linked album make a hand-over worth a notification when it is done.
const bigBatch = 20

// Reporter is implemented by a Provider that wants to record what a hand-over from a linked album did.
type Reporter interface {
	AlbumHandedOver(svc *Service, snd *Account, handed, refused, failed int)
}

// LimitReporter is implemented by a Provider that wants to hear when a sender's photos first start waiting for
// the daily cap.
type LimitReporter interface {
	LimitReached(svc *Service, snd *Account, waiting int, until time.Time)
}

// AcceptReporter is implemented by a Provider that wants to hear when photos were not handed over because the service
// does not take them in (its rules), once per sender per pass.
type AcceptReporter interface {
	NotAccepted(svc *Service, snd *Account, n int)
}

// Ledger is implemented by a Provider that remembers which copy came from which photo (for the hand-overs of people who
// joined from the page), so a sender can take their last hand-over back. batch is one sender's work in one pass. remote is
// a copy on another Immich server (a bridge), not in the receiver's own library.
type Ledger interface {
	Remember(ctx context.Context, snd *Account, batch string, senderAsset, receiverAsset uuid.UUID, clip, remote bool)
}

// ExpiryReporter is implemented by a Provider that wants to hear when copies that reached the end of their lifetime
// were moved to the receiver's trash.
type ExpiryReporter interface {
	Expired(service string, rcv *Account, n int)
}

// AlbumPending returns the photos in the sender's linked album that still have to be handed over: photos the
// sender owns (an album can hold other people's photos) that are not in the trash and do not already carry
// the service's "handed over" or "refused" tag. Those tags are how a photo that stays in the album (copy
// mode) is remembered, so it is not handed over again; a refused one is left alone until the sender removes
// that tag. The web page uses it too, to count what is waiting.
func AlbumPending(ctx context.Context, svc *Service, snd *Account, owner uuid.UUID) ([]immich.AssetResponseDto, error) {
	items, err := snd.API.AlbumAssets(ctx, snd.WatchAlbum)
	if err != nil {
		return nil, err
	}
	skip := map[uuid.UUID]bool{}
	for _, name := range []string{svc.SentTag, svc.RefusedTag} {
		if name == "" {
			continue
		}
		id, err := snd.API.FindTag(ctx, name, false)
		if err != nil {
			return nil, err
		}
		if id == nil {
			continue
		}
		tagged, err := snd.API.Search(ctx, immich.MetadataSearchDto{TagIds: &[]uuid.UUID{*id}})
		if err != nil {
			return nil, err
		}
		for _, a := range tagged {
			skip[a.Id] = true
		}
	}
	var out []immich.AssetResponseDto
	for _, a := range items {
		if a.OwnerId != owner || a.IsTrashed || skip[a.Id] {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// HasWatchers reports whether any sender has linked an album, so the timer has something to look at.
func (s *Syncer) HasWatchers() bool {
	for _, svc := range s.Current() {
		for _, a := range svc.Senders {
			if a.WatchAlbum != uuid.Nil {
				return true
			}
		}
	}
	return false
}

// watchProblem reports, once until it works again, that a linked album cannot be read (deleted, or the key
// may not read albums). Photos tagged for the service are still handed over.
func (s *Syncer) watchProblem(log *slog.Logger, svc *Service, snd *Account, err error) {
	log.Warn("cannot read the linked album; photos tagged for the service are still handed over", "album", snd.WatchAlbumName, "err", err)
	key := "watch/" + svc.Label() + "/" + snd.Name
	s.albumMu.Lock()
	first := !s.albumBroken[key]
	if s.albumBroken == nil {
		s.albumBroken = map[string]bool{}
	}
	s.albumBroken[key] = true
	s.albumMu.Unlock()
	if first {
		s.notify(notify.Problem, fmt.Sprintf("cannot read the album %q that %s linked to %s (%v); photos tagged for the service are still handed over. If the album was deleted, link another on the page.", snd.WatchAlbumName, snd.Name, svc.Label(), err))
	}
}

func (s *Syncer) watchOK(svc *Service, snd *Account) {
	s.albumMu.Lock()
	delete(s.albumBroken, "watch/"+svc.Label()+"/"+snd.Name)
	s.albumMu.Unlock()
}

// publishSoon tells the open pages that something changed, at most about once a second, so their "waiting"
// counters count down photo by photo during a long hand-over without a request for every photo.
func (s *Syncer) publishSoon() {
	now := time.Now().UnixNano()
	last := s.lastPublish.Load()
	if now-last < int64(time.Second) {
		return
	}
	if s.lastPublish.CompareAndSwap(last, now) {
		s.publish()
	}
}

// reportAlbum records what a pass did for a sender's linked album, and notifies for a big hand-over.
func (s *Syncer) reportAlbum(svc *Service, snd *Account, handed, refused, failed int) {
	if snd.WatchAlbum == uuid.Nil || handed+refused+failed == 0 {
		return
	}
	if r, ok := s.Provider.(Reporter); ok {
		r.AlbumHandedOver(svc, snd, handed, refused, failed)
	}
	if handed+refused >= bigBatch {
		msg := fmt.Sprintf("%d photos from the album %q were handed over to %s for %s", handed, snd.WatchAlbumName, svc.Label(), snd.Name)
		if refused > 0 {
			msg += fmt.Sprintf(" (%d refused: the receiver has them in the trash)", refused)
		}
		if failed > 0 {
			msg += fmt.Sprintf(", %d failed and will be retried", failed)
		}
		s.notify(notify.Info, msg)
	}
}
