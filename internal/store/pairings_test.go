package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAPairingKeepsItsPeopleAndTheRulesAndPoolThatBelongToIt(t *testing.T) {
	st, err := open(t, t.TempDir(), "pairing-secret-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	if _, err := st.AddService(ctx, Service{Name: "pair-1", Tag: "pair-1", OwnerID: a, OwnerName: "Ann", KeyID: uuid.New(), Key: "k", Policy: PolicyOpen, Visibility: VisInvite, Invite: "tok", Pool: true}); err != nil {
		t.Fatal(err)
	}
	p, err := st.AddPairing(ctx, Pairing{Service: "pair-1", AID: a, AName: "Ann", BID: b, BName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	if p.State != PairingInvited || p.ExpiresAt.Sub(p.CreatedAt) < 6*24*time.Hour {
		t.Errorf("a new pairing waits for a yes for a week: %+v", p)
	}
	if _, err := st.AddPairing(ctx, Pairing{Service: "pair-1", AID: a, BID: b}); !errors.Is(err, ErrExists) {
		t.Errorf("a pool belongs to one pairing only: %v", err)
	}
	got, err := st.PairingOfService(ctx, "pair-1")
	if err != nil || got.ID != p.ID || got.AName != "Ann" || got.BName != "Bob" || !got.Takes(a) || !got.Takes(b) || got.Takes(uuid.New()) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if _, err := st.PairingOfService(ctx, "family"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an ordinary service has no pairing: %v", err)
	}
	if mine, _ := st.PairingsOf(ctx, b); len(mine) != 1 {
		t.Errorf("each side finds it: %v", mine)
	}
	if other, _ := st.PairingsOf(ctx, uuid.New()); len(other) != 0 {
		t.Errorf("a stranger finds nothing: %v", other)
	}

	if err := st.SetServicePairing(ctx, "pair-1", p.ID); err != nil {
		t.Fatal(err)
	}
	if svcs, _ := st.Services(ctx); len(svcs) != 1 || svcs[0].Pairing != p.ID {
		t.Errorf("the pool carries the pairing's id: %+v", svcs)
	}
	ra, _ := st.AddRule(ctx, Rule{OwnerID: a, Service: "pair-1", Cond: RuleCond{People: []RuleRef{{ID: uuid.NewString(), Name: "Nora"}}}})
	rb, _ := st.AddRule(ctx, Rule{OwnerID: b, Service: "pair-1", Cond: RuleCond{People: []RuleRef{{ID: uuid.NewString(), Name: "Leo"}}}})
	plain, _ := st.AddRule(ctx, Rule{OwnerID: a, Service: "family", Cond: RuleCond{Phrase: "dog"}})
	for _, id := range []string{ra.ID, rb.ID} {
		if err := st.SetRulePairing(ctx, id, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.PutRulePhoto(ctx, ra.ID, "asset-1", RuleSuggested, "Nora is in it")
	if rs, _ := st.RulesOfPairing(ctx, p.ID); len(rs) != 2 || rs[0].Pairing != p.ID {
		t.Fatalf("both sides' rules are found by the pairing: %+v", rs)
	}
	if r, _ := st.RuleByID(ctx, plain.ID); r.Pairing != "" {
		t.Errorf("an ordinary rule has none: %+v", r)
	}

	if err := st.SetPairingRulesPaused(ctx, p.ID, true); err != nil {
		t.Fatal(err)
	}
	if rs, _ := st.RulesOfPairing(ctx, p.ID); !rs[0].Paused || !rs[1].Paused {
		t.Errorf("pausing the pairing pauses both rules: %+v", rs)
	}
	if r, _ := st.RuleByID(ctx, plain.ID); r.Paused {
		t.Errorf("and only them")
	}
	if err := st.SetPairingState(ctx, p.ID, PairingStopped, "Bob stopped it"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.PairingByID(ctx, p.ID); got.State != PairingStopped || got.Note != "Bob stopped it" {
		t.Errorf("the state and the reason are kept: %+v", got)
	}
	if err := st.SetPairingState(ctx, "nope", PairingActive, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown pairing: %v", err)
	}

	if err := st.RemovePairing(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PairingByID(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the pairing is gone: %v", err)
	}
	if rs, _ := st.RulesOf(ctx, a); len(rs) != 1 || rs[0].ID != plain.ID {
		t.Errorf("both sides' rules go with it and an ordinary rule stays: %+v", rs)
	}
	if rs, _ := st.RulesOf(ctx, b); len(rs) != 0 {
		t.Errorf("the other side's rule goes too: %+v", rs)
	}
	if states, _ := st.RulePhotoStates(ctx, ra.ID); len(states) != 0 {
		t.Errorf("what a rule remembered goes with it: %v", states)
	}
	if svcs, _ := st.Services(ctx); len(svcs) != 1 || svcs[0].Pairing != "" {
		t.Errorf("the pool stays, free of the pairing (the hub removes it): %+v", svcs)
	}
}
