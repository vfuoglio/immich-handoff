package dest_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/webdav"

	"github.com/vfuoglio/immich-handoff/internal/bridge"
	"github.com/vfuoglio/immich-handoff/internal/dest"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

type davServer struct {
	*httptest.Server
	dir      string
	down     atomic.Bool
	redirect atomic.Bool
	hang     atomic.Bool
	mu       sync.Mutex
	methods  map[string]int
}

// newDAV is a WebDAV server on a folder, with a login, that can be made to fail.
func newDAV(t *testing.T) *davServer {
	t.Helper()
	s := &davServer{dir: t.TempDir(), methods: map[string]int{}}
	h := &webdav.Handler{Prefix: "/dav", FileSystem: webdav.Dir(s.dir), LockSystem: webdav.NewMemLS()}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.methods[r.Method]++
		s.mu.Unlock()
		if s.down.Load() {
			http.Error(w, "down", 503)
			return
		}
		if s.redirect.Load() {
			http.Redirect(w, r, "/elsewhere", 302)
			return
		}
		if u, p, ok := r.BasicAuth(); !ok || u != "ann" || p != "hunter2" {
			w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
			http.Error(w, "no", 401)
			return
		}
		if s.hang.Load() && r.Method == "PUT" {
			io.Copy(io.Discard, r.Body) // the server notices a client that went away only once it has read the body
			<-r.Context().Done()
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *davServer) dest(t *testing.T, pass string) *dest.WebDAV {
	t.Helper()
	d, err := dest.NewWebDAV(s.URL+"/dav/photos", "ann", pass, dest.Net{AllowPrivate: true}) // the test server is on 127.0.0.1
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(s.dir, "photos"), 0o755)
	return d
}

func TestAWebDAVServerGetsFilesWholeCheckedSkippedWhenTheSameAndSuffixedWhenDifferent(t *testing.T) {
	s := newDAV(t)
	d := s.dest(t, "hunter2")
	a := put(t, d, "family/Bob/2026/03/IMG_1.jpg", "one")
	if a.Ref != "family/Bob/2026/03/IMG_1.jpg" || a.Existed || a.Size != 3 {
		t.Fatalf("%+v", a)
	}
	if b, _ := os.ReadFile(filepath.Join(s.dir, "photos/family/Bob/2026/03/IMG_1.jpg")); string(b) != "one" {
		t.Error("the bytes are there, in the folders it made")
	}
	if same := put(t, d, "family/Bob/2026/03/IMG_1.jpg", "one"); !same.Existed || same.Ref != a.Ref {
		t.Errorf("the same content is left alone: %+v", same)
	}
	if other := put(t, d, "family/Bob/2026/03/IMG_1.jpg", "two"); other.Ref != "family/Bob/2026/03/IMG_1 (2).jpg" || other.Existed {
		t.Errorf("another file with the name: %+v", other)
	}
	if other := put(t, d, "family/Bob/2026/03/IMG_1.jpg", "tw0"); other.Ref != "family/Bob/2026/03/IMG_1 (3).jpg" {
		t.Errorf("same size, other content: %+v", other)
	}
	var left []string
	filepath.WalkDir(filepath.Join(s.dir, "photos"), func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() && strings.Contains(e.Name(), ".handoff-tmp-") {
			left = append(left, p)
		}
		return nil
	})
	if len(left) != 0 {
		t.Errorf("no temporary file is left: %v", left)
	}
	info, err := d.Stat(context.Background(), a.Ref)
	if err != nil || info.Size != 3 {
		t.Errorf("%+v %v", info, err)
	}
	if _, err := d.Stat(context.Background(), "nope.jpg"); !errors.Is(err, dest.ErrNotFound) {
		t.Errorf("%v", err)
	}
	r, err := d.Open(context.Background(), a.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(r); string(b) != "one" {
		t.Error("open reads it")
	}
	r.Close()
	if err := d.Delete(context.Background(), a.Ref); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(context.Background(), a.Ref); err != nil {
		t.Errorf("a file that is not there is not an error: %v", err)
	}
	all, err := d.List(context.Background(), "")
	if err != nil || len(all) != 2 || all[0].Ref != "family/Bob/2026/03/IMG_1 (2).jpg" || all[0].Size != 3 {
		t.Errorf("listed folder by folder: %+v %v", all, err)
	}
	if sub, _ := d.List(context.Background(), "family/Bob"); len(sub) != 2 {
		t.Errorf("%+v", sub)
	}
}

func TestALargeFileIsStreamedToAWebDAVServerWithoutBeingHeldInMemory(t *testing.T) {
	s := newDAV(t)
	d := s.dest(t, "hunter2")
	big := make([]byte, 6<<20)
	rand.Read(big)
	st, err := d.Put(context.Background(), "big.mov", bytes.NewReader(big), int64(len(big)))
	if err != nil || st.Size != int64(len(big)) {
		t.Fatalf("%+v %v", st, err)
	}
	if got, _ := os.ReadFile(filepath.Join(s.dir, "photos/big.mov")); !bytes.Equal(got, big) {
		t.Error("the same bytes")
	}
	// a size that is not the body's is refused and leaves nothing
	if _, err := d.Put(context.Background(), "short.mov", bytes.NewReader([]byte("abc")), 10); err == nil {
		t.Error("a short body")
	}
	if _, err := os.Stat(filepath.Join(s.dir, "photos/short.mov")); err == nil {
		t.Error("no file for a failed write")
	}
}

func TestACheckOfAWebDAVServerSaysInWordsWhatIsWrongAndNeverShowsThePasswordOrTheAddress(t *testing.T) {
	s := newDAV(t)
	if msg, err := s.dest(t, "hunter2").Check(context.Background()); err != nil || !strings.Contains(msg, "can write in it") {
		t.Fatalf("%q %v", msg, err)
	}
	if left, _ := os.ReadDir(filepath.Join(s.dir, "photos")); len(left) != 0 {
		t.Errorf("the test file is removed: %v", left)
	}
	check := func(d dest.Destination) string {
		_, err := d.Check(context.Background())
		if err == nil {
			t.Fatal("it should fail")
		}
		for _, secret := range []string{"hunter2", "wrong", s.Listener.Addr().String()} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the message gives away %q: %v", secret, err)
			}
		}
		return err.Error()
	}
	if msg := check(s.dest(t, "wrong")); !strings.Contains(msg, "did not accept the user name and password") {
		t.Errorf("%q", msg)
	}
	missing, _ := dest.NewWebDAV(s.URL+"/dav/not-a-folder", "ann", "hunter2", dest.Net{AllowPrivate: true})
	if msg := check(missing); !strings.Contains(msg, "does not have that folder") {
		t.Errorf("%q", msg)
	}
	s.redirect.Store(true)
	if msg := check(s.dest(t, "hunter2")); !strings.Contains(msg, "redirect, which the app does not follow") {
		t.Errorf("%q", msg)
	}
	s.redirect.Store(false)
	s.down.Store(true)
	if msg := check(s.dest(t, "hunter2")); !strings.Contains(msg, "answered 503") {
		t.Errorf("%q", msg)
	}
	d := s.dest(t, "hunter2")
	s.Close()
	check(d) // unreachable
}

func TestAWebDAVAddressOnAPrivateNetworkNeedsTheAdministratorsSwitchAndOnlyHttpsIsOtherwiseAllowed(t *testing.T) {
	for _, u := range []string{"http://nas.example.org/dav", "https://127.0.0.1/dav", "https://192.168.1.20/dav", "https://user:pw@nas.example.org/dav", "ftp://x/y", ""} {
		if _, err := dest.NewWebDAV(u, "a", "b", dest.Net{}); !errors.Is(err, bridge.ErrBadAddress) {
			t.Errorf("%q: %v", u, err)
		}
	}
	if _, err := dest.NewWebDAV("https://cloud.example.org/remote.php/dav/files/me", "a", "b", dest.Net{}); err != nil {
		t.Errorf("a public https address: %v", err)
	}
	if _, err := dest.NewWebDAV("http://192.168.1.20:5005/dav", "a", "b", dest.Net{AllowPrivate: true}); err != nil {
		t.Errorf("allowed by the administrator: %v", err)
	}
	// and the server it names is not reached when the name points inside a private network
	d, err := dest.NewWebDAV("https://localhost/dav", "a", "b", dest.Net{})
	if err != nil {
		t.Skip("localhost is refused by name here")
	}
	if _, err := d.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "private network") {
		t.Errorf("a name that points inside is refused when it connects: %v", err)
	}
}

func TestAWriteThatHangsEndsWithTheContextAndLeavesNothing(t *testing.T) {
	s := newDAV(t)
	d := s.dest(t, "hunter2")
	s.hang.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := d.Put(ctx, "x.jpg", strings.NewReader("abc"), 3); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
	s.hang.Store(false)
	if left, _ := os.ReadDir(filepath.Join(s.dir, "photos")); len(left) != 0 {
		t.Errorf("%v", left)
	}
}

func TestAWebDAVServerAsAReceiverGetsThePhotoItsSidecarAndRecordAndTakeBackDeletesThemAll(t *testing.T) {
	s := newDAV(t)
	d := s.dest(t, "hunter2")
	a, id := photo("IMG_7.HEIC")
	rating := 5
	a.ExifInfo = &immich.ExifResponseDto{Rating: &rating}
	clip, cid := photo("IMG_7.MOV")
	api := immichWith(t, map[string][]byte{id: []byte("still"), cid: []byte("clip")}, false)
	r := &dest.Remote{Ident: "cloud", Name: "Nextcloud", Service: "family", D: d}
	cp, err := r.Deliver(context.Background(), api, a, &clip, syncer.RemoteJob{Sender: "Bob", Details: true})
	if err != nil || cp.Ref != "IMG_7.HEIC" || cp.Clip != "IMG_7.MOV" {
		t.Fatalf("%+v %v", cp, err)
	}
	for _, f := range []string{"IMG_7.HEIC", "IMG_7.HEIC.xmp", "IMG_7.HEIC.handoff.json", "IMG_7.MOV"} {
		if _, err := os.Stat(filepath.Join(s.dir, "photos", f)); err != nil {
			t.Errorf("%s is missing", f)
		}
	}
	rec, _ := os.ReadFile(filepath.Join(s.dir, "photos/IMG_7.HEIC.handoff.json"))
	if !strings.Contains(string(rec), `"verified": "size only"`) {
		t.Errorf("this server gives no checksum, and the record says so:\n%s", rec)
	}
	if err := r.TakeBackRefs(context.Background(), []string{cp.Ref, cp.Clip}); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(filepath.Join(s.dir, "photos")); len(left) != 0 {
		t.Errorf("everything is deleted: %v", left)
	}
}

func TestAWebDAVServerThatGivesAChecksumHasItComparedAndAWrongOneRefusesTheFile(t *testing.T) {
	s := newDAV(t)
	good := true
	// a server that says in every PROPFIND answer that it holds a SHA-1 (the real one, or a wrong one)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		s.Config.Handler.ServeHTTP(rec, r)
		body := rec.Body.Bytes()
		if r.Method == "PROPFIND" && rec.Code == 207 {
			sum := strings.Repeat("0", 40)
			if good {
				data, _ := os.ReadFile(filepath.Join(s.dir, "photos", filepath.Base(r.URL.Path)))
				sum = sha1hex(data)
			}
			body = bytes.ReplaceAll(body, []byte("</D:prop>"), []byte(`<oc:checksums xmlns:oc="http://owncloud.org/ns">SHA1:`+sum+`</oc:checksums></D:prop>`))
		}
		for k, v := range rec.Header() {
			if k != "Content-Length" {
				w.Header()[k] = v
			}
		}
		w.WriteHeader(rec.Code)
		w.Write(body)
	}))
	defer proxy.Close()
	os.MkdirAll(filepath.Join(s.dir, "photos"), 0o755)
	d, _ := dest.NewWebDAV(proxy.URL+"/dav/photos", "ann", "hunter2", dest.Net{AllowPrivate: true})
	st, err := d.Put(context.Background(), "a.jpg", strings.NewReader("hello"), 5)
	if err != nil || st.Sum != sha1hex([]byte("hello")) {
		t.Fatalf("the server's checksum is taken: %+v %v", st, err)
	}
	good = false
	if _, err := d.Put(context.Background(), "b.jpg", strings.NewReader("hello"), 5); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("a wrong checksum refuses the file: %v", err)
	}
}

func sha1hex(b []byte) string {
	h := sha1.Sum(b)
	return hex.EncodeToString(h[:])
}
