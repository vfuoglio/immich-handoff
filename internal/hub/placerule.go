package hub

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// A rule of kind place (#273) gives the person's photos that have no place the place of a located photo of the same camera taken
// minutes from them (placefind.go). It looks with a key of the person's own, offers each guess with the photo it was taken from, and
// writes a place only when the person says so, or, when they have let it, for a photo with a located photo on both sides. Immich
// cannot be told to clear a place (a null is refused on 3.4.1), so a written place is not taken back by the app: the person removes it
// in Immich if one is wrong, which is why the first answer is always asked for. See docs/contributing/design/places-design.md.

const (
	placeKeyName    = "handoff: places"
	placePerPass    = 200 // the most photos one look at one rule takes up
	placeDaysPerRun = 30  // the most days read again for neighbours in one look
	placeDayMax     = 800 // the most photos read for one day
	placeListMax    = 3000
)

var (
	ErrNoPlaceKey    = errors.New("the key the app uses for places is gone: make the rule again")
	ErrPlacesTwice   = errors.New("you already have a rule that gives photos places")
	ErrPlaceKeyMiss  = errors.New("the app could not read your photos with its key")
	ErrPlacesNothing = errors.New("no photo without a place has a located photo of the same camera close to it")
)

func (h *Hub) ensurePlaceKey(ctx context.Context, sess *immich.API, user immich.User) (*immich.API, error) {
	if !h.SelfService() {
		return nil, ErrNotJoined
	}
	k, ok, err := h.Store.PlaceKey(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	if ok {
		return h.Connect(k.Key)
	}
	id, secret, err := sess.CreateAPIKey(ctx, placeKeyName, immich.PlacePermissions)
	if err != nil {
		return nil, fmt.Errorf("could not make the app a key for places: %w", err)
	}
	if err := h.Store.SetPlaceKey(ctx, user.ID, store.PlaceKey{KeyID: id, Key: secret}); err != nil {
		_ = sess.DeleteAPIKey(ctx, id)
		return nil, err
	}
	return h.Connect(secret)
}

func (h *Hub) placeAPI(ctx context.Context, user uuid.UUID) (*immich.API, error) {
	if h.Store == nil {
		return nil, ErrNotJoined
	}
	k, ok, err := h.Store.PlaceKey(ctx, user)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNoPlaceKey
	}
	return h.Connect(k.Key)
}

// dropPlaceKeyIfUnused deletes the person's key (in their Immich, with their session) when no rule of theirs gives places any more.
func (h *Hub) dropPlaceKeyIfUnused(ctx context.Context, sess *immich.API, user uuid.UUID) {
	rules, err := h.Store.RulesOf(ctx, user)
	if err != nil {
		return
	}
	for _, r := range rules {
		if r.Kind == store.RuleKindPlace {
			return
		}
	}
	k, ok, err := h.Store.PlaceKey(ctx, user)
	if err != nil || !ok {
		return
	}
	if sess != nil && k.KeyID != uuid.Nil {
		if err := sess.DeleteAPIKey(ctx, k.KeyID); err != nil && !immich.IsNotFound(err) {
			h.Log.Debug("could not delete the key for places", "err", err)
		}
	}
	_ = h.Store.DeletePlaceKey(ctx, user)
}

// FollowPlaces makes the person's rule that gives photos places. Nothing is written until they say so.
func (h *Hub) FollowPlaces(ctx context.Context, sess *immich.API, user immich.User) (store.Rule, error) {
	if !h.SelfService() {
		return store.Rule{}, ErrNotJoined
	}
	rules, err := h.Store.RulesOf(ctx, user.ID)
	if err != nil {
		return store.Rule{}, err
	}
	if len(rules) >= maxRules {
		return store.Rule{}, ErrTooManyRules
	}
	for _, r := range rules {
		if r.Kind == store.RuleKindPlace {
			return store.Rule{}, ErrPlacesTwice
		}
	}
	api, err := h.ensurePlaceKey(ctx, sess, user)
	if err != nil {
		return store.Rule{}, err
	}
	if _, err := api.PlaceSearch(ctx, time.Time{}, time.Time{}, time.Now().Add(time.Hour), 1); err != nil { // asks for nothing: it shows that the key can search
		h.dropPlaceKeyIfUnused(ctx, sess, user.ID)
		return store.Rule{}, fmt.Errorf("%w: %v", ErrPlaceKeyMiss, trimNet(err))
	}
	rule, err := h.Store.AddRule(ctx, store.Rule{OwnerID: user.ID, Kind: store.RuleKindPlace})
	if err != nil {
		h.dropPlaceKeyIfUnused(ctx, sess, user.ID)
		return store.Rule{}, err
	}
	h.event(ctx, "", user.ID, "rule", i18n.Pack("{name} lets the app offer places to photos that have none", "name", user.Name))
	return rule, nil
}

func placePhotoOf(a immich.AssetResponseDto) PlacePhoto {
	p := PlacePhoto{ID: uuid.UUID(a.Id), Name: a.OriginalFileName, Local: a.LocalDateTime, Created: a.CreatedAt}
	if e := a.ExifInfo; e != nil {
		if e.DateTimeOriginal != nil {
			p.Taken = *e.DateTimeOriginal
		}
		p.Lat, p.Lon = e.Latitude, e.Longitude
		str := func(s *string) string {
			if s == nil {
				return ""
			}
			return *s
		}
		p.Make, p.Model, p.Lens, p.Zone = str(e.Make), str(e.Model), str(e.LensModel), str(e.TimeZone)
		p.City, p.Country = str(e.City), str(e.Country)
	}
	return p
}

// dayBounds is the stretch of time that surely holds a local day, whatever the time zone it was taken in.
func dayBounds(local time.Time) (time.Time, time.Time) {
	y, m, d := local.Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return start.Add(-14 * time.Hour), start.Add(24*time.Hour + 14*time.Hour)
}

// lookPlace looks at one rule that gives places: the photos that have none become suggestions (each with the photo its place comes
// from), or, when the person has let the rule go on by itself, get their place, but only a photo with a located photo on both sides,
// that came after the switch and has settled. back looks at older photos too (the person asked for it). It returns how many are new.
func (h *Hub) lookPlace(ctx context.Context, r store.Rule, back bool) (int, error) {
	fail := func(problem string, err error) (int, error) {
		h.Store.MarkRuleLook(ctx, r.ID, h.now(), problem)
		return 0, err
	}
	api, err := h.placeAPI(ctx, r.OwnerID)
	if err != nil {
		return fail(ErrNoPlaceKey.Error(), err)
	}
	me, err := api.Me(ctx)
	if err != nil {
		return fail("your Immich does not accept the app's key", err)
	}
	var after time.Time
	max := placeListMax
	if !back {
		after = r.Since
		if week := h.now().Add(-ruleWindow); week.After(after) {
			after = week
		}
	}
	list, err := api.PlaceSearch(ctx, time.Time{}, time.Time{}, after, max)
	if err != nil {
		return fail(trimNet(err), err)
	}
	known, err := h.Store.RulePhotoStates(ctx, r.ID)
	if err != nil {
		return 0, err
	}
	candidate := map[uuid.UUID]bool{}
	stuck := 0 // photos given a place that Immich did not keep
	byID := map[uuid.UUID]PlacePhoto{}
	days := map[string]time.Time{}
	var cands []PlacePhoto
	for _, a := range list {
		if a.OwnerId != me.ID || a.IsTrashed || a.IsArchived || a.Type != immich.IMAGE {
			continue
		}
		p := placePhotoOf(a)
		byID[p.ID] = p
		if !p.located() && known[p.ID.String()] == store.RuleSent {
			stuck++ // written on an earlier look, and the photo still has no place
		}
		if p.located() || p.Make == "" || p.Taken.IsZero() || known[p.ID.String()] != "" {
			continue
		}
		if !a.HasMetadata || a.Thumbhash == nil || *a.Thumbhash == "" || h.now().Sub(a.CreatedAt) < h.ruleWait(r) {
			continue // not settled: Immich has not finished reading it
		}
		if len(cands) >= placePerPass {
			break
		}
		cands = append(cands, p)
		candidate[p.ID] = true
		days[p.Local.Format("2006-01-02")] = p.Local
	}
	if len(cands) == 0 {
		problem := ""
		if stuck > 0 {
			problem = fmt.Sprintf("Immich did not keep the place written into %d photos: a photo of an external library, whose files cannot be changed, may not keep one", stuck)
		}
		h.Store.MarkRuleLook(ctx, r.ID, h.now(), problem)
		return 0, nil
	}
	// the located photos around them, a day at a time, newest day first, at most placeDaysPerRun days
	keys := make([]string, 0, len(days))
	for k := range days {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	if len(keys) > placeDaysPerRun {
		keys = keys[:placeDaysPerRun]
	}
	for _, k := range keys {
		from, to := dayBounds(days[k])
		near, err := api.PlaceSearch(ctx, from, to, time.Time{}, placeDayMax)
		if err != nil {
			return fail(trimNet(err), err)
		}
		for _, a := range near {
			if a.OwnerId != me.ID || a.IsTrashed || a.IsArchived || a.Type != immich.IMAGE {
				continue
			}
			if _, have := byID[uuid.UUID(a.Id)]; !have {
				byID[uuid.UUID(a.Id)] = placePhotoOf(a)
			}
		}
	}
	all := make([]PlacePhoto, 0, len(byID))
	for _, p := range byID {
		all = append(all, p)
	}
	waiting, _ := h.Store.RulePhotos(ctx, r.ID, store.RuleSuggested)
	var auto []string
	suggested := 0
	for _, g := range GuessPlaces(all) {
		if !candidate[g.Photo.ID] {
			continue
		}
		why := placeWhy(g)
		if err := h.Store.PutRulePhoto(ctx, r.ID, g.Photo.ID.String(), store.RuleSuggested, why); err != nil {
			return 0, err
		}
		if known[g.Near.ID.String()] == "" { // so that the review page may show the photo the place comes from
			_ = h.Store.PutRulePhoto(ctx, r.ID, g.Near.ID.String(), store.RuleSeen, "")
			known[g.Near.ID.String()] = store.RuleSeen
		}
		if r.Mode == store.RuleAuto && g.TwoSided && !back && !g.Photo.Created.Before(r.AutoSince) && h.now().Sub(g.Photo.Created) >= 6*h.ruleWait(r) {
			auto = append(auto, g.Photo.ID.String())
		} else {
			suggested++
		}
	}
	if len(auto) > 0 {
		if err := h.placeForRule(ctx, r, auto); err != nil {
			h.Store.MarkRuleLook(ctx, r.ID, h.now(), trimNet(err)) // they stay suggestions: nothing is lost
			return suggested, err
		}
		h.event(ctx, "", r.OwnerID, "place", i18n.PackN("{n} photo got a place from the photos around it on its own", "{n} photos got a place from the photos around them on their own", len(auto)))
	}
	problem := ""
	if stuck > 0 {
		problem = fmt.Sprintf("Immich did not keep the place written into %d photos: a photo of an external library, whose files cannot be changed, may not keep one", stuck)
	}
	h.Store.MarkRuleLook(ctx, r.ID, h.now(), problem)
	if suggested > 0 && len(waiting) == 0 && !r.Paused && !back { // told once for a batch, when it begins
		h.event(ctx, "", r.OwnerID, "suggestion", i18n.PackN("{n} photo can get a place from a photo next to it. Look at it on the Overview.", "{n} photos can get a place from the photos next to them. Look at them on the Overview.", suggested))
	}
	return suggested + len(auto), nil
}

// placeForRule writes the places of the photos (read from the rows of the rule), with the person's key: the photos that got the same
// place are written together. A photo that was written is marked sent; one Immich refuses stays a suggestion, and the first refusal is
// returned after the others have been tried.
func (h *Hub) placeForRule(ctx context.Context, r store.Rule, assets []string) error {
	api, err := h.placeAPI(ctx, r.OwnerID)
	if err != nil {
		return err
	}
	rows, err := h.Store.RulePhotos(ctx, r.ID, store.RuleSuggested)
	if err != nil {
		return err
	}
	chosen := map[string]bool{}
	for _, a := range assets {
		chosen[a] = true
	}
	type at struct{ lat, lon float64 }
	groups := map[at][]string{}
	var order []at
	for _, row := range rows {
		if !chosen[row.AssetID] {
			continue
		}
		reason, ok := ParsePlaceWhy(row.Why)
		if !ok {
			continue
		}
		k := at{reason.Lat, reason.Lon}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], row.AssetID)
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].lat != order[j].lat {
			return order[i].lat < order[j].lat
		}
		return order[i].lon < order[j].lon
	})
	var first error
	for _, k := range order {
		ids := groups[k]
		var uu []openapi_types.UUID
		for _, id := range ids {
			if u, err := uuid.Parse(id); err == nil {
				uu = append(uu, u)
			}
		}
		for len(uu) > 0 { // in pieces, so that one request is never huge
			n := min(len(uu), 200)
			if err := api.SetLocation(ctx, uu[:n], k.lat, k.lon); err != nil {
				if first == nil {
					first = fmt.Errorf("could not write a place into %d photos: %w", n, err)
				}
				uu = uu[n:]
				ids = ids[min(n, len(ids)):]
				continue
			}
			h.Store.SetRulePhotos(ctx, r.ID, ids[:n], store.RuleSuggested, store.RuleSent)
			uu, ids = uu[n:], ids[n:]
		}
	}
	return first
}

// PlaceSentence says what a rule that gives places does, in a few words.
func PlaceSentence() string {
	return "give photos that have no place the place of a photo of the same camera taken minutes from them"
}

// PlaceMapLink is the address of the place on OpenStreetMap: a plain link a person may click, which loads nothing by itself.
func PlaceMapLink(lat, lon float64) string {
	la, lo := strconv.FormatFloat(lat, 'f', 5, 64), strconv.FormatFloat(lon, 'f', 5, 64)
	return "https://www.openstreetmap.org/?mlat=" + la + "&mlon=" + lo + "#map=16/" + la + "/" + lo
}

// PlaceReasonWords says in a sentence why a guess was made, for the person to judge it.
func PlaceReasonWords(r PlaceReason) string {
	side := "on one side"
	if r.TwoSided() {
		side = "on both sides"
	}
	words := fmt.Sprintf("same camera, %d min away, a located photo %s", r.Minutes, side)
	if r.Metres > 0 {
		words += fmt.Sprintf(", the two %d m apart", r.Metres)
	}
	return strings.TrimSpace(words)
}
