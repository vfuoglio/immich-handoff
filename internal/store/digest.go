package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Digest is a service's weekly digest: whether it is on, which permissions it added to the receiver key, and the last
// one made.
type Digest struct {
	Service  string
	Enabled  bool
	Added    string // the permissions the digest added to the receiver key, comma separated; only these come off again
	Week     string // the Monday (2006-01-02) of the week the digest last made, or was turned on in, so a week is done once
	MadeAt   time.Time
	Album    string // the album's name
	LinkID   string // the shared link's id in Immich, to end it
	LinkKey  string // and its key, which makes the address
	LinkEnds time.Time
	Photos   int
	People   int
	LeftOut  int
	LeftJSON string // what was left out and why, as JSON, for the page
	Note     string // why the last run did not make one ("nothing arrived", or an error)
}

const digestCols = `service, enabled, added, week, made_at, album, link_id, link_key, link_ends, photos, people, left_out, left_json, note`

func scanDigest(sc interface{ Scan(...any) error }) (Digest, error) {
	var d Digest
	var made, ends string
	var on int
	if err := sc.Scan(&d.Service, &on, &d.Added, &d.Week, &made, &d.Album, &d.LinkID, &d.LinkKey, &ends, &d.Photos, &d.People, &d.LeftOut, &d.LeftJSON, &d.Note); err != nil {
		return d, err
	}
	d.Enabled = on != 0
	d.MadeAt, _ = time.Parse(time.RFC3339Nano, made)
	d.LinkEnds, _ = time.Parse(time.RFC3339Nano, ends)
	return d, nil
}

func tstr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// DigestFor returns a service's digest, if it ever had one.
func (s *Store) DigestFor(ctx context.Context, service string) (Digest, bool, error) {
	d, err := scanDigest(s.db.QueryRowContext(ctx, `SELECT `+digestCols+` FROM digests WHERE service = ?`, service))
	if errors.Is(err, sql.ErrNoRows) {
		return Digest{}, false, nil
	}
	return d, err == nil, err
}

// SetDigest stores a service's digest whole.
func (s *Store) SetDigest(ctx context.Context, d Digest) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO digests (`+digestCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	  ON CONFLICT(service) DO UPDATE SET enabled=excluded.enabled, added=excluded.added, week=excluded.week, made_at=excluded.made_at, album=excluded.album,
	    link_id=excluded.link_id, link_key=excluded.link_key, link_ends=excluded.link_ends, photos=excluded.photos, people=excluded.people,
	    left_out=excluded.left_out, left_json=excluded.left_json, note=excluded.note`,
		d.Service, b2i(d.Enabled), d.Added, d.Week, tstr(d.MadeAt), d.Album, d.LinkID, d.LinkKey, tstr(d.LinkEnds), d.Photos, d.People, d.LeftOut, d.LeftJSON, d.Note)
	return err
}

// DigestsEnabled lists the services whose digest is on.
func (s *Store) DigestsEnabled(ctx context.Context) ([]Digest, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+digestCols+` FROM digests WHERE enabled = 1 ORDER BY service`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Digest
	for rows.Next() {
		d, err := scanDigest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteDigest forgets a service's digest (the service was removed).
func (s *Store) DeleteDigest(ctx context.Context, service string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM digests WHERE service = ?`, service)
	return err
}

// ArrivedItem is a copy that arrived in a service, from the hand-over ledger.
type ArrivedItem struct {
	LinkID        string
	ReceiverAsset string
	At            time.Time
}

// HandoverItemsSince lists the copies that arrived through these links since the given time: not motion clips, not
// copies on another server, not taken back.
func (s *Store) HandoverItemsSince(ctx context.Context, linkIDs []string, since time.Time) ([]ArrivedItem, error) {
	var out []ArrivedItem
	for _, id := range linkIDs {
		rows, err := s.db.QueryContext(ctx, `SELECT receiver_asset, at FROM handover_items WHERE link_id = ? AND at >= ? AND clip = 0 AND remote = 0 AND taken_back = 0 ORDER BY at, id`,
			id, since.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			it := ArrivedItem{LinkID: id}
			var at string
			if err := rows.Scan(&it.ReceiverAsset, &at); err != nil {
				rows.Close()
				return nil, err
			}
			it.At, _ = time.Parse(time.RFC3339Nano, at)
			out = append(out, it)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}
