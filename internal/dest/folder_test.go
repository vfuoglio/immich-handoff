package dest_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/dest"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

func newFolder(t *testing.T) (*dest.Folder, string) {
	t.Helper()
	root := t.TempDir()
	f, err := dest.NewFolder(root)
	if err != nil {
		t.Fatal(err)
	}
	return f, root
}

func put(t *testing.T, d dest.Destination, name, body string) dest.Stored {
	t.Helper()
	st, err := d.Put(context.Background(), name, bytes.NewReader([]byte(body)), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestAFolderWritesWholeFilesChecksThemSkipsTheSameAndSuffixesADifferentOne(t *testing.T) {
	f, root := newFolder(t)
	a := put(t, f, "family/Bob/2026/03/IMG_1.jpg", "one")
	if a.Ref != "family/Bob/2026/03/IMG_1.jpg" || a.Existed || a.Size != 3 || len(a.Sum) != 40 {
		t.Fatalf("%+v", a)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "family/Bob/2026/03/IMG_1.jpg")); string(b) != "one" {
		t.Error("the bytes are there")
	}
	if same := put(t, f, "family/Bob/2026/03/IMG_1.jpg", "one"); !same.Existed || same.Ref != a.Ref {
		t.Errorf("the same file is left alone: %+v", same)
	}
	if other := put(t, f, "family/Bob/2026/03/IMG_1.jpg", "two"); other.Ref != "family/Bob/2026/03/IMG_1 (2).jpg" || other.Existed {
		t.Errorf("a different file with the name: %+v", other)
	}
	if third := put(t, f, "family/Bob/2026/03/IMG_1.jpg", "three"); third.Ref != "family/Bob/2026/03/IMG_1 (3).jpg" {
		t.Errorf("%+v", third)
	}
	if again := put(t, f, "family/Bob/2026/03/IMG_1.jpg", "two"); !again.Existed || again.Ref != "family/Bob/2026/03/IMG_1 (2).jpg" {
		t.Errorf("a repeat of the second one finds it: %+v", again)
	}
	// nothing temporary is left behind, and a short body is refused without leaving a file
	if _, err := f.Put(context.Background(), "x/short.jpg", strings.NewReader("ab"), 10); err == nil {
		t.Error("a body shorter than expected")
	}
	entries, _ := os.ReadDir(filepath.Join(root, "x"))
	if len(entries) != 0 {
		t.Errorf("no half file: %v", entries)
	}
	info, err := f.Stat(context.Background(), a.Ref)
	if err != nil || info.Size != 3 || info.Sum != a.Sum {
		t.Errorf("%+v %v", info, err)
	}
	if _, err := f.Stat(context.Background(), "nope.jpg"); !errors.Is(err, dest.ErrNotFound) {
		t.Errorf("%v", err)
	}
	r, err := f.Open(context.Background(), a.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(r); string(b) != "one" {
		t.Error("open reads it")
	}
	r.Close()
}

func TestAFolderNeverLeavesItsRootAndSaysSoForAnOddName(t *testing.T) {
	f, root := newFolder(t)
	for _, name := range []string{"../escape.jpg", "a/../../escape.jpg", "/etc/passwd-x", "..\\escape.jpg", "", "/"} {
		st, err := f.Put(context.Background(), name, strings.NewReader("x"), 1)
		if err == nil {
			// a name that cleans to something inside the root is fine; it must be inside
			if strings.HasPrefix(st.Ref, "..") || filepath.IsAbs(st.Ref) {
				t.Errorf("%q went to %q", name, st.Ref)
			}
			if _, e := os.Stat(filepath.Join(filepath.Dir(root), "escape.jpg")); e == nil {
				t.Errorf("%q wrote outside the root", name)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.jpg")); err == nil {
		t.Error("a file outside the root")
	}
	if err := f.Delete(context.Background(), "../../etc/hosts"); err != nil {
		// cleaned to a path inside the root, which is not there: nothing happens
		t.Logf("delete: %v", err)
	}
	if _, err := dest.NewFolder("relative/path"); err == nil {
		t.Error("a root must be absolute")
	}
}

func TestDeletingAFolderFileRemovesTheFoldersItLeavesEmptyUpToTheRootOnly(t *testing.T) {
	f, root := newFolder(t)
	put(t, f, "a/b/c/one.jpg", "1")
	put(t, f, "a/two.jpg", "2")
	if err := f.Delete(context.Background(), "a/b/c/one.jpg"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "a/b")); err == nil {
		t.Error("the empty folders went")
	}
	if _, err := os.Stat(filepath.Join(root, "a/two.jpg")); err != nil {
		t.Error("a folder with something in it stays")
	}
	if err := f.Delete(context.Background(), "a/two.jpg"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Error("the root itself is never removed")
	}
	if err := f.Delete(context.Background(), "a/not-there.jpg"); err != nil {
		t.Errorf("a file that is not there is not an error: %v", err)
	}
}

func TestAFolderListsWhatIsUnderAPrefixWithoutTheFilesOfAWriteInProgress(t *testing.T) {
	f, root := newFolder(t)
	put(t, f, "a/1.jpg", "1")
	put(t, f, "a/b/2.jpg", "22")
	put(t, f, "c/3.jpg", "333")
	os.WriteFile(filepath.Join(root, "a", ".handoff-tmp-123"), []byte("half"), 0o644)
	all, err := f.List(context.Background(), "")
	if err != nil || len(all) != 3 || all[0].Ref != "a/1.jpg" || all[1].Ref != "a/b/2.jpg" || all[2].Size != 3 {
		t.Fatalf("%+v %v", all, err)
	}
	if sub, _ := f.List(context.Background(), "a"); len(sub) != 2 {
		t.Errorf("%+v", sub)
	}
	if none, err := f.List(context.Background(), "missing"); err != nil || len(none) != 0 {
		t.Errorf("%+v %v", none, err)
	}
}

func TestACheckSaysWhatIsWrongWithAFolder(t *testing.T) {
	f, root := newFolder(t)
	if msg, err := f.Check(context.Background()); err != nil || !strings.Contains(msg, "the app can write in it") {
		t.Fatalf("%q %v", msg, err)
	}
	if left, _ := os.ReadDir(root); len(left) != 0 {
		t.Errorf("the test file is removed: %v", left)
	}
	missing, _ := dest.NewFolder(filepath.Join(root, "not-mounted"))
	if _, err := missing.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "is the share mounted there?") {
		t.Errorf("%v", err)
	}
	file := filepath.Join(root, "a-file")
	os.WriteFile(file, []byte("x"), 0o644)
	notDir, _ := dest.NewFolder(file)
	if _, err := notDir.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "is not a folder") {
		t.Errorf("%v", err)
	}
	if os.Getuid() != 0 {
		ro := filepath.Join(root, "ro")
		os.Mkdir(ro, 0o555)
		readOnly, _ := dest.NewFolder(ro)
		if _, err := readOnly.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "must be allowed to write there") {
			t.Errorf("%v", err)
		}
	}
}

func TestTheFolderKindIsRegisteredAndBuiltFromASpec(t *testing.T) {
	d, err := dest.Build(dest.Spec{Name: "nas", Kind: "folder", Settings: map[string]string{"root": t.TempDir()}}, dest.Net{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Check(context.Background()); err != nil {
		t.Error(err)
	}
}

// ---- a folder as a receiver: the sidecar, the record, the live photo, take-back ----

func TestAPhotoIsWrittenWithItsSidecarAndRecordAndALivePhotoWithItsClipUnderOneBaseName(t *testing.T) {
	f, root := newFolder(t)
	a, id := photo("IMG_1234.HEIC")
	rating, desc := 4, "Rex at the beach"
	lat, lon := 38.7, -9.1
	a.ExifInfo = &immich.ExifResponseDto{Rating: &rating, Description: &desc, Latitude: &lat, Longitude: &lon}
	a.Checksum = "c2hhMQ=="
	clip, cid := photo("IMG_1234.MOV")
	api := immichWith(t, map[string][]byte{id: []byte("the still"), cid: []byte("the clip")}, false)
	r := &dest.Remote{Ident: "nas", Name: "the NAS", Service: "family", D: f}
	// a different file already has the photo's name: the still is suffixed, and the clip follows it
	put(t, f, "IMG_1234.HEIC", "somebody else's photo")
	cp, err := r.Deliver(context.Background(), api, a, &clip, syncer.RemoteJob{Sender: "Bob", Details: true})
	if err != nil {
		t.Fatal(err)
	}
	if cp.Ref != "IMG_1234 (2).HEIC" || cp.Clip != "IMG_1234 (2).MOV" || !cp.Created || !cp.ClipCreated {
		t.Fatalf("one base name: %+v", cp)
	}
	for _, name := range []string{"IMG_1234 (2).HEIC", "IMG_1234 (2).HEIC.xmp", "IMG_1234 (2).HEIC.handoff.json", "IMG_1234 (2).MOV"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("%s is missing", name)
		}
	}
	xmp, _ := os.ReadFile(filepath.Join(root, "IMG_1234 (2).HEIC.xmp"))
	for _, want := range []string{`xmp:Rating="4"`, "Rex at the beach", "exif:GPSLatitude"} {
		if !strings.Contains(string(xmp), want) {
			t.Errorf("the sidecar lacks %q", want)
		}
	}
	rec, _ := os.ReadFile(filepath.Join(root, "IMG_1234 (2).HEIC.handoff.json"))
	for _, want := range []string{`"sender": "Bob"`, `"service": "family"`, `"original_name": "IMG_1234.HEIC"`, id, `"source_checksum": "c2hhMQ=="`, `"sha1"`, `"verified": "size and checksum"`} {
		if !strings.Contains(string(rec), want) {
			t.Errorf("the record lacks %q:\n%s", want, rec)
		}
	}
	// without details no sidecar is written (CARRY_DETAILS=off); the record still is
	b, bid := photo("IMG_5.jpg")
	b.ExifInfo = a.ExifInfo
	api2 := immichWith(t, map[string][]byte{bid: []byte("b")}, false)
	if _, err := r.Deliver(context.Background(), api2, b, nil, syncer.RemoteJob{Sender: "Bob", Details: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "IMG_5.jpg.xmp")); err == nil {
		t.Error("no sidecar without details")
	}
	if _, err := os.Stat(filepath.Join(root, "IMG_5.jpg.handoff.json")); err != nil {
		t.Error("the record is always written")
	}
	// a repeat keeps the first record
	before, _ := os.ReadFile(filepath.Join(root, "IMG_5.jpg.handoff.json"))
	cp2, err := r.Deliver(context.Background(), api2, b, nil, syncer.RemoteJob{Sender: "Someone else", Details: false})
	after, _ := os.ReadFile(filepath.Join(root, "IMG_5.jpg.handoff.json"))
	if err != nil || cp2.Created || !bytes.Equal(before, after) {
		t.Errorf("a repeat is held and leaves the first record: %+v %v", cp2, err)
	}
	// take-back removes the photo, its sidecar and its record, and the clip
	if err := r.TakeBackRefs(context.Background(), []string{cp.Ref, cp.Clip}); err != nil {
		t.Fatal(err)
	}
	left, _ := os.ReadDir(root)
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	for _, n := range names {
		if strings.Contains(n, "(2)") {
			t.Errorf("%s was not taken back: %v", n, names)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "IMG_1234.HEIC")); err != nil {
		t.Error("what was somebody else's stays")
	}
}

// breaks a write whose name ends so
type breaking struct {
	dest.Destination
	suffix string
}

func (b breaking) Put(ctx context.Context, name string, r io.Reader, size int64) (dest.Stored, error) {
	if strings.HasSuffix(name, b.suffix) {
		return dest.Stored{}, errors.New("disk full")
	}
	return b.Destination.Put(ctx, name, r, size)
}

func TestWhenTheSidecarOrTheClipCannotBeWrittenThePhotoIsRemovedAgainSoARetryStartsClean(t *testing.T) {
	for _, suffix := range []string{".xmp", ".MOV"} {
		f, root := newFolder(t)
		a, id := photo("IMG_9.HEIC")
		rating := 3
		a.ExifInfo = &immich.ExifResponseDto{Rating: &rating}
		clip, cid := photo("IMG_9.MOV")
		api := immichWith(t, map[string][]byte{id: []byte("s"), cid: []byte("c")}, false)
		r := &dest.Remote{Ident: "nas", Name: "nas", Service: "s", D: breaking{f, suffix}}
		if _, err := r.Deliver(context.Background(), api, a, &clip, syncer.RemoteJob{Sender: "Bob", Details: true}); err == nil {
			t.Fatalf("%s: it should fail", suffix)
		}
		if left, _ := os.ReadDir(root); len(left) != 0 {
			t.Errorf("%s: what this delivery wrote is removed: %v", suffix, left)
		}
	}
}
