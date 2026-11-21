package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
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
	RuleSeen      = "seen"      // a photo that was in a followed album when the rule began: not offered, only remembered

	// What a rule does with what it finds: send it to a service (the first kind of rule), or copy it into the person's own
	// library (follow an album that was shared with them).
	RuleKindSend    = "send"
	RuleKindLibrary = "library"
	// RuleKindFolder makes albums from the folders of the person's external library (Cond.Folder, Level and Naming).
	RuleKindFolder = "folder"
	// RuleKindStack stacks the person's photos that belong together: a RAW and its JPEG, an edit and its original, a burst.
	RuleKindStack = "stack"
	// RuleKindImport follows a destination (a folder, a bucket, a server): the files that appear in it are offered, and added to the
	// person's library, with the sender's own key. Cond.Source names the destination, Cond.Prefix the folder under it.
	RuleKindImport = "import"
	// RuleKindPlace gives the person's photos that have no place the place of a located photo of the same camera taken minutes from it.
	RuleKindPlace = "place"
	// RuleKindAlbum keeps one of the person's albums up to date with what a rule finds (Dest is the album's id and DestAlbum its name; the
	// conditions are the ordinary ones). See docs/design/albums-design.md.
	RuleKindAlbum = "album"
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
	// Make and Model are a camera (as Immich names it), and Tags the tags a photo must all have.
	Make  string    `json:"make,omitempty"`
	Model string    `json:"model,omitempty"`
	Tags  []RuleRef `json:"tags,omitempty"`
	// RemoveStale, for a rule of kind album, lets it take out of the album the photos it put there that no longer match.
	RemoveStale bool `json:"removeStale,omitempty"`
	// Examples are the photos of the person's own library that a pet rule looks for look-alikes of ("photos like these"), and Rank is how
	// far down Immich's list of look-alikes of an example a photo may be and still be offered, learned from the answers.
	Examples []string `json:"examples,omitempty"`
	Rank     int      `json:"rank,omitempty"`
	// Cutoff is how like an example's animal a crop must be (cosine of the sidecar's fingerprints) for a photo to be offered, when the
	// optional pet sidecar is on; the person's answers move it. Zero: the default.
	Cutoff float64 `json:"cutoff,omitempty"`
	// Source, SourceName and Prefix are what an import rule follows: the destination ("admin:<name>" or "own:<id>"), the name it had, and the
	// folder under its root.
	Source     string `json:"source,omitempty"`
	SourceName string `json:"sourceName,omitempty"`
	Prefix     string `json:"prefix,omitempty"`
	// AlbumOwner is who shared the album, for a rule that follows an album shared with the person.
	AlbumOwner string `json:"albumOwner,omitempty"`
	// Folder, Level and Naming say which folders of an external library become albums, for a rule of kind folder.
	Folder string `json:"folder,omitempty"`
	Level  int    `json:"level,omitempty"`
	Naming string `json:"naming,omitempty"`
	// Signals and Cover say what a rule of kind stack groups ("pair", "edit", "burst") and which photo is the cover of a stack;
	// Folder, when set, limits it to that folder.
	Signals []string `json:"signals,omitempty"`
	Cover   string   `json:"cover,omitempty"`
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
	// Kind is RuleKindSend (the default) or RuleKindLibrary. A library rule has no service: it follows the album in
	// Cond.Album and copies what others add to it into the owner's library, to Dest ("tag", "plain" or "album", named DestAlbum).
	Kind      string
	Dest      string
	DestAlbum string
	// CaughtUp is when a rule of kind folder finished its first full look at the folder (zero: not yet). From then on it only
	// lists the photos that came to Immich after it, which is what keeps a pass cheap in a very large library.
	CaughtUp time.Time
	// Pairing is the id of the pairing this rule belongs to (empty: an ordinary rule). A rule of a pairing cannot be changed or
	// deleted by hand: the pairing's card is the only place.
	Pairing string
	// SettleMinutes is how long a photo must have been in Immich before this rule offers it, and a sixth of how long before it sends
	// one by itself (zero: the app's usual wait, RULES_SETTLE_MINUTES). Immich never says that it has finished looking for faces, so
	// this is the person's choice between seeing new photos sooner and being sure a second person in them has been found.
	SettleMinutes int
}

const ruleCols = `id, owner_id, service, cond, mode, always, paused, since, auto_since, last_look, problem, created_at, kind, dest, dest_album, caught_up, pairing, settle_minutes`

func scanRule(sc interface{ Scan(...any) error }) (Rule, error) {
	var r Rule
	var owner, cond, since, auto, last, created, caught string
	if err := sc.Scan(&r.ID, &owner, &r.Service, &cond, &r.Mode, &r.Always, &r.Paused, &since, &auto, &last, &r.Problem, &created, &r.Kind, &r.Dest, &r.DestAlbum, &caught, &r.Pairing, &r.SettleMinutes); err != nil {
		return r, err
	}
	r.OwnerID, _ = uuid.Parse(owner)
	_ = json.Unmarshal([]byte(cond), &r.Cond)
	r.Since, _ = time.Parse(time.RFC3339Nano, since)
	r.AutoSince, _ = time.Parse(time.RFC3339Nano, auto)
	r.LastLook, _ = time.Parse(time.RFC3339Nano, last)
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	r.CaughtUp, _ = time.Parse(time.RFC3339Nano, caught)
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
	if r.Kind == "" {
		r.Kind = RuleKindSend
	}
	r.CreatedAt = time.Now().UTC()
	if r.Since.IsZero() {
		r.Since = r.CreatedAt
	}
	cond, _ := json.Marshal(r.Cond)
	_, err := s.db.ExecContext(ctx, `INSERT INTO rules (`+ruleCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.OwnerID.String(), r.Service, string(cond), r.Mode, r.Always, b2i(r.Paused), r.Since.Format(time.RFC3339Nano), tstr(r.AutoSince), tstr(r.LastLook), r.Problem, r.CreatedAt.Format(time.RFC3339Nano), r.Kind, r.Dest, r.DestAlbum, tstr(r.CaughtUp), r.Pairing, r.SettleMinutes)
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
	if _, err := s.db.ExecContext(ctx, `DELETE FROM pet_prints WHERE rule_id = ?`, id); err != nil {
		return err
	}
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

// AddRuleAlways counts one more "Always" answer.
func (s *Store) AddRuleAlways(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE rules SET always = always + 1 WHERE id = ?`, id)
	return err
}

// SetRuleMode switches a rule between asking first and sending by itself. Switching to auto records the moment: only photos that
// reached Immich after it are ever sent without asking.
func (s *Store) SetRuleMode(ctx context.Context, id, mode string, at time.Time) error {
	auto := ""
	if mode == RuleAuto {
		auto = at.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE rules SET mode = ?, auto_since = ? WHERE id = ?`, mode, auto, id)
	return err
}

// RuleSentAssets are the photos that any rule of this person for this service has sent, so that two rules that match the same
// photo hand it over once.
func (s *Store) RuleSentAssets(ctx context.Context, owner uuid.UUID, service string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.asset_id FROM rule_photos p JOIN rules r ON r.id = p.rule_id WHERE r.owner_id = ? AND r.service = ? AND p.state = ?`, owner.String(), service, RuleSent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// MarkRuleCaughtUp records that a rule of kind folder has looked at everything in its folder up to that moment.
func (s *Store) MarkRuleCaughtUp(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE rules SET caught_up = ? WHERE id = ?`, at.UTC().Format(time.RFC3339Nano), id)
	return err
}

// Pet says whether the rule looks for look-alikes of example photos (a pet), which can only ever suggest.
func (c RuleCond) Pet() bool { return len(c.Examples) > 0 }

// NeverAuto says whether a rule with this condition always asks first: a phrase and a pet are never certain.
func (c RuleCond) NeverAuto() bool { return c.Phrase != "" || c.Pet() }

// SetRuleCond replaces what a rule looks for (a pet rule changes its examples and its rank as it learns).
func (s *Store) SetRuleCond(ctx context.Context, id string, c RuleCond) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE rules SET cond = ? WHERE id = ?`, string(b), id)
	return err
}

// PetPrint is the fingerprint of the animal in an example photo of a pet rule, and where it is in the photo (x, y, width, height as
// fractions of the photo, for the card to draw it).
type PetPrint struct {
	Asset string
	Kind  string
	Box   [4]float64
	Vec   []float32
}

// PutPetPrint keeps (or replaces) the fingerprint of the animal in an example photo.
func (s *Store) PutPetPrint(ctx context.Context, rule string, p PetPrint) error {
	box, _ := json.Marshal(p.Box)
	b := make([]byte, 4*len(p.Vec))
	for i, v := range p.Vec {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(v))
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO pet_prints (rule_id, asset_id, kind, box, vec) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(rule_id, asset_id) DO UPDATE SET kind = excluded.kind, box = excluded.box, vec = excluded.vec`, rule, p.Asset, p.Kind, string(box), b)
	return err
}

// PetPrints are the fingerprints a pet rule has, in the order they were made.
func (s *Store) PetPrints(ctx context.Context, rule string) ([]PetPrint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT asset_id, kind, box, vec FROM pet_prints WHERE rule_id = ? ORDER BY rowid`, rule)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PetPrint
	for rows.Next() {
		var p PetPrint
		var box string
		var b []byte
		if err := rows.Scan(&p.Asset, &p.Kind, &box, &b); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(box), &p.Box)
		for i := 0; i+4 <= len(b); i += 4 {
			p.Vec = append(p.Vec, math.Float32frombits(binary.LittleEndian.Uint32(b[i:])))
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
