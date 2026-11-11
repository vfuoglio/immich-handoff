package syncer_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// fakeRemote stands for a receiver on another server.
type fakeRemote struct {
	mu        sync.Mutex
	notReady  error
	failWith  error
	delivered []string // "sender:filename"
	ready     int
	sawClip   bool
	dup       bool // answer as a server that already had the photo
	n         int
}

func (r *fakeRemote) Label() string { return "aunt lia" }
func (r *fakeRemote) Ready(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ready++
	return r.notReady
}
func (r *fakeRemote) Deliver(_ context.Context, _ *immich.API, a immich.AssetResponseDto, clip *immich.AssetResponseDto, job syncer.RemoteJob) (syncer.RemoteCopy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return syncer.RemoteCopy{}, r.failWith
	}
	r.sawClip = r.sawClip || clip != nil
	r.delivered = append(r.delivered, job.Sender+":"+a.OriginalFileName)
	r.n++
	cp := syncer.RemoteCopy{ID: uuid.MustParse(fmt.Sprintf("f0000000-0000-4000-8000-%012d", r.n)), Created: !r.dup}
	if clip != nil {
		cp.Clip, cp.ClipCreated = uuid.MustParse(fmt.Sprintf("f1000000-0000-4000-8000-%012d", r.n)), !r.dup
	}
	return cp, nil
}

func withRemote(t *testing.T, f *fakeImmich, r *fakeRemote, tune func(*syncer.Service)) syncer.Counts {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	svc := service(
		[]*syncer.Account{{Name: "alice", API: newAPI(t, srv.URL, "src")}},
		[]*syncer.Account{{Name: "family", API: newAPI(t, srv.URL, "dest")}})
	svc.Remote = r
	if tune != nil {
		tune(svc)
	}
	s := &syncer.Syncer{Services: []*syncer.Service{svc}, Log: quiet(), Cfg: syncer.Config{}}
	s.Run(context.Background(), "test", false)
	return s.Last().Counts
}

func TestAPhotoGoesToTheOwnerAndToTheReceiverOnAnotherServer(t *testing.T) {
	f, r := &fakeImmich{uploadStatus: "created"}, &fakeRemote{}
	c := withRemote(t, f, r, nil)
	if c.Imported != 1 || c.Errors != 0 {
		t.Fatalf("counts = %+v", c)
	}
	if len(f.uploaded) != 1 || len(f.tagged) != 1 {
		t.Errorf("the owner's own receiver still gets its copy: %v %v", f.uploaded, f.tagged)
	}
	if len(r.delivered) != 1 || !strings.HasPrefix(r.delivered[0], "alice:") || r.delivered[0] == "alice:" {
		t.Errorf("delivered to the remote: %v", r.delivered)
	}
	if len(f.deleted) != 1 {
		t.Errorf("both hold a copy, so the original goes: %v", f.deleted)
	}
}

func TestTheOriginalIsKeptWhenTheRemoteFails(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created"}
	for name, err := range map[string]error{"unreachable": errors.New("timeout"), "refused": errors.New("403")} {
		r := &fakeRemote{failWith: err}
		c := withRemote(t, f, r, nil)
		if c.Errors != 1 || c.Imported != 0 {
			t.Errorf("%s: counts = %+v", name, c)
		}
		if len(f.deleted) != 0 {
			t.Fatalf("%s: the original was touched although the remote has no copy: %v", name, f.deleted)
		}
	}
}

func TestInCopyModeTheOriginalIsNotMarkedUntilTheRemoteHasIt(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created"}
	c := withRemote(t, f, &fakeRemote{failWith: errors.New("down")}, func(s *syncer.Service) { s.Mode, s.SentTag = syncer.ModeCopy, "handed-over" })
	if c.Errors != 1 || len(f.refusedTagged) != 0 || len(f.untaggedShare) != 0 {
		t.Fatalf("counts=%+v sent=%v untagged=%v: the photo must stay tagged for the next pass", c, f.refusedTagged, f.untaggedShare)
	}
	f2, r := &fakeImmich{uploadStatus: "created"}, &fakeRemote{}
	c = withRemote(t, f2, r, func(s *syncer.Service) { s.Mode, s.SentTag = syncer.ModeCopy, "handed-over" })
	if c.Imported != 1 || len(f2.refusedTagged) != 1 || len(r.delivered) != 1 {
		t.Fatalf("counts=%+v sent=%v delivered=%v", c, f2.refusedTagged, r.delivered)
	}
}

func TestAPhotoWaitsWhenTheRemoteIsNotReady(t *testing.T) {
	f, r := &fakeImmich{uploadStatus: "created"}, &fakeRemote{notReady: errors.New("revoked")}
	c := withRemote(t, f, r, nil)
	if c != (syncer.Counts{}) {
		t.Errorf("counts = %+v", c)
	}
	if len(f.uploaded) != 0 || len(f.deleted) != 0 || len(f.tagged) != 0 || len(r.delivered) != 0 {
		t.Fatalf("nothing may be sent, tagged or deleted, not even for the owner: up=%v tagged=%v del=%v remote=%v", f.uploaded, f.tagged, f.deleted, r.delivered)
	}
	// when it is ready again the waiting photo goes
	r.notReady = nil
	c = withRemote(t, f, r, nil)
	if c.Imported != 1 || len(r.delivered) != 1 {
		t.Fatalf("after recovering: %+v %v", c, r.delivered)
	}
}

func TestARetryAfterARemoteFailureIsSafe(t *testing.T) {
	// the owner's copy already exists (a duplicate) and the remote now works: the photo is delivered once, the
	// original goes
	f, r := &fakeImmich{uploadStatus: "duplicate"}, &fakeRemote{}
	c := withRemote(t, f, r, nil)
	if c.Errors != 0 || len(r.delivered) != 1 || len(f.deleted) != 1 {
		t.Fatalf("counts=%+v delivered=%v deleted=%v", c, r.delivered, f.deleted)
	}
}

func TestADryRunDeliversNothingToTheRemote(t *testing.T) {
	f, r := &fakeImmich{uploadStatus: "created"}, &fakeRemote{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	svc := service([]*syncer.Account{{Name: "alice", API: newAPI(t, srv.URL, "src")}}, []*syncer.Account{{Name: "family", API: newAPI(t, srv.URL, "dest")}})
	svc.Remote = r
	s := &syncer.Syncer{Services: []*syncer.Service{svc}, Log: quiet(), Cfg: syncer.Config{DryRun: true}}
	s.Run(context.Background(), "test", false)
	if len(r.delivered) != 0 || len(f.deleted) != 0 {
		t.Fatalf("dry run: %v %v", r.delivered, f.deleted)
	}
}

func TestWhatTheOtherServerHoldsIsRememberedForTakingBack(t *testing.T) {
	for _, dup := range []bool{false, true} {
		f, r := &fakeImmich{uploadStatus: "created"}, &fakeRemote{dup: dup}
		srv := httptest.NewServer(f.handler())
		p := &ledgerProvider{svcs: []*syncer.Service{service(
			[]*syncer.Account{{Name: "alice", API: newAPI(t, srv.URL, "src")}},
			[]*syncer.Account{{Name: "family", API: newAPI(t, srv.URL, "dest")}})}}
		p.svcs[0].Remote = r
		s := &syncer.Syncer{Provider: p, Log: quiet()}
		s.Run(context.Background(), "test", false)
		srv.Close()
		var local, remote int
		for _, row := range p.rows {
			if row.remote {
				remote++
				if row.receiver.String()[:1] != "f" {
					t.Errorf("a remote row must hold the other server's id: %+v", row)
				}
			} else {
				local++
			}
		}
		if local != 1 {
			t.Errorf("dup=%v: the owner's copy is remembered as before: %+v", dup, p.rows)
		}
		if want := map[bool]int{false: 1, true: 0}[dup]; remote != want {
			t.Errorf("dup=%v: %d remote rows, want %d (a duplicate is not ours to take back): %+v", dup, remote, want, p.rows)
		}
	}
}
