package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Frame states.
const (
	FrameActive = "active"
	FrameEnded  = "ended" // its link stopped (the end date came); the owner removes it, which deletes its key
)

// Frame is a link that shows an album full screen on a device with no account.
type Frame struct {
	ID, Token string
	OwnerID   uuid.UUID
	OwnerName string
	Name      string
	AlbumID   uuid.UUID
	AlbumName string
	KeyID     uuid.UUID // the frame's key in Immich: it can look at pictures and read albums
	Key       string    // decrypted
	Hold      int       // seconds each photo is shown
	EndsAt    time.Time // zero: until the owner ends it
	State     string
	CreatedAt time.Time
}

const frameCols = `id, token, owner_id, owner_name, name, album_id, album_name, key_id, key_enc, hold, ends_at, state, created_at`

func (s *Store) scanFrame(sc interface{ Scan(...any) error }) (Frame, error) {
	var f Frame
	var owner, album, key, ends, created string
	var enc []byte
	if err := sc.Scan(&f.ID, &f.Token, &owner, &f.OwnerName, &f.Name, &album, &f.AlbumName, &key, &enc, &f.Hold, &ends, &f.State, &created); err != nil {
		return f, err
	}
	f.OwnerID, _ = uuid.Parse(owner)
	f.AlbumID, _ = uuid.Parse(album)
	f.KeyID, _ = uuid.Parse(key)
	f.EndsAt, _ = time.Parse(time.RFC3339Nano, ends)
	f.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	plain, err := s.vault.Open(enc, f.ID)
	if err != nil {
		return f, err
	}
	f.Key = string(plain)
	return f, nil
}

// AddFrame stores a frame (its key sealed) and returns it with its id.
func (s *Store) AddFrame(ctx context.Context, f Frame) (Frame, error) {
	f.ID = uuid.NewString()
	f.State = FrameActive
	f.CreatedAt = time.Now().UTC()
	enc, err := s.vault.Seal([]byte(f.Key), f.ID)
	if err != nil {
		return f, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO frames (`+frameCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.ID, f.Token, f.OwnerID.String(), f.OwnerName, f.Name, f.AlbumID.String(), f.AlbumName, f.KeyID.String(), enc, f.Hold, tstr(f.EndsAt), f.State, f.CreatedAt.Format(time.RFC3339Nano))
	return f, err
}

// FramesOf are a person's frames, oldest first.
func (s *Store) FramesOf(ctx context.Context, owner uuid.UUID) ([]Frame, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+frameCols+` FROM frames WHERE owner_id = ? ORDER BY created_at, id`, owner.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Frame
	for rows.Next() {
		f, err := s.scanFrame(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FrameByToken finds the frame a link shows; one that has ended is not found.
func (s *Store) FrameByToken(ctx context.Context, token string) (Frame, error) {
	f, err := s.scanFrame(s.db.QueryRowContext(ctx, `SELECT `+frameCols+` FROM frames WHERE token = ? AND state = ?`, token, FrameActive))
	if errors.Is(err, sql.ErrNoRows) {
		return Frame{}, ErrNotFound
	}
	return f, err
}

// FrameByID finds one of a person's frames.
func (s *Store) FrameByID(ctx context.Context, owner uuid.UUID, id string) (Frame, error) {
	f, err := s.scanFrame(s.db.QueryRowContext(ctx, `SELECT `+frameCols+` FROM frames WHERE id = ? AND owner_id = ?`, id, owner.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return Frame{}, ErrNotFound
	}
	return f, err
}

// DeleteFrame forgets a frame.
func (s *Store) DeleteFrame(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM frames WHERE id = ?`, id)
	return err
}

// EndFramesDue marks the active frames whose end date has come as ended and returns how many.
func (s *Store) EndFramesDue(ctx context.Context, now time.Time) (int, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE frames SET state = ? WHERE state = ? AND ends_at <> '' AND ends_at <= ?`, FrameEnded, FrameActive, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	n, _ := r.RowsAffected()
	return int(n), nil
}

// CountFrames is how many frames a person has.
func (s *Store) CountFrames(ctx context.Context, owner uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM frames WHERE owner_id = ?`, owner.String()).Scan(&n)
	return n, err
}
