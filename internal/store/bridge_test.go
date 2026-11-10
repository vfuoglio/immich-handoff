package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBridgeRoundTripStatesAndEncryption(t *testing.T) {
	dir, ctx := t.TempDir(), context.Background()
	s, err := open(t, dir, "first-secret-value-1234")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bridge(ctx, "holiday"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no bridge yet: %v", err)
	}
	checked := time.Now().Add(-time.Minute)
	b := Bridge{Service: "holiday", Address: "https://photos.example.org", Key: "REMOTE-SECRET-KEY", DisplayName: "Aunt Lia", Version: "v3.2.0", CheckedAt: checked}
	if err := s.SetBridge(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, err := s.Bridge(ctx, "holiday")
	if err != nil || got.Key != "REMOTE-SECRET-KEY" || got.Address != b.Address || got.DisplayName != "Aunt Lia" ||
		got.State != BridgeOK || got.Version != "v3.2.0" || got.CheckedAt.Sub(checked).Abs() > time.Second || got.CreatedAt.IsZero() {
		t.Fatalf("bridge: %+v %v", got, err)
	}

	// a state change is reported once, and keeps the version when none is given
	retry := time.Now().Add(time.Hour)
	changed, err := s.SetBridgeState(ctx, "holiday", BridgeUnreachable, "timed out", "", 2, retry)
	if err != nil || !changed {
		t.Fatalf("first change: %v %v", changed, err)
	}
	changed, _ = s.SetBridgeState(ctx, "holiday", BridgeUnreachable, "timed out again", "", 3, retry)
	if changed {
		t.Fatal("the same state again is not a change")
	}
	got, _ = s.Bridge(ctx, "holiday")
	if got.State != BridgeUnreachable || got.Note != "timed out again" || got.Failures != 3 || got.Version != "v3.2.0" || got.RetryAt.Sub(retry).Abs() > time.Second {
		t.Fatalf("after failing: %+v", got)
	}
	if changed, _ = s.SetBridgeState(ctx, "holiday", BridgeOK, "ignored", "v3.3.0", 9, retry); !changed {
		t.Fatal("recovery is a change")
	}
	got, _ = s.Bridge(ctx, "holiday")
	if got.State != BridgeOK || got.Note != "" || got.Failures != 0 || !got.RetryAt.IsZero() || got.Version != "v3.3.0" {
		t.Fatalf("after recovering: %+v", got)
	}
	if _, err := s.SetBridgeState(ctx, "nothing", BridgeRevoked, "x", "", 0, time.Time{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown service: %v", err)
	}

	// replacing the key (or the address) starts again from ok, one bridge per service
	s.SetBridgeState(ctx, "holiday", BridgeRevoked, "401", "", 0, time.Time{})
	if err := s.SetBridge(ctx, Bridge{Service: "holiday", Address: "https://new.example.org", Key: "SECOND-KEY", DisplayName: "Aunt Lia"}); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.Bridges(ctx); len(all) != 1 || all[0].Key != "SECOND-KEY" || all[0].State != BridgeOK || all[0].Address != "https://new.example.org" {
		t.Fatalf("replaced: %+v", all)
	}
	s.Close()

	raw, _ := readAll(filepath.Join(dir, "handoff.db"))
	if strings.Contains(string(raw), "SECOND-KEY") || strings.Contains(string(raw), "REMOTE-SECRET-KEY") {
		t.Error("the remote key is on disk in clear text")
	}
	// it survives a restart, and a different store key cannot read it
	s2, err := open(t, dir, "first-secret-value-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if got, err := s2.Bridge(ctx, "holiday"); err != nil || got.Key != "SECOND-KEY" {
		t.Fatalf("after reopening: %+v %v", got, err)
	}
}

func TestABridgeGoesWithItsService(t *testing.T) {
	ctx := context.Background()
	s, err := open(t, t.TempDir(), "first-secret-value-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, n := range []string{"holiday", "school"} {
		if _, err := s.AddService(ctx, Service{Name: n, Tag: n, InboxTag: "inbox", Refused: "r", ExpiryDays: 30, OwnerID: uuid.New(), OwnerName: "Ann", OwnerEmail: "a@x", KeyID: uuid.New(), Key: "k", Policy: PolicyOpen}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetBridge(ctx, Bridge{Service: n, Address: "https://" + n + ".example.org", Key: "key-" + n, DisplayName: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RemoveService(ctx, "holiday"); err != nil {
		t.Fatal(err)
	}
	all, _ := s.Bridges(ctx)
	if len(all) != 1 || all[0].Service != "school" {
		t.Fatalf("removing a service must remove its bridge and only its: %+v", all)
	}
	if err := s.RemoveBridge(ctx, "school"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveBridge(ctx, "school"); err != nil {
		t.Fatalf("removing none is not an error: %v", err)
	}
	if all, _ := s.Bridges(ctx); len(all) != 0 {
		t.Fatal("still there")
	}
}
