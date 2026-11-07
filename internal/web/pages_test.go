package web_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

// withSecret adds an invitation-only service "secret" (token "tok123") offered by the "family" user.
func withSecret(t *testing.T, st **store.Store) func(*web.Server) {
	club := withClub(t, st)
	return func(srv *web.Server) {
		club(srv)
		if _, err := (*st).AddService(context.Background(), store.Service{Name: "secret", Tag: "secret-tag", InboxTag: "inbox/secret", Refused: "dup", ExpiryDays: 30,
			OwnerID: uuid.MustParse(familyID), OwnerName: "Family", OwnerEmail: "family@x.test", KeyID: uuid.New(), Key: "k-family",
			Policy: store.PolicyOpen, Visibility: store.VisInvite, Invite: "tok123"}); err != nil {
			t.Fatal(err)
		}
		srv.Hub.Reload(context.Background())
	}
}

func TestPagesThatStopAPersonAreOnePlainPageNotACard(t *testing.T) {
	app, _ := setup(t)
	code, body := do(t, "GET", app.URL+"/", "", nil)
	has(t, body, `<section class="notice-page">`, `class="notice-icon"`, "<h1>Sign in to Immich first</h1>", `class="lead"`)
	lacks(t, body, `<nav class="tabs"`, `id="palette"`)
	if code != 401 {
		t.Errorf("signed out: %d", code)
	}
	_, bad := do(t, "GET", app.URL+"/invite/not-a-token", "sess=alice", nil)
	has(t, bad, `<section class="notice-page bad">`, "<h1>Invitation</h1>", "not valid any more")
}

func TestTheInvitationPageIsTheInvitationAndNothingElse(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withSecret(t, &st))
	// the administrator has no part in it and may switch Tags on, so the page is not switched off
	postForm(t, app.URL+"/preferences/tags", "sess=admin", "")
	code, body := do(t, "GET", app.URL+"/invite/tok123", "sess=admin", nil)
	if code != 200 {
		t.Fatalf("invitation: %d", code)
	}
	has(t, body, `<base href="../">`, `<section class="invite-hero">`, "You are invited to <em>secret</em>", "Family offers this service.", `class="invite-card"`,
		"Not now: open your own page", `hx-post="join"`)
	lacks(t, body, `<nav class="tabs"`, `id="v-overview"`, "Needs you", "Get started")
}

func TestTheTagsNoticeIsTheLoudestThingOnThePage(t *testing.T) {
	app, _ := setup(t)
	_, body := do(t, "GET", app.URL+"/", "sess=alice", nil) // Tags are off for alice in the fake Immich
	has(t, body, `class="card notice tags" id="tags-notice"`, `<span class="chip warn">Tags are off</span>`, "Tags are switched off in your Immich account", ">Turn on Tags<")
	// and the page under it is switched off until they are on
	has(t, body, `id="gated"`, `class="gated"`)
}

func TestTheAdministratorsPageSummarisesThenListsServicesAsRows(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	_, body := do(t, "GET", app.URL+"/admin", "sess=admin", nil)
	has(t, body, "Everything on this <em>server</em>", `class="kpis admin-kpis"`, "Services", "Senders", "Need attention", ">Check now<")
	// two services (the configured one and the one a person made), two senders (alice, and the one who joined), nothing needs attention
	for _, want := range []string{"Services</span><span class=\"num\">2<", "Senders</span><span class=\"num\">2<", "Need attention</span><span class=\"num\">0<"} {
		if !strings.Contains(body, want) {
			t.Errorf("the summary lacks %q", want)
		}
	}
	if n := strings.Count(body, `<details class="svc card" name="adm"`); n != 2 {
		t.Errorf("each service is a row: %d", n)
	}
	has(t, body, `class="member-list"`, `class="danger-zone"`, ">Remove this service<")
	// a pause or a lost connection is counted
	postForm(t, app.URL+"/services/pause", "sess=family", "service=club&paused=1")
	_, body = do(t, "GET", app.URL+"/admin", "sess=admin", nil)
	if !strings.Contains(body, "Need attention</span><span class=\"num\">1<") {
		t.Error("a paused service needs attention")
	}
	has(t, body, `<ol class="timeline">`, `class="dot warn"`) // the pause is in the events, as a timeline with a coloured dot
	// the configured service has no Remove button, and its row has no danger zone
	row := regexp.MustCompile(`(?s)<details class="svc card" name="adm".*?</details>`).FindAllString(body, -1)
	if len(row) != 2 {
		t.Fatalf("rows: %d", len(row))
	}
	for _, r := range row {
		if strings.Contains(r, "From configuration") && strings.Contains(r, "danger-zone") {
			t.Error("a service from the configuration cannot be removed here")
		}
	}
}

func TestTheOfferFormIsAGridAndEveryLabelBelongsToAField(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withClub(t, &st))
	_, body := do(t, "GET", app.URL+"/", "sess=family", nil)
	form := regexp.MustCompile(`(?s)<form class="card offer-form".*?</form>`).FindString(body)
	if form == "" {
		t.Fatal("no offer form")
	}
	has(t, form, `class="form-grid"`, `<details class="more">`, `class="row form-actions"`, "Create service")
	for _, m := range regexp.MustCompile(`<label for="([^"]+)"`).FindAllStringSubmatch(form, -1) {
		if !strings.Contains(form, `id="`+m[1]+`"`) {
			t.Errorf("the label for %q points at no field", m[1])
		}
	}
	// no field is missing a label
	for _, m := range regexp.MustCompile(`<(?:input|select)[^>]*\bid="([^"]+)"`).FindAllStringSubmatch(form, -1) {
		if !strings.Contains(form, `for="`+m[1]+`"`) {
			t.Errorf("the field %q has no label", m[1])
		}
	}
}
