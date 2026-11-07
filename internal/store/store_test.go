package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestHealthStatesKeyReplacementAndEvents(t *testing.T) {
	ctx := context.Background()
	s, err := open(t, t.TempDir(), "first-secret-value-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := uuid.New()
	svc, _ := s.AddService(ctx, Service{Name: "x", Tag: "t", InboxTag: "i", Refused: "r", ExpiryDays: 1, OwnerID: owner, KeyID: uuid.New(), Key: "old", Policy: PolicyOpen})
	link, _ := s.AddLink(ctx, Link{Service: "x", Role: RoleSender, ImmichUserID: uuid.New(), KeyID: uuid.New(), Key: "k"})

	if ok, err := s.SetLinkState(ctx, link.ID, StateBroken, "key deleted"); !ok || err != nil {
		t.Fatalf("link state: %v %v", ok, err)
	}
	if ok, _ := s.SetLinkState(ctx, "nope", StateBroken, ""); ok {
		t.Error("an unknown link must report not found")
	}
	if ok, _ := s.SetServiceState(ctx, svc.ID, StateBroken, "receiver key deleted"); !ok {
		t.Fatal("service state")
	}
	links, _ := s.Links(ctx)
	svcs, _ := s.Services(ctx)
	if links[0].State != StateBroken || links[0].Note != "key deleted" || svcs[0].State != StateBroken {
		t.Fatalf("states not stored: %+v %+v", links[0], svcs[0])
	}
	if svcs[0].Visibility != VisListed || svcs[0].Invite != "" {
		t.Errorf("services are listed by default: %+v", svcs[0])
	}
	if err := s.SetServiceVisibility(ctx, svc.ID, VisInvite, "tok"); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Services(ctx); again[0].Visibility != VisInvite || again[0].Invite != "tok" {
		t.Errorf("visibility not stored: %+v", again[0])
	}
	newKey := uuid.New()
	if err := s.ReplaceServiceKey(ctx, svc.ID, newKey, "new-key"); err != nil {
		t.Fatal(err)
	}
	svcs, _ = s.Services(ctx)
	if svcs[0].Key != "new-key" || svcs[0].KeyID != newKey || svcs[0].State != StateActive || svcs[0].Note != "" {
		t.Errorf("replace: %+v", svcs[0])
	}
	for i := 0; i < maxEvents+20; i++ {
		s.AddEvent(ctx, Event{Service: "x", UserID: owner, Kind: "test", Text: "e"})
	}
	evs, _ := s.Events(ctx, 1000)
	if len(evs) > maxEvents+1 || len(evs) < maxEvents {
		t.Errorf("the log must stay bounded, got %d", len(evs))
	}
}

// Members who joined before senders could choose keep what they had: the mode of their service, or move
// for services from the configuration (their keys were made with delete permission).
func TestMigrationKeepsExistingMembersBehaviour(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "handoff.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:5] {
		if _, err := db.Exec(m); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec(`PRAGMA user_version = 5`)
	now := time.Now().Format(time.RFC3339Nano)
	must := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"keepers", "movers"} {
		mode := map[string]string{"keepers": "copy", "movers": "move"}[name]
		must(`INSERT INTO services (id,name,tag,inbox_tag,refused_tag,expiry_days,owner_id,owner_name,owner_email,key_id,key_enc,policy,created_at,mode)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, name, name, "t", "i", "r", 1, "o", "o", "o", "k", []byte("x"), "open", now, mode)
	}
	for id, svc := range map[string]string{"a": "keepers", "b": "movers", "c": "configured-service"} {
		must(`INSERT INTO links (id,service,role,immich_user_id,name,email,key_id,key_enc,state,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, svc, "sender", id, id, id, "k", []byte("x"), "active", now)
	}
	db.Close()

	v, _ := vault.New("first-secret-value-1234")
	s, err := Open(path, v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got := map[string]string{}
	rows, _ := s.db.Query(`SELECT id, mode FROM links`)
	for rows.Next() {
		var id, mode string
		rows.Scan(&id, &mode)
		got[id] = mode
	}
	if got["a"] != "copy" || got["b"] != "move" || got["c"] != "move" {
		t.Errorf("existing members must keep their behaviour: %v", got)
	}
}

func TestAServicesAlbumSurvivesARestart(t *testing.T) {
	dir, ctx := t.TempDir(), context.Background()
	s, err := open(t, dir, "first-secret-value-1234")
	if err != nil {
		t.Fatal(err)
	}
	owner := uuid.New()
	for _, v := range []Service{
		{Name: "filed", Tag: "t1", InboxTag: "i1", Refused: "r", ExpiryDays: 30, OwnerID: owner, KeyID: uuid.New(), Key: "k1", Policy: PolicyOpen, Album: "Holiday 2026"},
		{Name: "plain", Tag: "t2", InboxTag: "i2", Refused: "r", ExpiryDays: 30, OwnerID: owner, KeyID: uuid.New(), Key: "k2", Policy: PolicyOpen},
	} {
		if _, err := s.AddService(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s2, err := open(t, dir, "first-secret-value-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.Services(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("services: %v %v", got, err)
	}
	for _, v := range got {
		want := map[string]string{"filed": "Holiday 2026", "plain": ""}[v.Name]
		if v.Album != want {
			t.Errorf("%s: album %q, want %q", v.Name, v.Album, want)
		}
	}
}

func TestALinkedAlbumIsStoredChangedAndCleared(t *testing.T) {
	dir, ctx := t.TempDir(), context.Background()
	s, err := open(t, dir, "first-secret-value-1234")
	if err != nil {
		t.Fatal(err)
	}
	user, album := uuid.New(), uuid.New()
	l, err := s.AddLink(ctx, Link{Service: "holiday", Role: RoleSender, ImmichUserID: user, Name: "Bob", Email: "b@x", KeyID: uuid.New(), Key: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetLinkAlbum(ctx, l.ID, album, "To send"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := open(t, dir, "first-secret-value-1234")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if links, _ := s2.Links(ctx); len(links) != 1 || links[0].AlbumID != album || links[0].AlbumName != "To send" {
		t.Fatalf("the album must survive a restart: %+v", links)
	}
	if err := s2.SetLinkAlbum(ctx, l.ID, uuid.Nil, "ignored"); err != nil {
		t.Fatal(err)
	}
	if links, _ := s2.Links(ctx); links[0].AlbumID != uuid.Nil || links[0].AlbumName != "" {
		t.Errorf("unlinking clears both fields: %+v", links)
	}
	if err := s2.SetLinkAlbum(ctx, "no-such-link", album, "x"); err != ErrNotFound {
		t.Errorf("a missing membership: %v", err)
	}
}

func TestHandoverDaysAreCountedPerDayAndOldOnesForgotten(t *testing.T) {
	st, err := open(t, t.TempDir(), "0123456789abcdef0123")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	// the fake clock: rows are written with their own timestamps by going through AddHandover then rewriting "at"
	for i := 0; i < 3; i++ {
		if err := st.AddHandover(ctx, "link-a", 1000); err != nil {
			t.Fatal(err)
		}
	}
	st.AddHandover(ctx, "link-b", 500)
	st.AddHandover(ctx, "link-other", 500)
	days, err2 := st.HandoverDays(ctx, []string{"link-a", "link-b"}, time.Now().Add(-time.Hour))
	if err2 != nil {
		t.Fatal(err2)
	}
	today := time.Now().UTC().Format("2006-01-02")
	if days[today] != 4 || len(days) != 1 {
		t.Errorf("per day: %v (link-other must not count)", days)
	}
	if got, _ := st.HandoverDays(ctx, nil, time.Now().Add(-time.Hour)); len(got) != 0 {
		t.Errorf("no memberships, no days: %v", got)
	}
	// records older than the retention are dropped the next time one is added
	old := time.Now().UTC().Add(-HandoverRetention - time.Hour).Format(time.RFC3339Nano)
	if _, err := st.db.ExecContext(ctx, `INSERT INTO handovers (link_id, at, bytes) VALUES ('link-a', ?, 1)`, old); err != nil {
		t.Fatal(err)
	}
	st.AddHandover(ctx, "link-a", 1)
	photos, _, _, _ := st.Handovers(ctx, "link-a", time.Now().Add(-30*24*time.Hour))
	if photos != 4 {
		t.Errorf("the record past the retention must be gone, link-a has %d (want 3 + 1 new)", photos)
	}
}

func TestTheFirstRunGuideIsRememberedPerPerson(t *testing.T) {
	st, err := open(t, t.TempDir(), "0123456789abcdef0123")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	if done, err := st.GuideDone(ctx, a); err != nil || done {
		t.Fatalf("a new person has not finished it: %v %v", done, err)
	}
	if err := st.SetGuideDone(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGuideDone(ctx, a); err != nil {
		t.Errorf("hiding it twice is fine: %v", err)
	}
	if done, _ := st.GuideDone(ctx, a); !done {
		t.Error("it must stay finished")
	}
	if done, _ := st.GuideDone(ctx, b); done {
		t.Error("it is remembered per person")
	}
}
