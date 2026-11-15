package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Rule modes and what a rule has done with a photo.
const (
	RuleSuggest = "suggest"
	RuleAuto    = "auto"

	RuleSuggested = "suggested" // offered, waiting for the person's answer
	RuleSent      = "sent"      // the person said yes (or auto sent it)
	RuleDeclined  = "declined"  // "not these": never offered again by this rule
)

// RuleRef is a person or an album a rule names, with the name it had when the rule was made.
type RuleRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// RuleCond are the conditions of a rule: all of them must hold.
type RuleCond struct {
	People    []RuleRef `json:"people,omitempty"`
	NotPeople []RuleRef `json:"notPeople,omitempty"`
	After     string    `json:"after,omitempty"`  // taken on or after, YYYY-MM-DD
	Before    string    `json:"before,omitempty"` // taken on or before, YYYY-MM-DD
	Country   string    `json:"country,omitempty"`
	City      string    `json:"city,omitempty"`
	Album     *RuleRef  `json:"album,omitempty"`
	NotAlbums []RuleRef `json:"notAlbums,omitempty"`
	Phrase    string    `json:"phrase,omitempty"`
}

// Rule is one rule of a sender for a service.
type Rule struct {
	ID        string
	OwnerID   uuid.UUID
	Service   string
	Cond      RuleCond
	Mode      string
	Always    int // how many times the person answered "Always"
	Paused    bool
	Since     time.Time // the rule only looks at photos that reached Immich after this
	AutoSince time.Time // when auto was switched on (zero: it is not on)
	LastLook  time.Time
	Problem   string
	CreatedAt time.Time
}

const ruleCols = `id, owner_id, service, cond, mode, always, paused, since, auto_since, last_look, problem, created_at`

func scanRule(sc interface{ Scan(...any) error }) (Rule, error) {
	var r Rule
	var owner, cond, since, auto, last, created string
	if err := sc.Scan(&r.ID, &owner, &r.Service, &cond, &r.Mode, &r.Always, &r.Paused, &since, &auto, &last, &r.Problem, &created); err != nil {
		return r, err
	}
	r.OwnerID, _ = uuid.Parse(owner)
	_ = json.Unmarshal([]byte(cond), &r.Cond)
	r.Since, _ = time.Parse(time.RFC3339Nano, since)
	r.AutoSince, _ = time.Parse(time.RFC3339Nano, auto)
	r.LastLook, _ = time.Parse(time.RFC3339Nano, last)
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return r, nil
}

// AddRule stores a rule. It looks only at photos that reach Immich from now on.
func (s *Store) AddRule(ctx context.Context, r Rule) (Rule, error) {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if r.Mode == "" {
		r.Mode = RuleSuggest
	}
	r.CreatedAt = time.Now().UTC()
	if r.Since.IsZero() {
		r.Since = r.CreatedAt
	}
	cond, _ := json.Marshal(r.Cond)
	_, err := s.db.ExecContext(ctx, `INSERT INTO rules (`+ruleCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.OwnerID.String(), r.Service, string(cond), r.Mode, r.Always, b2i(r.Paused), r.Since.Format(time.RFC3339Nano), tstr(r.AutoSince), tstr(r.LastLook), r.Problem, r.CreatedAt.Format(time.RFC3339Nano))
	return r, err
}

// RuleByID finds a rule.
func (s *Store) RuleByID(ctx context.Context, id string) (Rule, error) {
	r, err := scanRule(s.db.QueryRowContext(ctx, `SELECT `+ruleCols+` FROM rules WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Rule{}, ErrNotFound
	}
	return r, err
}

func (s *Store) rulesQuery(ctx context.Context, q string, args ...any) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RulesOf are a person's rules, oldest first.
func (s *Store) RulesOf(ctx context.Context, owner uuid.UUID) ([]Rule, error) {
	return s.rulesQuery(ctx, `SELECT `+ruleCols+` FROM rules WHERE owner_id = ? ORDER BY created_at, id`, owner.String())
}

// AllRules lists every rule that is not paused, for the timer.
func (s *Store) AllRules(ctx context.Context) ([]Rule, error) {
	return s.rulesQuery(ctx, `SELECT `+ruleCols+` FROM rules WHERE paused = 0 ORDER BY created_at, id`)
}

// SetRulePaused pauses or resumes a rule.
func (s *Store) SetRulePaused(ctx context.Context, id string, paused bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE rules SET paused = ? WHERE id = ?`, b2i(paused), id)
	return err
}

// DeleteRule forgets a rule and what it did with each photo.
func (s *Store) DeleteRule(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM rule_photos WHERE rule_id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM rules WHERE id = ?`, id)
	return err
}

// DeleteRulesOf forgets a person's rules for a service (they left it).
func (s *Store) DeleteRulesOf(ctx context.Context, owner uuid.UUID, service string) error {
	rs, err := s.rulesQuery(ctx, `SELECT `+ruleCols+` FROM rules WHERE owner_id = ? AND service = ?`, owner.String(), service)
	if err != nil {
		return err
	}
	for _, r := range rs {
		if err := s.DeleteRule(ctx, r.ID); err != nil {
			return err
		}
	}
	return nil
}

// RulePhoto is what a rule has done with one photo.
type RulePhoto struct {
	AssetID string
	State   string
	Why     string
	At      time.Time
}

// RulePhotoStates tells, for every photo a rule has handled, what it did with it.
func (s *Store) RulePhotoStates(ctx context.Context, rule string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT asset_id, state FROM rule_photos WHERE rule_id = ?`, rule)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, st string
		if err := rows.Scan(&id, &st); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// RulePhotos lists the photos of a rule in one state, oldest first.
func (s *Store) RulePhotos(ctx context.Context, rule, state string) ([]RulePhoto, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT asset_id, state, why, at FROM rule_photos WHERE rule_id = ? AND state = ? ORDER BY at, asset_id`, rule, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RulePhoto
	for rows.Next() {
		var p RulePhoto
		var at string
		if err := rows.Scan(&p.AssetID, &p.State, &p.Why, &at); err != nil {
			return nil, err
		}
		p.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, p)
	}
	return out, rows.Err()
}

// PutRulePhoto records what a rule did with a photo, keeping the first reason it gave.
func (s *Store) PutRulePhoto(ctx context.Context, rule, asset, state, why string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO rule_photos (rule_id, asset_id, state, why, at) VALUES (?,?,?,?,?)
		ON CONFLICT(rule_id, asset_id) DO UPDATE SET state = excluded.state`, rule, asset, state, why, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// SetRulePhotos moves some photos of a rule from one state to another.
func (s *Store) SetRulePhotos(ctx context.Context, rule string, assets []string, from, to string) error {
	for _, a := range assets {
		if _, err := s.db.ExecContext(ctx, `UPDATE rule_photos SET state = ?, at = ? WHERE rule_id = ? AND asset_id = ? AND state = ?`, to, time.Now().UTC().Format(time.RFC3339Nano), rule, a, from); err != nil {
			return err
		}
	}
	return nil
}

// MarkRuleLook records that a rule was looked at, and what went wrong (empty: nothing).
func (s *Store) MarkRuleLook(ctx context.Context, id string, at time.Time, problem string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE rules SET last_look = ?, problem = ? WHERE id = ?`, at.UTC().Format(time.RFC3339Nano), problem, id)
	return err
}
