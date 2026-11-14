package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Room states.
const (
	RoomActive = "active"
	RoomPaused = "paused" // the link shows nothing until the photographer resumes it
	RoomEnded  = "ended"  // the end date came; the photographer removes it, which deletes its key
)

// Room is a client room: a link for a client to look at an album of the photographer's, through marked pictures.
type Room struct {
	ID, Token string
	OwnerID   uuid.UUID
	OwnerName string
	Name      string // what the photographer calls it
	Title     string // what the client sees (empty: the name)
	AlbumID   uuid.UUID
	AlbumName string
	KeyID     uuid.UUID // the room's key in Immich
	Key       string    // decrypted
	Password  string    // salted hash of the PIN; empty: none
	EndsAt    time.Time // zero: until the photographer ends it
	State     string
	CreatedAt time.Time
}

// ClientTitle is the name the client sees.
func (r Room) ClientTitle() string {
	if r.Title != "" {
		return r.Title
	}
	return r.Name
}

const roomCols = `id, token, owner_id, owner_name, name, title, album_id, album_name, key_id, key_enc, password, ends_at, state, created_at`

func (s *Store) scanRoom(sc interface{ Scan(...any) error }) (Room, error) {
	var r Room
	var owner, album, key, ends, created string
	var enc []byte
	if err := sc.Scan(&r.ID, &r.Token, &owner, &r.OwnerName, &r.Name, &r.Title, &album, &r.AlbumName, &key, &enc, &r.Password, &ends, &r.State, &created); err != nil {
		return r, err
	}
	r.OwnerID, _ = uuid.Parse(owner)
	r.AlbumID, _ = uuid.Parse(album)
	r.KeyID, _ = uuid.Parse(key)
	r.EndsAt, _ = time.Parse(time.RFC3339Nano, ends)
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	plain, err := s.vault.Open(enc, r.ID)
	if err != nil {
		return r, err
	}
	r.Key = string(plain)
	return r, nil
}

// AddRoom stores a room (its key sealed) and returns it with its id.
func (s *Store) AddRoom(ctx context.Context, r Room) (Room, error) {
	r.ID = uuid.NewString()
	if r.State == "" {
		r.State = RoomActive
	}
	r.CreatedAt = time.Now().UTC()
	enc, err := s.vault.Seal([]byte(r.Key), r.ID)
	if err != nil {
		return r, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO rooms (`+roomCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Token, r.OwnerID.String(), r.OwnerName, r.Name, r.Title, r.AlbumID.String(), r.AlbumName, r.KeyID.String(), enc, r.Password, tstr(r.EndsAt), r.State, r.CreatedAt.Format(time.RFC3339Nano))
	return r, err
}

// RoomsOf are a person's rooms, oldest first.
func (s *Store) RoomsOf(ctx context.Context, owner uuid.UUID) ([]Room, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+roomCols+` FROM rooms WHERE owner_id = ? ORDER BY created_at, id`, owner.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Room
	for rows.Next() {
		r, err := s.scanRoom(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RoomByToken finds the room a link opens; one that is ended is not found, a paused one is (the page says so).
func (s *Store) RoomByToken(ctx context.Context, token string) (Room, error) {
	r, err := s.scanRoom(s.db.QueryRowContext(ctx, `SELECT `+roomCols+` FROM rooms WHERE token = ? AND state <> ?`, token, RoomEnded))
	if errors.Is(err, sql.ErrNoRows) {
		return Room{}, ErrNotFound
	}
	return r, err
}

// RoomByID finds one of a person's rooms.
func (s *Store) RoomByID(ctx context.Context, owner uuid.UUID, id string) (Room, error) {
	r, err := s.scanRoom(s.db.QueryRowContext(ctx, `SELECT `+roomCols+` FROM rooms WHERE id = ? AND owner_id = ?`, id, owner.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return Room{}, ErrNotFound
	}
	return r, err
}

// RoomByIDAny finds a room by its id alone, for the app's own work (never for a person's request).
func (s *Store) RoomByIDAny(ctx context.Context, id string) (Room, error) {
	r, err := s.scanRoom(s.db.QueryRowContext(ctx, `SELECT `+roomCols+` FROM rooms WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Room{}, ErrNotFound
	}
	return r, err
}

// SetRoomState pauses or resumes a room.
func (s *Store) SetRoomState(ctx context.Context, id, state string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE rooms SET state = ? WHERE id = ? AND state <> ?`, state, id, RoomEnded)
	return err
}

// DeleteRoom forgets a room.
func (s *Store) DeleteRoom(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM rooms WHERE id = ?`, id)
	return err
}

// EndRoomsDue marks the rooms whose end date has come as ended and returns how many.
func (s *Store) EndRoomsDue(ctx context.Context, now time.Time) (int, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE rooms SET state = ? WHERE state <> ? AND ends_at <> '' AND ends_at <= ?`, RoomEnded, RoomEnded, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	n, _ := r.RowsAffected()
	return int(n), nil
}

// CountRooms is how many rooms a person has.
func (s *Store) CountRooms(ctx context.Context, owner uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rooms WHERE owner_id = ?`, owner.String()).Scan(&n)
	return n, err
}
