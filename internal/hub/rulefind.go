package hub

import (
	"context"
	"errors"
	"fmt"
	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Finding what a rule matches (docs/design/rules-design.md). A pass asks Immich for what it can filter (people, dates, place, album, a
// phrase) among the photos that reached it in the last week, then the app finishes the job with what Immich cannot: only the
// sender's own photos, nothing archived, trashed or hidden, nothing with a person to leave out or in an album to leave out, nothing
// that has not settled yet, and nothing the rule has already handled. What is left joins the rule's batch of suggestions, each with
// the reason it matched. Immich never says that face detection has finished, so a rule never decides "no match": every pass looks at
// the whole week again.

const (
	ruleWindow = 7 * 24 * time.Hour
	// defaultSettle is how long a photo is in Immich before it is offered, so that its faces have been looked for
	// (RULES_SETTLE_MINUTES); a rule sends one by itself after six times as long.
	defaultSettle = 5 * time.Minute
)

// ruleWait is how long a photo must have been in Immich before this rule offers it: the rule's own choice, or the app's usual wait.
func (h *Hub) ruleWait(r store.Rule) time.Duration {
	if r.SettleMinutes > 0 {
		return time.Duration(r.SettleMinutes) * time.Minute
	}
	return h.ruleSettle()
}

// RuleWait is the app's usual wait before a rule offers a photo (RULES_SETTLE_MINUTES, five minutes unless set), for a form to say.
func (h *Hub) RuleWait() time.Duration { return h.ruleSettle() }

func (h *Hub) ruleSettle() time.Duration {
	if h.RulesSettle > 0 {
		return h.RulesSettle
	}
	return defaultSettle
}

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

// ruleFilter is what Immich is asked for a rule. back leaves out the window of the last week and the rule's start: it is every photo
// that matches, which a look back and the taking out of what no longer matches both need.
func (h *Hub) ruleFilter(r store.Rule, back bool) immich.RuleFilter {
	c := r.Cond
	f := immich.RuleFilter{Country: c.Country, City: c.City, Phrase: c.Phrase, TakenAfter: dayStart(c.After), Make: c.Make, Model: c.Model}
	for _, t := range c.Tags {
		id, _ := uuid.Parse(t.ID)
		f.TagIDs = append(f.TagIDs, id)
	}
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
	if !back { // a look back is the one time a rule looks at photos that were in Immich before it was made
		f.CreatedAfter = r.Since
		if week := h.now().Add(-ruleWindow); week.After(f.CreatedAfter) {
			f.CreatedAfter = week
		}
	}
	return f
}

// FindForRule finds what a rule matches now and has not handled yet. It changes nothing.
func (h *Hub) FindForRule(ctx context.Context, api *immich.API, me uuid.UUID, r store.Rule, back bool) ([]match, error) {
	c := r.Cond
	f := h.ruleFilter(r, back)
	var found []immich.AssetResponseDto
	var whys map[uuid.UUID]string
	var err error
	if c.Pet() {
		found, whys, err = h.findPet(ctx, api, me, r, f.CreatedAfter, back)
	} else {
		found, err = api.RuleSearch(ctx, f)
	}
	if err != nil {
		return nil, err
	}
	known, err := h.Store.RulePhotoStates(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	sentByAny := map[string]bool{}
	if r.Kind != store.RuleKindAlbum { // a smart album has no service: what other rules did is nothing to it
		if sentByAny, err = h.Store.RuleSentAssets(ctx, r.OwnerID, r.Service); err != nil { // another rule for this service handed it over already
			return nil, err
		}
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
	if r.Kind == store.RuleKindAlbum { // what is in the album already is not offered
		album, perr := uuid.Parse(r.Dest)
		if perr != nil {
			return nil, ErrAlbumGone
		}
		items, err := api.AlbumAssets(ctx, album)
		if err != nil {
			if immich.IsNotFound(err) {
				return nil, ErrAlbumGone
			}
			return nil, fmt.Errorf("could not read the album %s: %w", r.DestAlbum, err)
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
		if a.OwnerId != me || a.IsArchived || a.IsTrashed || out[a.Id] || known[a.Id.String()] != "" || sentByAny[a.Id.String()] {
			continue
		}
		if !a.HasMetadata || a.Thumbhash == nil || *a.Thumbhash == "" || h.now().Sub(a.CreatedAt) < h.ruleWait(r) {
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
		why := whyMatched(c, a)
		if c.Pet() {
			why = whys[a.Id]
		}
		matches = append(matches, match{Asset: a, Why: why})
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
	if c.Make != "" {
		why = append(why, "taken with "+strings.TrimSpace(c.Make+" "+c.Model))
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
	if r.Kind == store.RuleKindImport {
		return h.lookImport(ctx, r)
	}
	if r.Kind == store.RuleKindLibrary {
		return h.lookLibrary(ctx, r)
	}
	if r.Kind == store.RuleKindFolder {
		return h.lookFolder(ctx, r)
	}
	if r.Kind == store.RuleKindStack {
		return h.lookStack(ctx, r)
	}
	if r.Kind == store.RuleKindAlbum {
		return h.lookAlbum(ctx, r)
	}
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
	matches, err := h.FindForRule(ctx, api, me.ID, r, false)
	if err != nil {
		problem := trimNet(err)
		if errors.Is(err, immich.ErrNoSmartSearch) {
			problem = "your Immich server cannot do a smart search (it runs without machine learning), so the phrase finds nothing"
			if r.Cond.Pet() {
				problem = "your Immich server cannot do a smart search (it runs without machine learning), so photos like your examples cannot be found"
			}
		}
		h.Store.MarkRuleLook(ctx, r.ID, h.now(), problem)
		return 0, err
	}
	waiting, _ := h.Store.RulePhotos(ctx, r.ID, store.RuleSuggested)
	var auto []string
	var suggested int
	for _, m := range matches {
		id := m.Asset.Id.String()
		if r.Mode == store.RuleAuto && !r.Cond.NeverAuto() && !m.Asset.CreatedAt.Before(r.AutoSince) {
			if h.now().Sub(m.Asset.CreatedAt) < 6*h.ruleWait(r) {
				continue // not settled enough to send without asking; it is looked at again next time
			}
			auto = append(auto, id)
			if err := h.Store.PutRulePhoto(ctx, r.ID, id, store.RuleSuggested, m.Why); err != nil {
				return 0, err
			}
			continue
		}
		suggested++
		if err := h.Store.PutRulePhoto(ctx, r.ID, id, store.RuleSuggested, m.Why); err != nil {
			return 0, err
		}
	}
	if len(auto) > 0 {
		if err := h.sendForRule(ctx, r, auto); err != nil {
			// they stay suggestions: nothing is lost, and the person can still answer
			h.Store.MarkRuleLook(ctx, r.ID, h.now(), trimNet(err))
			return suggested, err
		}
		h.Store.SetRulePhotos(ctx, r.ID, auto, store.RuleSuggested, store.RuleSent)
		h.event(ctx, r.Service, r.OwnerID, "rule", i18n.PackN("{n} photo sent to {service} by a rule on its own: {rule}", "{n} photos sent to {service} by a rule on its own: {rule}", len(auto), "service", r.Service, "rule", RuleSentence(r.Cond)))
	}
	h.Store.MarkRuleLook(ctx, r.ID, h.now(), h.takePetNote(r.ID)) // empty, or that the pet helper is down and Immich's own search was used
	if suggested > 0 {
		h.Log.Debug("a rule found photos", "rule", r.ID, "count", suggested)
		if len(waiting) == 0 && !r.Paused { // told once for a batch, when it begins, not for each photo that joins it
			user := immich.User{ID: r.OwnerID, Name: link.Name}
			h.eventWith(ctx, r.Service, user.ID, "suggestion",
				i18n.PackN("{n} new photo for {service} matches your rule: {rule}. Look at it on the Overview.", "{n} new photos for {service} match your rule: {rule}. Look at them on the Overview.", suggested, "service", r.Service, "rule", RuleSentence(r.Cond)),
				[]ActionSpec{
					{Recipient: r.OwnerID, RecipientName: link.Name, Kind: ActionRuleSend, Service: r.Service, Target: r.ID, Group: r.ID},
					{Recipient: r.OwnerID, RecipientName: link.Name, Kind: ActionRuleDecline, Service: r.Service, Target: r.ID, Group: r.ID}})
		}
	}
	return suggested, nil
}

// RulesMaintenance looks at every rule that is not paused, one after the other.
func (h *Hub) RulesMaintenance(ctx context.Context) {
	if h.Store == nil || !h.SelfService() {
		return
	}
	h.ExpirePairings(ctx)
	h.CirclesMaintenance(ctx)
	h.CastMaintenance(ctx)
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

var (
	ErrLookBackBusy = errors.New("answer the photos that are waiting for this rule first, then look back")
	ErrNothingOlder = errors.New("no older photo matches this rule")
)

// lookBackMost is the most photos one look back suggests.
const lookBackMost = 200

// LookBack suggests the older photos that match a rule (the ones that were in Immich before it was made, or before last week), the
// newest first, as one batch of suggestions. Nothing is sent: they wait for the person's answer like any other.
func (h *Hub) LookBack(ctx context.Context, user immich.User, id string) (int, error) {
	r, err := h.ruleOf(ctx, user.ID, id)
	if err != nil {
		return 0, err
	}
	if r.Kind == store.RuleKindLibrary || r.Kind == store.RuleKindFolder || r.Kind == store.RuleKindStack || r.Kind == store.RuleKindImport { // these look at everything there is: there is nothing older to look back at
		return 0, ErrNothingOlder
	}
	if waiting, _ := h.Store.RulePhotos(ctx, r.ID, store.RuleSuggested); len(waiting) > 0 {
		return 0, ErrLookBackBusy
	}
	var api *immich.API
	if r.Kind == store.RuleKindAlbum {
		api, err = h.albumAPI(ctx, r.OwnerID)
	} else {
		var link *store.Link
		if link, err = h.senderLink(ctx, r.OwnerID, r.Service); err == nil {
			api, err = h.Connect(link.Key)
		}
	}
	if err != nil {
		return 0, err
	}
	me, err := api.Me(ctx)
	if err != nil {
		return 0, err
	}
	matches, err := h.FindForRule(ctx, api, me.ID, r, true)
	if err != nil {
		return 0, err
	}
	if len(matches) == 0 {
		return 0, ErrNothingOlder
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Asset.FileCreatedAt.After(matches[j].Asset.FileCreatedAt) })
	most := lookBackMost
	if r.Kind == store.RuleKindAlbum {
		most = albumBackfillMost
	}
	if len(matches) > most {
		matches = matches[:most]
	}
	for _, m := range matches {
		if err := h.Store.PutRulePhoto(ctx, r.ID, m.Asset.Id.String(), store.RuleSuggested, m.Why); err != nil {
			return 0, err
		}
	}
	if r.Kind == store.RuleKindAlbum {
		h.event(ctx, "", user.ID, "rule", i18n.PackN("{n} older photo found for the album {album} by a rule", "{n} older photos found for the album {album} by a rule", len(matches), "album", r.DestAlbum))
		return len(matches), nil
	}
	h.event(ctx, r.Service, user.ID, "rule", i18n.PackN("{n} older photo found for {service} by a rule: {rule}", "{n} older photos found for {service} by a rule: {rule}", len(matches), "service", r.Service, "rule", RuleSentence(r.Cond)))
	return len(matches), nil
}
