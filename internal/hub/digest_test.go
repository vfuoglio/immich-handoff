package hub_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// feedService makes bob's service "feed" with ann as its sender, and returns the id of ann's sender link.
func feedService(t *testing.T, e *env) string {
	t.Helper()
	ctx := context.Background()
	if err := e.h.CreateService(ctx, e.sess, bob, hub.CreateParams{Name: "feed", Tag: "feed", Policy: "open"}); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Join(ctx, e.ann, ann, "feed", "", "copy"); err != nil {
		t.Fatal(err)
	}
	links, _ := e.st.Links(ctx)
	for _, l := range links {
		if l.Service == "feed" && l.Role == store.RoleSender {
			return l.ID
		}
	}
	t.Fatal("no sender link")
	return ""
}

func serviceKey(t *testing.T, e *env, name string) store.Service {
	t.Helper()
	all, _ := e.st.Services(context.Background())
	for _, v := range all {
		if v.Name == name {
			return v
		}
	}
	t.Fatal("no such service")
	return store.Service{}
}

// arrive puts a photo in the fake Immich as bob's copy and records it in the ledger as arrived from the link.
func arrive(t *testing.T, e *env, link, name string, clip, remote bool) string {
	t.Helper()
	id := uuid.NewString()
	e.f.mu.Lock()
	if e.f.assetTags == nil {
		e.f.assetTags = map[string][]string{}
	}
	if e.f.assetName == nil {
		e.f.assetName = map[string]string{}
	}
	e.f.assetTags[id] = []string{}
	e.f.assetName[id] = name
	e.f.mu.Unlock()
	if err := e.st.AddHandoverItem(context.Background(), link, "b1", uuid.New(), uuid.MustParse(id), clip, remote); err != nil {
		t.Fatal(err)
	}
	return id
}

func waitDigest(t *testing.T, e *env, service string, madeAfter time.Time) hub.DigestInfo {
	t.Helper()
	for i := 0; i < 400; i++ {
		info := e.h.DigestFor(context.Background(), service)
		if !info.Running && info.Made.After(madeAfter) {
			return info
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("the digest did not finish")
	return hub.DigestInfo{}
}

func TestTheDigestIsTurnedOnWithTheOwnersSessionAndOffTakesBackOnlyWhatItAdded(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	feedService(t, e)
	v := serviceKey(t, e, "feed")
	before := append([]string(nil), e.f.keyPerms[v.KeyID.String()]...)

	if err := e.h.EnableDigest(ctx, e.ann, ann, "feed"); !isErr(err, hub.ErrNotOwner) {
		t.Errorf("someone who is not the owner: %v", err)
	}
	if err := e.h.EnableDigest(ctx, e.sess, bob, "feed"); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, p := range e.f.keyPerms[v.KeyID.String()] {
		have[p] = true
	}
	for _, p := range immich.DigestPermissions {
		if !have[string(p)] {
			t.Errorf("the key lacks %s", p)
		}
	}
	for _, p := range before {
		if !have[p] {
			t.Errorf("the key lost %s", p)
		}
	}
	if info := e.h.DigestFor(ctx, "feed"); !info.On || info.Next.IsZero() {
		t.Errorf("%+v", info)
	}
	if err := e.h.EnableDigest(ctx, e.sess, bob, "feed"); err != nil { // turning it on again changes nothing
		t.Fatal(err)
	}

	if err := e.h.DisableDigest(ctx, e.sess, bob, "feed"); err != nil {
		t.Fatal(err)
	}
	after := map[string]bool{}
	for _, p := range e.f.keyPerms[v.KeyID.String()] {
		after[p] = true
	}
	for _, p := range immich.DigestPermissions {
		if after[string(p)] && !contains(before, string(p)) {
			t.Errorf("the digest left %s on the key", p)
		}
	}
	for _, p := range before {
		if !after[p] {
			t.Errorf("turning the digest off took %s, which it never added", p)
		}
	}
	if info := e.h.DigestFor(ctx, "feed"); info.On {
		t.Error("still on")
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func isErr(err, target error) bool {
	return err != nil && strings.Contains(err.Error(), target.Error())
}

func TestADigestKeepsTheBestOfTheWeekMakesAnAlbumAndALinkThatEndsAndSaysWhatItLeftOut(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	e.f.thumbs = map[string][]byte{"sharp1": synth("sharp"), "sharp2": synth("sharp-copy"), "scene": synth("scene"), "blur": synth("blur"), "dark": synth("dark"), "bright": synth("bright"), "gone": synth("scene"), "clip": synth("scene"), "far": synth("scene")}
	link := feedService(t, e)
	if err := e.h.EnableDigest(ctx, e.sess, bob, "feed"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"sharp1.jpg", "sharp2.jpg", "scene.jpg", "blur.jpg", "dark.jpg", "bright.jpg"} {
		arrive(t, e, link, n, false, false)
	}
	trashed := arrive(t, e, link, "gone.jpg", false, false) // in the owner's trash by now
	e.f.trashed = map[string]bool{trashed: true}
	arrive(t, e, link, "clip.mov", true, false) // a live photo's motion clip is not a photo of its own
	arrive(t, e, link, "far.jpg", false, true)  // a copy on another server cannot be looked at from here

	if err := e.h.MakeDigestNow(ctx, ann, "feed"); !isErr(err, hub.ErrNotOwner) {
		t.Errorf("someone who is not the owner: %v", err)
	}
	started := time.Now()
	if err := e.h.MakeDigestNow(ctx, bob, "feed"); err != nil {
		t.Fatal(err)
	}
	info := waitDigest(t, e, "feed", started.Add(-time.Second))
	if info.Photos != 2 || info.Left != 4 || info.People != 1 || info.Album == "" || !strings.HasPrefix(info.Album, "feed week of ") || info.LinkKey == "" {
		t.Fatalf("%+v", info)
	}
	why := map[string]string{}
	for _, l := range info.LeftList {
		why[l.Name] = l.Reason
	}
	if why["blur.jpg"] != hub.VerdictBlurry || why["dark.jpg"] != hub.VerdictDark || why["bright.jpg"] != hub.VerdictBright || len(why) != 4 {
		t.Errorf("left out: %v", why)
	}
	for n, r := range why {
		if r == hub.VerdictDuplicate && !strings.HasPrefix(n, "sharp") {
			t.Errorf("the repeat must be one of the two sharp shots: %s", n)
		}
	}
	// the album holds the two kept photos; nothing was deleted or changed
	if len(e.f.albums) != 1 || e.f.albums[0]["albumName"] != info.Album {
		t.Fatalf("albums: %v", e.f.albums)
	}
	albumID := e.f.albums[0]["id"].(string)
	if len(e.f.members[albumID]) != 2 || !e.f.members[albumID][idOfName(e, "scene.jpg")] {
		t.Errorf("the album: %v", e.f.members[albumID])
	}
	if len(e.f.trashed) != 1 {
		t.Errorf("a digest must never delete: %v", e.f.trashed)
	}
	// one shared link, for that album, ending in about a month
	if len(e.f.links) != 1 {
		t.Fatalf("links: %v", e.f.links)
	}
	for _, l := range e.f.links {
		if l["albumId"] != albumID || l["allowUpload"] == true {
			t.Errorf("the link: %v", l)
		}
		ends, _ := time.Parse(time.RFC3339, l["expiresAt"].(string))
		if d := time.Until(ends); d < 29*24*time.Hour || d > 31*24*time.Hour {
			t.Errorf("the link ends in %v", d)
		}
	}
	// the owner, and only the owner, is told
	acts, _ := e.h.Activity(ctx, bobID, 10)
	found := false
	for _, a := range acts {
		if a.Kind == "digest" && strings.Contains(en(a.Text), "feed is ready: 2 photos of the week") {
			found = true
		}
	}
	if !found {
		t.Errorf("the owner's activity: %+v", acts)
	}
	if acts, _ := e.h.Activity(ctx, annID, 10); len(acts) > 0 {
		for _, a := range acts {
			if a.Kind == "digest" {
				t.Errorf("the service's members must not get the owner's digest line: %+v", a)
			}
		}
	}

	// making another ends the first link: a person has one to follow
	first := e.f.links
	var firstID string
	for id := range first {
		firstID = id
	}
	again := time.Now()
	if err := e.h.MakeDigestNow(ctx, bob, "feed"); err != nil {
		t.Fatal(err)
	}
	waitDigest(t, e, "feed", again.Add(-time.Second))
	if _, still := e.f.links[firstID]; still || len(e.f.links) != 1 {
		t.Errorf("the old link must end: %v (removed %v)", e.f.links, e.f.removedLinks)
	}
}

func TestADigestOfAWeekWhenNothingArrivedSaysSoAndMakesNothing(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	feedService(t, e)
	e.h.EnableDigest(ctx, e.sess, bob, "feed")
	started := time.Now()
	if err := e.h.MakeDigestNow(ctx, bob, "feed"); err != nil {
		t.Fatal(err)
	}
	info := waitDigest(t, e, "feed", started.Add(-time.Second))
	if info.Photos != 0 || !strings.Contains(info.Note, "Nothing arrived") || len(e.f.albums) != 0 || len(e.f.links) != 0 {
		t.Errorf("%+v albums %v links %v", info, e.f.albums, e.f.links)
	}
	if err := e.h.DisableDigest(ctx, e.sess, bob, "feed"); err != nil {
		t.Fatal(err)
	}
	if err := e.h.MakeDigestNow(ctx, bob, "feed"); err != hub.ErrDigestOff {
		t.Errorf("a digest that is off: %v", err)
	}
}

// at is a time on the given weekday (0 is Sunday), hour and minute, in the server's zone, in the week of 2026-11-09.
func at(weekday time.Weekday, hour, min int) time.Time {
	monday := time.Date(2026, 11, 9, 0, 0, 0, 0, time.Local)
	return monday.AddDate(0, 0, (int(weekday)+6)%7).Add(time.Duration(hour)*time.Hour + time.Duration(min)*time.Minute)
}

func TestTheDigestIsMadeOnceAWeekFromMondayMorningAndNotInTheWeekItWasTurnedOn(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	feedService(t, e)
	clock := at(time.Wednesday, 12, 0)
	e.h.Now = func() time.Time { return clock }
	if err := e.h.EnableDigest(ctx, e.sess, bob, "feed"); err != nil {
		t.Fatal(err)
	}
	made := func() bool {
		d, _, _ := e.st.DigestFor(ctx, "feed")
		return !d.MadeAt.IsZero()
	}
	wait := func() {
		for i := 0; i < 100 && e.h.DigestRunning("feed"); i++ {
			time.Sleep(20 * time.Millisecond)
		}
	}
	for _, c := range []time.Time{at(time.Wednesday, 13, 0), at(time.Saturday, 12, 0), at(time.Sunday, 23, 59), at(time.Monday, 7, 59) /* the next Monday, before eight */} {
		clock = c.AddDate(0, 0, map[bool]int{true: 7, false: 0}[c.Weekday() == time.Monday])
		e.h.DigestMaintenance(ctx)
		wait()
		if made() {
			t.Fatalf("a digest was made at %v, before next Monday 08:00", clock)
		}
	}
	clock = at(time.Monday, 8, 0).AddDate(0, 0, 7)
	e.h.DigestMaintenance(ctx)
	wait()
	d, _, _ := e.st.DigestFor(ctx, "feed")
	if !made() || d.Week != "2026-11-16" {
		t.Fatalf("not made on Monday 08:00: %+v", d)
	}
	first := d.MadeAt
	clock = at(time.Thursday, 9, 0).AddDate(0, 0, 7) // the same week again
	e.h.DigestMaintenance(ctx)
	wait()
	if d2, _, _ := e.st.DigestFor(ctx, "feed"); !d2.MadeAt.Equal(first) {
		t.Error("made twice in one week")
	}
	clock = at(time.Monday, 8, 5).AddDate(0, 0, 14) // the week after
	e.h.DigestMaintenance(ctx)
	wait()
	if d3, _, _ := e.st.DigestFor(ctx, "feed"); d3.Week != "2026-11-23" {
		t.Errorf("not made the week after: %+v", d3)
	}
	// a digest that is off is never made
	e.h.DisableDigest(ctx, e.sess, bob, "feed")
	clock = at(time.Monday, 9, 0).AddDate(0, 0, 21)
	e.h.DigestMaintenance(ctx)
	wait()
	if d4, _, _ := e.st.DigestFor(ctx, "feed"); d4.Week != "2026-11-23" {
		t.Errorf("a digest that is off was made: %+v", d4)
	}
}

func TestAKeyMadeAgainKeepsWhatTheDigestNeedsAndRemovingTheServiceEndsItsLink(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	e.f.thumbs = map[string][]byte{"scene": synth("scene")}
	link := feedService(t, e)
	e.h.EnableDigest(ctx, e.sess, bob, "feed")
	v := serviceKey(t, e, "feed")
	// something takes the digest's permissions off the key (it was recreated)
	e.f.mu.Lock()
	var keep []string
	for _, p := range e.f.keyPerms[v.KeyID.String()] {
		if !strings.HasPrefix(p, "sharedLink") && p != "asset.view" {
			keep = append(keep, p)
		}
	}
	e.f.keyPerms[v.KeyID.String()] = keep
	e.f.mu.Unlock()
	e.h.ForgetGranted()
	e.h.EnsureKeyPermissions(ctx, e.sess, bob)
	have := map[string]bool{}
	for _, p := range e.f.keyPerms[v.KeyID.String()] {
		have[p] = true
	}
	if !have["sharedLink.create"] || !have["asset.view"] {
		t.Errorf("the digest's permissions were not put back: %v", e.f.keyPerms[v.KeyID.String()])
	}

	arrive(t, e, link, "scene.jpg", false, false)
	started := time.Now()
	e.h.MakeDigestNow(ctx, bob, "feed")
	waitDigest(t, e, "feed", started.Add(-time.Second))
	if len(e.f.links) != 1 {
		t.Fatalf("links %v", e.f.links)
	}
	if err := e.h.DeleteService(ctx, e.sess, bob, "feed"); err != nil {
		t.Fatal(err)
	}
	if len(e.f.links) != 0 {
		t.Errorf("the digest's link outlived the service: %v", e.f.links)
	}
	if _, ok, _ := e.st.DigestFor(ctx, "feed"); ok {
		t.Error("the digest row outlived the service")
	}
}
