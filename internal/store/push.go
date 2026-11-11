package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// MaxPushDevices is how many browsers one person can allow.
const MaxPushDevices = 10

// ErrTooManyDevices: the person already has MaxPushDevices.
var ErrTooManyDevices = errors.New("store: too many devices")

// PushSub is one browser a person allowed pushes to.
type PushSub struct {
	ID        string // a hash of Endpoint
	UserID    uuid.UUID
	Endpoint  string
	P256dh    []byte
	Auth      []byte
	Lang      string
	Mode      string // MailAll or MailNeeds
	Label     string // what the person sees in their list of devices
	CreatedAt time.Time
	LastOK    time.Time
	Failures  int
}

// PushID is the id of a subscription: a hash of its address.
func PushID(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:])
}

type pushSecret struct {
	Endpoint string `json:"e"`
	P256dh   []byte `json:"k"`
	Auth     []byte `json:"a"`
}

const pushCols = `id, user_id, sealed, lang, mode, label, created_at, last_ok, failures`

func (s *Store) scanPush(sc interface{ Scan(...any) error }) (PushSub, error) {
	var p PushSub
	var user, created, ok string
	var sealed []byte
	if err := sc.Scan(&p.ID, &user, &sealed, &p.Lang, &p.Mode, &p.Label, &created, &ok, &p.Failures); err != nil {
		return p, err
	}
	p.UserID, _ = uuid.Parse(user)
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	p.LastOK, _ = time.Parse(time.RFC3339Nano, ok)
	plain, err := s.vault.Open(sealed, "push:"+p.ID)
	if err != nil {
		return p, err
	}
	var sec pushSecret
	if err := json.Unmarshal(plain, &sec); err != nil {
		return p, err
	}
	p.Endpoint, p.P256dh, p.Auth = sec.Endpoint, sec.P256dh, sec.Auth
	return p, nil
}

// PushSubscribe stores a device, or updates it when the same browser subscribes again (same address). A person can
// have MaxPushDevices; a ninth browser replaces nothing and is refused.
func (s *Store) PushSubscribe(ctx context.Context, p PushSub) (string, error) {
	id := PushID(p.Endpoint)
	var user string
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM push_subs WHERE id = ?`, id).Scan(&user)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if exists && user != p.UserID.String() {
		// the same browser now belongs to someone else (another person signed in on it): it moves
		if _, err := s.db.ExecContext(ctx, `DELETE FROM push_subs WHERE id = ?`, id); err != nil {
			return "", err
		}
		exists = false
	}
	if !exists {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM push_subs WHERE user_id = ?`, p.UserID.String()).Scan(&n); err != nil {
			return "", err
		}
		if n >= MaxPushDevices {
			return "", ErrTooManyDevices
		}
	}
	plain, err := json.Marshal(pushSecret{p.Endpoint, p.P256dh, p.Auth})
	if err != nil {
		return "", err
	}
	sealed, err := s.vault.Seal(plain, "push:"+id)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO push_subs (`+pushCols+`) VALUES (?,?,?,?,?,?,?,'',0)
	  ON CONFLICT(id) DO UPDATE SET sealed = excluded.sealed, lang = excluded.lang, mode = excluded.mode, label = excluded.label, failures = 0`,
		id, p.UserID.String(), sealed, p.Lang, p.Mode, p.Label, time.Now().UTC().Format(time.RFC3339Nano))
	return id, err
}

// PushSubs are a person's devices, oldest first.
func (s *Store) PushSubs(ctx context.Context, user uuid.UUID) ([]PushSub, error) {
	return s.pushList(ctx, `SELECT `+pushCols+` FROM push_subs WHERE user_id = ? ORDER BY created_at`, user.String())
}

// PushSubsAll are everyone's devices.
func (s *Store) PushSubsAll(ctx context.Context) ([]PushSub, error) {
	return s.pushList(ctx, `SELECT `+pushCols+` FROM push_subs ORDER BY created_at`)
}

func (s *Store) pushList(ctx context.Context, q string, args ...any) ([]PushSub, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSub
	for rows.Next() {
		p, err := s.scanPush(rows)
		if err != nil {
			return nil, fmt.Errorf("store: a push device cannot be read (wrong store key?): %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PushRemove forgets one of the person's devices; it reports whether there was one.
func (s *Store) PushRemove(ctx context.Context, user uuid.UUID, id string) (bool, error) {
	r, err := s.db.ExecContext(ctx, `DELETE FROM push_subs WHERE id = ? AND user_id = ?`, id, user.String())
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

// PushRemoveID forgets a device whose push service said it is gone.
func (s *Store) PushRemoveID(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM push_subs WHERE id = ?`, id)
	return err
}

// PushSetMode changes what one of the person's devices is told; it reports whether there was one.
func (s *Store) PushSetMode(ctx context.Context, user uuid.UUID, id, mode string) (bool, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE push_subs SET mode = ? WHERE id = ? AND user_id = ?`, mode, id, user.String())
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

// PushMarkOK remembers that a push reached the push service.
func (s *Store) PushMarkOK(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE push_subs SET last_ok = ?, failures = 0 WHERE id = ?`, at.UTC().Format(time.RFC3339Nano), id)
	return err
}

// PushMarkFailed counts a push that did not get through and returns how many failed in a row.
func (s *Store) PushMarkFailed(ctx context.Context, id string) (int, error) {
	if _, err := s.db.ExecContext(ctx, `UPDATE push_subs SET failures = failures + 1 WHERE id = ?`, id); err != nil {
		return 0, err
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT failures FROM push_subs WHERE id = ?`, id).Scan(&n)
	return n, err
}

// PushKey returns the app's push signing key (the private half), making and keeping one the first time. It is sealed
// by the store key like every other secret.
func (s *Store) PushKey(ctx context.Context, make func() ([]byte, error)) ([]byte, error) {
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT v FROM meta WHERE k = 'vapid-key'`).Scan(&sealed)
	if err == nil {
		return s.vault.Open(sealed, "vapid-key")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	raw, err := make()
	if err != nil {
		return nil, err
	}
	sealed, err = s.vault.Seal(raw, "vapid-key")
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO meta (k, v) VALUES ('vapid-key', ?)`, sealed); err != nil {
		return nil, err
	}
	return raw, nil
}
