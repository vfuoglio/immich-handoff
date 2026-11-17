package hub

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// A rule of kind stack (#197) stacks the person's photos that belong together: a RAW and its JPEG, an edit and its original, a burst.
// It looks with a key of their own, offers each group, and only ever makes stacks: it never changes or deletes a photo, and never
// touches a photo that is already in a stack. See docs/design/stacks-design.md.

// stackKeyName is what the key is called in the person's Immich, where they can see it and delete it.
const stackKeyName = "handoff: stacks"

// StackPerPass is the most photos one look at one rule takes up; the rest wait for the next look.
var StackPerPass = 500

// StackUndoWindow is how long after the app made a stack the person can take it apart again.
var StackUndoWindow = 15 * time.Minute

// stackFolders is the most folders a later look reads again to find the partners of the photos that came since (a RAW can arrive
// a while after its JPEG).
const stackFolders = 50

var (
	ErrNoStackKey    = errors.New("the key the app uses for stacking is gone: make the rule again")
	ErrStackTwice    = errors.New("you already have a stacking rule for that")
	ErrBadSignals    = errors.New("choose at least one thing to stack: a RAW and its JPEG, an edit and its original, or a burst")
	ErrBadStackCover = errors.New("the cover is the JPEG, the RAW or the edit")
	ErrStackNothing  = errors.New("there is nothing to take apart: the app made no stack in the last 15 minutes")
	ErrBurstOnly     = errors.New("a rule that only stacks bursts always asks first, because a burst is a guess")
)

// StackRuleParams is what a person fills in to make a stacking rule.
type StackRuleParams struct {
	Folder string // optional
	Pair   bool
	Edit   bool
	Burst  bool
	Cover  string
}

func (p StackRuleParams) signals() []string {
	var out []string
	if p.Pair {
		out = append(out, SignalPair)
	}
	if p.Edit {
		out = append(out, SignalEdit)
	}
	if p.Burst {
		out = append(out, SignalBurst)
	}
	return out
}

// clean checks the parameters and returns them with the folder written as a folder.
func (p StackRuleParams) clean() (StackRuleParams, error) {
	if !p.Pair && !p.Edit && !p.Burst {
		return p, ErrBadSignals
	}
	switch p.Cover {
	case "":
		p.Cover = CoverJPEG
	case CoverJPEG, CoverRAW, CoverEdited:
	default:
		return p, ErrBadStackCover
	}
	if strings.TrimSpace(p.Folder) != "" {
		s, err := FolderSpec{Prefix: p.Folder}.Clean()
		if err != nil {
			return p, err
		}
		p.Folder = s.Prefix
	} else {
		p.Folder = ""
	}
	return p, nil
}

// StackSpecOf is what a rule of kind stack groups.
func StackSpecOf(r store.Rule) StackSpec {
	s := StackSpec{Prefix: r.Cond.Folder, Cover: r.Cond.Cover}
	for _, sig := range r.Cond.Signals {
		switch sig {
		case SignalPair:
			s.Pair = true
		case SignalEdit:
			s.Edit = true
		case SignalBurst:
			s.Burst = true
		}
	}
	return s
}

// stackWhy is what a photo of a group carries in a rule's rows: the signal and the cover, so that the group can be put together again.
func stackWhy(signal string, cover uuid.UUID) string { return signal + ":" + cover.String() }

func splitStackWhy(why string) (signal, cover string) {
	i := strings.Index(why, ":")
	if i < 0 {
		return "", ""
	}
	return why[:i], why[i+1:]
}

// onlyBursts says whether a rule can stack nothing but bursts: it may never go on by itself.
func onlyBursts(c store.RuleCond) bool {
	for _, s := range c.Signals {
		if s != SignalBurst {
			return false
		}
	}
	return len(c.Signals) > 0
}

// ensureStackKey makes the person's key for stacking the first time (with their own session) and returns a client that acts with it.
func (h *Hub) ensureStackKey(ctx context.Context, sess *immich.API, user immich.User) (*immich.API, error) {
	if !h.SelfService() {
		return nil, ErrNotJoined
	}
	k, ok, err := h.Store.StackKey(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	if ok {
		return h.Connect(k.Key)
	}
	id, secret, err := sess.CreateAPIKey(ctx, stackKeyName, immich.StackPermissions)
	if err != nil {
		return nil, fmt.Errorf("could not make the app a key for stacking: %w", err)
	}
	if err := h.Store.SetStackKey(ctx, user.ID, store.StackKey{KeyID: id, Key: secret}); err != nil {
		_ = sess.DeleteAPIKey(ctx, id)
		return nil, err
	}
	return h.Connect(secret)
}

func (h *Hub) stackAPI(ctx context.Context, user uuid.UUID) (*immich.API, error) {
	if h.Store == nil {
		return nil, ErrNotJoined
	}
	k, ok, err := h.Store.StackKey(ctx, user)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNoStackKey
	}
	return h.Connect(k.Key)
}

// dropStackKeyIfUnused deletes the person's key (in their Immich, with their session) when no rule of theirs stacks any more.
func (h *Hub) dropStackKeyIfUnused(ctx context.Context, sess *immich.API, user uuid.UUID) {
	rules, err := h.Store.RulesOf(ctx, user)
	if err != nil {
		return
	}
	for _, r := range rules {
		if r.Kind == store.RuleKindStack {
			return
		}
	}
	k, ok, err := h.Store.StackKey(ctx, user)
	if err != nil || !ok {
		return
	}
	if sess != nil && k.KeyID != uuid.Nil {
		if err := sess.DeleteAPIKey(ctx, k.KeyID); err != nil && !immich.IsNotFound(err) {
			h.Log.Debug("could not delete the key for stacking", "err", err)
		}
	}
	_ = h.Store.DeleteStackKey(ctx, user)
}

// StackCounts says how many groups of each signal a spec finds among the person's photos now, with their own session, for the form
// to show before the rule is made. It changes nothing.
func (h *Hub) StackCounts(ctx context.Context, sess *immich.API, user immich.User, p StackRuleParams) (map[string]int, int, error) {
	p, err := p.clean()
	if err != nil {
		return nil, 0, err
	}
	photos, err := h.StackPhotos(ctx, sess, user.ID, p.Folder, time.Time{})
	if err != nil {
		return nil, 0, err
	}
	out := map[string]int{}
	for _, g := range PlanStacks(StackSpec{Prefix: p.Folder, Pair: p.Pair, Edit: p.Edit, Burst: p.Burst, Cover: p.Cover}, photos) {
		out[g.Signal]++
	}
	return out, len(photos), nil
}

// FollowStacking makes a rule that stacks. What is in the library now is offered too; nothing is stacked until the person says so.
func (h *Hub) FollowStacking(ctx context.Context, sess *immich.API, user immich.User, p StackRuleParams) (store.Rule, error) {
	if !h.SelfService() {
		return store.Rule{}, ErrNotJoined
	}
	p, err := p.clean()
	if err != nil {
		return store.Rule{}, err
	}
	rules, err := h.Store.RulesOf(ctx, user.ID)
	if err != nil {
		return store.Rule{}, err
	}
	if len(rules) >= maxRules {
		return store.Rule{}, ErrTooManyRules
	}
	sigs := strings.Join(p.signals(), ",")
	for _, r := range rules {
		if r.Kind == store.RuleKindStack && r.Cond.Folder == p.Folder && strings.Join(r.Cond.Signals, ",") == sigs && r.Cond.Cover == p.Cover {
			return store.Rule{}, ErrStackTwice
		}
	}
	api, err := h.ensureStackKey(ctx, sess, user)
	if err != nil {
		return store.Rule{}, err
	}
	if _, err := h.StackPhotos(ctx, api, user.ID, p.Folder, time.Now().Add(time.Hour)); err != nil { // asks for nothing: it shows that the key can search
		h.dropStackKeyIfUnused(ctx, sess, user.ID)
		return store.Rule{}, fmt.Errorf("the app could not read your photos with its key: %v", trimNet(err))
	}
	rule, err := h.Store.AddRule(ctx, store.Rule{OwnerID: user.ID, Kind: store.RuleKindStack,
		Cond: store.RuleCond{Folder: p.Folder, Signals: p.signals(), Cover: p.Cover}})
	if err != nil {
		return store.Rule{}, err
	}
	h.event(ctx, "", user.ID, "rule", i18n.Pack("{name} stacks photos that belong together", "name", user.Name))
	return rule, nil
}

// lookStack looks at one rule that stacks: the groups it has not handled become suggestions (a group is its members' rows, each with
// the signal and the cover), or, when the person has let the rule go on by itself, are stacked, but only a pair or an edit whose
// photos all came after the switch. It takes up at most StackPerPass photos, and goes on with the rest at the next look. It returns
// how many groups are new.
func (h *Hub) lookStack(ctx context.Context, r store.Rule) (int, error) {
	fail := func(problem string, err error) (int, error) {
		h.Store.MarkRuleLook(ctx, r.ID, h.now(), problem)
		return 0, err
	}
	api, err := h.stackAPI(ctx, r.OwnerID)
	if err != nil {
		return fail(ErrNoStackKey.Error(), err)
	}
	me, err := api.Me(ctx)
	if err != nil {
		return fail("your Immich does not accept the app's key", err)
	}
	started := h.now()
	spec := StackSpecOf(r)
	var after time.Time
	if !r.CaughtUp.IsZero() {
		after = r.CaughtUp.Add(-folderRecheck)
	}
	photos, err := h.StackPhotos(ctx, api, me.ID, spec.Prefix, after)
	if err != nil {
		return fail(trimNet(err), err)
	}
	if !after.IsZero() && len(photos) > 0 {
		// what came since has partners that came before (the RAW after its JPEG): read the folders it is in again
		have := map[uuid.UUID]bool{}
		dirs := map[string]bool{}
		for _, p := range photos {
			have[p.ID] = true
			dirs[path.Dir(p.Path)] = true
		}
		names := make([]string, 0, len(dirs))
		for d := range dirs {
			names = append(names, d)
		}
		sort.Strings(names)
		if len(names) > stackFolders {
			names = names[:stackFolders]
		}
		for _, d := range names {
			more, err := h.StackPhotos(ctx, api, me.ID, d, time.Time{})
			if err != nil {
				return fail(trimNet(err), err)
			}
			for _, p := range more {
				if !have[p.ID] {
					have[p.ID] = true
					photos = append(photos, p)
				}
			}
		}
	}
	known, err := h.Store.RulePhotoStates(ctx, r.ID)
	if err != nil {
		return 0, err
	}
	var fresh []StackGroup
	for _, g := range PlanStacks(spec, photos) {
		seen := false
		for _, m := range g.Members {
			seen = seen || known[m.ID.String()] != ""
		}
		if !seen {
			fresh = append(fresh, g)
		}
	}
	more := false
	taken, budget := fresh[:0:0], StackPerPass
	for _, g := range fresh {
		if len(g.Members) > budget && len(taken) > 0 {
			more = true
			break
		}
		budget -= len(g.Members)
		taken = append(taken, g)
	}
	fresh = taken
	waiting, _ := h.Store.RulePhotos(ctx, r.ID, store.RuleSuggested)
	var auto []StackGroup
	suggested := 0
	for _, g := range fresh {
		why := stackWhy(g.Signal, g.Members[0].ID)
		goAuto := r.Mode == store.RuleAuto && g.AutoOK()
		for _, m := range g.Members {
			goAuto = goAuto && !m.Created.Before(r.AutoSince)
		}
		for _, m := range g.Members {
			if err := h.Store.PutRulePhoto(ctx, r.ID, m.ID.String(), store.RuleSuggested, why); err != nil {
				return 0, err
			}
		}
		if goAuto {
			auto = append(auto, g)
		} else {
			suggested++
		}
	}
	if len(auto) > 0 {
		var ids []string
		for _, g := range auto {
			for _, m := range g.Members {
				ids = append(ids, m.ID.String())
			}
		}
		if err := h.stackForRule(ctx, r, ids); err != nil {
			h.Store.MarkRuleLook(ctx, r.ID, h.now(), trimNet(err)) // they stay suggestions: nothing is lost
			return suggested, err
		}
		h.event(ctx, "", r.OwnerID, "stack", i18n.PackN("{n} group of photos stacked on its own", "{n} groups of photos stacked on their own", len(auto)))
	}
	if !more {
		h.Store.MarkRuleCaughtUp(ctx, r.ID, started)
	}
	h.Store.MarkRuleLook(ctx, r.ID, h.now(), "")
	if suggested > 0 && len(waiting) == 0 { // told once for a batch, when it begins
		h.event(ctx, "", r.OwnerID, "suggestion", i18n.PackN("{n} group of photos can be stacked. Look at it on the Overview.", "{n} groups of photos can be stacked. Look at them on the Overview.", suggested))
	}
	return suggested + len(auto), nil
}

// stackForRule stacks the groups the photos belong to (each group is put together again from the rows of the rule): the cover first.
// A group that was stacked is marked sent; a group Immich refuses stays a suggestion, and the first refusal is returned after the others
// have been tried.
func (h *Hub) stackForRule(ctx context.Context, r store.Rule, assets []string) error {
	api, err := h.stackAPI(ctx, r.OwnerID)
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
	groups := map[string][]string{}
	var order []string
	for _, row := range rows {
		if !chosen[row.AssetID] {
			continue
		}
		if _, ok := groups[row.Why]; !ok {
			order = append(order, row.Why)
		}
		groups[row.Why] = append(groups[row.Why], row.AssetID)
	}
	sort.Strings(order)
	var first error
	for _, why := range order {
		_, cover := splitStackWhy(why)
		ids := groups[why]
		sort.SliceStable(ids, func(i, j int) bool { return ids[i] == cover && ids[j] != cover })
		var uu []uuid.UUID
		for _, id := range ids {
			if u, err := uuid.Parse(id); err == nil {
				uu = append(uu, u)
			}
		}
		if len(uu) < 2 {
			continue
		}
		st, err := api.CreateStack(ctx, uu)
		if err != nil {
			if first == nil {
				first = fmt.Errorf("could not stack %d photos: %w", len(uu), err)
			}
			continue
		}
		h.Store.AddStackMade(ctx, store.StackMade{StackID: st.Id.String(), RuleID: r.ID, UserID: r.OwnerID, Assets: ids, At: h.now()})
		h.Store.SetRulePhotos(ctx, r.ID, ids, store.RuleSuggested, store.RuleSent)
	}
	return first
}

// UndoStacks takes apart the stacks the app made for a rule in the last 15 minutes, and only those, and only if each is still as it was
// made (a stack the person has changed since is left alone). The photos stay; they are not offered again. It returns how many stacks it
// took apart.
func (h *Hub) UndoStacks(ctx context.Context, user immich.User, ruleID string) (int, error) {
	r, err := h.ruleOf(ctx, user.ID, ruleID)
	if err != nil {
		return 0, err
	}
	if r.Kind != store.RuleKindStack {
		return 0, ErrNoRule
	}
	made, err := h.Store.StacksMadeSince(ctx, r.ID, h.now().Add(-StackUndoWindow))
	if err != nil {
		return 0, err
	}
	if len(made) == 0 {
		return 0, ErrStackNothing
	}
	api, err := h.stackAPI(ctx, r.OwnerID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range made {
		id, err := uuid.Parse(m.StackID)
		if err != nil {
			continue
		}
		st, err := api.Stack(ctx, id)
		if err != nil {
			if immich.IsNotFound(err) || immich.IsForbidden(err) { // it is gone already
				h.Store.DeleteStackMade(ctx, m.StackID)
			}
			continue
		}
		have := map[string]bool{}
		for _, a := range st.Assets {
			have[a.Id.String()] = true
		}
		same := len(have) == len(m.Assets)
		for _, a := range m.Assets {
			same = same && have[a]
		}
		if !same { // the person changed it: it is theirs now
			h.Store.DeleteStackMade(ctx, m.StackID)
			continue
		}
		if err := api.DeleteStack(ctx, id); err != nil && !immich.IsNotFound(err) {
			return n, fmt.Errorf("could not take a stack apart: %w", err)
		}
		h.Store.DeleteStackMade(ctx, m.StackID)
		h.Store.SetRulePhotos(ctx, r.ID, m.Assets, store.RuleSent, store.RuleDeclined)
		n++
	}
	if n > 0 {
		h.event(ctx, "", user.ID, "stack", i18n.PackN("{n} stack taken apart again", "{n} stacks taken apart again", n))
	}
	return n, nil
}

// StackUndoable says how many stacks of a rule the person can still take apart.
func (h *Hub) StackUndoable(ctx context.Context, user uuid.UUID, ruleID string) int {
	r, err := h.ruleOf(ctx, user, ruleID)
	if err != nil || r.Kind != store.RuleKindStack {
		return 0
	}
	made, err := h.Store.StacksMadeSince(ctx, r.ID, h.now().Add(-StackUndoWindow))
	if err != nil {
		return 0
	}
	return len(made)
}

// StackSentence says what a stacking rule does, in a few words.
func StackSentence(c store.RuleCond) string {
	var what []string
	for _, s := range c.Signals {
		switch s {
		case SignalPair:
			what = append(what, "a RAW with its JPEG")
		case SignalEdit:
			what = append(what, "an edit with its original")
		case SignalBurst:
			what = append(what, "a burst")
		}
	}
	out := "stack " + strings.Join(what, ", ")
	if c.Folder != "" {
		out += " in " + c.Folder
	}
	return out
}
