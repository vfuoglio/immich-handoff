package syncer_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// poolOf is a pool in which "alice" (key src) is a member and so both a sender and a receiver, and "family" (key dest) is
// another member that receives.
func poolOf(t *testing.T, f *fakeImmich) (*syncer.Service, *syncer.Syncer) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	alice := &syncer.Account{Name: "alice", API: newAPI(t, srv.URL, "src")}
	family := &syncer.Account{Name: "family", API: newAPI(t, srv.URL, "dest")}
	svc := service([]*syncer.Account{alice}, []*syncer.Account{alice, family})
	svc.Mode, svc.SentTag, svc.Pool = syncer.ModeCopy, "handed-over", true
	return svc, &syncer.Syncer{Services: []*syncer.Service{svc}, Log: quiet()}
}

func TestInAPoolAPhotoGoesToEveryoneButItsSender(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created"}
	svc, s := poolOf(t, f)
	s.Run(context.Background(), "test", false)
	c := s.Last().Counts
	if c.Imported != 1 || c.Errors != 0 {
		t.Fatalf("counts = %+v", c)
	}
	if len(f.uploaded) != 1 {
		t.Errorf("the photo reaches the other member once, and not its own sender: %v", f.uploaded)
	}
	if len(f.deleted) != 0 {
		t.Errorf("nobody's original is ever removed in a pool: %v", f.deleted)
	}
	if len(f.tagged) != 1 || len(f.fromTagged) != 1 || f.fromTagged[0] != "from/alice" {
		t.Errorf("the copy carries the inbox tag and says it is from alice: inbox %v, from %v", f.tagged, f.fromTagged)
	}
	_ = svc
}

func TestTheSameAccountAsSenderAndReceiverIsStillRefusedOutsideAPool(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created"}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	alice := &syncer.Account{Name: "alice", API: newAPI(t, srv.URL, "src")}
	svc := service([]*syncer.Account{alice}, []*syncer.Account{alice, {Name: "family", API: newAPI(t, srv.URL, "dest")}})
	s := &syncer.Syncer{Services: []*syncer.Service{svc}, Log: quiet()}
	s.Run(context.Background(), "test", false)
	if c := s.Last().Counts; c.Errors != 1 || c.Imported != 0 || len(f.uploaded) != 0 {
		t.Errorf("a service that is not a pool must refuse: %+v, uploaded %v", c, f.uploaded)
	}
}

func TestAPoolWithNobodyElseInItLeavesTaggedPhotosAlone(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created"}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	alice := &syncer.Account{Name: "alice", API: newAPI(t, srv.URL, "src")}
	svc := service([]*syncer.Account{alice}, []*syncer.Account{alice})
	svc.Mode, svc.SentTag, svc.Pool = syncer.ModeCopy, "handed-over", true
	s := &syncer.Syncer{Services: []*syncer.Service{svc}, Log: quiet()}
	s.Run(context.Background(), "test", false)
	if c := s.Last().Counts; c.Errors != 0 || c.Imported != 0 || len(f.uploaded) != 0 || len(f.refusedTagged) != 0 || len(f.untaggedShare) != 0 {
		t.Errorf("alone in a pool nothing happens and nothing is changed: %+v %v %v %v", c, f.uploaded, f.refusedTagged, f.untaggedShare)
	}
}

func TestAPoolThatHasEndedHandsNothingOver(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created"}
	svc, s := poolOf(t, f)
	svc.EndsAt = time.Now().Add(-time.Minute)
	if !svc.Ended(time.Now()) {
		t.Fatal("it ended a minute ago")
	}
	s.Run(context.Background(), "test", false)
	if c := s.Last().Counts; c.Imported != 0 || c.Errors != 0 || len(f.uploaded) != 0 || len(f.untaggedShare) != 0 {
		t.Errorf("%+v %v %v", c, f.uploaded, f.untaggedShare)
	}
	svc2, s2 := poolOf(t, &fakeImmich{uploadStatus: "created"})
	svc2.EndsAt = time.Now().Add(time.Hour) // not yet
	s2.Run(context.Background(), "test", false)
	if s2.Last().Counts.Imported != 1 {
		t.Errorf("a pool that has not ended works: %+v", s2.Last().Counts)
	}
}

func TestEndedIsOnlyForPoolsWithAnEnd(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		svc  syncer.Service
		want bool
	}{
		{syncer.Service{}, false},
		{syncer.Service{Pool: true}, false},
		{syncer.Service{Pool: true, EndsAt: now.Add(time.Hour)}, false},
		{syncer.Service{Pool: true, EndsAt: now.Add(-time.Hour)}, true},
		{syncer.Service{EndsAt: now.Add(-time.Hour)}, false}, // not a pool: an end date means nothing
	} {
		if got := tc.svc.Ended(now); got != tc.want {
			t.Errorf("%+v: %v, want %v", tc.svc, got, tc.want)
		}
	}
}

func TestOutsideAPoolACopyIsNotTaggedWithWhoItIsFrom(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created"}
	if c := run(t, f); c.Imported != 1 {
		t.Fatalf("%+v", c)
	}
	if len(f.fromTagged) != 0 {
		t.Errorf("only a pool tells who a copy is from: %v", f.fromTagged)
	}
}
