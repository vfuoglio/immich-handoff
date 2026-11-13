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

func TestDropBoxPromptsMessagesAndShare(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t, t.TempDir(), "drop-secret-value-1234")
	b, _ := s.AddDropBox(ctx, DropBox{Token: "prompt-token-1234567", Name: "Hunt", Key: "k", Prompts: []Prompt{{"p1", "The cake"}, {"p2", "The band"}}, PromptRequired: true, GuestBook: true})
	got, _ := s.DropBox(ctx, b.ID)
	if len(got.Prompts) != 2 || got.Prompts[1].Text != "The band" || !got.PromptRequired || !got.GuestBook {
		t.Errorf("round trip: %+v", got)
	}
	// a required list needs a list
	s.SetDropPrompts(ctx, b.ID, nil, true)
	if g, _ := s.DropBox(ctx, b.ID); len(g.Prompts) != 0 || g.PromptRequired {
		t.Errorf("an empty list requires nothing: %+v", g)
	}
	s.SetDropPrompts(ctx, b.ID, []Prompt{{"p1", "The cake"}}, true)
	s.SetDropGuestBook(ctx, b.ID, false)
	if g, _ := s.DropBox(ctx, b.ID); len(g.Prompts) != 1 || !g.PromptRequired || g.GuestBook {
		t.Errorf("edit: %+v", g)
	}
	// counts go up and down and never below nothing
	s.AddPromptCount(ctx, b.ID, "p1", 1)
	s.AddPromptCount(ctx, b.ID, "p1", 1)
	s.AddPromptCount(ctx, b.ID, "p2", 1)
	s.AddPromptCount(ctx, b.ID, "p2", -1)
	s.AddPromptCount(ctx, b.ID, "p2", -1)
	if c, _ := s.PromptCounts(ctx, b.ID); c["p1"] != 2 || c["p2"] != 0 {
		t.Errorf("counts: %v", c)
	}
	// a waiting file remembers its prompt
	p, _ := s.AddDropPending(ctx, DropPending{BoxID: b.ID, Name: "a.jpg", Size: 1, Kind: "image", Path: "/x", Prompt: "p1"})
	if got, _ := s.DropPendingByID(ctx, p.ID); got.Prompt != "p1" {
		t.Errorf("pending prompt: %+v", got)
	}

	// messages: kept with or without a voice note, listed newest first, played, removed
	s.AddDropMessage(ctx, DropMessage{BoxID: b.ID, Who: "w1", Name: "Eve", Text: "first"}, nil)
	time.Sleep(2 * time.Millisecond)
	s.AddDropMessage(ctx, DropMessage{BoxID: b.ID, Who: "w2", Text: "second", AudioKind: "audio/webm"}, []byte{1, 2, 3})
	msgs, _ := s.DropMessages(ctx, b.ID, 10)
	if len(msgs) != 2 || msgs[0].Text != "second" || !msgs[0].HasAudio || msgs[1].HasAudio || s.DropMessageCount(ctx, b.ID) != 2 {
		t.Fatalf("messages: %+v", msgs)
	}
	if box, kind, data, err := s.DropMessageAudio(ctx, msgs[0].ID); err != nil || box != b.ID || kind != "audio/webm" || len(data) != 3 {
		t.Errorf("audio: %s %s %v %v", box, kind, data, err)
	}
	if _, _, _, err := s.DropMessageAudio(ctx, msgs[1].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("no audio: %v", err)
	}
	if all, _ := s.DropMessageAudios(ctx, b.ID); len(all) != 1 {
		t.Errorf("audios: %v", all)
	}
	s.RemoveDropMessage(ctx, msgs[0].ID)
	if s.DropMessageCount(ctx, b.ID) != 1 {
		t.Error("removing a message")
	}

	// the share: one per box, replaced by a new one, gone when removed, and forgotten with a revoked box
	if _, err := s.DropShareOf(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("no share yet: %v", err)
	}
	end := time.Now().Add(time.Hour)
	s.SetDropShare(ctx, DropShare{BoxID: b.ID, LinkID: "l1", Key: "k1", Message: "thanks", ExpiresAt: end})
	s.SetDropShare(ctx, DropShare{BoxID: b.ID, LinkID: "l2", Key: "k2", Message: "thanks again", ExpiresAt: end})
	if sh, _ := s.DropShareOf(ctx, b.ID); sh.Key != "k2" || sh.LinkID != "l2" || sh.Message != "thanks again" || sh.ExpiresAt.Sub(end) > time.Second {
		t.Errorf("share: %+v", sh)
	}
	s.SetDropBoxState(ctx, b.ID, DropRevoked, "")
	s.PruneDropBoxes(ctx, time.Now().Add(time.Hour))
	if _, err := s.DropShareOf(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a pruned box keeps no share: %v", err)
	}
	if s.DropMessageCount(ctx, b.ID) != 0 {
		t.Error("a pruned box keeps no messages")
	}
}

func TestDropBoxAssetsWallHighlightsAndOnward(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t, t.TempDir(), "drop-secret-value-1234")
	b, _ := s.AddDropBox(ctx, DropBox{Token: "wall-token-12345678", Name: "Wall", Key: "k", Wall: "the-wall-token-0123456789012345678901"})
	other, _ := s.AddDropBox(ctx, DropBox{Token: "other-token-1234567", Name: "Other", Key: "k"})

	// the photos a box kept: once each, in order, found by their place, and forgotten when taken back
	for _, a := range []string{"a1", "a2", "a3", "a2"} {
		s.AddDropAsset(ctx, b.ID, a, "p1", "Ann")
	}
	s.AddDropAsset(ctx, other.ID, "o1", "", "")
	list, _ := s.DropAssetsAfter(ctx, b.ID, 0, 10)
	if len(list) != 3 || list[0].AssetID != "a1" || list[2].AssetID != "a3" || list[0].ID >= list[1].ID {
		t.Fatalf("assets: %+v", list)
	}
	if after, _ := s.DropAssetsAfter(ctx, b.ID, list[1].ID, 10); len(after) != 1 || after[0].AssetID != "a3" {
		t.Errorf("after: %+v", after)
	}
	if latest, _ := s.DropAssetsLatest(ctx, b.ID, 2); len(latest) != 2 || latest[0].AssetID != "a3" {
		t.Errorf("latest: %+v", latest)
	}
	if a, err := s.DropAssetByID(ctx, b.ID, list[1].ID); err != nil || a.AssetID != "a2" || a.Prompt != "p1" {
		t.Errorf("by id: %+v %v", a, err)
	}
	if _, err := s.DropAssetByID(ctx, other.ID, list[1].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another box's photo: %v", err)
	}
	s.RemoveDropAsset(ctx, b.ID, "a2")
	if ids, _ := s.DropAssetIDs(ctx, b.ID); len(ids) != 2 || ids[0] != "a1" || ids[1] != "a3" {
		t.Errorf("ids: %v", ids)
	}

	// the wall: found by its token, not by the box's, and gone with a revoked box
	if got, err := s.DropBoxByWall(ctx, "the-wall-token-0123456789012345678901"); err != nil || got.ID != b.ID || got.Key != "k" {
		t.Errorf("by wall: %+v %v", got, err)
	}
	if _, err := s.DropBoxByWall(ctx, ""); !errors.Is(err, ErrDropBoxGone) {
		t.Errorf("no token: %v", err)
	}
	if _, err := s.DropBoxByWall(ctx, "wall-token-12345678"); err == nil {
		t.Error("a box's own token is not a wall")
	}
	s.SetDropWall(ctx, b.ID, "")
	if _, err := s.DropBoxByWall(ctx, "the-wall-token-0123456789012345678901"); err == nil {
		t.Error("a stopped wall")
	}
	s.SetDropWall(ctx, b.ID, "new-wall-token-0123456789012345678901234")

	// messages on the wall
	s.AddDropMessage(ctx, DropMessage{BoxID: b.ID, Name: "Eve", Text: "Hello"}, nil)
	s.AddDropMessage(ctx, DropMessage{BoxID: b.ID, Text: "Not chosen"}, nil)
	all, _ := s.DropMessages(ctx, b.ID, 10)
	for _, m := range all {
		if m.Text == "Hello" {
			s.SetMessageOnWall(ctx, m.ID, true)
		}
	}
	if w, _ := s.WallMessages(ctx, b.ID, 10); len(w) != 1 || w[0].Text != "Hello" || w[0].Name != "Eve" {
		t.Errorf("wall messages: %+v", w)
	}

	// the best-of: state and verdicts, replaced as a whole
	started := time.Now()
	s.SetDropHighlight(ctx, DropHighlight{BoxID: b.ID, Strictness: "normal", Status: HighlightRunning, Total: 10, Started: started})
	s.SetDropHighlight(ctx, DropHighlight{BoxID: b.ID, Strictness: "normal", Status: HighlightDone, Total: 10, Done: 10, Kept: 6, Album: "Wall best of", Started: started, Finished: time.Now()})
	if h, err := s.DropHighlightOf(ctx, b.ID); err != nil || h.Status != HighlightDone || h.Kept != 6 || h.Album != "Wall best of" || h.Finished.IsZero() {
		t.Errorf("highlight: %+v %v", h, err)
	}
	if _, err := s.DropHighlightOf(ctx, other.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("none made: %v", err)
	}
	s.ReplaceDropVerdicts(ctx, b.ID, []Verdict{{"a1", "kept", "", "one.jpg"}, {"a2", "blurry", "detail 3", "two.jpg"}, {"a3", "kept", "", "three.jpg"}})
	s.ReplaceDropVerdicts(ctx, b.ID, []Verdict{{"a1", "kept", "", "one.jpg"}, {"a2", "dark", "brightness 9 of 255", "two.jpg"}})
	if ex, _ := s.DropVerdicts(ctx, b.ID, true, 10); len(ex) != 1 || ex[0].Verdict != "dark" || ex[0].Detail != "brightness 9 of 255" {
		t.Errorf("left out: %+v", ex)
	}
	if k, _ := s.DropVerdicts(ctx, b.ID, false, 10); len(k) != 1 || k[0].AssetID != "a1" {
		t.Errorf("kept: %+v", k)
	}

	// handing on: the choice and what was done
	s.SetDropOnward(ctx, b.ID, "family", true)
	if g, _ := s.DropBox(ctx, b.ID); g.OnwardService != "family" || !g.OnwardAuto {
		t.Errorf("onward: %+v", g)
	}
	s.SetDropOnward(ctx, b.ID, "", true)
	if g, _ := s.DropBox(ctx, b.ID); g.OnwardService != "" || g.OnwardAuto {
		t.Errorf("cleared: %+v", g)
	}
	s.AddDropOnward(ctx, b.ID, DropOnward{Service: "family", Scope: "all", Count: 5, By: "Bob"})
	s.AddDropOnward(ctx, b.ID, DropOnward{Service: "print", Scope: "best", Count: 2, By: "Bob"})
	if log, _ := s.DropOnwardLog(ctx, b.ID, 10); len(log) != 2 || log[0].Service != "print" || log[0].Count != 2 || log[1].Scope != "all" {
		t.Errorf("log: %+v", log)
	}

	// a revoked box is forgotten with all of it
	s.SetDropBoxState(ctx, b.ID, DropRevoked, "")
	s.PruneDropBoxes(ctx, time.Now().Add(time.Hour))
	if ids, _ := s.DropAssetIDs(ctx, b.ID); len(ids) != 0 {
		t.Error("a pruned box keeps no photos")
	}
	if _, err := s.DropHighlightOf(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Error("a pruned box keeps no best-of")
	}
	if log, _ := s.DropOnwardLog(ctx, b.ID, 10); len(log) != 0 {
		t.Error("a pruned box keeps no log")
	}
}

func TestHeartsAndNamesOfAGallery(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t, t.TempDir(), "drop-secret-value-1234")
	b, _ := s.AddDropBox(ctx, DropBox{Token: "gallery-token-123456", Name: "Gallery", Key: "k", Gallery: true, GalleryDownload: true, GalleryNames: true, KeepDays: 7})
	other, _ := s.AddDropBox(ctx, DropBox{Token: "other-token-1234567", Name: "Other", Key: "k"})
	if got, _ := s.DropBox(ctx, b.ID); !got.Gallery || !got.GalleryDownload || !got.GalleryNames || got.KeepDays != 7 {
		t.Fatalf("the settings come back: %+v", got)
	}
	for _, a := range []string{"a1", "a2", "a3", "a4"} {
		s.AddDropAsset(ctx, b.ID, a, "", "Name of "+a)
	}
	s.AddDropAsset(ctx, other.ID, "o1", "", "")
	all, _ := s.DropAssetsAfter(ctx, b.ID, 0, 10)
	if all[2].GuestName != "Name of a3" {
		t.Errorf("the name given comes back: %+v", all[2])
	}

	// a page of older photos, newest first
	if older, _ := s.DropAssetsBefore(ctx, b.ID, all[3].ID, 2); len(older) != 2 || older[0].AssetID != "a3" || older[1].AssetID != "a2" {
		t.Errorf("before the newest: %+v", older)
	}
	if first, _ := s.DropAssetsBefore(ctx, b.ID, 0, 10); len(first) != 4 || first[0].AssetID != "a4" {
		t.Errorf("from the newest: %+v", first)
	}

	// a heart is one guest's, given once, taken back by the same tap, counted for everyone
	h, n, err := s.ToggleHeart(ctx, b.ID, all[0].ID, "ann")
	if err != nil || !h || n != 1 {
		t.Fatalf("first heart: %v %d %v", h, n, err)
	}
	if h, n, _ = s.ToggleHeart(ctx, b.ID, all[0].ID, "bob"); !h || n != 2 {
		t.Errorf("second guest: %v %d", h, n)
	}
	if h, n, _ = s.ToggleHeart(ctx, b.ID, all[0].ID, "ann"); h || n != 1 {
		t.Errorf("taking it back: %v %d", h, n)
	}
	s.ToggleHeart(ctx, b.ID, all[1].ID, "bob")
	counts, _ := s.HeartCounts(ctx, b.ID, all[0].ID, all[3].ID)
	if counts[all[0].ID] != 1 || counts[all[1].ID] != 1 || len(counts) != 2 {
		t.Errorf("counts: %v", counts)
	}
	if mine, _ := s.GuestHearts(ctx, b.ID, "bob", all[0].ID, all[3].ID); !mine[all[0].ID] || !mine[all[1].ID] || len(mine) != 2 {
		t.Errorf("bob's hearts: %v", mine)
	}
	if mine, _ := s.GuestHearts(ctx, b.ID, "ann", all[0].ID, all[3].ID); len(mine) != 0 {
		t.Errorf("ann took hers back: %v", mine)
	}
	// a photo of another box cannot be hearted through this one
	oa, _ := s.DropAssetsAfter(ctx, other.ID, 0, 1)
	if _, _, err := s.ToggleHeart(ctx, b.ID, oa[0].ID, "ann"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another box's photo: %v", err)
	}
	// a photo that goes takes its hearts with it
	s.RemoveDropAsset(ctx, b.ID, all[0].AssetID)
	if counts, _ = s.HeartCounts(ctx, b.ID, 0, 1<<40); len(counts) != 1 || counts[all[1].ID] != 1 {
		t.Errorf("after the photo went: %v", counts)
	}

	// the settings are switched together: download and names need the gallery
	s.SetDropGallery(ctx, b.ID, false, true, true)
	if got, _ := s.DropBox(ctx, b.ID); got.Gallery || got.GalleryDownload || got.GalleryNames {
		t.Errorf("no gallery, no download or names: %+v", got)
	}
}

func TestAModeratorLinkAndOnceOnlyMessages(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t, t.TempDir(), "drop-secret-value-1234")
	b, _ := s.AddDropBox(ctx, DropBox{Token: "mod-box-token-123456", Name: "Mod", Key: "k"})
	if _, err := s.DropBoxByMod(ctx, ""); !errors.Is(err, ErrDropBoxGone) {
		t.Errorf("an empty token finds nothing: %v", err)
	}
	if err := s.SetDropMod(ctx, b.ID, "the-moderator-token"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.DropBoxByMod(ctx, "the-moderator-token"); err != nil || got.ID != b.ID || got.Key != "k" || got.ModToken != "the-moderator-token" {
		t.Errorf("by moderator token: %+v %v", got, err)
	}
	if _, err := s.DropBoxByMod(ctx, "mod-box-token-123456"); !errors.Is(err, ErrDropBoxGone) {
		t.Errorf("the box's own token is not a moderator link: %v", err)
	}
	s.SetDropMod(ctx, b.ID, "")
	if _, err := s.DropBoxByMod(ctx, "the-moderator-token"); !errors.Is(err, ErrDropBoxGone) {
		t.Errorf("an ended link: %v", err)
	}
	s.SetDropMod(ctx, b.ID, "again")
	s.SetDropBoxState(ctx, b.ID, DropRevoked, "")
	if _, err := s.DropBoxByMod(ctx, "again"); !errors.Is(err, ErrDropBoxGone) {
		t.Errorf("a revoked box: %v", err)
	}

	// of several callers, one claims a bit; clearing it lets the next claim
	c, _ := s.AddDropBox(ctx, DropBox{Token: "claim-box-token-12345", Name: "Claim", Key: "k"})
	first, _ := s.ClaimDropNotified(ctx, c.ID, 8)
	second, _ := s.ClaimDropNotified(ctx, c.ID, 8)
	if !first || second {
		t.Errorf("claims: %v %v", first, second)
	}
	s.MarkDropNotified(ctx, c.ID, 1)
	s.ClearDropNotified(ctx, c.ID, 8)
	if got, _ := s.DropBox(ctx, c.ID); got.Notified != 1 {
		t.Errorf("only the cleared bit goes: %d", got.Notified)
	}
	if again, _ := s.ClaimDropNotified(ctx, c.ID, 8); !again {
		t.Error("after clearing, the bit can be claimed again")
	}
}

func TestHidingAndPinningPhotosOfAGallery(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t, t.TempDir(), "drop-secret-value-1234")
	b, _ := s.AddDropBox(ctx, DropBox{Token: "pins-token-1234567890", Name: "Pins", Key: "k"})
	other, _ := s.AddDropBox(ctx, DropBox{Token: "other-pins-token-12345", Name: "Other", Key: "k"})
	for _, a := range []string{"a1", "a2", "a3", "a4"} {
		s.AddDropAsset(ctx, b.ID, a, "", "")
	}
	s.AddDropAsset(ctx, other.ID, "o1", "", "")
	all, _ := s.DropAssetsAfter(ctx, b.ID, 0, 10)
	if err := s.MarkAsset(ctx, b.ID, all[1].ID, "hidden", true); err != nil {
		t.Fatal(err)
	}
	s.MarkAsset(ctx, b.ID, all[2].ID, "pinned", true)
	if err := s.MarkAsset(ctx, b.ID, all[0].ID, "colour", true); err == nil {
		t.Error("an unknown mark is refused")
	}
	oa, _ := s.DropAssetsAfter(ctx, other.ID, 0, 1)
	if err := s.MarkAsset(ctx, b.ID, oa[0].ID, "hidden", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("another box's photo: %v", err)
	}

	// guests' queries leave the hidden out; the host's do not
	if got, _ := s.DropAssetsAfter(ctx, b.ID, 0, 10); len(got) != 3 {
		t.Errorf("after: %d", len(got))
	}
	if got, _ := s.DropAssetsBefore(ctx, b.ID, 0, 10); len(got) != 3 || got[0].AssetID != "a4" {
		t.Errorf("before: %+v", got)
	}
	if got, _ := s.DropAssetsLatest(ctx, b.ID, 10); len(got) != 3 {
		t.Errorf("latest: %d", len(got))
	}
	host, _ := s.DropAssetsAll(ctx, b.ID, 0, 10)
	if len(host) != 4 || !host[2].Hidden || !host[1].Pinned || host[3].Hidden {
		t.Errorf("all: %+v", host)
	}
	if ids, _ := s.DropVisibleAssetIDs(ctx, b.ID); len(ids) != 3 || ids[1] != "a3" {
		t.Errorf("visible ids: %v", ids)
	}
	if ids, _ := s.DropAssetIDs(ctx, b.ID); len(ids) != 4 {
		t.Errorf("all ids: %v", ids)
	}
	if _, _, err := s.ToggleHeart(ctx, b.ID, all[1].ID, "ann"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a heart on a hidden photo: %v", err)
	}
	hidden, pinned, _ := s.AssetMarks(ctx, b.ID)
	if !hidden["a2"] || !pinned["a3"] || len(hidden) != 1 || len(pinned) != 1 {
		t.Errorf("marks: %v %v", hidden, pinned)
	}

	// loved: the pinned first, then the most hearts, never a hidden one
	s.ToggleHeart(ctx, b.ID, all[0].ID, "g1")
	s.ToggleHeart(ctx, b.ID, all[0].ID, "g2")
	s.ToggleHeart(ctx, b.ID, all[3].ID, "g1")
	if loved, _ := s.LovedAssetIDs(ctx, b.ID, 10); len(loved) != 3 || loved[0] != "a3" || loved[1] != "a1" || loved[2] != "a4" {
		t.Errorf("loved: %v", loved)
	}
	if loved, _ := s.LovedAssetIDs(ctx, b.ID, 2); len(loved) != 2 {
		t.Errorf("the most: %v", loved)
	}
	s.MarkAsset(ctx, b.ID, all[0].ID, "hidden", true)
	if loved, _ := s.LovedAssetIDs(ctx, b.ID, 10); len(loved) != 2 || loved[1] != "a4" {
		t.Errorf("a hidden one is never loved: %v", loved)
	}
	s.MarkAsset(ctx, b.ID, all[1].ID, "hidden", false)
	if got, _ := s.DropAssetsAfter(ctx, b.ID, 0, 10); len(got) != 3 {
		t.Errorf("shown again: %d", len(got))
	}
}
