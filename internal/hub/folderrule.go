package hub

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// A rule of kind folder (#198) makes albums from the folders of the person's external library: it looks at their photos under a
// folder with a key of their own, offers the albums (or, once they have let it, makes them), and only ever adds. See
// docs/contributing/design/folders-design.md.

// folderKeyName is what the key is called in the person's Immich, where they can see it and delete it.
const folderKeyName = "handoff: folders"

// FolderPerPass is the most new photos one look at one rule takes up; the rest wait for the next look. It keeps a first look at a
// folder of hundreds of thousands of photos from being one very long pass.
var FolderPerPass = 500

// folderRecheck is how far before the last complete look a pass starts listing again, so that a photo that was being
// scanned in at that moment is not missed.
const folderRecheck = time.Hour

var (
	ErrNoFolderKey   = errors.New("the key the app uses for your folders is gone: make the rule again")
	ErrFolderTwice   = errors.New("you already have a rule for that folder")
	ErrFolderKeyMiss = errors.New("the app could not read your photos with its key")
)

// ensureFolderKey makes the person's key for folder albums the first time (with their own session) and returns a client that acts
// with it.
func (h *Hub) ensureFolderKey(ctx context.Context, sess *immich.API, user immich.User) (*immich.API, error) {
	if !h.SelfService() {
		return nil, ErrNotJoined
	}
	k, ok, err := h.Store.FolderKey(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	if ok {
		return h.Connect(k.Key)
	}
	id, secret, err := sess.CreateAPIKey(ctx, folderKeyName, immich.FolderPermissions)
	if err != nil {
		return nil, fmt.Errorf("could not make the app a key for your folders: %w", err)
	}
	if err := h.Store.SetFolderKey(ctx, user.ID, store.FolderKey{KeyID: id, Key: secret}); err != nil {
		_ = sess.DeleteAPIKey(ctx, id)
		return nil, err
	}
	return h.Connect(secret)
}

// folderAPI is the client that acts with the person's key for folder albums.
func (h *Hub) folderAPI(ctx context.Context, user uuid.UUID) (*immich.API, error) {
	if h.Store == nil {
		return nil, ErrNotJoined
	}
	k, ok, err := h.Store.FolderKey(ctx, user)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNoFolderKey
	}
	return h.Connect(k.Key)
}

// dropFolderKeyIfUnused deletes the person's key (in their Immich, with their session) when no rule of theirs makes albums from
// folders any more.
func (h *Hub) dropFolderKeyIfUnused(ctx context.Context, sess *immich.API, user uuid.UUID) {
	rules, err := h.Store.RulesOf(ctx, user)
	if err != nil {
		return
	}
	for _, r := range rules {
		if r.Kind == store.RuleKindFolder {
			return
		}
	}
	k, ok, err := h.Store.FolderKey(ctx, user)
	if err != nil || !ok {
		return
	}
	if sess != nil && k.KeyID != uuid.Nil {
		if err := sess.DeleteAPIKey(ctx, k.KeyID); err != nil && !immich.IsNotFound(err) {
			h.Log.Debug("could not delete the key for folder albums", "err", err)
		}
	}
	_ = h.Store.DeleteFolderKey(ctx, user)
}

// FolderSpecOf is what a rule of kind folder looks at.
func FolderSpecOf(r store.Rule) FolderSpec {
	return FolderSpec{Prefix: r.Cond.Folder, Depth: r.Cond.Level, Naming: r.Cond.Naming}
}

// CountFolderPhotos says how many of the person's photos are under a folder, with their own session, for the form to show
// before the rule is made. It lists nothing the rule would not.
func (h *Hub) CountFolderPhotos(ctx context.Context, sess *immich.API, user immich.User, s FolderSpec) (int, FolderPlan, error) {
	s, err := s.Clean()
	if err != nil {
		return 0, FolderPlan{}, err
	}
	photos, err := h.FolderPhotos(ctx, sess, user.ID, s)
	if err != nil {
		return 0, FolderPlan{}, err
	}
	plan, err := PlanFolders(s, photos)
	return len(photos), plan, err
}

// FollowFolder makes a rule that makes albums from the folders under a folder of the person's external library. What is there now is
// offered too. Nothing is made until the person says so.
func (h *Hub) FollowFolder(ctx context.Context, sess *immich.API, user immich.User, s FolderSpec) (store.Rule, error) {
	if !h.SelfService() {
		return store.Rule{}, ErrNotJoined
	}
	s, err := s.Clean()
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
	for _, r := range rules {
		if r.Kind == store.RuleKindFolder && r.Cond.Folder == s.Prefix && r.Cond.Level == s.Depth && r.Cond.Naming == s.Naming {
			return store.Rule{}, ErrFolderTwice
		}
	}
	// the person's own session finds out whether there is anything under that folder, before a key is made for it
	photos, err := h.FolderPhotos(ctx, sess, user.ID, s)
	if err != nil {
		return store.Rule{}, fmt.Errorf("could not look for your photos under that folder: %w", err)
	}
	if len(photos) == 0 {
		return store.Rule{}, ErrNoFolderPh
	}
	api, err := h.ensureFolderKey(ctx, sess, user)
	if err != nil {
		return store.Rule{}, err
	}
	if _, err := h.FolderPhotos(ctx, api, user.ID, s, time.Now().Add(time.Hour)); err != nil { // asks for nothing: it shows that the key can search
		h.dropFolderKeyIfUnused(ctx, sess, user.ID)
		return store.Rule{}, fmt.Errorf("%w: %v", ErrFolderKeyMiss, trimNet(err))
	}
	rule, err := h.Store.AddRule(ctx, store.Rule{OwnerID: user.ID, Kind: store.RuleKindFolder,
		Cond: store.RuleCond{Folder: s.Prefix, Level: s.Depth, Naming: s.Naming}})
	if err != nil {
		return store.Rule{}, err
	}
	h.event(ctx, "", user.ID, "rule", i18n.Pack("{name} makes albums from the folders under {folder}", "name", user.Name, "folder", s.Prefix))
	return rule, nil
}

// lookFolder looks at one rule that makes albums from folders: the photos it has not handled become suggestions, each with the
// album it would go to, or, when the person has let the rule go on by itself, are put in their albums. It takes up at most
// FolderPerPass photos, and goes on with the rest at the next look. It returns how many photos are new.
func (h *Hub) lookFolder(ctx context.Context, r store.Rule) (int, error) {
	fail := func(problem string, err error) (int, error) {
		h.Store.MarkRuleLook(ctx, r.ID, h.now(), problem)
		return 0, err
	}
	api, err := h.folderAPI(ctx, r.OwnerID)
	if err != nil {
		return fail(ErrNoFolderKey.Error(), err)
	}
	me, err := api.Me(ctx)
	if err != nil {
		return fail("your Immich does not accept the app's key", err)
	}
	started := h.now()
	var after time.Time
	if !r.CaughtUp.IsZero() {
		after = r.CaughtUp.Add(-folderRecheck)
	}
	spec := FolderSpecOf(r)
	all, err := h.FolderPhotos(ctx, api, me.ID, spec, after)
	if err != nil {
		return fail(trimNet(err), err)
	}
	known, err := h.Store.RulePhotoStates(ctx, r.ID)
	if err != nil {
		return 0, err
	}
	var fresh []PathAsset
	created := map[string]time.Time{}
	for _, a := range all {
		if known[a.ID.String()] == "" {
			fresh = append(fresh, a)
			created[a.ID.String()] = a.Created
		}
	}
	more := false
	if len(fresh) > FolderPerPass { // the first look at a big folder is spread over several passes
		sort.Slice(fresh, func(i, j int) bool { return fresh[i].Path < fresh[j].Path })
		fresh, more = fresh[:FolderPerPass], true
	}
	plan, err := PlanFolders(spec, fresh)
	if err != nil {
		return fail(err.Error(), err)
	}
	for _, id := range plan.LooseIDs { // no folder at that level: looked at, never offered
		if err := h.Store.PutRulePhoto(ctx, r.ID, id.String(), store.RuleSeen, ""); err != nil {
			return 0, err
		}
	}
	waiting, _ := h.Store.RulePhotos(ctx, r.ID, store.RuleSuggested)
	byAlbum := map[string][]string{} // what the rule makes by itself, by album
	suggested, albums := 0, map[string]bool{}
	for _, al := range plan.Albums {
		for _, id := range al.Photos {
			sid := id.String()
			if r.Mode == store.RuleAuto && !created[sid].Before(r.AutoSince) {
				byAlbum[al.Name] = append(byAlbum[al.Name], sid)
				if err := h.Store.PutRulePhoto(ctx, r.ID, sid, store.RuleSuggested, al.Name); err != nil {
					return 0, err
				}
				continue
			}
			suggested++
			albums[al.Name] = true
			if err := h.Store.PutRulePhoto(ctx, r.ID, sid, store.RuleSuggested, al.Name); err != nil {
				return 0, err
			}
		}
	}
	if len(byAlbum) > 0 {
		var ids []string
		for _, v := range byAlbum {
			ids = append(ids, v...)
		}
		if err := h.fillFolderAlbums(ctx, r, byAlbum); err != nil {
			h.Store.MarkRuleLook(ctx, r.ID, h.now(), trimNet(err)) // they stay suggestions: nothing is lost
			return suggested, err
		}
		h.Store.SetRulePhotos(ctx, r.ID, ids, store.RuleSuggested, store.RuleSent)
		h.event(ctx, "", r.OwnerID, "folder", i18n.PackN("{n} photo put in albums made from your folders on its own", "{n} photos put in albums made from your folders on their own", len(ids)))
	}
	if !more { // everything up to this pass has been looked at: the next one lists only what comes after
		h.Store.MarkRuleCaughtUp(ctx, r.ID, started)
	}
	h.Store.MarkRuleLook(ctx, r.ID, h.now(), "")
	if suggested > 0 && len(waiting) == 0 { // told once for a batch, when it begins
		h.event(ctx, "", r.OwnerID, "suggestion", i18n.PackN("{n} new photo can go in albums made from your folders. Look at it on the Overview.", "{n} new photos can go in albums made from your folders. Look at them on the Overview.", suggested))
	}
	return suggested + len(byAlbum), nil
}

// fillFolderAlbums puts photos in albums of the person's, with their key: an album of that name that they own is used, else it is
// made. It only adds: nothing is removed, renamed or deleted, and a photo already in the album stays there once.
func (h *Hub) fillFolderAlbums(ctx context.Context, r store.Rule, byAlbum map[string][]string) error {
	api, err := h.folderAPI(ctx, r.OwnerID)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(byAlbum))
	for n := range byAlbum {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		id, _, err := api.FindAlbum(ctx, name)
		if err != nil {
			return fmt.Errorf("could not look for the album %s: %w", name, err)
		}
		if id == nil {
			made, err := api.CreateAlbum(ctx, name)
			if err != nil {
				return fmt.Errorf("could not make the album %s: %w", name, err)
			}
			id = &made
		}
		var photos []uuid.UUID
		for _, s := range byAlbum[name] {
			if u, err := uuid.Parse(s); err == nil {
				photos = append(photos, u)
			}
		}
		for len(photos) > 0 { // in pieces, so that one request is never huge
			n := min(len(photos), 250)
			if err := api.AddToAlbum(ctx, *id, photos[:n]...); err != nil {
				return fmt.Errorf("could not put photos in the album %s: %w", name, err)
			}
			photos = photos[n:]
		}
	}
	return nil
}

// folderForRule is what an answer of Send or Always does for a rule of kind folder: the photos go to the album each was offered for.
func (h *Hub) folderForRule(ctx context.Context, r store.Rule, assets []string) error {
	suggested, err := h.Store.RulePhotos(ctx, r.ID, store.RuleSuggested)
	if err != nil {
		return err
	}
	albumOf := map[string]string{}
	for _, p := range suggested {
		albumOf[p.AssetID] = p.Why
	}
	byAlbum := map[string][]string{}
	for _, a := range assets {
		if name := albumOf[a]; name != "" {
			byAlbum[name] = append(byAlbum[name], a)
		}
	}
	return h.fillFolderAlbums(ctx, r, byAlbum)
}

// FolderSentence says what a folder rule does, in a few words.
func FolderSentence(c store.RuleCond) string {
	s := "an album for each folder in " + c.Folder
	if c.Level > 0 {
		s = fmt.Sprintf("an album for each folder %d level(s) below %s", c.Level, c.Folder)
	}
	if c.Naming == NameParent {
		s += ", named after the folder and its parent"
	}
	return strings.TrimSpace(s)
}
