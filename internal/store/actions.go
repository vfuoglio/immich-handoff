package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ActionLife is how long an action link works.
const ActionLife = 24 * time.Hour

// actionTime is a fixed-width time, so the database can compare times as text.
const actionTime = "2006-01-02T15:04:05.000000000Z"

// ActionLink is a one-tap action offered to one person: the whole credential is its random token.
type ActionLink struct {
	Token     string
	UserID    uuid.UUID
	UserName  string
	Kind      string // approve, decline, resume-service or resume-sender
	Service   string
	Target    string // what the action is about: a request's id
	Group     string // links made for one decision; using one ends the others
	ExpiresAt time.Time
}

// ActionMake stores a link for a person and returns its token (128 random bits). Links a week past their end are
// forgotten as a side effect.
func (s *Store) ActionMake(ctx context.Context, a ActionLink) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now().UTC()
	_, _ = s.db.ExecContext(ctx, `DELETE FROM action_links WHERE expires_at < ?`, now.Add(-7*24*time.Hour).Format(actionTime))
	_, err := s.db.ExecContext(ctx, `INSERT INTO action_links (token, user_id, user_name, kind, service, target, grp, created_at, expires_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		token, a.UserID.String(), a.UserName, a.Kind, a.Service, a.Target, a.Group, now.Format(actionTime), now.Add(ActionLife).Format(actionTime))
	return token, err
}

func scanAction(sc interface{ Scan(...any) error }) (ActionLink, error) {
	var a ActionLink
	var user, expires string
	if err := sc.Scan(&a.Token, &user, &a.UserName, &a.Kind, &a.Service, &a.Target, &a.Group, &expires); err != nil {
		return a, err
	}
	a.UserID, _ = uuid.Parse(user)
	a.ExpiresAt, _ = time.Parse(actionTime, expires)
	return a, nil
}

const actionCols = `token, user_id, user_name, kind, service, target, grp, expires_at`

// ActionGet returns a link that can still be used: not used, not expired.
func (s *Store) ActionGet(ctx context.Context, token string) (ActionLink, bool, error) {
	a, err := scanAction(s.db.QueryRowContext(ctx, `SELECT `+actionCols+` FROM action_links WHERE token = ? AND used_at = '' AND expires_at > ?`,
		token, time.Now().UTC().Format(actionTime)))
	if errors.Is(err, sql.ErrNoRows) {
		return ActionLink{}, false, nil
	}
	return a, err == nil, err
}

// ActionUse spends a link: it returns it only to the one caller who gets to use it, and ends the others made for the
// same decision. A link already used, expired or unknown gives false.
func (s *Store) ActionUse(ctx context.Context, token string) (ActionLink, bool, error) {
	now := time.Now().UTC().Format(actionTime)
	r, err := s.db.ExecContext(ctx, `UPDATE action_links SET used_at = ? WHERE token = ? AND used_at = '' AND expires_at > ?`, now, token, now)
	if err != nil {
		return ActionLink{}, false, err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return ActionLink{}, false, nil
	}
	a, err := scanAction(s.db.QueryRowContext(ctx, `SELECT `+actionCols+` FROM action_links WHERE token = ?`, token))
	if err != nil {
		return ActionLink{}, false, err
	}
	if a.Group != "" {
		if _, err := s.db.ExecContext(ctx, `UPDATE action_links SET used_at = ? WHERE grp = ? AND used_at = ''`, now, a.Group); err != nil {
			return a, true, err
		}
	}
	return a, true, nil
}
