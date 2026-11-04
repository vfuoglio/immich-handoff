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

func TestServicesRequestsAndOrphaning(t *testing.T) {
	dir, ctx := t.TempDir(), context.Background()
	s, err := open(t, dir, "first-secret-value-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner, bob := uuid.New(), uuid.New()
	svc, err := s.AddService(ctx, Service{Name: "holiday", Tag: "hol", InboxTag: "inbox", Refused: "r", ExpiryDays: 30, OwnerID: owner, OwnerName: "Ann", OwnerEmail: "a@x",
		KeyID: uuid.New(), Key: "RECEIVER-KEY", Policy: PolicyApproval})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddService(ctx, Service{Name: "holiday", Tag: "x", InboxTag: "i", Refused: "r", ExpiryDays: 1, OwnerID: owner, KeyID: uuid.New(), Key: "k", Policy: PolicyOpen}); err != ErrExists {
		t.Errorf("duplicate service name: %v", err)
	}
	got, _ := s.Services(ctx)
	if len(got) != 1 || got[0].Key != "RECEIVER-KEY" || got[0].ID != svc.ID || got[0].Policy != PolicyApproval {
		t.Fatalf("services: %+v", got)
	}
	req, err := s.AddRequest(ctx, Request{Service: "holiday", UserID: bob, Name: "Bob", Email: "b@x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRequest(ctx, Request{Service: "holiday", UserID: bob}); err != ErrExists {
		t.Errorf("duplicate request: %v", err)
	}
	if err := s.SetRequestStatus(ctx, req.ID, StatusApproved); err != nil {
		t.Fatal(err)
	}
	if rs, _ := s.Requests(ctx); len(rs) != 1 || rs[0].Status != StatusApproved {
		t.Errorf("requests: %+v", rs)
	}
	if _, err := s.AddLink(ctx, Link{Service: "holiday", Role: RoleSender, ImmichUserID: bob, KeyID: uuid.New(), Key: "bobkey"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveService(ctx, "holiday"); err != nil {
		t.Fatal(err)
	}
	if rest, _ := s.Services(ctx); len(rest) != 0 {
		t.Errorf("service must be gone")
	}
	if rs, _ := s.Requests(ctx); len(rs) != 0 {
		t.Errorf("its requests must be gone")
	}
	links, _ := s.Links(ctx)
	if len(links) != 1 || links[0].State != StateOrphaned {
		t.Errorf("the member's link must be kept but orphaned so they can clean up: %+v", links)
	}
}
