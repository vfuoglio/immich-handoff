package syncer_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/live"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

type pages struct {
	bus                                     *live.Bus
	sender, receiver, owner, stranger, root *live.Sub
	closers                                 []func()
}

// open pages for the five kinds of person who might be looking while a photo is handed over
func watch(t *testing.T, r *limitRig) *pages {
	t.Helper()
	p := &pages{bus: &live.Bus{Window: 15 * time.Millisecond}}
	senderID, receiverID, ownerID := uuid.New(), uuid.New(), uuid.New()
	r.alice.UserID, r.svc.Receivers[0].UserID, r.svc.OwnerID = senderID, receiverID, ownerID
	r.s.Bus = p.bus
	sub := func(id uuid.UUID, all bool) *live.Sub {
		s, c := p.bus.Subscribe(id, all)
		p.closers = append(p.closers, c)
		return s
	}
	p.sender, p.receiver, p.owner = sub(senderID, false), sub(receiverID, false), sub(ownerID, false)
	p.stranger, p.root = sub(uuid.New(), false), sub(uuid.New(), true)
	t.Cleanup(func() {
		for _, c := range p.closers {
			c()
		}
	})
	return p
}

func collect(s *live.Sub) []live.Event {
	var out []live.Event
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case e := <-s.C:
			out = append(out, e)
		case <-deadline:
			return out
		}
	}
}

func kinds(evs []live.Event) string {
	var k []string
	for _, e := range evs {
		k = append(k, string(e.Kind))
	}
	return strings.Join(k, ",")
}

func TestAHandOverIsToldToTheSenderTheReceiversAndTheOwnerOnly(t *testing.T) {
	r := newLimitRig(t)
	p := watch(t, r)
	r.s.Run(context.Background(), "test", false)
	for name, s := range map[string]*live.Sub{"sender": p.sender, "receiver": p.receiver, "owner": p.owner, "administrator": p.root} {
		evs := collect(s)
		if !strings.Contains(kinds(evs), "started") || !strings.Contains(kinds(evs), "handed") {
			t.Errorf("%s must hear that a photo started and was handed over: %s", name, kinds(evs))
		}
		for _, e := range evs {
			if e.Service != "default" || e.Sender != "alice" || e.Count != 1 {
				t.Errorf("%s: %+v", name, e)
			}
		}
	}
	if evs := collect(p.stranger); len(evs) != 0 {
		t.Errorf("someone with no part in it must hear nothing: %s", kinds(evs))
	}
}

func TestAnEventNamesNoPhotoOrFile(t *testing.T) {
	r := newLimitRig(t)
	p := watch(t, r)
	r.s.Run(context.Background(), "test", false)
	for _, e := range collect(p.root) {
		b, _ := json.Marshal(e)
		for _, secret := range []string{"IMG_1.jpg", srcAsset, dstAsset, "src", "dest"} {
			if strings.Contains(string(b), secret) {
				t.Errorf("an event must name no photo, file or key (%q): %s", secret, b)
			}
		}
	}
}

func TestAPhotoTheReceiverAlreadyHadIsStillHandedOverButSaysSo(t *testing.T) {
	f := &fakeImmich{uploadStatus: "duplicate"}
	r := newLimitRigWith(t, f)
	p := watch(t, r)
	r.s.Run(context.Background(), "test", false)
	var got live.Event
	for _, e := range collect(p.owner) {
		if e.Kind == live.Handed {
			got = e
		}
	}
	if got.Reason != "duplicate" {
		t.Errorf("a duplicate must say so: %+v", got)
	}
}

func TestARefusedAndAFailedPhotoAreToldToo(t *testing.T) {
	r := newLimitRigWith(t, &fakeImmich{uploadStatus: "duplicate", destTrashed: true})
	p := watch(t, r)
	r.s.Run(context.Background(), "test", false)
	if got := kinds(collect(p.sender)); !strings.Contains(got, "refused") {
		t.Errorf("a copy in the receiver's trash must be told as refused: %s", got)
	}

	r = newLimitRigWith(t, &fakeImmich{uploadStatus: "created", storedChecksum: "bm9wZQ=="})
	p = watch(t, r)
	r.s.Run(context.Background(), "test", false)
	if got := kinds(collect(p.sender)); !strings.Contains(got, "failed") || strings.Contains(got, "handed") {
		t.Errorf("a checksum mismatch must be told as failed, not handed over: %s", got)
	}
}

func TestPhotosHeldByTheDailyLimitAreToldWithWhenTheyGo(t *testing.T) {
	r := newLimitRig(t)
	p := watch(t, r)
	r.svc.LimitPhotos = 1
	earlier := time.Now().Add(-2 * time.Hour)
	r.use.at, r.use.bytes = []time.Time{earlier}, []int64{1000}
	r.s.Run(context.Background(), "test", false)
	var held live.Event
	for _, e := range collect(p.owner) {
		if e.Kind == live.Held {
			held = e
		}
	}
	if held.Reason != "limit" || held.Count != 1 || held.Until == nil || held.Until.Sub(earlier.Add(syncer.LimitWindow)) > time.Second {
		t.Errorf("held: %+v", held)
	}
	// the same wait on the next pass is not announced again
	r.s.Run(context.Background(), "test", false)
	for _, e := range collect(p.owner) {
		if e.Kind == live.Held {
			t.Errorf("an unchanged wait must not be announced again: %+v", e)
		}
	}
}

func TestAPausedServiceOrSenderMakesNoNoise(t *testing.T) {
	r := newLimitRig(t)
	p := watch(t, r)
	r.svc.Paused = true
	r.s.Run(context.Background(), "test", false)
	if evs := collect(p.root); len(evs) != 0 {
		t.Errorf("a paused service touches nothing and says nothing (the pause itself was announced when it happened): %s", kinds(evs))
	}
}

func TestADryRunMakesNoEvents(t *testing.T) {
	r := newLimitRig(t)
	p := watch(t, r)
	r.s.Cfg.DryRun = true
	r.s.Run(context.Background(), "test", false)
	if evs := collect(p.root); len(evs) != 0 {
		t.Errorf("a dry run hands nothing over: %s", kinds(evs))
	}
}
