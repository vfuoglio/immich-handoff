package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// PlaceKey is the key a person made, from their own session, for places:
// it can read their photos and write the place of a photo, and nothing else the app does. The secret is stored sealed.
type PlaceKey struct {
	KeyID uuid.UUID
	Key   string
}

// PlaceKey returns the person's key for places, if they have one.
func (s *Store) PlaceKey(ctx context.Context, user uuid.UUID) (PlaceKey, bool, error) {
	var id string
	var enc []byte
	err := s.db.QueryRowContext(ctx, `SELECT key_id, key_enc FROM place_keys WHERE user_id = ?`, user.String()).Scan(&id, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return PlaceKey{}, false, nil
	}
	if err != nil {
		return PlaceKey{}, false, err
	}
	plain, err := s.vault.Open(enc, "places:"+user.String())
	if err != nil {
		return PlaceKey{}, false, err
	}
	k := PlaceKey{Key: string(plain)}
	k.KeyID, _ = uuid.Parse(id)
	return k, true, nil
}

// SetPlaceKey keeps the person's key, replacing an earlier one.
func (s *Store) SetPlaceKey(ctx context.Context, user uuid.UUID, k PlaceKey) error {
	enc, err := s.vault.Seal([]byte(k.Key), "places:"+user.String())
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO place_keys (user_id, key_id, key_enc, created_at) VALUES (?,?,?,?)
	  ON CONFLICT(user_id) DO UPDATE SET key_id = excluded.key_id, key_enc = excluded.key_enc`,
		user.String(), k.KeyID.String(), enc, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// DeletePlaceKey forgets the person's key (the app has deleted it in their Immich, or they did).
func (s *Store) DeletePlaceKey(ctx context.Context, user uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM place_keys WHERE user_id = ?`, user.String())
	return err
}
