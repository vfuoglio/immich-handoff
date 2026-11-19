package dest_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/dest"
)

// What an import needs of a destination, whatever its kind: list it (by folder, in pages, with sizes and times), look at one file, read it,
// and be told plainly that a file is not there or that the place holds too many files to list. The same steps for all four kinds.
func TestEveryKindOfDestinationCanBeListedLookedAtAndReadForAnImport(t *testing.T) {
	makers := map[string]func(t *testing.T) dest.Destination{
		"folder": func(t *testing.T) dest.Destination { f, _ := newFolder(t); return f },
		"webdav": func(t *testing.T) dest.Destination { s := newDAV(t); return s.dest(t, "hunter2") },
		"s3":     func(t *testing.T) dest.Destination { return newS3(t).dest(t, nil) },
		"sftp":   func(t *testing.T) dest.Destination { return newSFTP(t).dest(t, nil) },
	}
	for kind, make := range makers {
		t.Run(kind, func(t *testing.T) {
			d := make(t)
			ctx := context.Background()
			put(t, d, "a/x.jpg", "one")
			put(t, d, "a/b/y.mov", "two2")
			put(t, d, "z.heic", "three")
			all, err := d.List(ctx, "")
			if err != nil || len(all) != 3 || all[0].Ref != "a/b/y.mov" || all[1].Ref != "a/x.jpg" || all[2].Ref != "z.heic" {
				t.Fatalf("every file, by its path: %+v %v", all, err)
			}
			if all[0].Size != 4 || all[2].Size != 5 {
				t.Errorf("with their sizes: %+v", all)
			}
			for _, e := range all {
				if e.Modified.IsZero() {
					t.Errorf("%s has no time: a new file cannot be told from an old one", e.Ref)
				}
			}
			if sub, err := d.List(ctx, "a"); err != nil || len(sub) != 2 {
				t.Errorf("under a folder: %+v %v", sub, err)
			}
			if none, err := d.List(ctx, "no/such/folder"); err != nil || len(none) != 0 {
				t.Errorf("a folder that is not there lists nothing: %+v %v", none, err)
			}
			info, err := d.Stat(ctx, "a/x.jpg")
			if err != nil || info.Size != 3 || info.Modified.IsZero() {
				t.Errorf("%+v %v", info, err)
			}
			rc, err := d.Open(ctx, "a/x.jpg")
			if err != nil {
				t.Fatal(err)
			}
			if b, _ := io.ReadAll(rc); string(b) != "one" {
				t.Errorf("reads it: %q", b)
			}
			rc.Close()
			if _, err := d.Open(ctx, "a/x.jpg.xmp"); !errors.Is(err, dest.ErrNotFound) {
				t.Errorf("a sidecar that is not there is not found, plainly: %v", err)
			}
			if _, err := d.Stat(ctx, "nope.jpg"); !errors.Is(err, dest.ErrNotFound) {
				t.Errorf("%v", err)
			}
			// and a place that holds too many files says so, so that a folder inside it is followed instead
			dest.ListLimit = 2
			defer func() { dest.ListLimit = 200000 }()
			if _, err := d.List(ctx, ""); !errors.Is(err, dest.ErrTooManyFiles) {
				t.Errorf("too many to list: %v", err)
			}
			if sub, err := d.List(ctx, "a/b"); err != nil || len(sub) != 1 {
				t.Errorf("a folder inside is fine: %+v %v", sub, err)
			}
		})
	}
}
