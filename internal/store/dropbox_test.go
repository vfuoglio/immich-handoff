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

func TestDropBoxHostsPendingAndSummary(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t, t.TempDir(), "drop-secret-value-1234")
	b, _ := s.AddDropBox(ctx, DropBox{Token: "t4", Name: "Gala", Key: "k", Album: "Gala photos", ExtraTag: "gala", Approve: true, StartsAt: time.Now().Add(time.Hour)})
	got, _ := s.DropBox(ctx, b.ID)
	if got.Album != "Gala photos" || got.ExtraTag != "gala" || !got.Approve || !got.NotStarted(time.Now()) || got.Open(time.Now()) {
		t.Errorf("round trip: %+v", got)
	}

	// co-hosts: added, changed, listed, and found from the person's side
	ann := uuid.New()
	s.SetDropHost(ctx, b.ID, DropHost{UserID: ann, Name: "Ann", Email: "ann@x", Role: HostView})
	s.SetDropHost(ctx, b.ID, DropHost{UserID: ann, Name: "Ann", Email: "ann@x", Role: HostManage})
	if hs, _ := s.DropHosts(ctx, b.ID); len(hs) != 1 || hs[0].Role != HostManage {
		t.Errorf("hosts: %+v", hs)
	}
	if m, _ := s.HostedBoxes(ctx, ann); m[b.ID] != HostManage {
		t.Errorf("hosted: %v", m)
	}
	s.RemoveDropHost(ctx, b.ID, ann)
	if m, _ := s.HostedBoxes(ctx, ann); len(m) != 0 {
		t.Errorf("removed: %v", m)
	}

	// files that wait: listed oldest first, counted, claimed once, found by age
	p1, _ := s.AddDropPending(ctx, DropPending{BoxID: b.ID, Name: "a.jpg", Size: 100, Kind: "image", Who: "w", Path: "/x/a"})
	s.AddDropPending(ctx, DropPending{BoxID: b.ID, Name: "b.jpg", Size: 50, Kind: "image", Who: "w", Path: "/x/b"})
	if n, size, _ := s.DropPendingTotals(ctx, b.ID); n != 2 || size != 150 {
		t.Errorf("totals: %d %d", n, size)
	}
	if l, _ := s.DropPendingOf(ctx, b.ID, 1); len(l) != 1 || l[0].Name != "a.jpg" {
		t.Errorf("list: %+v", l)
	}
	if ok, _ := s.RemoveDropPending(ctx, p1.ID); !ok {
		t.Error("first claim")
	}
	if ok, _ := s.RemoveDropPending(ctx, p1.ID); ok {
		t.Error("a file can be claimed once")
	}
	if _, err := s.DropPendingByID(ctx, p1.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("claimed file: %v", err)
	}
	if old, _ := s.DropPendingBefore(ctx, time.Now().Add(time.Hour)); len(old) != 1 {
		t.Errorf("by age: %+v", old)
	}
	if old, _ := s.DropPendingBefore(ctx, time.Now().Add(-time.Hour)); len(old) != 0 {
		t.Errorf("nothing is that old: %+v", old)
	}

	// the summary: guests counted once, busiest hour
	now := time.Now()
	for _, w := range []string{"x", "x", "y"} {
		s.RecordDropArrival(ctx, b.ID, w, now)
	}
	s.RecordDropArrival(ctx, b.ID, "z", now.Add(-5*time.Hour))
	sum, _ := s.DropSummaryOf(ctx, b.ID)
	if sum.Guests != 3 || sum.BusiestFiles != 3 || sum.BusiestHour != now.UTC().Format("2006-01-02T15") {
		t.Errorf("summary: %+v", sum)
	}

	// schedule and the two messages
	end := time.Now().Add(48 * time.Hour)
	s.MarkDropNotified(ctx, b.ID, 1)
	s.MarkDropNotified(ctx, b.ID, 2)
	if g, _ := s.DropBox(ctx, b.ID); g.Notified != 3 {
		t.Errorf("notified: %d", g.Notified)
	}
	s.SetDropSchedule(ctx, b.ID, time.Time{}, end)
	if g, _ := s.DropBox(ctx, b.ID); g.Notified != 0 || g.StartsAt != (time.Time{}) || g.ExpiresAt.Sub(end) > time.Second || end.Sub(g.ExpiresAt) > time.Second {
		t.Errorf("schedule: %+v", g)
	}
}
