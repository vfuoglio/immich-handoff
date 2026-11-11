// Package notify sends the messages the app has for whoever runs it: to an ntfy topic, to a JSON webhook such as
// Home Assistant's, or to several of them, each taking only the kinds of message it asked for. It depends on nothing
// else in the app, so every part can hold a Func and be sure a message goes the same way whoever sends it.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Kind says what a message is about, so a destination can take only some.
type Kind string

const (
	Problem Kind = "problem" // something failed or needs the administrator
	DropBox Kind = "dropbox" // a drop box paused, is about to close, or closed
	Backup  Kind = "backup"  // a backup failed, was forced or was downloaded
	Bridge  Kind = "bridge"  // a receiver on another server became unreachable, was revoked or is back
	Info    Kind = "info"    // worth knowing, nothing to do
)

// Kinds lists every kind, in the order the documentation names them.
var Kinds = []Kind{Problem, DropBox, Backup, Bridge, Info}

// Message is one thing to tell.
type Message struct {
	Kind Kind
	Text string
	Link string // a path on the app's own pages, such as "admin"; made into a link when the app's address is known (PUBLIC_APP_URL); problems, backups and bridges default to the administrator's page, the rest to the front page
}

// Func is what the rest of the app holds. It never blocks and never fails the caller.
type Func func(Message)

// Destination is one place messages go.
type Destination struct {
	Name   string
	Format string // "ntfy" or "json"
	URL    string
	Auth   string // sent as a bearer token when set
	Kinds  map[Kind]bool
}

// Formats a destination may use.
const (
	FormatNtfy = "ntfy"
	FormatJSON = "json"
)

func (d Destination) wants(k Kind) bool { return len(d.Kinds) == 0 || d.Kinds[k] }

func title(k Kind) string {
	switch k {
	case Problem:
		return "immich-handoff: needs attention"
	case DropBox:
		return "immich-handoff: drop box"
	case Backup:
		return "immich-handoff: backup"
	case Bridge:
		return "immich-handoff: another server"
	}
	return "immich-handoff"
}

func priority(k Kind) string {
	switch k {
	case Problem:
		return "4"
	case Info:
		return "2"
	}
	return "3"
}

// Notifier delivers messages in the background.
type Notifier struct {
	dests  []Destination
	appURL string
	log    *slog.Logger
	client *http.Client
	queue  chan Message
	wait   []time.Duration // pauses before each new attempt after a failure that may pass
	done   chan struct{}
	once   sync.Once

	mu      sync.Mutex
	dropped bool // the queue was full and that was logged; reset when it drains
}

// New starts a notifier. appURL is the address of the app as people reach it (may be empty).
func New(dests []Destination, appURL string, log *slog.Logger) *Notifier {
	if log == nil {
		log = slog.Default()
	}
	n := &Notifier{
		dests: dests, appURL: strings.TrimRight(appURL, "/"), log: log,
		client: &http.Client{
			Timeout: 15 * time.Second,
			// A redirect would turn the POST into a GET without anyone noticing; it is an error to fix in the address.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		queue: make(chan Message, 64),
		wait:  []time.Duration{2 * time.Second, 10 * time.Second},
		done:  make(chan struct{}),
	}
	go n.run()
	return n
}

// Enabled reports whether any destination exists.
func (n *Notifier) Enabled() bool { return n != nil && len(n.dests) > 0 }

// Send queues a message. It returns at once; when the queue is full the message is dropped and that is logged once.
func (n *Notifier) Send(m Message) {
	if !n.Enabled() {
		return
	}
	select {
	case n.queue <- m:
	default:
		n.mu.Lock()
		first := !n.dropped
		n.dropped = true
		n.mu.Unlock()
		if first {
			n.log.Warn("notifications are piling up; messages are dropped until the destinations answer")
		}
	}
}

// Close stops taking messages and gives the ones already queued up to five seconds.
func (n *Notifier) Close() {
	if n == nil {
		return
	}
	n.once.Do(func() { close(n.queue) })
	select {
	case <-n.done:
	case <-time.After(5 * time.Second):
	}
}

func (n *Notifier) run() {
	defer close(n.done)
	for m := range n.queue {
		n.mu.Lock()
		n.dropped = false
		n.mu.Unlock()
		for _, d := range n.dests {
			if d.wants(m.Kind) {
				n.deliver(d, m)
			}
		}
	}
}

func (n *Notifier) deliver(d Destination, m Message) {
	link := ""
	if n.appURL != "" {
		path := m.Link
		if path == "" && (m.Kind == Problem || m.Kind == Backup || m.Kind == Bridge) {
			path = "admin" // what needs attention is on the administrator's page
		}
		link = n.appURL + "/" + strings.TrimLeft(path, "/")
	}
	var err error
	for attempt := 0; ; attempt++ {
		var retry bool
		retry, err = n.post(d, m, link)
		if err == nil {
			return
		}
		if !retry || attempt >= len(n.wait) {
			break
		}
		time.Sleep(n.wait[attempt])
	}
	// the address is never logged: a topic or a webhook id is a secret
	n.log.Warn("notification failed", "destination", d.Name, "err", err)
}

// post makes one attempt. retry says whether trying again could help (the network or the other side's server, not a
// refusal of what we sent).
func (n *Notifier) post(d Destination, m Message, link string) (retry bool, err error) {
	var body io.Reader
	h := http.Header{}
	switch d.Format {
	case FormatJSON:
		b, _ := json.Marshal(map[string]string{
			"source": "immich-handoff", "kind": string(m.Kind), "title": title(m.Kind), "text": m.Text, "link": link,
			"time": time.Now().UTC().Format(time.RFC3339),
		})
		body = bytes.NewReader(b)
		h.Set("Content-Type", "application/json")
	default:
		body = strings.NewReader(m.Text)
		h.Set("Content-Type", "text/plain; charset=utf-8")
		h.Set("Title", title(m.Kind))
		h.Set("Priority", priority(m.Kind))
		h.Set("Tags", string(m.Kind))
		if link != "" {
			h.Set("Click", link)
		}
	}
	if d.Auth != "" {
		h.Set("Authorization", "Bearer "+d.Auth)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, body)
	if rerr != nil {
		return false, fmt.Errorf("bad address")
	}
	req.Header = h
	resp, derr := n.client.Do(req)
	if derr != nil {
		// the error text holds the address; keep only what went wrong
		return true, fmt.Errorf("could not reach it (%s)", reason(derr))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return false, nil
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return false, fmt.Errorf("answered %d: it redirects, so use the address it redirects to", resp.StatusCode)
	case resp.StatusCode >= 500:
		return true, fmt.Errorf("answered %d", resp.StatusCode)
	}
	return false, fmt.Errorf("answered %d", resp.StatusCode)
}

// reason is a short, address-free account of a network error.
func reason(err error) string {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok || u.Unwrap() == nil {
			break
		}
		err = u.Unwrap()
	}
	return err.Error()
}
