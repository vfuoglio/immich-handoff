package hub

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// The gallery of a drop box: guests, the ones with the link, look at the photos the box has kept, heart the ones they
// like and, if the owner allows, download them and see who sent them. What a guest sees is only what is in the owner's
// library under the box: a photo waiting for approval, a rejected one or one a guest took back is not there.

const (
	galleryPage       = 48  // the most photos one answer holds
	heartsPerHour     = 240 // the most hearts one guest may give or take back in an hour
	originalsAtOnce   = 8   // downloads of originals going on at once, in the whole app
	guestNameMaxRunes = 40  // the longest name a guest can give
	heartWindow       = time.Hour
)

// originals is the places for downloads of originals, made when first needed.
func (h *Hub) originals() chan struct{} {
	h.slotsOnce.Do(func() { h.slots = make(chan struct{}, originalsAtOnce) })
	return h.slots
}

// CleanGuestName makes what a guest typed as their name fit to be shown: no control characters, one space between
// words, at most forty letters. It may be empty.
func CleanGuestName(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r): // a tab or a line break is a space too
			space = b.Len() > 0
			continue
		case unicode.IsControl(r) || r == '<' || r == '>' || r == '"' || r == '\u200b':
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if utf8.RuneCountInString(out) > guestNameMaxRunes {
		out = string([]rune(out)[:guestNameMaxRunes])
	}
	return out
}

// GalleryBox finds the box a guest's link shows as a gallery: one that has a gallery and is not revoked or paused,
// open now or, once it has ended, until what guests left with it goes (see the box's retention).
func (h *Hub) GalleryBox(ctx context.Context, token string) (store.DropBox, error) {
	b, err := h.DropBoxForGuest(ctx, token)
	if err != nil {
		return b, err
	}
	if !b.Gallery || b.State != store.DropActive {
		return b, ErrDropGone
	}
	now := time.Now()
	if b.NotStarted(now) {
		return b, ErrDropGone
	}
	if b.Expired(now) {
		if until := b.KeepUntil(); !until.IsZero() && !now.Before(until) {
			return b, ErrDropGone
		}
	}
	return b, nil
}

// GalleryItem is a photo as a guest sees it in the gallery.
type GalleryItem struct {
	ID      int64 // its place in the box's list, never its id in Immich
	Name    string
	Caption string
	Hearts  int
	Mine    bool // this guest has given it a heart
}

// GalleryFeed lists photos for a guest, newest first: the photos after one the page has (to add what came since), or
// the photos before one (for the next page), or, with neither, the newest. secret is the guest's own, to say which
// hearts are theirs.
func (h *Hub) GalleryFeed(ctx context.Context, b store.DropBox, secret string, before, after int64, limit int) ([]GalleryItem, error) {
	if limit <= 0 || limit > galleryPage {
		limit = galleryPage
	}
	var list []store.DropAsset
	var err error
	if after > 0 {
		if list, err = h.Store.DropAssetsAfter(ctx, b.ID, after, limit); err != nil {
			return nil, err
		}
		for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
			list[i], list[j] = list[j], list[i]
		}
	} else if list, err = h.Store.DropAssetsBefore(ctx, b.ID, before, limit); err != nil {
		return nil, err
	}
	items := make([]GalleryItem, 0, len(list))
	if len(list) == 0 {
		return items, nil
	}
	lo, hi := list[0].ID, list[0].ID
	for _, a := range list {
		lo, hi = min(lo, a.ID), max(hi, a.ID)
	}
	hearts, err := h.Store.HeartCounts(ctx, b.ID, lo, hi)
	if err != nil {
		return nil, err
	}
	mine := map[int64]bool{}
	if secret != "" {
		if mine, err = h.Store.GuestHearts(ctx, b.ID, GuestKey(secret), lo, hi); err != nil {
			return nil, err
		}
	}
	text := map[string]string{}
	for _, p := range b.Prompts {
		text[p.ID] = p.Text
	}
	for _, a := range list {
		it := GalleryItem{ID: a.ID, Caption: text[a.Prompt], Hearts: hearts[a.ID], Mine: mine[a.ID]}
		if b.GalleryNames {
			it.Name = a.GuestName
		}
		items = append(items, it)
	}
	return items, nil
}

// heartGuard keeps, in memory, when each guest gave hearts in the last hour.
type heartGuard struct {
	mu   sync.Mutex
	seen map[string][]time.Time
}

func (g *heartGuard) allow(key string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.seen == nil {
		g.seen = map[string][]time.Time{}
	}
	ts := within(g.seen[key], heartWindow, now)
	if len(ts) >= heartsPerHour {
		g.seen[key] = ts
		return false
	}
	g.seen[key] = append(ts, now)
	if len(g.seen) > 20000 { // a flood of guests: forget the old ones rather than grow
		for k, v := range g.seen {
			if len(within(v, heartWindow, now)) == 0 {
				delete(g.seen, k)
			}
		}
	}
	return true
}

// GalleryHeart gives a photo the guest's heart, or takes it back. A guest may heart as often as they like, up to a
// number an hour.
func (h *Hub) GalleryHeart(ctx context.Context, b store.DropBox, secret string, id int64) (hearted bool, count int, err error) {
	if secret == "" {
		return false, 0, ErrDropGone
	}
	guest := GuestKey(secret)
	if !h.hearts.allow(b.ID+"|"+guest, time.Now()) {
		return false, 0, ErrDropBusy
	}
	hearted, count, err = h.Store.ToggleHeart(ctx, b.ID, id, guest)
	if err != nil {
		return false, 0, ErrDropGone
	}
	return hearted, count, nil
}

// GalleryImage fetches the picture of one photo for a guest, the same as the wall's.
func (h *Hub) GalleryImage(ctx context.Context, b store.DropBox, id int64) ([]byte, string, error) {
	full, err := h.Store.DropBox(ctx, b.ID)
	if err != nil {
		return nil, "", ErrDropGone
	}
	return h.WallImage(ctx, full, id)
}

// original is an original being streamed to a guest; closing it gives its place back.
type original struct {
	io.ReadCloser
	release func()
}

func (o original) Close() error { o.release(); return o.ReadCloser.Close() }

// GalleryOriginal opens the original of a photo for a guest, if the box lets guests download. It returns the
// response (the caller closes its body, and may pass on its headers) and the name to save it under.
func (h *Hub) GalleryOriginal(ctx context.Context, b store.DropBox, id int64) (*http.Response, error) {
	if !b.GalleryDownload {
		return nil, ErrDropGone
	}
	a, err := h.Store.DropAssetByID(ctx, b.ID, id)
	if err != nil {
		return nil, ErrDropGone
	}
	asset, err := uuid.Parse(a.AssetID)
	if err != nil {
		return nil, ErrDropGone
	}
	full, err := h.Store.DropBox(ctx, b.ID)
	if err != nil {
		return nil, ErrDropGone
	}
	api, err := h.Connect(full.Key)
	if err != nil {
		return nil, err
	}
	select {
	case h.originals() <- struct{}{}:
	default:
		return nil, ErrDropBusy
	}
	resp, err := api.Original(ctx, asset)
	if err != nil {
		<-h.originals()
		if immich.IsNotFound(err) {
			return nil, ErrDropGone
		}
		return nil, err
	}
	resp.Body = original{resp.Body, func() { <-h.originals() }}
	return resp, nil
}
