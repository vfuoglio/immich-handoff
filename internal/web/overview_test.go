package web_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/store"
)

func has(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("the page lacks %q", w)
		}
	}
}

func lacks(t *testing.T, body string, nope ...string) {
	t.Helper()
	for _, w := range nope {
		if strings.Contains(body, w) {
			t.Errorf("the page must not contain %q", w)
		}
	}
}

func TestTheOverviewLeadsWithWhatNeedsThePerson(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	ctx := context.Background()

	// nothing wrong: a greeting, an all-clear, no strip of cards
	owner := get(t, app.URL, "sess=family")
	has(t, owner, "<h1>Hello, Family. <em>Nothing needs you.</em></h1>", "Nothing needs you right now.", "All clear", `Needs you <span class="count-badge">0</span>`)

	// a request to answer, from someone who wants to send to the owner's service
	if _, err := st.AddRequest(ctx, store.Request{Service: "club", UserID: uuid.New(), Name: "Dana", Email: "dana@x.test"}); err != nil {
		t.Fatal(err)
	}
	owner = get(t, app.URL, "sess=family")
	has(t, owner, "<em>One thing needs you.</em>", "Dana wants to send to club", "dana@x.test", "Approve", "Decline", `class="count-badge">1<`)
	lacks(t, owner, "All clear")
	if n := strings.Count(owner, ">Approve<"); n != 1 {
		t.Errorf("a request is answered in one place, found %d Approve buttons", n)
	}

	// the owner's own service lost its connection: that is listed before the request to answer
	svcs, _ := st.Services(ctx)
	st.SetServiceState(ctx, svcs[0].ID, store.StateBroken, "The receiver key was deleted.")
	postForm(t, app.URL+"/services/limits", "sess=family", "service=club&photos=&mb=") // any change makes the app reload its state
	owner = get(t, app.URL, "sess=family")
	has(t, owner, "Disconnected", ">Reconnect<")
	if i, j := strings.Index(owner, "Disconnected"), strings.Index(owner, "wants to send to"); i < 0 || j < 0 || i > j {
		t.Error("a lost connection is listed before a request")
	}
	st.SetServiceState(ctx, svcs[0].ID, store.StateActive, "")
	postForm(t, app.URL+"/services/limits", "sess=family", "service=club&photos=&mb=")

	// a lost connection comes first, with its way out
	links, _ := st.Links(ctx)
	st.SetLinkState(ctx, links[0].ID, store.StateBroken, "Other's API key was deleted.")
	sender := get(t, app.URL, "sess=other")
	has(t, sender, "Connection lost", "Other&#39;s API key was deleted.", ">Clean up<")
	if i, j := strings.Index(sender, "Connection lost"), strings.Index(sender, "You send to"); i < 0 || j < 0 || i > j {
		t.Error("what needs the person comes before the list of services")
	}
}

func TestPausesAndLimitsAppearInTheStripWithTheirAction(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	postForm(t, app.URL+"/services/pause", "sess=family", "service=club&paused=1")
	postForm(t, app.URL+"/pause", "sess=other", "service=club&paused=1")
	owner := get(t, app.URL, "sess=family")
	has(t, owner, "You paused this service", ">Resume service<")
	sender := get(t, app.URL, "sess=other")
	has(t, sender, "You paused sending", ">Resume sending<")

	// when the owner paused it, the sender is told and has nothing to press
	postForm(t, app.URL+"/pause", "sess=other", "service=club&paused=0")
	sender = get(t, app.URL, "sess=other")
	has(t, sender, "The owner paused this service.")
	lacks(t, sender, ">Resume sending<")

	// a sender at the limit: told on their own page, and the owner is told who
	postForm(t, app.URL+"/services/pause", "sess=family", "service=club&paused=0")
	postForm(t, app.URL+"/services/limits", "sess=family", "service=club&photos=2")
	links, _ := st.Links(context.Background())
	st.AddHandover(context.Background(), links[0].ID, 1)
	st.AddHandover(context.Background(), links[0].ID, 1)
	owner = get(t, app.URL, "sess=family")
	has(t, owner, "Other reached the daily limit for club")
}

func TestTheNumbersFragmentCountsWaitingInboxAndTheWeek(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	links, _ := st.Links(context.Background())
	for i := 0; i < 3; i++ {
		st.AddHandover(context.Background(), links[0].ID, 1<<20)
	}
	if code, _ := do(t, "GET", app.URL+"/fragments/numbers", "", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/fragments/numbers", "sess=admin", nil); code != 403 {
		t.Errorf("someone who takes part in nothing: %d", code)
	}
	// the sender: waiting counts (the fake Immich lists two tagged photos), no inbox, three this week
	_, sender := do(t, "GET", app.URL+"/fragments/numbers", "sess=other", nil)
	has(t, sender, "Handed over, last 7 days", "Waiting to go", ">3<", ">2<")
	lacks(t, sender, "In your inbox")
	// the owner of the service receives: an inbox, and the same three arrived
	_, owner := do(t, "GET", app.URL+"/fragments/numbers", "sess=family", nil)
	has(t, owner, "In your inbox", ">3<")
	lacks(t, owner, "Waiting to go")
	// the chart: seven points, seven labels in a seven-column list, and a label a screen reader can read
	for _, body := range []string{sender, owner} {
		if n := strings.Count(body, "<li><b>"); n != 7 {
			t.Errorf("seven day labels expected, found %d", n)
		}
		has(t, body, `role="img"`, "Photos handed over per day, oldest first:", `class="spark-line"`)
		if m := regexp.MustCompile(`class="spark-line" d="([^"]+)"`).FindStringSubmatch(body); m == nil || strings.Count(m[1], "L") != 6 {
			t.Errorf("the line must have seven points: %v", m)
		}
	}
	// the dashboard itself loads them lazily and keeps them fresh from the live events
	page := get(t, app.URL, "sess=other")
	has(t, page, `id="numbers"`, `hx-get="fragments/numbers"`, `hx-trigger="load, handoff:run from:body"`)
}

func TestTheFirstRunGuideFollowsTheStepsAndGoesAwayWhenDone(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	// the "family" user (Tags on in the fake Immich) owns a service but nothing has been handed over: 2 of 3 steps
	g := get(t, app.URL, "sess=family")
	has(t, g, "Get started", "Hand over your first photo", "Hide this guide", `class="ring r2"`, "2/3")
	// hiding it is remembered
	if code, _ := postForm(t, app.URL+"/guide/dismiss", "sess=family", ""); code != 204 {
		t.Fatalf("dismiss: %d", code)
	}
	lacks(t, get(t, app.URL, "sess=family"), "Get started")
	if code, _ := postForm(t, app.URL+"/guide/dismiss", "", ""); code != 401 {
		t.Errorf("signed out: %d", code)
	}

	// finishing it (a first hand-over) hides it for good, without anyone pressing anything
	var st2 *store.Store
	app2, _ := setup(t, withClub(t, &st2))
	links, _ := st2.Links(context.Background())
	st2.AddHandover(context.Background(), links[0].ID, 1)
	lacks(t, get(t, app2.URL, "sess=family"), "Get started")
	if done, _ := st2.GuideDone(context.Background(), uuid.MustParse(familyID)); !done {
		t.Error("a finished guide must be remembered, so it never comes back after a quiet week")
	}

	// someone with no service yet (the administrator, once they switch Tags on) is told what to do first
	if code, body := postForm(t, app.URL+"/preferences/tags", "sess=admin", ""); code != 204 && code != 200 {
		t.Fatalf("turning Tags on: %d %s", code, body)
	}
	has(t, get(t, app.URL, "sess=admin"), "Get started", `class="ring r1"`, "Join a service, or offer one of your own")

	// while Tags are off the whole page is switched off, and so is the guide
	lacks(t, get(t, app.URL, "sess=other"), "Get started")
}

// An install with only fixed accounts (no data file) has no memberships to remember, but the page still opens with a
// greeting, the numbers and the live scene, and without the guide.
func TestAnInstallWithOnlyFixedAccountsHasTheOverviewToo(t *testing.T) {
	app, _ := setup(t)
	body := get(t, app.URL, "sess=alice")
	has(t, body, "<h1>Hello, Alice. <em>Nothing needs you.</em></h1>", "Nothing needs you right now.", `id="numbers"`, `id="scene"`)
	lacks(t, body, "Get started", "<h1></h1>")
}

// The dashboard is three views of one page, chosen from the bar; the other pages have no tabs.
func TestTheDashboardHasThreeViewsAndTabsToSwitchThem(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	body := get(t, app.URL, "sess=family")
	has(t, body, `<nav class="tabs" aria-label="Views">`, `href="#overview" data-view="overview"`, `href="#services" data-view="services"`, `href="#activity" data-view="activity"`,
		`<section class="view" id="v-overview"`, `<section class="view" id="v-services"`, `<section class="view" id="v-activity"`)
	// what belongs in each view
	view := func(id string) string {
		i := strings.Index(body, `id="`+id+`"`)
		j := strings.Index(body[i+1:], `<section class="view"`)
		if j < 0 {
			return body[i:]
		}
		return body[i : i+1+j]
	}
	has(t, view("v-overview"), "Needs you", "This week", `id="scene"`)
	lacks(t, view("v-overview"), "Save limits", "Last run")
	has(t, view("v-services"), "You offer", "Save limits", "Offer a service of your own")
	has(t, view("v-activity"), "What <em>happened</em>", "Last run", "Sync now")
	// without script everything is shown; with it the early script marks the page, so only the chosen view shows
	_, js := do(t, "GET", app.URL+"/static/theme.js", "", nil)
	has(t, js, `classList.add("js")`, "overview", "services", "activity")
	// pages that are not the dashboard have no tabs
	_, admin := do(t, "GET", app.URL+"/admin", "sess=admin", nil)
	lacks(t, admin, `<nav class="tabs"`)
}

func TestTheHeadingSaysHowManyThingsNeedThePerson(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	ctx := context.Background()
	for i, want := range []string{"One thing needs you.", "Two things need you.", "Three things need you."} {
		if _, err := st.AddRequest(ctx, store.Request{Service: "club", UserID: uuid.New(), Name: "Person", Email: "p" + string(rune('a'+i)) + "@x.test"}); err != nil {
			t.Fatal(err)
		}
		has(t, get(t, app.URL, "sess=family"), "<em>"+want+"</em>")
	}
}

// The bar carries the search and the chime on the dashboard, and the effects have what they need in the markup.
func TestTheDashboardHasTheSearchTheChimeAndWhatTheEffectsNeed(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	body := get(t, app.URL, "sess=family")
	has(t, body, `id="palette"`, "Ctrl K", `<dialog id="cmd" class="cmd"`, `id="cmd-q"`, `role="listbox"`, `id="sound"`, `aria-pressed="true"`, `id="glow"`)
	// the numbers that roll are marked, with a key so a refreshed number rolls from what it showed before
	_, nums := do(t, "GET", app.URL+"/fragments/numbers", "sess=family", nil)
	has(t, nums, `data-odo="week"`, `data-odo="inbox"`)
	_, count := do(t, "GET", app.URL+"/fragments/count?service=club&role=receiver&sum=sum-own-0", "sess=family", nil)
	has(t, count, `data-odo="sum-own-0"`)
	has(t, body, `data-odo="arrived:club"`)
	// the other pages have neither the search nor the chime
	_, admin := do(t, "GET", app.URL+"/admin", "sess=admin", nil)
	lacks(t, admin, `id="palette"`, `id="sound"`, `id="cmd"`)
}
