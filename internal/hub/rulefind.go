package hub

import (
	"context"
	"errors"
	"fmt"
	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Finding what a rule matches (docs/rules-design.md). A pass asks Immich for what it can filter (people, dates, place, album, a
// phrase) among the photos that reached it in the last week, then the app finishes the job with what Immich cannot: only the
// sender's own photos, nothing archived, trashed or hidden, nothing with a person to leave out or in an album to leave out, nothing
// that has not settled yet, and nothing the rule has already handled. What is left joins the rule's batch of suggestions, each with
// the reason it matched. Immich never says that face detection has finished, so a rule never decides "no match": every pass looks at
// the whole week again.

const (
	ruleWindow = 7 * 24 * time.Hour
	ruleSettle = 5 * time.Minute // a photo is offered once it has been in Immich this long, so that its faces have been looked for
)

// ruleEvery is how often rules look (RULES_CHECK_MINUTES, default 10).
func (h *Hub) ruleEvery() time.Duration {
	if h.RulesEvery > 0 {
		return h.RulesEvery
	}
	return 10 * time.Minute
}

func dayStart(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// match is a photo a rule found and why.
type match struct {
	Asset immich.AssetResponseDto
	Why   string
}

// FindForRule finds what a rule matches now and has not handled yet. It changes nothing.
func (h *Hub) FindForRule(ctx context.Context, api *immich.API, me uuid.UUID, r store.Rule) ([]match, error) {
	c := r.Cond
	f := immich.RuleFilter{Country: c.Country, City: c.City, Phrase: c.Phrase, TakenAfter: dayStart(c.After)}
	if c.Before != "" {
		f.TakenBefore = dayStart(c.Before).Add(24*time.Hour - time.Second)
	}
	for _, p := range c.People {
		id, _ := uuid.Parse(p.ID)
		f.PersonIDs = append(f.PersonIDs, id)
	}
	if c.Album != nil {
		f.AlbumID, _ = uuid.Parse(c.Album.ID)
	}
	f.CreatedAfter = r.Since
	if week := h.now().Add(-ruleWindow); week.After(f.CreatedAfter) {
		f.CreatedAfter = week
	}
	found, err := api.RuleSearch(ctx, f)
	if err != nil {
		return nil, err
	}
	known, err := h.Store.RulePhotoStates(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	// the photos of the albums to leave out
	out := map[uuid.UUID]bool{}
	for _, al := range c.NotAlbums {
		id, _ := uuid.Parse(al.ID)
		items, err := api.AlbumAssets(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("could not read the album %s: %w", al.Name, err)
		}
		for _, it := range items {
			out[it.Id] = true
		}
	}
	notPeople := map[string]bool{}
	for _, p := range c.NotPeople {
		notPeople[p.ID] = true
	}
	var matches []match
	for _, a := range found {
		if a.OwnerId != me || a.IsArchived || a.IsTrashed || out[a.Id] || known[a.Id.String()] != "" {
			continue
		}
		if !a.HasMetadata || a.Thumbhash == nil || *a.Thumbhash == "" || h.now().Sub(a.CreatedAt) < ruleSettle {
			continue
		}
		people := a.People
		if len(notPeople) > 0 && people == nil { // a smart search does not say who is in a photo
			full, err := api.Asset(ctx, a.Id)
			if err != nil {
				continue
			}
			people = full.People
		}
		skip := false
		if people != nil {
			for _, p := range *people {
				if notPeople[p.Id.String()] {
					skip = true
				}
			}
		}
		if skip {
			continue
		}
		matches = append(matches, match{Asset: a, Why: whyMatched(c, a)})
	}
	return matches, nil
}

// whyMatched says in a few words why a photo matched, for the person to judge a suggestion.
func whyMatched(c store.RuleCond, a immich.AssetResponseDto) string {
	var why []string
	if len(c.People) > 0 {
		var names []string
		for _, p := range c.People {
			names = append(names, p.Name)
		}
		why = append(why, joinAnd(names)+" in it")
	}
	if c.After != "" || c.Before != "" {
		why = append(why, "taken "+a.LocalDateTime.Format("2 Jan 2006"))
	}
	switch {
	case c.City != "":
		why = append(why, "in "+c.City)
	case c.Country != "":
		why = append(why, "in "+c.Country)
	}
	if c.Album != nil {
		why = append(why, "in the album "+c.Album.Name)
	}
	if c.Phrase != "" {
		why = append(why, "looks like \""+c.Phrase+"\"")
	}
	return strings.Join(why, ", ")
}

// LookRule looks at one rule and adds what it finds to its batch of suggestions. It returns how many photos are new.
func (h *Hub) LookRule(ctx context.Context, r store.Rule) (int, error) {
	link, err := h.senderLink(ctx, r.OwnerID, r.Service)
	if err != nil {
		return 0, err
	}
	api, err := h.Connect(link.Key)
	if err != nil {
		return 0, err
	}
	me, err := api.Me(ctx)
	if err != nil {
		h.Store.MarkRuleLook(ctx, r.ID, h.now(), "your Immich does not accept the app's key")
		return 0, err
	}
	matches, err := h.FindForRule(ctx, api, me.ID, r)
	if err != nil {
		problem := trimNet(err)
		if errors.Is(err, immich.ErrNoSmartSearch) {
			problem = "your Immich server cannot do a smart search (it runs without machine learning), so the phrase finds nothing"
		}
		h.Store.MarkRuleLook(ctx, r.ID, h.now(), problem)
		return 0, err
	}
	waiting, _ := h.Store.RulePhotos(ctx, r.ID, store.RuleSuggested)
	for _, m := range matches {
		if err := h.Store.PutRulePhoto(ctx, r.ID, m.Asset.Id.String(), store.RuleSuggested, m.Why); err != nil {
			return 0, err
		}
	}
	h.Store.MarkRuleLook(ctx, r.ID, h.now(), "")
	if len(matches) > 0 {
		h.Log.Debug("a rule found photos", "rule", r.ID, "count", len(matches))
		if len(waiting) == 0 && !r.Paused { // told once for a batch, when it begins, not for each photo that joins it
			user := immich.User{ID: r.OwnerID, Name: link.Name}
			h.eventWith(ctx, r.Service, user.ID, "suggestion",
				i18n.PackN("{n} new photo for {service} matches your rule: {rule}. Look at it on the Overview.", "{n} new photos for {service} match your rule: {rule}. Look at them on the Overview.", len(matches), "service", r.Service, "rule", RuleSentence(r.Cond)),
				[]ActionSpec{
					{Recipient: r.OwnerID, RecipientName: link.Name, Kind: ActionRuleSend, Service: r.Service, Target: r.ID, Group: r.ID},
					{Recipient: r.OwnerID, RecipientName: link.Name, Kind: ActionRuleDecline, Service: r.Service, Target: r.ID, Group: r.ID}})
		}
	}
	return len(matches), nil
}

// RulesMaintenance looks at every rule that is not paused, one after the other.
func (h *Hub) RulesMaintenance(ctx context.Context) {
	if h.Store == nil || !h.SelfService() {
		return
	}
	list, err := h.Store.AllRules(ctx)
	if err != nil {
		return
	}
	for _, r := range list {
		if ctx.Err() != nil {
			return
		}
		if _, err := h.LookRule(ctx, r); err != nil && !errors.Is(err, ErrNotJoined) {
			h.Log.Debug("a rule could not be looked at", "rule", r.ID, "err", err)
		}
	}
}

// RulesLoop looks at the rules every few minutes until the context ends.
func (h *Hub) RulesLoop(ctx context.Context) {
	t := time.NewTicker(h.ruleEvery())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.RulesMaintenance(ctx)
		}
	}
}
