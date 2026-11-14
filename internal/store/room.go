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

// Who chooses in a room.
const (
	RoomShared  = "shared"  // one selection for everyone who has the link
	RoomVisitor = "visitor" // one selection for each visitor
	RoomNamed   = "named"   // one client, the first to give a name; the others look
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
	Mark      string    // how the pictures are marked: "text", "logo" or "both"
	MarkText  string    // the text repeated over the pictures
	PickMode  string    // who chooses: "shared" (one selection for everyone), "visitor" (one each) or "named" (one client)
	PickLimit int       // the most photos a selection may hold; 0: no limit
	ClaimWho  string    // for "named": the visitor who claimed the room (empty: nobody yet)
	ClaimName string    // and the name they gave
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

const roomCols = `id, token, owner_id, owner_name, name, title, album_id, album_name, key_id, key_enc, password, ends_at, state, created_at, wm_mode, mark_text, pick_mode, pick_limit, claim_who, claim_name`

func (s *Store) scanRoom(sc interface{ Scan(...any) error }) (Room, error) {
	var r Room
	var owner, album, key, ends, created string
	var enc []byte
	if err := sc.Scan(&r.ID, &r.Token, &owner, &r.OwnerName, &r.Name, &r.Title, &album, &r.AlbumName, &key, &enc, &r.Password, &ends, &r.State, &created, &r.Mark, &r.MarkText, &r.PickMode, &r.PickLimit, &r.ClaimWho, &r.ClaimName); err != nil {
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
	if r.Mark == "" {
		r.Mark = "text"
	}
	if r.PickMode == "" {
		r.PickMode = RoomShared
	}
	r.CreatedAt = time.Now().UTC()
	enc, err := s.vault.Seal([]byte(r.Key), r.ID)
	if err != nil {
		return r, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO rooms (`+roomCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Token, r.OwnerID.String(), r.OwnerName, r.Name, r.Title, r.AlbumID.String(), r.AlbumName, r.KeyID.String(), enc, r.Password, tstr(r.EndsAt), r.State, r.CreatedAt.Format(time.RFC3339Nano), r.Mark, r.MarkText, r.PickMode, r.PickLimit, r.ClaimWho, r.ClaimName)
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
	for _, q := range []string{`DELETE FROM room_picks WHERE room_id = ?`, `DELETE FROM room_done WHERE room_id = ?`, `DELETE FROM rooms WHERE id = ?`} {
		if _, err := s.db.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return nil
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

// SetRoomLogo keeps the logo a room puts on its pictures (kind is its content type; empty data removes it).
func (s *Store) SetRoomLogo(ctx context.Context, id, kind string, data []byte) error {
	_, err := s.db.ExecContext(ctx, `UPDATE rooms SET logo_kind = ?, logo = ? WHERE id = ?`, kind, data, id)
	return err
}

// RoomLogo is the room's logo (empty when it has none).
func (s *Store) RoomLogo(ctx context.Context, id string) (kind string, data []byte, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT logo_kind, logo FROM rooms WHERE id = ?`, id).Scan(&kind, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	return kind, data, err
}

// ErrPickLimit and ErrPickLocked are what TogglePick says when a pick cannot be added.
var (
	ErrPickLimit  = errors.New("store: the selection is full")
	ErrPickLocked = errors.New("store: the selection is finished")
)

// TogglePick adds a pick, or takes it away if it is there, in one selection (room, round, who), and returns whether the photo is
// picked now and how many the selection holds. A finished selection cannot change, and one with `limit` picks (0: no limit) takes
// no more.
func (s *Store) TogglePick(ctx context.Context, room string, round int, who, photo, name string, limit int) (picked bool, count int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM room_done WHERE room_id = ? AND round = ? AND who = ?`, room, round, who).Scan(&n); err != nil {
		return false, 0, err
	}
	if n > 0 {
		return false, 0, ErrPickLocked
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM room_picks WHERE room_id = ? AND round = ? AND who = ? AND photo = ?`, room, round, who, photo)
	if err != nil {
		return false, 0, err
	}
	if k, _ := res.RowsAffected(); k == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM room_picks WHERE room_id = ? AND round = ? AND who = ?`, room, round, who).Scan(&n); err != nil {
			return false, 0, err
		}
		if limit > 0 && n >= limit {
			return false, n, ErrPickLimit
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO room_picks (room_id, round, who, photo, name, at) VALUES (?,?,?,?,?,?)`, room, round, who, photo, name, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return false, 0, err
		}
		picked = true
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM room_picks WHERE room_id = ? AND round = ? AND who = ?`, room, round, who).Scan(&count); err != nil {
		return false, 0, err
	}
	return picked, count, tx.Commit()
}

// Pick is one choice, with the name of who made it.
type Pick struct {
	Photo, Who, Name string
	At               time.Time
}

// Picks lists the picks of one selection (who set) or of all of them (who nil), oldest first.
func (s *Store) Picks(ctx context.Context, room string, round int, who *string) ([]Pick, error) {
	q, args := `SELECT photo, who, name, at FROM room_picks WHERE room_id = ? AND round = ?`, []any{room, round}
	if who != nil {
		q += ` AND who = ?`
		args = append(args, *who)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY at, photo`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Pick
	for rows.Next() {
		var p Pick
		var at string
		if err := rows.Scan(&p.Photo, &p.Who, &p.Name, &at); err != nil {
			return nil, err
		}
		p.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, p)
	}
	return out, rows.Err()
}

// FinishSelection marks a selection as sent to the photographer. It reports false when it already was.
func (s *Store) FinishSelection(ctx context.Context, room string, round int, who, name string) (bool, error) {
	r, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO room_done (room_id, round, who, name, at) VALUES (?,?,?,?,?)`, room, round, who, name, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

// SelectionDone reports whether a selection was sent.
func (s *Store) SelectionDone(ctx context.Context, room string, round int, who string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM room_done WHERE room_id = ? AND round = ? AND who = ?`, room, round, who).Scan(&n)
	return n > 0, err
}

// ReopenSelection lets a sent selection be changed again.
func (s *Store) ReopenSelection(ctx context.Context, room string, round int, who string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM room_done WHERE room_id = ? AND round = ? AND who = ?`, room, round, who)
	return err
}

// ClaimRoom lets one visitor claim a room that has one named client. It reports false when somebody already has.
func (s *Store) ClaimRoom(ctx context.Context, id, who, name string) (bool, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE rooms SET claim_who = ?, claim_name = ? WHERE id = ? AND claim_who = ''`, who, name, id)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

// SelectionSummary is one selection of a room as the photographer sees it: whose it is, how many photos it holds, and whether
// it was sent. Who is empty for the shared selection and the named client's.
type SelectionSummary struct {
	Who, Name string
	Count     int
	Done      bool
	DoneAt    time.Time
}

// SelectionsOf lists the selections of one round of a room that hold a pick or were sent, those sent first.
func (s *Store) SelectionsOf(ctx context.Context, room string, round int) ([]SelectionSummary, error) {
	picks, err := s.Picks(ctx, room, round, nil)
	if err != nil {
		return nil, err
	}
	by := map[string]*SelectionSummary{}
	var order []string
	add := func(who string) *SelectionSummary {
		if x, ok := by[who]; ok {
			return x
		}
		x := &SelectionSummary{Who: who}
		by[who] = x
		order = append(order, who)
		return x
	}
	for _, p := range picks {
		x := add(p.Who)
		x.Count++
		if p.Name != "" {
			x.Name = p.Name
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT who, name, at FROM room_done WHERE room_id = ? AND round = ? ORDER BY at`, room, round)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var who, name, at string
		if err := rows.Scan(&who, &name, &at); err != nil {
			return nil, err
		}
		x := add(who)
		x.Done = true
		x.DoneAt, _ = time.Parse(time.RFC3339Nano, at)
		if name != "" {
			x.Name = name
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []SelectionSummary
	for _, who := range order {
		out = append(out, *by[who])
	}
	sortSelections(out)
	return out, nil
}

func sortSelections(l []SelectionSummary) {
	for i := 1; i < len(l); i++ {
		for j := i; j > 0 && less(l[j], l[j-1]); j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
}

func less(a, b SelectionSummary) bool {
	if a.Done != b.Done {
		return a.Done
	}
	return a.DoneAt.Before(b.DoneAt)
}
