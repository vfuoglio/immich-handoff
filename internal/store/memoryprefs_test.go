package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestAPersonsOnThisDaySettingsAndKeyAreKeptSealedAndForgotten(t *testing.T) {
	dir := t.TempDir()
	st, err := open(t, dir, "memory-prefs-secret-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	u, k1, k2 := uuid.New(), uuid.New(), uuid.New()
	if _, ok, err := st.MemoryPref(ctx, u); ok || err != nil {
		t.Fatalf("nothing yet: %v %v", ok, err)
	}
	p := MemoryPref{UserID: u, Name: "Ann", Lang: "de", At: "07:30", TZ: "Europe/Rome", Push: true, Pictures: true, KeyID: k1, Key: "a-very-secret-memory-key"}
	if err := st.SetMemoryPref(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.MemoryPref(ctx, u)
	if err != nil || !ok || got.Key != p.Key || got.KeyID != k1 || got.At != "07:30" || got.TZ != "Europe/Rome" || !got.Push || got.Email || !got.Pictures || got.Lang != "de" {
		t.Fatalf("round trip: %+v %v %v", got, ok, err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "handoff.db")); err == nil && bytes.Contains(raw, []byte(p.Key)) {
		t.Error("the secret must not be in the database file in the clear")
	}

	// a day sent and a key that broke survive a change of settings, but a new key starts clean
	if err := st.MarkMemorySent(ctx, u, "2026-11-16"); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkMemoryBroken(ctx, u, true); err != nil {
		t.Fatal(err)
	}
	p.At, p.Email = "09:00", true
	if err := st.SetMemoryPref(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, _, _ = st.MemoryPref(ctx, u)
	if got.LastDay != "2026-11-16" || !got.Broken || got.At != "09:00" || !got.Email {
		t.Fatalf("settings changed, the rest kept: %+v", got)
	}
	p.KeyID, p.Key = k2, "another-secret-memory-key-1"
	if err := st.SetMemoryPref(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, _, _ = st.MemoryPref(ctx, u)
	if got.Broken || got.Key != p.Key {
		t.Fatalf("a new key is not broken: %+v", got)
	}
	if all, _ := st.MemoryPrefs(ctx); len(all) != 1 {
		t.Fatalf("everyone: %v", all)
	}
	if err := st.DeleteMemoryPref(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.MemoryPref(ctx, u); ok {
		t.Error("forgotten")
	}
}
