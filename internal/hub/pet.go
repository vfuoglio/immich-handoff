package hub

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/pets"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// A pet rule ("photos like these"): Immich cannot name pets, so the person picks a few photos of the animal and the rule offers the
// photos Immich's own smart search ranks closest to them. Immich gives no score, only the order, so how far down the list a photo may
// be is learned from the person's answers. It only ever suggests (docs/contributing/design/pairing-design.md, #227).
const (
	petMinExamples  = 3
	petMaxExamples  = 8  // what a person picks to begin with
	petMostExamples = 12 // what the examples may grow to as the person answers Send
	petStartRank    = 10 // how far down the list a photo may be, before any answer
	petMostRank     = 300
)

var (
	ErrBadExamples = errors.New("a pet rule needs 3 to 8 photos of your own as examples")
	ErrPetAlone    = errors.New("a rule for a pet looks only at its example photos: make a separate rule for people, places or dates")
	ErrAutoPet     = errors.New("a rule for a pet always asks first, because a look-alike is never certain")
)

var rankRe = regexp.MustCompile(`\(rank (\d+)\)`)

// petRank is the rank written in why a photo was offered (0 when there is none).
func petRank(why string) int {
	m := rankRe.FindStringSubmatch(why)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func petWhy(rank int) string { return fmt.Sprintf("looks like your example photos (rank %d)", rank) }

// petSearch finds the photos of the person's library that rank within the rule's rank of any example, newest rule window applied
// afterwards (the ranking is over the whole library, as it was measured). It returns them nearest first, with the best rank each had.
func (h *Hub) petSearch(ctx context.Context, api *immich.API, c store.RuleCond, after time.Time) ([]immich.AssetResponseDto, map[uuid.UUID]string, error) {
	m := c.Rank
	if m < petStartRank {
		m = petStartRank
	}
	if m > petMostRank {
		m = petMostRank
	}
	isExample := map[uuid.UUID]bool{}
	for _, e := range c.Examples {
		if id, err := uuid.Parse(e); err == nil {
			isExample[id] = true
		}
	}
	best := map[uuid.UUID]int{}
	byID := map[uuid.UUID]immich.AssetResponseDto{}
	for e := range isExample {
		items, err := api.SimilarTo(ctx, e, m+len(isExample)+1)
		if err != nil {
			return nil, nil, err
		}
		rank := 0
		for _, it := range items {
			if isExample[it.Id] {
				continue
			}
			rank++
			if rank > m {
				break
			}
			if !after.IsZero() && it.CreatedAt.Before(after) {
				continue
			}
			if r, ok := best[it.Id]; !ok || rank < r {
				best[it.Id] = rank
			}
			byID[it.Id] = it
		}
	}
	out := make([]immich.AssetResponseDto, 0, len(byID))
	for id, a := range byID {
		_ = id
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if best[out[i].Id] != best[out[j].Id] {
			return best[out[i].Id] < best[out[j].Id]
		}
		return out[i].Id.String() < out[j].Id.String()
	})
	whys := map[uuid.UUID]string{}
	for id, r := range best {
		whys[id] = petWhy(r)
	}
	return out, whys, nil
}

// learnPet is what a pet rule learns from the answers. Without the sidecar: how far down the list the worst accepted photo was widens
// the rank (1.5 times it, plus one, never smaller than it was). With it: the cutoff moves to just below the worst accepted match, or,
// when everything was turned down, to just above the best match that was. Each accepted photo becomes one more example, up to a limit.
func (h *Hub) learnPet(ctx context.Context, r store.Rule, accepted, rejected []store.RulePhoto) {
	if !r.Cond.Pet() {
		return
	}
	c := r.Cond
	worst := 0
	have := map[string]bool{}
	for _, e := range c.Examples {
		have[e] = true
	}
	for _, p := range accepted {
		if n := petRank(p.Why); n > worst {
			worst = n
		}
		if len(c.Examples) < petMostExamples && !have[p.AssetID] {
			c.Examples = append(c.Examples, p.AssetID)
			have[p.AssetID] = true
		}
	}
	if c.Rank < petStartRank {
		c.Rank = petStartRank
	}
	if w := int(math.Ceil(1.5*float64(worst))) + 1; worst > 0 && w > c.Rank {
		c.Rank = w
	}
	if c.Rank > petMostRank {
		c.Rank = petMostRank
	}
	cur := c.Cutoff
	if cur == 0 {
		cur = petDefaultCutoff
	}
	lowest, highest := 2.0, -2.0
	for _, p := range accepted {
		if m, ok := petMatch(p.Why); ok && m < lowest {
			lowest = m
		}
	}
	for _, p := range rejected {
		if m, ok := petMatch(p.Why); ok && m > highest {
			highest = m
		}
	}
	switch {
	case lowest < 2:
		c.Cutoff = math.Max(petCutoffFloor, math.Min(cur, lowest-0.02))
	case len(accepted) == 0 && highest > -2:
		c.Cutoff = math.Min(petCutoffCeiling, math.Max(cur, highest+0.02))
	}
	if err := h.Store.SetRuleCond(ctx, r.ID, c); err != nil {
		h.Log.Warn("a pet rule could not learn", "rule", r.ID, "err", err)
	}
	if h.Pets != nil && len(accepted) > 0 {
		h.addPrints(ctx, r, accepted)
	}
}

// ---- the optional sidecar ----

const (
	petDefaultCutoff = 0.45 // how like an example's animal a crop must be, from the calibration in #228
	petCutoffFloor   = 0.30
	petCutoffCeiling = 0.70
	petPassMax       = 30  // photos looked at by the sidecar in one pass (about 370 ms each on two processors)
	petBackMax       = 150 // and in a look back
)

// ErrNoAnimal is a photo picked as an example in which the sidecar found no cat or dog.
var ErrNoAnimal = errors.New("no cat or dog was found in one of the photos: pick photos where the pet is clearly in view")

var matchRe = regexp.MustCompile(`\(match (-?[0-9.]+)\)`)

func petMatch(why string) (float64, bool) {
	m := matchRe.FindStringSubmatch(why)
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(m[1], 64)
	return f, err == nil
}

func petMatchWhy(score float64) string { return fmt.Sprintf("looks like your pet (match %.2f)", score) }

// best is the animal of the photo with the highest score.
func bestAnimal(a []pets.Animal) (pets.Animal, bool) {
	if len(a) == 0 {
		return pets.Animal{}, false
	}
	b := a[0]
	for _, x := range a[1:] {
		if x.Score > b.Score {
			b = x
		}
	}
	return b, true
}

func animalPrint(a pets.Animal) store.PetPrint {
	return store.PetPrint{Kind: a.Kind, Box: a.Box, Vec: a.Print}
}

// examplePrints asks the sidecar for the animal in each example (a preview, read through the person's own session).
func (h *Hub) examplePrints(ctx context.Context, sess *immich.API, ids []uuid.UUID) ([]store.PetPrint, error) {
	var out []store.PetPrint
	for _, id := range ids {
		data, _, err := sess.Thumbnail(ctx, id, true)
		if err != nil {
			return nil, ErrBadExamples
		}
		animals, err := h.Pets.Analyze(ctx, data)
		if err != nil {
			return nil, err
		}
		a, ok := bestAnimal(animals)
		if !ok {
			return nil, ErrNoAnimal
		}
		pr := animalPrint(a)
		pr.Asset = id.String()
		out = append(out, pr)
	}
	return out, nil
}

// addPrints takes the animal of each accepted photo as one more example (the one most like the examples it has).
func (h *Hub) addPrints(ctx context.Context, r store.Rule, accepted []store.RulePhoto) {
	prints, err := h.Store.PetPrints(ctx, r.ID)
	if err != nil || len(prints) == 0 {
		return
	}
	link, err := h.senderLink(ctx, r.OwnerID, r.Service)
	if err != nil {
		return
	}
	api, err := h.Connect(link.Key)
	if err != nil {
		return
	}
	for _, p := range accepted {
		if len(prints) >= petMostExamples {
			return
		}
		id, err := uuid.Parse(p.AssetID)
		if err != nil {
			continue
		}
		data, _, err := api.Thumbnail(ctx, id, true)
		if err != nil {
			continue
		}
		animals, err := h.Pets.Analyze(ctx, data)
		if err != nil {
			return // down: it learns nothing this time
		}
		var best pets.Animal
		top := -2.0
		for _, a := range animals {
			if s := nearest(a.Print, prints); s > top {
				best, top = a, s
			}
		}
		if top < -1 {
			continue
		}
		pr := animalPrint(best)
		pr.Asset = p.AssetID
		if h.Store.PutPetPrint(ctx, r.ID, pr) == nil {
			prints = append(prints, pr)
		}
	}
}

// nearest is the highest cosine of a fingerprint to any of the examples'.
func nearest(v []float32, prints []store.PetPrint) float64 {
	top := -2.0
	for _, p := range prints {
		if s := pets.Cosine(v, p.Vec); s > top {
			top = s
		}
	}
	return top
}

func (h *Hub) setPetNote(rule, note string) { h.petNotes.Store(rule, note) }
func (h *Hub) takePetNote(rule string) string {
	if v, ok := h.petNotes.LoadAndDelete(rule); ok {
		return v.(string)
	}
	return ""
}

// findPet finds the photos a pet rule matches: with the sidecar on, crops compared to the examples' animals; otherwise (or when the
// sidecar is down, which it says) Immich's own look-alike search.
func (h *Hub) findPet(ctx context.Context, api *immich.API, me uuid.UUID, r store.Rule, after time.Time, back bool) ([]immich.AssetResponseDto, map[uuid.UUID]string, error) {
	h.petNotes.Delete(r.ID)
	if h.Pets != nil {
		if prints, err := h.Store.PetPrints(ctx, r.ID); err == nil && len(prints) > 0 {
			err := h.Pets.Ping(ctx)
			if err == nil {
				var found []immich.AssetResponseDto
				var whys map[uuid.UUID]string
				found, whys, err = h.petSidecar(ctx, api, me, r, prints, after, back)
				if !errors.Is(err, pets.ErrDown) {
					return found, whys, err
				}
			}
			h.setPetNote(r.ID, "the pet helper is not answering, so this rule looks with Immich's own search for now")
		}
	}
	return h.petSearch(ctx, api, r.Cond, after)
}

// petSidecar looks at the person's photos that have settled and that the rule has not looked at yet, newest first, a few at a time,
// and keeps those with an animal like an example's. A photo that was looked at and does not match is remembered as seen, so that it is
// not sent to the sidecar again.
func (h *Hub) petSidecar(ctx context.Context, api *immich.API, me uuid.UUID, r store.Rule, prints []store.PetPrint, after time.Time, back bool) ([]immich.AssetResponseDto, map[uuid.UUID]string, error) {
	var all []immich.AssetResponseDto
	var err error
	max := petPassMax
	if back {
		all, err = api.Recent(ctx, 500)
		max = petBackMax
	} else {
		all, err = api.RuleSearch(ctx, immich.RuleFilter{CreatedAfter: after})
	}
	if err != nil {
		return nil, nil, err
	}
	known, err := h.Store.RulePhotoStates(ctx, r.ID)
	if err != nil {
		return nil, nil, err
	}
	var todo []immich.AssetResponseDto
	for _, a := range all {
		if a.OwnerId != me || a.IsArchived || a.IsTrashed || a.Type != immich.IMAGE || known[a.Id.String()] != "" || !a.HasMetadata || a.Thumbhash == nil || *a.Thumbhash == "" || h.now().Sub(a.CreatedAt) < h.ruleWait(r) {
			continue
		}
		todo = append(todo, a)
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].CreatedAt.After(todo[j].CreatedAt) })
	if len(todo) > max {
		todo = todo[:max]
	}
	cut := r.Cond.Cutoff
	if cut == 0 {
		cut = petDefaultCutoff
	}
	whys := map[uuid.UUID]string{}
	type scored struct {
		a immich.AssetResponseDto
		s float64
	}
	var hits []scored
	for _, a := range todo {
		data, _, err := api.Thumbnail(ctx, a.Id, true)
		if err != nil {
			continue // not readable now (perhaps the key lacks asset.view): it is tried again next time
		}
		animals, err := h.Pets.Analyze(ctx, data)
		if err != nil {
			return nil, nil, err
		}
		top := -2.0
		for _, an := range animals {
			if s := nearest(an.Print, prints); s > top {
				top = s
			}
		}
		if top >= cut {
			hits = append(hits, scored{a, top})
			whys[a.Id] = petMatchWhy(top)
		} else {
			h.Store.PutRulePhoto(ctx, r.ID, a.Id.String(), store.RuleSeen, "")
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].s > hits[j].s })
	out := make([]immich.AssetResponseDto, 0, len(hits))
	for _, x := range hits {
		out = append(out, x.a)
	}
	return out, whys, nil
}
