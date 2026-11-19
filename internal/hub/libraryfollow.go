package hub

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Following an album that was shared with the person (#188): a rule of kind "library" looks at the album from time to time
// and offers (or, when the person has let it, copies) what others added into the person's own library, with the same
// copy as the Add to my library button. The app does this with a key the person made for it, so it needs no session.

// libraryKeyName is what the key is called in the person's Immich, where they can see it and delete it.
const libraryKeyName = "handoff: library"

var (
	ErrNoLibraryKey     = errors.New("the key the app uses to follow your albums is gone: follow the album again")
	ErrAlbumNotShared   = errors.New("that album is not shared with you")
	ErrAlreadyFollowed  = errors.New("you already follow that album")
	ErrBadFollowHistory = errors.New("choose whether to start with what is in the album now, or only with what is added from now on")
)

// History choices when an album is followed: what is in it now is offered too, or only what is added from now on.
const (
	FollowAll = "all"
	FollowNew = "new"
)

// FollowParams is what a person fills in to follow an album that was shared with them.
type FollowParams struct {
	Album   uuid.UUID
	Dest    LibraryDest
	History string // FollowNew (the default) or FollowAll
}

// ensureLibraryKey makes the person's key for following albums the first time (with their own session), and gives it the
// permissions to make and fill an album when the copies are to go to one. It returns a client that acts with the key.
func (h *Hub) ensureLibraryKey(ctx context.Context, sess *immich.API, user immich.User, withAlbum bool) (*immich.API, error) {
	if !h.SelfService() {
		return nil, ErrNotJoined
	}
	k, ok, err := h.Store.LibraryKey(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	if ok {
		if withAlbum {
			for _, p := range immich.LibraryPermissions(true) {
				if err := sess.AddKeyPermission(ctx, k.KeyID, p); err != nil {
					return nil, fmt.Errorf("could not let the app make an album for you: %w", err)
				}
			}
		}
		return h.Connect(k.Key)
	}
	id, secret, err := sess.CreateAPIKey(ctx, libraryKeyName, immich.LibraryPermissions(withAlbum))
	if err != nil {
		return nil, fmt.Errorf("could not make the app a key to follow your albums: %w", err)
	}
	if err := h.Store.SetLibraryKey(ctx, user.ID, store.LibraryKey{KeyID: id, Key: secret}); err != nil {
		_ = sess.DeleteAPIKey(ctx, id)
		return nil, err
	}
	return h.Connect(secret)
}

// libraryAPI is the client that acts with the person's key for following albums.
func (h *Hub) libraryAPI(ctx context.Context, user uuid.UUID) (*immich.API, error) {
	if h.Store == nil {
		return nil, ErrNotJoined
	}
	k, ok, err := h.Store.LibraryKey(ctx, user)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNoLibraryKey
	}
	return h.Connect(k.Key)
}

// dropLibraryKeyIfUnused deletes the person's key (in their Immich, with their session) when no rule of theirs follows an
// album any more.
func (h *Hub) dropLibraryKeyIfUnused(ctx context.Context, sess *immich.API, user uuid.UUID) {
	rules, err := h.Store.RulesOf(ctx, user)
	if err != nil {
		return
	}
	for _, r := range rules {
		if r.Kind == "library" || r.Kind == store.RuleKindImport {
			return
		}
	}
	k, ok, err := h.Store.LibraryKey(ctx, user)
	if err != nil || !ok {
		return
	}
	if sess != nil && k.KeyID != uuid.Nil {
		if err := sess.DeleteAPIKey(ctx, k.KeyID); err != nil && !immich.IsNotFound(err) {
			h.Log.Debug("could not delete the key for following albums", "err", err)
		}
	}
	_ = h.Store.DeleteLibraryKey(ctx, user)
}

// SharedAlbumsFor lists the albums other people shared with the person, with who shared each, for the form.
func (h *Hub) SharedAlbumsFor(ctx context.Context, sess *immich.API) ([]immich.SharedAlbumInfo, error) {
	return sess.AlbumsSharedWithMe(ctx)
}

// FollowAlbum makes a rule that follows an album shared with the person: what others add to it is offered (or, once the
// person has let the rule go on by itself, copied) into their own library. With FollowNew what is in the album now is only
// remembered, never offered; with FollowAll it is offered too.
func (h *Hub) FollowAlbum(ctx context.Context, sess *immich.API, user immich.User, p FollowParams) (store.Rule, error) {
	if !h.SelfService() {
		return store.Rule{}, ErrNotJoined
	}
	dest, err := checkLibraryDest(p.Dest)
	if err != nil {
		return store.Rule{}, err
	}
	switch p.History {
	case "":
		p.History = FollowNew
	case FollowNew, FollowAll:
	default:
		return store.Rule{}, ErrBadFollowHistory
	}
	shared, err := sess.AlbumsSharedWithMe(ctx)
	if err != nil {
		return store.Rule{}, fmt.Errorf("could not list the albums shared with you: %w", err)
	}
	var album *immich.SharedAlbumInfo
	for i := range shared {
		if uuid.UUID(shared[i].ID) == p.Album {
			album = &shared[i]
		}
	}
	if album == nil {
		return store.Rule{}, ErrAlbumNotShared
	}
	rules, err := h.Store.RulesOf(ctx, user.ID)
	if err != nil {
		return store.Rule{}, err
	}
	if len(rules) >= maxRules {
		return store.Rule{}, ErrTooManyRules
	}
	for _, r := range rules {
		if r.Kind == store.RuleKindLibrary && r.Cond.Album != nil && r.Cond.Album.ID == p.Album.String() {
			return store.Rule{}, ErrAlreadyFollowed
		}
	}
	api, err := h.ensureLibraryKey(ctx, sess, user, dest.Kind == LibraryAlbum)
	if err != nil {
		return store.Rule{}, err
	}
	var baseline []immich.AssetResponseDto
	if p.History == FollowNew { // read with the key: this also shows that it can read the album
		if baseline, err = api.AlbumAssets(ctx, p.Album); err != nil {
			return store.Rule{}, fmt.Errorf("the app could not read the album with its key: %w", err)
		}
	}
	rule, err := h.Store.AddRule(ctx, store.Rule{OwnerID: user.ID, Kind: store.RuleKindLibrary, Dest: dest.Kind, DestAlbum: dest.Album,
		Cond: store.RuleCond{Album: &store.RuleRef{ID: p.Album.String(), Name: album.Name}, AlbumOwner: album.Owner}})
	if err != nil {
		return store.Rule{}, err
	}
	for _, a := range baseline {
		if err := h.Store.PutRulePhoto(ctx, rule.ID, a.Id.String(), store.RuleSeen, ""); err != nil {
			return rule, err
		}
	}
	h.event(ctx, "", user.ID, "rule", i18n.Pack("{name} follows the album {album}, shared by {owner}", "name", user.Name, "album", album.Name, "owner", album.Owner))
	return rule, nil
}

// lookLibrary looks at one rule that follows an album: what others have added since it last looked becomes suggestions, or,
// when the person has let the rule go on by itself, is copied into their library. It returns how many photos are new.
func (h *Hub) lookLibrary(ctx context.Context, r store.Rule) (int, error) {
	fail := func(problem string, err error) (int, error) {
		h.Store.MarkRuleLook(ctx, r.ID, h.now(), problem)
		return 0, err
	}
	api, err := h.libraryAPI(ctx, r.OwnerID)
	if err != nil {
		return fail("the key the app uses to follow your albums is gone: follow the album again", err)
	}
	me, err := api.Me(ctx)
	if err != nil {
		return fail("your Immich does not accept the app's key", err)
	}
	if r.Cond.Album == nil {
		return 0, nil
	}
	aid, _ := uuid.Parse(r.Cond.Album.ID)
	still, err := api.AlbumsSharedWithMe(ctx)
	if err != nil {
		return fail(trimNet(err), err)
	}
	shared := false
	for _, al := range still {
		shared = shared || uuid.UUID(al.ID) == aid
	}
	if !shared { // the owner stopped sharing it, or deleted it
		return fail("the album is not shared with you any more", ErrAlbumNotShared)
	}
	items, err := api.AlbumAssets(ctx, aid)
	if err != nil {
		problem := trimNet(err)
		if immich.IsForbidden(err) || immich.IsNotFound(err) {
			problem = "the album is not shared with you any more"
		}
		return fail(problem, err)
	}
	known, err := h.Store.RulePhotoStates(ctx, r.ID)
	if err != nil {
		return 0, err
	}
	why := "added to the album " + r.Cond.Album.Name
	if r.Cond.AlbumOwner != "" {
		why += ", shared by " + r.Cond.AlbumOwner
	}
	var fresh []string
	for _, a := range items {
		if a.OwnerId == me.ID || a.IsTrashed || (a.Type != immich.IMAGE && a.Type != immich.VIDEO) || known[a.Id.String()] != "" || h.now().Sub(a.CreatedAt) < h.ruleSettle() {
			continue
		}
		fresh = append(fresh, a.Id.String())
	}
	waiting, _ := h.Store.RulePhotos(ctx, r.ID, store.RuleSuggested)
	for _, id := range fresh {
		if err := h.Store.PutRulePhoto(ctx, r.ID, id, store.RuleSuggested, why); err != nil {
			return 0, err
		}
	}
	if r.Mode == store.RuleAuto && len(fresh) > 0 {
		// the person has let this rule go on by itself: what is new is copied; what was already waiting stays a suggestion
		if err := h.copyForRule(ctx, r, fresh); err != nil {
			h.Store.MarkRuleLook(ctx, r.ID, h.now(), trimNet(err))
			return len(fresh), err
		}
		h.Store.SetRulePhotos(ctx, r.ID, fresh, store.RuleSuggested, store.RuleSent)
		h.event(ctx, "", r.OwnerID, "library", i18n.PackN("{n} photo added to your library from {album} on its own", "{n} photos added to your library from {album} on their own", len(fresh), "album", r.Cond.Album.Name))
		h.Store.MarkRuleLook(ctx, r.ID, h.now(), "")
		return len(fresh), nil
	}
	h.Store.MarkRuleLook(ctx, r.ID, h.now(), "")
	if len(fresh) > 0 && len(waiting) == 0 { // told once for a batch, when it begins
		h.event(ctx, "", r.OwnerID, "suggestion", i18n.PackN("{n} new photo in the album {album} you follow. Look at it on the Overview.", "{n} new photos in the album {album} you follow. Look at them on the Overview.", len(fresh), "album", r.Cond.Album.Name))
	}
	return len(fresh), nil
}

// copyForRule copies photos of a followed album into the owner's library, with the owner's key, to where the rule says.
func (h *Hub) copyForRule(ctx context.Context, r store.Rule, assets []string) error {
	api, err := h.libraryAPI(ctx, r.OwnerID)
	if err != nil {
		return err
	}
	me, err := api.Me(ctx)
	if err != nil {
		return err
	}
	_, err = h.addToLibrary(ctx, api, me, assets, LibraryDest{Kind: r.Dest, Album: r.DestAlbum}, true)
	return err
}
