package web_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/store"
)

func lane(t *testing.T, body, sender string) (state, paused, label string) {
	t.Helper()
	re := regexp.MustCompile(`(?s)<div class="lane" data-lane data-sender="` + regexp.QuoteMeta(sender) + `" data-state="([a-z]*)" data-paused="([a-z]*)">.*?data-lane-state>([^<]*)<`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no lane for %q in the page", sender)
	}
	return m[1], m[2], m[3]
}

func TestTheLiveSceneHasALanePerSenderAndSaysWhatEachIsDoing(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))

	// the owner sees the stage of their service with the one sender who joined
	owner := get(t, app.URL, "sess=family")
	has(t, owner, `<h2>Live `, `id="scene"`, `data-service="club"`, `hx-get="fragments/scene"`, `hx-trigger="scene:refresh from:body"`, "arrived while you watch")
	if state, _, label := lane(t, owner, "Other"); state != "flow" || label != "Flowing" {
		t.Errorf("a healthy lane: %q %q", state, label)
	}
	// the sender sees only their own lane
	sender := get(t, app.URL, "sess=other")
	if state, _, _ := lane(t, sender, "Other"); state != "flow" {
		t.Errorf("the sender's own lane: %q", state)
	}
	if n := strings.Count(sender, `data-lane `); n != 1 {
		t.Errorf("a sender sees only their own lane, found %d", n)
	}

	// the owner pauses the service: every lane says so, and who paused it
	postForm(t, app.URL+"/services/pause", "sess=family", "service=club&paused=1")
	for _, who := range []string{"sess=family", "sess=other"} {
		if state, by, label := lane(t, get(t, app.URL, who), "Other"); state != "paused" || by != "service" || label != "Paused by the owner" {
			t.Errorf("%s: owner's pause shown as %q/%q/%q", who, state, by, label)
		}
	}
	postForm(t, app.URL+"/services/pause", "sess=family", "service=club&paused=0")

	// the sender pauses themselves
	postForm(t, app.URL+"/pause", "sess=other", "service=club&paused=1")
	if state, by, label := lane(t, get(t, app.URL, "sess=family"), "Other"); state != "paused" || by != "sender" || label != "Paused by the sender" {
		t.Errorf("the sender's pause: %q/%q/%q", state, by, label)
	}
	postForm(t, app.URL+"/pause", "sess=other", "service=club&paused=0")

	// the sender reaches the limit: the owner sees the lane held
	postForm(t, app.URL+"/services/limits", "sess=family", "service=club&photos=1")
	links, _ := st.Links(context.Background())
	st.AddHandover(context.Background(), links[0].ID, 1)
	if state, _, label := lane(t, get(t, app.URL, "sess=family"), "Other"); state != "capped" || !strings.HasPrefix(label, "Daily limit reached") {
		t.Errorf("the limit: %q %q", state, label)
	}
	postForm(t, app.URL+"/services/limits", "sess=family", "service=club&photos=&mb=")

	// the sender's key was deleted: the owner sees a cut lane (the sender's own page has nothing to send through)
	st.SetLinkState(context.Background(), links[0].ID, store.StateBroken, "Other's API key was deleted.")
	postForm(t, app.URL+"/services/limits", "sess=family", "service=club&photos=&mb=") // makes the app reload its state
	if state, _, label := lane(t, get(t, app.URL, "sess=family"), "Other"); state != "broken" || label != "Connection lost" {
		t.Errorf("a lost connection: %q %q", state, label)
	}
}

func TestEveryLaneStateHasWordsAndTheSceneHasWhatScreenReadersNeed(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	owner := get(t, app.URL, "sess=family")
	// the page works without script or motion: lane words, a counter and a log are all in the markup
	has(t, owner, "data-lane-state", `data-arrived data-odo="arrived:club">0<`, "data-log", `aria-label="What just happened"`, "Nothing yet. What happens next shows here.")
	// what is read aloud, and the notice for when the stream is down (hidden until the script says so)
	has(t, owner, `role="status" aria-live="polite" data-announce`, `data-offline hidden`, "Live updates are off for now")
	// a token is never part of the markup: it is made by the script for a real event, so nothing moves by itself
	lacks(t, owner, `class="token`, "animation:", "style=")
}

func TestAReceiverFromTheConfigurationSeesTheSendersAsLanes(t *testing.T) {
	app, _ := setup(t)
	family := get(t, app.URL, "sess=family") // receives from the configured service that alice sends to
	has(t, family, `data-service="family"`, "into your library")
	if state, _, _ := lane(t, family, "alice"); state != "flow" {
		t.Errorf("alice's lane: %q", state)
	}
	alice := get(t, app.URL, "sess=alice")
	has(t, alice, `data-service="family"`, "1 receiver receiving")
	// someone who takes part in nothing has no scene
	lacks(t, get(t, app.URL, "sess=admin"), `id="scene"`, "<h2>Live ")
}

func TestTheSceneCanBeAskedForAgain(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	if code, _ := do(t, "GET", app.URL+"/fragments/scene", "", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/fragments/scene", "sess=admin", nil); code != 403 {
		t.Errorf("someone who takes part in nothing: %d", code)
	}
	code, body := do(t, "GET", app.URL+"/fragments/scene", "sess=family", nil)
	if code != 200 || !strings.HasPrefix(strings.TrimSpace(body), `<section id="scene"`) || !strings.Contains(body, `data-service="club"`) {
		t.Errorf("the scene fragment: %d %.200s", code, body)
	}
}
