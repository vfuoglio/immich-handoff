package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	DropCreated      = "created"
	DropUploaded     = "uploaded"
	DropRejected     = "rejected" // a file that was refused: its type, its size, a limit
	DropBlocked      = "blocked"  // a guest was turned away: too many requests, a wrong password
	DropPausedOn     = "paused"
	DropResumed      = "resumed"
	DropRevokedK     = "revoked"
	DropExpired      = "expired"
	DropMessageEvent = "message" // a guest left a message in the guest book
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
	PasswordScope            string    // what it guards: DropScopeUpload (sending) or DropScopePage (everything a guest can reach)
	State, Note              string
	Files                    int
	Bytes                    int64
	Album, ExtraTag          string    // where a file is filed besides the inbox tag: an album (made if missing) and a tag of its own
	Approve                  bool      // files wait for a host's approval before they reach the library
	StartsAt                 time.Time // zero: open from the start
	Notified                 int       // bits: 1 the reminder before the end was sent, 2 the closing summary was
	Short                    string    // a short code that opens the box too (empty: none)
	Accent, Lang             string    // the guest page's colour (#rrggbb) and language (empty: the guest's own)
	MaxDim                   int       // photos larger than this many pixels are shrunk in the guest's browser (0: not)
	GuestDelete              bool      // guests may delete what they sent while it is still in the inbox
	Removed                  int       // files guests deleted
	Prompts                  []Prompt  // the shot list: what guests are asked to photograph
	PromptRequired           bool      // every photo must say which prompt it is for
	GuestBook                bool      // guests may leave a message and a voice note
	Wall                     string    // the token of the slideshow link (empty: no wall)
	OnwardService            string    // the service the photos are handed on to when the box closes (empty: none)
	OnwardAuto               bool      // hand them on by itself when the box closes
	MailCode                 string    // the part after the plus sign of the box's mail address (empty: none)
	KeepDays                 int       // how many days after the box ends what guests left with it (names, hearts, the gallery) is kept; 0: until the box is revoked
	CreatedAt, EndedAt       time.Time
}

// Expired reports whether the box has passed its end.
func (b DropBox) Expired(now time.Time) bool {
	return !b.ExpiresAt.IsZero() && !now.Before(b.ExpiresAt)
}

// KeepUntil is when what guests left with the box (names, hearts, the gallery) goes: KeepDays after the box ends. It is
// zero for a box that does not end, or that keeps it until it is revoked.
func (b DropBox) KeepUntil() time.Time {
	if b.KeepDays <= 0 || b.ExpiresAt.IsZero() {
		return time.Time{}
	}
	return b.ExpiresAt.AddDate(0, 0, b.KeepDays)
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

const dropCols = `id, token, name, message, owner_id, owner_name, key_id, key_enc, inbox_tag, expires_at, max_files, max_mb, max_file_mb, photos_only, password, state, note, files, bytes, album, extra_tag, approve, starts_at, notified, short, accent, lang, max_dim, guest_delete, removed, prompts, prompt_required, guest_book, wall, onward_service, onward_auto, created_at, ended_at, mail_code, password_scope, keep_days`

// What a box's password guards.
const (
	DropScopeUpload = "upload" // the page can be seen; sending needs the password
	DropScopePage   = "page"   // nothing is shown, and nothing can be sent, without it
)

func scopeOrDefault(s string) string {
	if s == DropScopePage {
		return s
	}
	return DropScopeUpload
}

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
		`INSERT INTO dropboxes (`+dropCols+`) VALUES (`+strings.TrimSuffix(strings.Repeat("?,", strings.Count(dropCols, ",")+1), ",")+`)`,
		b.ID, b.Token, b.Name, b.Message, b.OwnerID.String(), b.OwnerName, b.KeyID.String(), enc, b.InboxTag, exp,
		b.MaxFiles, b.MaxMB, b.MaxFileMB, b2i(b.PhotosOnly), b.Password, b.State, b.Note, 0, 0,
		b.Album, b.ExtraTag, b2i(b.Approve), timeText(b.StartsAt), 0, b.Short, b.Accent, b.Lang, b.MaxDim, b2i(b.GuestDelete), 0, promptsJSON(b.Prompts), b2i(b.PromptRequired), b2i(b.GuestBook), b.Wall, b.OnwardService, b2i(b.OnwardAuto), b.CreatedAt.Format(time.RFC3339Nano), "", b.MailCode, scopeOrDefault(b.PasswordScope), b.KeepDays)
	return b, err
}

func (s *Store) scanDropBox(sc interface{ Scan(...any) error }) (DropBox, error) {
	var b DropBox
	var owner, keyID, exp, starts, created, ended, prompts string
	var enc []byte
	if err := sc.Scan(&b.ID, &b.Token, &b.Name, &b.Message, &owner, &b.OwnerName, &keyID, &enc, &b.InboxTag, &exp,
		&b.MaxFiles, &b.MaxMB, &b.MaxFileMB, &b.PhotosOnly, &b.Password, &b.State, &b.Note, &b.Files, &b.Bytes, &b.Album, &b.ExtraTag, &b.Approve, &starts, &b.Notified, &b.Short, &b.Accent, &b.Lang, &b.MaxDim, &b.GuestDelete, &b.Removed, &prompts, &b.PromptRequired, &b.GuestBook, &b.Wall, &b.OnwardService, &b.OnwardAuto, &created, &ended, &b.MailCode, &b.PasswordScope, &b.KeepDays); err != nil {
		return b, err
	}
	b.OwnerID, _ = uuid.Parse(owner)
	b.KeyID, _ = uuid.Parse(keyID)
	b.ExpiresAt, _ = time.Parse(time.RFC3339Nano, exp)
	b.StartsAt, _ = time.Parse(time.RFC3339Nano, starts)
	_ = json.Unmarshal([]byte(prompts), &b.Prompts)
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

// DropBoxByToken finds the box a link opens, by its long token or by its short code. A revoked box is not found.
func (s *Store) DropBoxByToken(ctx context.Context, token string) (DropBox, error) {
	b, err := s.scanDropBox(s.db.QueryRowContext(ctx, `SELECT `+dropCols+` FROM dropboxes WHERE (token = ? OR (short <> '' AND short = ?)) AND state <> ?`, token, token, DropRevoked))
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
		`DELETE FROM dropbox_uploads WHERE box_id IN (SELECT id FROM dropboxes WHERE state = 'revoked' AND ended_at <> '' AND ended_at < ?)`,
		`DELETE FROM dropbox_messages WHERE box_id IN (SELECT id FROM dropboxes WHERE state = 'revoked' AND ended_at <> '' AND ended_at < ?)`,
		`DELETE FROM dropbox_prompt_counts WHERE box_id IN (SELECT id FROM dropboxes WHERE state = 'revoked' AND ended_at <> '' AND ended_at < ?)`,
		`DELETE FROM dropbox_shares WHERE box_id IN (SELECT id FROM dropboxes WHERE state = 'revoked' AND ended_at <> '' AND ended_at < ?)`,
		`DELETE FROM dropbox_assets WHERE box_id IN (SELECT id FROM dropboxes WHERE state = 'revoked' AND ended_at <> '' AND ended_at < ?)`,
		`DELETE FROM dropbox_highlights WHERE box_id IN (SELECT id FROM dropboxes WHERE state = 'revoked' AND ended_at <> '' AND ended_at < ?)`,
		`DELETE FROM dropbox_verdicts WHERE box_id IN (SELECT id FROM dropboxes WHERE state = 'revoked' AND ended_at <> '' AND ended_at < ?)`,
		`DELETE FROM dropbox_onward WHERE box_id IN (SELECT id FROM dropboxes WHERE state = 'revoked' AND ended_at <> '' AND ended_at < ?)`,
		`DELETE FROM dropbox_covers WHERE box_id IN (SELECT id FROM dropboxes WHERE state = 'revoked' AND ended_at <> '' AND ended_at < ?)`,
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
	Prompt                           string // the id of the prompt it is for
	Size                             int64
	At                               time.Time
}

// AddDropPending records a staged file.
func (s *Store) AddDropPending(ctx context.Context, p DropPending) (DropPending, error) {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	p.At = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO dropbox_pending (id, box_id, name, size, kind, who, at, path, prompt) VALUES (?,?,?,?,?,?,?,?,?)`,
		p.ID, p.BoxID, p.Name, p.Size, p.Kind, p.Who, p.At.Format(time.RFC3339Nano), p.Path, p.Prompt)
	return p, err
}

func scanPending(rows *sql.Rows) ([]DropPending, error) {
	defer rows.Close()
	var out []DropPending
	for rows.Next() {
		var p DropPending
		var at string
		if err := rows.Scan(&p.ID, &p.BoxID, &p.Name, &p.Size, &p.Kind, &p.Who, &at, &p.Path, &p.Prompt); err != nil {
			return nil, err
		}
		p.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, p)
	}
	return out, rows.Err()
}

const pendingCols = `id, box_id, name, size, kind, who, at, path, prompt`

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

// SetDropShort gives a box a short code, or takes it away (empty). A code already in use is ErrExists.
func (s *Store) SetDropShort(ctx context.Context, id, code string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dropboxes SET short = ? WHERE id = ? AND state <> ?`, code, id, DropRevoked)
	if isUnique(err) {
		return ErrExists
	}
	return err
}

// SetDropToken replaces a box's long token: the old link stops working.
func (s *Store) SetDropToken(ctx context.Context, id, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dropboxes SET token = ? WHERE id = ? AND state <> ?`, token, id, DropRevoked)
	return err
}

// SetDropLook changes how the guest page looks and what the guest's browser does: colour, language, shrinking.
func (s *Store) SetDropLook(ctx context.Context, id, accent, lang string, maxDim int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dropboxes SET accent = ?, lang = ?, max_dim = ? WHERE id = ? AND state <> ?`, accent, lang, maxDim, id, DropRevoked)
	return err
}

// SetDropKeep changes how many days after the box ends what guests left with it is kept.
func (s *Store) SetDropKeep(ctx context.Context, id string, days int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dropboxes SET keep_days = ? WHERE id = ? AND state <> ?`, days, id, DropRevoked)
	return err
}

// AddDropRemoved counts a file a guest deleted.
func (s *Store) AddDropRemoved(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dropboxes SET removed = removed + 1 WHERE id = ?`, id)
	return err
}

// --- the cover photo ---

// SetDropCover keeps a box's cover photo (empty data: none).
func (s *Store) SetDropCover(ctx context.Context, id, kind string, data []byte) error {
	if len(data) == 0 {
		_, err := s.db.ExecContext(ctx, `DELETE FROM dropbox_covers WHERE box_id = ?`, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO dropbox_covers (box_id, kind, data) VALUES (?,?,?)
		ON CONFLICT(box_id) DO UPDATE SET kind = excluded.kind, data = excluded.data`, id, kind, data)
	return err
}

// DropCover reads a box's cover photo; ErrNotFound when it has none.
func (s *Store) DropCover(ctx context.Context, id string) (kind string, data []byte, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT kind, data FROM dropbox_covers WHERE box_id = ?`, id).Scan(&kind, &data)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

// HasDropCover says whether a box has a cover photo.
func (s *Store) HasDropCover(ctx context.Context, id string) bool {
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dropbox_covers WHERE box_id = ?`, id).Scan(&n)
	return n > 0
}

// --- templates ---

// DropTemplate is a person's saved settings for a drop box, as JSON the hub reads and writes.
type DropTemplate struct {
	ID, OwnerName string
	OwnerID       uuid.UUID
	Name, Data    string
	CreatedAt     time.Time
}

// AddDropTemplate saves a template.
func (s *Store) AddDropTemplate(ctx context.Context, t DropTemplate) (DropTemplate, error) {
	t.ID = uuid.NewString()
	t.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO dropbox_templates (id, owner_id, name, data, created_at) VALUES (?,?,?,?,?)`,
		t.ID, t.OwnerID.String(), t.Name, t.Data, t.CreatedAt.Format(time.RFC3339Nano))
	return t, err
}

// DropTemplates lists a person's templates, newest first.
func (s *Store) DropTemplates(ctx context.Context, owner uuid.UUID) ([]DropTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, data, created_at FROM dropbox_templates WHERE owner_id = ? ORDER BY created_at DESC`, owner.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DropTemplate
	for rows.Next() {
		t := DropTemplate{OwnerID: owner}
		var at string
		if err := rows.Scan(&t.ID, &t.Name, &t.Data, &at); err != nil {
			return nil, err
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, t)
	}
	return out, rows.Err()
}

// RemoveDropTemplate deletes one of a person's templates.
func (s *Store) RemoveDropTemplate(ctx context.Context, owner uuid.UUID, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM dropbox_templates WHERE id = ? AND owner_id = ?`, id, owner.String())
	return err
}

// --- what each guest sent ---

// DropUpload is a file a guest sent, kept so the same guest can see and delete it. Guest is a hash of the secret the
// guest's browser holds; the secret itself is never stored.
type DropUpload struct {
	ID, BoxID, Guest, AssetID, PendingID, Name string
	Prompt                                     string
	Size                                       int64
	At                                         time.Time
}

// AddDropUpload records a file a guest sent.
func (s *Store) AddDropUpload(ctx context.Context, u DropUpload) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO dropbox_uploads (id, box_id, guest, asset_id, pending_id, name, size, at, prompt) VALUES (?,?,?,?,?,?,?,?,?)`,
		uuid.NewString(), u.BoxID, u.Guest, u.AssetID, u.PendingID, u.Name, u.Size, time.Now().UTC().Format(time.RFC3339Nano), u.Prompt)
	return err
}

// DropUploadsOf lists what a guest sent to a box, newest first.
func (s *Store) DropUploadsOf(ctx context.Context, boxID, guest string, limit int) ([]DropUpload, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, box_id, guest, asset_id, pending_id, name, size, at, prompt FROM dropbox_uploads WHERE box_id = ? AND guest = ? ORDER BY at DESC LIMIT ?`, boxID, guest, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DropUpload
	for rows.Next() {
		var u DropUpload
		var at string
		if err := rows.Scan(&u.ID, &u.BoxID, &u.Guest, &u.AssetID, &u.PendingID, &u.Name, &u.Size, &at, &u.Prompt); err != nil {
			return nil, err
		}
		u.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, u)
	}
	return out, rows.Err()
}

// DropUploadByID finds one of a guest's files; it must be theirs, in that box.
func (s *Store) DropUploadByID(ctx context.Context, boxID, guest, id string) (DropUpload, error) {
	var u DropUpload
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT id, box_id, guest, asset_id, pending_id, name, size, at, prompt FROM dropbox_uploads WHERE id = ? AND box_id = ? AND guest = ?`, id, boxID, guest).
		Scan(&u.ID, &u.BoxID, &u.Guest, &u.AssetID, &u.PendingID, &u.Name, &u.Size, &at, &u.Prompt)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	u.At, _ = time.Parse(time.RFC3339Nano, at)
	return u, err
}

// RemoveDropUpload forgets a record of a guest's file; it reports whether it was there.
func (s *Store) RemoveDropUpload(ctx context.Context, id string) (bool, error) {
	r, err := s.db.ExecContext(ctx, `DELETE FROM dropbox_uploads WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

// DropUploadApproved says a waiting file was approved and is now this asset in the library.
func (s *Store) DropUploadApproved(ctx context.Context, pendingID, assetID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dropbox_uploads SET asset_id = ?, pending_id = '' WHERE pending_id = ?`, assetID, pendingID)
	return err
}

// DropUploadGone forgets the record of a waiting file that was rejected or deleted.
func (s *Store) DropUploadGone(ctx context.Context, pendingID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM dropbox_uploads WHERE pending_id = ?`, pendingID)
	return err
}

// --- the shot list ---

// Prompt is one thing guests are asked to photograph. Its id stays the same when the list is edited, so photos stay
// counted under it.
type Prompt struct {
	ID, Text string
}

func promptsJSON(p []Prompt) string {
	if len(p) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// SetDropPrompts replaces a box's shot list.
func (s *Store) SetDropPrompts(ctx context.Context, id string, prompts []Prompt, required bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dropboxes SET prompts = ?, prompt_required = ? WHERE id = ? AND state <> ?`, promptsJSON(prompts), b2i(required && len(prompts) > 0), id, DropRevoked)
	return err
}

// SetDropGuestBook switches a box's guest book on or off.
func (s *Store) SetDropGuestBook(ctx context.Context, id string, on bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dropboxes SET guest_book = ? WHERE id = ? AND state <> ?`, b2i(on), id, DropRevoked)
	return err
}

// AddPromptCount counts a file kept under a prompt (a negative n takes one back).
func (s *Store) AddPromptCount(ctx context.Context, boxID, prompt string, n int) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO dropbox_prompt_counts (box_id, prompt, files) VALUES (?,?,MAX(?,0))
		ON CONFLICT(box_id, prompt) DO UPDATE SET files = MAX(files + ?, 0)`, boxID, prompt, n, n)
	return err
}

// PromptCounts says how many files are kept under each prompt of a box.
func (s *Store) PromptCounts(ctx context.Context, boxID string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT prompt, files FROM dropbox_prompt_counts WHERE box_id = ?`, boxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var p string
		var n int
		if err := rows.Scan(&p, &n); err != nil {
			return nil, err
		}
		out[p] = n
	}
	return out, rows.Err()
}

// --- the guest book ---

// DropMessage is something a guest left for the owner: a few words, a voice note, or both.
type DropMessage struct {
	ID, BoxID, Who, Name, Text string
	AudioKind                  string // "" when there is no voice note
	HasAudio                   bool
	OnWall                     bool // the owner chose to show it on the wall
	At                         time.Time
}

// AddDropMessage keeps a message; audio may be empty.
func (s *Store) AddDropMessage(ctx context.Context, m DropMessage, audio []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO dropbox_messages (id, box_id, who, name, text, audio, audio_kind, at) VALUES (?,?,?,?,?,?,?,?)`,
		uuid.NewString(), m.BoxID, m.Who, m.Name, m.Text, audio, m.AudioKind, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// DropMessages lists a box's messages, newest first, without their audio.
func (s *Store) DropMessages(ctx context.Context, boxID string, limit int) ([]DropMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, box_id, who, name, text, audio_kind, at, COALESCE(length(audio), 0) > 0, on_wall FROM dropbox_messages WHERE box_id = ? ORDER BY at DESC LIMIT ?`, boxID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DropMessage
	for rows.Next() {
		var m DropMessage
		var at string
		if err := rows.Scan(&m.ID, &m.BoxID, &m.Who, &m.Name, &m.Text, &m.AudioKind, &at, &m.HasAudio, &m.OnWall); err != nil {
			return nil, err
		}
		m.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, m)
	}
	return out, rows.Err()
}

// DropMessageCount says how many messages a box holds.
func (s *Store) DropMessageCount(ctx context.Context, boxID string) int {
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dropbox_messages WHERE box_id = ?`, boxID).Scan(&n)
	return n
}

// DropMessageAudio reads one message's voice note and says which box it is in.
func (s *Store) DropMessageAudio(ctx context.Context, id string) (boxID, kind string, data []byte, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT box_id, audio_kind, audio FROM dropbox_messages WHERE id = ?`, id).Scan(&boxID, &kind, &data)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && len(data) == 0) {
		err = ErrNotFound
	}
	return
}

// DropMessageBox says which box a message is in.
func (s *Store) DropMessageBox(ctx context.Context, id string) (string, error) {
	var box string
	err := s.db.QueryRowContext(ctx, `SELECT box_id FROM dropbox_messages WHERE id = ?`, id).Scan(&box)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return box, err
}

// RemoveDropMessage deletes a message and its voice note.
func (s *Store) RemoveDropMessage(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM dropbox_messages WHERE id = ?`, id)
	return err
}

// DropMessageAudios lists a box's voice notes with their messages, oldest first, for an export.
func (s *Store) DropMessageAudios(ctx context.Context, boxID string) (map[string][]byte, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, audio FROM dropbox_messages WHERE box_id = ? AND COALESCE(length(audio), 0) > 0`, boxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		out[id] = data
	}
	return out, rows.Err()
}

// --- sharing the result back ---

// DropShare is the shared link a box's owner made so guests can see and download the photos.
type DropShare struct {
	BoxID, LinkID, Key, Message string
	ExpiresAt, CreatedAt        time.Time
}

// SetDropShare keeps a box's shared link (one per box).
func (s *Store) SetDropShare(ctx context.Context, sh DropShare) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO dropbox_shares (box_id, link_id, key, message, expires_at, created_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT(box_id) DO UPDATE SET link_id = excluded.link_id, key = excluded.key, message = excluded.message, expires_at = excluded.expires_at, created_at = excluded.created_at`,
		sh.BoxID, sh.LinkID, sh.Key, sh.Message, timeText(sh.ExpiresAt), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// DropShareOf reads a box's shared link; ErrNotFound when it has none.
func (s *Store) DropShareOf(ctx context.Context, boxID string) (DropShare, error) {
	var sh DropShare
	var exp, created string
	err := s.db.QueryRowContext(ctx, `SELECT box_id, link_id, key, message, expires_at, created_at FROM dropbox_shares WHERE box_id = ?`, boxID).
		Scan(&sh.BoxID, &sh.LinkID, &sh.Key, &sh.Message, &exp, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return sh, ErrNotFound
	}
	sh.ExpiresAt, _ = time.Parse(time.RFC3339Nano, exp)
	sh.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return sh, err
}

// RemoveDropShare forgets a box's shared link.
func (s *Store) RemoveDropShare(ctx context.Context, boxID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM dropbox_shares WHERE box_id = ?`, boxID)
	return err
}

// --- the photos a box kept ---

// DropAsset is a photo a box put in its owner's library. ID grows with each one, so a wall can ask for what is newer.
type DropAsset struct {
	ID             int64
	BoxID, AssetID string
	Prompt         string
	At             time.Time
}

// AddDropAsset records a photo a box kept (once).
func (s *Store) AddDropAsset(ctx context.Context, boxID, assetID, prompt string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO dropbox_assets (box_id, asset_id, prompt, at) VALUES (?,?,?,?)`,
		boxID, assetID, prompt, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// RemoveDropAsset forgets a photo (a guest took it back).
func (s *Store) RemoveDropAsset(ctx context.Context, boxID, assetID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM dropbox_assets WHERE box_id = ? AND asset_id = ?`, boxID, assetID)
	return err
}

// DropAssetsAfter lists a box's photos newer than a position, oldest first.
func (s *Store) DropAssetsAfter(ctx context.Context, boxID string, after int64, limit int) ([]DropAsset, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, box_id, asset_id, prompt, at FROM dropbox_assets WHERE box_id = ? AND id > ? ORDER BY id LIMIT ?`, boxID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DropAsset
	for rows.Next() {
		var a DropAsset
		var at string
		if err := rows.Scan(&a.ID, &a.BoxID, &a.AssetID, &a.Prompt, &at); err != nil {
			return nil, err
		}
		a.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, a)
	}
	return out, rows.Err()
}

// DropAssetsLatest lists the newest photos of a box, newest first.
func (s *Store) DropAssetsLatest(ctx context.Context, boxID string, limit int) ([]DropAsset, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, box_id, asset_id, prompt, at FROM dropbox_assets WHERE box_id = ? ORDER BY id DESC LIMIT ?`, boxID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DropAsset
	for rows.Next() {
		var a DropAsset
		var at string
		if err := rows.Scan(&a.ID, &a.BoxID, &a.AssetID, &a.Prompt, &at); err != nil {
			return nil, err
		}
		a.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, a)
	}
	return out, rows.Err()
}

// DropAssetByID finds one photo of a box.
func (s *Store) DropAssetByID(ctx context.Context, boxID string, id int64) (DropAsset, error) {
	var a DropAsset
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT id, box_id, asset_id, prompt, at FROM dropbox_assets WHERE box_id = ? AND id = ?`, boxID, id).
		Scan(&a.ID, &a.BoxID, &a.AssetID, &a.Prompt, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	a.At, _ = time.Parse(time.RFC3339Nano, at)
	return a, err
}

// DropAssetIDs lists every photo a box kept (their ids in the library), oldest first.
func (s *Store) DropAssetIDs(ctx context.Context, boxID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT asset_id FROM dropbox_assets WHERE box_id = ? ORDER BY id`, boxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// --- the wall ---

// SetDropWall gives a box a wall link, or takes it away (empty).
func (s *Store) SetDropWall(ctx context.Context, id, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dropboxes SET wall = ? WHERE id = ? AND state <> ?`, token, id, DropRevoked)
	return err
}

// DropBoxByWall finds the box a wall link shows. A revoked box has no wall.
func (s *Store) DropBoxByWall(ctx context.Context, token string) (DropBox, error) {
	if token == "" {
		return DropBox{}, ErrDropBoxGone
	}
	b, err := s.scanDropBox(s.db.QueryRowContext(ctx, `SELECT `+dropCols+` FROM dropboxes WHERE wall = ? AND state <> ?`, token, DropRevoked))
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrDropBoxGone
	}
	return b, err
}

// SetMessageOnWall chooses whether a guest-book message is shown on the wall.
func (s *Store) SetMessageOnWall(ctx context.Context, id string, on bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dropbox_messages SET on_wall = ? WHERE id = ?`, b2i(on), id)
	return err
}

// WallMessages lists the messages the owner chose to show on the wall, newest first.
func (s *Store) WallMessages(ctx context.Context, boxID string, limit int) ([]DropMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, text FROM dropbox_messages WHERE box_id = ? AND on_wall = 1 AND text <> '' ORDER BY at DESC LIMIT ?`, boxID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DropMessage
	for rows.Next() {
		m := DropMessage{BoxID: boxID, OnWall: true}
		if err := rows.Scan(&m.ID, &m.Name, &m.Text); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// --- the best-of ---

const (
	HighlightRunning = "running"
	HighlightDone    = "done"
	HighlightFailed  = "failed"
)

// DropHighlight is the state of a box's best-of: how far it is, and what it found.
type DropHighlight struct {
	BoxID, Strictness, Status, Album, Note string
	Total, Done, Kept                      int
	Started, Finished                      time.Time
}

// SetDropHighlight keeps (or updates) the state of a box's best-of.
func (s *Store) SetDropHighlight(ctx context.Context, h DropHighlight) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO dropbox_highlights (box_id, strictness, status, total, done, kept, album, note, started_at, finished_at) VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(box_id) DO UPDATE SET strictness = excluded.strictness, status = excluded.status, total = excluded.total, done = excluded.done, kept = excluded.kept,
		album = excluded.album, note = excluded.note, started_at = excluded.started_at, finished_at = excluded.finished_at`,
		h.BoxID, h.Strictness, h.Status, h.Total, h.Done, h.Kept, h.Album, h.Note, timeText(h.Started), timeText(h.Finished))
	return err
}

// DropHighlightOf reads a box's best-of; ErrNotFound when none was made.
func (s *Store) DropHighlightOf(ctx context.Context, boxID string) (DropHighlight, error) {
	var h DropHighlight
	var st, fin string
	err := s.db.QueryRowContext(ctx, `SELECT box_id, strictness, status, total, done, kept, album, note, started_at, finished_at FROM dropbox_highlights WHERE box_id = ?`, boxID).
		Scan(&h.BoxID, &h.Strictness, &h.Status, &h.Total, &h.Done, &h.Kept, &h.Album, &h.Note, &st, &fin)
	if errors.Is(err, sql.ErrNoRows) {
		return h, ErrNotFound
	}
	h.Started, _ = time.Parse(time.RFC3339Nano, st)
	h.Finished, _ = time.Parse(time.RFC3339Nano, fin)
	return h, err
}

// Verdict is what the best-of decided about one photo, and why.
type Verdict struct {
	AssetID, Verdict, Detail, Name string
}

// ReplaceDropVerdicts replaces every verdict of a box at once.
func (s *Store) ReplaceDropVerdicts(ctx context.Context, boxID string, vs []Verdict) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM dropbox_verdicts WHERE box_id = ?`, boxID); err != nil {
		return err
	}
	for _, v := range vs {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO dropbox_verdicts (box_id, asset_id, verdict, detail, name) VALUES (?,?,?,?,?)`, boxID, v.AssetID, v.Verdict, v.Detail, v.Name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DropVerdicts lists a box's verdicts: with excluded, only the photos that were left out.
func (s *Store) DropVerdicts(ctx context.Context, boxID string, excluded bool, limit int) ([]Verdict, error) {
	q := `SELECT asset_id, verdict, detail, name FROM dropbox_verdicts WHERE box_id = ?`
	if excluded {
		q += ` AND verdict <> 'kept'`
	} else {
		q += ` AND verdict = 'kept'`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY verdict, name LIMIT ?`, boxID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Verdict
	for rows.Next() {
		var v Verdict
		if err := rows.Scan(&v.AssetID, &v.Verdict, &v.Detail, &v.Name); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// --- handing the set on ---

// SetDropOnward chooses the service a box's photos are handed on to when it closes, and whether that happens by itself.
func (s *Store) SetDropOnward(ctx context.Context, id, service string, auto bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dropboxes SET onward_service = ?, onward_auto = ? WHERE id = ? AND state <> ?`, service, b2i(auto && service != ""), id, DropRevoked)
	return err
}

// DropOnward is one time a box's photos were handed on.
type DropOnward struct {
	At             time.Time
	Service, Scope string // scope: "all" or "best"
	Count          int
	By             string
}

// AddDropOnward records a hand-on.
func (s *Store) AddDropOnward(ctx context.Context, boxID string, o DropOnward) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO dropbox_onward (box_id, at, service, scope, count, by_name) VALUES (?,?,?,?,?,?)`,
		boxID, time.Now().UTC().Format(time.RFC3339Nano), o.Service, o.Scope, o.Count, o.By)
	return err
}

// DropOnwardLog lists what was handed on, newest first.
func (s *Store) DropOnwardLog(ctx context.Context, boxID string, limit int) ([]DropOnward, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT at, service, scope, count, by_name FROM dropbox_onward WHERE box_id = ? ORDER BY id DESC LIMIT ?`, boxID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DropOnward
	for rows.Next() {
		var o DropOnward
		var at string
		if err := rows.Scan(&at, &o.Service, &o.Scope, &o.Count, &o.By); err != nil {
			return nil, err
		}
		o.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, o)
	}
	return out, rows.Err()
}

// SetDropMailCode gives a box the code of its mail address, or takes it away (empty). A code already in use is ErrExists.
func (s *Store) SetDropMailCode(ctx context.Context, id, code string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dropboxes SET mail_code = ? WHERE id = ? AND state <> ?`, code, id, DropRevoked)
	if isUnique(err) {
		return ErrExists
	}
	return err
}

// DropBoxByMailCode finds the box a mail address belongs to. A revoked box is not found.
func (s *Store) DropBoxByMailCode(ctx context.Context, code string) (DropBox, error) {
	if code == "" {
		return DropBox{}, ErrDropBoxGone
	}
	b, err := s.scanDropBox(s.db.QueryRowContext(ctx, `SELECT `+dropCols+` FROM dropboxes WHERE mail_code = ? AND state <> ?`, code, DropRevoked))
	if errors.Is(err, sql.ErrNoRows) {
		return DropBox{}, ErrDropBoxGone
	}
	return b, err
}
