package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// A cast session (#269) is a TV that a person has paired with a code: it shows photos of one source until the person stops it or it
// goes quiet. The TV holds the session's id as its only secret; the code it showed before is not stored (waiting codes live in the
// app's memory).

// Sources a TV can show.
const (
	CastAlbum  = "album"
	CastPerson = "person"
	CastNew    = "new"
	CastMemory = "memory"
	CastStory  = "story" // one of the person's stories, played in order
)

// CastSession is one paired TV.
type CastSession struct {
	ID         string // the TV's secret
	UserID     uuid.UUID
	Kind       string // CastAlbum, CastPerson, CastNew or CastMemory
	SourceID   string // the album or person's id
	SourceName string // what the person called it, for their card
	Day        string // for a memory: YYYY-MM-DD
	Seconds    int    // how long each photo stays
	FX         string // how one photo gives way to the next: fade, zoom, slide or rise
	Paused     bool
	Seq        int    // counts the commands the person has given (next, previous)
	Cmd        string // the last of them
	PairedAt   time.Time
	LastSeen   time.Time
	Ended      bool
}

const castCols = `id, user_id, kind, source_id, source_name, day, seconds, fx, paused, seq, cmd, paired_at, last_seen, ended`

func scanCast(sc interface{ Scan(...any) error }) (CastSession, error) {
	var c CastSession
	var user, paired, seen string
	if err := sc.Scan(&c.ID, &user, &c.Kind, &c.SourceID, &c.SourceName, &c.Day, &c.Seconds, &c.FX, &c.Paused, &c.Seq, &c.Cmd, &paired, &seen, &c.Ended); err != nil {
		return c, err
	}
	c.UserID, _ = uuid.Parse(user)
	c.PairedAt, _ = time.Parse(time.RFC3339Nano, paired)
	c.LastSeen, _ = time.Parse(time.RFC3339Nano, seen)
	return c, nil
}

// AddCastSession stores a paired TV.
func (s *Store) AddCastSession(ctx context.Context, c CastSession) (CastSession, error) {
	c.PairedAt = time.Now().UTC()
	c.LastSeen = c.PairedAt
	_, err := s.db.ExecContext(ctx, `INSERT INTO cast_sessions (`+castCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.UserID.String(), c.Kind, c.SourceID, c.SourceName, c.Day, c.Seconds, c.FX, b2i(c.Paused), c.Seq, c.Cmd,
		c.PairedAt.Format(time.RFC3339Nano), c.LastSeen.Format(time.RFC3339Nano), b2i(c.Ended))
	return c, err
}

// CastSessionByID finds a TV by its secret (an ended one too: it has to be told it ended).
func (s *Store) CastSessionByID(ctx context.Context, id string) (CastSession, error) {
	c, err := scanCast(s.db.QueryRowContext(ctx, `SELECT `+castCols+` FROM cast_sessions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return CastSession{}, ErrNotFound
	}
	return c, err
}

// CastSessionsOf lists a person's TVs that have not ended, oldest first.
func (s *Store) CastSessionsOf(ctx context.Context, user uuid.UUID) ([]CastSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+castCols+` FROM cast_sessions WHERE user_id = ? AND ended = 0 ORDER BY paired_at, id`, user.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CastSession
	for rows.Next() {
		c, err := scanCast(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetCastSource changes what a TV shows (and, so it starts over, counts it as a command).
func (s *Store) SetCastSource(ctx context.Context, id, kind, source, name, day string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cast_sessions SET kind = ?, source_id = ?, source_name = ?, day = ?, seq = seq + 1, cmd = 'source' WHERE id = ? AND ended = 0`, kind, source, name, day, id)
	return err
}

// SetCastPlayback sets how a TV plays: paused or not, how long each photo stays and the effect.
func (s *Store) SetCastPlayback(ctx context.Context, id string, paused bool, seconds int, fx string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cast_sessions SET paused = ?, seconds = ?, fx = ? WHERE id = ? AND ended = 0`, b2i(paused), seconds, fx, id)
	return err
}

// CommandCast gives a TV a command to carry out once (next or previous).
func (s *Store) CommandCast(ctx context.Context, id, cmd string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cast_sessions SET seq = seq + 1, cmd = ? WHERE id = ? AND ended = 0`, cmd, id)
	return err
}

// TouchCast records that the TV asked, so that one that has gone quiet can be ended.
func (s *Store) TouchCast(ctx context.Context, id string, at time.Time) {
	s.db.ExecContext(ctx, `UPDATE cast_sessions SET last_seen = ? WHERE id = ?`, at.UTC().Format(time.RFC3339Nano), id)
}

// EndCast ends a TV.
func (s *Store) EndCast(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cast_sessions SET ended = 1 WHERE id = ?`, id)
	return err
}

// EndStaleCasts ends the TVs that have not asked since idleBefore or were paired before olderThan, and forgets ended ones that are
// more than a day old. It returns how many it ended.
func (s *Store) EndStaleCasts(ctx context.Context, idleBefore, olderThan time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE cast_sessions SET ended = 1 WHERE ended = 0 AND (last_seen < ? OR paired_at < ?)`,
		idleBefore.UTC().Format(time.RFC3339Nano), olderThan.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	s.db.ExecContext(ctx, `DELETE FROM cast_sessions WHERE ended = 1 AND last_seen < ?`, time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339Nano))
	n, _ := res.RowsAffected()
	return int(n), nil
}

// CountActiveCasts says how many TVs of a person have not ended.
func (s *Store) CountActiveCasts(ctx context.Context, user uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM cast_sessions WHERE user_id = ? AND ended = 0`, user.String()).Scan(&n)
	return n, err
}
