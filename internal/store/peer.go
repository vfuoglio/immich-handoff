package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Peer states and roles.
const (
	PeerActive = "active"
	PeerLeft   = "left" // the other side ended it; the person removes the record

	PeerInviter = "inviter" // this app made the invitation
	PeerJoiner  = "joiner"  // this app accepted one
)

// Peer is another Handoff app that this person shares albums with.
type Peer struct {
	ID         string
	OwnerID    uuid.UUID
	OwnerName  string
	Name       string // what the other person is called
	Address    string // the other app's address, without a trailing slash
	Secret     string // decrypted
	TheirID    string // the id the other app knows this one by
	Role       string
	State      string
	IPaused    bool // this side paused
	TheyPaused bool // the other side paused
	Version    int
	SeenAt     time.Time // the last time the other app answered
	Problem    string    // why the last attempt to reach it failed (empty: it answered)
	CreatedAt  time.Time
}

const peerCols = `id, owner_id, owner_name, name, address, secret_enc, their_id, role, state, i_paused, they_paused, version, seen_at, problem, created_at`

func (s *Store) scanPeer(sc interface{ Scan(...any) error }) (Peer, error) {
	var p Peer
	var owner, seen, created string
	var enc []byte
	if err := sc.Scan(&p.ID, &owner, &p.OwnerName, &p.Name, &p.Address, &enc, &p.TheirID, &p.Role, &p.State, &p.IPaused, &p.TheyPaused, &p.Version, &seen, &p.Problem, &created); err != nil {
		return p, err
	}
	p.OwnerID, _ = uuid.Parse(owner)
	p.SeenAt, _ = time.Parse(time.RFC3339Nano, seen)
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	plain, err := s.vault.Open(enc, p.ID)
	if err != nil {
		return p, err
	}
	p.Secret = string(plain)
	return p, nil
}

// AddPeer stores a peer (its secret sealed). The id is made here unless the caller gave one.
func (s *Store) AddPeer(ctx context.Context, p Peer) (Peer, error) {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if p.State == "" {
		p.State = PeerActive
	}
	if p.Version == 0 {
		p.Version = 1
	}
	p.CreatedAt = time.Now().UTC()
	enc, err := s.vault.Seal([]byte(p.Secret), p.ID)
	if err != nil {
		return p, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO peers (`+peerCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.OwnerID.String(), p.OwnerName, p.Name, p.Address, enc, p.TheirID, p.Role, p.State, b2i(p.IPaused), b2i(p.TheyPaused), p.Version, tstr(p.SeenAt), p.Problem, p.CreatedAt.Format(time.RFC3339Nano))
	return p, err
}

// PeerByID finds a peer by this app's id for it.
func (s *Store) PeerByID(ctx context.Context, id string) (Peer, error) {
	p, err := s.scanPeer(s.db.QueryRowContext(ctx, `SELECT `+peerCols+` FROM peers WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Peer{}, ErrNotFound
	}
	return p, err
}

// PeersOf are a person's peers, oldest first.
func (s *Store) PeersOf(ctx context.Context, owner uuid.UUID) ([]Peer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+peerCols+` FROM peers WHERE owner_id = ? ORDER BY created_at, id`, owner.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Peer
	for rows.Next() {
		p, err := s.scanPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AllPeers lists every active peer, for the app's own upkeep.
func (s *Store) AllPeers(ctx context.Context) ([]Peer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+peerCols+` FROM peers WHERE state = ? ORDER BY created_at, id`, PeerActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Peer
	for rows.Next() {
		p, err := s.scanPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePeer forgets a peer.
func (s *Store) DeletePeer(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM peers WHERE id = ?`, id)
	return err
}

// SetPeerTheirID records the id the other app gave this one.
func (s *Store) SetPeerTheirID(ctx context.Context, id, theirID, name string, version int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET their_id = ?, name = ?, version = ? WHERE id = ?`, theirID, name, version, id)
	return err
}

// SetPeerPaused records that this side paused or resumed.
func (s *Store) SetPeerPaused(ctx context.Context, id string, paused bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET i_paused = ? WHERE id = ?`, b2i(paused), id)
	return err
}

// SetPeerTheyPaused records that the other side paused or resumed.
func (s *Store) SetPeerTheyPaused(ctx context.Context, id string, paused bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET they_paused = ? WHERE id = ?`, b2i(paused), id)
	return err
}

// SetPeerState sets a peer's state (the other side left).
func (s *Store) SetPeerState(ctx context.Context, id, state string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET state = ? WHERE id = ?`, state, id)
	return err
}

// MarkPeerSeen records that the other app answered (problem empty) or why it did not.
func (s *Store) MarkPeerSeen(ctx context.Context, id string, at time.Time, problem string) error {
	if problem == "" {
		_, err := s.db.ExecContext(ctx, `UPDATE peers SET seen_at = ?, problem = '' WHERE id = ?`, tstr(at), id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE peers SET problem = ? WHERE id = ?`, problem, id)
	return err
}

// PeerInvite is an invitation this app made: only the hash of its token is kept.
type PeerInvite struct {
	OwnerID   uuid.UUID
	OwnerName string
	ExpiresAt time.Time
	UsedBy    string
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte("peer-invite|" + token))
	return hex.EncodeToString(sum[:])
}

// AddPeerInvite keeps an invitation.
func (s *Store) AddPeerInvite(ctx context.Context, token string, owner uuid.UUID, ownerName string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO peer_invites (token_hash, owner_id, owner_name, expires_at) VALUES (?,?,?,?)`, tokenHash(token), owner.String(), ownerName, tstr(expires))
	return err
}

// PeerInviteByToken finds an invitation by its token.
func (s *Store) PeerInviteByToken(ctx context.Context, token string) (PeerInvite, error) {
	var in PeerInvite
	var owner, exp string
	err := s.db.QueryRowContext(ctx, `SELECT owner_id, owner_name, expires_at, used_by FROM peer_invites WHERE token_hash = ?`, tokenHash(token)).Scan(&owner, &in.OwnerName, &exp, &in.UsedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return in, ErrNotFound
	}
	in.OwnerID, _ = uuid.Parse(owner)
	in.ExpiresAt, _ = time.Parse(time.RFC3339Nano, exp)
	return in, err
}

// UsePeerInvite marks an invitation as used by a peer; it reports false when it already was.
func (s *Store) UsePeerInvite(ctx context.Context, token, peer string) (bool, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE peer_invites SET used_by = ? WHERE token_hash = ? AND used_by = ''`, peer, tokenHash(token))
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

// PrunePeerInvites forgets invitations that expired more than a day ago.
func (s *Store) PrunePeerInvites(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM peer_invites WHERE expires_at <> '' AND expires_at < ?`, now.Add(-24*time.Hour).UTC().Format(time.RFC3339Nano))
	return err
}
