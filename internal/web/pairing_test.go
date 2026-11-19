package web_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

// seeOther asks without following redirects and says where the answer points.
func seeOther(t *testing.T, url, cookie string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	return res.StatusCode, res.Header.Get("Location")
}

const (
	noraID = "eeeeeeee-0000-4000-8000-000000000001"
	leoID  = "eeeeeeee-0000-4000-8000-000000000002"
)

// withPairings is the page with a hub that can make pools and keys: "family" offers, "alice" is invited, "other" is a stranger.
func withPairings(t *testing.T, st **store.Store) func(*web.Server) {
	club := withClub(t, st)
	return func(srv *web.Server) {
		club(srv)
		srv.Hub.Options = hub.Options{InboxTag: "inbox", RefusedTag: "dup", SentTag: "handed-over", ExpiryDays: 30}
	}
}

func onlyPairing(t *testing.T, st *store.Store) store.Pairing {
	t.Helper()
	all, err := st.Pairings(context.Background())
	if err != nil || len(all) != 1 {
		t.Fatalf("one pairing expected: %+v %v", all, err)
	}
	return all[0]
}

func TestSharingPhotosWithSomeoneIsOfferedFromTheOverviewAndSaidYesToOnItsOwnPage(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withPairings(t, &st))
	ctx := context.Background()

	if code, _ := do(t, "GET", app.URL+"/fragments/pair-form", "", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/pairings", "", "email=alice@x.test"); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if _, page := do(t, "GET", app.URL+"/", "sess=family", nil); !strings.Contains(page, "Share photos with someone") || !strings.Contains(page, "Offer to share photos") {
		t.Fatal("the card is on the Overview")
	}
	_, form := do(t, "GET", app.URL+"/fragments/pair-form", "sess=family", nil)
	for _, want := range []string{"Nora", "Leo", `name="email"`, `name="person"`, "Nothing is sent until they say yes"} {
		if !strings.Contains(form, want) {
			t.Errorf("the form lacks %q", want)
		}
	}

	// refused offers say why, and make nothing
	if code, b := postForm(t, app.URL+"/pairings", "sess=family", "email=nobody@x.test&person="+noraID); code != 409 {
		t.Errorf("nobody of that email: %d %s", code, b)
	}
	if code, b := postForm(t, app.URL+"/pairings", "sess=family", "email=alice@x.test"); code != 409 || !strings.Contains(b, "between one and ten named people") {
		t.Errorf("nobody chosen: %d %s", code, b)
	}
	if code, b := postForm(t, app.URL+"/pairings", "sess=family", "email=family@x.test&person="+noraID); code != 409 || !strings.Contains(b, "yourself") {
		t.Errorf("with yourself: %d %s", code, b)
	}
	if all, _ := st.Pairings(ctx); len(all) != 0 {
		t.Fatalf("a refused offer makes nothing: %+v", all)
	}

	if code, b := postForm(t, app.URL+"/pairings", "sess=family", "email=alice@x.test&person="+noraID); code != 204 {
		t.Fatalf("the offer: %d %s", code, b)
	}
	p := onlyPairing(t, st)
	if p.State != store.PairingInvited || p.AID.String() != familyID || p.BID.String() != aliceID {
		t.Fatalf("%+v", p)
	}

	// the starter sees the offer with a link to give; the pool's generated name is shown nowhere
	_, mine := do(t, "GET", app.URL+"/", "sess=family", nil)
	for _, want := range []string{"You and Alice", "Waiting for a yes", `data-link="pair/` + p.ID + `"`, "You send photos of Nora.", "Cancel the offer"} {
		if !strings.Contains(mine, want) {
			t.Errorf("the starter's card lacks %q", want)
		}
	}
	if strings.Contains(mine, p.Service) {
		t.Errorf("the pool's generated name %q is shown to the starter", p.Service)
	}

	// the invited person sees the offer and opens its page; the generic invitation leads there too
	_, theirs := do(t, "GET", app.URL+"/", "sess=alice", nil)
	for _, want := range []string{"You and Family", "Family offered to share photos with you, both ways.", `href="pair/` + p.ID + `"`, "Family sends photos of Nora."} {
		if !strings.Contains(theirs, want) {
			t.Errorf("the invited person's card lacks %q", want)
		}
	}
	svcs, _ := st.Services(ctx)
	var token string
	for _, s := range svcs {
		if s.Name == p.Service {
			token = s.Invite
		}
	}
	if code, to := seeOther(t, app.URL+"/invite/"+token, "sess=alice"); code != 303 || to != "../pair/"+p.ID {
		t.Errorf("the generic invitation of a pairing's pool leads to the pairing's page: %d %q", code, to)
	}
	if code, _ := do(t, "GET", app.URL+"/pair/"+p.ID, "", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, _ := do(t, "GET", app.URL+"/pair/"+p.ID, "sess=other", nil); code != 404 {
		t.Errorf("a stranger cannot see it: %d", code)
	}
	if code, to := seeOther(t, app.URL+"/pair/"+p.ID, "sess=family"); code != 303 || to != "../" {
		t.Errorf("the starter has the card: %d %q", code, to)
	}
	code, page := do(t, "GET", app.URL+"/pair/"+p.ID, "sess=alice", nil)
	if code != 200 {
		t.Fatalf("the consent page: %d", code)
	}
	for _, want := range []string{"Family would like to share photos with you", "photos of Nora into your library", "Only photos that arrive after you say yes", "nothing is ever deleted from anyone", "Leo", `name="person"`, "Yes, share with Family", "No thanks"} {
		if !strings.Contains(page, want) {
			t.Errorf("the consent page lacks %q", want)
		}
	}
	if strings.Contains(page, `Join the pool`) {
		t.Error("the page is the pairing's own, not the generic pool card")
	}

	// saying yes: only the invited person, with a person of their own
	if code, _ := postForm(t, app.URL+"/pairings/accept", "sess=other", "pairing="+p.ID+"&person="+leoID); code != 409 {
		t.Errorf("a stranger cannot say yes: %d", code)
	}
	if code, b := postForm(t, app.URL+"/pairings/accept", "sess=alice", "pairing="+p.ID); code != 409 || !strings.Contains(b, "between one and ten named people") {
		t.Errorf("a yes needs someone to send back: %d %s", code, b)
	}
	if code, b := postForm(t, app.URL+"/pairings/accept", "sess=alice", "pairing="+p.ID+"&person="+leoID); code != 204 {
		t.Fatalf("the yes: %d %s", code, b)
	}
	if got, _ := st.PairingByID(ctx, p.ID); got.State != store.PairingActive {
		t.Fatalf("%+v", got)
	}
	if rs, _ := st.RulesOfPairing(ctx, p.ID); len(rs) != 2 {
		t.Errorf("a rule for each side: %+v", rs)
	}
	if code, to := seeOther(t, app.URL+"/pair/"+p.ID, "sess=alice"); code != 303 || to != "../" {
		t.Errorf("once said, the page leads home: %d %q", code, to)
	}
	for who, want := range map[string][]string{
		"sess=family": {"You and Alice", "Sharing", "You send photos of Nora.", "Alice sends photos of Leo.", "Pause sharing", "Stop sharing", "Suggest older photos of Nora"},
		"sess=alice":  {"You and Family", "You send photos of Leo.", "Family sends photos of Nora.", "Pause sharing", "Stop sharing", "Suggest older photos of Leo"},
	} {
		_, pg := do(t, "GET", app.URL+"/", who, nil)
		for _, w := range want {
			if !strings.Contains(pg, w) {
				t.Errorf("%s: the card lacks %q", who, w)
			}
		}
		if strings.Contains(pg, p.Service) {
			t.Errorf("%s: the pool's generated name is shown", who)
		}
	}

	// the switch to send by themselves: its confirmation names the photos and who they go to, it is each person's own, and it can be undone
	_, pg0 := do(t, "GET", app.URL+"/", "sess=family", nil)
	for _, w := range []string{"Send by themselves from now on", "photos of Nora go to Alice without asking you", "a wrong photo can reach Alice", "switch this off at any time"} {
		if !strings.Contains(pg0, w) {
			t.Errorf("the card's switch lacks %q", w)
		}
	}
	if code, _ := postForm(t, app.URL+"/pairings/auto", "sess=other", "pairing="+p.ID+"&on=1"); code != 409 {
		t.Errorf("a stranger cannot switch it: %d", code)
	}
	if code, b := postForm(t, app.URL+"/pairings/auto", "sess=family", "pairing="+p.ID+"&on=1"); code != 204 {
		t.Fatalf("switching on: %d %s", code, b)
	}
	_, pg1 := do(t, "GET", app.URL+"/", "sess=family", nil)
	_, pg2 := do(t, "GET", app.URL+"/", "sess=alice", nil)
	if !strings.Contains(pg1, "Your photos of Nora go to Alice by themselves") || !strings.Contains(pg1, "Ask me first again") {
		t.Errorf("the starter's card says it sends by itself and offers the way back: %s", pg1)
	}
	if strings.Contains(pg2, "go to Family by themselves") || !strings.Contains(pg2, "Send by themselves from now on") {
		t.Errorf("the other person's side is still their own choice")
	}
	if code, _ := postForm(t, app.URL+"/pairings/auto", "sess=family", "pairing="+p.ID+"&on=0"); code != 204 {
		t.Errorf("switching off: %d", code)
	}

	// a suggestion says who it is for, not the pool
	rules, _ := st.RulesOfPairing(ctx, p.ID)
	var famRule store.Rule
	for _, r := range rules {
		if r.OwnerID.String() == familyID {
			famRule = r
		}
	}
	if err := st.PutRulePhoto(ctx, famRule.ID, "aaaaaaaa-1111-4111-8111-000000000001", store.RuleSuggested, "Nora is in it"); err != nil {
		t.Fatal(err)
	}
	_, pg := do(t, "GET", app.URL+"/", "sess=family", nil)
	if !strings.Contains(pg, "1 new photo for Alice") || strings.Contains(pg, "for "+p.Service) {
		t.Errorf("the suggestion is for the other person: %s", pg)
	}
	if code, rv := do(t, "GET", app.URL+"/rules/review/"+famRule.ID, "sess=family", nil); code != 200 || !strings.Contains(rv, "Photos for Alice") {
		t.Errorf("the review page too: %d", code)
	}

	// nothing of it can be changed by hand
	if code, b := postForm(t, app.URL+"/rules/delete", "sess=family", "rule="+famRule.ID); code != 409 || !strings.Contains(b, "part of your photo sharing") {
		t.Errorf("a rule is locked: %d %s", code, b)
	}
	if code, b := postForm(t, app.URL+"/leave", "sess=alice", "service="+p.Service); code != 409 || !strings.Contains(b, "part of your photo sharing") {
		t.Errorf("the pool is locked: %d %s", code, b)
	}

	// either person can pause and resume
	if code, b := postForm(t, app.URL+"/pairings/pause", "sess=alice", "pairing="+p.ID+"&paused=1"); code != 204 {
		t.Fatalf("pause: %d %s", code, b)
	}
	if _, pg := do(t, "GET", app.URL+"/", "sess=family", nil); !strings.Contains(pg, "Resume sharing") {
		t.Error("the other person's card offers to resume it")
	}
	if code, _ := postForm(t, app.URL+"/pairings/pause", "sess=family", "pairing="+p.ID+"&paused=0"); code != 204 {
		t.Errorf("resume: %d", code)
	}

	// the other person stops it: their side is clean, the starter's card says why and offers to remove it
	if code, b := postForm(t, app.URL+"/pairings/stop", "sess=alice", "pairing="+p.ID); code != 204 {
		t.Fatalf("stop: %d %s", code, b)
	}
	_, pg = do(t, "GET", app.URL+"/", "sess=family", nil)
	for _, want := range []string{"Stopped", "Alice stopped sharing photos with you", "Remove this sharing"} {
		if !strings.Contains(pg, want) {
			t.Errorf("the starter's card after a stop lacks %q", want)
		}
	}
	if strings.Contains(pg, "Pause sharing") {
		t.Error("a stopped sharing cannot be paused")
	}
	if code, b := postForm(t, app.URL+"/pairings/stop", "sess=family", "pairing="+p.ID); code != 204 {
		t.Fatalf("the starter takes it apart: %d %s", code, b)
	}
	if all, _ := st.Pairings(ctx); len(all) != 0 {
		t.Errorf("it is gone: %+v", all)
	}
	if _, pg := do(t, "GET", app.URL+"/", "sess=family", nil); strings.Contains(pg, "You and Alice") {
		t.Error("and so is its card")
	}
}

func TestADeclinedOfferShowsNothingToAcceptAndALapsedOneSaysSo(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withPairings(t, &st))
	ctx := context.Background()
	if code, b := postForm(t, app.URL+"/pairings", "sess=family", "email=alice@x.test&person="+noraID); code != 204 {
		t.Fatalf("%d %s", code, b)
	}
	p := onlyPairing(t, st)
	if code, b := postForm(t, app.URL+"/pairings/stop", "sess=alice", "pairing="+p.ID); code != 204 {
		t.Fatalf("declined: %d %s", code, b)
	}
	code, page := do(t, "GET", app.URL+"/pair/"+p.ID, "sess=alice", nil)
	if code != 200 || !strings.Contains(page, "This sharing has ended") || !strings.Contains(page, "Alice declined the invitation") || strings.Contains(page, "Yes, share with") {
		t.Errorf("nothing to accept after a decline: %d %s", code, page)
	}
	if code, _ := postForm(t, app.URL+"/pairings/accept", "sess=alice", "pairing="+p.ID+"&person="+leoID); code != 409 {
		t.Errorf("it cannot be accepted after: %d", code)
	}
	_, pg := do(t, "GET", app.URL+"/", "sess=family", nil)
	if !strings.Contains(pg, "Alice declined the invitation") || !strings.Contains(pg, "Remove this sharing") {
		t.Error("the starter is told and can remove it")
	}
	if _, err := uuid.Parse(p.ID); err != nil {
		t.Fatal(err)
	}
	_ = ctx
}
