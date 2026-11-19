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
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// A pet rule ("photos like these"): Immich cannot name pets, so the person picks a few photos of the animal and the rule offers the
// photos Immich's own smart search ranks closest to them. Immich gives no score, only the order, so how far down the list a photo may
// be is learned from the person's answers. It only ever suggests (docs/design/pairing-design.md, #227).
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
func (h *Hub) petSearch(ctx context.Context, api *immich.API, c store.RuleCond, after time.Time) ([]immich.AssetResponseDto, map[uuid.UUID]int, error) {
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
	return out, best, nil
}

// learnPet is what a pet rule learns from a Send: how far down the list the worst accepted photo was widens the rank (1.5 times it,
// plus one, never smaller than it was), and each accepted photo becomes one more example, up to a limit.
func (h *Hub) learnPet(ctx context.Context, r store.Rule, accepted []store.RulePhoto) {
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
	if err := h.Store.SetRuleCond(ctx, r.ID, c); err != nil {
		h.Log.Warn("a pet rule could not learn", "rule", r.ID, "err", err)
	}
}
