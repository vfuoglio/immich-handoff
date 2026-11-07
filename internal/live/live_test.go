package live_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/live"
)

var (
	sender, owner, receiver, other, admin = uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
)

func next(t *testing.T, s *live.Sub) (live.Event, bool) {
	t.Helper()
	select {
	case e := <-s.C:
		return e, true
	case <-time.After(400 * time.Millisecond):
		return live.Event{}, false
	}
}

func TestAnEventGoesOnlyToThoseItConcernsAndToAdministrators(t *testing.T) {
	bus := &live.Bus{Window: 10 * time.Millisecond}
	subSender, c1 := bus.Subscribe(sender, false)
	subOwner, c2 := bus.Subscribe(owner, false)
	subReceiver, c3 := bus.Subscribe(receiver, false)
	subOther, c4 := bus.Subscribe(other, false)
	subAdmin, c5 := bus.Subscribe(admin, true)
	for _, c := range []func(){c1, c2, c3, c4, c5} {
		defer c()
	}
	bus.Publish(live.Event{Kind: live.Paused, Service: "club", Reason: "service"}.To(owner, sender, receiver, uuid.Nil))
	for name, s := range map[string]*live.Sub{"sender": subSender, "owner": subOwner, "receiver": subReceiver, "admin": subAdmin} {
		if e, ok := next(t, s); !ok || e.Kind != live.Paused || e.Service != "club" {
			t.Errorf("%s must hear it: %+v %v", name, e, ok)
		}
	}
	if e, ok := next(t, subOther); ok {
		t.Errorf("someone it does not concern must hear nothing: %+v", e)
	}
}

func TestABurstOfPhotosBecomesFewEventsWithACount(t *testing.T) {
	bus := &live.Bus{Window: 60 * time.Millisecond}
	s, cancel := bus.Subscribe(owner, false)
	defer cancel()
	for i := 0; i < 200; i++ {
		bus.Publish(live.Event{Kind: live.Handed, Service: "club", Sender: "bob"}.To(owner))
	}
	e, ok := next(t, s)
	if !ok || e.Kind != live.Handed || e.Count != 200 {
		t.Fatalf("200 photos must arrive as one event with a count: %+v %v", e, ok)
	}
	if e2, ok := next(t, s); ok {
		t.Errorf("nothing else expected: %+v", e2)
	}
	// different senders, services or kinds are different batches
	bus.Publish(live.Event{Kind: live.Handed, Service: "club", Sender: "bob"}.To(owner))
	bus.Publish(live.Event{Kind: live.Handed, Service: "club", Sender: "carol"}.To(owner))
	bus.Publish(live.Event{Kind: live.Failed, Service: "club", Sender: "bob"}.To(owner))
	got := map[string]int{}
	for i := 0; i < 3; i++ {
		e, ok := next(t, s)
		if !ok {
			t.Fatal("expected three batches")
		}
		got[string(e.Kind)+"/"+e.Sender] += e.Count
	}
	if got["handed/bob"] != 1 || got["handed/carol"] != 1 || got["failed/bob"] != 1 {
		t.Errorf("batches: %v", got)
	}
}

func TestStateChangesAreNotBatched(t *testing.T) {
	bus := &live.Bus{Window: 200 * time.Millisecond}
	s, cancel := bus.Subscribe(owner, false)
	defer cancel()
	start := time.Now()
	bus.Publish(live.Event{Kind: live.Lost, Service: "club"}.To(owner))
	e, ok := next(t, s)
	if !ok || e.Kind != live.Lost || time.Since(start) > 100*time.Millisecond {
		t.Errorf("a connection lost is told at once: %+v after %v", e, time.Since(start))
	}
}

func TestASlowPageIsToldItMissedSomething(t *testing.T) {
	bus := &live.Bus{}
	s, cancel := bus.Subscribe(owner, false)
	defer cancel()
	for i := 0; i < 200; i++ { // far more than the page reads
		bus.Publish(live.Event{Kind: live.Paused, Service: "club"}.To(owner))
	}
	for len(s.C) > 5 { // the page catches up
		<-s.C
	}
	bus.Publish(live.Event{Kind: live.Resumed, Service: "club"}.To(owner))
	var kinds []live.Kind
	for len(s.C) > 0 {
		kinds = append(kinds, (<-s.C).Kind)
	}
	if len(kinds) < 2 || kinds[len(kinds)-2] != live.Resync || kinds[len(kinds)-1] != live.Resumed {
		t.Errorf("after dropping events the page must be told to ask again, then hear the next one: %v", kinds)
	}
}

func TestAnEventNamesNoPhotoAndNoPerson(t *testing.T) {
	until := time.Now().Add(time.Hour)
	b, err := json.Marshal(live.Event{Kind: live.Held, Service: "club", Sender: "bob", Reason: "limit", Count: 3, Until: &until, At: time.Now()}.To(owner, sender))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{owner.String(), sender.String()} {
		if strings.Contains(string(b), id) {
			t.Errorf("routing must never leave the server: %s", b)
		}
	}
	var keys map[string]any
	json.Unmarshal(b, &keys)
	for k := range keys {
		switch k {
		case "kind", "service", "sender", "reason", "count", "until", "at":
		default:
			t.Errorf("unexpected field %q in an event", k)
		}
	}
}

func TestANilBusIsHarmless(t *testing.T) {
	var bus *live.Bus
	bus.Publish(live.Event{Kind: live.Paused})
	s, cancel := bus.Subscribe(owner, false)
	cancel()
	if s == nil {
		t.Error("a feed is still returned")
	}
}
