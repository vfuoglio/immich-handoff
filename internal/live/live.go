// Package live carries what is happening to the pages that are open: a photo started, handed over, refused or failed,
// photos held back, a connection lost or restored, a service paused or resumed. Each event goes only to the people
// it concerns (the sender, the owner and the receivers of the service) and to an Immich administrator, never to
// anyone else. An event names no photo and no file: only the service, the sender's name and a count.
package live

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// Kind is what happened.
type Kind string

const (
	Started  Kind = "started"  // a photo is being handed over
	Handed   Kind = "handed"   // a photo was handed over (Reason "duplicate": the receiver already had it)
	Refused  Kind = "refused"  // the receiver has it in their trash; the original was kept
	Failed   Kind = "failed"   // it could not be handed over; the original was kept and it is retried
	Held     Kind = "held"     // photos wait, still tagged: Reason "limit", Count how many, Until when they can go
	Lost     Kind = "lost"     // a connection was cut (a key was deleted in Immich)
	Restored Kind = "restored" // an owner reconnected a service
	Paused   Kind = "paused"   // Reason "service" (the owner paused it) or "sender" (a sender paused themselves)
	Resumed  Kind = "resumed"  // the same two, resumed
	Limit    Kind = "limit"    // a sender reached the daily limit
	Resync   Kind = "resync"   // some events were dropped: ask for the current state again
)

// Event is one thing that happened. The fields after At are routing and never leave the server.
type Event struct {
	Kind    Kind       `json:"kind"`
	Service string     `json:"service,omitempty"`
	Sender  string     `json:"sender,omitempty"`
	Reason  string     `json:"reason,omitempty"`
	Count   int        `json:"count,omitempty"`
	Until   *time.Time `json:"until,omitempty"`
	At      time.Time  `json:"at"`

	to []uuid.UUID
}

// To says who the event concerns. The zero id is ignored.
func (e Event) To(users ...uuid.UUID) Event {
	e.to = nil
	for _, u := range users {
		if u != uuid.Nil {
			e.to = append(e.to, u)
		}
	}
	return e
}

func (e Event) concerns(user uuid.UUID) bool {
	for _, u := range e.to {
		if u == user {
			return true
		}
	}
	return false
}

// batched kinds describe single photos: a big upload would make hundreds, so within Window they become one event
// with a Count.
func (k Kind) batched() bool { return k == Started || k == Handed || k == Refused || k == Failed }

// Sub is one open page.
type Sub struct {
	C       chan Event
	user    uuid.UUID
	all     bool
	dropped bool
}

// Bus passes events to the pages that are open. A nil *Bus is valid and does nothing, so code that publishes needs
// no check.
type Bus struct {
	// Window is how long single-photo events are gathered into one (default 300 ms).
	Window time.Duration

	mu      sync.Mutex
	subs    map[*Sub]struct{}
	pending map[string]*pending
}

type pending struct {
	ev Event
	n  int
}

// Subscribe opens a feed for a person. An administrator (all) sees every event.
func (b *Bus) Subscribe(user uuid.UUID, all bool) (*Sub, func()) {
	s := &Sub{C: make(chan Event, 64), user: user, all: all}
	if b == nil {
		return s, func() {}
	}
	b.mu.Lock()
	if b.subs == nil {
		b.subs = map[*Sub]struct{}{}
	}
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s, func() {
		b.mu.Lock()
		delete(b.subs, s)
		b.mu.Unlock()
	}
}

// Publish sends an event to everyone it concerns (and every administrator).
func (b *Bus) Publish(e Event) {
	if b == nil {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if !e.Kind.batched() {
		b.deliver(e)
		return
	}
	key := string(e.Kind) + "\x00" + e.Service + "\x00" + e.Sender + "\x00" + e.Reason
	b.mu.Lock()
	if p, ok := b.pending[key]; ok {
		p.n++
		b.mu.Unlock()
		return
	}
	if b.pending == nil {
		b.pending = map[string]*pending{}
	}
	b.pending[key] = &pending{ev: e, n: 1}
	window := b.Window
	if window <= 0 {
		window = 300 * time.Millisecond
	}
	b.mu.Unlock()
	time.AfterFunc(window, func() {
		b.mu.Lock()
		p := b.pending[key]
		delete(b.pending, key)
		b.mu.Unlock()
		if p != nil {
			p.ev.Count = p.n
			b.deliver(p.ev)
		}
	})
}

func (b *Bus) deliver(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs {
		if !s.all && e.Kind != Resync && !e.concerns(s.user) {
			continue
		}
		if s.dropped && len(s.C) < cap(s.C)-1 { // room again: tell the page it missed something
			s.C <- Event{Kind: Resync, At: time.Now()}
			s.dropped = false
		}
		select {
		case s.C <- e:
		default:
			s.dropped = true
		}
	}
}
