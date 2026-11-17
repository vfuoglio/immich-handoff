package hub

import (
	"context"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
)

// Stacking by rule (#197): which of a person's photos belong in one stack. This file is the grouping alone: it reads names and
// EXIF fields it is given and asks nothing of Immich (docs/design/stacks-design.md).

// The signals a group is found by.
const (
	SignalPair  = "pair"  // a RAW and a JPEG of the same shot: the same base name in one folder, taken at the same moment
	SignalEdit  = "edit"  // a photo and an edit of it (IMG_1-edited.jpg): the same stem in one folder, taken at the same moment
	SignalBurst = "burst" // three or more shots from one camera, each a moment after the last
)

// Which photo is the cover of a stack.
const (
	CoverJPEG   = "jpeg"   // the plain JPEG, then the edits, then the RAW (the default)
	CoverRAW    = "raw"    // the RAW first
	CoverEdited = "edited" // an edit first
)

const (
	// sameShot is how far apart two files of one shot may have been taken (the clock is to the second, and a RAW and its JPEG
	// can be written a moment apart).
	sameShot = 2 * time.Second
	// burstGap is the longest pause between two shots of one burst, and burstMin the fewest shots that make one.
	burstGap = 1500 * time.Millisecond
	burstMin = 3
)

// StackSpec says what a stacking rule groups.
type StackSpec struct {
	Prefix string // only photos under this folder; empty: all
	Pair   bool
	Edit   bool
	Burst  bool
	Cover  string // CoverJPEG (the default), CoverRAW or CoverEdited
}

// StackPhoto is what the grouping needs to know about a photo.
type StackPhoto struct {
	ID          uuid.UUID
	Path        string // the original path
	Taken       time.Time
	Make, Model string
	Stacked     bool // already in a stack
}

// StackGroup is photos that belong in one stack, the cover first.
type StackGroup struct {
	Signal  string
	Members []StackPhoto
}

// AutoOK says whether a group may be stacked without asking once the person has let the rule go on by itself: the strong
// signals may, a burst never.
func (g StackGroup) AutoOK() bool { return g.Signal != SignalBurst }

var rawExt = map[string]bool{".cr2": true, ".cr3": true, ".crw": true, ".nef": true, ".nrw": true, ".arw": true, ".srf": true, ".sr2": true,
	".dng": true, ".raf": true, ".orf": true, ".rw2": true, ".pef": true, ".srw": true, ".raw": true, ".3fr": true, ".x3f": true, ".iiq": true, ".erf": true, ".mrw": true}

var editSuffixes = []string{"-edited", "_edited", "-edit", "_edit"}

func isRaw(p string) bool { return rawExt[strings.ToLower(path.Ext(p))] }

// stemOf is a file's name without its extension and without an edit's suffix, lower-cased, and whether it had one: "IMG_1-edited"
// and "IMG_1 (1)" are edits of "img_1".
func stemOf(p string) (stem string, edited bool) {
	b := strings.ToLower(strings.TrimSuffix(path.Base(p), path.Ext(p)))
	for _, s := range editSuffixes {
		if strings.HasSuffix(b, s) && len(b) > len(s) {
			return strings.TrimSuffix(b, s), true
		}
	}
	// "name (1)" and "name(1)": a copy a program or a person numbered
	if strings.HasSuffix(b, ")") {
		if i := strings.LastIndex(b, "("); i > 0 {
			digits := b[i+1 : len(b)-1]
			if digits != "" && strings.Trim(digits, "0123456789") == "" {
				return strings.TrimSpace(b[:i]), true
			}
		}
	}
	return b, false
}

// coverRank orders the members of a group for a cover: the lowest is the cover.
func coverRank(cover string, p StackPhoto) int {
	_, edited := stemOf(p.Path)
	raw := isRaw(p.Path)
	switch cover {
	case CoverRAW:
		switch {
		case raw:
			return 0
		case !edited:
			return 1
		}
		return 2
	case CoverEdited:
		switch {
		case edited:
			return 0
		case !raw:
			return 1
		}
		return 2
	}
	switch { // CoverJPEG
	case !raw && !edited:
		return 0
	case edited:
		return 1
	}
	return 2
}

func orderMembers(cover string, ms []StackPhoto) {
	sort.SliceStable(ms, func(i, j int) bool {
		ri, rj := coverRank(cover, ms[i]), coverRank(cover, ms[j])
		if ri != rj {
			return ri < rj
		}
		return ms[i].Path < ms[j].Path
	})
}

// PlanStacks groups the photos by the signals the spec asks for. A photo is in at most one group, and the strong signals come first
// (a pair or an edit, then a burst). A photo already in a stack is left alone, and so is every group that has one among its
// members: nothing is merged. Groups come in the order of their covers' paths.
func PlanStacks(spec StackSpec, photos []StackPhoto) []StackGroup {
	prefix := ""
	if p := strings.TrimRight(strings.TrimSpace(spec.Prefix), "/"); p != "" {
		prefix = p + "/"
	}
	var in []StackPhoto
	seen := map[uuid.UUID]bool{}
	for _, p := range photos {
		if seen[p.ID] || (prefix != "" && !strings.HasPrefix(p.Path, prefix)) {
			continue
		}
		seen[p.ID] = true
		in = append(in, p)
	}
	used := map[uuid.UUID]bool{}
	var groups []StackGroup
	add := func(signal string, ms []StackPhoto) {
		for _, m := range ms {
			if m.Stacked {
				return // a group with a stacked member is skipped whole
			}
		}
		orderMembers(spec.Cover, ms)
		for _, m := range ms {
			used[m.ID] = true
		}
		groups = append(groups, StackGroup{Signal: signal, Members: ms})
	}

	// the same stem in one folder, taken at the same moment: a pair when there is a RAW and another file, an edit when one is an edit
	if spec.Pair || spec.Edit {
		type key struct{ dir, stem string }
		byKey := map[key][]StackPhoto{}
		for _, p := range in {
			st, _ := stemOf(p.Path)
			k := key{path.Dir(p.Path), st}
			byKey[k] = append(byKey[k], p)
		}
		keys := make([]key, 0, len(byKey))
		for k := range byKey {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].dir != keys[j].dir {
				return keys[i].dir < keys[j].dir
			}
			return keys[i].stem < keys[j].stem
		})
		for _, k := range keys {
			list := byKey[k]
			if len(list) < 2 {
				continue
			}
			sort.SliceStable(list, func(i, j int) bool { return list[i].Taken.Before(list[j].Taken) })
			for _, cl := range clusters(list, sameShot) {
				if len(cl) < 2 {
					continue
				}
				var raw, other, edit bool
				for _, m := range cl {
					_, e := stemOf(m.Path)
					switch {
					case isRaw(m.Path):
						raw = true
					default:
						other = true
					}
					edit = edit || e
				}
				switch {
				case raw && other && spec.Pair:
					add(SignalPair, append([]StackPhoto(nil), cl...))
				case edit && spec.Edit:
					add(SignalEdit, append([]StackPhoto(nil), cl...))
				}
			}
		}
	}

	// a burst: three or more shots from one camera in one folder, each within a moment of the last
	if spec.Burst {
		type key struct{ dir, make, model string }
		byCam := map[key][]StackPhoto{}
		for _, p := range in {
			if used[p.ID] || p.Make == "" || p.Taken.IsZero() || isRaw(p.Path) {
				continue
			}
			k := key{path.Dir(p.Path), strings.ToLower(p.Make), strings.ToLower(p.Model)}
			byCam[k] = append(byCam[k], p)
		}
		keys := make([]key, 0, len(byCam))
		for k := range byCam {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			return keys[i].dir+keys[i].make+keys[i].model < keys[j].dir+keys[j].make+keys[j].model
		})
		for _, k := range keys {
			list := byCam[k]
			sort.SliceStable(list, func(i, j int) bool { return list[i].Taken.Before(list[j].Taken) })
			for _, cl := range clusters(list, burstGap) {
				if len(cl) >= burstMin {
					add(SignalBurst, append([]StackPhoto(nil), cl...))
				}
			}
		}
	}

	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Members[0].Path < groups[j].Members[0].Path })
	return groups
}

// clusters splits photos sorted by time into runs in which each is within gap of the one before.
func clusters(sorted []StackPhoto, gap time.Duration) [][]StackPhoto {
	var out [][]StackPhoto
	var cur []StackPhoto
	for _, p := range sorted {
		if len(cur) > 0 && p.Taken.Sub(cur[len(cur)-1].Taken) > gap {
			out = append(out, cur)
			cur = nil
		}
		cur = append(cur, p)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// StackPhotoFrom is a search result as the grouping needs it (it cannot tell whether the photo is stacked: see StackPhotos).
func StackPhotoFrom(a immich.AssetResponseDto) StackPhoto {
	p := StackPhoto{ID: a.Id, Path: a.OriginalPath, Taken: a.FileCreatedAt}
	if a.ExifInfo != nil {
		if a.ExifInfo.DateTimeOriginal != nil {
			p.Taken = *a.ExifInfo.DateTimeOriginal
		}
		if a.ExifInfo.Make != nil {
			p.Make = *a.ExifInfo.Make
		}
		if a.ExifInfo.Model != nil {
			p.Model = *a.ExifInfo.Model
		}
	}
	return p
}

// StackPhotos reads the person's photos for stacking with their key: images only, not trashed, with their EXIF and whether each is
// already in a stack. folder, when not empty, limits it to a folder (with a trailing slash is added here); createdAfter, when not
// zero, to photos that came to Immich after it.
func (h *Hub) StackPhotos(ctx context.Context, api *immich.API, me uuid.UUID, folder string, createdAfter time.Time) ([]StackPhoto, error) {
	if folder = strings.TrimRight(strings.TrimSpace(folder), "/"); folder != "" {
		folder += "/"
	}
	found, err := api.StackSearch(ctx, folder, createdAfter, true)
	if err != nil {
		return nil, err
	}
	// a search does not say which photos are stacked, but leaves them all out when asked not to include stacked ones
	loose, err := api.StackSearch(ctx, folder, createdAfter, false)
	if err != nil {
		return nil, err
	}
	unstacked := map[uuid.UUID]bool{}
	for _, a := range loose {
		unstacked[a.Id] = true
	}
	var out []StackPhoto
	for _, a := range found {
		if a.OwnerId != me || a.IsTrashed || (folder != "" && !strings.HasPrefix(a.OriginalPath, folder)) {
			continue
		}
		p := StackPhotoFrom(a)
		p.Stacked = !unstacked[a.Id]
		out = append(out, p)
	}
	return out, nil
}

// DryRunStacks is the read-only look: the groups a spec would stack among the person's photos now. It changes nothing.
func (h *Hub) DryRunStacks(ctx context.Context, api *immich.API, me uuid.UUID, s StackSpec) ([]StackGroup, error) {
	photos, err := h.StackPhotos(ctx, api, me, s.Prefix, time.Time{})
	if err != nil {
		return nil, err
	}
	return PlanStacks(s, photos), nil
}

// String writes a group as the dry run prints it: the signal and the files, the cover first.
func (g StackGroup) String() string {
	var names []string
	for _, m := range g.Members {
		names = append(names, path.Base(m.Path))
	}
	return g.Signal + ": " + strings.Join(names, ", ")
}
