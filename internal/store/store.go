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

	StateActive = "active"
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
		l.ID, l.Service, l.Role, l.ImmichUserID.String(), l.Name, l.Email, l.KeyID.String(), enc, l.State, l.CreatedAt.Format(time.RFC3339))
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
		`SELECT id, service, role, immich_user_id, name, email, key_id, key_enc, state, created_at FROM links ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		var user, keyID, created string
		var enc []byte
		if err := rows.Scan(&l.ID, &l.Service, &l.Role, &user, &l.Name, &l.Email, &keyID, &enc, &l.State, &created); err != nil {
			return nil, err
		}
		l.ImmichUserID, _ = uuid.Parse(user)
		l.KeyID, _ = uuid.Parse(keyID)
		l.CreatedAt, _ = time.Parse(time.RFC3339, created)
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
