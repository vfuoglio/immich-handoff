package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// States of a drop box. A revoked box answers like one that never existed; its row stays for a while so its owner
// can still read what happened.
const (
	DropActive  = "active"
	DropPaused  = "paused"
	DropRevoked = "revoked"
)

// Kinds of entries in a drop box's log.
const (
	DropCreated  = "created"
	DropUploaded = "uploaded"
	DropRejected = "rejected" // a file that was refused: its type, its size, a limit
	DropBlocked  = "blocked"  // a guest was turned away: too many requests, a wrong password
	DropPausedOn = "paused"
	DropResumed  = "resumed"
	DropRevokedK = "revoked"
	DropExpired  = "expired"
)

// DropBox is a link through which people without an Immich account send photos to its owner.
type DropBox struct {
	ID, Token, Name, Message string
	OwnerID                  uuid.UUID
	OwnerName                string
	KeyID                    uuid.UUID // the owner's key that stores the photos
	Key                      string    // decrypted
	InboxTag                 string
	ExpiresAt                time.Time // zero: none
	MaxFiles, MaxMB          int       // the most the box takes in all; 0: no limit
	MaxFileMB                int       // the largest single file; 0: no limit
	PhotosOnly               bool      // no videos
	Password                 string    // salted hash; empty: none
	State, Note              string
	Files                    int
	Bytes                    int64
	CreatedAt, EndedAt       time.Time
}

// Expired reports whether the box has passed its end.
func (b DropBox) Expired(now time.Time) bool {
	return !b.ExpiresAt.IsZero() && !now.Before(b.ExpiresAt)
}

// Open reports whether guests may use the box now.
func (b DropBox) Open(now time.Time) bool { return b.State == DropActive && !b.Expired(now) }

// DropEvent is one entry in a drop box's log.
type DropEvent struct {
	At         time.Time
	Kind, Text string
	Who        string // a short, one-way mark of the guest's address; never the address
}

// ErrDropBoxGone is returned when a box is not there, or no longer takes uploads.
var ErrDropBoxGone = errors.New("store: no such drop box")

// ErrDropFull is returned when a box has taken all it was set to take.
var ErrDropFull = errors.New("store: the drop box is full")

const dropCols = `id, token, name, message, owner_id, owner_name, key_id, key_enc, inbox_tag, expires_at, max_files, max_mb, max_file_mb, photos_only, password, state, note, files, bytes, created_at, ended_at`

// AddDropBox stores a new box. Its id is made here.
func (s *Store) AddDropBox(ctx context.Context, b DropBox) (DropBox, error) {
	b.ID = uuid.NewString()
	b.CreatedAt = time.Now().UTC()
	if b.State == "" {
		b.State = DropActive
	}
	enc, err := s.vault.Seal([]byte(b.Key), b.ID)
	if err != nil {
		return b, err
	}
	exp := ""
	if !b.ExpiresAt.IsZero() {
		exp = b.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO dropboxes (`+dropCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		b.ID, b.Token, b.Name, b.Message, b.OwnerID.String(), b.OwnerName, b.KeyID.String(), enc, b.InboxTag, exp,
		b.MaxFiles, b.MaxMB, b.MaxFileMB, b2i(b.PhotosOnly), b.Password, b.State, b.Note, 0, 0,
		b.CreatedAt.Format(time.RFC3339Nano), "")
	return b, err
}

func (s *Store) scanDropBox(sc interface{ Scan(...any) error }) (DropBox, error) {
	var b DropBox
	var owner, keyID, exp, created, ended string
	var enc []byte
	if err := sc.Scan(&b.ID, &b.Token, &b.Name, &b.Message, &owner, &b.OwnerName, &keyID, &enc, &b.InboxTag, &exp,
		&b.MaxFiles, &b.MaxMB, &b.MaxFileMB, &b.PhotosOnly, &b.Password, &b.State, &b.Note, &b.Files, &b.Bytes, &created, &ended); err != nil {
		return b, err
	}
	b.OwnerID, _ = uuid.Parse(owner)
	b.KeyID, _ = uuid.Parse(keyID)
	b.ExpiresAt, _ = time.Parse(time.RFC3339Nano, exp)
	b.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	b.EndedAt, _ = time.Parse(time.RFC3339Nano, ended)
	if b.State != DropRevoked {
		plain, err := s.vault.Open(enc, b.ID)
		if err != nil {
			return b, fmt.Errorf("drop box %s: %w", b.ID, err)
		}
		b.Key = string(plain)
	}
	return b, nil
}

// DropBoxes returns every box, oldest first.
func (s *Store) DropBoxes(ctx context.Context) ([]DropBox, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+dropCols+` FROM dropboxes ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DropBox
	for rows.Next() {
		b, err := s.scanDropBox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DropBoxByToken finds the box a link opens. A revoked box is not found.
func (s *Store) DropBoxByToken(ctx context.Context, token string) (DropBox, error) {
	b, err := s.scanDropBox(s.db.QueryRowContext(ctx, `SELECT `+dropCols+` FROM dropboxes WHERE token = ? AND state <> ?`, token, DropRevoked))
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrDropBoxGone
	}
	return b, err
}

// DropBox finds a box by its id.
func (s *Store) DropBox(ctx context.Context, id string) (DropBox, error) {
	b, err := s.scanDropBox(s.db.QueryRowContext(ctx, `SELECT `+dropCols+` FROM dropboxes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrDropBoxGone
	}
	return b, err
}

// SetDropBoxState pauses, resumes or revokes a box. A revoked box loses its key here (the caller deletes the key in
// Immich) and its link stops working for good. It reports whether anything changed.
func (s *Store) SetDropBoxState(ctx context.Context, id, state, note string) (bool, error) {
	q, args := `UPDATE dropboxes SET state = ?, note = ? WHERE id = ? AND state <> ? AND state <> ?`, []any{state, note, id, DropRevoked, state}
	if state == DropRevoked {
		q = `UPDATE dropboxes SET state = ?, note = ?, key_enc = x'', ended_at = ? WHERE id = ? AND state <> ?`
		args = []any{state, note, time.Now().UTC().Format(time.RFC3339Nano), id, DropRevoked}
	}
	r, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

// TakeDropSlot reserves room for one more file before it is stored, so two guests at once cannot together go past
// the limit on the number of files, and returns the box as it was. It fails with ErrDropFull when the box has taken
// all it was set to take. Give the slot back with ReleaseDropSlot when the file was not kept.
func (s *Store) TakeDropSlot(ctx context.Context, id string) (DropBox, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DropBox{}, err
	}
	defer tx.Rollback()
	b, err := s.scanDropBox(tx.QueryRowContext(ctx, `SELECT `+dropCols+` FROM dropboxes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrDropBoxGone
	} else if err != nil {
		return b, err
	}
	if !b.Open(time.Now()) {
		return b, ErrDropBoxGone
	}
	if (b.MaxFiles > 0 && b.Files >= b.MaxFiles) || (b.MaxMB > 0 && b.Bytes >= int64(b.MaxMB)<<20) {
		return b, ErrDropFull
	}
	if _, err := tx.ExecContext(ctx, `UPDATE dropboxes SET files = files + 1 WHERE id = ?`, id); err != nil {
		return b, err
	}
	return b, tx.Commit()
}

// ReleaseDropSlot undoes TakeDropSlot for a file that was not kept.
func (s *Store) ReleaseDropSlot(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dropboxes SET files = MAX(files - 1, 0) WHERE id = ?`, id)
	return err
}

// AddDropBytes counts the bytes of a file that was kept.
func (s *Store) AddDropBytes(ctx context.Context, id string, n int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dropboxes SET bytes = bytes + ? WHERE id = ?`, n, id)
	return err
}

const maxDropEvents = 200

// AddDropEvent appends to a box's log, which keeps its latest entries.
func (s *Store) AddDropEvent(ctx context.Context, id, kind, text, who string) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO dropbox_events (box_id, at, kind, text, who) VALUES (?,?,?,?,?)`,
		id, time.Now().UTC().Format(time.RFC3339Nano), kind, text, who); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM dropbox_events WHERE box_id = ? AND id <= (SELECT MAX(id) FROM dropbox_events WHERE box_id = ?) - ?`, id, id, maxDropEvents)
	return err
}

// DropEvents returns a box's log, newest first.
func (s *Store) DropEvents(ctx context.Context, id string, limit int) ([]DropEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT at, kind, text, who FROM dropbox_events WHERE box_id = ? ORDER BY id DESC LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DropEvent
	for rows.Next() {
		var e DropEvent
		var at string
		if err := rows.Scan(&at, &e.Kind, &e.Text, &e.Who); err != nil {
			return nil, err
		}
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// DropEventCount counts a box's log entries of a kind since a time, for the limits that pause a box by themselves.
func (s *Store) DropEventCount(ctx context.Context, id, kind string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dropbox_events WHERE box_id = ? AND kind = ? AND at >= ?`,
		id, kind, since.UTC().Format(time.RFC3339Nano)).Scan(&n)
	return n, err
}

// PruneDropBoxes forgets revoked boxes, and their logs, that ended before the cutoff, and old entries of the others.
func (s *Store) PruneDropBoxes(ctx context.Context, cutoff time.Time) error {
	c := cutoff.UTC().Format(time.RFC3339Nano)
	for _, q := range []string{
		`DELETE FROM dropbox_events WHERE box_id IN (SELECT id FROM dropboxes WHERE state = 'revoked' AND ended_at <> '' AND ended_at < ?)`,
		`DELETE FROM dropboxes WHERE state = 'revoked' AND ended_at <> '' AND ended_at < ?`,
		`DELETE FROM dropbox_events WHERE at < ?`,
	} {
		if _, err := s.db.ExecContext(ctx, q, c); err != nil {
			return err
		}
	}
	return nil
}
