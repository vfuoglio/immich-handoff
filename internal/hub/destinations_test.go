package hub_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/dest"
	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// refusing is a receiver that does not allow what it was given to be taken back.
type refusing struct{ *dest.Remote }

func (refusing) TakeBackRefs(context.Context, []string) error { return syncer.ErrCannotTakeBack }

// nasEnv makes a service whose photos go only to a destination, owned by ann, that bob has joined.
func nasEnv(t *testing.T, remote func(m *dest.Memory) syncer.Remote) (*env, context.Context, *dest.Memory, store.Link) {
	t.Helper()
	e, ctx := setup(t, true), context.Background()
	e.h.ReceiverDelete = true
	keys := len(e.f.keys)
	m := &dest.Memory{}
	e.h.Destinations = func(service string) []syncer.Remote {
		if service == "nas" {
			return []syncer.Remote{remote(m)}
		}
		return nil
	}
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "nas", Tag: "to-nas", Policy: store.PolicyOpen, DestinationOnly: true}); err != nil {
		t.Fatal(err)
	}
	if len(e.f.keys) != keys {
		t.Fatalf("a service with only destinations makes no key in the owner's Immich: %d keys before, %d after", keys, len(e.f.keys))
	}
	if err := e.h.Join(ctx, e.sess, bob, "nas", "", ""); err != nil {
		t.Fatal(err)
	}
	links, _ := e.st.Links(ctx)
	for _, l := range links {
		if l.Service == "nas" && l.ImmichUserID == bobID {
			return e, ctx, m, l
		}
	}
	t.Fatal("bob has no link")
	return nil, nil, nil, store.Link{}
}

func plain(m *dest.Memory) syncer.Remote { return &dest.Remote{Ident: "nas", Name: "the NAS", D: m} }

func TestAServiceWithOnlyDestinationsHasNoImmichReceiverAndNoKeyAndCannotBeAPool(t *testing.T) {
	e, ctx, _, _ := nasEnv(t, plain)
	var svc *syncer.Service
	for _, s := range e.h.Services() {
		if s.Label() == "nas" {
			svc = s
		}
	}
	if svc == nil || len(svc.Receivers) != 0 || len(svc.Remotes) != 1 || svc.Remotes[0].ID() != "nas" || svc.Broken != "" || len(svc.Senders) != 1 {
		t.Fatalf("%+v", svc)
	}
	stored, _ := e.st.Services(ctx)
	var found bool
	for _, v := range stored {
		if v.Name == "nas" {
			found = v.DestOnly && v.Key == "" && v.KeyID == uuid.Nil
		}
	}
	if !found {
		t.Errorf("stored without a key: %+v", stored)
	}
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "pool2", Tag: "to-pool2", DestinationOnly: true, Pool: true}); !errors.Is(err, hub.ErrDestOnly) || !hub.IsUserError(err) {
		t.Errorf("no pool: %v", err)
	}
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "alb", Tag: "to-alb", DestinationOnly: true, Album: "Trips"}); !errors.Is(err, hub.ErrDestOnly) {
		t.Errorf("no album: %v", err)
	}
	// and it can be deleted again with no key to remove
	if err := e.h.DeleteService(ctx, e.ann, ann, "nas"); err != nil {
		t.Errorf("delete: %v", err)
	}
}

func TestTakingBackFromADestinationDeletesTheFilesAndSettlesTheHandOver(t *testing.T) {
	e, ctx, m, link := nasEnv(t, plain)
	sender := uuid.New()
	m.Put(ctx, "bob/IMG_1.jpg", bytes.NewReader([]byte("x")), 1)
	m.Put(ctx, "bob/IMG_1.mov", bytes.NewReader([]byte("y")), 1)
	e.st.AddRemoteHandoverItem(ctx, link.ID, "b1", sender, "nas", "bob/IMG_1.jpg", false)
	e.st.AddRemoteHandoverItem(ctx, link.ID, "b1", uuid.New(), "nas", "bob/IMG_1.mov", true)
	if n, _ := e.h.TakeBackOffer(ctx, bobID, "nas"); n != 1 {
		t.Errorf("a service with only destinations offers to take back what it handed over, counting the photo once and not its clip: %d", n)
	}
	res, err := e.h.TakeBack(ctx, bob, "nas")
	if err != nil {
		t.Fatal(err)
	}
	if res.RemoteTaken != 1 || res.Taken != 1 || len(m.Refs()) != 0 {
		t.Errorf("both files are deleted and one photo counted: %+v %v", res, m.Refs())
	}
	if again, err := e.st.LastHandover(ctx, link.ID, time.Now().Add(-time.Hour)); err != nil || len(again) != 0 {
		t.Errorf("nothing is left to take back: %v %v", again, err)
	}
	if e.st.Delivered(ctx, sender, "nas") {
		t.Error("a photo taken back is not held any more, so it can be delivered again")
	}
}

func TestADestinationThatDoesNotAllowTakeBackKeepsItsCopiesAndSaysSo(t *testing.T) {
	e, ctx, m, link := nasEnv(t, func(m *dest.Memory) syncer.Remote { return refusing{&dest.Remote{Ident: "nas", Name: "the NAS", D: m}} })
	m.Put(ctx, "IMG_1.jpg", bytes.NewReader([]byte("x")), 1)
	e.st.AddRemoteHandoverItem(ctx, link.ID, "b1", uuid.New(), "nas", "IMG_1.jpg", false)
	res, err := e.h.TakeBack(ctx, bob, "nas")
	if err != nil || res.RemoteCannot != 1 || res.RemoteTaken != 0 || len(m.Refs()) != 1 {
		t.Fatalf("%+v %v %v", res, err, m.Refs())
	}
}

func TestADestinationThatCannotBeReachedStopsTheTakeBackBeforeAnythingChanges(t *testing.T) {
	e, ctx, m, link := nasEnv(t, plain)
	m.Put(ctx, "IMG_1.jpg", bytes.NewReader([]byte("x")), 1)
	e.st.AddRemoteHandoverItem(ctx, link.ID, "b1", uuid.New(), "nas", "IMG_1.jpg", false)
	m.Fail = errors.New("unreachable")
	if _, err := e.h.TakeBack(ctx, bob, "nas"); err == nil {
		t.Fatal("it cannot reach the destination")
	}
	if items, _ := e.st.LastHandover(ctx, link.ID, time.Now().Add(-time.Hour)); len(items) != 1 {
		t.Errorf("the hand-over is still there to take back later: %v", items)
	}
}
