package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Bridge states.
const (
	BridgeOK          = "ok"
	BridgeUnreachable = "unreachable" // a timeout or a server error: tried again later
	BridgeRevoked     = "revoked"     // the other server rejected the key: not tried again until it is replaced
)

// Bridge is the receiver of a service that lives on another Immich server. A service has at most one.
// The key is encrypted at rest and never shown.
type Bridge struct {
	Service     string
	Address     string // scheme and host, as Policy.Address returned it
	Key         string
	DisplayName string // what the receiver chose to be called
	State       string
	Note        string // why it is not ok
	Version     string // the other server's Immich version, as of the last check
	CheckedAt   time.Time
	Failures    int       // checks or hand-overs that failed in a row (drives the backoff)
	RetryAt     time.Time // not before this time while unreachable
	CreatedAt   time.Time
}

func timeOrZero(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// SetBridge makes b the service's remote receiver, replacing the one it had (a new key or a new address).
// The state starts as ok.
func (s *Store) SetBridge(ctx context.Context, b Bridge) error {
	enc, err := s.vault.Seal([]byte(b.Key), "bridge:"+b.Service)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO bridges (service, address, key_enc, display_name, state, note, version, checked_at, failures, retry_at, created_at)
		 VALUES (?,?,?,?, 'ok', '', ?, ?, 0, '', ?)
		 ON CONFLICT(service) DO UPDATE SET address = excluded.address, key_enc = excluded.key_enc,
		   display_name = excluded.display_name, state = 'ok', note = '', version = excluded.version,
		   checked_at = excluded.checked_at, failures = 0, retry_at = ''`,
		b.Service, b.Address, enc, b.DisplayName, b.Version, timeText(b.CheckedAt), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) scanBridge(row interface{ Scan(...any) error }) (Bridge, error) {
	var b Bridge
	var enc []byte
	var checked, retry, created string
	if err := row.Scan(&b.Service, &b.Address, &enc, &b.DisplayName, &b.State, &b.Note, &b.Version, &checked, &b.Failures, &retry, &created); err != nil {
		return Bridge{}, err
	}
	plain, err := s.vault.Open(enc, "bridge:"+b.Service)
	if err != nil {
		return Bridge{}, err
	}
	b.Key, b.CheckedAt, b.RetryAt, b.CreatedAt = string(plain), timeOrZero(checked), timeOrZero(retry), timeOrZero(created)
	return b, nil
}

const bridgeColumns = `service, address, key_enc, display_name, state, note, version, checked_at, failures, retry_at, created_at`

// Bridge returns the service's remote receiver, or ErrNotFound.
func (s *Store) Bridge(ctx context.Context, service string) (Bridge, error) {
	b, err := s.scanBridge(s.db.QueryRowContext(ctx, `SELECT `+bridgeColumns+` FROM bridges WHERE service = ?`, service))
	if errors.Is(err, sql.ErrNoRows) {
		return Bridge{}, ErrNotFound
	}
	return b, err
}

// Bridges returns every remote receiver.
func (s *Store) Bridges(ctx context.Context) ([]Bridge, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+bridgeColumns+` FROM bridges ORDER BY created_at, service`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bridge
	for rows.Next() {
		b, err := s.scanBridge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SetBridgeState records what the last check or hand-over found. It reports whether anything changed, so that
// a person is told once about a state and not on every pass. A change to ok clears the note and the failures.
func (s *Store) SetBridgeState(ctx context.Context, service, state, note, version string, failures int, retryAt time.Time) (changed bool, err error) {
	var old, oldNote string
	if err := s.db.QueryRowContext(ctx, `SELECT state, note FROM bridges WHERE service = ?`, service).Scan(&old, &oldNote); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, err
	}
	if state == BridgeOK {
		note, failures, retryAt = "", 0, time.Time{}
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE bridges SET state = ?, note = ?, version = CASE WHEN ? = '' THEN version ELSE ? END, checked_at = ?, failures = ?, retry_at = ? WHERE service = ?`,
		state, note, version, version, timeText(time.Now()), failures, timeText(retryAt), service)
	return old != state, err
}

// RemoveBridge deletes the service's remote receiver. It is not an error if there is none.
func (s *Store) RemoveBridge(ctx context.Context, service string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM bridges WHERE service = ?`, service)
	return err
}

// BridgeInvite is the link an owner sends to someone on another server.
type BridgeInvite struct {
	Token     string
	Service   string
	CreatedAt time.Time
}

// SetBridgeInvite makes token the service's one open invitation; an older one stops working.
func (s *Store) SetBridgeInvite(ctx context.Context, service, token string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM bridge_invites WHERE service = ?`, service); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bridge_invites (token, service, created_at) VALUES (?,?,?)`,
		token, service, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

// BridgeInviteByToken finds an invitation, or ErrNotFound.
func (s *Store) BridgeInviteByToken(ctx context.Context, token string) (BridgeInvite, error) {
	var i BridgeInvite
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT token, service, created_at FROM bridge_invites WHERE token = ?`, token).Scan(&i.Token, &i.Service, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return BridgeInvite{}, ErrNotFound
	}
	i.CreatedAt = timeOrZero(created)
	return i, err
}

// BridgeInviteOf is the service's open invitation, or ErrNotFound.
func (s *Store) BridgeInviteOf(ctx context.Context, service string) (BridgeInvite, error) {
	var token string
	err := s.db.QueryRowContext(ctx, `SELECT token FROM bridge_invites WHERE service = ?`, service).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return BridgeInvite{}, ErrNotFound
	}
	if err != nil {
		return BridgeInvite{}, err
	}
	return s.BridgeInviteByToken(ctx, token)
}

// DeleteBridgeInvite withdraws an invitation (used up, or replaced). It is not an error if there is none.
func (s *Store) DeleteBridgeInvite(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM bridge_invites WHERE token = ?`, token)
	return err
}
