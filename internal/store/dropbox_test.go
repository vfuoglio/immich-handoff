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

func TestDropBoxShortLookCoverTemplatesAndUploads(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t, t.TempDir(), "drop-secret-value-1234")
	a, _ := s.AddDropBox(ctx, DropBox{Token: "long-token-one-1234", Name: "A", Key: "k", Short: "shortcode1", Accent: "#112233", Lang: "fr", MaxDim: 2000, GuestDelete: true})
	b, _ := s.AddDropBox(ctx, DropBox{Token: "long-token-two-1234", Name: "B", Key: "k"})
	// a short code opens the box as the long token does, is unique, and can be taken away or the long token changed
	if got, err := s.DropBoxByToken(ctx, "shortcode1"); err != nil || got.ID != a.ID || got.Accent != "#112233" || got.Lang != "fr" || got.MaxDim != 2000 || !got.GuestDelete {
		t.Errorf("by short: %+v %v", got, err)
	}
	if err := s.SetDropShort(ctx, b.ID, "shortcode1"); !errors.Is(err, ErrExists) {
		t.Errorf("a code in use: %v", err)
	}
	s.SetDropShort(ctx, a.ID, "")
	if _, err := s.DropBoxByToken(ctx, "shortcode1"); err == nil {
		t.Error("a removed code must not open the box")
	}
	s.SetDropShort(ctx, b.ID, "shortcode1")
	s.SetDropToken(ctx, b.ID, "brand-new-token-1234")
	if _, err := s.DropBoxByToken(ctx, "long-token-two-1234"); err == nil {
		t.Error("the old long token must stop")
	}
	if got, err := s.DropBoxByToken(ctx, "shortcode1"); err != nil || got.Token != "brand-new-token-1234" {
		t.Errorf("the short code still opens it: %+v %v", got, err)
	}
	// cover
	if _, _, err := s.DropCover(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("no cover: %v", err)
	}
	s.SetDropCover(ctx, a.ID, "image/png", []byte{1, 2, 3})
	if k, d, err := s.DropCover(ctx, a.ID); err != nil || k != "image/png" || len(d) != 3 {
		t.Errorf("cover: %s %v %v", k, d, err)
	}
	s.SetDropCover(ctx, a.ID, "", nil)
	if s.HasDropCover(ctx, a.ID) {
		t.Error("removed cover")
	}
	// templates belong to their person
	me, other := uuid.New(), uuid.New()
	tp, _ := s.AddDropTemplate(ctx, DropTemplate{OwnerID: me, Name: "Mine", Data: `{"Days":3}`})
	if l, _ := s.DropTemplates(ctx, me); len(l) != 1 || l[0].Data != `{"Days":3}` {
		t.Errorf("templates: %+v", l)
	}
	if l, _ := s.DropTemplates(ctx, other); len(l) != 0 {
		t.Errorf("another person's: %+v", l)
	}
	s.RemoveDropTemplate(ctx, other, tp.ID)
	if l, _ := s.DropTemplates(ctx, me); len(l) != 1 {
		t.Error("another person removed it")
	}
	s.RemoveDropTemplate(ctx, me, tp.ID)
	if l, _ := s.DropTemplates(ctx, me); len(l) != 0 {
		t.Error("removing a template")
	}
	// what a guest sent: found by that guest only; a waiting file becomes an asset; a rejected one is forgotten
	s.AddDropUpload(ctx, DropUpload{BoxID: a.ID, Guest: "g1", PendingID: "p1", Name: "w.jpg", Size: 5})
	s.AddDropUpload(ctx, DropUpload{BoxID: a.ID, Guest: "g1", AssetID: "asset1", Name: "s.jpg", Size: 6})
	s.AddDropUpload(ctx, DropUpload{BoxID: a.ID, Guest: "g2", AssetID: "asset2", Name: "x.jpg", Size: 7})
	mine, _ := s.DropUploadsOf(ctx, a.ID, "g1", 10)
	if len(mine) != 2 {
		t.Fatalf("uploads: %+v", mine)
	}
	if _, err := s.DropUploadByID(ctx, a.ID, "g2", mine[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("someone else's file: %v", err)
	}
	s.DropUploadApproved(ctx, "p1", "asset9")
	if u, _ := s.DropUploadsOf(ctx, a.ID, "g1", 10); u[0].PendingID != "" && u[1].PendingID != "" {
		t.Errorf("approval: %+v", u)
	}
	s.AddDropUpload(ctx, DropUpload{BoxID: a.ID, Guest: "g1", PendingID: "p2", Name: "r.jpg", Size: 1})
	s.DropUploadGone(ctx, "p2")
	if u, _ := s.DropUploadsOf(ctx, a.ID, "g1", 10); len(u) != 2 {
		t.Errorf("rejected file forgotten: %+v", u)
	}
	if ok, _ := s.RemoveDropUpload(ctx, mine[0].ID); !ok {
		t.Error("remove")
	}
	s.AddDropRemoved(ctx, a.ID)
	if g, _ := s.DropBox(ctx, a.ID); g.Removed != 1 {
		t.Errorf("removed counter: %d", g.Removed)
	}
}
