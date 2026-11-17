package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAPersonsKeyForStackingIsKeptSealedAndForgotten(t *testing.T) {
	dir := t.TempDir()
	st, err := open(t, dir, "stack-key-secret-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	u, id := uuid.New(), uuid.New()
	if _, ok, err := st.StackKey(ctx, u); ok || err != nil {
		t.Fatalf("no key yet: %v %v", ok, err)
	}
	if err := st.SetStackKey(ctx, u, StackKey{KeyID: id, Key: "a-very-secret-key-value"}); err != nil {
		t.Fatal(err)
	}
	if k, ok, err := st.StackKey(ctx, u); err != nil || !ok || k.Key != "a-very-secret-key-value" || k.KeyID != id {
		t.Fatalf("round trip: %+v %v %v", k, ok, err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "handoff.db")); err == nil && bytes.Contains(raw, []byte("a-very-secret-key-value")) {
		t.Error("the secret must not be in the database file in the clear")
	}
	if err := st.DeleteStackKey(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.StackKey(ctx, u); ok {
		t.Error("forgotten")
	}
}

func TestTheStacksTheAppMadeAreKeptForTheUndoAndForgottenAfterADay(t *testing.T) {
	st, err := open(t, t.TempDir(), "stacks-made-secret-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	u := uuid.New()
	now := time.Now()
	st.AddStackMade(ctx, StackMade{StackID: "s1", RuleID: "r1", UserID: u, Assets: []string{"a", "b"}, At: now.Add(-time.Minute)})
	st.AddStackMade(ctx, StackMade{StackID: "s2", RuleID: "r1", UserID: u, Assets: []string{"c", "d"}, At: now.Add(-20 * time.Minute)})
	st.AddStackMade(ctx, StackMade{StackID: "s3", RuleID: "r2", UserID: u, Assets: []string{"e", "f"}, At: now.Add(-time.Minute)})
	st.AddStackMade(ctx, StackMade{StackID: "old", RuleID: "r1", UserID: u, Assets: []string{"x"}, At: now.Add(-30 * time.Hour)})
	got, err := st.StacksMadeSince(ctx, "r1", now.Add(-15*time.Minute))
	if err != nil || len(got) != 1 || got[0].StackID != "s1" || len(got[0].Assets) != 2 || got[0].Assets[0] != "a" {
		t.Fatalf("this rule's stacks of the last 15 minutes: %+v %v", got, err)
	}
	if all, _ := st.StacksMadeSince(ctx, "r1", now.Add(-time.Hour)); len(all) != 2 {
		t.Errorf("the stack of 30 hours ago is forgotten: %+v", all)
	}
	st.DeleteStackMade(ctx, "s1")
	if got, _ := st.StacksMadeSince(ctx, "r1", now.Add(-15*time.Minute)); len(got) != 0 {
		t.Error("forgotten once taken apart")
	}
}
