package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// LibraryKey is the key a person made, from their own session, for following albums shared with them: it can read what is
// shared with them and add to their own library, and nothing else. The secret is stored sealed.
type LibraryKey struct {
	KeyID uuid.UUID
	Key   string
}

// LibraryKey returns the person's key for following albums, if they have one.
func (s *Store) LibraryKey(ctx context.Context, user uuid.UUID) (LibraryKey, bool, error) {
	var id string
	var enc []byte
	err := s.db.QueryRowContext(ctx, `SELECT key_id, key_enc FROM library_keys WHERE user_id = ?`, user.String()).Scan(&id, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return LibraryKey{}, false, nil
	}
	if err != nil {
		return LibraryKey{}, false, err
	}
	plain, err := s.vault.Open(enc, "library:"+user.String())
	if err != nil {
		return LibraryKey{}, false, err
	}
	k := LibraryKey{Key: string(plain)}
	k.KeyID, _ = uuid.Parse(id)
	return k, true, nil
}

// SetLibraryKey keeps the person's key, replacing an earlier one.
func (s *Store) SetLibraryKey(ctx context.Context, user uuid.UUID, k LibraryKey) error {
	enc, err := s.vault.Seal([]byte(k.Key), "library:"+user.String())
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO library_keys (user_id, key_id, key_enc, created_at) VALUES (?,?,?,?)
	  ON CONFLICT(user_id) DO UPDATE SET key_id = excluded.key_id, key_enc = excluded.key_enc`,
		user.String(), k.KeyID.String(), enc, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// DeleteLibraryKey forgets the person's key (the app has deleted it in their Immich, or they did).
func (s *Store) DeleteLibraryKey(ctx context.Context, user uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM library_keys WHERE user_id = ?`, user.String())
	return err
}
