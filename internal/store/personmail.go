package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Modes of a person's mail.
const (
	MailAll   = "all"   // everything the page writes to their recent events
	MailNeeds = "needs" // only what needs them: a broken connection, a removal, a request, a limit reached
)

// PersonMail is one person's wish to be told by email.
type PersonMail struct {
	UserID    uuid.UUID
	Name      string
	Email     string
	Lang      string // the language of their page when they last saved
	Mode      string
	CreatedAt time.Time
	LastSent  time.Time
}

const personMailCols = `user_id, name, email, lang, mode, created_at, last_sent`

func scanPersonMail(sc interface{ Scan(...any) error }) (PersonMail, error) {
	var p PersonMail
	var id, created, sent string
	if err := sc.Scan(&id, &p.Name, &p.Email, &p.Lang, &p.Mode, &created, &sent); err != nil {
		return p, err
	}
	p.UserID, _ = uuid.Parse(id)
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	p.LastSent, _ = time.Parse(time.RFC3339Nano, sent)
	return p, nil
}

// PersonMail returns the person's mail settings, if they have turned it on.
func (s *Store) PersonMail(ctx context.Context, user uuid.UUID) (PersonMail, bool, error) {
	p, err := scanPersonMail(s.db.QueryRowContext(ctx, `SELECT `+personMailCols+` FROM person_mail WHERE user_id = ?`, user.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return PersonMail{}, false, nil
	}
	return p, err == nil, err
}

// SetPersonMail turns mail on for a person, or changes their name, address, language or mode (the first date and the
// last-sent date stay).
func (s *Store) SetPersonMail(ctx context.Context, p PersonMail) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO person_mail (`+personMailCols+`) VALUES (?,?,?,?,?,?,'')
	  ON CONFLICT(user_id) DO UPDATE SET name = excluded.name, email = excluded.email, lang = excluded.lang, mode = excluded.mode`,
		p.UserID.String(), p.Name, p.Email, p.Lang, p.Mode, now)
	return err
}

// DeletePersonMail turns mail off and forgets the address.
func (s *Store) DeletePersonMail(ctx context.Context, user uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM person_mail WHERE user_id = ?`, user.String())
	return err
}

// PersonMailList is everyone who has turned mail on.
func (s *Store) PersonMailList(ctx context.Context) ([]PersonMail, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+personMailCols+` FROM person_mail`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PersonMail
	for rows.Next() {
		p, err := scanPersonMail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MarkPersonMailSent remembers when the last mail went.
func (s *Store) MarkPersonMailSent(ctx context.Context, user uuid.UUID, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE person_mail SET last_sent = ? WHERE user_id = ?`, at.UTC().Format(time.RFC3339Nano), user.String())
	return err
}
