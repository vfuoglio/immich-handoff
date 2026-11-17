package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// StackMade is a stack the app made for a rule, kept for a while so that the person can take it apart again.
type StackMade struct {
	StackID string
	RuleID  string
	UserID  uuid.UUID
	Assets  []string // the photos in it, the cover first
	At      time.Time
}

// AddStackMade remembers a stack the app made.
func (s *Store) AddStackMade(ctx context.Context, m StackMade) error {
	assets, _ := json.Marshal(m.Assets)
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO stacks_made (stack_id, rule_id, user_id, assets, at) VALUES (?,?,?,?,?)`,
		m.StackID, m.RuleID, m.UserID.String(), string(assets), m.At.UTC().Format(time.RFC3339Nano))
	return err
}

// StacksMadeSince lists the stacks the app made for a rule since a moment, newest first. It also forgets the older ones of every rule.
func (s *Store) StacksMadeSince(ctx context.Context, rule string, since time.Time) ([]StackMade, error) {
	cut := since.UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM stacks_made WHERE at < ?`, time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT stack_id, rule_id, user_id, assets, at FROM stacks_made WHERE rule_id = ? AND at >= ? ORDER BY at DESC`, rule, cut)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StackMade
	for rows.Next() {
		var m StackMade
		var user, assets, at string
		if err := rows.Scan(&m.StackID, &m.RuleID, &user, &assets, &at); err != nil {
			return nil, err
		}
		m.UserID, _ = uuid.Parse(user)
		_ = json.Unmarshal([]byte(assets), &m.Assets)
		m.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteStackMade forgets a stack (it was taken apart, or is gone).
func (s *Store) DeleteStackMade(ctx context.Context, stack string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM stacks_made WHERE stack_id = ?`, stack)
	return err
}
