package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
)

// LibraryPref is where one person's "Add to my library" copies go by default: a tag naming who shared the photo, the
// library as it is, or an album of theirs.
type LibraryPref struct {
	Dest  string // "tag", "plain" or "album"
	Album string // the album's name, for "album"
}

// LibraryPref returns the person's choice, if they made one.
func (s *Store) LibraryPref(ctx context.Context, user uuid.UUID) (LibraryPref, bool, error) {
	var p LibraryPref
	err := s.db.QueryRowContext(ctx, `SELECT dest, album FROM library_prefs WHERE user_id = ?`, user.String()).Scan(&p.Dest, &p.Album)
	if errors.Is(err, sql.ErrNoRows) {
		return LibraryPref{}, false, nil
	}
	return p, err == nil, err
}

// SetLibraryPref saves the person's choice, replacing an earlier one.
func (s *Store) SetLibraryPref(ctx context.Context, user uuid.UUID, p LibraryPref) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO library_prefs (user_id, dest, album) VALUES (?,?,?)
	  ON CONFLICT(user_id) DO UPDATE SET dest = excluded.dest, album = excluded.album`, user.String(), p.Dest, p.Album)
	return err
}
