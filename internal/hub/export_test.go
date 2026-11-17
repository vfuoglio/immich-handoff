package hub

import (
	"context"

	"github.com/vfuoglio/immich-handoff/internal/immich"
)

// EnsureLibraryKeyForTest lets the tests of package hub_test reach the key for following albums.
func (h *Hub) EnsureLibraryKeyForTest(ctx context.Context, sess *immich.API, user immich.User, withAlbum bool) (*immich.API, error) {
	return h.ensureLibraryKey(ctx, sess, user, withAlbum)
}

// DropLibraryKeyForTest ends the key when no rule uses it.
func (h *Hub) DropLibraryKeyForTest(ctx context.Context, sess *immich.API, user immich.User) {
	h.dropLibraryKeyIfUnused(ctx, sess, user.ID)
}
