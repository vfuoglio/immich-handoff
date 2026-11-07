package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// LimitWindow is how far back the daily cap looks. A variable so tests can shorten it.
var LimitWindow = 24 * time.Hour

// Snapshot is what a sender has handed over to a service within the window.
type Snapshot struct {
	Tracked bool // false for senders from the configuration: nothing is recorded for them, so no cap applies
	Photos  int
	Bytes   int64
	Oldest  time.Time // when the earliest of them was handed over (zero when none)
}

// Usage keeps count of what each sender hands over, for the daily cap. The hub keeps it in the data file.
type Usage interface {
	Handed(ctx context.Context, acct *Account, since time.Time) (Snapshot, error)
	Record(ctx context.Context, acct *Account, bytes int64) error
}

// Held says a sender's photos are waiting for the daily cap to free up.
type Held struct {
	Waiting int       // photos left waiting this pass
	Until   time.Time // when the oldest hand-over leaves the window and something can go
	Photos  int       // what the sender has handed over within the window
	Bytes   int64
}

// HeldFor reports whether the sender's photos are waiting for the cap.
func (s *Syncer) HeldFor(a *Account) (Held, bool) {
	s.heldMu.Lock()
	defer s.heldMu.Unlock()
	h, ok := s.held[a]
	return h, ok
}

// Capped reports whether a sender is over a daily cap, given what they handed over in the window.
func Capped(svc *Service, snap Snapshot) bool {
	if !snap.Tracked {
		return false
	}
	return (svc.LimitPhotos > 0 && snap.Photos >= svc.LimitPhotos) ||
		(svc.LimitMB > 0 && snap.Bytes >= int64(svc.LimitMB)<<20)
}

// hold records that photos are waiting for the cap, tells the people involved the first time, and arranges for
// a pass to run when the oldest hand-over leaves the window.
func (s *Syncer) hold(ctx context.Context, log *slog.Logger, svc *Service, snd *Account, snap Snapshot, waiting int) {
	until := snap.Oldest.Add(LimitWindow)
	s.heldMu.Lock()
	if s.held == nil {
		s.held, s.wake = map[*Account]Held{}, map[*Account]*time.Timer{}
	}
	_, was := s.held[snd]
	s.held[snd] = Held{Waiting: waiting, Until: until, Photos: snap.Photos, Bytes: snap.Bytes}
	if t := s.wake[snd]; t != nil {
		t.Stop()
	}
	delay := time.Until(until) + time.Second
	if delay < time.Second {
		delay = time.Second
	}
	s.wake[snd] = time.AfterFunc(delay, func() { s.Run(ctx, "daily limit window moved", false) })
	s.heldMu.Unlock()
	log.Info("daily limit reached; photos wait", "waiting", waiting, "until", until.Format(time.RFC3339), "handed", snap.Photos)
	if !was {
		if r, ok := s.Provider.(LimitReporter); ok {
			r.LimitReached(svc, snd, waiting, until)
		}
		s.notify(fmt.Sprintf("immich-handoff: %s reached the daily limit for %s; %d photo(s) wait until %s", snd.Name, svc.Label(), waiting, until.Local().Format("2 Jan 15:04")))
	}
}

// release forgets a sender's wait (they are under the cap again, or paused, or nothing waits).
func (s *Syncer) release(snd *Account) {
	s.heldMu.Lock()
	defer s.heldMu.Unlock()
	if _, ok := s.held[snd]; !ok {
		return
	}
	delete(s.held, snd)
	if t := s.wake[snd]; t != nil {
		t.Stop()
		delete(s.wake, snd)
	}
}

func sizeOf(bytes *int) int64 {
	if bytes == nil {
		return 0
	}
	return int64(*bytes)
}
