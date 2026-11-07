package syncer

import (
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/live"
)

// emit tells the open pages something happened with a sender's photos in a service. It names no photo: only the
// service, the sender's name and what happened. It reaches the sender, the owner and the receivers, and every
// Immich administrator, nobody else. Without a Bus it does nothing.
func (s *Syncer) emit(kind live.Kind, svc *Service, snd *Account, reason string, count int, until *time.Time) {
	if s.Bus == nil {
		return
	}
	to := []uuid.UUID{snd.UserID, svc.OwnerID}
	for _, r := range svc.Receivers {
		to = append(to, r.UserID)
	}
	s.Bus.Publish(live.Event{Kind: kind, Service: svc.Label(), Sender: snd.Name, Reason: reason, Count: count, Until: until}.To(to...))
}
