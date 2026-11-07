package web_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/live"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

func withBus(bus *live.Bus) func(*web.Server) { return func(s *web.Server) { s.Bus = bus } }

// stream is an open /events connection; what the server sends arrives on lines.
type stream struct {
	status int
	lines  chan string
	cancel context.CancelFunc
}

func listenTo(t *testing.T, base, cookie string) *stream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/events", nil)
	req.Header.Set("Cookie", cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	st := &stream{status: res.StatusCode, lines: make(chan string, 64), cancel: cancel}
	if res.StatusCode != 200 {
		res.Body.Close()
		return st
	}
	go func() {
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			st.lines <- sc.Text()
		}
	}()
	// wait until the server says it is listening, so nothing published afterwards is missed
	for {
		select {
		case l := <-st.lines:
			if strings.HasPrefix(l, ": connected") {
				return st
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the stream never said it was connected")
		}
	}
}

// next returns the next live event on the stream, skipping the old "run" signal and comments.
func (s *stream) next(wait time.Duration) (map[string]any, bool) {
	deadline := time.After(wait)
	for {
		select {
		case l := <-s.lines:
			if strings.HasPrefix(l, "data: {") {
				var m map[string]any
				json.Unmarshal([]byte(strings.TrimPrefix(l, "data: ")), &m)
				return m, true
			}
		case <-deadline:
			return nil, false
		}
	}
}

func TestTheStreamCarriesLiveEventsToThoseTheyConcern(t *testing.T) {
	bus := &live.Bus{Window: 10 * time.Millisecond}
	app, _ := setup(t, withBus(bus))
	family := listenTo(t, app.URL, "sess=family") // receives from the configured service
	alice := listenTo(t, app.URL, "sess=alice")   // sends to it
	admin := listenTo(t, app.URL, "sess=admin")   // administers the server, takes part in nothing

	// an event for the receiver only
	bus.Publish(live.Event{Kind: live.Paused, Service: "family", Reason: "service"}.To(uuid.MustParse(familyID)))
	if ev, ok := family.next(time.Second); !ok || ev["kind"] != "paused" || ev["service"] != "family" || ev["reason"] != "service" {
		t.Errorf("the receiver must hear it: %v %v", ev, ok)
	}
	if ev, ok := admin.next(time.Second); !ok || ev["kind"] != "paused" {
		t.Errorf("the administrator hears everything: %v %v", ev, ok)
	}
	if ev, ok := alice.next(300 * time.Millisecond); ok {
		t.Errorf("a sender it does not concern must not hear it: %v", ev)
	}

	// a burst of photos arrives as one event with a count
	for i := 0; i < 50; i++ {
		bus.Publish(live.Event{Kind: live.Handed, Service: "family", Sender: "alice"}.To(uuid.MustParse(aliceID), uuid.MustParse(familyID)))
	}
	ev, ok := alice.next(time.Second)
	if !ok || ev["kind"] != "handed" || ev["count"] != float64(50) {
		t.Errorf("a burst is one event with a count: %v %v", ev, ok)
	}
	if _, ok := alice.next(200 * time.Millisecond); ok {
		t.Error("only one event for the whole burst")
	}
	// nothing in it that is routing or a person
	for k := range ev {
		switch k {
		case "kind", "service", "sender", "reason", "count", "until", "at":
		default:
			t.Errorf("unexpected field %q", k)
		}
	}
}

func TestOnlyMembersAndAdministratorsMayListen(t *testing.T) {
	app, _ := setup(t)
	if s := listenTo(t, app.URL, ""); s.status != 401 {
		t.Errorf("signed out: %d", s.status)
	}
	if s := listenTo(t, app.URL, "sess=other"); s.status != 403 {
		t.Errorf("someone who takes part in nothing: %d", s.status)
	}
	if s := listenTo(t, app.URL, "sess=admin"); s.status != 200 {
		t.Errorf("the administrator may listen: %d", s.status)
	}
	if s := listenTo(t, app.URL, "sess=alice"); s.status != 200 {
		t.Errorf("a member may listen: %d", s.status)
	}
}

func TestTheOldRunSignalStillComes(t *testing.T) {
	app, s := setup(t)
	st := listenTo(t, app.URL, "sess=alice")
	s.Run(context.Background(), "test", false)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case l := <-st.lines:
			if l == "event: run" {
				return
			}
		case <-deadline:
			t.Fatal("the run signal that refreshes the page's counters must still be sent")
		}
	}
}
