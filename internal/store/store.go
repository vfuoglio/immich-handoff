// Package store keeps the app's own state in one SQLite file: which people have joined which service.
// API keys are encrypted with the vault before they reach the disk.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
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

// ErrNotFound is returned when the membership or service asked about does not exist.
var ErrNotFound = errors.New("store: not found")

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
	Note         string    // why the link is not active
	Mode         string    // "copy" keeps the sender's originals, "move" trashes them; chosen by the sender
	AlbumID      uuid.UUID // an album of the sender's whose photos are all handed over; zero: none
	AlbumName    string    // its name when it was linked, for display
	Paused       bool      // the sender paused: their tagged photos wait, tagged, until they resume
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
	// The sender, not the service owner, decides what happens to their originals. Existing members keep
	// what they had: the mode of their service, or move for services from the configuration.
	`ALTER TABLE links ADD COLUMN mode TEXT NOT NULL DEFAULT 'move';
	 UPDATE links SET mode = COALESCE((SELECT mode FROM services WHERE services.name = links.service), 'move');`,
	// A service may name an album that handed-over photos are put in.
	`ALTER TABLE services ADD COLUMN album TEXT NOT NULL DEFAULT '';`,
	// A sender may link one of their own albums to a service: everything in it is handed over.
	`ALTER TABLE links ADD COLUMN album_id TEXT NOT NULL DEFAULT '';
	 ALTER TABLE links ADD COLUMN album_name TEXT NOT NULL DEFAULT '';`,
	// A sender or an owner may pause; an owner may cap what each sender hands over in a day. handovers records
	// what each sender handed over (for the cap), and only the last few days of it are kept.
	`ALTER TABLE links ADD COLUMN paused INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE services ADD COLUMN paused INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE services ADD COLUMN limit_photos INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE services ADD COLUMN limit_mb INTEGER NOT NULL DEFAULT 0;
	 CREATE TABLE handovers (
	   link_id TEXT NOT NULL,
	   at      TEXT NOT NULL,
	   bytes   INTEGER NOT NULL
	 );
	 CREATE INDEX handovers_link ON handovers (link_id, at);`,
	// The first-run guide on the Overview is shown until a person has finished or hidden it.
	`CREATE TABLE guide (
	   user_id TEXT PRIMARY KEY,
	   done_at TEXT NOT NULL
	 );`,
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

// Backup writes a consistent copy of the whole database to dest, which must not exist, while the app keeps
// using it. The keys in it stay encrypted with the store key. The copy is readable by its owner only.
func (s *Store) Backup(ctx context.Context, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("store: %s already exists", dest)
	}
	// VACUUM INTO does not take parameters for the file name, so it is quoted as an SQL string
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO '`+strings.ReplaceAll(dest, `'`, `''`)+`'`); err != nil {
		os.Remove(dest)
		return fmt.Errorf("store: backup: %w", err)
	}
	return os.Chmod(dest, 0o600)
}

// Check runs SQLite's own integrity check and reports what it found when it is not "ok".
func (s *Store) Check(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return err
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("store: the file is damaged: %s", strings.Join(problems, "; "))
	}
	return rows.Err()
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
	if l.Mode == "" {
		l.Mode = "copy"
	}
	l.ID = uuid.NewString()
	l.State = StateActive
	l.CreatedAt = time.Now().UTC()
	enc, err := s.vault.Seal([]byte(l.Key), l.ID)
	if err != nil {
		return l, err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO links (id, service, role, immich_user_id, name, email, key_id, key_enc, state, mode, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		l.ID, l.Service, l.Role, l.ImmichUserID.String(), l.Name, l.Email, l.KeyID.String(), enc, l.State, l.Mode, l.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if isUnique(err) {
			return l, ErrExists
		}
		return l, err
	}
	return l, nil
}

// SetLinkAlbum links an album of the sender's to this membership (the zero id unlinks it).
func (s *Store) SetLinkAlbum(ctx context.Context, id string, album uuid.UUID, name string) error {
	albumID := ""
	if album != uuid.Nil {
		albumID = album.String()
	} else {
		name = ""
	}
	res, err := s.db.ExecContext(ctx, `UPDATE links SET album_id = ?, album_name = ? WHERE id = ?`, albumID, name, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ForgetLinkKey overwrites the stored API key of a membership with nothing. The membership stays (so the person
// still sees why it ended and can clean up), but the app no longer holds a key that works.
func (s *Store) ForgetLinkKey(ctx context.Context, id string) error {
	enc, err := s.vault.Seal(nil, id)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE links SET key_enc = ? WHERE id = ?`, enc, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RemoveLink(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM handovers WHERE link_id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM links WHERE id = ?`, id)
	return err
}

// SetLinkPaused records that a sender paused or resumed their membership.
func (s *Store) SetLinkPaused(ctx context.Context, id string, paused bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE links SET paused = ? WHERE id = ?`, b2i(paused), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetServicePaused records that the owner paused or resumed a service.
func (s *Store) SetServicePaused(ctx context.Context, name string, paused bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE services SET paused = ? WHERE name = ?`, b2i(paused), name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetServiceLimits sets the most a sender may hand over to the service in a day: photos and megabytes, 0 for none.
func (s *Store) SetServiceLimits(ctx context.Context, name string, photos, megabytes int) error {
	res, err := s.db.ExecContext(ctx, `UPDATE services SET limit_photos = ?, limit_mb = ? WHERE name = ?`, photos, megabytes, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// HandoverRetention is how long a record of a hand-over is kept: the daily cap looks back one day and the
// Overview's chart a week.
const HandoverRetention = 8 * 24 * time.Hour

// AddHandover records a photo a sender handed over, so the daily cap can be checked and the Overview can chart
// the week. Entries older than HandoverRetention are dropped.
func (s *Store) AddHandover(ctx context.Context, linkID string, bytes int64) error {
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO handovers (link_id, at, bytes) VALUES (?,?,?)`, linkID, now.Format(time.RFC3339Nano), bytes); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM handovers WHERE at < ?`, now.Add(-HandoverRetention).Format(time.RFC3339Nano))
	return err
}

// Handovers is what the sender handed over since the given time: how many photos, how many bytes, and when
// the earliest of them was (the zero time when there were none).
func (s *Store) Handovers(ctx context.Context, linkID string, since time.Time) (photos int, bytes int64, oldest time.Time, err error) {
	var first sql.NullString
	var total sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(bytes), 0), MIN(at) FROM handovers WHERE link_id = ? AND at >= ?`,
		linkID, since.UTC().Format(time.RFC3339Nano)).Scan(&photos, &total, &first)
	if err != nil {
		return 0, 0, time.Time{}, err
	}
	bytes = total.Int64
	if first.Valid {
		oldest, _ = time.Parse(time.RFC3339Nano, first.String)
	}
	return photos, bytes, oldest, nil
}

// Links returns every membership with its key decrypted.
func (s *Store) Links(ctx context.Context) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, service, role, immich_user_id, name, email, key_id, key_enc, state, note, mode, album_id, album_name, paused, created_at FROM links ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		var user, keyID, albumID, created string
		var enc []byte
		if err := rows.Scan(&l.ID, &l.Service, &l.Role, &user, &l.Name, &l.Email, &keyID, &enc, &l.State, &l.Note, &l.Mode, &albumID, &l.AlbumName, &l.Paused, &created); err != nil {
			return nil, err
		}
		l.AlbumID, _ = uuid.Parse(albumID)
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
	Album                  string // name of the album handed-over photos are put in, in the owner's account; empty: none
	Paused                 bool   // the owner paused the service: nothing is handed over until they resume
	LimitPhotos, LimitMB   int    // most a sender may hand over in a day (photos, megabytes); 0: no limit
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
	v.ID = uuid.NewString()
	v.CreatedAt = time.Now().UTC()
	enc, err := s.vault.Seal([]byte(v.Key), v.ID)
	if err != nil {
		return v, err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO services (id, name, tag, inbox_tag, refused_tag, expiry_days, owner_id, owner_name, owner_email, key_id, key_enc, policy, visibility, invite, mode, sent_tag, album, limit_photos, limit_mb, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.ID, v.Name, v.Tag, v.InboxTag, v.Refused, v.ExpiryDays, v.OwnerID.String(), v.OwnerName, v.OwnerEmail, v.KeyID.String(), enc, v.Policy, v.Visibility, v.Invite, v.Mode, v.SentTag, v.Album, v.LimitPhotos, v.LimitMB, v.CreatedAt.Format(time.RFC3339Nano))
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
		`SELECT id, name, tag, inbox_tag, refused_tag, expiry_days, owner_id, owner_name, owner_email, key_id, key_enc, policy, state, note, visibility, invite, mode, sent_tag, album, paused, limit_photos, limit_mb, created_at FROM services ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Service
	for rows.Next() {
		var v Service
		var owner, keyID, created string
		var enc []byte
		if err := rows.Scan(&v.ID, &v.Name, &v.Tag, &v.InboxTag, &v.Refused, &v.ExpiryDays, &owner, &v.OwnerName, &v.OwnerEmail, &keyID, &enc, &v.Policy, &v.State, &v.Note, &v.Visibility, &v.Invite, &v.Mode, &v.SentTag, &v.Album, &v.Paused, &v.LimitPhotos, &v.LimitMB, &created); err != nil {
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
		`UPDATE links SET state = 'orphaned', paused = 0 WHERE service = ?`,
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

// HandoverDays counts, per day, the photos handed over through these memberships since the given time. The days
// are UTC dates ("2026-11-06"); a day without any is simply absent.
func (s *Store) HandoverDays(ctx context.Context, linkIDs []string, since time.Time) (map[string]int, error) {
	out := map[string]int{}
	if len(linkIDs) == 0 {
		return out, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(linkIDs)), ",")
	args := make([]any, 0, len(linkIDs)+1)
	for _, id := range linkIDs {
		args = append(args, id)
	}
	args = append(args, since.UTC().Format(time.RFC3339Nano))
	rows, err := s.db.QueryContext(ctx, `SELECT substr(at, 1, 10), COUNT(*) FROM handovers WHERE link_id IN (`+marks+`) AND at >= ? GROUP BY 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var day string
		var n int
		if err := rows.Scan(&day, &n); err != nil {
			return nil, err
		}
		out[day] = n
	}
	return out, rows.Err()
}

// GuideDone reports whether the person has finished or hidden the first-run guide.
func (s *Store) GuideDone(ctx context.Context, user uuid.UUID) (bool, error) {
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT done_at FROM guide WHERE user_id = ?`, user.String()).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// SetGuideDone remembers that the person finished or hid the first-run guide.
func (s *Store) SetGuideDone(ctx context.Context, user uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO guide (user_id, done_at) VALUES (?, ?)`, user.String(), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}
