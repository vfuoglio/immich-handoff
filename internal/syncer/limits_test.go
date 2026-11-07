package syncer_test

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// memUsage keeps what senders handed over, like the hub does in the data file.
type memUsage struct {
	mu        sync.Mutex
	at        []time.Time
	bytes     []int64
	untracked bool // a sender from the configuration
}

func (m *memUsage) Handed(_ context.Context, _ *syncer.Account, since time.Time) (syncer.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := syncer.Snapshot{Tracked: !m.untracked}
	for i, t := range m.at {
		if t.After(since) {
			snap.Photos++
			snap.Bytes += m.bytes[i]
			if snap.Oldest.IsZero() || t.Before(snap.Oldest) {
				snap.Oldest = t
			}
		}
	}
	return snap, nil
}

func (m *memUsage) Record(_ context.Context, _ *syncer.Account, b int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.at, m.bytes = append(m.at, time.Now()), append(m.bytes, b)
	return nil
}

func (m *memUsage) count() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.at) }

type limitRig struct {
	f     *fakeImmich
	s     *syncer.Syncer
	svc   *syncer.Service
	alice *syncer.Account
	use   *memUsage
}

func newLimitRig(t *testing.T) *limitRig {
	return newLimitRigWith(t, &fakeImmich{uploadStatus: "created"})
}

func newLimitRigWith(t *testing.T, f *fakeImmich) *limitRig {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	alice := &syncer.Account{Name: "alice", API: newAPI(t, srv.URL, "src")}
	svc := service([]*syncer.Account{alice}, []*syncer.Account{{Name: "family", API: newAPI(t, srv.URL, "dest")}})
	use := &memUsage{}
	s := &syncer.Syncer{Services: []*syncer.Service{svc}, Usage: use, Log: quiet()}
	return &limitRig{f, s, svc, alice, use}
}

func (r *limitRig) uploads() int { r.f.mu.Lock(); defer r.f.mu.Unlock(); return len(r.f.uploaded) }

func TestAPausedServiceLeavesTaggedPhotosAloneUntilItResumes(t *testing.T) {
	r := newLimitRig(t)
	r.svc.Paused = true
	r.s.Run(context.Background(), "test", false)
	if r.uploads() != 0 || len(r.f.deleted) != 0 || len(r.f.untaggedShare) != 0 {
		t.Fatalf("a paused service must touch nothing: uploads %d deleted %v untagged %v", r.uploads(), r.f.deleted, r.f.untaggedShare)
	}
	r.svc.Paused = false
	r.s.Run(context.Background(), "test", false)
	if r.uploads() != 1 {
		t.Errorf("after resuming the waiting photo must go: uploads %d", r.uploads())
	}
}

func TestAPausedSenderIsLeftAloneButOthersAreNot(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created"}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	paused := &syncer.Account{Name: "alice", API: newAPI(t, srv.URL, "src"), Paused: true}
	svc := service([]*syncer.Account{paused}, []*syncer.Account{{Name: "family", API: newAPI(t, srv.URL, "dest")}})
	s := &syncer.Syncer{Services: []*syncer.Service{svc}, Log: quiet()}
	s.Run(context.Background(), "test", false)
	if len(f.uploaded) != 0 || len(f.deleted) != 0 {
		t.Fatalf("a paused sender's photos must stay: uploaded %v deleted %v", f.uploaded, f.deleted)
	}
	paused2 := &syncer.Account{Name: "alice", API: newAPI(t, srv.URL, "src")} // the same person, resumed
	svc.Senders = []*syncer.Account{paused2}
	s.Run(context.Background(), "test", false)
	if len(f.uploaded) != 1 {
		t.Errorf("a sender who resumed hands over: %v", f.uploaded)
	}
}

func TestTheDailyPhotoLimitHoldsPhotosAndSaysUntilWhen(t *testing.T) {
	r := newLimitRig(t)
	r.svc.LimitPhotos = 1
	earlier := time.Now().Add(-2 * time.Hour)
	r.use.at, r.use.bytes = []time.Time{earlier}, []int64{1000} // one photo handed over two hours ago: the limit is reached
	r.s.Run(context.Background(), "test", false)
	if r.uploads() != 0 || len(r.f.deleted) != 0 {
		t.Fatalf("over the limit nothing may be handed over: uploads %d deleted %v", r.uploads(), r.f.deleted)
	}
	h, ok := r.s.HeldFor(r.alice)
	if !ok || h.Waiting != 1 || h.Photos != 1 {
		t.Fatalf("the photo must be reported as waiting: %+v %v", h, ok)
	}
	if want := earlier.Add(syncer.LimitWindow); h.Until.Sub(want) > time.Second || want.Sub(h.Until) > time.Second {
		t.Errorf("until = %v, want about %v (when the oldest hand-over leaves the window)", h.Until, want)
	}
}

func TestUnderTheLimitPhotosGoAndAreCounted(t *testing.T) {
	r := newLimitRig(t)
	r.svc.LimitPhotos = 3
	r.s.Run(context.Background(), "test", false)
	if r.uploads() != 1 || r.use.count() != 1 {
		t.Fatalf("handed over %d, recorded %d", r.uploads(), r.use.count())
	}
	if r.use.bytes[0] != 2_000_000 {
		t.Errorf("the size recorded is the original's: %d", r.use.bytes[0])
	}
	if _, held := r.s.HeldFor(r.alice); held {
		t.Error("under the limit nothing waits")
	}
}

func TestTheDailySizeLimit(t *testing.T) {
	r := newLimitRig(t)
	r.svc.LimitMB = 1
	r.use.at, r.use.bytes = []time.Time{time.Now().Add(-time.Hour)}, []int64{1 << 20}
	r.s.Run(context.Background(), "test", false)
	if r.uploads() != 0 {
		t.Fatalf("at the size limit nothing may go: %d", r.uploads())
	}
	if h, ok := r.s.HeldFor(r.alice); !ok || h.Bytes != 1<<20 {
		t.Errorf("held: %+v %v", h, ok)
	}
	// a sender under the size limit hands over (the last photo may take them past it)
	r2 := newLimitRig(t)
	r2.svc.LimitMB = 1
	r2.s.Run(context.Background(), "test", false)
	if r2.uploads() != 1 {
		t.Errorf("a sender under the size limit hands over: %d", r2.uploads())
	}
}

func TestSendersWithoutARecordAreNeverCapped(t *testing.T) {
	r := newLimitRig(t)
	r.svc.LimitPhotos = 1
	r.use.untracked = true // from the configuration: nothing is recorded, so there is nothing to cap
	r.use.at, r.use.bytes = []time.Time{time.Now()}, []int64{1}
	r.s.Run(context.Background(), "test", false)
	if r.uploads() != 1 {
		t.Errorf("an untracked sender must not be capped: %d", r.uploads())
	}
}

func TestWaitingPhotosGoWhenTheWindowMovesOn(t *testing.T) {
	old := syncer.LimitWindow
	syncer.LimitWindow = 300 * time.Millisecond
	t.Cleanup(func() { syncer.LimitWindow = old })
	r := newLimitRig(t)
	r.svc.LimitPhotos = 1
	r.use.at, r.use.bytes = []time.Time{time.Now()}, []int64{1}
	r.s.Run(context.Background(), "test", false)
	if r.uploads() != 0 {
		t.Fatalf("held first: %d", r.uploads())
	}
	deadline := time.Now().Add(5 * time.Second)
	for r.uploads() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if r.uploads() != 1 {
		t.Fatal("when the window moves on, a pass must run by itself and hand the photo over")
	}
	if _, held := r.s.HeldFor(r.alice); held {
		t.Error("the wait must be cleared once it goes")
	}
}

func TestCapped(t *testing.T) {
	svc := &syncer.Service{LimitPhotos: 2, LimitMB: 1}
	for _, c := range []struct {
		snap syncer.Snapshot
		want bool
	}{
		{syncer.Snapshot{Tracked: true, Photos: 1, Bytes: 100}, false},
		{syncer.Snapshot{Tracked: true, Photos: 2}, true},
		{syncer.Snapshot{Tracked: true, Photos: 1, Bytes: 1 << 20}, true},
		{syncer.Snapshot{Photos: 99, Bytes: 1 << 40}, false}, // untracked
	} {
		if got := syncer.Capped(svc, c.snap); got != c.want {
			t.Errorf("Capped(%+v) = %v, want %v", c.snap, got, c.want)
		}
	}
	if syncer.Capped(&syncer.Service{}, syncer.Snapshot{Tracked: true, Photos: 1000}) {
		t.Error("no limit set must never cap")
	}
}

func TestHandOversAreCountedEvenWithoutALimit(t *testing.T) {
	r := newLimitRig(t) // no limit set
	r.s.Run(context.Background(), "test", false)
	if r.uploads() != 1 || r.use.count() != 1 {
		t.Errorf("a limit set later must count what already went today: handed over %d, recorded %d", r.uploads(), r.use.count())
	}
}
