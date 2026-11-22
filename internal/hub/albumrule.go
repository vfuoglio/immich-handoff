package hub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// A rule of kind album (#271) keeps one of the person's albums up to date with what a rule finds: the photos that match become
// suggestions (or, once the person has let it, go in by themselves) and are put in the album with a key of the person's own. It only
// adds, unless the person asked for it to take out what no longer matches, and then only photos it put there itself. See
// docs/contributing/design/albums-design.md.

// albumKeyName is what the key is called in the person's Immich, where they can see it and delete it.
const albumKeyName = "handoff: smart albums"

// albumBackfillMost is the most older photos one look back offers for a smart album.
const albumBackfillMost = 500

var (
	ErrNoAlbumKey   = errors.New("the key the app uses for your smart albums is gone: make the rule again")
	ErrAlbumKeyMiss = errors.New("the app could not read your photos with its key")
	ErrAlbumTwice   = errors.New("you already keep that album up to date with a rule of the same kind")
	ErrBadAlbumName = errors.New("name the album, up to 100 characters, or choose one of your own")
	ErrAlbumGone    = errors.New("the album this rule fills is gone: end the rule and make it again")
	ErrStalePhrase  = errors.New("taking out what no longer matches does not work with a phrase, because a phrase only finds the closest 100 photos")
	ErrAlbumNotMine = errors.New("that album is not one of your own albums")
)

// AlbumTarget is the album a smart album rule fills: one of the person's own (Existing), or one to make (New).
type AlbumTarget struct {
	Existing    uuid.UUID
	New         string
	RemoveStale bool
}

// AlbumParams is what a person fills in to make a smart album.
type AlbumParams struct {
	RuleParams
	AlbumTarget
}

func (h *Hub) ensureAlbumKey(ctx context.Context, sess *immich.API, user immich.User, stale bool) (*immich.API, error) {
	if !h.SelfService() {
		return nil, ErrNotJoined
	}
	k, ok, err := h.Store.AlbumKey(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	if ok {
		if stale { // a key made for an album that never takes photos out is given that right too, through the person's own session
			if err := sess.AddKeyPermission(ctx, k.KeyID, "albumAsset.delete"); err != nil {
				return nil, fmt.Errorf("could not allow the app to take photos out of your albums: %w", err)
			}
		}
		return h.Connect(k.Key)
	}
	perms := immich.AlbumPermissions
	if stale {
		perms = append(append(perms[:0:0], perms...), "albumAsset.delete")
	}
	id, secret, err := sess.CreateAPIKey(ctx, albumKeyName, perms)
	if err != nil {
		return nil, fmt.Errorf("could not make the app a key for your smart albums: %w", err)
	}
	if err := h.Store.SetAlbumKey(ctx, user.ID, store.AlbumKey{KeyID: id, Key: secret}); err != nil {
		_ = sess.DeleteAPIKey(ctx, id)
		return nil, err
	}
	return h.Connect(secret)
}

func (h *Hub) albumAPI(ctx context.Context, user uuid.UUID) (*immich.API, error) {
	if h.Store == nil {
		return nil, ErrNotJoined
	}
	k, ok, err := h.Store.AlbumKey(ctx, user)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNoAlbumKey
	}
	return h.Connect(k.Key)
}

// dropAlbumKeyIfUnused deletes the person's key (in their Immich, with their session) when no smart album of theirs is left.
func (h *Hub) dropAlbumKeyIfUnused(ctx context.Context, sess *immich.API, user uuid.UUID) {
	rules, err := h.Store.RulesOf(ctx, user)
	if err != nil {
		return
	}
	for _, r := range rules {
		if r.Kind == store.RuleKindAlbum {
			return
		}
	}
	k, ok, err := h.Store.AlbumKey(ctx, user)
	if err != nil || !ok {
		return
	}
	if sess != nil && k.KeyID != uuid.Nil {
		if err := sess.DeleteAPIKey(ctx, k.KeyID); err != nil && !immich.IsNotFound(err) {
			h.Log.Debug("could not delete the key for smart albums", "err", err)
		}
	}
	_ = h.Store.DeleteAlbumKey(ctx, user)
}

// FollowAlbumRule makes a smart album: a rule that puts what matches in one of the person's albums. Nothing is put in until they say so.
func (h *Hub) FollowAlbumRule(ctx context.Context, sess *immich.API, user immich.User, p AlbumParams) (store.Rule, error) {
	if !h.SelfService() {
		return store.Rule{}, ErrNotJoined
	}
	if list, err := h.Store.RulesOf(ctx, user.ID); err != nil {
		return store.Rule{}, err
	} else if len(list) >= maxRules {
		return store.Rule{}, ErrTooManyRules
	}
	name := strings.TrimSpace(p.New)
	if (p.Existing == uuid.Nil) == (name == "") || utf8.RuneCountInString(name) > 100 || strings.ContainsAny(name, "\r\n\x00") {
		return store.Rule{}, ErrBadAlbumName
	}
	if p.SettleMinutes < 0 || p.SettleMinutes > maxSettleMinutes {
		return store.Rule{}, ErrBadSettle
	}
	cond, _, needAlbums, err := h.ruleCond(ctx, sess, user, p.RuleParams, true)
	if err != nil {
		return store.Rule{}, err
	}
	if p.RemoveStale && cond.Phrase != "" {
		return store.Rule{}, ErrStalePhrase
	}
	cond.RemoveStale = p.RemoveStale
	var target store.RuleRef
	if p.Existing != uuid.Nil {
		albums, err := sess.OwnedAlbums(ctx)
		if err != nil {
			return store.Rule{}, fmt.Errorf("could not list your albums: %w", err)
		}
		for _, a := range albums {
			if uuid.UUID(a.ID) == p.Existing {
				target = store.RuleRef{ID: p.Existing.String(), Name: a.Name}
			}
		}
		if target.ID == "" {
			return store.Rule{}, ErrAlbumNotMine
		}
	}
	rules, err := h.Store.RulesOf(ctx, user.ID)
	if err != nil {
		return store.Rule{}, err
	}
	api, err := h.ensureAlbumKey(ctx, sess, user, p.RemoveStale)
	if err != nil {
		return store.Rule{}, err
	}
	fail := func(err error) (store.Rule, error) {
		h.dropAlbumKeyIfUnused(ctx, sess, user.ID)
		return store.Rule{}, err
	}
	if target.ID == "" { // the album is made now, with the key, so that the rule knows it by its id from the start
		id, _, err := api.FindAlbum(ctx, name)
		if err != nil {
			return fail(fmt.Errorf("could not look for the album %s: %w", name, err))
		}
		if id == nil {
			made, err := api.CreateAlbum(ctx, name)
			if err != nil {
				return fail(fmt.Errorf("could not make the album %s: %w", name, err))
			}
			id = &made
		}
		target = store.RuleRef{ID: uuid.UUID(*id).String(), Name: name}
	}
	for _, r := range rules {
		if r.Kind == store.RuleKindAlbum && r.Dest == target.ID && sameCond(r.Cond, cond) {
			return fail(ErrAlbumTwice)
		}
	}
	if needAlbums { // the rule reads albums to leave out or to look in
		if _, err := api.OwnedAlbums(ctx); err != nil {
			return fail(fmt.Errorf("%w: %v", ErrAlbumKeyMiss, trimNet(err)))
		}
	}
	if _, err := api.RuleSearch(ctx, immich.RuleFilter{CreatedAfter: time.Now().Add(time.Hour)}); err != nil { // asks for nothing: it shows that the key can search
		return fail(fmt.Errorf("%w: %v", ErrAlbumKeyMiss, trimNet(err)))
	}
	r, err := h.Store.AddRule(ctx, store.Rule{OwnerID: user.ID, Kind: store.RuleKindAlbum, Cond: cond, Dest: target.ID, DestAlbum: target.Name, SettleMinutes: p.SettleMinutes})
	if err != nil {
		return fail(err)
	}
	h.event(ctx, "", user.ID, "rule", i18n.Pack("{name} keeps the album {album} up to date: {rule}", "name", user.Name, "album", target.Name, "rule", RuleSentence(cond)))
	return r, nil
}

func sameCond(a, b store.RuleCond) bool {
	return RuleSentence(a) == RuleSentence(b) && a.RemoveStale == b.RemoveStale
}

// lookAlbum looks at one smart album rule: what matches and is not in the album yet becomes suggestions, or, when the person has let
// the rule go on by itself, is put in the album. When the person asked for it, photos it put there that no longer match are taken out.
func (h *Hub) lookAlbum(ctx context.Context, r store.Rule) (int, error) {
	fail := func(problem string, err error) (int, error) {
		h.Store.MarkRuleLook(ctx, r.ID, h.now(), problem)
		return 0, err
	}
	api, err := h.albumAPI(ctx, r.OwnerID)
	if err != nil {
		return fail(ErrNoAlbumKey.Error(), err)
	}
	me, err := api.Me(ctx)
	if err != nil {
		return fail("your Immich does not accept the app's key", err)
	}
	matches, err := h.FindForRule(ctx, api, me.ID, r, false)
	if err != nil {
		problem := trimNet(err)
		if errors.Is(err, immich.ErrNoSmartSearch) {
			problem = "your Immich server cannot do a smart search (it runs without machine learning), so the phrase finds nothing"
		}
		if errors.Is(err, ErrAlbumGone) {
			problem = ErrAlbumGone.Error()
		}
		return fail(problem, err)
	}
	waiting, _ := h.Store.RulePhotos(ctx, r.ID, store.RuleSuggested)
	var auto []string
	suggested := 0
	for _, m := range matches {
		id := m.Asset.Id.String()
		if r.Mode == store.RuleAuto && !r.Cond.NeverAuto() && !m.Asset.CreatedAt.Before(r.AutoSince) {
			if h.now().Sub(m.Asset.CreatedAt) < 6*h.ruleWait(r) {
				continue
			}
			auto = append(auto, id)
		} else {
			suggested++
		}
		if err := h.Store.PutRulePhoto(ctx, r.ID, id, store.RuleSuggested, m.Why); err != nil {
			return 0, err
		}
	}
	if len(auto) > 0 {
		if err := h.albumForRule(ctx, r, auto); err != nil {
			h.Store.MarkRuleLook(ctx, r.ID, h.now(), trimNet(err)) // they stay suggestions: nothing is lost
			return suggested, err
		}
		h.Store.SetRulePhotos(ctx, r.ID, auto, store.RuleSuggested, store.RuleSent)
		h.event(ctx, "", r.OwnerID, "album", i18n.PackN("{n} photo put in the album {album} by a rule on its own", "{n} photos put in the album {album} by a rule on their own", len(auto), "album", r.DestAlbum))
	}
	if r.Cond.RemoveStale {
		if n, err := h.takeOutStale(ctx, api, me.ID, r); err != nil {
			h.Store.MarkRuleLook(ctx, r.ID, h.now(), trimNet(err))
			return suggested, err
		} else if n > 0 {
			h.event(ctx, "", r.OwnerID, "album", i18n.PackN("{n} photo taken out of the album {album} because it no longer matches", "{n} photos taken out of the album {album} because they no longer match", n, "album", r.DestAlbum))
		}
	}
	h.Store.MarkRuleLook(ctx, r.ID, h.now(), "")
	if suggested > 0 && len(waiting) == 0 && !r.Paused { // told once for a batch, when it begins
		h.event(ctx, "", r.OwnerID, "suggestion", i18n.PackN("{n} new photo can go in the album {album}. Look at it on the Overview.", "{n} new photos can go in the album {album}. Look at them on the Overview.", suggested, "album", r.DestAlbum))
	}
	return suggested + len(auto), nil
}

// albumForRule puts photos in the rule's album, with the person's key. It only adds: one already there stays there once.
func (h *Hub) albumForRule(ctx context.Context, r store.Rule, assets []string) error {
	api, err := h.albumAPI(ctx, r.OwnerID)
	if err != nil {
		return err
	}
	album, err := uuid.Parse(r.Dest)
	if err != nil {
		return ErrAlbumGone
	}
	var photos []openapi_types.UUID
	for _, s := range assets {
		if u, err := uuid.Parse(s); err == nil {
			photos = append(photos, u)
		}
	}
	for len(photos) > 0 { // in pieces, so that one request is never huge
		n := min(len(photos), 250)
		if err := api.AddToAlbum(ctx, album, photos[:n]...); err != nil {
			if immich.IsNotFound(err) {
				return ErrAlbumGone
			}
			return fmt.Errorf("could not put photos in the album %s: %w", r.DestAlbum, err)
		}
		photos = photos[n:]
	}
	return nil
}

// takeOutStale takes out of the album the photos this rule put there that Immich no longer finds for it. It never touches a photo the
// rule did not put there. The photos are marked declined, so that the rule does not offer them again by itself.
func (h *Hub) takeOutStale(ctx context.Context, api *immich.API, me uuid.UUID, r store.Rule) (int, error) {
	sent, err := h.Store.RulePhotos(ctx, r.ID, store.RuleSent)
	if err != nil || len(sent) == 0 {
		return 0, err
	}
	album, err := uuid.Parse(r.Dest)
	if err != nil {
		return 0, ErrAlbumGone
	}
	in, err := api.AlbumAssets(ctx, album)
	if err != nil {
		return 0, err
	}
	inAlbum := map[string]bool{}
	for _, a := range in {
		inAlbum[a.Id.String()] = true
	}
	found, err := api.RuleSearch(ctx, h.ruleFilter(r, true))
	if err != nil {
		return 0, err
	}
	matching := map[string]bool{}
	for _, a := range found {
		matching[a.Id.String()] = true
	}
	var gone []string
	var ids []openapi_types.UUID
	for _, p := range sent {
		if !inAlbum[p.AssetID] || matching[p.AssetID] {
			continue
		}
		if u, err := uuid.Parse(p.AssetID); err == nil {
			gone, ids = append(gone, p.AssetID), append(ids, u)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	for i := 0; i < len(ids); i += 250 {
		if err := api.RemoveFromAlbum(ctx, album, ids[i:min(i+250, len(ids))]...); err != nil {
			return 0, err
		}
	}
	h.Store.SetRulePhotos(ctx, r.ID, gone, store.RuleSent, store.RuleDeclined)
	return len(gone), nil
}

// AlbumSentence says what a smart album rule does, in a few words.
func AlbumSentence(r store.Rule) string {
	return "the album " + r.DestAlbum + " holds " + RuleSentence(r.Cond)
}
