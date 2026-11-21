package store

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/vault"
)

func TestRotateSealsEverythingAgainWithANewKey(t *testing.T) {
	dir, ctx := t.TempDir(), context.Background()
	const oldKey, newKey = "old-store-key-0123456789", "new-store-key-0123456789"
	s, err := open(t, dir, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	user := uuid.New()
	// a value from a version before the salted form: sealed by hand with the first form
	first, _ := vault.New(oldKey)
	enc, _ := first.Seal([]byte("legacy-key"), "library:"+user.String())
	if _, err := s.db.Exec(`INSERT INTO library_keys (user_id, key_id, key_enc, created_at) VALUES (?,?,?,?)`, user.String(), uuid.NewString(), enc, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	other := uuid.New()
	if err := s.SetLibraryKey(ctx, other, LibraryKey{KeyID: uuid.New(), Key: "salted-key"}); err != nil {
		t.Fatal(err)
	}
	if k, ok, err := s.LibraryKey(ctx, user); err != nil || !ok || k.Key != "legacy-key" {
		t.Fatalf("the first form is not read: %+v %v %v", k, ok, err)
	}

	nv, _ := vault.New(newKey)
	n, err := s.Rotate(ctx, nv)
	if err != nil || n < 3 { // two keys and the canary
		t.Fatalf("rotated %d, %v", n, err)
	}
	s.Close()

	if _, err := open(t, dir, oldKey); err == nil {
		t.Fatal("the old store key still opens the database")
	}
	s2, err := open(t, dir, newKey)
	if err != nil {
		t.Fatalf("the new store key does not open it: %v", err)
	}
	defer s2.Close()
	for u, want := range map[uuid.UUID]string{user: "legacy-key", other: "salted-key"} {
		if k, ok, err := s2.LibraryKey(ctx, u); err != nil || !ok || k.Key != want {
			t.Errorf("after the rotation: %+v %v %v, want %q", k, ok, err, want)
		}
	}
}
