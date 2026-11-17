package web_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

// withMemories is the club, with push set up so that the messages can be offered.
func withMemories(t *testing.T, st **store.Store) func(*web.Server) {
	return func(srv *web.Server) {
		withClub(t, st)(srv)
		p := &hub.Push{Store: *st, Sender: &pushSink{}, Key: "PUBLIC-KEY-FOR-THE-BROWSER", Log: srv.Log}
		t.Cleanup(p.Close)
		srv.Hub.Push = p
	}
}

func TestTheOnThisDayCardTurnsTheMessagesOnChangesThemAndTurnsThemOff(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withMemories(t, &st))
	ctx := context.Background()
	if _, page := do(t, "GET", app.URL+"/", "sess=other", nil); !strings.Contains(page, "On this day") || !strings.Contains(page, "Turn on") || strings.Contains(page, "Every day at") {
		t.Fatal("the card is offered, and it is off")
	}
	if code, _ := postForm(t, app.URL+"/me/memories", "", "action=on&at=07:30&push=1"); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, b := postForm(t, app.URL+"/me/memories", "sess=other", "action=on&at=7pm&push=1"); code != 409 || !strings.Contains(b, "08:00") {
		t.Errorf("a time that is not a time: %d %s", code, b)
	}
	if code, _ := postForm(t, app.URL+"/me/memories", "sess=other", "action=on&at=07:30"); code != 409 {
		t.Errorf("no channel: %d", code)
	}
	if code, b := postForm(t, app.URL+"/me/memories", "sess=other", "action=on&at=07:30&tz=Europe/Rome&push=1"); code != 204 {
		t.Fatalf("on: %d %s", code, b)
	}
	p, ok, _ := st.MemoryPref(ctx, mustUUID(otherID))
	if !ok || p.At != "07:30" || p.TZ != "Europe/Rome" || !p.Push || p.Email {
		t.Fatalf("saved: %+v", p)
	}
	_, page := do(t, "GET", app.URL+"/", "sess=other", nil)
	for _, want := range []string{"Every day at 07:30", `value="07:30"`, `value="Europe/Rome"`, "A notification needs a device"} {
		if !strings.Contains(page, want) {
			t.Errorf("the card lacks %q", want)
		}
	}
	if code, _ := postForm(t, app.URL+"/me/memories", "sess=other", "action=off"); code != 204 {
		t.Fatalf("off: %d", code)
	}
	if _, ok, _ := st.MemoryPref(ctx, mustUUID(otherID)); ok {
		t.Error("turned off")
	}
	if code, _ := postForm(t, app.URL+"/me/memories", "sess=other", "action=off"); code != 409 {
		t.Errorf("turning off what is off: %d", code)
	}
}

func TestTheOnThisDayCardIsNotOfferedWhenNothingCanSendIt(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st)) // no push, no email
	if _, page := do(t, "GET", app.URL+"/", "sess=other", nil); strings.Contains(page, "memoriescard") {
		t.Error("no card without push or email")
	}
	if code, _ := postForm(t, app.URL+"/me/memories", "sess=other", "action=on&at=07:30&push=1"); code != 409 {
		t.Errorf("turning it on anyway: %d", code)
	}
}

func TestSendToSomeoneShowsTheDaysPhotosAndHandsTheTickedOnesOver(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withMemories(t, &st))
	if code, _ := do(t, "GET", app.URL+"/memories/2026-11-16", "", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/memories/not-a-day", "sess=other", nil); code != 404 {
		t.Errorf("not a day: %d", code)
	}
	code, page := do(t, "GET", app.URL+"/memories/2026-11-16", "sess=other", nil)
	for _, want := range []string{"6 years ago", "memories/2026-11-16/photo/" + notMyPhoto, `value="club"`, "Hand over the ticked photos"} {
		if code != 200 || !strings.Contains(page, want) {
			t.Fatalf("the page lacks %q: %d", want, code)
		}
	}
	if _, empty := do(t, "GET", app.URL+"/memories/2026-01-01", "sess=other", nil); !strings.Contains(empty, "no memories for this day") {
		t.Error("a day with none says so")
	}
	// the pictures are the person's own, and only those of the day's memories
	if code, _ := do(t, "GET", app.URL+"/memories/2026-11-16/photo/"+notMyPhoto+"?size=large", "sess=other", nil); code != 200 {
		t.Errorf("the memory's photo: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/memories/2026-11-16/photo/"+myPhoto, "sess=other", nil); code != 404 {
		t.Errorf("a photo that is not in a memory: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/memories/2026-11-16/photo/"+notMyPhoto, "", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}

	buttonTagged.Clear()
	if code, b := postForm(t, app.URL+"/memories/send", "sess=other", "service=club&photo="+notMyPhoto); code != 204 {
		t.Fatalf("hand over: %d %s", code, b)
	}
	if v, _ := buttonTagged.Load(notMyPhoto); v != "on" {
		t.Errorf("tagged: %v", v)
	}
	if code, _ := postForm(t, app.URL+"/memories/send", "sess=other", "service=club&photo="+myPhoto); code != 409 {
		t.Errorf("a photo that is not theirs: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/memories/send", "", "service=club&photo="+notMyPhoto); code != 401 {
		t.Errorf("signed out: %d", code)
	}
}
