package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// MemoryPref is one person's "On this day" messages: when they want one, how, and the key they made for it in their
// own Immich (it can read their memories and look at pictures, nothing else). The secret is stored sealed.
type MemoryPref struct {
	UserID   uuid.UUID
	Name     string
	Lang     string // the language of their page when they last saved
	At       string // the time of day, "08:00"
	TZ       string // an IANA time zone name; empty is the server's
	Push     bool
	Email    bool
	Pictures bool // thumbnails in the email
	KeyID    uuid.UUID
	Key      string
	LastDay  string // the day (in their zone) a message was last sent for, "2026-11-16"
	Broken   bool   // the key stopped working and the person was told
	Created  time.Time
}

const memoryCols = `user_id, name, lang, at, tz, by_push, by_email, pictures, key_id, key_enc, last_day, broken, created_at`

func (s *Store) scanMemoryPref(sc interface{ Scan(...any) error }) (MemoryPref, error) {
	var p MemoryPref
	var id, kid, created string
	var enc []byte
	var push, email, pics, broken int
	if err := sc.Scan(&id, &p.Name, &p.Lang, &p.At, &p.TZ, &push, &email, &pics, &kid, &enc, &p.LastDay, &broken, &created); err != nil {
		return p, err
	}
	p.UserID, _ = uuid.Parse(id)
	p.KeyID, _ = uuid.Parse(kid)
	p.Push, p.Email, p.Pictures, p.Broken = push == 1, email == 1, pics == 1, broken == 1
	p.Created, _ = time.Parse(time.RFC3339Nano, created)
	plain, err := s.vault.Open(enc, "memories:"+id)
	if err != nil {
		return p, err
	}
	p.Key = string(plain)
	return p, nil
}

// MemoryPref returns the person's settings, if they turned the messages on.
func (s *Store) MemoryPref(ctx context.Context, user uuid.UUID) (MemoryPref, bool, error) {
	p, err := s.scanMemoryPref(s.db.QueryRowContext(ctx, `SELECT `+memoryCols+` FROM memory_prefs WHERE user_id = ?`, user.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return MemoryPref{}, false, nil
	}
	return p, err == nil, err
}

// MemoryPrefs lists everyone who has the messages on.
func (s *Store) MemoryPrefs(ctx context.Context) ([]MemoryPref, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+memoryCols+` FROM memory_prefs ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoryPref
	for rows.Next() {
		p, err := s.scanMemoryPref(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetMemoryPref saves the person's settings and key, replacing earlier ones. What a day sent and whether the key broke
// are kept as they were unless the key is a new one.
func (s *Store) SetMemoryPref(ctx context.Context, p MemoryPref) error {
	enc, err := s.vault.Seal([]byte(p.Key), "memories:"+p.UserID.String())
	if err != nil {
		return err
	}
	created := p.Created
	if created.IsZero() {
		created = time.Now()
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO memory_prefs (`+memoryCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
	  ON CONFLICT(user_id) DO UPDATE SET name = excluded.name, lang = excluded.lang, at = excluded.at, tz = excluded.tz,
	    by_push = excluded.by_push, by_email = excluded.by_email, pictures = excluded.pictures,
	    key_id = excluded.key_id, key_enc = excluded.key_enc,
	    broken = CASE WHEN memory_prefs.key_id = excluded.key_id THEN memory_prefs.broken ELSE 0 END`,
		p.UserID.String(), p.Name, p.Lang, p.At, p.TZ, b2i(p.Push), b2i(p.Email), b2i(p.Pictures), p.KeyID.String(), enc,
		p.LastDay, b2i(p.Broken), created.UTC().Format(time.RFC3339Nano))
	return err
}

// MarkMemorySent records that the message for a day was sent (or that there was nothing to send), so the day is not done twice.
func (s *Store) MarkMemorySent(ctx context.Context, user uuid.UUID, day string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE memory_prefs SET last_day = ? WHERE user_id = ?`, day, user.String())
	return err
}

// MarkMemoryBroken records that the person's key stopped working (and that they were told once).
func (s *Store) MarkMemoryBroken(ctx context.Context, user uuid.UUID, broken bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE memory_prefs SET broken = ? WHERE user_id = ?`, b2i(broken), user.String())
	return err
}

// DeleteMemoryPref forgets the person's settings and key (the app has deleted the key in their Immich, or they did).
func (s *Store) DeleteMemoryPref(ctx context.Context, user uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM memory_prefs WHERE user_id = ?`, user.String())
	return err
}
