package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDropBoxLifecycle(t *testing.T) {
	ctx := context.Background()
	s, err := open(t, t.TempDir(), "drop-secret-value-1234")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AddDropBox(ctx, DropBox{Token: "tok1", Name: "Wedding", OwnerID: uuid.New(), OwnerName: "Ann", KeyID: uuid.New(), Key: "SECRET", InboxTag: "inbox/Wedding", MaxFiles: 2})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.DropBoxByToken(ctx, "tok1")
	if err != nil || got.Key != "SECRET" || got.Name != "Wedding" {
		t.Fatalf("by token: %+v %v", got, err)
	}
	if _, err := s.DropBoxByToken(ctx, "nope"); !errors.Is(err, ErrDropBoxGone) {
		t.Errorf("unknown token: %v", err)
	}

	// two slots, then full; a slot given back can be taken again
	for i := 0; i < 2; i++ {
		if _, err := s.TakeDropSlot(ctx, b.ID); err != nil {
			t.Fatalf("slot %d: %v", i, err)
		}
	}
	if _, err := s.TakeDropSlot(ctx, b.ID); !errors.Is(err, ErrDropFull) {
		t.Errorf("third slot: %v", err)
	}
	s.ReleaseDropSlot(ctx, b.ID)
	if _, err := s.TakeDropSlot(ctx, b.ID); err != nil {
		t.Errorf("after release: %v", err)
	}
	s.AddDropBytes(ctx, b.ID, 1234)
	if got, _ = s.DropBox(ctx, b.ID); got.Files != 2 || got.Bytes != 1234 {
		t.Errorf("counters: %+v", got)
	}

	// paused boxes take nothing; resumed ones do again
	if ok, _ := s.SetDropBoxState(ctx, b.ID, DropPaused, "by hand"); !ok {
		t.Fatal("pause")
	}
	if _, err := s.TakeDropSlot(ctx, b.ID); !errors.Is(err, ErrDropBoxGone) {
		t.Errorf("paused: %v", err)
	}

	// revoking loses the key and the link, keeps the log
	s.AddDropEvent(ctx, b.ID, DropUploaded, "a photo", "ab12")
	if ok, _ := s.SetDropBoxState(ctx, b.ID, DropRevoked, ""); !ok {
		t.Fatal("revoke")
	}
	if _, err := s.DropBoxByToken(ctx, "tok1"); !errors.Is(err, ErrDropBoxGone) {
		t.Errorf("revoked link: %v", err)
	}
	if ok, _ := s.SetDropBoxState(ctx, b.ID, DropActive, ""); ok {
		t.Error("a revoked box cannot be resumed")
	}
	if ev, _ := s.DropEvents(ctx, b.ID, 10); len(ev) != 1 || ev[0].Who != "ab12" {
		t.Errorf("log: %+v", ev)
	}
	if err := s.PruneDropBoxes(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DropBox(ctx, b.ID); !errors.Is(err, ErrDropBoxGone) {
		t.Errorf("pruned: %v", err)
	}
}

func TestDropBoxExpiryAndSizeLimit(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t, t.TempDir(), "drop-secret-value-1234")
	b, _ := s.AddDropBox(ctx, DropBox{Token: "t2", Name: "Party", Key: "k", MaxMB: 1, ExpiresAt: time.Now().Add(time.Hour)})
	if _, err := s.TakeDropSlot(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	s.AddDropBytes(ctx, b.ID, 2<<20)
	if _, err := s.TakeDropSlot(ctx, b.ID); !errors.Is(err, ErrDropFull) {
		t.Errorf("size limit: %v", err)
	}
	e, _ := s.AddDropBox(ctx, DropBox{Token: "t3", Name: "Old", Key: "k", ExpiresAt: time.Now().Add(-time.Minute)})
	if _, err := s.TakeDropSlot(ctx, e.ID); !errors.Is(err, ErrDropBoxGone) {
		t.Errorf("expired: %v", err)
	}
}
