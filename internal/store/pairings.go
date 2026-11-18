package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// A pairing is two people sharing the photos of chosen people with each other, both ways, through a private pool of their own
// (docs/design/pairing-design.md). The pool's service and each side's rules carry the pairing's id.
const (
	PairingInvited = "invited" // the starter made it; the other person has not said yes yet
	PairingActive  = "active"
	PairingPaused  = "paused"  // nothing is handed over until someone resumes it
	PairingStopped = "stopped" // ended by one side (or declined, or lapsed); the starter's own pieces wait for their next visit to be cleaned
)

// PairingInviteDays is how long an invitation waits for a yes.
const PairingInviteDays = 7

// Pairing is one pairing. A is the person who started it and owns the pool; B is the person who was invited.
type Pairing struct {
	ID        string
	Service   string // the name of the pool
	AID       uuid.UUID
	AName     string
	BID       uuid.UUID
	BName     string
	State     string
	Note      string // why it stopped, in words for the people
	CreatedAt time.Time
	ExpiresAt time.Time // while invited: when the invitation lapses
}

const pairingCols = `id, service, a_id, a_name, b_id, b_name, state, note, created_at, expires_at`

func scanPairing(sc interface{ Scan(...any) error }) (Pairing, error) {
	var p Pairing
	var a, b, created, expires string
	if err := sc.Scan(&p.ID, &p.Service, &a, &p.AName, &b, &p.BName, &p.State, &p.Note, &created, &expires); err != nil {
		return p, err
	}
	p.AID, _ = uuid.Parse(a)
	p.BID, _ = uuid.Parse(b)
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	p.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	return p, nil
}

// Takes reports whether the person is one of the two.
func (p Pairing) Takes(user uuid.UUID) bool { return p.AID == user || p.BID == user }

// AddPairing stores a pairing in the invited state. The pool's name is unique: a pool belongs to one pairing.
func (s *Store) AddPairing(ctx context.Context, p Pairing) (Pairing, error) {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if p.State == "" {
		p.State = PairingInvited
	}
	p.CreatedAt = time.Now().UTC()
	if p.ExpiresAt.IsZero() {
		p.ExpiresAt = p.CreatedAt.AddDate(0, 0, PairingInviteDays)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO pairings (`+pairingCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Service, p.AID.String(), p.AName, p.BID.String(), p.BName, p.State, p.Note, p.CreatedAt.Format(time.RFC3339Nano), tstr(p.ExpiresAt))
	if err != nil && isUnique(err) {
		return p, ErrExists
	}
	return p, err
}

// PairingByID finds a pairing.
func (s *Store) PairingByID(ctx context.Context, id string) (Pairing, error) {
	p, err := scanPairing(s.db.QueryRowContext(ctx, `SELECT `+pairingCols+` FROM pairings WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Pairing{}, ErrNotFound
	}
	return p, err
}

// PairingOfService finds the pairing a pool belongs to (ErrNotFound for an ordinary service).
func (s *Store) PairingOfService(ctx context.Context, service string) (Pairing, error) {
	p, err := scanPairing(s.db.QueryRowContext(ctx, `SELECT `+pairingCols+` FROM pairings WHERE service = ?`, service))
	if errors.Is(err, sql.ErrNoRows) {
		return Pairing{}, ErrNotFound
	}
	return p, err
}

// Pairings lists the pairings, oldest first.
func (s *Store) Pairings(ctx context.Context) ([]Pairing, error) {
	return s.pairingsQuery(ctx, `SELECT `+pairingCols+` FROM pairings ORDER BY created_at, id`)
}

// PairingsOf lists the pairings a person is one of the two in.
func (s *Store) PairingsOf(ctx context.Context, user uuid.UUID) ([]Pairing, error) {
	return s.pairingsQuery(ctx, `SELECT `+pairingCols+` FROM pairings WHERE a_id = ? OR b_id = ? ORDER BY created_at, id`, user.String(), user.String())
}

func (s *Store) pairingsQuery(ctx context.Context, q string, args ...any) ([]Pairing, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Pairing
	for rows.Next() {
		p, err := scanPairing(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetPairingState changes a pairing's state and the note that says why.
func (s *Store) SetPairingState(ctx context.Context, id, state, note string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE pairings SET state = ?, note = ? WHERE id = ?`, state, note, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetServicePairing marks a pool as belonging to a pairing (an empty id frees it).
func (s *Store) SetServicePairing(ctx context.Context, service, pairing string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE services SET pairing = ? WHERE name = ?`, pairing, service)
	return err
}

// SetRulePairing marks a rule as belonging to a pairing (an empty id frees it).
func (s *Store) SetRulePairing(ctx context.Context, rule, pairing string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE rules SET pairing = ? WHERE id = ?`, pairing, rule)
	return err
}

// RulesOfPairing lists the rules that belong to a pairing, both sides'.
func (s *Store) RulesOfPairing(ctx context.Context, pairing string) ([]Rule, error) {
	return s.rulesQuery(ctx, `SELECT `+ruleCols+` FROM rules WHERE pairing = ? ORDER BY created_at, id`, pairing)
}

// SetPairingRulesPaused pauses or resumes every rule of a pairing.
func (s *Store) SetPairingRulesPaused(ctx context.Context, pairing string, paused bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE rules SET paused = ? WHERE pairing = ?`, b2i(paused), pairing)
	return err
}

// StartPairingRules unpauses every rule of a pairing and makes them look only at photos that reach Immich after at, which is
// the moment the other person said yes: nothing that arrived while the invitation waited is ever handed over.
func (s *Store) StartPairingRules(ctx context.Context, pairing string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE rules SET paused = 0, since = ?, auto_since = '', mode = ? WHERE pairing = ?`, at.UTC().Format(time.RFC3339Nano), RuleSuggest, pairing)
	return err
}

// RemovePairing deletes a pairing and every rule that belongs to it (and what those rules remembered). It does not touch the
// pool's service or any photo.
func (s *Store) RemovePairing(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM rule_photos WHERE rule_id IN (SELECT id FROM rules WHERE pairing = ?)`,
		`DELETE FROM rules WHERE pairing = ?`,
		`DELETE FROM pairings WHERE id = ?`,
		`UPDATE services SET pairing = '' WHERE pairing = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
