package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestAPersonsKeyForFollowingAlbumsIsKeptSealedAndForgotten(t *testing.T) {
	dir := t.TempDir()
	st, err := open(t, dir, "library-key-secret-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	u, id := uuid.New(), uuid.New()
	if _, ok, err := st.LibraryKey(ctx, u); ok || err != nil {
		t.Fatalf("no key yet: %v %v", ok, err)
	}
	if err := st.SetLibraryKey(ctx, u, LibraryKey{KeyID: id, Key: "a-very-secret-key-value"}); err != nil {
		t.Fatal(err)
	}
	if k, ok, err := st.LibraryKey(ctx, u); err != nil || !ok || k.Key != "a-very-secret-key-value" || k.KeyID != id {
		t.Fatalf("round trip: %+v %v %v", k, ok, err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "handoff.db")); err == nil && bytes.Contains(raw, []byte("a-very-secret-key-value")) {
		t.Error("the secret must not be in the database file in the clear")
	}
	if err := st.DeleteLibraryKey(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.LibraryKey(ctx, u); ok {
		t.Error("forgotten")
	}
}
