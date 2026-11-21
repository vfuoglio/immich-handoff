package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// CircleKey is the key a person made, from their own session, for circles:
// it can read their albums and add a person to one or take one off, and nothing else. The secret is stored sealed.
type CircleKey struct {
	KeyID uuid.UUID
	Key   string
}

// CircleKey returns the person's key for circles, if they have one.
func (s *Store) CircleKey(ctx context.Context, user uuid.UUID) (CircleKey, bool, error) {
	var id string
	var enc []byte
	err := s.db.QueryRowContext(ctx, `SELECT key_id, key_enc FROM circle_keys WHERE user_id = ?`, user.String()).Scan(&id, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return CircleKey{}, false, nil
	}
	if err != nil {
		return CircleKey{}, false, err
	}
	plain, err := s.vault.Open(enc, "circles:"+user.String())
	if err != nil {
		return CircleKey{}, false, err
	}
	k := CircleKey{Key: string(plain)}
	k.KeyID, _ = uuid.Parse(id)
	return k, true, nil
}

// SetCircleKey keeps the person's key, replacing an earlier one.
func (s *Store) SetCircleKey(ctx context.Context, user uuid.UUID, k CircleKey) error {
	enc, err := s.vault.Seal([]byte(k.Key), "circles:"+user.String())
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO circle_keys (user_id, key_id, key_enc, created_at) VALUES (?,?,?,?)
	  ON CONFLICT(user_id) DO UPDATE SET key_id = excluded.key_id, key_enc = excluded.key_enc`,
		user.String(), k.KeyID.String(), enc, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// DeleteCircleKey forgets the person's key (the app has deleted it in their Immich, or they did).
func (s *Store) DeleteCircleKey(ctx context.Context, user uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM circle_keys WHERE user_id = ?`, user.String())
	return err
}
