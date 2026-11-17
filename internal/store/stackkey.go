package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// StackKey is the key a person made, from their own session, for stacking by rule:
// it can search their photos, make a stack and take it apart, and nothing else. The secret is stored sealed.
type StackKey struct {
	KeyID uuid.UUID
	Key   string
}

// StackKey returns the person's key for stacking, if they have one.
func (s *Store) StackKey(ctx context.Context, user uuid.UUID) (StackKey, bool, error) {
	var id string
	var enc []byte
	err := s.db.QueryRowContext(ctx, `SELECT key_id, key_enc FROM stack_keys WHERE user_id = ?`, user.String()).Scan(&id, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return StackKey{}, false, nil
	}
	if err != nil {
		return StackKey{}, false, err
	}
	plain, err := s.vault.Open(enc, "stacks:"+user.String())
	if err != nil {
		return StackKey{}, false, err
	}
	k := StackKey{Key: string(plain)}
	k.KeyID, _ = uuid.Parse(id)
	return k, true, nil
}

// SetStackKey keeps the person's key, replacing an earlier one.
func (s *Store) SetStackKey(ctx context.Context, user uuid.UUID, k StackKey) error {
	enc, err := s.vault.Seal([]byte(k.Key), "stacks:"+user.String())
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO stack_keys (user_id, key_id, key_enc, created_at) VALUES (?,?,?,?)
	  ON CONFLICT(user_id) DO UPDATE SET key_id = excluded.key_id, key_enc = excluded.key_enc`,
		user.String(), k.KeyID.String(), enc, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// DeleteStackKey forgets the person's key (the app has deleted it in their Immich, or they did).
func (s *Store) DeleteStackKey(ctx context.Context, user uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM stack_keys WHERE user_id = ?`, user.String())
	return err
}
