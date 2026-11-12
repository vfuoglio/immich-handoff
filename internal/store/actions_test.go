package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAnActionLinkWorksOnceForTheOneItWasMadeFor(t *testing.T) {
	st, err := open(t, t.TempDir(), "action-secret-key-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, owner := context.Background(), uuid.New()
	tok, err := st.ActionMake(ctx, ActionLink{UserID: owner, UserName: "Ann", Kind: "approve", Service: "party", Target: "req-1", Group: "req-1"})
	if err != nil || len(tok) != 22 {
		t.Fatalf("%q %v", tok, err)
	}
	got, ok, _ := st.ActionGet(ctx, tok)
	if !ok || got.UserID != owner || got.UserName != "Ann" || got.Kind != "approve" || got.Service != "party" || got.Target != "req-1" || time.Until(got.ExpiresAt) < 23*time.Hour {
		t.Fatalf("%+v %v", got, ok)
	}
	if _, ok, _ := st.ActionGet(ctx, "nonsense"); ok {
		t.Error("an unknown token was accepted")
	}
	if _, ok, _ := st.ActionGet(ctx, tok); !ok {
		t.Error("looking at a link must not spend it")
	}
	used, ok, err := st.ActionUse(ctx, tok)
	if err != nil || !ok || used.Target != "req-1" {
		t.Fatalf("%+v %v %v", used, ok, err)
	}
	if _, ok, _ := st.ActionUse(ctx, tok); ok {
		t.Error("a link worked twice")
	}
	if _, ok, _ := st.ActionGet(ctx, tok); ok {
		t.Error("a used link still shows")
	}
}

func TestOnlyOneOfManyTapsOnTheSameLinkWins(t *testing.T) {
	st, _ := open(t, t.TempDir(), "action-secret-key-1234")
	defer st.Close()
	ctx := context.Background()
	tok, _ := st.ActionMake(ctx, ActionLink{UserID: uuid.New(), Kind: "resume-service", Service: "party"})
	var wins int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok, _ := st.ActionUse(ctx, tok); ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Errorf("%d taps won", wins)
	}
}

func TestUsingOneLinkOfADecisionEndsTheOthers(t *testing.T) {
	st, _ := open(t, t.TempDir(), "action-secret-key-1234")
	defer st.Close()
	ctx, owner := context.Background(), uuid.New()
	approve, _ := st.ActionMake(ctx, ActionLink{UserID: owner, Kind: "approve", Service: "party", Target: "r1", Group: "r1"})
	decline, _ := st.ActionMake(ctx, ActionLink{UserID: owner, Kind: "decline", Service: "party", Target: "r1", Group: "r1"})
	other, _ := st.ActionMake(ctx, ActionLink{UserID: owner, Kind: "approve", Service: "party", Target: "r2", Group: "r2"})
	if _, ok, _ := st.ActionUse(ctx, decline); !ok {
		t.Fatal("the first tap must work")
	}
	if _, ok, _ := st.ActionUse(ctx, approve); ok {
		t.Error("the other answer to the same request still worked")
	}
	if _, ok, _ := st.ActionGet(ctx, other); !ok {
		t.Error("another request's link was ended")
	}
}

func TestALinkExpiresAndOldOnesAreForgotten(t *testing.T) {
	st, _ := open(t, t.TempDir(), "action-secret-key-1234")
	defer st.Close()
	ctx := context.Background()
	tok, _ := st.ActionMake(ctx, ActionLink{UserID: uuid.New(), Kind: "resume-service", Service: "party"})
	old := time.Now().Add(-time.Hour).UTC().Format(actionTime)
	st.db.Exec(`UPDATE action_links SET expires_at = ? WHERE token = ?`, old, tok)
	if _, ok, _ := st.ActionGet(ctx, tok); ok {
		t.Error("an expired link showed")
	}
	if _, ok, _ := st.ActionUse(ctx, tok); ok {
		t.Error("an expired link worked")
	}
	st.db.Exec(`UPDATE action_links SET expires_at = ? WHERE token = ?`, time.Now().Add(-8*24*time.Hour).UTC().Format(actionTime), tok)
	st.ActionMake(ctx, ActionLink{UserID: uuid.New(), Kind: "resume-service", Service: "party"}) // making one clears the old
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM action_links`).Scan(&n)
	if n != 1 {
		t.Errorf("%d rows", n)
	}
}

func TestTokensAreDifferentEveryTime(t *testing.T) {
	st, _ := open(t, t.TempDir(), "action-secret-key-1234")
	defer st.Close()
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		tok, err := st.ActionMake(context.Background(), ActionLink{UserID: uuid.New(), Kind: "approve", Service: "s"})
		if err != nil || seen[tok] {
			t.Fatalf("%q %v", tok, err)
		}
		seen[tok] = true
	}
}
