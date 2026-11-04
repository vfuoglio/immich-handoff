package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/vault"
)

func open(t *testing.T, dir, secret string) (*Store, error) {
	t.Helper()
	v, err := vault.New(secret)
	if err != nil {
		t.Fatal(err)
	}
	return Open(filepath.Join(dir, "handoff.db"), v)
}

func TestLinksRoundTripAndEncryptedAtRest(t *testing.T) {
	dir, ctx := t.TempDir(), context.Background()
	s, err := open(t, dir, "first-secret-value-1234")
	if err != nil {
		t.Fatal(err)
	}
	user := uuid.New()
	l, err := s.AddLink(ctx, Link{Service: "family", Role: RoleSender, ImmichUserID: user, Name: "Bob", Email: "bob@x", KeyID: uuid.New(), Key: "SECRET-API-KEY"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddLink(ctx, Link{Service: "family", Role: RoleSender, ImmichUserID: user, Name: "Bob", Email: "bob@x", KeyID: uuid.New(), Key: "other"}); err != ErrExists {
		t.Errorf("second join must be ErrExists, got %v", err)
	}
	// the same person may hold another role or service
	if _, err := s.AddLink(ctx, Link{Service: "grandma", Role: RoleSender, ImmichUserID: user, Name: "Bob", Email: "bob@x", KeyID: uuid.New(), Key: "k2"}); err != nil {
		t.Errorf("another service must work: %v", err)
	}
	links, err := s.Links(ctx)
	if err != nil || len(links) != 2 || links[0].Key != "SECRET-API-KEY" || links[0].ID != l.ID || links[0].State != StateActive {
		t.Fatalf("links: %+v %v", links, err)
	}
	s.Close()

	raw, _ := readAll(filepath.Join(dir, "handoff.db"))
	if strings.Contains(string(raw), "SECRET-API-KEY") {
		t.Error("the API key is on disk in clear text")
	}

	s2, err := open(t, dir, "first-secret-value-1234")
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := s2.Links(ctx); len(again) != 2 {
		t.Errorf("data must survive a reopen: %d", len(again))
	}
	if err := s2.RemoveLink(ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	if rest, _ := s2.Links(ctx); len(rest) != 1 {
		t.Errorf("remove: %d left", len(rest))
	}
	s2.Close()

	if _, err := open(t, dir, "a-completely-other-secret"); err == nil {
		t.Error("opening with a different store key must fail")
	}
}
