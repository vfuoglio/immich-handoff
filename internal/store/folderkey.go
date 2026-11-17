package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// FolderKey is the key a person made, from their own session, for albums made from the folders of their external library:
// it can read their photos and make and fill albums of their own, and nothing else. The secret is stored sealed.
type FolderKey struct {
	KeyID uuid.UUID
	Key   string
}

// FolderKey returns the person's key for folder albums, if they have one.
func (s *Store) FolderKey(ctx context.Context, user uuid.UUID) (FolderKey, bool, error) {
	var id string
	var enc []byte
	err := s.db.QueryRowContext(ctx, `SELECT key_id, key_enc FROM folder_keys WHERE user_id = ?`, user.String()).Scan(&id, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return FolderKey{}, false, nil
	}
	if err != nil {
		return FolderKey{}, false, err
	}
	plain, err := s.vault.Open(enc, "folders:"+user.String())
	if err != nil {
		return FolderKey{}, false, err
	}
	k := FolderKey{Key: string(plain)}
	k.KeyID, _ = uuid.Parse(id)
	return k, true, nil
}

// SetFolderKey keeps the person's key, replacing an earlier one.
func (s *Store) SetFolderKey(ctx context.Context, user uuid.UUID, k FolderKey) error {
	enc, err := s.vault.Seal([]byte(k.Key), "folders:"+user.String())
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO folder_keys (user_id, key_id, key_enc, created_at) VALUES (?,?,?,?)
	  ON CONFLICT(user_id) DO UPDATE SET key_id = excluded.key_id, key_enc = excluded.key_enc`,
		user.String(), k.KeyID.String(), enc, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// DeleteFolderKey forgets the person's key (the app has deleted it in their Immich, or they did).
func (s *Store) DeleteFolderKey(ctx context.Context, user uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM folder_keys WHERE user_id = ?`, user.String())
	return err
}
