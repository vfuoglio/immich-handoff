package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// AlbumKey is the key a person made, from their own session, for smart albums:
// it can read their photos, make and fill albums of their own (and, when they allow it, take photos out of them), and nothing else. The secret is stored sealed.
type AlbumKey struct {
	KeyID uuid.UUID
	Key   string
}

// AlbumKey returns the person's key for smart albums, if they have one.
func (s *Store) AlbumKey(ctx context.Context, user uuid.UUID) (AlbumKey, bool, error) {
	var id string
	var enc []byte
	err := s.db.QueryRowContext(ctx, `SELECT key_id, key_enc FROM album_keys WHERE user_id = ?`, user.String()).Scan(&id, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return AlbumKey{}, false, nil
	}
	if err != nil {
		return AlbumKey{}, false, err
	}
	plain, err := s.vault.Open(enc, "albums:"+user.String())
	if err != nil {
		return AlbumKey{}, false, err
	}
	k := AlbumKey{Key: string(plain)}
	k.KeyID, _ = uuid.Parse(id)
	return k, true, nil
}

// SetAlbumKey keeps the person's key, replacing an earlier one.
func (s *Store) SetAlbumKey(ctx context.Context, user uuid.UUID, k AlbumKey) error {
	enc, err := s.vault.Seal([]byte(k.Key), "albums:"+user.String())
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO album_keys (user_id, key_id, key_enc, created_at) VALUES (?,?,?,?)
	  ON CONFLICT(user_id) DO UPDATE SET key_id = excluded.key_id, key_enc = excluded.key_enc`,
		user.String(), k.KeyID.String(), enc, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// DeleteAlbumKey forgets the person's key (the app has deleted it in their Immich, or they did).
func (s *Store) DeleteAlbumKey(ctx context.Context, user uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM album_keys WHERE user_id = ?`, user.String())
	return err
}
