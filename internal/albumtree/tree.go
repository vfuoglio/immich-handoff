// Package albumtree shows albums whose names say where they belong ("Trips / 2024 / Rome") as a tree. Immich has no nested albums;
// this only reads the names. It is pure: no Immich, no pages.
package albumtree

import (
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// MaxDepth is how many levels of folders a name is split into; the rest stays in the last one.
	MaxDepth = 8
	// MaxName is the longest album name (and folder name) the app writes.
	MaxName = 100
)

// Entry is an album to place in the tree.
type Entry struct {
	ID     string
	Name   string // the album's whole name
	Count  int    // photos
	Cover  string // an id the page can ask a cover for (empty: none)
	Owner  string // for an album shared with the person: who owns it
	Shared bool   // shared with the person, not their own
}

// Node is a folder in the tree: its albums (the ones whose path ends here) and its folders.
type Node struct {
	Name     string
	Path     string // the folders from the top, joined by " / "
	Albums   []Leaf
	Children []*Node
	// Albums and Photos are counted over this folder and everything below it.
	AlbumCount, PhotoCount int
}

// Leaf is an album at a node, with the last part of its name.
type Leaf struct {
	Entry
	Title string
}

// Clean makes a name fit an album: no control characters, spaces collapsed, at most MaxName characters.
func Clean(n string) string {
	n = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, n)
	n = strings.Join(strings.Fields(n), " ")
	for utf8.RuneCountInString(n) > MaxName {
		_, size := utf8.DecodeLastRuneInString(n)
		n = n[:len(n)-size]
	}
	return strings.TrimSpace(n)
}

// Split cuts an album name into its folders and its own title: "Trips / 2024 / Rome" and "Trips/2024/Rome" are the same. Empty parts
// are dropped. A name with no slash is a title with no folders. Beyond MaxDepth levels, the rest stays in the title.
func Split(name string) (folders []string, title string) {
	var parts []string
	for _, p := range strings.Split(name, "/") {
		if c := Clean(p); c != "" {
			parts = append(parts, c)
		}
	}
	if len(parts) == 0 {
		return nil, Clean(name)
	}
	if len(parts) > MaxDepth+1 {
		parts = append(parts[:MaxDepth], strings.Join(parts[MaxDepth:], " / "))
	}
	return parts[:len(parts)-1], parts[len(parts)-1]
}

// Join writes folders and a title as a name: "Trips / 2024 / Rome".
func Join(folders []string, title string) string {
	return strings.Join(append(append([]string(nil), folders...), title), " / ")
}

// Build places the entries in a tree. The top node has no name. Folders and albums are ordered by name, ignoring case.
func Build(entries []Entry) *Node {
	root := &Node{}
	for _, e := range entries {
		folders, title := Split(e.Name)
		n := root
		for _, f := range folders {
			var next *Node
			for _, c := range n.Children {
				if strings.EqualFold(c.Name, f) {
					next = c
					break
				}
			}
			if next == nil {
				path := f
				if n.Path != "" {
					path = n.Path + " / " + f
				}
				next = &Node{Name: f, Path: path}
				n.Children = append(n.Children, next)
			}
			n = next
		}
		n.Albums = append(n.Albums, Leaf{Entry: e, Title: title})
	}
	root.finish()
	return root
}

func (n *Node) finish() {
	sort.SliceStable(n.Children, func(i, j int) bool { return strings.ToLower(n.Children[i].Name) < strings.ToLower(n.Children[j].Name) })
	sort.SliceStable(n.Albums, func(i, j int) bool { return strings.ToLower(n.Albums[i].Title) < strings.ToLower(n.Albums[j].Title) })
	n.AlbumCount = len(n.Albums)
	for _, a := range n.Albums {
		n.PhotoCount += a.Count
	}
	for _, c := range n.Children {
		c.finish()
		n.AlbumCount += c.AlbumCount
		n.PhotoCount += c.PhotoCount
	}
}

// Folders lists every folder path in the tree ("Trips", "Trips / 2024"), in order, for a menu to move an album into.
func (n *Node) Folders() []string {
	var out []string
	for _, c := range n.Children {
		out = append(out, c.Path)
		out = append(out, c.Folders()...)
	}
	return out
}

// Rename is the name an album gets when it is moved into a folder path (empty: the top): the folders and the album's own title.
// The folder may be written with either separator. It fails if nothing is left of the title.
func Rename(oldName, folder string) (string, bool) {
	if strings.TrimSpace(strings.ReplaceAll(oldName, "/", " ")) == "" {
		return "", false
	}
	_, title := Split(oldName)
	var folders []string
	for _, p := range strings.Split(folder, "/") {
		if c := Clean(p); c != "" {
			folders = append(folders, c)
		}
	}
	name := Join(folders, title)
	if utf8.RuneCountInString(name) > MaxName {
		return "", false
	}
	return name, true
}
