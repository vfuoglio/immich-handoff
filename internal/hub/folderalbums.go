package hub

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
)

// Albums made from the folders of an external library (#198). Immich keeps the real path of the files of an external
// library, so the folder a photo is in can be read from its path. This file turns paths into the albums a person's folders
// would make; it asks nothing of Immich beyond one search and changes nothing (docs/design/folders-design.md).

var (
	ErrBadFolder  = errors.New("write the folder as a path, such as /external/photos")
	ErrBadDepth   = errors.New("the folder level is a number from 0 to 8")
	ErrBadNaming  = errors.New("name the album after the last folder, or after the parent and the last folder")
	ErrNoFolderPh = errors.New("none of your photos is under that folder")
)

// Namings of a folder album.
const (
	NameLast   = "last"   // the last folder: "paris"
	NameParent = "parent" // the parent and the last folder: "2025-02 / paris"
)

// FolderMaxDepth is the deepest level a rule may ask for.
const FolderMaxDepth = 8

// folderAlbumName limits a name the way the album forms do.
const folderAlbumName = 100

// FolderSpec says which folders of the person's photos become albums.
type FolderSpec struct {
	Prefix string // the folder to look under, "/external/photos"
	Depth  int    // 0: the folder each photo is in; n: the folder n levels below Prefix
	Naming string // NameLast (the default) or NameParent
}

// Clean checks a spec and returns it with the prefix written as a folder: one leading slash, no trailing slash.
func (s FolderSpec) Clean() (FolderSpec, error) {
	p := strings.TrimSpace(s.Prefix)
	if p == "" || !strings.HasPrefix(p, "/") || strings.Contains(p, "\x00") || strings.Contains(p, "//") || len(p) > 500 {
		return s, ErrBadFolder
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." {
			return s, ErrBadFolder
		}
	}
	p = strings.TrimRight(p, "/")
	if p == "" {
		return s, ErrBadFolder // the whole disk is not a folder to make albums from
	}
	if s.Depth < 0 || s.Depth > FolderMaxDepth {
		return s, ErrBadDepth
	}
	switch s.Naming {
	case "":
		s.Naming = NameLast
	case NameLast, NameParent:
	default:
		return s, ErrBadNaming
	}
	s.Prefix = p
	return s, nil
}

// folderQuery is what is sent to Immich: the folder with a trailing slash, so that /photos does not also find /photos-old.
func (s FolderSpec) query() string { return s.Prefix + "/" }

// FolderAlbum is one album a spec would make: its name, the folders that feed it and its photos.
type FolderAlbum struct {
	Name    string
	Folders []string // the folders (below the prefix) whose photos it holds; several when the name is the same
	Photos  []uuid.UUID
}

// FolderPlan is what a spec would make, and what it leaves out.
type FolderPlan struct {
	Albums   []FolderAlbum
	Loose    int         // photos directly in the prefix (or shallower than the level asked for): no folder, no album
	LooseIDs []uuid.UUID // the same photos, so that a rule can remember it has looked at them
}

// PathAsset is a photo with the path Immich keeps for it.
type PathAsset struct {
	ID      uuid.UUID
	Path    string
	Created time.Time // when it came to Immich
}

// albumFolder is the folder a photo's album is made from: relative to the prefix, or "" when the photo has none at that level.
func (s FolderSpec) albumFolder(path string) (string, bool) {
	if !strings.HasPrefix(path, s.query()) {
		return "", false // Immich matches anywhere in the path: this is not under the folder
	}
	rest := path[len(s.query()):]
	parts := strings.Split(rest, "/")
	dirs := parts[:len(parts)-1] // the folders between the prefix and the file
	if len(dirs) == 0 {
		return "", false
	}
	if s.Depth == 0 {
		return strings.Join(dirs, "/"), true
	}
	if len(dirs) < s.Depth {
		return "", false
	}
	return strings.Join(dirs[:s.Depth], "/"), true
}

// albumName names the album of a folder (relative to the prefix).
func (s FolderSpec) albumName(folder string) string {
	segs := strings.Split(folder, "/")
	name := segs[len(segs)-1]
	if s.Naming == NameParent {
		parent := ""
		if len(segs) >= 2 {
			parent = segs[len(segs)-2]
		} else {
			parent = s.Prefix[strings.LastIndex(s.Prefix, "/")+1:]
		}
		name = parent + " / " + name
	}
	return cleanAlbumName(name)
}

// cleanAlbumName makes a folder name fit an album: no control characters, spaces collapsed, at most 100 characters.
func cleanAlbumName(n string) string {
	n = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, n)
	n = strings.Join(strings.Fields(n), " ")
	for utf8.RuneCountInString(n) > folderAlbumName {
		_, size := utf8.DecodeLastRuneInString(n)
		n = n[:len(n)-size]
	}
	return strings.TrimSpace(n)
}

// PlanFolders turns the person's photos into the albums the spec makes. Photos that are not under the folder (Immich
// matches anywhere in a path) are ignored; photos with no folder at the level asked for are counted as loose. Folders
// whose albums have the same name make one album. Albums come in name order, and so do the folders and the photos in each.
func PlanFolders(s FolderSpec, assets []PathAsset) (FolderPlan, error) {
	s, err := s.Clean()
	if err != nil {
		return FolderPlan{}, err
	}
	byName := map[string]*FolderAlbum{}
	seen := map[uuid.UUID]bool{}
	var plan FolderPlan
	for _, a := range assets {
		if seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		folder, ok := s.albumFolder(a.Path)
		if !ok {
			if strings.HasPrefix(a.Path, s.query()) {
				plan.Loose++
				plan.LooseIDs = append(plan.LooseIDs, a.ID)
			}
			continue
		}
		name := s.albumName(folder)
		if name == "" {
			plan.Loose++
			plan.LooseIDs = append(plan.LooseIDs, a.ID)
			continue
		}
		al := byName[name]
		if al == nil {
			al = &FolderAlbum{Name: name}
			byName[name] = al
		}
		al.Photos = append(al.Photos, a.ID)
		have := false
		for _, f := range al.Folders {
			if f == folder {
				have = true
			}
		}
		if !have {
			al.Folders = append(al.Folders, folder)
		}
	}
	for _, al := range byName {
		sort.Strings(al.Folders)
		sort.Slice(al.Photos, func(i, j int) bool { return al.Photos[i].String() < al.Photos[j].String() })
		plan.Albums = append(plan.Albums, *al)
	}
	sort.Slice(plan.Albums, func(i, j int) bool { return plan.Albums[i].Name < plan.Albums[j].Name })
	return plan, nil
}

// String writes a plan as the dry run prints it.
func (p FolderPlan) String() string {
	var b strings.Builder
	n := 0
	for _, al := range p.Albums {
		n += len(al.Photos)
		fmt.Fprintf(&b, "%s: %d photo(s) from %s\n", al.Name, len(al.Photos), strings.Join(al.Folders, ", "))
	}
	fmt.Fprintf(&b, "%d album(s), %d photo(s)", len(p.Albums), n)
	if p.Loose > 0 {
		fmt.Fprintf(&b, ", %d photo(s) with no folder at that level", p.Loose)
	}
	return b.String()
}

// FolderPhotos reads the person's photos under the folder with the person's key: one search through every page, only the
// person's own, not trashed. createdAfter, when not zero, lists only photos that came to Immich after it.
func (h *Hub) FolderPhotos(ctx context.Context, api *immich.API, me uuid.UUID, s FolderSpec, createdAfter ...time.Time) ([]PathAsset, error) {
	s, err := s.Clean()
	if err != nil {
		return nil, err
	}
	var after time.Time
	if len(createdAfter) > 0 {
		after = createdAfter[0]
	}
	found, err := api.PathSearch(ctx, s.query(), after)
	if err != nil {
		return nil, err
	}
	var out []PathAsset
	for _, a := range found {
		if a.OwnerId != me || a.IsTrashed || !strings.HasPrefix(a.OriginalPath, s.query()) {
			continue
		}
		out = append(out, PathAsset{ID: a.Id, Path: a.OriginalPath, Created: a.CreatedAt})
	}
	return out, nil
}

// DryRunFolders is the read-only look: the albums a spec would make for the person's photos now. It changes nothing.
func (h *Hub) DryRunFolders(ctx context.Context, api *immich.API, me uuid.UUID, s FolderSpec) (FolderPlan, error) {
	photos, err := h.FolderPhotos(ctx, api, me, s)
	if err != nil {
		return FolderPlan{}, err
	}
	return PlanFolders(s, photos)
}
