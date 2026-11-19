package syncer_test

import (
	"context"
	"errors"
	"io/fs"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/dest"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// A destination stands for a place that is not an Immich account: a folder, a server. Unlike a bridge it does not hold up the service
// while it is down.
type destRemote struct {
	mu       sync.Mutex
	id       string
	notReady error
	failWith error
	got      []string
	n        int
}

func (d *destRemote) ID() string    { return d.id }
func (d *destRemote) Label() string { return "destination " + d.id }
func (d *destRemote) Ready(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.notReady
}
func (d *destRemote) Deliver(_ context.Context, _ *immich.API, a immich.AssetResponseDto, _ *immich.AssetResponseDto, job syncer.RemoteJob) (syncer.RemoteCopy, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failWith != nil {
		return syncer.RemoteCopy{}, d.failWith
	}
	d.n++
	d.got = append(d.got, a.OriginalFileName)
	return syncer.RemoteCopy{Ref: "/" + d.id + "/" + a.OriginalFileName, Created: true}, nil
}
func (d *destRemote) TakeBackRefs(context.Context, []string) error { return nil }

// destLedger remembers what each destination holds, as the hub does.
type destLedger struct {
	svcs []*syncer.Service
	mu   sync.Mutex
	rows map[string][]string // dest -> refs
	have map[string]bool     // dest + sender asset
}

func (p *destLedger) Services() []*syncer.Service { return p.svcs }
func (p *destLedger) RememberRemote(_ context.Context, _ *syncer.Account, _ string, sender uuid.UUID, dest, ref string, clip bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rows == nil {
		p.rows, p.have = map[string][]string{}, map[string]bool{}
	}
	p.rows[dest] = append(p.rows[dest], ref)
	if !clip {
		p.have[dest+sender.String()] = true
	}
}
func (p *destLedger) Delivered(_ context.Context, sender uuid.UUID, dest string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.have[dest+sender.String()]
}

func destEnv(t *testing.T, receivers bool, remotes ...syncer.Remote) (*fakeImmich, *destLedger, *syncer.Syncer) {
	t.Helper()
	f := &fakeImmich{uploadStatus: "created"}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	var rcv []*syncer.Account
	if receivers {
		rcv = []*syncer.Account{{Name: "family", API: newAPI(t, srv.URL, "dest")}}
	}
	svc := service([]*syncer.Account{{Name: "alice", API: newAPI(t, srv.URL, "src")}}, rcv)
	svc.Remotes = remotes
	p := &destLedger{svcs: []*syncer.Service{svc}}
	return f, p, &syncer.Syncer{Provider: p, Log: quiet()}
}

func TestADestinationThatIsDownDoesNotHoldUpTheOthersButHoldsTheOriginalUntilItIsBack(t *testing.T) {
	down, ok := &destRemote{id: "nas", notReady: errors.New("unreachable")}, &destRemote{id: "cloud"}
	f, p, s := destEnv(t, true, down, ok)
	s.Run(context.Background(), "test", false)
	if len(f.uploaded) != 1 || len(ok.got) != 1 || len(down.got) != 0 {
		t.Fatalf("the others take the photo now: owner=%v cloud=%v nas=%v", f.uploaded, ok.got, down.got)
	}
	if len(f.deleted) != 0 {
		t.Fatalf("a move waits for every receiver: the original was deleted: %v", f.deleted)
	}
	if len(p.rows["cloud"]) != 1 || len(p.rows["nas"]) != 0 {
		t.Fatalf("each destination's copy is recorded under its own name: %+v", p.rows)
	}
	// it is back: only it receives the photo, and the original goes
	down.notReady = nil
	s.Run(context.Background(), "test", false)
	if len(down.got) != 1 || len(ok.got) != 1 {
		t.Fatalf("a retry delivers only to what lacks the photo: nas=%v cloud=%v", down.got, ok.got)
	}
	if len(f.deleted) != 1 {
		t.Fatalf("now that all hold it the original goes: %v", f.deleted)
	}
}

func TestADestinationThatFailsHoldsTheOriginalAndTheOtherStillGetsTheCopy(t *testing.T) {
	bad, ok := &destRemote{id: "nas", failWith: errors.New("disk full")}, &destRemote{id: "cloud"}
	f, p, s := destEnv(t, false, bad, ok)
	s.Run(context.Background(), "test", false)
	if len(ok.got) != 1 || len(p.rows["cloud"]) != 1 {
		t.Fatalf("the other destination holds it and it is recorded: %v %+v", ok.got, p.rows)
	}
	if len(f.deleted) != 0 || s.Last().Counts.Errors != 1 {
		t.Fatalf("the failure keeps the original and is counted: deleted=%v counts=%+v", f.deleted, s.Last().Counts)
	}
}

func TestAServiceWithOnlyADestinationHandsOverAndMovesTheOriginalAndCounts(t *testing.T) {
	d := &destRemote{id: "nas"}
	f, p, s := destEnv(t, false, d)
	s.Run(context.Background(), "test", false)
	c := s.Last().Counts
	if len(d.got) != 1 || len(f.deleted) != 1 || c.Imported != 1 || c.Errors != 0 {
		t.Fatalf("delivered=%v deleted=%v counts=%+v", d.got, f.deleted, c)
	}
	if len(p.rows["nas"]) != 1 {
		t.Errorf("what it holds is recorded: %+v", p.rows)
	}
	// with the only destination down nothing is deleted and nothing is an error
	f2, _, s2 := destEnv(t, false, &destRemote{id: "nas", notReady: errors.New("unreachable")})
	s2.Run(context.Background(), "test", false)
	if len(f2.deleted) != 0 || s2.Last().Counts.Errors != 0 {
		t.Errorf("waits quietly: deleted=%v counts=%+v", f2.deleted, s2.Last().Counts)
	}
}

func TestInCopyModeTheTagStaysUntilEveryDestinationHoldsThePhoto(t *testing.T) {
	down := &destRemote{id: "nas", notReady: errors.New("unreachable")}
	f, _, s := destEnv(t, true, down)
	s.Provider.(*destLedger).svcs[0].Mode = syncer.ModeCopy
	s.Provider.(*destLedger).svcs[0].SentTag = "handed-over"
	s.Run(context.Background(), "test", false)
	if len(f.refusedTagged) != 0 || len(f.untaggedShare) != 0 {
		t.Fatalf("the service tag stays so the destination can catch up: sent=%v untagged=%v", f.refusedTagged, f.untaggedShare)
	}
	down.notReady = nil
	s.Run(context.Background(), "test", false)
	if len(down.got) != 1 || len(f.refusedTagged) != 1 {
		t.Fatalf("then it is delivered and marked: got=%v sent=%v", down.got, f.refusedTagged)
	}
}

func TestAFolderDestinationGetsTheOriginalWithItsRecordAndTheOriginalMoves(t *testing.T) {
	root := t.TempDir()
	d, err := dest.NewFolder(root)
	if err != nil {
		t.Fatal(err)
	}
	r := &dest.Remote{Ident: "nas", Name: "the NAS", Service: "backup", D: d}
	f, _, s := destEnv(t, false, r)
	s.Run(context.Background(), "test", false)
	var files []string
	filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			rel, _ := filepath.Rel(root, p)
			files = append(files, rel)
		}
		return nil
	})
	if len(files) != 2 || len(f.deleted) != 1 || s.Last().Counts.Imported != 1 {
		t.Fatalf("the photo and its record are in the folder and the original moved: %v deleted=%v counts=%+v", files, f.deleted, s.Last().Counts)
	}
	if !strings.HasSuffix(files[0]+files[1], ".handoff.json") && !strings.HasSuffix(files[1], ".handoff.json") {
		t.Errorf("%v", files)
	}
}
