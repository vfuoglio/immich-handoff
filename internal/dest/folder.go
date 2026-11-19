package dest

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func init() {
	Register("folder", func(s Spec, _ Net) (Destination, error) { return NewFolder(s.Settings["root"]) })
}

// Folder is a Destination in a directory of the container's file system: a NAS share, an SMB or NFS mount, an rclone mount (which is how
// a cloud drive is reached). The app runs as an unprivileged user, so the folder must be writable by it. Files are written under a
// temporary name beside their place, checked, and moved into it, so a file appears whole or not at all and a reader never sees half of
// one.
type Folder struct {
	root string
}

// NewFolder makes a Destination of the directory root (an absolute path; it is not created).
func NewFolder(root string) (*Folder, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if !filepath.IsAbs(root) {
		return nil, errors.New("a folder's root must be an absolute path such as /destinations/nas")
	}
	return &Folder{root: root}, nil
}

const tmpPrefix = ".handoff-tmp-"

// local is the path on disk of a name under the root; it refuses a name that would leave the root.
func (f *Folder) local(name string) (string, error) {
	name = path.Clean("/" + strings.ReplaceAll(name, "\\", "/"))
	if name == "/" {
		return "", errors.New("a file needs a name")
	}
	p := filepath.Join(f.root, filepath.FromSlash(name))
	if rel, err := filepath.Rel(f.root, p); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q is outside the folder", name)
	}
	return p, nil
}

func (f *Folder) ref(p string) string {
	rel, _ := filepath.Rel(f.root, p)
	return filepath.ToSlash(rel)
}

func fileSum(p string) (string, int64, error) {
	fh, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer fh.Close()
	h := sha1.New()
	n, err := io.Copy(h, fh)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// Put implements Destination. A file with the name and the same content is left as it is (Existed); one with the name and other content
// is not touched, and the new file is written as "name (2).ext", "name (3).ext", and so on.
func (f *Folder) Put(ctx context.Context, name string, r io.Reader, size int64) (Stored, error) {
	target, err := f.local(name)
	if err != nil {
		return Stored{}, err
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Stored{}, fmt.Errorf("cannot make the folder %s: %w", f.ref(dir), err)
	}
	tmp, err := os.CreateTemp(dir, tmpPrefix+"*")
	if err != nil {
		return Stored{}, fmt.Errorf("cannot write in %s: %w", f.ref(dir), err)
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	h := sha1.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), &ctxReader{ctx: ctx, r: r})
	if err != nil {
		return Stored{}, err
	}
	if size >= 0 && n != size {
		return Stored{}, fmt.Errorf("%d bytes arrived, %d were expected", n, size)
	}
	if err := tmp.Sync(); err != nil {
		return Stored{}, err
	}
	if err := tmp.Close(); err != nil {
		return Stored{}, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	st := Stored{Size: n, Sum: sum}
	final := target
	for i := 2; ; i++ {
		info, err := os.Lstat(final)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return Stored{}, err
		}
		if info.Mode().IsRegular() {
			if have, hn, err := fileSum(final); err == nil && have == sum && hn == n {
				st.Ref, st.Existed = f.ref(final), true
				return st, nil // already there
			}
		}
		final = suffixedPath(target, i)
		if i > 10000 {
			return Stored{}, errors.New("too many files with that name")
		}
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return Stored{}, err
	}
	if err := os.Rename(tmpName, final); err != nil {
		return Stored{}, err
	}
	done = true
	st.Ref = f.ref(final)
	return st, nil
}

func suffixedPath(p string, n int) string {
	ext := filepath.Ext(p)
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(p, ext), n, ext)
}

// ctxReader stops a copy when the context is cancelled (a stalled transfer), between reads.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// Stat implements Destination.
func (f *Folder) Stat(_ context.Context, ref string) (Info, error) {
	p, err := f.local(ref)
	if err != nil {
		return Info{}, err
	}
	info, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Info{}, ErrNotFound
	}
	if err != nil {
		return Info{}, err
	}
	if !info.Mode().IsRegular() {
		return Info{}, ErrNotFound
	}
	sum, n, err := fileSum(p)
	if err != nil {
		return Info{}, err
	}
	return Info{Size: n, Sum: sum, Modified: info.ModTime()}, nil
}

// Delete implements Destination: the file goes (there is no trash), and the folders it leaves empty go with it, up to the root.
func (f *Folder) Delete(_ context.Context, ref string) error {
	p, err := f.local(ref)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for dir := filepath.Dir(p); dir != f.root && strings.HasPrefix(dir, f.root+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil { // not empty (or not ours to remove): stop
			break
		}
	}
	return nil
}

// List implements Destination: the files under a prefix, recursively (not the temporary files of a write that is going on).
func (f *Folder) List(ctx context.Context, prefix string) ([]Entry, error) {
	start := f.root
	if strings.Trim(prefix, "/") != "" {
		p, err := f.local(prefix)
		if err != nil {
			return nil, err
		}
		start = p
	}
	var out []Entry
	err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == start {
				return filepath.SkipAll
			}
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if d.IsDir() || !d.Type().IsRegular() || strings.HasPrefix(d.Name(), tmpPrefix) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, Entry{Ref: f.ref(p), Size: info.Size(), Modified: info.ModTime()})
		if len(out) > ListLimit {
			return ErrTooManyFiles
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, err
}

// Open implements Destination.
func (f *Folder) Open(_ context.Context, ref string) (io.ReadCloser, error) {
	p, err := f.local(ref)
	if err != nil {
		return nil, err
	}
	fh, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return fh, err
}

// Check implements Destination: the folder is there, it is a directory, and the app can write in it and remove what it wrote (it does, with
// a file of its own that it removes at once). The words say what to look at, because the usual cause is a share that is not mounted or a
// folder the app's user may not write to.
func (f *Folder) Check(context.Context) (string, error) {
	info, err := os.Stat(f.root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("the folder %s does not exist inside the container: is the share mounted there?", f.root)
	case err != nil:
		return "", fmt.Errorf("the folder %s cannot be looked at: %w", f.root, err)
	case !info.IsDir():
		return "", fmt.Errorf("%s is not a folder", f.root)
	}
	var b [6]byte
	rand.Read(b[:])
	test := filepath.Join(f.root, tmpPrefix+"check-"+hex.EncodeToString(b[:]))
	fh, err := os.OpenFile(test, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("the app cannot write in %s (it runs as user %d, which must be allowed to write there): %w", f.root, os.Getuid(), err)
	}
	_, werr := fh.WriteString(time.Now().UTC().Format(time.RFC3339))
	fh.Close()
	if err := os.Remove(test); err != nil {
		return "", fmt.Errorf("the app wrote in %s but could not remove its test file: %w", f.root, err)
	}
	if werr != nil {
		return "", fmt.Errorf("the app cannot write in %s: %w", f.root, werr)
	}
	return fmt.Sprintf("the folder %s exists and the app can write in it", f.root), nil
}
