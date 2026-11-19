package dest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/bridge"
)

func init() {
	Register("webdav", func(s Spec, n Net) (Destination, error) {
		return NewWebDAV(s.Settings["url"], s.Settings["user"], s.Settings["password"], n)
	})
}

// WebDAV is a Destination on a WebDAV server (Nextcloud, ownCloud and others), written on the standard library: PUT, MKCOL, PROPFIND,
// MOVE and DELETE with Basic authentication (a password or an app password). It reaches the server only through the hardened client of
// bridges (https unless private addresses are allowed, no redirect, no proxy, the name resolved and judged when it connects).
type WebDAV struct {
	base *url.URL // the server and the path of the folder files go under
	user string
	pass string
	hc   *http.Client

	mu   sync.Mutex
	made map[string]bool // folders known to exist
}

// NewWebDAV makes a Destination of the folder at address (an https address such as https://cloud.example.org/remote.php/dav/files/me/Photos).
func NewWebDAV(address, user, pass string, n Net) (*WebDAV, error) {
	p := bridge.Policy{AllowPrivate: n.AllowPrivate}
	if _, err := p.Address(address); err != nil {
		return nil, err
	}
	if !strings.Contains(address, "://") {
		address = "https://" + strings.TrimSpace(address)
	}
	u, err := url.Parse(strings.TrimSpace(address))
	if err != nil {
		return nil, fmt.Errorf("%w: it cannot be read", bridge.ErrBadAddress)
	}
	u.RawQuery, u.Fragment = "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return &WebDAV{base: u, user: user, pass: pass, hc: p.HTTPClient(), made: map[string]bool{}}, nil
}

// clean takes the server's address out of a message: messages end up in logs, pages and notifications.
func (w *WebDAV) clean(err error) error {
	msg := err.Error()
	msg = strings.ReplaceAll(msg, w.base.String(), "the WebDAV server")
	msg = strings.ReplaceAll(msg, w.base.Host, "the WebDAV server")
	return errors.New(msg)
}

func (w *WebDAV) urlFor(ref string) string {
	u := *w.base
	segs := []string{strings.TrimRight(w.base.EscapedPath(), "/")}
	for _, s := range strings.Split(strings.Trim(ref, "/"), "/") {
		if s != "" {
			segs = append(segs, url.PathEscape(s))
		}
	}
	u.Path = ""
	u.RawPath = ""
	return u.String() + strings.Join(segs, "/")
}

// refOf is the name under the root of an address the server gave in a listing.
func (w *WebDAV) refOf(href string) string {
	if u, err := url.Parse(href); err == nil {
		href = u.Path
	}
	href = strings.TrimPrefix(strings.TrimSuffix(href, "/"), strings.TrimRight(w.base.Path, "/"))
	return strings.Trim(href, "/")
}

func (w *WebDAV) do(ctx context.Context, method, target string, body io.Reader, size int64, hdr map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	if body != nil && size >= 0 {
		req.ContentLength = size
	}
	if w.user != "" || w.pass != "" {
		req.SetBasicAuth(w.user, w.pass)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := w.hc.Do(req)
	if err != nil {
		return nil, w.clean(err)
	}
	return resp, nil
}

// failure says in words what an answer of the server means for what the app was doing.
func failure(resp *http.Response, doing string) error {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("the WebDAV server did not accept the user name and password (while %s)", doing)
	case http.StatusForbidden:
		return fmt.Errorf("the WebDAV server does not allow this user to do that (while %s)", doing)
	case http.StatusNotFound:
		return fmt.Errorf("the WebDAV server does not have that folder (while %s): check the address", doing)
	case http.StatusLocked:
		return fmt.Errorf("the WebDAV server says the file is locked (while %s)", doing)
	case http.StatusInsufficientStorage, http.StatusRequestEntityTooLarge:
		return fmt.Errorf("the WebDAV server is out of room or refuses a file this large (while %s)", doing)
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return fmt.Errorf("the WebDAV server answers with a redirect, which the app does not follow: use the address it redirects to (while %s)", doing)
	}
	return fmt.Errorf("the WebDAV server answered %d (while %s)", resp.StatusCode, doing)
}

// ok is a 2xx answer.
func ok(resp *http.Response) bool { return resp.StatusCode >= 200 && resp.StatusCode < 300 }

type propEntry struct {
	Ref      string
	Dir      bool
	Size     int64
	Sum      string // a SHA-1 the server holds, when it says so (Nextcloud and ownCloud do for files that were given one)
	Modified time.Time
}

type multistatus struct {
	Responses []struct {
		Href     string `xml:"href"`
		Propstat []struct {
			Status string `xml:"status"`
			Prop   struct {
				Len       string `xml:"getcontentlength"`
				Mod       string `xml:"getlastmodified"`
				Checksums string `xml:"checksums"`
				Type      struct {
					Collection *struct{} `xml:"collection"`
				} `xml:"resourcetype"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

const propfindBody = `<?xml version="1.0" encoding="utf-8"?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop><d:getcontentlength/><d:getlastmodified/><d:resourcetype/><oc:checksums/></d:prop></d:propfind>`

// propfind asks about ref (depth 0: itself, 1: and what is directly in it). A 404 is (nil, nil).
func (w *WebDAV) propfind(ctx context.Context, ref string, depth int) ([]propEntry, error) {
	resp, err := w.do(ctx, "PROPFIND", w.urlFor(ref), strings.NewReader(propfindBody), int64(len(propfindBody)), map[string]string{"Depth": strconv.Itoa(depth), "Content-Type": "application/xml"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, resp.Body)
		return nil, nil
	}
	if resp.StatusCode != http.StatusMultiStatus {
		return nil, failure(resp, "looking at "+path.Base("/"+ref))
	}
	var ms multistatus
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&ms); err != nil {
		return nil, fmt.Errorf("the WebDAV server's answer could not be read: %w", err)
	}
	var out []propEntry
	for _, r := range ms.Responses {
		e := propEntry{Ref: w.refOf(r.Href)}
		for _, ps := range r.Propstat {
			if !strings.Contains(ps.Status, " 200") {
				continue
			}
			e.Dir = e.Dir || ps.Prop.Type.Collection != nil
			if n, err := strconv.ParseInt(strings.TrimSpace(ps.Prop.Len), 10, 64); err == nil {
				e.Size = n
			}
			if t, err := http.ParseTime(strings.TrimSpace(ps.Prop.Mod)); err == nil {
				e.Modified = t
			}
			for _, c := range strings.Fields(ps.Prop.Checksums) {
				if len(c) > 5 && strings.EqualFold(c[:5], "SHA1:") {
					e.Sum = strings.ToLower(c[5:])
				}
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// self is the entry of a depth-0 answer, or nil when there is none.
func self(es []propEntry) *propEntry {
	if len(es) == 0 {
		return nil
	}
	return &es[0]
}

// mkdirs makes the folders of dir, top down, remembering the ones it knows exist.
func (w *WebDAV) mkdirs(ctx context.Context, dir string) error {
	cur := ""
	for _, seg := range strings.Split(strings.Trim(dir, "/"), "/") {
		if seg == "" || seg == "." {
			continue
		}
		cur = path.Join(cur, seg)
		w.mu.Lock()
		known := w.made[cur]
		w.mu.Unlock()
		if known {
			continue
		}
		resp, err := w.do(ctx, "MKCOL", w.urlFor(cur), nil, -1, nil)
		if err != nil {
			return err
		}
		switch resp.StatusCode {
		case http.StatusCreated, http.StatusMethodNotAllowed, http.StatusOK: // made now, or there already
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		default:
			err := failure(resp, "making the folder "+cur)
			resp.Body.Close()
			return err
		}
		w.mu.Lock()
		w.made[cur] = true
		w.mu.Unlock()
	}
	return nil
}

func randName() string {
	var b [6]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (w *WebDAV) cleanName(name string) (string, error) {
	name = path.Clean("/" + strings.ReplaceAll(name, "\\", "/"))
	if name == "/" {
		return "", errors.New("a file needs a name")
	}
	return strings.TrimPrefix(name, "/"), nil
}

// sumOf reads a file of the server and returns its SHA-1.
func (w *WebDAV) sumOf(ctx context.Context, ref string) (string, error) {
	resp, err := w.do(ctx, "GET", w.urlFor(ref), nil, -1, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if !ok(resp) {
		return "", failure(resp, "reading a file")
	}
	h := sha1.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Put implements Destination. The bytes go to a temporary name in the file's folder (so nobody sees half a file), the server's own size is
// checked, and the file is moved into place: a name already taken by a file with the same content (the server's checksum when it gives
// one, otherwise a read of the file) is left alone; a different one gets "name (2).ext".
func (w *WebDAV) Put(ctx context.Context, name string, r io.Reader, size int64) (Stored, error) {
	name, err := w.cleanName(name)
	if err != nil {
		return Stored{}, err
	}
	dir := path.Dir(name)
	if dir == "." {
		dir = ""
	}
	if err := w.mkdirs(ctx, dir); err != nil {
		return Stored{}, err
	}
	tmp := path.Join(dir, tmpPrefix+randName())
	h := sha1.New()
	counted := &countingReader{r: io.TeeReader(r, h)}
	resp, err := w.do(ctx, "PUT", w.urlFor(tmp), counted, size, nil)
	if err != nil {
		w.Delete(context.WithoutCancel(ctx), tmp)
		return Stored{}, err
	}
	if !ok(resp) {
		err := failure(resp, "writing a file")
		resp.Body.Close()
		w.Delete(context.WithoutCancel(ctx), tmp)
		return Stored{}, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	cleanup := func() { w.Delete(context.WithoutCancel(ctx), tmp) }
	n, sum := counted.n, hex.EncodeToString(h.Sum(nil))
	if size >= 0 && n != size {
		cleanup()
		return Stored{}, fmt.Errorf("%d bytes arrived, %d were expected", n, size)
	}
	es, err := w.propfind(ctx, tmp, 0)
	if err != nil {
		cleanup()
		return Stored{}, err
	}
	held := self(es)
	if held == nil || held.Size != n {
		cleanup()
		got := int64(0)
		if held != nil {
			got = held.Size
		}
		return Stored{}, fmt.Errorf("the WebDAV server holds %d bytes of the file, %d were sent", got, n)
	}
	st := Stored{Size: n, Sum: held.Sum} // the server's own checksum when it gives one; empty when it does not
	if st.Sum != "" && st.Sum != sum {
		cleanup()
		return Stored{}, errors.New("the WebDAV server's checksum of the file differs from what was sent")
	}
	for i := 1; i < 10000; i++ {
		final := name
		if i > 1 {
			final = suffixedRef(name, i)
		}
		es, err := w.propfind(ctx, final, 0)
		if err != nil {
			cleanup()
			return Stored{}, err
		}
		if e := self(es); e != nil { // taken
			same := false
			if e.Size == n {
				have := e.Sum
				if have == "" {
					if have, err = w.sumOf(ctx, final); err != nil {
						cleanup()
						return Stored{}, err
					}
				}
				same = have == sum
			}
			if same {
				cleanup()
				st.Ref, st.Existed = final, true
				return st, nil
			}
			continue
		}
		resp, err := w.do(ctx, "MOVE", w.urlFor(tmp), nil, -1, map[string]string{"Destination": w.urlFor(final), "Overwrite": "F"})
		if err != nil {
			cleanup()
			return Stored{}, err
		}
		switch {
		case ok(resp):
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			st.Ref = final
			return st, nil
		case resp.StatusCode == http.StatusPreconditionFailed: // somebody made it a moment ago: the next name
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		default:
			err := failure(resp, "putting the file in place")
			resp.Body.Close()
			cleanup()
			return Stored{}, err
		}
	}
	cleanup()
	return Stored{}, errors.New("too many files with that name")
}

func suffixedRef(p string, n int) string {
	ext := path.Ext(p)
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(p, ext), n, ext)
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Stat implements Destination.
func (w *WebDAV) Stat(ctx context.Context, ref string) (Info, error) {
	ref, err := w.cleanName(ref)
	if err != nil {
		return Info{}, err
	}
	es, err := w.propfind(ctx, ref, 0)
	if err != nil {
		return Info{}, err
	}
	e := self(es)
	if e == nil || e.Dir {
		return Info{}, ErrNotFound
	}
	return Info{Size: e.Size, Sum: e.Sum, Modified: e.Modified}, nil
}

// Delete implements Destination: the file goes (a WebDAV server may keep its own trash). Folders it leaves empty stay: deleting a folder
// on WebDAV deletes what is in it, which is not worth the risk of a file that arrived a moment ago.
func (w *WebDAV) Delete(ctx context.Context, ref string) error {
	ref, err := w.cleanName(ref)
	if err != nil {
		return err
	}
	resp, err := w.do(ctx, "DELETE", w.urlFor(ref), nil, -1, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if ok(resp) || resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return failure(resp, "deleting a file")
}

const (
	listMaxDepth   = 16
	listMaxEntries = 200000
)

// List implements Destination: the files under a prefix, found folder by folder (a server may refuse a listing of everything at once).
func (w *WebDAV) List(ctx context.Context, prefix string) ([]Entry, error) {
	prefix = strings.Trim(strings.ReplaceAll(prefix, "\\", "/"), "/")
	var out []Entry
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		es, err := w.propfind(ctx, dir, 1)
		if err != nil {
			return err
		}
		for _, e := range es {
			if e.Ref == dir || e.Ref == "" && dir == "" {
				continue
			}
			if e.Dir {
				if depth < listMaxDepth {
					if err := walk(e.Ref, depth+1); err != nil {
						return err
					}
				}
				continue
			}
			if strings.HasPrefix(path.Base(e.Ref), tmpPrefix) {
				continue
			}
			out = append(out, Entry{Ref: e.Ref, Size: e.Size, Modified: e.Modified})
			if len(out) > listMaxEntries {
				return errors.New("the destination holds too many files to list")
			}
		}
		return ctx.Err()
	}
	if err := walk(prefix, 0); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, nil
}

// Open implements Destination.
func (w *WebDAV) Open(ctx context.Context, ref string) (io.ReadCloser, error) {
	ref, err := w.cleanName(ref)
	if err != nil {
		return nil, err
	}
	resp, err := w.do(ctx, "GET", w.urlFor(ref), nil, -1, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, ErrNotFound
	}
	if !ok(resp) {
		err := failure(resp, "reading a file")
		resp.Body.Close()
		return nil, err
	}
	return resp.Body, nil
}

// Check implements Destination: the address answers like a WebDAV folder, the login is accepted, and the app can write a small file there
// and remove it.
func (w *WebDAV) Check(ctx context.Context) (string, error) {
	es, err := w.propfind(ctx, "", 0)
	if err != nil {
		return "", err
	}
	if e := self(es); e == nil {
		return "", errors.New("the WebDAV server does not have that folder: check the address, and that the folder exists")
	} else if !e.Dir {
		return "", errors.New("that address is a file, not a WebDAV folder")
	}
	test := tmpPrefix + "check-" + randName()
	body := []byte(time.Now().UTC().Format(time.RFC3339))
	resp, err := w.do(ctx, "PUT", w.urlFor(test), bytes.NewReader(body), int64(len(body)), nil)
	if err != nil {
		return "", err
	}
	if !ok(resp) {
		err := failure(resp, "writing a test file")
		resp.Body.Close()
		return "", err
	}
	resp.Body.Close()
	if err := w.Delete(ctx, test); err != nil {
		return "", fmt.Errorf("the app wrote a test file but could not remove it: %w", err)
	}
	return "the WebDAV folder answers, the login is accepted and the app can write in it", nil
}
