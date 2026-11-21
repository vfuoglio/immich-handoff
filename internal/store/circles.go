package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// A circle (#270) is a named group of people. Every new album its owner makes is offered to them (or, once the owner has let it,
// shared with them at once), with the owner's own key. What the app shared is recorded album by album and person by person, so that
// taking a person out undoes only that, and a person who left an album by hand is not added again.

// Circle modes: the same two as a rule.
const (
	CircleSuggest = RuleSuggest
	CircleAuto    = RuleAuto
)

// What a circle has done about one album for one person.
const (
	CircleOffered  = "offered"  // waiting for the owner's answer
	CircleShared   = "shared"   // the app added the person to the album
	CircleDeclined = "declined" // the owner said no: never offered again
	CircleHad      = "had"      // the person was on the album already: not touched, never taken off by the app
	CircleLeft     = "left"     // the app added the person, who has since left the album: not added again
)

// Circle is one group of a person's.
type Circle struct {
	ID        string
	OwnerID   uuid.UUID
	Name      string
	Role      string // "viewer" or "editor"
	Prefix    string // only albums whose name starts with this (empty: every album)
	Mode      string
	Always    int
	Paused    bool
	Since     time.Time // the circle only looks at albums made after this
	AutoSince time.Time // when sharing by itself was switched on (zero: it is not on)
	LastLook  time.Time
	Problem   string
	CreatedAt time.Time
}

// CircleMember is a person in a circle.
type CircleMember struct {
	UserID      uuid.UUID
	Name, Email string
}

// CircleShare is what a circle did, or would do, with one album for one person.
type CircleShare struct {
	CircleID  string
	AlbumID   uuid.UUID
	AlbumName string
	UserID    uuid.UUID
	State     string
	At        time.Time
}

const circleCols = `id, owner_id, name, role, prefix, mode, always, paused, since, auto_since, last_look, problem, created_at`

func scanCircle(sc interface{ Scan(...any) error }) (Circle, error) {
	var c Circle
	var owner, since, auto, last, created string
	if err := sc.Scan(&c.ID, &owner, &c.Name, &c.Role, &c.Prefix, &c.Mode, &c.Always, &c.Paused, &since, &auto, &last, &c.Problem, &created); err != nil {
		return c, err
	}
	c.OwnerID, _ = uuid.Parse(owner)
	c.Since, _ = time.Parse(time.RFC3339Nano, since)
	c.AutoSince, _ = time.Parse(time.RFC3339Nano, auto)
	c.LastLook, _ = time.Parse(time.RFC3339Nano, last)
	c.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return c, nil
}

// AddCircle stores a circle. It looks only at albums made after now.
func (s *Store) AddCircle(ctx context.Context, c Circle) (Circle, error) {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	if c.Mode == "" {
		c.Mode = CircleSuggest
	}
	c.CreatedAt = time.Now().UTC()
	if c.Since.IsZero() {
		c.Since = c.CreatedAt
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO circles (`+circleCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.OwnerID.String(), c.Name, c.Role, c.Prefix, c.Mode, c.Always, b2i(c.Paused), c.Since.Format(time.RFC3339Nano), tstr(c.AutoSince), tstr(c.LastLook), c.Problem, c.CreatedAt.Format(time.RFC3339Nano))
	return c, err
}

// CircleByID finds a circle.
func (s *Store) CircleByID(ctx context.Context, id string) (Circle, error) {
	c, err := scanCircle(s.db.QueryRowContext(ctx, `SELECT `+circleCols+` FROM circles WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Circle{}, ErrNotFound
	}
	return c, err
}

func (s *Store) circlesQuery(ctx context.Context, q string, args ...any) ([]Circle, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Circle
	for rows.Next() {
		c, err := scanCircle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CirclesOf lists a person's circles, oldest first.
func (s *Store) CirclesOf(ctx context.Context, owner uuid.UUID) ([]Circle, error) {
	return s.circlesQuery(ctx, `SELECT `+circleCols+` FROM circles WHERE owner_id = ? ORDER BY created_at, id`, owner.String())
}

// AllCircles lists every circle, for the sweep.
func (s *Store) AllCircles(ctx context.Context) ([]Circle, error) {
	return s.circlesQuery(ctx, `SELECT `+circleCols+` FROM circles ORDER BY created_at, id`)
}

// DeleteCircle forgets a circle, its members and what it recorded. What it shared stays shared.
func (s *Store) DeleteCircle(ctx context.Context, id string) error {
	for _, q := range []string{`DELETE FROM circle_shares WHERE circle_id = ?`, `DELETE FROM circle_members WHERE circle_id = ?`, `DELETE FROM circles WHERE id = ?`} {
		if _, err := s.db.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return nil
}

// SetCirclePaused pauses or resumes a circle.
func (s *Store) SetCirclePaused(ctx context.Context, id string, paused bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE circles SET paused = ? WHERE id = ?`, b2i(paused), id)
	return err
}

// AddCircleAlways counts one more "Always" answer.
func (s *Store) AddCircleAlways(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE circles SET always = always + 1 WHERE id = ?`, id)
	return err
}

// SetCircleMode switches a circle between asking first and sharing by itself; switching to auto records the moment.
func (s *Store) SetCircleMode(ctx context.Context, id, mode string, at time.Time) error {
	auto := ""
	if mode == CircleAuto {
		auto = at.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE circles SET mode = ?, auto_since = ? WHERE id = ?`, mode, auto, id)
	return err
}

// MarkCircleLook records when the sweep looked at a circle and what went wrong (empty: nothing).
func (s *Store) MarkCircleLook(ctx context.Context, id string, at time.Time, problem string) {
	s.db.ExecContext(ctx, `UPDATE circles SET last_look = ?, problem = ? WHERE id = ?`, at.UTC().Format(time.RFC3339Nano), problem, id)
}

// AddCircleMember puts a person in a circle (again is not an error).
func (s *Store) AddCircleMember(ctx context.Context, circle string, m CircleMember) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO circle_members (circle_id, user_id, name, email) VALUES (?,?,?,?)
		ON CONFLICT(circle_id, user_id) DO UPDATE SET name = excluded.name, email = excluded.email`, circle, m.UserID.String(), m.Name, m.Email)
	return err
}

// RemoveCircleMember takes a person out of a circle and forgets the offers made for them (not the shares: the hub unshares first).
func (s *Store) RemoveCircleMember(ctx context.Context, circle string, user uuid.UUID) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM circle_shares WHERE circle_id = ? AND user_id = ?`, circle, user.String()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM circle_members WHERE circle_id = ? AND user_id = ?`, circle, user.String())
	return err
}

// CircleMembers lists the people in a circle, by name.
func (s *Store) CircleMembers(ctx context.Context, circle string) ([]CircleMember, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, name, email FROM circle_members WHERE circle_id = ? ORDER BY lower(name), user_id`, circle)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CircleMember
	for rows.Next() {
		var m CircleMember
		var id string
		if err := rows.Scan(&id, &m.Name, &m.Email); err != nil {
			return nil, err
		}
		m.UserID, _ = uuid.Parse(id)
		out = append(out, m)
	}
	return out, rows.Err()
}

// PutCircleShare records what a circle did with an album for a person.
func (s *Store) PutCircleShare(ctx context.Context, sh CircleShare) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO circle_shares (circle_id, album_id, user_id, album_name, state, at) VALUES (?,?,?,?,?,?)
		ON CONFLICT(circle_id, album_id, user_id) DO UPDATE SET state = excluded.state, at = excluded.at`,
		sh.CircleID, sh.AlbumID.String(), sh.UserID.String(), sh.AlbumName, sh.State, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// CircleShares lists what a circle recorded, with the given state (empty: every state).
func (s *Store) CircleShares(ctx context.Context, circle, state string) ([]CircleShare, error) {
	q, args := `SELECT album_id, user_id, album_name, state, at FROM circle_shares WHERE circle_id = ?`, []any{circle}
	if state != "" {
		q, args = q+` AND state = ?`, append(args, state)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY at, album_id, user_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CircleShare
	for rows.Next() {
		sh := CircleShare{CircleID: circle}
		var album, user, at string
		if err := rows.Scan(&album, &user, &sh.AlbumName, &sh.State, &at); err != nil {
			return nil, err
		}
		sh.AlbumID, _ = uuid.Parse(album)
		sh.UserID, _ = uuid.Parse(user)
		sh.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, sh)
	}
	return out, rows.Err()
}

// SetCircleShareState changes the state of the given shares of a circle (one album and person each) from one state to another.
func (s *Store) SetCircleShareState(ctx context.Context, circle string, album, user uuid.UUID, from, to string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE circle_shares SET state = ?, at = ? WHERE circle_id = ? AND album_id = ? AND user_id = ? AND state = ?`,
		to, time.Now().UTC().Format(time.RFC3339Nano), circle, album.String(), user.String(), from)
	return err
}

// Meta flags the administrator sets from the page (not secrets).

// CircleMethod is how a person is chosen for a circle: 1, they type an email (the default), or 2, they pick from every account.
func (s *Store) CircleMethod(ctx context.Context) int {
	var v string
	if err := s.db.QueryRowContext(ctx, `SELECT v FROM settings WHERE k = 'circle-method'`).Scan(&v); err == nil && v == "2" {
		return 2
	}
	return 1
}

// SetCircleMethod keeps the administrator's choice.
func (s *Store) SetCircleMethod(ctx context.Context, method int) error {
	v := "1"
	if method == 2 {
		v = "2"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (k, v) VALUES ('circle-method', ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, v)
	return err
}
