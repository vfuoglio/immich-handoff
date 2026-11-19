package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Destination states.
const (
	DestOK      = "ok"
	DestFailing = "failing" // it did not answer or refused a file: photos for it wait
)

// OwnDestination is a network destination a person entered on their service card. Its credentials are sealed with the store key and never shown.
type OwnDestination struct {
	ID        string
	OwnerID   uuid.UUID
	Name      string
	Kind      string
	Settings  map[string]string // what is not secret: an address, a bucket, a user name
	Secrets   map[string]string // decrypted
	CreatedAt time.Time
}

// AddOwnDestination keeps a destination a person entered.
func (s *Store) AddOwnDestination(ctx context.Context, d OwnDestination) (OwnDestination, error) {
	d.ID = uuid.NewString()
	d.CreatedAt = time.Now().UTC()
	set, _ := json.Marshal(d.Settings)
	sec, _ := json.Marshal(d.Secrets)
	enc, err := s.vault.Seal(sec, "destination:"+d.ID)
	if err != nil {
		return d, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO destinations (id, owner_id, name, kind, settings, secret_enc, created_at) VALUES (?,?,?,?,?,?,?)`,
		d.ID, d.OwnerID.String(), d.Name, d.Kind, string(set), enc, d.CreatedAt.Format(time.RFC3339Nano))
	return d, err
}

func (s *Store) scanOwnDestination(row interface{ Scan(...any) error }) (OwnDestination, error) {
	var d OwnDestination
	var owner, set, created string
	var enc []byte
	if err := row.Scan(&d.ID, &owner, &d.Name, &d.Kind, &set, &enc, &created); err != nil {
		return OwnDestination{}, err
	}
	d.OwnerID, _ = uuid.Parse(owner)
	d.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	json.Unmarshal([]byte(set), &d.Settings)
	plain, err := s.vault.Open(enc, "destination:"+d.ID)
	if err != nil {
		return OwnDestination{}, err
	}
	json.Unmarshal(plain, &d.Secrets)
	return d, nil
}

const ownDestCols = `id, owner_id, name, kind, settings, secret_enc, created_at`

// OwnDestination is one destination a person entered, or ErrNotFound.
func (s *Store) OwnDestination(ctx context.Context, id string) (OwnDestination, error) {
	d, err := s.scanOwnDestination(s.db.QueryRowContext(ctx, `SELECT `+ownDestCols+` FROM destinations WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return OwnDestination{}, ErrNotFound
	}
	return d, err
}

// OwnDestinationsOf are the destinations a person entered.
func (s *Store) OwnDestinationsOf(ctx context.Context, owner uuid.UUID) ([]OwnDestination, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+ownDestCols+` FROM destinations WHERE owner_id = ? ORDER BY created_at, id`, owner.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OwnDestination
	for rows.Next() {
		d, err := s.scanOwnDestination(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RemoveOwnDestination forgets a destination a person entered (and its credentials).
func (s *Store) RemoveOwnDestination(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM destinations WHERE id = ?`, id)
	return err
}

// ServiceDestination is a destination used by a service: the administrator's by name ("admin:<name>") or a person's own ("own:<id>"), with
// where it puts files, its own limit and pause, and how it is doing.
type ServiceDestination struct {
	ID, Service, Ref string
	Template         string // where files go under the destination's root ("" for the default)
	CapPhotos, CapMB int    // its own daily limit (0: none)
	Paused           bool
	State, Note      string
	Since            time.Time // when it began failing
	CreatedAt        time.Time
}

const svcDestCols = `id, service, ref, template, cap_photos, cap_mb, paused, state, note, since, created_at`

func scanServiceDestination(row interface{ Scan(...any) error }) (ServiceDestination, error) {
	var d ServiceDestination
	var since, created string
	if err := row.Scan(&d.ID, &d.Service, &d.Ref, &d.Template, &d.CapPhotos, &d.CapMB, &d.Paused, &d.State, &d.Note, &since, &created); err != nil {
		return ServiceDestination{}, err
	}
	d.Since, _ = time.Parse(time.RFC3339Nano, since)
	d.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return d, nil
}

// AddServiceDestination makes a destination one of a service's receivers; ErrExists when the service already uses it.
func (s *Store) AddServiceDestination(ctx context.Context, d ServiceDestination) (ServiceDestination, error) {
	d.ID = uuid.NewString()
	d.State = DestOK
	d.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO service_destinations (id, service, ref, template, cap_photos, cap_mb, paused, state, note, since, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.Service, d.Ref, d.Template, d.CapPhotos, d.CapMB, b2i(d.Paused), d.State, d.Note, "", d.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if isUnique(err) {
			return d, ErrExists
		}
		return d, err
	}
	return d, nil
}

func (s *Store) serviceDestinationsQuery(ctx context.Context, q string, args ...any) ([]ServiceDestination, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceDestination
	for rows.Next() {
		d, err := scanServiceDestination(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ServiceDestinations are the destinations of one service, in the order they were added.
func (s *Store) ServiceDestinations(ctx context.Context, service string) ([]ServiceDestination, error) {
	return s.serviceDestinationsQuery(ctx, `SELECT `+svcDestCols+` FROM service_destinations WHERE service = ? ORDER BY created_at, id`, service)
}

// AllServiceDestinations are those of every service.
func (s *Store) AllServiceDestinations(ctx context.Context) ([]ServiceDestination, error) {
	return s.serviceDestinationsQuery(ctx, `SELECT `+svcDestCols+` FROM service_destinations ORDER BY created_at, id`)
}

// ServiceDestination is one binding by id, or ErrNotFound.
func (s *Store) ServiceDestination(ctx context.Context, id string) (ServiceDestination, error) {
	d, err := scanServiceDestination(s.db.QueryRowContext(ctx, `SELECT `+svcDestCols+` FROM service_destinations WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ServiceDestination{}, ErrNotFound
	}
	return d, err
}

// SetServiceDestinationPaused pauses or resumes one destination of a service.
func (s *Store) SetServiceDestinationPaused(ctx context.Context, id string, paused bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE service_destinations SET paused = ? WHERE id = ?`, b2i(paused), id)
	return err
}

// SetServiceDestinationCap sets a destination's own daily limit (0 for none).
func (s *Store) SetServiceDestinationCap(ctx context.Context, id string, photos, mb int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE service_destinations SET cap_photos = ?, cap_mb = ? WHERE id = ?`, photos, mb, id)
	return err
}

// SetServiceDestinationTemplate changes where a destination puts files.
func (s *Store) SetServiceDestinationTemplate(ctx context.Context, id, template string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE service_destinations SET template = ? WHERE id = ?`, template, id)
	return err
}

// SetServiceDestinationState records how a destination is doing; since is when it began failing (kept while it keeps failing).
func (s *Store) SetServiceDestinationState(ctx context.Context, id, state, note string, since time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE service_destinations SET state = ?, note = ?, since = ? WHERE id = ?`, state, note, tstr(since), id)
	return err
}

// RemoveServiceDestination stops a service using a destination (what it delivered stays there).
func (s *Store) RemoveServiceDestination(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM destination_log WHERE binding = ?`, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM service_destinations WHERE id = ?`, id)
	return err
}

// LogDestinationUse records that a destination received a photo of that many bytes, for its daily limit. Only two days are kept.
func (s *Store) LogDestinationUse(ctx context.Context, binding string, bytes int64) error {
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO destination_log (binding, at, bytes) VALUES (?,?,?)`, binding, now.Format(time.RFC3339Nano), bytes); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM destination_log WHERE at < ?`, now.Add(-48*time.Hour).Format(time.RFC3339Nano))
	return err
}

// DestinationUse is what a destination received since the given time: photos and bytes.
func (s *Store) DestinationUse(ctx context.Context, binding string, since time.Time) (photos int, bytes int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(bytes), 0) FROM destination_log WHERE binding = ? AND at >= ?`, binding, since.UTC().Format(time.RFC3339Nano)).Scan(&photos, &bytes)
	return
}
