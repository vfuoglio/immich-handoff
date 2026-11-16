package hub

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Following an album that was shared with the person (#188): a rule of kind "library" looks at the album from time to time
// and offers (or, when the person has let it, copies) what others added into the person's own library, with the same
// copy as the Add to my library button. The app does this with a key the person made for it, so it needs no session.

// libraryKeyName is what the key is called in the person's Immich, where they can see it and delete it.
const libraryKeyName = "handoff: library"

var ErrNoLibraryKey = errors.New("the key the app uses to follow your albums is gone: follow the album again")

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
		if r.Kind == "library" {
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
