package hub_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

func pairingEnv(t *testing.T) (*env, context.Context) {
	t.Helper()
	e, ctx := setup(t, true), context.Background()
	e.f.named = []map[string]any{ // Immich's named people; the fake lists the same for everyone
		{"id": noraID, "name": "Nora", "isHidden": false, "thumbnailPath": "x"},
		{"id": leoID, "name": "Leo", "isHidden": false, "thumbnailPath": "x"},
		{"id": uuid.NewString(), "name": "", "isHidden": false, "thumbnailPath": "x"},
	}
	return e, ctx
}

var (
	nora = uuid.MustParse(noraID)
	leo  = uuid.MustParse(leoID)
)

// bob offers to share Nora with ann; nothing has been answered yet.
func offered(t *testing.T, e *env, ctx context.Context) (store.Pairing, string) {
	t.Helper()
	p, token, err := e.h.StartPairing(ctx, e.sess, bob, hub.PairingParams{With: ann, People: []uuid.UUID{nora}})
	if err != nil {
		t.Fatal(err)
	}
	return p, token
}

// ann says yes and sends Leo back.
func accepted(t *testing.T, e *env, ctx context.Context, p store.Pairing) {
	t.Helper()
	if err := e.h.AcceptPairing(ctx, e.ann, ann, p.ID, []uuid.UUID{leo}, 0); err != nil {
		t.Fatal(err)
	}
}

func ruleOfUser(t *testing.T, e *env, ctx context.Context, user uuid.UUID) store.Rule {
	t.Helper()
	rs, err := e.st.RulesOf(ctx, user)
	if err != nil || len(rs) != 1 {
		t.Fatalf("expected one rule: %+v %v", rs, err)
	}
	return rs[0]
}

func TestAPairingIsOfferedInOneStepAndNothingMovesUntilTheOtherPersonSaysYes(t *testing.T) {
	e, ctx := pairingEnv(t)
	p, token := offered(t, e, ctx)
	if p.State != store.PairingInvited || p.AID != bobID || p.BID != annID || p.BName != "Ann" || token == "" {
		t.Fatalf("an offer waits for a yes: %+v %q", p, token)
	}
	all, _ := e.st.Services(ctx)
	if len(all) != 1 || !all[0].Pool || all[0].Pairing != p.ID || all[0].Visibility != store.VisInvite || all[0].Policy != store.PolicyOpen || all[0].OwnerID != bobID || all[0].Invite != token {
		t.Fatalf("a private pool by invitation, owned by the starter and marked as the pairing's: %+v", all)
	}
	if links, _ := e.st.Links(ctx); len(links) != 0 {
		t.Errorf("nothing is made for the other person before they say yes: %+v", links)
	}
	r := ruleOfUser(t, e, ctx, bobID)
	if r.Pairing != p.ID || !r.Paused || r.Service != all[0].Name || len(r.Cond.People) != 1 || r.Cond.People[0].Name != "Nora" {
		t.Fatalf("the starter's rule is made, marked, and waits: %+v", r)
	}
	// a photo of Nora arrives while the invitation waits: it is not even suggested
	e.f.ruleHits = []map[string]any{hit(bobID, 60, nil, noraID)}
	e.h.RulesMaintenance(ctx)
	if got, _ := e.st.RulePhotos(ctx, r.ID, store.RuleSuggested); len(got) != 0 {
		t.Errorf("a paused rule suggests nothing: %v", got)
	}
	if mine, _ := e.h.PairingsFor(ctx, annID); len(mine) != 1 {
		t.Errorf("the other person finds it: %v", mine)
	}
}

func TestOnlyTheInvitedPersonCanSayYesAndOnlyWithRealNamedPeople(t *testing.T) {
	e, ctx := pairingEnv(t)
	p, _ := offered(t, e, ctx)
	stranger := immich.User{ID: uuid.New(), Name: "Eve"}
	if err := e.h.AcceptPairing(ctx, e.ann, stranger, p.ID, []uuid.UUID{leo}, 0); !errors.Is(err, hub.ErrNoPairing) {
		t.Errorf("a stranger cannot see it: %v", err)
	}
	if err := e.h.AcceptPairing(ctx, e.sess, bob, p.ID, []uuid.UUID{leo}, 0); !errors.Is(err, hub.ErrNoPairing) {
		t.Errorf("the starter cannot say the other person's yes: %v", err)
	}
	for name, people := range map[string][]uuid.UUID{"none": nil, "a stranger's person": {uuid.New()}, "twice": {leo, leo}} {
		if err := e.h.AcceptPairing(ctx, e.ann, ann, p.ID, people, 0); err == nil || !hub.IsUserError(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if links, _ := e.st.Links(ctx); len(links) != 0 {
		t.Errorf("a refused yes joins nobody: %+v", links)
	}
	// the generic way into a pool is closed for a pairing's pool, even with the invitation
	name := p.Service
	token := mustInvite(t, e, ctx, name)
	if err := e.h.JoinWith(ctx, e.ann, ann, name, token, "copy", true); !errors.Is(err, hub.ErrLockedByPairing) {
		t.Errorf("a pairing's pool is joined only by saying yes to the pairing: %v", err)
	}
	accepted(t, e, ctx, p)
	if err := e.h.AcceptPairing(ctx, e.ann, ann, p.ID, []uuid.UUID{leo}, 0); !errors.Is(err, hub.ErrPairingState) {
		t.Errorf("a yes is given once: %v", err)
	}
}

func mustInvite(t *testing.T, e *env, ctx context.Context, name string) string {
	t.Helper()
	all, _ := e.st.Services(ctx)
	for _, s := range all {
		if s.Name == name {
			return s.Invite
		}
	}
	t.Fatalf("no service %s", name)
	return ""
}

func TestSayingYesJoinsTheOtherPersonAndStartsBothSidesFromThatMoment(t *testing.T) {
	e, ctx := pairingEnv(t)
	p, _ := offered(t, e, ctx)
	before := time.Now()
	accepted(t, e, ctx, p)
	got, _ := e.st.PairingByID(ctx, p.ID)
	if got.State != store.PairingActive {
		t.Fatalf("it is on: %+v", got)
	}
	links, _ := e.st.Links(ctx)
	if len(links) != 1 || links[0].ImmichUserID != annID || links[0].Role != store.RoleSender || links[0].Service != p.Service || links[0].Mode != "copy" {
		t.Fatalf("the other person joined with their own key: %+v", links)
	}
	for _, c := range []struct {
		user   uuid.UUID
		person string
	}{{bobID, "Nora"}, {annID, "Leo"}} {
		r := ruleOfUser(t, e, ctx, c.user)
		if r.Pairing != p.ID || r.Paused || r.Mode != store.RuleSuggest || r.Service != p.Service || r.Cond.People[0].Name != c.person {
			t.Errorf("each side has its own rule, running, and asking first: %+v", r)
		}
		if r.Since.Before(before.Add(-time.Second)) {
			t.Errorf("only photos from the moment of yes: since %v, yes at %v", r.Since, before)
		}
	}
	// a photo of each person's own reaches the other's way: both suggest, and a yes tags it with the pool's tag in the sender's own Immich
	a, b := hit(bobID, 60, nil, noraID), hit(annID, 60, nil, leoID)
	e.f.ruleHits = []map[string]any{a, b}
	e.h.RulesMaintenance(ctx)
	bobRule, annRule := ruleOfUser(t, e, ctx, bobID), ruleOfUser(t, e, ctx, annID)
	for _, r := range []store.Rule{bobRule, annRule} {
		if got, _ := e.st.RulePhotos(ctx, r.ID, store.RuleSuggested); len(got) != 1 {
			t.Fatalf("each side is offered its own photo and not the other's: %v", got)
		}
	}
	if err := e.h.AnswerSuggestion(ctx, bob, bobRule.ID, hub.AnswerSend, nil); err != nil {
		t.Fatalf("the starter, who has no link row of their own, can send through their pool key: %v", err)
	}
	if err := e.h.AnswerSuggestion(ctx, ann, annRule.ID, hub.AnswerSend, nil); err != nil {
		t.Fatal(err)
	}
	if len(e.f.tagged) != 2 || !(e.f.tagged[0] == a["id"] && e.f.tagged[1] == b["id"] || e.f.tagged[0] == b["id"] && e.f.tagged[1] == a["id"]) {
		t.Errorf("both photos are tagged for the pool, each by its own sender: %v", e.f.tagged)
	}
}

func TestEveryPieceOfAPairingIsLockedToItsCard(t *testing.T) {
	e, ctx := pairingEnv(t)
	p, _ := offered(t, e, ctx)
	accepted(t, e, ctx, p)
	name := p.Service
	bobRule, annRule := ruleOfUser(t, e, ctx, bobID), ruleOfUser(t, e, ctx, annID)
	locked := map[string]error{
		"pause a rule":          e.h.PauseRule(ctx, bobID, bobRule.ID, true),
		"delete a rule":         e.h.DeleteRule(ctx, bob, bobRule.ID),
		"delete the other rule": e.h.DeleteRule(ctx, ann, annRule.ID),
		"delete the pool":       e.h.DeleteService(ctx, e.sess, bob, name),
		"leave the pool":        e.h.Leave(ctx, e.ann, ann, name),
		"pause the pool":        e.h.SetServicePaused(ctx, bob, name, true),
		"pause as a member":     e.h.SetMemberPaused(ctx, ann, name, true),
		"end the pool":          e.h.EndPoolNow(ctx, bob, name),
		"new invitation":        e.h.NewInvite(ctx, bob, name),
		"limits":                e.h.SetLimits(ctx, bob, name, 5, 5),
		"link an album":         e.h.LinkAlbum(ctx, e.ann, ann, name, uuid.New()),
		"reconnect":             e.h.ReconnectService(ctx, e.sess, bob, name),
		"visibility":            e.h.SetVisibility(ctx, bob, name, store.VisListed),
	}
	if _, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: name, Phrase: "beach"}); err != nil {
		locked["add a rule of your own to the pool"] = err
	}
	for what, err := range locked {
		if !errors.Is(err, hub.ErrLockedByPairing) {
			t.Errorf("%s: should be refused, got %v", what, err)
		}
	}
	if !hub.IsUserError(hub.ErrLockedByPairing) {
		t.Error("the refusal is said as it is")
	}
	// what the card does is not locked: answering a suggestion and looking back
	if _, err := e.h.LookBack(ctx, bob, bobRule.ID); err != nil && !errors.Is(err, hub.ErrNothingOlder) {
		t.Errorf("look back stays: %v", err)
	}
	if rs, _ := e.st.RulesOf(ctx, bobID); len(rs) != 1 {
		t.Errorf("nothing was removed: %+v", rs)
	}
}

func TestEitherPersonCanPauseAndResumeAndThatStopsAndRestartsBothWays(t *testing.T) {
	e, ctx := pairingEnv(t)
	p, _ := offered(t, e, ctx)
	if err := e.h.PausePairing(ctx, bob, p.ID, true); !errors.Is(err, hub.ErrPairingState) {
		t.Errorf("there is nothing to pause before the yes: %v", err)
	}
	accepted(t, e, ctx, p)
	if err := e.h.PausePairing(ctx, immich.User{ID: uuid.New()}, p.ID, true); !errors.Is(err, hub.ErrNoPairing) {
		t.Errorf("a stranger: %v", err)
	}
	if err := e.h.PausePairing(ctx, ann, p.ID, true); err != nil {
		t.Fatal(err)
	}
	svcs, _ := e.st.Services(ctx)
	if !svcs[0].Paused || !ruleOfUser(t, e, ctx, bobID).Paused || !ruleOfUser(t, e, ctx, annID).Paused {
		t.Errorf("the pool and both rules wait: %+v", svcs[0])
	}
	if got, _ := e.st.PairingByID(ctx, p.ID); got.State != store.PairingPaused {
		t.Errorf("%+v", got)
	}
	if err := e.h.PausePairing(ctx, ann, p.ID, true); err != nil {
		t.Errorf("pausing twice changes nothing: %v", err)
	}
	if err := e.h.PausePairing(ctx, bob, p.ID, false); err != nil {
		t.Fatal(err)
	}
	svcs, _ = e.st.Services(ctx)
	if svcs[0].Paused || ruleOfUser(t, e, ctx, bobID).Paused || ruleOfUser(t, e, ctx, annID).Paused {
		t.Errorf("the other person can resume it: %+v", svcs[0])
	}
}

func TestTheStarterStoppingTakesTheirSideApartAndLeavesTheOtherAbleToCleanUpAndNothingIsDeleted(t *testing.T) {
	e, ctx := pairingEnv(t)
	p, _ := offered(t, e, ctx)
	accepted(t, e, ctx, p)
	if err := e.h.StopPairing(ctx, e.sess, bob, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.PairingByID(ctx, p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the pairing is gone: %v", err)
	}
	if svcs, _ := e.st.Services(ctx); len(svcs) != 0 {
		t.Errorf("the pool is gone: %+v", svcs)
	}
	for _, u := range []uuid.UUID{bobID, annID} {
		if rs, _ := e.st.RulesOf(ctx, u); len(rs) != 0 {
			t.Errorf("both sides' rules go: %+v", rs)
		}
	}
	if orphans, _ := e.h.Orphaned(ctx, annID); len(orphans) != 1 || orphans[0] != p.Service {
		t.Errorf("the other person sees it removed and can clean up their own key: %v", orphans)
	}
	if err := e.h.Leave(ctx, e.ann, ann, p.Service); err != nil {
		t.Fatalf("with the pairing gone nothing is locked: %v", err)
	}
	if len(e.f.keys) != 0 || len(e.f.workflows) != 0 {
		t.Errorf("both people's keys and workflows are gone from Immich: keys %d workflows %d", len(e.f.keys), len(e.f.workflows))
	}
	if len(e.f.trashed) != 0 {
		t.Errorf("no photo was touched: %v", e.f.trashed)
	}
}

func TestTheOtherPersonStoppingStopsEverythingAtOnceAndWaitsForTheStarterToTakeTheirSideApart(t *testing.T) {
	e, ctx := pairingEnv(t)
	p, _ := offered(t, e, ctx)
	accepted(t, e, ctx, p)
	if err := e.h.StopPairing(ctx, e.ann, ann, p.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.st.PairingByID(ctx, p.ID)
	if got.State != store.PairingStopped || got.Note == "" {
		t.Fatalf("stopped, with a reason: %+v", got)
	}
	if links, _ := e.st.Links(ctx); len(links) != 0 {
		t.Errorf("their own side is clean: %+v", links)
	}
	if rs, _ := e.st.RulesOf(ctx, annID); len(rs) != 0 {
		t.Errorf("their rules are gone: %+v", rs)
	}
	svcs, _ := e.st.Services(ctx)
	if len(svcs) != 1 || !svcs[0].Paused || !ruleOfUser(t, e, ctx, bobID).Paused {
		t.Errorf("nothing is handed over any more, and the starter's pieces wait: %+v", svcs)
	}
	if err := e.h.PausePairing(ctx, bob, p.ID, false); !errors.Is(err, hub.ErrPairingState) {
		t.Errorf("a stopped sharing cannot be resumed: %v", err)
	}
	if err := e.h.StopPairing(ctx, e.ann, ann, p.ID); err != nil {
		t.Errorf("stopping again changes nothing: %v", err)
	}
	if err := e.h.StopPairing(ctx, e.sess, bob, p.ID); err != nil {
		t.Fatal(err)
	}
	if svcs, _ := e.st.Services(ctx); len(svcs) != 0 {
		t.Errorf("the starter's next visit takes their side apart: %+v", svcs)
	}
	if len(e.f.keys) != 0 || len(e.f.workflows) != 0 {
		t.Errorf("nothing left in Immich: keys %d workflows %d", len(e.f.keys), len(e.f.workflows))
	}
}

func TestDecliningAnInvitationStopsItWithoutJoiningAnything(t *testing.T) {
	e, ctx := pairingEnv(t)
	p, _ := offered(t, e, ctx)
	if err := e.h.StopPairing(ctx, e.ann, ann, p.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.st.PairingByID(ctx, p.ID)
	if got.State != store.PairingStopped || got.Note == "" {
		t.Errorf("%+v", got)
	}
	if err := e.h.AcceptPairing(ctx, e.ann, ann, p.ID, []uuid.UUID{leo}, 0); !errors.Is(err, hub.ErrPairingState) {
		t.Errorf("it cannot be accepted after: %v", err)
	}
}

func TestAnInvitationNobodyAnsweredLapsesAndCannotBeAccepted(t *testing.T) {
	e, ctx := pairingEnv(t)
	p, _ := offered(t, e, ctx)
	e.h.Now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	e.h.RulesMaintenance(ctx) // the timer lets it go
	got, _ := e.st.PairingByID(ctx, p.ID)
	if got.State != store.PairingStopped {
		t.Fatalf("lapsed after a week: %+v", got)
	}
	if err := e.h.AcceptPairing(ctx, e.ann, ann, p.ID, []uuid.UUID{leo}, 0); !errors.Is(err, hub.ErrPairingState) {
		t.Errorf("%v", err)
	}
	// and a yes that comes first but too late is refused as lapsed and stops it too
	e2, ctx2 := pairingEnv(t)
	p2, _ := offered(t, e2, ctx2)
	e2.h.Now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	if err := e2.h.AcceptPairing(ctx2, e2.ann, ann, p2.ID, []uuid.UUID{leo}, 0); !errors.Is(err, hub.ErrPairingLapsed) {
		t.Errorf("%v", err)
	}
	if got, _ := e2.st.PairingByID(ctx2, p2.ID); got.State != store.PairingStopped {
		t.Errorf("%+v", got)
	}
	if err := e.h.StopPairing(ctx, e.sess, bob, p.ID); err != nil {
		t.Errorf("the starter takes it apart: %v", err)
	}
}

func TestAPairingIsOnlyMadeFromRealChoicesAndLeavesNothingBehindWhenItFailsHalfway(t *testing.T) {
	e, ctx := pairingEnv(t)
	start := func(with immich.User, people ...uuid.UUID) error {
		_, _, err := e.h.StartPairing(ctx, e.sess, bob, hub.PairingParams{With: with, People: people})
		return err
	}
	if err := start(bob, nora); !errors.Is(err, hub.ErrPairingSelf) {
		t.Errorf("with yourself: %v", err)
	}
	if err := start(ann); !errors.Is(err, hub.ErrPairingPeople) {
		t.Errorf("nobody chosen: %v", err)
	}
	if err := start(ann, nora, nora); !errors.Is(err, hub.ErrPairingPeople) {
		t.Errorf("the same person twice: %v", err)
	}
	if err := start(ann, uuid.New()); !errors.Is(err, hub.ErrRulePerson) {
		t.Errorf("a person who is not in their library: %v", err)
	}
	if svcs, _ := e.st.Services(ctx); len(svcs) != 0 {
		t.Errorf("a refused offer makes nothing: %+v", svcs)
	}
	// halfway: bob already has 19 rules, so the second of two rules fails after the pool was made
	if err := e.h.Join(ctx, e.sess, bob, "family", "", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 19; i++ {
		if _, err := e.h.AddRule(ctx, e.sess, bob, hub.RuleParams{Service: "family", Phrase: "p" + string(rune('a'+i))}); err != nil {
			t.Fatal(err)
		}
	}
	keys := len(e.f.keys)
	if err := start(ann, nora, leo); !errors.Is(err, hub.ErrTooManyRules) {
		t.Fatalf("the second rule cannot be made: %v", err)
	}
	if svcs, _ := e.st.Services(ctx); len(svcs) != 0 {
		t.Errorf("the pool was taken back: %+v", svcs)
	}
	if pairs, _ := e.st.Pairings(ctx); len(pairs) != 0 {
		t.Errorf("and the record: %+v", pairs)
	}
	if rs, _ := e.st.RulesOf(ctx, bobID); len(rs) != 19 {
		t.Errorf("and the rule that was made: %d", len(rs))
	}
	if len(e.f.keys) != keys {
		t.Errorf("and the pool's key in Immich: %d keys, was %d", len(e.f.keys), keys)
	}
}

func TestAPersonIsInAtMostFiveSharingsAtOnceAndAStoppedOneDoesNotCount(t *testing.T) {
	e, ctx := pairingEnv(t)
	var made []store.Pairing
	for i := 0; i < 5; i++ {
		p, _, err := e.h.StartPairing(ctx, e.sess, bob, hub.PairingParams{With: immich.User{ID: uuid.New(), Name: "Friend"}, People: []uuid.UUID{nora}})
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, p)
	}
	if _, _, err := e.h.StartPairing(ctx, e.sess, bob, hub.PairingParams{With: immich.User{ID: uuid.New(), Name: "Friend"}, People: []uuid.UUID{nora}}); !errors.Is(err, hub.ErrTooManyPairings) {
		t.Fatalf("the sixth: %v", err)
	}
	if err := e.h.StopPairing(ctx, e.sess, bob, made[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.h.StartPairing(ctx, e.sess, bob, hub.PairingParams{With: immich.User{ID: uuid.New(), Name: "Friend"}, People: []uuid.UUID{nora}}); err != nil {
		t.Errorf("room again after one ends: %v", err)
	}
}

func TestAnAdministratorRemovingAPairingsPoolOrMemberEndsTheSharing(t *testing.T) {
	e, ctx := pairingEnv(t)
	p, _ := offered(t, e, ctx)
	accepted(t, e, ctx, p)
	links, _ := e.st.Links(ctx)
	if err := e.h.AdminRemoveMember(ctx, bob, links[0].ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.st.PairingByID(ctx, p.ID); got.State != store.PairingStopped {
		t.Errorf("removing a member stops the sharing: %+v", got)
	}
	if err := e.h.AdminRemoveService(ctx, bob, p.Service); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.PairingByID(ctx, p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("removing the pool removes the pairing: %v", err)
	}
	for _, u := range []uuid.UUID{bobID, annID} {
		if rs, _ := e.st.RulesOf(ctx, u); len(rs) != 0 {
			t.Errorf("and both sides' rules: %+v", rs)
		}
	}
}

func TestAPairingsPoolNameIsRecognisedAndNothingElseIs(t *testing.T) {
	for name, want := range map[string]bool{"pair-0a1b2c3d": true, "pair-0A1B2C3D": true, "pair-0a1b2c3": false, "pair-0a1b2c3dd": false, "pair-0a1b2c3g": false, "pair-": false, "trip": false, "Pair-0a1b2c3d": false, "": false} {
		if got := hub.IsPairingName(name); got != want {
			t.Errorf("%q: %v", name, got)
		}
	}
}

func TestEachPersonOfAPairingChoosesTheirOwnWaitAndABadWaitMakesNothing(t *testing.T) {
	e, ctx := pairingEnv(t)
	for _, n := range []int{-1, 61} {
		if _, _, err := e.h.StartPairing(ctx, e.sess, bob, hub.PairingParams{With: ann, People: []uuid.UUID{nora}, SettleMinutes: n}); !errors.Is(err, hub.ErrBadSettle) || !hub.IsUserError(err) {
			t.Errorf("%d minutes: %v", n, err)
		}
	}
	if svcs, _ := e.st.Services(ctx); len(svcs) != 0 {
		t.Fatalf("a bad wait is refused before anything is made: %+v", svcs)
	}
	p, _, err := e.h.StartPairing(ctx, e.sess, bob, hub.PairingParams{With: ann, People: []uuid.UUID{nora}, SettleMinutes: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := ruleOfUser(t, e, ctx, bobID).SettleMinutes; got != 2 {
		t.Errorf("the starter's rule waits two minutes: %d", got)
	}
	if err := e.h.AcceptPairing(ctx, e.ann, ann, p.ID, []uuid.UUID{leo}, 61); !errors.Is(err, hub.ErrBadSettle) {
		t.Errorf("a bad wait on the yes: %v", err)
	}
	if links, _ := e.st.Links(ctx); len(links) != 0 {
		t.Errorf("a refused yes joins nobody: %+v", links)
	}
	if got, _ := e.st.PairingByID(ctx, p.ID); got.State != store.PairingInvited {
		t.Errorf("and it still waits: %+v", got)
	}
	if err := e.h.AcceptPairing(ctx, e.ann, ann, p.ID, []uuid.UUID{leo}, 10); err != nil {
		t.Fatal(err)
	}
	if got := ruleOfUser(t, e, ctx, annID).SettleMinutes; got != 10 {
		t.Errorf("the other person's own choice is on their own rule: %d", got)
	}
	if got := ruleOfUser(t, e, ctx, bobID).SettleMinutes; got != 2 {
		t.Errorf("and the starter's stays: %d", got)
	}
}
