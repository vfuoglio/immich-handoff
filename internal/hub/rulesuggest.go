package hub

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Answering what a rule suggests (docs/design/rules-design.md). The photos a rule found wait in its open batch. "Send" tags them with the
// service's tag through the sender's own key, which is exactly what the person would have done by hand, so everything that already
// exists (limits, quiet hours, the daily cap, take-back, the live scene) applies. "Not these" remembers them as declined for the
// rule, which stays. "Always" sends them and counts, so that after a few the person can let the rule go on by itself.

var ErrNoSuggestion = errors.New("there is nothing waiting for that rule")

// alwaysToAuto is how many times the person answers "Always" before automatic sending is offered.
const alwaysToAuto = 3

// Suggestion is a rule with the photos it found and has not been answered about.
type Suggestion struct {
	Rule   store.Rule
	Photos []store.RulePhoto
}

// Suggestions lists the rules of a person that have photos waiting.
func (h *Hub) Suggestions(ctx context.Context, user uuid.UUID) ([]Suggestion, error) {
	if h.Store == nil {
		return nil, nil
	}
	rules, err := h.Store.RulesOf(ctx, user)
	if err != nil {
		return nil, err
	}
	var out []Suggestion
	for _, r := range rules {
		if r.Paused {
			continue
		}
		photos, err := h.Store.RulePhotos(ctx, r.ID, store.RuleSuggested)
		if err != nil {
			return nil, err
		}
		if len(photos) > 0 {
			out = append(out, Suggestion{Rule: r, Photos: photos})
		}
	}
	return out, nil
}

// SuggestionOf is one rule's waiting photos, for its owner.
func (h *Hub) SuggestionOf(ctx context.Context, user uuid.UUID, id string) (Suggestion, error) {
	r, err := h.ruleOf(ctx, user, id)
	if err != nil {
		return Suggestion{}, err
	}
	photos, err := h.Store.RulePhotos(ctx, r.ID, store.RuleSuggested)
	if err != nil {
		return Suggestion{}, err
	}
	return Suggestion{Rule: r, Photos: photos}, nil
}

// Answers to a suggestion.
const (
	AnswerSend    = "send"
	AnswerDecline = "decline"
	AnswerAlways  = "always"
)

// AnswerSuggestion answers the waiting photos of a rule. only, when given, are the photos the answer is about (the review page
// unticks some); the others in the batch are declined when the answer is a yes, so a review settles the whole batch.
func (h *Hub) AnswerSuggestion(ctx context.Context, user immich.User, ruleID, answer string, only []string) error {
	r, err := h.ruleOf(ctx, user.ID, ruleID)
	if err != nil {
		return err
	}
	photos, err := h.Store.RulePhotos(ctx, r.ID, store.RuleSuggested)
	if err != nil {
		return err
	}
	if len(photos) == 0 {
		return ErrNoSuggestion
	}
	waiting := map[string]bool{}
	for _, p := range photos {
		waiting[p.AssetID] = true
	}
	if r.Kind == store.RuleKindStack && only != nil { // a group is answered whole: ticking one of its photos ticks them all
		whys := map[string]bool{}
		for _, id := range only {
			for _, p := range photos {
				if p.AssetID == id {
					whys[p.Why] = true
				}
			}
		}
		only = []string{}
		for _, p := range photos {
			if whys[p.Why] {
				only = append(only, p.AssetID)
			}
		}
	}
	var yes, no []string
	switch answer {
	case AnswerDecline:
		for _, p := range photos {
			no = append(no, p.AssetID)
		}
	case AnswerSend, AnswerAlways:
		if only == nil {
			for _, p := range photos {
				yes = append(yes, p.AssetID)
			}
		} else {
			chosen := map[string]bool{}
			for _, id := range only {
				if waiting[id] {
					chosen[id] = true
				}
			}
			for _, p := range photos {
				if chosen[p.AssetID] {
					yes = append(yes, p.AssetID)
				} else {
					no = append(no, p.AssetID)
				}
			}
		}
	default:
		return ErrNoSuggestion
	}
	if len(yes) > 0 {
		// a photo another rule of this person's for the same service has sent is not sent twice
		var already map[string]bool
		if r.Kind == store.RuleKindSend {
			already, _ = h.Store.RuleSentAssets(ctx, r.OwnerID, r.Service)
		}
		var fresh []string
		for _, id := range yes {
			if !already[id] {
				fresh = append(fresh, id)
			}
		}
		if len(fresh) > 0 {
			if err := h.sendForRule(ctx, r, fresh); err != nil {
				return err
			}
		}
		h.Store.SetRulePhotos(ctx, r.ID, yes, store.RuleSuggested, store.RuleSent)
		var accepted []store.RulePhoto
		for _, p := range photos {
			for _, id := range yes {
				if p.AssetID == id {
					accepted = append(accepted, p)
				}
			}
		}
		var rejected []store.RulePhoto
		for _, p := range photos {
			for _, id := range no {
				if p.AssetID == id {
					rejected = append(rejected, p)
				}
			}
		}
		h.learnPet(ctx, r, accepted, rejected) // a pet rule widens its reach and takes the accepted photos as examples
	} else if len(no) > 0 && r.Cond.Pet() {
		var rejected []store.RulePhoto
		for _, p := range photos {
			for _, id := range no {
				if p.AssetID == id {
					rejected = append(rejected, p)
				}
			}
		}
		h.learnPet(ctx, r, nil, rejected)
	}
	if len(no) > 0 {
		h.Store.SetRulePhotos(ctx, r.ID, no, store.RuleSuggested, store.RuleDeclined)
	}
	if answer == AnswerAlways && len(yes) > 0 {
		h.Store.AddRuleAlways(ctx, r.ID)
	}
	if r.Kind == store.RuleKindLibrary {
		album := r.Cond.Album.Name
		switch {
		case len(yes) > 0:
			h.event(ctx, "", user.ID, "library", i18n.PackN("{n} photo added to your library from {album}", "{n} photos added to your library from {album}", len(yes), "album", album))
		case len(no) > 0:
			h.event(ctx, "", user.ID, "rule", i18n.PackN("{n} photo of the album {album} left out of your library", "{n} photos of the album {album} left out of your library", len(no), "album", album))
		}
		return nil
	}
	if r.Kind == store.RuleKindStack {
		switch {
		case len(yes) > 0:
			h.event(ctx, "", user.ID, "stack", i18n.PackN("{n} photo stacked with the ones it belongs with", "{n} photos stacked with the ones they belong with", len(yes)))
		case len(no) > 0:
			h.event(ctx, "", user.ID, "rule", i18n.PackN("{n} photo left out of stacking", "{n} photos left out of stacking", len(no)))
		}
		return nil
	}
	if r.Kind == store.RuleKindFolder {
		switch {
		case len(yes) > 0:
			h.event(ctx, "", user.ID, "folder", i18n.PackN("{n} photo put in albums made from your folders", "{n} photos put in albums made from your folders", len(yes)))
		case len(no) > 0:
			h.event(ctx, "", user.ID, "rule", i18n.PackN("{n} photo left out of the albums made from your folders", "{n} photos left out of the albums made from your folders", len(no)))
		}
		return nil
	}
	switch {
	case len(yes) > 0:
		h.event(ctx, r.Service, user.ID, "rule", i18n.PackN("{n} photo sent to {service} by a rule: {rule}", "{n} photos sent to {service} by a rule: {rule}", len(yes), "service", r.Service, "rule", RuleSentence(r.Cond)))
	case len(no) > 0:
		h.event(ctx, r.Service, user.ID, "rule", i18n.PackN("{n} suggested photo left out of {service}: {rule}", "{n} suggested photos left out of {service}: {rule}", len(no), "service", r.Service, "rule", RuleSentence(r.Cond)))
	}
	return nil
}

// sendForRule tags photos with the service's tag, in the sender's own account, so that the normal hand-over takes them.
func (h *Hub) sendForRule(ctx context.Context, r store.Rule, assets []string) error {
	if r.Kind == store.RuleKindLibrary { // a followed album: the photos are copied into the person's own library
		return h.copyForRule(ctx, r, assets)
	}
	if r.Kind == store.RuleKindFolder { // the photos are put in the albums they were offered for
		return h.folderForRule(ctx, r, assets)
	}
	if r.Kind == store.RuleKindStack { // the groups the photos belong to are stacked, the cover first
		return h.stackForRule(ctx, r, assets)
	}
	return h.tagFor(ctx, r.OwnerID, r.Service, assets, true, "a rule sent photos")
}

// tagFor puts a service's tag on photos (or takes it off), with the key the person made when they joined, so that the
// normal hand-over takes them: the pass, the limits, the quiet hours and taking back all apply as for a tag put on in Immich.
func (h *Hub) tagFor(ctx context.Context, user uuid.UUID, service string, assets []string, on bool, why string) error {
	var tag string
	for _, svc := range h.Services() {
		if svc.Label() == service {
			tag = svc.Tag
		}
	}
	if tag == "" {
		return fmt.Errorf("the service %s is not here any more", service)
	}
	link, err := h.senderLink(ctx, user, service)
	if err != nil {
		return err
	}
	api, err := h.Connect(link.Key)
	if err != nil {
		return err
	}
	tagID, err := api.UpsertTag(ctx, tag)
	if err != nil {
		return fmt.Errorf("could not tag the photos: %w", err)
	}
	var ids []openapi_types.UUID
	for _, a := range assets {
		if id, err := uuid.Parse(a); err == nil {
			ids = append(ids, id)
		}
	}
	if !on {
		return api.UntagAssets(ctx, tagID, ids)
	}
	if err := api.TagAssets(ctx, tagID, ids...); err != nil {
		return fmt.Errorf("could not tag the photos: %w", err)
	}
	if h.Wake != nil {
		h.Wake(why + ": " + user.String())
	}
	return nil
}

// RulePhotoOf checks that a photo is one a rule of this person has handled, and returns its id, for showing its thumbnail.
func (h *Hub) RulePhotoOf(ctx context.Context, user uuid.UUID, ruleID, asset string) (uuid.UUID, error) {
	r, err := h.ruleOf(ctx, user, ruleID)
	if err != nil {
		return uuid.Nil, err
	}
	states, err := h.Store.RulePhotoStates(ctx, r.ID)
	if err != nil || states[asset] == "" {
		return uuid.Nil, ErrNoSuggestion
	}
	return uuid.Parse(asset)
}

var (
	ErrAutoTooSoon = errors.New("a rule can send by itself after you have answered Always to it three times")
	ErrAutoPhrase  = errors.New("a rule with a phrase always asks first, because a look-alike is never certain")
	ErrRuleNotAuto = errors.New("that rule is not sending by itself")
)

// SetRuleAuto lets a rule send what it finds by itself, or takes that back. Only photos that reach Immich after the switch are
// ever sent without asking; what was already waiting stays a suggestion.
func (h *Hub) SetRuleAuto(ctx context.Context, user immich.User, id string, on bool) error {
	r, err := h.ruleOf(ctx, user.ID, id)
	if err != nil {
		return err
	}
	if !on {
		if r.Mode != store.RuleAuto {
			return nil
		}
		if err := h.Store.SetRuleMode(ctx, r.ID, store.RuleSuggest, h.now()); err != nil {
			return err
		}
		if r.Kind == store.RuleKindLibrary {
			h.event(ctx, "", user.ID, "rule", i18n.Pack("{name} made the album {album} ask first again", "name", user.Name, "album", r.Cond.Album.Name))
			return nil
		}
		if r.Kind == store.RuleKindFolder {
			h.event(ctx, "", user.ID, "rule", i18n.Pack("{name} made the albums from {folder} ask first again", "name", user.Name, "folder", r.Cond.Folder))
			return nil
		}
		if r.Kind == store.RuleKindStack {
			h.event(ctx, "", user.ID, "rule", i18n.Pack("{name} made stacking ask first again", "name", user.Name))
			return nil
		}
		h.event(ctx, r.Service, user.ID, "rule", i18n.Pack("{name} made a rule for {service} ask first again: {rule}", "name", user.Name, "service", r.Service, "rule", RuleSentence(r.Cond)))
		return nil
	}
	if r.Cond.Phrase != "" {
		return ErrAutoPhrase
	}
	if r.Cond.Pet() {
		return ErrAutoPet
	}
	if r.Kind == store.RuleKindStack && onlyBursts(r.Cond) {
		return ErrBurstOnly
	}
	if r.Kind != store.RuleKindSend && r.Always < alwaysToAuto {
		return ErrAutoTooSoon // a rule that sends may be switched on at once; the others wait for three Always
	}
	if err := h.Store.SetRuleMode(ctx, r.ID, store.RuleAuto, h.now()); err != nil {
		return err
	}
	if r.Kind == store.RuleKindLibrary {
		h.event(ctx, "", user.ID, "rule", i18n.Pack("{name} let the album {album} add photos to the library by itself", "name", user.Name, "album", r.Cond.Album.Name))
		return nil
	}
	if r.Kind == store.RuleKindFolder {
		h.event(ctx, "", user.ID, "rule", i18n.Pack("{name} let the albums from {folder} be made by themselves", "name", user.Name, "folder", r.Cond.Folder))
		return nil
	}
	if r.Kind == store.RuleKindStack {
		h.event(ctx, "", user.ID, "rule", i18n.Pack("{name} let stacking go on by itself for pairs and edits", "name", user.Name))
		return nil
	}
	h.event(ctx, r.Service, user.ID, "rule", i18n.Pack("{name} let a rule for {service} send by itself: {rule}", "name", user.Name, "service", r.Service, "rule", RuleSentence(r.Cond)))
	return nil
}

// CanAuto says whether a rule may be let go on by itself: it is not automatic yet, it has no phrase, and either it sends photos
// (the person may switch it on at once, naming who they go to) or it has been answered Always often enough.
func CanAuto(r store.Rule) bool {
	return r.Mode != store.RuleAuto && (r.Kind == store.RuleKindSend || r.Always >= alwaysToAuto) && !r.Cond.NeverAuto() && !(r.Kind == store.RuleKindStack && onlyBursts(r.Cond))
}
