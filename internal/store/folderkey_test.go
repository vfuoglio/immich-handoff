package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestAPersonsKeyForFolderAlbumsIsKeptSealedAndForgotten(t *testing.T) {
	dir := t.TempDir()
	st, err := open(t, dir, "folder-key-secret-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	u, id := uuid.New(), uuid.New()
	if _, ok, err := st.FolderKey(ctx, u); ok || err != nil {
		t.Fatalf("no key yet: %v %v", ok, err)
	}
	if err := st.SetFolderKey(ctx, u, FolderKey{KeyID: id, Key: "a-very-secret-key-value"}); err != nil {
		t.Fatal(err)
	}
	if k, ok, err := st.FolderKey(ctx, u); err != nil || !ok || k.Key != "a-very-secret-key-value" || k.KeyID != id {
		t.Fatalf("round trip: %+v %v %v", k, ok, err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "handoff.db")); err == nil && bytes.Contains(raw, []byte("a-very-secret-key-value")) {
		t.Error("the secret must not be in the database file in the clear")
	}
	if err := st.DeleteFolderKey(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.FolderKey(ctx, u); ok {
		t.Error("forgotten")
	}
}
