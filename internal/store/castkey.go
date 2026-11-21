package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// CastKey is the key a person made, from their own session, for casting to a TV:
// it can look at their pictures and read their library to list a source, and nothing else. The secret is stored sealed.
type CastKey struct {
	KeyID uuid.UUID
	Key   string
}

// CastKey returns the person's key for casting to a TV, if they have one.
func (s *Store) CastKey(ctx context.Context, user uuid.UUID) (CastKey, bool, error) {
	var id string
	var enc []byte
	err := s.db.QueryRowContext(ctx, `SELECT key_id, key_enc FROM cast_keys WHERE user_id = ?`, user.String()).Scan(&id, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return CastKey{}, false, nil
	}
	if err != nil {
		return CastKey{}, false, err
	}
	plain, err := s.vault.Open(enc, "cast:"+user.String())
	if err != nil {
		return CastKey{}, false, err
	}
	k := CastKey{Key: string(plain)}
	k.KeyID, _ = uuid.Parse(id)
	return k, true, nil
}

// SetCastKey keeps the person's key, replacing an earlier one.
func (s *Store) SetCastKey(ctx context.Context, user uuid.UUID, k CastKey) error {
	enc, err := s.vault.Seal([]byte(k.Key), "cast:"+user.String())
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO cast_keys (user_id, key_id, key_enc, created_at) VALUES (?,?,?,?)
	  ON CONFLICT(user_id) DO UPDATE SET key_id = excluded.key_id, key_enc = excluded.key_enc`,
		user.String(), k.KeyID.String(), enc, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// DeleteCastKey forgets the person's key (the app has deleted it in their Immich, or they did).
func (s *Store) DeleteCastKey(ctx context.Context, user uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM cast_keys WHERE user_id = ?`, user.String())
	return err
}
