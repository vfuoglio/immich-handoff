package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// A story (#272) is a short animated recap of an album, a person's photos, a range of dates or a drop box, opened from a public
// link with no account. The photos it shows (about 40, chosen once with the owner's session) and its chapters are stored; the public
// page never looks at the library again. Like a frame it has a key of its own, which is deleted when the owner removes the story.

// Story states.
const (
	StoryBuilding = "building"
	StoryReady    = "ready"
	StoryFailed   = "failed"
	StoryEnded    = "ended" // its link stopped (the end date came); the owner removes it, which deletes its key
)

// Where a story's photos come from.
const (
	StoryAlbum  = "album"
	StoryPerson = "person"
	StoryRange  = "range"
	StoryBox    = "box"
)

// StoryPick is one photo of a story, in the order it is shown.
type StoryPick struct {
	Asset   string   `json:"a"`           // the photo's id in Immich (never sent to the public page)
	Chapter int      `json:"c"`           // the index of its chapter
	Date    string   `json:"d,omitempty"` // YYYY-MM-DD, when it was taken
	Place   string   `json:"p,omitempty"` // the city and country, never coordinates
	People  []string `json:"n,omitempty"` // named people in it
}

// StoryChapter is a stretch of the story with a card of its own.
type StoryChapter struct {
	Title string `json:"t"`
	From  string `json:"f,omitempty"`
	To    string `json:"o,omitempty"`
}

// Story is one story of a person's.
type Story struct {
	ID, Token  string
	OwnerID    uuid.UUID
	OwnerName  string
	Title      string
	Kind       string // StoryAlbum, StoryPerson, StoryRange or StoryBox
	SourceID   string // the album, person or drop box (empty for a range of everything)
	SourceName string
	From, To   string // YYYY-MM-DD, the range (empty: not limited)
	ShowDates  bool
	ShowPlaces bool
	ShowPeople bool
	Preview    bool // chats may show its title and first photo
	KeyID      uuid.UUID
	Key        string // decrypted
	EndsAt     time.Time
	State      string
	Problem    string
	Done       int // photos looked at so far, while it is being built
	Total      int
	Picks      []StoryPick
	Chapters   []StoryChapter
	CreatedAt  time.Time
	BuiltAt    time.Time
}

const storyCols = `id, token, owner_id, owner_name, title, kind, source_id, source_name, from_day, to_day, show_dates, show_places, show_people, preview, key_id, key_enc, ends_at, state, problem, done, total, picks, chapters, created_at, built_at`

func (s *Store) scanStory(sc interface{ Scan(...any) error }) (Story, error) {
	var st Story
	var owner, key, ends, created, built, picks, chapters string
	var enc []byte
	if err := sc.Scan(&st.ID, &st.Token, &owner, &st.OwnerName, &st.Title, &st.Kind, &st.SourceID, &st.SourceName, &st.From, &st.To, &st.ShowDates, &st.ShowPlaces, &st.ShowPeople, &st.Preview,
		&key, &enc, &ends, &st.State, &st.Problem, &st.Done, &st.Total, &picks, &chapters, &created, &built); err != nil {
		return st, err
	}
	st.OwnerID, _ = uuid.Parse(owner)
	st.KeyID, _ = uuid.Parse(key)
	st.EndsAt, _ = time.Parse(time.RFC3339Nano, ends)
	st.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	st.BuiltAt, _ = time.Parse(time.RFC3339Nano, built)
	_ = json.Unmarshal([]byte(picks), &st.Picks)
	_ = json.Unmarshal([]byte(chapters), &st.Chapters)
	plain, err := s.vault.Open(enc, st.ID)
	if err != nil {
		return st, err
	}
	st.Key = string(plain)
	return st, nil
}

// AddStory stores a story (its key sealed) in the building state and returns it with its id.
func (s *Store) AddStory(ctx context.Context, st Story) (Story, error) {
	st.ID = uuid.NewString()
	st.State = StoryBuilding
	st.CreatedAt = time.Now().UTC()
	enc, err := s.vault.Seal([]byte(st.Key), st.ID)
	if err != nil {
		return st, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO stories (`+storyCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		st.ID, st.Token, st.OwnerID.String(), st.OwnerName, st.Title, st.Kind, st.SourceID, st.SourceName, st.From, st.To, b2i(st.ShowDates), b2i(st.ShowPlaces), b2i(st.ShowPeople), b2i(st.Preview),
		st.KeyID.String(), enc, tstr(st.EndsAt), st.State, st.Problem, st.Done, st.Total, "[]", "[]", st.CreatedAt.Format(time.RFC3339Nano), "")
	return st, err
}

// StoriesOf are a person's stories, newest first.
func (s *Store) StoriesOf(ctx context.Context, owner uuid.UUID) ([]Story, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+storyCols+` FROM stories WHERE owner_id = ? ORDER BY created_at DESC, id`, owner.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Story
	for rows.Next() {
		st, err := s.scanStory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// StoryByID finds one of a person's stories.
func (s *Store) StoryByID(ctx context.Context, owner uuid.UUID, id string) (Story, error) {
	st, err := s.scanStory(s.db.QueryRowContext(ctx, `SELECT `+storyCols+` FROM stories WHERE id = ? AND owner_id = ?`, id, owner.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return Story{}, ErrNotFound
	}
	return st, err
}

// StoryByToken finds a story that is ready to be shown (not still being built, not failed, not ended).
func (s *Store) StoryByToken(ctx context.Context, token string) (Story, error) {
	st, err := s.scanStory(s.db.QueryRowContext(ctx, `SELECT `+storyCols+` FROM stories WHERE token = ? AND state = ?`, token, StoryReady))
	if errors.Is(err, sql.ErrNoRows) {
		return Story{}, ErrNotFound
	}
	return st, err
}

// SetStoryProgress records how many photos have been looked at.
func (s *Store) SetStoryProgress(ctx context.Context, id string, done, total int) {
	s.db.ExecContext(ctx, `UPDATE stories SET done = ?, total = ? WHERE id = ?`, done, total, id)
}

// SetStoryBuilt records the result of a build: the photos and chapters, or what went wrong.
func (s *Store) SetStoryBuilt(ctx context.Context, id, state, problem string, picks []StoryPick, chapters []StoryChapter) error {
	p, _ := json.Marshal(picks)
	c, _ := json.Marshal(chapters)
	_, err := s.db.ExecContext(ctx, `UPDATE stories SET state = ?, problem = ?, picks = ?, chapters = ?, built_at = ? WHERE id = ?`,
		state, problem, string(p), string(c), time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

// SetStoryBuilding marks a story as being built again (a new pick).
func (s *Store) SetStoryBuilding(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE stories SET state = ?, problem = '', done = 0, total = 0 WHERE id = ? AND state != ?`, StoryBuilding, id, StoryEnded)
	return err
}

// UpdateStory changes what the owner can edit: the title, the chapters, the picks (a photo taken out), what the page shows, the
// preview switch and the end date.
func (s *Store) UpdateStory(ctx context.Context, st Story) error {
	p, _ := json.Marshal(st.Picks)
	c, _ := json.Marshal(st.Chapters)
	_, err := s.db.ExecContext(ctx, `UPDATE stories SET title = ?, show_dates = ?, show_places = ?, show_people = ?, preview = ?, ends_at = ?, picks = ?, chapters = ? WHERE id = ?`,
		st.Title, b2i(st.ShowDates), b2i(st.ShowPlaces), b2i(st.ShowPeople), b2i(st.Preview), tstr(st.EndsAt), string(p), string(c), st.ID)
	return err
}

// DeleteStory forgets a story.
func (s *Store) DeleteStory(ctx context.Context, owner uuid.UUID, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM stories WHERE id = ? AND owner_id = ?`, id, owner.String())
	return err
}

// EndStoriesDue ends the stories whose end date has come.
func (s *Store) EndStoriesDue(ctx context.Context, now time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE stories SET state = ? WHERE ends_at != '' AND ends_at <= ? AND state != ?`, StoryEnded, now.UTC().Format(time.RFC3339Nano), StoryEnded)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// CountStories says how many stories a person has.
func (s *Store) CountStories(ctx context.Context, owner uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM stories WHERE owner_id = ?`, owner.String()).Scan(&n)
	return n, err
}

// StoryOffer is what a person did with the end-of-year suggestion for a year: "" (not offered yet), "dismissed" or "made".
func (s *Store) StoryOffer(ctx context.Context, user uuid.UUID, year int) string {
	var st string
	s.db.QueryRowContext(ctx, `SELECT state FROM story_offers WHERE user_id = ? AND year = ?`, user.String(), year).Scan(&st)
	return st
}

// SetStoryOffer remembers what a person did with the suggestion for a year.
func (s *Store) SetStoryOffer(ctx context.Context, user uuid.UUID, year int, state string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO story_offers (user_id, year, state) VALUES (?,?,?) ON CONFLICT(user_id, year) DO UPDATE SET state = excluded.state`, user.String(), year, state)
	return err
}
