package hub_test

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

var linkRE = regexp.MustCompile(`https://app\.example/a/([A-Za-z0-9_-]{22})`)

func tokens(text string) []string {
	var out []string
	for _, m := range linkRE.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	return out
}

// an approval service owned by Ann; Ann and Bob take mail, Ann also has a phone
func actionSetup(t *testing.T) (*env, *fakeMailer, *fakePushService) {
	t.Helper()
	e := setup(t, false)
	_, m := withPeople(t, e, 150*time.Millisecond)
	_, ps := withPush(t, e, 100*time.Millisecond)
	e.h.AppURL = "https://app.example"
	// the service is made before anyone asks to be told, so what the tests read is only what they cause
	if err := e.h.CreateService(context.Background(), e.ann, ann, hub.CreateParams{Name: "party", Tag: "party", Policy: "approval", Visibility: ""}); err != nil {
		t.Fatal(err)
	}
	optIn(t, e, annID, "Ann", "ann@x.test", "en", store.MailAll)
	optIn(t, e, bobID, "Bob", "bob@x.test", "en", store.MailAll)
	subscribe(t, e, annID, annPhone, "en", store.MailAll)
	return e, m, ps
}

func TestOnlyTheOwnerGetsLinksToAnswerARequestByMailAndOnTheirPhone(t *testing.T) {
	e, m, ps := actionSetup(t)
	ctx := context.Background()
	if err := e.h.Ask(ctx, bob, "party", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "mails", func() bool { return len(m.to("ann@x.test")) >= 1 && len(m.to("bob@x.test")) >= 1 })
	waitFor(t, "ann's phone", func() bool { return len(ps.to(annPhone)) >= 1 })
	time.Sleep(300 * time.Millisecond)

	annMail := m.to("ann@x.test")[0]
	toks := tokens(annMail.Text)
	if len(toks) != 2 || !strings.Contains(annMail.Text, "Approve: https://app.example/a/") || !strings.Contains(annMail.Text, "Decline: https://app.example/a/") {
		t.Fatalf("the owner's mail should carry both answers:\n%s", annMail.Text)
	}
	if !strings.Contains(annMail.HTML, ">Approve</a>") || !strings.Contains(annMail.HTML, ">Decline</a>") {
		t.Errorf("and buttons: %s", annMail.HTML)
	}
	if got := tokens(m.to("bob@x.test")[0].Text); len(got) != 0 {
		t.Errorf("the person who asked must not get the owner's links: %v", got)
	}

	phone := ps.to(annPhone)[0]
	actions, _ := phone.raw["actions"].([]any)
	urls, _ := phone.raw["urls"].(map[string]any)
	if len(actions) != 2 || len(urls) != 2 {
		t.Fatalf("the notification should carry two buttons: %+v", phone.raw)
	}
	if first := actions[0].(map[string]any); first["title"] != "Approve" || !strings.HasPrefix(urls["a0"].(string), "https://app.example/a/") {
		t.Errorf("%+v %+v", first, urls)
	}
	// the mail and the notification share one pair of links
	if !strings.Contains(annMail.Text, strings.TrimPrefix(urls["a0"].(string), "https://app.example/a/")) {
		t.Error("the mail and the phone were given different links for the same decision")
	}
}

func TestApprovingFromTheLinkDoesWhatThePageDoesAndEndsTheOtherAnswer(t *testing.T) {
	e, m, _ := actionSetup(t)
	ctx := context.Background()
	e.h.Ask(ctx, bob, "party", "")
	waitFor(t, "owner's mail", func() bool { return len(m.to("ann@x.test")) >= 1 })
	toks := tokens(m.to("ann@x.test")[0].Text)
	approve, decline := toks[0], toks[1]

	info, ok := e.h.ActionPage(ctx, approve)
	if !ok || info.Kind != hub.ActionApprove || info.Name != "Bob" || info.Service != "party" {
		t.Fatalf("%+v %v", info, ok)
	}
	if reqs, _ := e.st.Requests(ctx); reqs[0].Status != store.StatusPending {
		t.Fatal("looking at the page must change nothing")
	}
	if _, err := e.h.RunAction(ctx, approve); err != nil {
		t.Fatal(err)
	}
	if reqs, _ := e.st.Requests(ctx); reqs[0].Status != store.StatusApproved {
		t.Errorf("%+v", reqs)
	}
	if acts, _ := e.h.Activity(ctx, bobID, 5); len(acts) == 0 || !strings.Contains(en(acts[0].Text), "approved Bob") {
		t.Errorf("the same Activity line the page writes: %+v", acts)
	}
	if _, err := e.h.RunAction(ctx, approve); err != hub.ErrActionInvalid {
		t.Errorf("a link works once: %v", err)
	}
	if _, ok := e.h.ActionPage(ctx, decline); ok {
		t.Error("the other answer to the same request is still offered")
	}
	if _, err := e.h.RunAction(ctx, decline); err != hub.ErrActionInvalid {
		t.Errorf("%v", err)
	}
}

func TestALinkForARequestAlreadyDecidedOnThePageChangesNothing(t *testing.T) {
	e, m, _ := actionSetup(t)
	ctx := context.Background()
	e.h.Ask(ctx, bob, "party", "")
	waitFor(t, "owner's mail", func() bool { return len(m.to("ann@x.test")) >= 1 })
	toks := tokens(m.to("ann@x.test")[0].Text)
	reqs, _ := e.st.Requests(ctx)
	if err := e.h.Decide(ctx, ann, "party", reqs[0].ID, true); err != nil { // Ann approves on the page first
		t.Fatal(err)
	}
	if _, ok := e.h.ActionPage(ctx, toks[1]); ok {
		t.Error("the decline link still shows after the page approved")
	}
	if _, err := e.h.RunAction(ctx, toks[1]); err != hub.ErrAlreadyDecided || !hub.IsUserError(err) {
		t.Errorf("%v", err)
	}
	if reqs, _ := e.st.Requests(ctx); reqs[0].Status != store.StatusApproved {
		t.Errorf("a late decline link overturned the approval: %+v", reqs)
	}
}

func TestPausingGivesTheOwnerALinkToResumeAndASenderALinkToResumeTheirSending(t *testing.T) {
	e, m, _ := actionSetup(t)
	ctx := context.Background()
	if err := e.h.SetServicePaused(ctx, ann, "party", true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "owner's mail", func() bool { return len(m.to("ann@x.test")) >= 1 })
	time.Sleep(300 * time.Millisecond)
	toks := tokens(m.to("ann@x.test")[len(m.to("ann@x.test"))-1].Text)
	if len(toks) != 1 || !strings.Contains(m.to("ann@x.test")[0].Text, "Resume service: ") {
		t.Fatalf("%v\n%s", toks, m.to("ann@x.test")[0].Text)
	}
	if info, ok := e.h.ActionPage(ctx, toks[0]); !ok || info.Kind != hub.ActionResumeService {
		t.Fatalf("%+v %v", info, ok)
	}
	if _, err := e.h.RunAction(ctx, toks[0]); err != nil {
		t.Fatal(err)
	}
	if svc := service(e, "party"); svc.Paused {
		t.Error("the service is still paused")
	}
	if _, ok := e.h.ActionPage(ctx, toks[0]); ok {
		t.Error("a spent link still shows")
	}

}

func TestASenderWhoPausesGetsALinkToResumeTheirOwnSendingAndNobodyElseDoes(t *testing.T) {
	e, m, _ := actionSetup(t)
	ctx := context.Background()
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "club", Tag: "club", Policy: "open", Visibility: ""}); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "club", "", ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond) // let what the joining caused go out first
	annBefore, bobBefore := len(m.to("ann@x.test")), len(m.to("bob@x.test"))
	if err := e.h.SetMemberPaused(ctx, bob, "club", true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bob's mail", func() bool { return len(m.to("bob@x.test")) > bobBefore })
	waitFor(t, "ann's mail", func() bool { return len(m.to("ann@x.test")) > annBefore })
	bobs := m.to("bob@x.test")[len(m.to("bob@x.test"))-1]
	anns := m.to("ann@x.test")[len(m.to("ann@x.test"))-1]
	toks := tokens(bobs.Text)
	if len(toks) != 1 || !strings.Contains(bobs.Text, "Resume sending: ") {
		t.Fatalf("%v\n%s", toks, bobs.Text)
	}
	if len(tokens(anns.Text)) != 0 {
		t.Errorf("the owner must not be offered a way to resume someone else's sending:\n%s", anns.Text)
	}
	if _, err := e.h.RunAction(ctx, toks[0]); err != nil {
		t.Fatal(err)
	}
	links, _ := e.st.Links(ctx)
	for _, l := range links {
		if l.ImmichUserID == bobID && l.Service == "club" && l.Paused {
			t.Error("bob's sending is still paused")
		}
	}
	if _, err := e.h.RunAction(ctx, toks[0]); err != hub.ErrActionInvalid {
		t.Errorf("%v", err)
	}
}

func TestAnExpiredOrUnknownLinkIsRefusedAndNoAddressMeansNoLinks(t *testing.T) {
	e, m, _ := actionSetup(t)
	ctx := context.Background()
	if _, ok := e.h.ActionPage(ctx, "nonsense"); ok {
		t.Error("an unknown link")
	}
	if _, err := e.h.RunAction(ctx, "nonsense"); err != hub.ErrActionInvalid {
		t.Errorf("%v", err)
	}
	e.h.AppURL = "" // no address to build a link from
	e.h.Ask(ctx, bob, "party", "")
	waitFor(t, "owner's mail", func() bool { return len(m.to("ann@x.test")) >= 1 })
	if got := tokens(m.to("ann@x.test")[0].Text); len(got) != 0 || strings.Contains(m.to("ann@x.test")[0].Text, "/a/") {
		t.Errorf("links without an address: %v", got)
	}
}
