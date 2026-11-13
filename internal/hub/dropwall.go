package hub

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// StartWall gives a box a wall: a link that shows the box's photos as a slideshow, as they arrive, on a screen with no
// sign-in (a TV or a projector). It has a link of its own, so the screen never holds anyone's session and ending it ends
// nothing else. To show pictures the box's key gets one more permission (asset.view, to fetch thumbnails), added with the
// owner's own session; only the owner may. With renew, a new link replaces the old one.
func (h *Hub) StartWall(ctx context.Context, sess *immich.API, owner immich.User, id string, renew bool) (string, error) {
	b, _, err := h.dropBoxFor(ctx, owner.ID, id, "owner")
	if err != nil {
		return "", err
	}
	if b.Wall != "" && !renew {
		return b.Wall, nil
	}
	if err := sess.AddKeyPermission(ctx, b.KeyID, "asset.view"); err != nil {
		return "", fmt.Errorf("could not let the drop box's key show pictures: %w", err)
	}
	token := newToken() + newToken()
	if err := h.Store.SetDropWall(ctx, b.ID, token); err != nil {
		return "", err
	}
	h.dropEvent(ctx, b.ID, store.DropCreated, i18n.Pack("{name} started the wall", "name", owner.Name), "")
	return token, nil
}

// StopWall ends a box's wall: its link stops working, and the key loses the permission to show pictures.
func (h *Hub) StopWall(ctx context.Context, sess *immich.API, owner immich.User, id string) error {
	b, _, err := h.dropBoxFor(ctx, owner.ID, id, "owner")
	if err != nil {
		return err
	}
	if b.Wall == "" {
		return nil
	}
	if err := h.Store.SetDropWall(ctx, b.ID, ""); err != nil {
		return err
	}
	if !b.Gallery { // the guests' gallery needs it too
		if err := sess.RemoveKeyPermission(ctx, b.KeyID, "asset.view"); err != nil {
			h.Log.Warn("stop the wall: could not take asset.view off the key", "err", err)
		}
	}
	h.dropEvent(ctx, b.ID, store.DropRevokedK, i18n.Pack("{name} stopped the wall", "name", owner.Name), "")
	return nil
}

// WallBox finds the box a wall link shows; a link that is unknown or ended gives ErrDropGone.
func (h *Hub) WallBox(ctx context.Context, token string) (store.DropBox, error) {
	if h.Store == nil || len(token) < 32 || len(token) > 128 {
		return store.DropBox{}, ErrDropGone
	}
	b, err := h.Store.DropBoxByWall(ctx, token)
	if err != nil {
		return b, ErrDropGone
	}
	return b, nil
}

// WallItem is a photo on the wall: its place in the box's list, what it is for if the guest named a prompt, and who sent
// it if the box shows names.
type WallItem struct {
	ID      int64
	Caption string
	By      string // the name the guest gave, when the box shows names
}

// WallFeed is what a wall page asks for: the photos after the last one it has (or the latest ones, for a page that has
// none yet), oldest first, and the messages the owner chose to show.
func (h *Hub) WallFeed(ctx context.Context, b store.DropBox, after int64) ([]WallItem, []store.DropMessage, error) {
	var list []store.DropAsset
	var err error
	if after <= 0 {
		if list, err = h.Store.DropAssetsLatest(ctx, b.ID, 30); err != nil {
			return nil, nil, err
		}
		for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
			list[i], list[j] = list[j], list[i]
		}
	} else if list, err = h.Store.DropAssetsAfter(ctx, b.ID, after, 60); err != nil {
		return nil, nil, err
	}
	text := map[string]string{}
	for _, p := range b.Prompts {
		text[p.ID] = p.Text
	}
	items := make([]WallItem, 0, len(list))
	for _, a := range list {
		it := WallItem{ID: a.ID, Caption: text[a.Prompt]}
		if b.GalleryNames {
			it.By = a.GuestName
		}
		items = append(items, it)
	}
	msgs, _ := h.Store.WallMessages(ctx, b.ID, 20)
	return items, msgs, nil
}

// wallCache keeps the last pictures a wall fetched, so a screen and a few phones asking for the same photo ask Immich once.
type wallCache struct {
	mu    sync.Mutex
	items map[string]wallPic
	order []string
	bytes int
}

type wallPic struct {
	data []byte
	kind string
}

const wallCacheBytes = 48 << 20

func (c *wallCache) get(k string) (wallPic, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.items[k]
	return p, ok
}

func (c *wallCache) put(k string, p wallPic) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]wallPic{}
	}
	if _, ok := c.items[k]; ok {
		return
	}
	c.items[k] = p
	c.order = append(c.order, k)
	c.bytes += len(p.data)
	for c.bytes > wallCacheBytes && len(c.order) > 1 {
		old := c.order[0]
		c.order = c.order[1:]
		c.bytes -= len(c.items[old].data)
		delete(c.items, old)
	}
}

// WallImage fetches the picture of one photo of a box, large enough for a screen, with the box's own key. Only a photo
// the box kept (and that was not taken back) can be asked for, by its place in the box's list, never by its id in Immich.
func (h *Hub) WallImage(ctx context.Context, b store.DropBox, id int64) ([]byte, string, error) {
	a, err := h.Store.DropAssetByID(ctx, b.ID, id)
	if err != nil {
		return nil, "", ErrDropGone
	}
	key := b.ID + "/" + a.AssetID
	if p, ok := h.wall.get(key); ok {
		return p.data, p.kind, nil
	}
	asset, err := uuid.Parse(a.AssetID)
	if err != nil {
		return nil, "", ErrDropGone
	}
	api, err := h.Connect(b.Key)
	if err != nil {
		return nil, "", err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, kind, err := api.Thumbnail(cctx, asset, true)
	if err != nil {
		if immich.IsNotFound(err) {
			return nil, "", ErrDropGone
		}
		return nil, "", err
	}
	h.wall.put(key, wallPic{data, kind})
	return data, kind, nil
}

// SetMessageOnWall chooses whether a guest-book message is shown on the wall. The owner and the co-hosts who manage may.
func (h *Hub) SetMessageOnWall(ctx context.Context, user immich.User, msgID string, on bool) error {
	box, err := h.Store.DropMessageBox(ctx, msgID)
	if err != nil {
		return ErrNoDropBox
	}
	if _, _, err := h.dropBoxFor(ctx, user.ID, box, "manage"); err != nil {
		return err
	}
	return h.Store.SetMessageOnWall(ctx, msgID, on)
}
