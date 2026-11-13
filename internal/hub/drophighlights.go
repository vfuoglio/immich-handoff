package hub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

var (
	ErrNoPhotosYet       = errors.New("there are no photos in this drop box's inbox to look at yet")
	ErrHighlightsRunning = errors.New("a best-of is already being made for this drop box")
	ErrNoHighlights      = errors.New("make a best-of first")
)

// The verdicts of a best-of.
const (
	VerdictKept      = "kept"
	VerdictDark      = "dark"
	VerdictBright    = "bright"
	VerdictBlurry    = "blurry"
	VerdictDuplicate = "duplicate"
	VerdictHidden    = "hidden" // the host hid it from the guests, so it is left out
)

const sameMoment = 60 * time.Minute // two pictures this far apart in time are never the same moment

// StartHighlights starts making a box's best-of: it looks at every photo in the box's inbox tag, leaves out the ones that
// are too dark, too bright or blurred, keeps the best of each set of near-identical shots, and puts what is left in an
// album of the owner's, "<box> best of". It never deletes or changes a photo; running it again with another strictness
// corrects the album. It needs the owner's own session (to read the photos), so only the owner may start it, and it works
// in the background: the box's row shows how far it is.
func (h *Hub) StartHighlights(ctx context.Context, sess *immich.API, owner immich.User, id, strictness string) error {
	b, _, err := h.dropBoxFor(ctx, owner.ID, id, "owner")
	if err != nil {
		return err
	}
	st := StrictnessNamed(strictness)
	h.mu.Lock()
	if h.hlRun == nil {
		h.hlRun = map[string]bool{}
	}
	if h.hlRun[b.ID] {
		h.mu.Unlock()
		return ErrHighlightsRunning
	}
	h.hlRun[b.ID] = true
	h.mu.Unlock()
	started := time.Now()
	_ = h.Store.SetDropHighlight(ctx, store.DropHighlight{BoxID: b.ID, Strictness: st.Name, Status: store.HighlightRunning, Started: started})
	go func() {
		defer func() {
			h.mu.Lock()
			delete(h.hlRun, b.ID)
			h.mu.Unlock()
		}()
		rctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		if err := h.runHighlights(rctx, sess, b, st, started); err != nil {
			h.Log.Warn("best-of failed", "box", b.Name, "err", err)
			_ = h.Store.SetDropHighlight(context.WithoutCancel(rctx), store.DropHighlight{BoxID: b.ID, Strictness: st.Name, Status: store.HighlightFailed, Note: err.Error(), Started: started, Finished: time.Now()})
		}
	}()
	return nil
}

type lookedAt struct {
	a immich.AssetResponseDto
	q *quality // nil: it could not be looked at (a format that cannot be decoded here), so it is kept
}

func (h *Hub) runHighlights(ctx context.Context, sess *immich.API, b store.DropBox, st Strictness, started time.Time) error {
	tag, err := sess.FindTag(ctx, b.InboxTag, false)
	if err != nil {
		return err
	}
	if tag == nil {
		return ErrNoPhotosYet
	}
	assets, err := sess.Search(ctx, immich.MetadataSearchDto{TagIds: &[]uuid.UUID{*tag}})
	if err != nil {
		return err
	}
	if len(assets) == 0 {
		return ErrNoPhotosYet
	}
	state := store.DropHighlight{BoxID: b.ID, Strictness: st.Name, Status: store.HighlightRunning, Total: len(assets), Started: started}
	var stateMu sync.Mutex
	progress := func(done int) {
		stateMu.Lock()
		defer stateMu.Unlock()
		state.Done = done
		_ = h.Store.SetDropHighlight(context.WithoutCancel(ctx), state)
	}
	progress(0)

	// look at each photo, four at a time, through the small picture Immich made of it
	results := make([]lookedAt, len(assets))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	var done atomic.Int64
	for i := range assets {
		results[i].a = assets[i]
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer func() {
				<-sem
				wg.Done()
				if d := int(done.Add(1)); d%10 == 0 || d == len(assets) {
					progress(d)
				}
			}()
			data, err := previewWhenReady(ctx, sess, assets[i].Id)
			if err != nil {
				return
			}
			if img, _, err := image.Decode(bytes.NewReader(data)); err == nil {
				q := analyze(img)
				results[i].q = &q
			}
		}(i)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}

	verdicts := judge(results, st)
	if hidden, pinned, err := h.Store.AssetMarks(ctx, b.ID); err == nil { // the host's picks win over the looks of a photo
		applyMarks(results, verdicts, hidden, pinned)
	}
	kept := map[uuid.UUID]bool{}
	var vs []store.Verdict
	for i, v := range verdicts {
		vs = append(vs, store.Verdict{AssetID: results[i].a.Id.String(), Verdict: v.verdict, Detail: v.detail, Name: results[i].a.OriginalFileName})
		if v.verdict == VerdictKept {
			kept[results[i].a.Id] = true
		}
	}
	albumName := b.Name + " best of"
	if err := h.fillAlbum(ctx, sess, albumName, assets, kept); err != nil {
		return fmt.Errorf("could not fill the album %q: %w", albumName, err)
	}
	if err := h.Store.ReplaceDropVerdicts(ctx, b.ID, vs); err != nil {
		return err
	}
	state.Status, state.Done, state.Kept, state.Album, state.Finished = store.HighlightDone, len(assets), len(kept), albumName, time.Now()
	stateMu.Lock()
	defer stateMu.Unlock()
	return h.Store.SetDropHighlight(context.WithoutCancel(ctx), state)
}

type judgement struct{ verdict, detail string }

// judge decides about every photo: too dark, too bright, blurred, or (of what is left) the same moment as a better one.
func judge(rs []lookedAt, st Strictness) []judgement {
	out := make([]judgement, len(rs))
	var cand []int
	for i, r := range rs {
		out[i] = judgement{verdict: VerdictKept}
		switch {
		case r.q == nil:
			out[i].detail = "could not be looked at"
		case r.q.Mean < st.DarkBelow:
			out[i] = judgement{VerdictDark, fmt.Sprintf("brightness %.0f of 255", r.q.Mean)}
		case r.q.Mean > st.BrightAbove:
			out[i] = judgement{VerdictBright, fmt.Sprintf("brightness %.0f of 255", r.q.Mean)}
		case r.q.Sharp < st.BlurBelow:
			out[i] = judgement{VerdictBlurry, fmt.Sprintf("detail %.0f, needs %.0f", r.q.Sharp, st.BlurBelow)}
		default:
			cand = append(cand, i)
		}
	}
	// near-identical shots within an hour of each other are one moment: only the sharpest stays
	sort.SliceStable(cand, func(x, y int) bool { return rs[cand[x]].a.FileCreatedAt.Before(rs[cand[y]].a.FileCreatedAt) })
	parent := make([]int, len(cand))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for i := range cand {
		for j := i + 1; j < len(cand); j++ {
			if rs[cand[j]].a.FileCreatedAt.Sub(rs[cand[i]].a.FileCreatedAt) > sameMoment {
				break
			}
			if hamming(rs[cand[i]].q.Hash, rs[cand[j]].q.Hash) <= st.SameWithin {
				parent[find(j)] = find(i)
			}
		}
	}
	best := map[int]int{} // group -> index into cand of its sharpest
	for i := range cand {
		g := find(i)
		if b, ok := best[g]; !ok || rs[cand[i]].q.Sharp > rs[cand[b]].q.Sharp {
			best[g] = i
		}
	}
	for i := range cand {
		if b := best[find(i)]; b != i {
			out[cand[i]] = judgement{VerdictDuplicate, "same moment as " + rs[cand[b]].a.OriginalFileName}
		}
	}
	return out
}

// applyMarks lets the host's picks win over what a photo looks like: a photo the host pinned is kept, whatever its
// brightness, sharpness or likeness to another; one the host hid is left out. A pick is by the photo's id in the library.
func applyMarks(rs []lookedAt, out []judgement, hidden, pinned map[string]bool) {
	for i, r := range rs {
		id := r.a.Id.String()
		switch {
		case hidden[id]:
			out[i] = judgement{VerdictHidden, "hidden by the host"}
		case pinned[id]:
			out[i] = judgement{VerdictKept, "pinned by the host"}
		}
	}
}

// fillAlbum makes the album hold exactly the kept photos among this box's, and leaves anything else the owner put in it alone.
func (h *Hub) fillAlbum(ctx context.Context, sess *immich.API, name string, boxAssets []immich.AssetResponseDto, keep map[uuid.UUID]bool) error {
	album, _, err := sess.FindAlbum(ctx, name)
	if err == nil && album == nil {
		var made uuid.UUID
		if made, err = sess.CreateAlbum(ctx, name); err == nil {
			album = &made
		}
	}
	if err != nil {
		return err
	}
	current, err := sess.AlbumAssets(ctx, *album)
	if err != nil {
		return err
	}
	in := map[uuid.UUID]bool{}
	for _, a := range current {
		in[a.Id] = true
	}
	var remove, add []uuid.UUID
	for _, a := range boxAssets {
		switch {
		case in[a.Id] && !keep[a.Id]:
			remove = append(remove, a.Id)
		case !in[a.Id] && keep[a.Id]:
			add = append(add, a.Id)
		}
	}
	for len(remove) > 0 {
		n := min(len(remove), 200)
		if err := sess.RemoveFromAlbum(ctx, *album, remove[:n]...); err != nil {
			return err
		}
		remove = remove[n:]
	}
	for len(add) > 0 {
		n := min(len(add), 200)
		if err := sess.AddToAlbum(ctx, *album, add[:n]...); err != nil {
			return err
		}
		add = add[n:]
	}
	return nil
}

// previewTries and previewWait bound how long the best-of waits for Immich to make the small picture of a photo that
// has just arrived (it makes them in the background, so a set judged straight after an event may not have them yet).
var (
	previewTries = 12
	previewWait  = 5 * time.Second
)

// previewWhenReady fetches the small picture of a photo, trying again while Immich has not made it. A photo whose
// picture never comes is kept, as one that cannot be looked at.
func previewWhenReady(ctx context.Context, sess *immich.API, id uuid.UUID) ([]byte, error) {
	var err error
	for try := 0; try < previewTries; try++ {
		var data []byte
		if data, _, err = sess.Thumbnail(ctx, id, true); err == nil && len(data) > 0 {
			return data, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(previewWait):
		}
	}
	return nil, err
}
