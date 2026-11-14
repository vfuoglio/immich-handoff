package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Shared album states.
const (
	ShareProposed = "proposed" // this side proposed; the other has not answered
	ShareInvited  = "invited"  // the other side proposed; this side has not answered
	ShareActive   = "active"
	ShareDeclined = "declined"
	ShareLeft     = "left" // the other side ended it
)

// What history means, and the deletion rules.
const (
	HistoryAll = "all" // share what is in the album already
	HistoryNew = "new" // only what is added from now on

	DeleteNothing = "nothing" // nothing travels
	DeleteFollow  = "follow"  // removal from the shared album follows (the photo itself stays)
)

// SharedAlbum is one album shared with a peer.
type SharedAlbum struct {
	ID, PeerID, TheirID string
	OwnerID             uuid.UUID
	Name                string // what the album is called between the two people
	AlbumID             uuid.UUID
	AlbumName           string // the person's own album
	KeyID               uuid.UUID
	Key                 string // decrypted
	State               string
	Proposer            bool
	History             string // what this side shares
	TheirHistory        string // what the other side shares
	Deletion            string
	CreatedAt           time.Time
}

const sharedCols = `id, owner_id, peer_id, their_id, name, album_id, album_name, key_id, key_enc, state, proposer, history, their_history, deletion, created_at`

func (s *Store) scanShared(sc interface{ Scan(...any) error }) (SharedAlbum, error) {
	var a SharedAlbum
	var owner, album, key, created string
	var enc []byte
	if err := sc.Scan(&a.ID, &owner, &a.PeerID, &a.TheirID, &a.Name, &album, &a.AlbumName, &key, &enc, &a.State, &a.Proposer, &a.History, &a.TheirHistory, &a.Deletion, &created); err != nil {
		return a, err
	}
	a.OwnerID, _ = uuid.Parse(owner)
	a.AlbumID, _ = uuid.Parse(album)
	a.KeyID, _ = uuid.Parse(key)
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if len(enc) > 0 {
		plain, err := s.vault.Open(enc, a.ID)
		if err != nil {
			return a, err
		}
		a.Key = string(plain)
	}
	return a, nil
}

// AddSharedAlbum stores a shared album (its key sealed, if it has one).
func (s *Store) AddSharedAlbum(ctx context.Context, a SharedAlbum) (SharedAlbum, error) {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	a.CreatedAt = time.Now().UTC()
	var enc []byte
	if a.Key != "" {
		var err error
		if enc, err = s.vault.Seal([]byte(a.Key), a.ID); err != nil {
			return a, err
		}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO shared_albums (`+sharedCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.OwnerID.String(), a.PeerID, a.TheirID, a.Name, nilUUID(a.AlbumID), a.AlbumName, nilUUID(a.KeyID), enc, a.State, b2i(a.Proposer), a.History, a.TheirHistory, a.Deletion, a.CreatedAt.Format(time.RFC3339Nano))
	return a, err
}

func nilUUID(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}

// SharedAlbumByID finds one.
func (s *Store) SharedAlbumByID(ctx context.Context, id string) (SharedAlbum, error) {
	a, err := s.scanShared(s.db.QueryRowContext(ctx, `SELECT `+sharedCols+` FROM shared_albums WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return SharedAlbum{}, ErrNotFound
	}
	return a, err
}

// SharedAlbumsOf are a person's shared albums, oldest first.
func (s *Store) SharedAlbumsOf(ctx context.Context, owner uuid.UUID) ([]SharedAlbum, error) {
	return s.sharedQuery(ctx, `SELECT `+sharedCols+` FROM shared_albums WHERE owner_id = ? ORDER BY created_at, id`, owner.String())
}

// SharedAlbumsWith are the shared albums with one peer.
func (s *Store) SharedAlbumsWith(ctx context.Context, peer string) ([]SharedAlbum, error) {
	return s.sharedQuery(ctx, `SELECT `+sharedCols+` FROM shared_albums WHERE peer_id = ? ORDER BY created_at, id`, peer)
}

func (s *Store) sharedQuery(ctx context.Context, q string, args ...any) ([]SharedAlbum, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SharedAlbum
	for rows.Next() {
		a, err := s.scanShared(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ActivateSharedAlbum makes a shared album active, with the album, the key and the histories as agreed.
func (s *Store) ActivateSharedAlbum(ctx context.Context, id, theirID string, album uuid.UUID, albumName string, key uuid.UUID, secret, history, theirHistory string) error {
	var enc []byte
	if secret != "" {
		var err error
		if enc, err = s.vault.Seal([]byte(secret), id); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE shared_albums SET state = ?, their_id = ?, album_id = ?, album_name = ?, key_id = ?, key_enc = COALESCE(?, key_enc), history = ?, their_history = ? WHERE id = ?`,
		ShareActive, theirID, nilUUID(album), albumName, nilUUID(key), nilBytes(enc), history, theirHistory, id)
	return err
}

func nilBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// SetSharedState sets a shared album's state.
func (s *Store) SetSharedState(ctx context.Context, id, state string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE shared_albums SET state = ? WHERE id = ?`, state, id)
	return err
}

// SetSharedTheirID records the id the other side gave the shared album.
func (s *Store) SetSharedTheirID(ctx context.Context, id, theirID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE shared_albums SET their_id = ? WHERE id = ?`, theirID, id)
	return err
}

// DeleteSharedAlbum forgets a shared album and what the ledger knew of it.
func (s *Store) DeleteSharedAlbum(ctx context.Context, id string) error {
	for _, q := range []string{`DELETE FROM shared_ledger WHERE album_id = ?`, `DELETE FROM shared_albums WHERE id = ?`} {
		if _, err := s.db.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return nil
}

// DeleteSharedWith forgets every shared album with a peer (the connection ended).
func (s *Store) DeleteSharedWith(ctx context.Context, peer string) error {
	list, err := s.SharedAlbumsWith(ctx, peer)
	if err != nil {
		return err
	}
	for _, a := range list {
		if err := s.DeleteSharedAlbum(ctx, a.ID); err != nil {
			return err
		}
	}
	return nil
}

// Ledger origins.
const (
	OriginMine     = "mine"     // the photo was in this person's album and is (to be) sent
	OriginArrived  = "arrived"  // it came from the other side: never sent back
	OriginBaseline = "baseline" // it was there when sharing began and this side chose to share only what is new: never sent
)

// LedgerPut remembers a photo of a shared album by its checksum. A photo already remembered keeps its first origin.
func (s *Store) LedgerPut(ctx context.Context, album, checksum, asset, origin string, sent bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO shared_ledger (album_id, checksum, asset_id, origin, sent, at) VALUES (?,?,?,?,?,?)`,
		album, checksum, asset, origin, b2i(sent), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// LedgerHas reports whether the ledger knows a checksum, and its origin and whether it was sent.
func (s *Store) LedgerHas(ctx context.Context, album, checksum string) (origin string, sent bool, ok bool, err error) {
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT origin, sent FROM shared_ledger WHERE album_id = ? AND checksum = ?`, album, checksum).Scan(&origin, &n)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, false, nil
	}
	return origin, n > 0, err == nil, err
}

// LedgerCount is how many photos the ledger knows of an album.
func (s *Store) LedgerCount(ctx context.Context, album string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM shared_ledger WHERE album_id = ?`, album).Scan(&n)
	return n, err
}

// ActivateProposed makes a proposal this side made active, once the other side accepted: its id for the album and what it shares.
func (s *Store) ActivateProposed(ctx context.Context, id, theirID, theirHistory string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE shared_albums SET state = ?, their_id = ?, their_history = ? WHERE id = ? AND state = ?`, ShareActive, theirID, theirHistory, id, ShareProposed)
	return err
}

// ClearLedger forgets what the ledger knew of a shared album (a proposal that was not carried through).
func (s *Store) ClearLedger(ctx context.Context, album string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM shared_ledger WHERE album_id = ?`, album)
	return err
}

// SetSharedStateWith sets the state of every shared album with a peer that is not over yet (the other side ended the connection).
func (s *Store) SetSharedStateWith(ctx context.Context, peer, state string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE shared_albums SET state = ? WHERE peer_id = ? AND state <> ? AND state <> ?`, state, peer, ShareDeclined, ShareLeft)
	return err
}
