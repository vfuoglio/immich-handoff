package notify

import (
	"fmt"
	"sync"
	"time"
)

// DefaultRemind is how long a problem that lasts is left alone before the administrator is reminded of it.
const DefaultRemind = 24 * time.Hour

// maxTracked bounds how many problems are remembered at once.
const maxTracked = 200

// Tracker keeps the administrator from being told about the same problem again and again. A problem has a key (a
// service, "backup"); the first report is sent, repeats are held back, one reminder goes out after Remind if it still
// lasts, and when it ends a message says so. What it remembers lives in memory only: after a restart a problem that
// still lasts is told once more.
type Tracker struct {
	Send   Func
	Remind time.Duration    // default DefaultRemind
	Now    func() time.Time // default time.Now

	mu   sync.Mutex
	seen map[string]*tracked
}

type tracked struct{ since, last time.Time }

// NewTracker makes a tracker that sends through send.
func NewTracker(send Func) *Tracker { return &Tracker{Send: send} }

func (t *Tracker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Tracker) remind() time.Duration {
	if t.Remind > 0 {
		return t.Remind
	}
	return DefaultRemind
}

// Report says that a problem exists now. It is sent the first time, and again (as a reminder) only when the last
// message about it is older than Remind.
func (t *Tracker) Report(key string, m Message) {
	var out *Message
	t.mu.Lock()
	now := t.now()
	switch e := t.seen[key]; {
	case e == nil:
		if t.seen == nil {
			t.seen = map[string]*tracked{}
		}
		if len(t.seen) < maxTracked {
			t.seen[key] = &tracked{since: now, last: now}
		}
		out = &m
	case now.Sub(e.last) >= t.remind():
		e.last = now
		r := m
		r.Text = "Still not fixed after " + ago(now.Sub(e.since)) + ": " + m.Text
		out = &r
	}
	t.mu.Unlock()
	if out != nil && t.Send != nil {
		t.Send(*out)
	}
}

// Clear says that the problem is over. A message goes out only if it had been reported.
func (t *Tracker) Clear(key string, m Message) {
	t.mu.Lock()
	_, was := t.seen[key]
	delete(t.seen, key)
	t.mu.Unlock()
	if was && t.Send != nil {
		t.Send(m)
	}
}

// ago says how long in words a person reads at a glance: hours up to two days, then days.
func ago(d time.Duration) string {
	h := int(d.Round(time.Hour) / time.Hour)
	switch {
	case h < 1:
		return "an hour"
	case h == 1:
		return "1 hour"
	case h < 48:
		return fmt.Sprintf("%d hours", h)
	}
	return fmt.Sprintf("%d days", h/24)
}
