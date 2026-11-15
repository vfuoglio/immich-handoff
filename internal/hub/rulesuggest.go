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

// Answering what a rule suggests (docs/rules-design.md). The photos a rule found wait in its open batch. "Send" tags them with the
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
		if err := h.sendForRule(ctx, r, yes); err != nil {
			return err
		}
		h.Store.SetRulePhotos(ctx, r.ID, yes, store.RuleSuggested, store.RuleSent)
	}
	if len(no) > 0 {
		h.Store.SetRulePhotos(ctx, r.ID, no, store.RuleSuggested, store.RuleDeclined)
	}
	if answer == AnswerAlways && len(yes) > 0 {
		h.Store.AddRuleAlways(ctx, r.ID)
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
	var tag string
	for _, svc := range h.Services() {
		if svc.Label() == r.Service {
			tag = svc.Tag
		}
	}
	if tag == "" {
		return fmt.Errorf("the service %s is not here any more", r.Service)
	}
	link, err := h.senderLink(ctx, r.OwnerID, r.Service)
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
	if err := api.TagAssets(ctx, tagID, ids...); err != nil {
		return fmt.Errorf("could not tag the photos: %w", err)
	}
	if h.Wake != nil {
		h.Wake("a rule sent photos: " + r.OwnerID.String())
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
