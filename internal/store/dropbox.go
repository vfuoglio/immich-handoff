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
	Album, ExtraTag          string    // where a file is filed besides the inbox tag: an album (made if missing) and a tag of its own
	Approve                  bool      // files wait for a host's approval before they reach the library
	StartsAt                 time.Time // zero: open from the start
	Notified                 int       // bits: 1 the reminder before the end was sent, 2 the closing summary was
	CreatedAt, EndedAt       time.Time
}

// Expired reports whether the box has passed its end.
func (b DropBox) Expired(now time.Time) bool {
	return !b.ExpiresAt.IsZero() && !now.Before(b.ExpiresAt)
}

// Open reports whether guests may use the box now.
func (b DropBox) Open(now time.Time) bool {
	return b.State == DropActive && !b.Expired(now) && !b.NotStarted(now)
}

// NotStarted reports whether the box has not opened yet.
func (b DropBox) NotStarted(now time.Time) bool { return now.Before(b.StartsAt) }

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

const dropCols = `id, token, name, message, owner_id, owner_name, key_id, key_enc, inbox_tag, expires_at, max_files, max_mb, max_file_mb, photos_only, password, state, note, files, bytes, album, extra_tag, approve, starts_at, notified, created_at, ended_at`

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
	exp := timeText(b.ExpiresAt)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO dropboxes (`+dropCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		b.ID, b.Token, b.Name, b.Message, b.OwnerID.String(), b.OwnerName, b.KeyID.String(), enc, b.InboxTag, exp,
		b.MaxFiles, b.MaxMB, b.MaxFileMB, b2i(b.PhotosOnly), b.Password, b.State, b.Note, 0, 0,
		b.Album, b.ExtraTag, b2i(b.Approve), timeText(b.StartsAt), 0, b.CreatedAt.Format(time.RFC3339Nano), "")
	return b, err
}

func (s *Store) scanDropBox(sc interface{ Scan(...any) error }) (DropBox, error) {
	var b DropBox
	var owner, keyID, exp, starts, created, ended string
	var enc []byte
	if err := sc.Scan(&b.ID, &b.Token, &b.Name, &b.Message, &owner, &b.OwnerName, &keyID, &enc, &b.InboxTag, &exp,
		&b.MaxFiles, &b.MaxMB, &b.MaxFileMB, &b.PhotosOnly, &b.Password, &b.State, &b.Note, &b.Files, &b.Bytes, &b.Album, &b.ExtraTag, &b.Approve, &starts, &b.Notified, &created, &ended); err != nil {
		return b, err
	}
	b.OwnerID, _ = uuid.Parse(owner)
	b.KeyID, _ = uuid.Parse(keyID)
	b.ExpiresAt, _ = time.Parse(time.RFC3339Nano, exp)
	b.StartsAt, _ = time.Parse(time.RFC3339Nano, starts)
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
		`DELETE FROM dropbox_hosts WHERE box_id IN (SELECT id FROM dropboxes WHERE state = 'revoked' AND ended_at <> '' AND ended_at < ?)`,
		`DELETE FROM dropbox_hours WHERE box_id IN (SELECT id FROM dropboxes WHERE state = 'revoked' AND ended_at <> '' AND ended_at < ?)`,
		`DELETE FROM dropbox_guests WHERE box_id IN (SELECT id FROM dropboxes WHERE state = 'revoked' AND ended_at <> '' AND ended_at < ?)`,
		`DELETE FROM dropboxes WHERE state = 'revoked' AND ended_at <> '' AND ended_at < ?`,
		`DELETE FROM dropbox_events WHERE at < ?`,
	} {
		if _, err := s.db.ExecContext(ctx, q, c); err != nil {
			return err
		}
	}
	return nil
}

func timeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// SetDropSchedule changes when a box opens and closes, and lets it send its reminder and summary again.
func (s *Store) SetDropSchedule(ctx context.Context, id string, starts, ends time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dropboxes SET starts_at = ?, expires_at = ?, notified = 0 WHERE id = ? AND state <> ?`,
		timeText(starts), timeText(ends), id, DropRevoked)
	return err
}

// MarkDropNotified records which of the two messages a box has sent (see DropBox.Notified).
func (s *Store) MarkDropNotified(ctx context.Context, id string, bit int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dropboxes SET notified = notified | ? WHERE id = ?`, bit, id)
	return err
}

// --- co-hosts ---

const (
	HostManage = "manage" // approves, pauses, resumes, extends: everything but ending the box or choosing co-hosts
	HostView   = "view"   // reads the summary and the log
)

// DropHost is a person who helps look after a box that is not theirs.
type DropHost struct {
	UserID      uuid.UUID
	Name, Email string
	Role        string
	AddedAt     time.Time
}

// SetDropHost adds a co-host, or changes their role.
func (s *Store) SetDropHost(ctx context.Context, boxID string, h DropHost) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO dropbox_hosts (box_id, user_id, name, email, role, added_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT(box_id, user_id) DO UPDATE SET role = excluded.role, name = excluded.name, email = excluded.email`,
		boxID, h.UserID.String(), h.Name, h.Email, h.Role, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// RemoveDropHost takes a co-host off a box.
func (s *Store) RemoveDropHost(ctx context.Context, boxID string, user uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM dropbox_hosts WHERE box_id = ? AND user_id = ?`, boxID, user.String())
	return err
}

// DropHosts lists the co-hosts of a box.
func (s *Store) DropHosts(ctx context.Context, boxID string) ([]DropHost, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, name, email, role, added_at FROM dropbox_hosts WHERE box_id = ? ORDER BY added_at`, boxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DropHost
	for rows.Next() {
		var h DropHost
		var u, at string
		if err := rows.Scan(&u, &h.Name, &h.Email, &h.Role, &at); err != nil {
			return nil, err
		}
		h.UserID, _ = uuid.Parse(u)
		h.AddedAt, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, h)
	}
	return out, rows.Err()
}

// HostedBoxes says, for each box a person helps with, their role in it.
func (s *Store) HostedBoxes(ctx context.Context, user uuid.UUID) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT box_id, role FROM dropbox_hosts WHERE user_id = ?`, user.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, role string
		if err := rows.Scan(&id, &role); err != nil {
			return nil, err
		}
		out[id] = role
	}
	return out, rows.Err()
}

// --- files waiting for approval ---

// DropPending is a file a guest sent that waits for a host's approval. Its bytes are on disk, at Path.
type DropPending struct {
	ID, BoxID, Name, Kind, Who, Path string
	Size                             int64
	At                               time.Time
}

// AddDropPending records a staged file.
func (s *Store) AddDropPending(ctx context.Context, p DropPending) (DropPending, error) {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	p.At = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO dropbox_pending (id, box_id, name, size, kind, who, at, path) VALUES (?,?,?,?,?,?,?,?)`,
		p.ID, p.BoxID, p.Name, p.Size, p.Kind, p.Who, p.At.Format(time.RFC3339Nano), p.Path)
	return p, err
}

func scanPending(rows *sql.Rows) ([]DropPending, error) {
	defer rows.Close()
	var out []DropPending
	for rows.Next() {
		var p DropPending
		var at string
		if err := rows.Scan(&p.ID, &p.BoxID, &p.Name, &p.Size, &p.Kind, &p.Who, &at, &p.Path); err != nil {
			return nil, err
		}
		p.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, p)
	}
	return out, rows.Err()
}

const pendingCols = `id, box_id, name, size, kind, who, at, path`

// DropPendingOf lists a box's waiting files, oldest first, at most limit of them.
func (s *Store) DropPendingOf(ctx context.Context, boxID string, limit int) ([]DropPending, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+pendingCols+` FROM dropbox_pending WHERE box_id = ? ORDER BY at LIMIT ?`, boxID, limit)
	if err != nil {
		return nil, err
	}
	return scanPending(rows)
}

// DropPendingByID finds one waiting file.
func (s *Store) DropPendingByID(ctx context.Context, id string) (DropPending, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+pendingCols+` FROM dropbox_pending WHERE id = ?`, id)
	if err != nil {
		return DropPending{}, err
	}
	ps, err := scanPending(rows)
	if err != nil {
		return DropPending{}, err
	}
	if len(ps) == 0 {
		return DropPending{}, ErrNotFound
	}
	return ps[0], nil
}

// DropPendingTotals says how many files wait in a box and how many bytes they hold.
func (s *Store) DropPendingTotals(ctx context.Context, boxID string) (n int, bytes int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size), 0) FROM dropbox_pending WHERE box_id = ?`, boxID).Scan(&n, &bytes)
	return
}

// RemoveDropPending forgets a waiting file; it reports whether it was there, so two hosts cannot decide on one file twice.
func (s *Store) RemoveDropPending(ctx context.Context, id string) (bool, error) {
	r, err := s.db.ExecContext(ctx, `DELETE FROM dropbox_pending WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

// DropPendingBefore lists the waiting files, of every box, that came before the cutoff.
func (s *Store) DropPendingBefore(ctx context.Context, cutoff time.Time) ([]DropPending, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+pendingCols+` FROM dropbox_pending WHERE at < ?`, cutoff.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	return scanPending(rows)
}

// --- what a summary needs ---

// RecordDropArrival counts a kept file in its hour, and the guest who sent it.
func (s *Store) RecordDropArrival(ctx context.Context, boxID, who string, at time.Time) error {
	hour := at.UTC().Format("2006-01-02T15")
	if _, err := s.db.ExecContext(ctx, `INSERT INTO dropbox_hours (box_id, hour, files) VALUES (?,?,1)
		ON CONFLICT(box_id, hour) DO UPDATE SET files = files + 1`, boxID, hour); err != nil {
		return err
	}
	if who == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO dropbox_guests (box_id, who) VALUES (?,?)`, boxID, who)
	return err
}

// DropSummary is what a box took: how many guests sent something, and its busiest hour (UTC, as "2006-01-02T15").
type DropSummary struct {
	Guests       int
	BusiestHour  string
	BusiestFiles int
}

// DropSummaryOf reads a box's summary.
func (s *Store) DropSummaryOf(ctx context.Context, boxID string) (DropSummary, error) {
	var out DropSummary
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dropbox_guests WHERE box_id = ?`, boxID).Scan(&out.Guests); err != nil {
		return out, err
	}
	err := s.db.QueryRowContext(ctx, `SELECT hour, files FROM dropbox_hours WHERE box_id = ? ORDER BY files DESC, hour LIMIT 1`, boxID).Scan(&out.BusiestHour, &out.BusiestFiles)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return out, err
}
