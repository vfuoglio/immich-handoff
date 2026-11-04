// Package store keeps the app's own state in one SQLite file: which people have joined which service.
// API keys are encrypted with the vault before they reach the disk.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite" // pure-Go driver, no CGO

	"github.com/vfuoglio/immich-handoff/internal/vault"
)

const (
	RoleSender   = "sender"
	RoleReceiver = "receiver"

	StateActive   = "active"
	StateOrphaned = "orphaned" // the service was removed by its owner; the member can still clean up
	StateBroken   = "broken"   // Immich rejected the key (it was deleted or revoked)

	VisListed = "listed" // shown to everyone signed in to Immich
	VisInvite = "invite" // reachable only through the owner's invitation link

	PolicyApproval = "approval"
	PolicyOpen     = "open"

	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusDeclined = "declined"
)

// ErrExists is returned when a person already holds that role in that service.
var ErrExists = errors.New("store: already joined")

type Store struct {
	db    *sql.DB
	vault *vault.Vault
}

// Link is one person's membership in a service, with the key the app uses on their behalf.
type Link struct {
	ID           string
	Service      string
	Role         string
	ImmichUserID uuid.UUID
	Name, Email  string
	KeyID        uuid.UUID // the key's id in Immich, so it can be deleted later
	Key          string    // decrypted; never stored in clear
	State        string
	Note         string // why the link is not active
	CreatedAt    time.Time
}

// migrations run in order; the number applied is kept in PRAGMA user_version.
var migrations = []string{
	`CREATE TABLE meta (k TEXT PRIMARY KEY, v BLOB NOT NULL);
	 CREATE TABLE links (
	   id             TEXT PRIMARY KEY,
	   service        TEXT NOT NULL,
	   role           TEXT NOT NULL CHECK (role IN ('sender','receiver')),
	   immich_user_id TEXT NOT NULL,
	   name           TEXT NOT NULL,
	   email          TEXT NOT NULL,
	   key_id         TEXT NOT NULL,
	   key_enc        BLOB NOT NULL,
	   state          TEXT NOT NULL DEFAULT 'active',
	   created_at     TEXT NOT NULL,
	   UNIQUE (service, role, immich_user_id)
	 );`,
	`CREATE TABLE services (
	   id          TEXT PRIMARY KEY,
	   name        TEXT NOT NULL UNIQUE,
	   tag         TEXT NOT NULL,
	   inbox_tag   TEXT NOT NULL,
	   refused_tag TEXT NOT NULL,
	   expiry_days INTEGER NOT NULL,
	   owner_id    TEXT NOT NULL,
	   owner_name  TEXT NOT NULL,
	   owner_email TEXT NOT NULL,
	   key_id      TEXT NOT NULL,
	   key_enc     BLOB NOT NULL,
	   policy      TEXT NOT NULL CHECK (policy IN ('approval','open')),
	   created_at  TEXT NOT NULL
	 );
	 CREATE TABLE requests (
	   id         TEXT PRIMARY KEY,
	   service    TEXT NOT NULL,
	   user_id    TEXT NOT NULL,
	   name       TEXT NOT NULL,
	   email      TEXT NOT NULL,
	   status     TEXT NOT NULL CHECK (status IN ('pending','approved','declined')),
	   created_at TEXT NOT NULL,
	   UNIQUE (service, user_id)
	 );`,
	`ALTER TABLE links ADD COLUMN note TEXT NOT NULL DEFAULT '';
	 ALTER TABLE services ADD COLUMN state TEXT NOT NULL DEFAULT 'active';
	 ALTER TABLE services ADD COLUMN note TEXT NOT NULL DEFAULT '';
	 CREATE TABLE events (
	   id      INTEGER PRIMARY KEY AUTOINCREMENT,
	   at      TEXT NOT NULL,
	   service TEXT NOT NULL,
	   user_id TEXT NOT NULL,
	   kind    TEXT NOT NULL,
	   text    TEXT NOT NULL
	 );`,
	`ALTER TABLE services ADD COLUMN visibility TEXT NOT NULL DEFAULT 'listed';
	 ALTER TABLE services ADD COLUMN invite TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE services ADD COLUMN mode TEXT NOT NULL DEFAULT 'move';
	 ALTER TABLE services ADD COLUMN sent_tag TEXT NOT NULL DEFAULT '';`,
}

// Open opens (creating if needed) the database at path and checks that v is the key it was made with.
func Open(path string, v *vault.Vault) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // a handful of rows; one connection avoids lock contention entirely
	s := &Store{db: db, vault: v}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.checkKey(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var have int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&have); err != nil {
		return err
	}
	if have > len(migrations) {
		return fmt.Errorf("store: database is from a newer version (schema %d, this build knows %d)", have, len(migrations))
	}
	for i := have; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// checkKey seals a known value on first use and verifies it afterwards, so starting with the wrong
// store key fails loudly instead of silently producing undecryptable rows.
func (s *Store) checkKey() error {
	var sealed []byte
	err := s.db.QueryRow(`SELECT v FROM meta WHERE k = 'canary'`).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		sealed, err = s.vault.Seal([]byte("immich-handoff"), "canary")
		if err != nil {
			return err
		}
		_, err = s.db.Exec(`INSERT INTO meta (k, v) VALUES ('canary', ?)`, sealed)
		return err
	}
	if err != nil {
		return err
	}
	if _, err := s.vault.Open(sealed, "canary"); err != nil {
		return errors.New("store: the store key does not match this database (was HANDOFF_STORE_KEY changed?)")
	}
	return nil
}

func (s *Store) AddLink(ctx context.Context, l Link) (Link, error) {
	l.ID = uuid.NewString()
	l.State = StateActive
	l.CreatedAt = time.Now().UTC()
	enc, err := s.vault.Seal([]byte(l.Key), l.ID)
	if err != nil {
		return l, err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO links (id, service, role, immich_user_id, name, email, key_id, key_enc, state, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		l.ID, l.Service, l.Role, l.ImmichUserID.String(), l.Name, l.Email, l.KeyID.String(), enc, l.State, l.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if isUnique(err) {
			return l, ErrExists
		}
		return l, err
	}
	return l, nil
}

func (s *Store) RemoveLink(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM links WHERE id = ?`, id)
	return err
}

// Links returns every membership with its key decrypted.
func (s *Store) Links(ctx context.Context) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, service, role, immich_user_id, name, email, key_id, key_enc, state, note, created_at FROM links ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		var user, keyID, created string
		var enc []byte
		if err := rows.Scan(&l.ID, &l.Service, &l.Role, &user, &l.Name, &l.Email, &keyID, &enc, &l.State, &l.Note, &created); err != nil {
			return nil, err
		}
		l.ImmichUserID, _ = uuid.Parse(user)
		l.KeyID, _ = uuid.Parse(keyID)
		l.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		plain, err := s.vault.Open(enc, l.ID)
		if err != nil {
			return nil, fmt.Errorf("link %s: %w", l.ID, err)
		}
		l.Key = string(plain)
		out = append(out, l)
	}
	return out, rows.Err()
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE")
}

// Service is a service someone created from the web page. Its receiver key lives in the owner's account.
type Service struct {
	ID, Name               string
	Tag, InboxTag, Refused string
	ExpiryDays             int
	OwnerID                uuid.UUID
	OwnerName, OwnerEmail  string
	KeyID                  uuid.UUID // the receiver key's id in Immich
	Key                    string    // decrypted
	Policy                 string
	State, Note            string // healthy ("active") or why not
	Visibility, Invite     string // who can see it; the invitation token (empty until one is made)
	Mode, SentTag          string // "move" or "copy"; the tag put on originals that were copied
	CreatedAt              time.Time
}

// Request is someone asking to send to a service that wants approval.
type Request struct {
	ID, Service string
	UserID      uuid.UUID
	Name, Email string
	Status      string
	CreatedAt   time.Time
}

func (s *Store) AddService(ctx context.Context, v Service) (Service, error) {
	if v.Visibility == "" {
		v.Visibility = VisListed
	}
	if v.Mode == "" {
		v.Mode = "copy"
	}
	v.ID = uuid.NewString()
	v.CreatedAt = time.Now().UTC()
	enc, err := s.vault.Seal([]byte(v.Key), v.ID)
	if err != nil {
		return v, err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO services (id, name, tag, inbox_tag, refused_tag, expiry_days, owner_id, owner_name, owner_email, key_id, key_enc, policy, visibility, invite, mode, sent_tag, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.ID, v.Name, v.Tag, v.InboxTag, v.Refused, v.ExpiryDays, v.OwnerID.String(), v.OwnerName, v.OwnerEmail, v.KeyID.String(), enc, v.Policy, v.Visibility, v.Invite, v.Mode, v.SentTag, v.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if isUnique(err) {
			return v, ErrExists
		}
		return v, err
	}
	return v, nil
}

func (s *Store) Services(ctx context.Context) ([]Service, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, tag, inbox_tag, refused_tag, expiry_days, owner_id, owner_name, owner_email, key_id, key_enc, policy, state, note, visibility, invite, mode, sent_tag, created_at FROM services ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Service
	for rows.Next() {
		var v Service
		var owner, keyID, created string
		var enc []byte
		if err := rows.Scan(&v.ID, &v.Name, &v.Tag, &v.InboxTag, &v.Refused, &v.ExpiryDays, &owner, &v.OwnerName, &v.OwnerEmail, &keyID, &enc, &v.Policy, &v.State, &v.Note, &v.Visibility, &v.Invite, &v.Mode, &v.SentTag, &created); err != nil {
			return nil, err
		}
		v.OwnerID, _ = uuid.Parse(owner)
		v.KeyID, _ = uuid.Parse(keyID)
		v.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		plain, err := s.vault.Open(enc, v.ID)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", v.ID, err)
		}
		v.Key = string(plain)
		out = append(out, v)
	}
	return out, rows.Err()
}

// RemoveService deletes the service and its requests, and marks its members' links orphaned.
func (s *Store) RemoveService(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`UPDATE links SET state = 'orphaned' WHERE service = ?`,
		`DELETE FROM requests WHERE service = ?`,
		`DELETE FROM services WHERE name = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) AddRequest(ctx context.Context, r Request) (Request, error) {
	r.ID = uuid.NewString()
	r.Status = StatusPending
	r.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO requests (id, service, user_id, name, email, status, created_at) VALUES (?,?,?,?,?,?,?)`,
		r.ID, r.Service, r.UserID.String(), r.Name, r.Email, r.Status, r.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if isUnique(err) {
			return r, ErrExists
		}
		return r, err
	}
	return r, nil
}

func (s *Store) Requests(ctx context.Context) ([]Request, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, service, user_id, name, email, status, created_at FROM requests ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Request
	for rows.Next() {
		var r Request
		var user, created string
		if err := rows.Scan(&r.ID, &r.Service, &user, &r.Name, &r.Email, &r.Status, &created); err != nil {
			return nil, err
		}
		r.UserID, _ = uuid.Parse(user)
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) SetRequestStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE requests SET status = ? WHERE id = ?`, status, id)
	return err
}

func (s *Store) RemoveRequest(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM requests WHERE id = ?`, id)
	return err
}

// AllLinks returns memberships in every state (Links is the same; kept for readability at call sites).
func (s *Store) AllLinks(ctx context.Context) ([]Link, error) { return s.Links(ctx) }

// SetLinkState records a link's health. It reports whether a link with that id exists.
func (s *Store) SetLinkState(ctx context.Context, id, state, note string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE links SET state = ?, note = ? WHERE id = ?`, state, note, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetServiceState records a service's health. It reports whether a service with that id exists.
func (s *Store) SetServiceState(ctx context.Context, id, state, note string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE services SET state = ?, note = ? WHERE id = ?`, state, note, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReplaceServiceKey swaps in a new receiver key and marks the service healthy again.
func (s *Store) ReplaceServiceKey(ctx context.Context, id string, keyID uuid.UUID, key string) error {
	enc, err := s.vault.Seal([]byte(key), id)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE services SET key_id = ?, key_enc = ?, state = 'active', note = '' WHERE id = ?`, keyID.String(), enc, id)
	return err
}

// Event is one line of the activity log.
type Event struct {
	At      time.Time
	Service string
	UserID  uuid.UUID // who it is about
	Kind    string
	Text    string
}

const maxEvents = 500

// AddEvent appends to the activity log, which keeps only the most recent entries.
func (s *Store) AddEvent(ctx context.Context, e Event) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO events (at, service, user_id, kind, text) VALUES (?,?,?,?,?)`,
		time.Now().UTC().Format(time.RFC3339Nano), e.Service, e.UserID.String(), e.Kind, e.Text); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE id <= (SELECT MAX(id) FROM events) - ?`, maxEvents)
	return err
}

// Events returns the most recent entries, newest first.
func (s *Store) Events(ctx context.Context, limit int) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT at, service, user_id, kind, text FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var at, user string
		if err := rows.Scan(&at, &e.Service, &user, &e.Kind, &e.Text); err != nil {
			return nil, err
		}
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		e.UserID, _ = uuid.Parse(user)
		out = append(out, e)
	}
	return out, rows.Err()
}

// SetServiceVisibility changes who can see a service and sets its invitation token.
func (s *Store) SetServiceVisibility(ctx context.Context, id, visibility, invite string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE services SET visibility = ?, invite = ? WHERE id = ?`, visibility, invite, id)
	return err
}
