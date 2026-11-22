package hub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/pets"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Rules (docs/contributing/design/rules-design.md): a sender's rule finds photos by who is in them, where and when, and suggests them for a service they
// joined. This file is making, listing, pausing and ending rules.

var (
	ErrRuleEmpty     = errors.New("choose at least one thing for the rule to look for: a person, dates, a place, an album, a phrase, a camera or a tag")
	ErrBadRuleDate   = errors.New("the dates must be real dates, and the first must not be after the last")
	ErrBadRulePhrase = errors.New("the phrase can be up to 100 characters")
	ErrBadSettle     = errors.New("the wait before a photo is offered can be from 1 to 60 minutes")
	ErrTooManyRules  = errors.New("you already have 20 rules; end one first")
	ErrNoRule        = errors.New("that is not one of your rules")
	ErrRulePerson    = errors.New("that person is not among the people Immich has named in your library")
	ErrRulePlace     = errors.New("that place is not one your library holds photos from")
	ErrRuleSame      = errors.New("a person cannot be both looked for and left out")
	ErrRuleCamera    = errors.New("that camera is not one your library holds photos from")
	ErrRuleTag       = errors.New("that tag is not one of yours")
)

const maxRules = 20

// RuleParams is what a person fills in to make a rule.
type RuleParams struct {
	Service   string
	People    []uuid.UUID
	NotPeople []uuid.UUID
	After     string // YYYY-MM-DD
	Before    string
	Country   string
	City      string
	Album     uuid.UUID
	NotAlbums []uuid.UUID
	Phrase    string
	Make      string      // a camera make, one the library holds photos from
	Model     string      // a model of that make
	Tags      []uuid.UUID // tags a photo must all have
	Examples  []uuid.UUID // a pet rule: photos of the person's own library that it looks for look-alikes of
	Also      []string    // other services of the person's to make the same rule for
	// SettleMinutes is how long a photo waits in Immich before the rule offers it (and a sixth of how long before it sends one by itself),
	// so that its faces have been looked for: 0 is the app's usual wait, otherwise 1 to 60.
	SettleMinutes int
}

// maxSettleMinutes is the longest wait a rule can be given before it offers a photo.
const maxSettleMinutes = 60

// RuleChoices are what a person can choose from, read from their own Immich with their own session.
type RuleChoices struct {
	People    []immich.PersonInfo
	Albums    []immich.AlbumInfo
	Countries []string
	Cities    []string
	Makes     []string
	Models    []string
	Tags      []immich.TagInfo
}

// RuleChoicesFor reads the people, albums and places the form offers.
func (h *Hub) RuleChoicesFor(ctx context.Context, sess *immich.API, country string, make ...string) (RuleChoices, error) {
	var c RuleChoices
	var err error
	if c.People, err = sess.People(ctx); err != nil {
		return c, fmt.Errorf("could not list the people in your library: %w", err)
	}
	if c.Albums, err = sess.OwnedAlbums(ctx); err != nil {
		return c, fmt.Errorf("could not list your albums: %w", err)
	}
	if c.Countries, err = sess.Places(ctx, ""); err != nil {
		return c, fmt.Errorf("could not list the places in your library: %w", err)
	}
	if country != "" {
		c.Cities, _ = sess.Places(ctx, country)
	}
	c.Makes, _ = sess.Cameras(ctx, "") // a server without them (or a key without the right) just has no camera to choose
	if len(make) > 0 && make[0] != "" {
		c.Models, _ = sess.Cameras(ctx, make[0])
	}
	c.Tags, _ = sess.Tags(ctx)
	return c, nil
}

func validDay(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// AddRule makes a rule from what the person chose, checking each choice against their own Immich so nothing is made up.
func (h *Hub) AddRule(ctx context.Context, sess *immich.API, user immich.User, p RuleParams) (store.Rule, error) {
	also := p.Also
	p.Also = nil
	first, err := h.addRule(ctx, sess, user, p)
	if err != nil {
		return first, err
	}
	for _, svc := range also { // the same rule for each other service: each hands over on its own
		if svc == p.Service {
			continue
		}
		q := p
		q.Service = svc
		if _, err := h.addRule(ctx, sess, user, q); err != nil {
			return first, fmt.Errorf("the rule for %s was made, but not for %s: %w", p.Service, svc, err)
		}
	}
	return first, nil
}

func (h *Hub) addRule(ctx context.Context, sess *immich.API, user immich.User, p RuleParams) (store.Rule, error) {
	if !h.SelfService() {
		return store.Rule{}, ErrNotJoined
	}
	if err := h.lockedService(ctx, p.Service); err != nil { // a pairing's pool takes only the pairing's own rules
		return store.Rule{}, err
	}
	link, err := h.senderLink(ctx, user.ID, p.Service)
	if err != nil {
		return store.Rule{}, err
	}
	if list, err := h.Store.RulesOf(ctx, user.ID); err != nil {
		return store.Rule{}, err
	} else if len(list) >= maxRules {
		return store.Rule{}, ErrTooManyRules
	}
	cond, prints, needAlbums, err := h.ruleCond(ctx, sess, user, p, false)
	if err != nil {
		return store.Rule{}, err
	}
	if needAlbums { // reading albums for the timer: the key gets album.read through the person's own session, as for a linked album
		if err := sess.AddKeyPermission(ctx, link.KeyID, "album.read"); err != nil {
			return store.Rule{}, fmt.Errorf("could not allow the app to read your albums: %w", err)
		}
	}
	if len(prints) > 0 { // the sidecar is sent a preview, so the rule's key may look at previews: given through the person's own session
		if err := sess.AddKeyPermission(ctx, link.KeyID, "asset.view"); err != nil {
			return store.Rule{}, fmt.Errorf("could not allow the app to look at previews of your photos: %w", err)
		}
	}
	r, err := h.Store.AddRule(ctx, store.Rule{OwnerID: user.ID, Service: p.Service, Cond: cond, SettleMinutes: p.SettleMinutes})
	if err != nil {
		return store.Rule{}, err
	}
	for _, pr := range prints {
		if err := h.Store.PutPetPrint(ctx, r.ID, pr); err != nil {
			h.Store.DeleteRule(ctx, r.ID)
			return store.Rule{}, err
		}
	}
	h.event(ctx, p.Service, user.ID, "rule", i18n.Pack("{name} made a rule for {service}: {rule}", "name", user.Name, "service", p.Service, "rule", RuleSentence(r.Cond)))
	return r, nil
}

// ruleCond checks what a person chose against their own Immich and makes the conditions of a rule from it. album says that the rule is
// for an album of theirs (a smart album): it cannot look for look-alikes of examples (a pet).
func (h *Hub) ruleCond(ctx context.Context, sess *immich.API, user immich.User, p RuleParams, album bool) (cond store.RuleCond, prints []store.PetPrint, needAlbums bool, err error) {
	phrase := strings.TrimSpace(p.Phrase)
	if utf8.RuneCountInString(phrase) > 100 || strings.ContainsAny(phrase, "\r\n\x00") {
		return store.RuleCond{}, nil, false, ErrBadRulePhrase
	}
	if p.SettleMinutes < 0 || p.SettleMinutes > maxSettleMinutes {
		return store.RuleCond{}, nil, false, ErrBadSettle
	}
	cond.Phrase = phrase
	if album && len(p.Examples) > 0 {
		return store.RuleCond{}, nil, false, ErrPetAlone
	}
	if p.Make != "" {
		makes, err := sess.Cameras(ctx, "")
		if err != nil {
			return store.RuleCond{}, nil, false, fmt.Errorf("could not list the cameras in your library: %w", err)
		}
		if !contains(makes, p.Make) {
			return store.RuleCond{}, nil, false, ErrRuleCamera
		}
		cond.Make = p.Make
		if p.Model != "" {
			models, err := sess.Cameras(ctx, p.Make)
			if err != nil || !contains(models, p.Model) {
				return store.RuleCond{}, nil, false, ErrRuleCamera
			}
			cond.Model = p.Model
		}
	} else if p.Model != "" {
		return store.RuleCond{}, nil, false, ErrRuleCamera
	}
	if len(p.Tags) > 0 {
		have, err := sess.Tags(ctx)
		if err != nil {
			return store.RuleCond{}, nil, false, fmt.Errorf("could not list your tags: %w", err)
		}
		for _, id := range p.Tags {
			ok := false
			for _, t := range have {
				if uuid.UUID(t.ID) == id {
					cond.Tags, ok = append(cond.Tags, store.RuleRef{ID: id.String(), Name: t.Value}), true
				}
			}
			if !ok {
				return store.RuleCond{}, nil, false, ErrRuleTag
			}
		}
	}
	if len(p.Examples) > 0 {
		if len(p.Examples) < petMinExamples || len(p.Examples) > petMaxExamples {
			return store.RuleCond{}, nil, false, ErrBadExamples
		}
		seen := map[uuid.UUID]bool{}
		for _, id := range p.Examples {
			if seen[id] {
				return store.RuleCond{}, nil, false, ErrBadExamples
			}
			seen[id] = true
			a, err := sess.Asset(ctx, id)
			if err != nil || a.OwnerId != user.ID || a.IsTrashed {
				return store.RuleCond{}, nil, false, ErrBadExamples // only the person's own photos
			}
			cond.Examples = append(cond.Examples, id.String())
		}
		cond.Rank = petStartRank
		if h.Pets != nil { // with the pet sidecar on: the animal in each example, so that rules compare crops instead of whole photos
			var err error
			prints, err = h.examplePrints(ctx, sess, p.Examples)
			if errors.Is(err, pets.ErrDown) {
				prints, err = nil, nil // it is down now: the rule works with Immich's search, and says so when it looks
			}
			if err != nil {
				return store.RuleCond{}, nil, false, err
			}
		}
	}
	if p.After != "" && !validDay(p.After) || p.Before != "" && !validDay(p.Before) || p.After != "" && p.Before != "" && p.After > p.Before {
		return store.RuleCond{}, nil, false, ErrBadRuleDate
	}
	cond.After, cond.Before = p.After, p.Before
	if len(p.People)+len(p.NotPeople) > 0 {
		people, err := sess.People(ctx)
		if err != nil {
			return store.RuleCond{}, nil, false, fmt.Errorf("could not list the people in your library: %w", err)
		}
		pick := func(ids []uuid.UUID) ([]store.RuleRef, error) {
			var out []store.RuleRef
			for _, id := range ids {
				found := false
				for _, x := range people {
					if uuid.UUID(x.ID) == id {
						out, found = append(out, store.RuleRef{ID: id.String(), Name: x.Name}), true
					}
				}
				if !found {
					return nil, ErrRulePerson
				}
			}
			return out, nil
		}
		if cond.People, err = pick(p.People); err != nil {
			return store.RuleCond{}, nil, false, err
		}
		if cond.NotPeople, err = pick(p.NotPeople); err != nil {
			return store.RuleCond{}, nil, false, err
		}
		for _, a := range cond.People {
			for _, b := range cond.NotPeople {
				if a.ID == b.ID {
					return store.RuleCond{}, nil, false, ErrRuleSame
				}
			}
		}
	}
	if p.Country != "" {
		countries, err := sess.Places(ctx, "")
		if err != nil {
			return store.RuleCond{}, nil, false, fmt.Errorf("could not list the places in your library: %w", err)
		}
		if !contains(countries, p.Country) {
			return store.RuleCond{}, nil, false, ErrRulePlace
		}
		cond.Country = p.Country
		if p.City != "" {
			cities, err := sess.Places(ctx, p.Country)
			if err != nil || !contains(cities, p.City) {
				return store.RuleCond{}, nil, false, ErrRulePlace
			}
			cond.City = p.City
		}
	}
	needAlbums = p.Album != uuid.Nil || len(p.NotAlbums) > 0
	if needAlbums {
		albums, err := sess.OwnedAlbums(ctx)
		if err != nil {
			return store.RuleCond{}, nil, false, fmt.Errorf("could not list your albums: %w", err)
		}
		find := func(id uuid.UUID) (store.RuleRef, error) {
			for _, a := range albums {
				if uuid.UUID(a.ID) == id {
					return store.RuleRef{ID: id.String(), Name: a.Name}, nil
				}
			}
			return store.RuleRef{}, ErrNoAlbum
		}
		if p.Album != uuid.Nil {
			ref, err := find(p.Album)
			if err != nil {
				return store.RuleCond{}, nil, false, err
			}
			cond.Album = &ref
		}
		for _, id := range p.NotAlbums {
			ref, err := find(id)
			if err != nil {
				return store.RuleCond{}, nil, false, err
			}
			cond.NotAlbums = append(cond.NotAlbums, ref)
		}
	}
	if cond.Pet() && (len(p.People)+len(p.NotPeople) > 0 || cond.After != "" || cond.Before != "" || cond.Country != "" || cond.Album != nil || len(cond.NotAlbums) > 0 || cond.Phrase != "" || cond.Make != "" || len(cond.Tags) > 0) {
		return store.RuleCond{}, nil, false, ErrPetAlone
	}
	if !cond.Pet() && len(cond.People) == 0 && cond.After == "" && cond.Before == "" && cond.Country == "" && cond.Album == nil && cond.Phrase == "" && cond.Make == "" && len(cond.Tags) == 0 {
		return store.RuleCond{}, nil, false, ErrRuleEmpty
	}
	return cond, prints, needAlbums, nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// RuleSentence says in plain English what a rule looks for, for the page, the activity and a suggestion.
func RuleSentence(c store.RuleCond) string {
	var parts []string
	if len(c.People) > 0 {
		var n []string
		for _, p := range c.People {
			n = append(n, p.Name)
		}
		parts = append(parts, "photos of "+joinAnd(n))
	} else {
		parts = append(parts, "photos")
	}
	if len(c.NotPeople) > 0 {
		var n []string
		for _, p := range c.NotPeople {
			n = append(n, p.Name)
		}
		parts = append(parts, "without "+joinAnd(n))
	}
	switch {
	case c.After != "" && c.Before != "":
		parts = append(parts, "taken from "+c.After+" to "+c.Before)
	case c.After != "":
		parts = append(parts, "taken from "+c.After)
	case c.Before != "":
		parts = append(parts, "taken until "+c.Before)
	}
	switch {
	case c.City != "":
		parts = append(parts, "in "+c.City+", "+c.Country)
	case c.Country != "":
		parts = append(parts, "in "+c.Country)
	}
	if c.Album != nil {
		parts = append(parts, "in the album "+c.Album.Name)
	}
	if len(c.NotAlbums) > 0 {
		var n []string
		for _, a := range c.NotAlbums {
			n = append(n, a.Name)
		}
		parts = append(parts, "not in "+joinAnd(n))
	}
	if c.Phrase != "" {
		parts = append(parts, "that look like \""+c.Phrase+"\"")
	}
	if c.Make != "" {
		parts = append(parts, "taken with "+strings.TrimSpace(c.Make+" "+c.Model))
	}
	if len(c.Tags) > 0 {
		var n []string
		for _, t := range c.Tags {
			n = append(n, t.Name)
		}
		parts = append(parts, "tagged "+joinAnd(n))
	}
	if c.Pet() {
		parts = append(parts, fmt.Sprintf("that look like your %d example photos", len(c.Examples)))
	}
	return strings.Join(parts, " ")
}

func joinAnd(n []string) string {
	switch len(n) {
	case 0:
		return ""
	case 1:
		return n[0]
	}
	return strings.Join(n[:len(n)-1], ", ") + " and " + n[len(n)-1]
}

// RulesFor lists a person's rules for a service.
func (h *Hub) RulesFor(ctx context.Context, user uuid.UUID, service string) ([]store.Rule, error) {
	if h.Store == nil {
		return nil, nil
	}
	all, err := h.Store.RulesOf(ctx, user)
	if err != nil {
		return nil, err
	}
	var out []store.Rule
	for _, r := range all {
		if r.Service == service {
			out = append(out, r)
		}
	}
	return out, nil
}

func (h *Hub) ruleOf(ctx context.Context, user uuid.UUID, id string) (store.Rule, error) {
	r, err := h.Store.RuleByID(ctx, id)
	if err != nil || r.OwnerID != user {
		return store.Rule{}, ErrNoRule
	}
	return r, nil
}

// PauseRule pauses or resumes a rule.
func (h *Hub) PauseRule(ctx context.Context, user uuid.UUID, id string, paused bool) error {
	r, err := h.ruleOf(ctx, user, id)
	if err != nil {
		return err
	}
	if r.Pairing != "" && !isPairingOp(ctx) {
		return ErrLockedByPairing
	}
	return h.Store.SetRulePaused(ctx, r.ID, paused)
}

// DeleteRule ends a rule. Photos already handed over stay where they are.
func (h *Hub) DeleteRule(ctx context.Context, user immich.User, id string) error {
	return h.DeleteRuleWith(ctx, nil, user, id)
}

// DeleteRuleWith is DeleteRule, with the person's own session when there is one: ending the last rule that follows an
// album then also deletes the key the app made for it in their Immich.
func (h *Hub) DeleteRuleWith(ctx context.Context, sess *immich.API, user immich.User, id string) error {
	r, err := h.ruleOf(ctx, user.ID, id)
	if err != nil {
		return err
	}
	if r.Pairing != "" && !isPairingOp(ctx) {
		return ErrLockedByPairing
	}
	if err := h.Store.DeleteRule(ctx, r.ID); err != nil {
		return err
	}
	if r.Kind == store.RuleKindImport {
		h.dropLibraryKeyIfUnused(ctx, sess, user.ID)
		h.event(ctx, "", user.ID, "rule", i18n.Pack("{name} stopped following the destination {destination}", "name", user.Name, "destination", r.Cond.SourceName))
		return nil
	}
	if r.Kind == store.RuleKindLibrary {
		h.dropLibraryKeyIfUnused(ctx, sess, user.ID)
		h.event(ctx, "", user.ID, "rule", i18n.Pack("{name} stopped following the album {album}", "name", user.Name, "album", r.Cond.Album.Name))
		return nil
	}
	if r.Kind == store.RuleKindStack {
		h.dropStackKeyIfUnused(ctx, sess, user.ID)
		h.event(ctx, "", user.ID, "rule", i18n.Pack("{name} stopped stacking photos by rule", "name", user.Name))
		return nil
	}
	if r.Kind == store.RuleKindPlace {
		h.dropPlaceKeyIfUnused(ctx, sess, user.ID)
		h.event(ctx, "", user.ID, "rule", i18n.Pack("{name} stopped offering places to photos that have none", "name", user.Name))
		return nil
	}
	if r.Kind == store.RuleKindAlbum {
		h.dropAlbumKeyIfUnused(ctx, sess, user.ID)
		h.event(ctx, "", user.ID, "rule", i18n.Pack("{name} stopped keeping the album {album} up to date", "name", user.Name, "album", r.DestAlbum))
		return nil
	}
	if r.Kind == store.RuleKindFolder {
		h.dropFolderKeyIfUnused(ctx, sess, user.ID)
		h.event(ctx, "", user.ID, "rule", i18n.Pack("{name} stopped making albums from the folders under {folder}", "name", user.Name, "folder", r.Cond.Folder))
		return nil
	}
	h.event(ctx, r.Service, user.ID, "rule", i18n.Pack("{name} ended a rule for {service}: {rule}", "name", user.Name, "service", r.Service, "rule", RuleSentence(r.Cond)))
	return nil
}
