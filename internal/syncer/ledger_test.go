package syncer_test

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// ledgerProvider is a provider that keeps a ledger, as the hub does for people who joined from the page.
type ledgerProvider struct {
	svcs []*syncer.Service
	mu   sync.Mutex
	rows []ledgerRow
}

type ledgerRow struct {
	batch            string
	sender, receiver uuid.UUID
	clip             bool
}

func (p *ledgerProvider) Services() []*syncer.Service { return p.svcs }
func (p *ledgerProvider) Remember(_ context.Context, _ *syncer.Account, batch string, sender, receiver uuid.UUID, clip bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rows = append(p.rows, ledgerRow{batch, sender, receiver, clip})
}

func (f *twoAccounts) handLedger(t *testing.T) *ledgerProvider {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	p := &ledgerProvider{svcs: []*syncer.Service{service(
		[]*syncer.Account{{Name: "alice", API: newAPI(t, srv.URL, "src")}},
		[]*syncer.Account{{Name: "family", API: newAPI(t, srv.URL, "dest")}})}}
	s := &syncer.Syncer{Provider: p, Log: quiet()}
	s.Run(context.Background(), "test", false)
	return p
}

func TestWhatWasHandedOverIsRememberedPhotoByCopyInOneBatch(t *testing.T) {
	f := aStack(stackCover) // a stack of three: the whole stack goes
	p := f.handLedger(t)
	if len(p.rows) != 3 {
		t.Fatalf("one row for each photo of the stack: %+v", p.rows)
	}
	for _, r := range p.rows {
		if r.batch != p.rows[0].batch {
			t.Errorf("one hand-over, one batch: %+v", p.rows)
		}
		a := f.assets["dest"][r.receiver.String()]
		if a == nil || a.name == "" {
			t.Errorf("the receiver's id must be that of the copy: %v", r.receiver)
		}
	}
}

func TestALivePhotoRemembersItsMotionClipToo(t *testing.T) {
	f := livePhoto()
	p := f.handLedger(t)
	if len(p.rows) != 2 {
		t.Fatalf("the still and its clip: %+v", p.rows)
	}
	var clips int
	for _, r := range p.rows {
		if r.sender.String() == liveClip {
			clips++
			if !r.clip {
				t.Error("the clip's row is marked as a clip")
			}
			if !f.assets["dest"][r.receiver.String()].hidden {
				t.Error("the copy of the clip is the hidden one")
			}
		}
	}
	if clips != 1 {
		t.Errorf("the clip's pair must be there once: %+v", p.rows)
	}
}

func TestACopyThatWasAlreadyThereIsNotRememberedForTakingBack(t *testing.T) {
	f := newTwoAccounts()
	f.addSender(&fakeAsset{id: stackCover, name: "p.jpg", data: []byte("same bytes"), tags: map[string]bool{"share": true}})
	f.addDest(&fakeAsset{id: "d0000000-0000-4000-8000-000000000999", name: "p.jpg", data: []byte("same bytes"), tags: map[string]bool{}})
	p := f.handLedger(t)
	if len(p.rows) != 0 {
		t.Errorf("a photo the receiver already had was not handed over now, so it must not be taken back: %+v", p.rows)
	}
}
