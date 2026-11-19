package dest_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/dest"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// s3Stub is a small S3: path-style, one bucket, single and multipart uploads, copy, list. It checks that a request is signed by the right
// key (the signature itself is checked against Amazon's example in s3sign_internal_test.go, and against MinIO by hand).
type s3Stub struct {
	*httptest.Server
	mu      sync.Mutex
	objects map[string][]byte
	uploads map[string]map[int][]byte
	aborted int
	calls   map[string]int
	// behaviour switches
	badETag   bool   // answer a single PUT with a wrong MD5
	region    string // answer every request as a bucket in this region
	forbidden bool
	noBucket  bool
	encrypted bool // ETags that are not an MD5 (an encrypted object's)
}

func newS3(t *testing.T) *s3Stub {
	t.Helper()
	s := &s3Stub{objects: map[string][]byte{}, uploads: map[string]map[int][]byte{}, calls: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *s3Stub) errorXML(w http.ResponseWriter, code int, c, msg string) {
	w.WriteHeader(code)
	fmt.Fprintf(w, `<?xml version="1.0"?><Error><Code>%s</Code><Message>%s</Message></Error>`, c, msg)
}

func (s *s3Stub) etag(b []byte) string {
	if s.encrypted {
		return "enc-" + hex.EncodeToString(b[:min(len(b), 4)])
	}
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

func (s *s3Stub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[r.Method]++
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKTEST/") || r.Header.Get("x-amz-date") == "" || r.Header.Get("x-amz-content-sha256") == "" {
		s.errorXML(w, 403, "SignatureDoesNotMatch", "the request signature does not match")
		return
	}
	if s.forbidden {
		s.errorXML(w, 403, "AccessDenied", "Access Denied")
		return
	}
	if s.region != "" {
		w.Header().Set("x-amz-bucket-region", s.region)
		s.errorXML(w, 301, "PermanentRedirect", "wrong endpoint")
		return
	}
	p := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	bucket, key, _ := strings.Cut(p, "/")
	if bucket != "photos" || s.noBucket {
		s.errorXML(w, 404, "NoSuchBucket", "The specified bucket does not exist")
		return
	}
	key, _ = urlDecode(key)
	q := r.URL.Query()
	switch {
	case key == "" && q.Get("list-type") == "2":
		prefix := q.Get("prefix")
		max, _ := strconv.Atoi(q.Get("max-keys"))
		var keys []string
		for k := range s.objects {
			if strings.HasPrefix(k, prefix) && k > q.Get("continuation-token") {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		truncated := false
		if max > 0 && len(keys) > max {
			keys, truncated = keys[:max], true
		}
		var b strings.Builder
		fmt.Fprintf(&b, `<?xml version="1.0"?><ListBucketResult><IsTruncated>%v</IsTruncated>`, truncated)
		if truncated {
			fmt.Fprintf(&b, "<NextContinuationToken>%s</NextContinuationToken>", keys[len(keys)-1])
		}
		for _, k := range keys {
			fmt.Fprintf(&b, "<Contents><Key>%s</Key><Size>%d</Size><LastModified>2026-03-09T12:00:00.000Z</LastModified></Contents>", k, len(s.objects[k]))
		}
		b.WriteString("</ListBucketResult>")
		io.WriteString(w, b.String())
	case r.Method == "POST" && q.Has("uploads"):
		id := fmt.Sprintf("u%d", len(s.uploads)+1)
		s.uploads[id] = map[int][]byte{}
		fmt.Fprintf(w, `<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, id)
	case r.Method == "PUT" && q.Has("partNumber"):
		n, _ := strconv.Atoi(q.Get("partNumber"))
		b, _ := io.ReadAll(r.Body)
		s.uploads[q.Get("uploadId")][n] = b
		w.Header().Set("ETag", `"`+s.etag(b)+`"`)
	case r.Method == "POST" && q.Has("uploadId"):
		var req struct {
			Parts []struct {
				PartNumber int
				ETag       string
			} `xml:"Part"`
		}
		b, _ := io.ReadAll(r.Body)
		xml.Unmarshal(b, &req)
		var all []byte
		for _, p := range req.Parts {
			all = append(all, s.uploads[q.Get("uploadId")][p.PartNumber]...)
		}
		s.objects[key] = all
		delete(s.uploads, q.Get("uploadId"))
		fmt.Fprint(w, `<CompleteMultipartUploadResult><ETag>"abc-5"</ETag></CompleteMultipartUploadResult>`)
	case r.Method == "DELETE" && q.Has("uploadId"):
		delete(s.uploads, q.Get("uploadId"))
		s.aborted++
		w.WriteHeader(204)
	case r.Method == "PUT" && r.Header.Get("x-amz-copy-source") != "":
		src, _ := urlDecode(strings.TrimPrefix(r.Header.Get("x-amz-copy-source"), "/photos/"))
		b, ok := s.objects[src]
		if !ok {
			s.errorXML(w, 404, "NoSuchKey", "no such key")
			return
		}
		s.objects[key] = b
		fmt.Fprint(w, `<CopyObjectResult><ETag>"x"</ETag></CopyObjectResult>`)
	case r.Method == "PUT":
		b, _ := io.ReadAll(r.Body)
		s.objects[key] = b
		et := s.etag(b)
		if s.badETag {
			et = strings.Repeat("0", 32)
		}
		w.Header().Set("ETag", `"`+et+`"`)
	case r.Method == "HEAD" || r.Method == "GET":
		b, ok := s.objects[key]
		if !ok {
			if r.Method == "HEAD" {
				w.WriteHeader(404)
				return
			}
			s.errorXML(w, 404, "NoSuchKey", "no such key")
			return
		}
		w.Header().Set("ETag", `"`+s.etag(b)+`"`)
		w.Header().Set("Last-Modified", "Mon, 09 Mar 2026 12:00:00 GMT")
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		if r.Method == "GET" {
			w.Write(b)
		}
	case r.Method == "DELETE":
		delete(s.objects, key)
		w.WriteHeader(204)
	default:
		s.errorXML(w, 400, "InvalidRequest", "not supported by the stub")
	}
}

func urlDecode(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			v, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String(), nil
}

func (s *s3Stub) dest(t *testing.T, mod func(*dest.S3Config)) *dest.S3 {
	t.Helper()
	cfg := dest.S3Config{Endpoint: s.URL, Bucket: "photos", AccessKey: "AKTEST", SecretKey: "s3cret-key", PartSize: 1 << 20, SingleMax: 2 << 20}
	if mod != nil {
		mod(&cfg)
	}
	d, err := dest.NewS3(cfg, dest.Net{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAnS3BucketGetsFilesByKeyCheckedByTheStoresMD5SkippedWhenTheSameAndSuffixedWhenDifferent(t *testing.T) {
	s := newS3(t)
	d := s.dest(t, func(c *dest.S3Config) { c.Prefix = "photos-from-handoff" })
	a := put(t, d, "family/Bob/2026/03/IMG 1 (é).jpg", "one")
	if a.Ref != "family/Bob/2026/03/IMG 1 (é).jpg" || a.Existed || a.Verified != "size and checksum" {
		t.Fatalf("%+v", a)
	}
	if string(s.objects["photos-from-handoff/family/Bob/2026/03/IMG 1 (é).jpg"]) != "one" {
		t.Errorf("under the prefix, with an odd name kept: %v", keys(s.objects))
	}
	if same := put(t, d, "family/Bob/2026/03/IMG 1 (é).jpg", "one"); !same.Existed || same.Ref != a.Ref {
		t.Errorf("the same file: %+v", same)
	}
	if other := put(t, d, "family/Bob/2026/03/IMG 1 (é).jpg", "two"); other.Ref != "family/Bob/2026/03/IMG 1 (é) (2).jpg" || other.Existed {
		t.Errorf("a different file with the name: %+v", other)
	}
	if again := put(t, d, "family/Bob/2026/03/IMG 1 (é).jpg", "two"); !again.Existed || again.Ref != "family/Bob/2026/03/IMG 1 (é) (2).jpg" {
		t.Errorf("a repeat of the second finds it: %+v", again)
	}
	for k := range s.objects {
		if strings.Contains(k, ".handoff-tmp-") {
			t.Errorf("a temporary object is left: %s", k)
		}
	}
	info, err := d.Stat(context.Background(), a.Ref)
	if err != nil || info.Size != 3 {
		t.Errorf("%+v %v", info, err)
	}
	if _, err := d.Stat(context.Background(), "nope.jpg"); err != dest.ErrNotFound {
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
	all, err := d.List(context.Background(), "")
	if err != nil || len(all) != 2 || all[0].Ref != "family/Bob/2026/03/IMG 1 (é) (2).jpg" || all[0].Size != 3 {
		t.Errorf("the prefix is not part of what is listed: %+v %v", all, err)
	}
	if err := d.Delete(context.Background(), a.Ref); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(context.Background(), a.Ref); err != nil {
		t.Errorf("a key that is not there is not an error: %v", err)
	}
}

func keys(m map[string][]byte) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

func TestALargeFileGoesInPartsEachCheckedAndAnUploadThatFailsIsAborted(t *testing.T) {
	s := newS3(t)
	d := s.dest(t, nil)
	big := make([]byte, 5<<20+123)
	rand.Read(big)
	st, err := d.Put(context.Background(), "big.mov", bytes.NewReader(big), int64(len(big)))
	if err != nil || st.Size != int64(len(big)) || st.Verified != "size and checksum" {
		t.Fatalf("%+v %v", st, err)
	}
	if !bytes.Equal(s.objects["big.mov"], big) {
		t.Error("the parts add up to the file")
	}
	if len(s.uploads) != 0 {
		t.Error("the upload was finished")
	}
	// the exact size of two parts: no empty third part
	two := make([]byte, 2<<20+1<<20) // three parts of 1 MiB
	rand.Read(two)
	if st, err := d.Put(context.Background(), "three.mov", bytes.NewReader(two), int64(len(two))); err != nil || st.Size != int64(len(two)) || !bytes.Equal(s.objects["three.mov"], two) {
		t.Errorf("%+v %v", st, err)
	}
	// a size that is not the body's aborts the upload and leaves nothing
	if _, err := d.Put(context.Background(), "short.mov", bytes.NewReader(big[:3<<20]), int64(len(big))); err == nil {
		t.Error("a short body")
	}
	if _, there := s.objects["short.mov"]; there || len(s.uploads) != 0 || s.aborted == 0 {
		t.Errorf("aborted, nothing left: objects=%v uploads=%d aborted=%d", keys(s.objects), len(s.uploads), s.aborted)
	}
	// a size that is not known goes in parts too
	if st, err := d.Put(context.Background(), "unknown.mov", io.MultiReader(bytes.NewReader(big[:1500000])), -1); err != nil || st.Size != 1500000 {
		t.Errorf("%+v %v", st, err)
	}
	// a clash with a file that was sent in parts is judged by reading it
	if same := mustPut(t, d, "big.mov", big); !same.Existed {
		t.Errorf("the same large file: %+v", same)
	}
	other := append([]byte(nil), big...)
	other[7] ^= 0xff
	if diff := mustPut(t, d, "big.mov", other); diff.Existed || diff.Ref != "big (2).mov" {
		t.Errorf("a different large file with the name: %+v", diff)
	}
}

func mustPut(t *testing.T, d dest.Destination, name string, b []byte) dest.Stored {
	t.Helper()
	st, err := d.Put(context.Background(), name, bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestAWrongChecksumFromTheStoreRefusesTheFileAndRemovesIt(t *testing.T) {
	s := newS3(t)
	d := s.dest(t, nil)
	s.badETag = true
	if _, err := d.Put(context.Background(), "a.jpg", strings.NewReader("hello"), 5); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("%v", err)
	}
	if _, there := s.objects["a.jpg"]; there {
		t.Error("the file that was refused is removed")
	}
	// a store whose ETag is not an MD5 (encrypted objects) is trusted for the size, and the record will say so
	s.badETag, s.encrypted = false, true
	st, err := d.Put(context.Background(), "b.jpg", strings.NewReader("hello"), 5)
	if err != nil || st.Verified != "size only" {
		t.Errorf("%+v %v", st, err)
	}
}

func TestACheckOfAnS3BucketSaysWhatIsWrongInWordsAndNeverShowsTheKeysOrTheAddress(t *testing.T) {
	s := newS3(t)
	if msg, err := s.dest(t, nil).Check(context.Background()); err != nil || !strings.Contains(msg, "can write and delete") {
		t.Fatalf("%q %v", msg, err)
	}
	if len(s.objects) != 0 {
		t.Errorf("the test object is deleted: %v", keys(s.objects))
	}
	fail := func(d dest.Destination) string {
		_, err := d.Check(context.Background())
		if err == nil {
			t.Fatal("it should fail")
		}
		for _, secret := range []string{"s3cret-key", "AKTEST", s.Listener.Addr().String()} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("gives away %q: %v", secret, err)
			}
		}
		return err.Error()
	}
	if msg := fail(s.dest(t, func(c *dest.S3Config) { c.AccessKey = "OTHER" })); !strings.Contains(msg, "did not accept the access key") {
		t.Errorf("%q", msg)
	}
	if msg := fail(s.dest(t, func(c *dest.S3Config) { c.Bucket = "other" })); !strings.Contains(msg, "no bucket by that name") {
		t.Errorf("%q", msg)
	}
	s.forbidden = true
	if msg := fail(s.dest(t, nil)); !strings.Contains(msg, "does not allow this key") {
		t.Errorf("%q", msg)
	}
	s.forbidden, s.region = false, "eu-west-3"
	if msg := fail(s.dest(t, nil)); !strings.Contains(msg, "region eu-west-3") {
		t.Errorf("%q", msg)
	}
	d := s.dest(t, nil)
	s.Close()
	fail(d)
}

func TestAnS3DestinationNeedsABucketAndKeysAndAPrivateEndpointNeedsTheAdministratorsSwitch(t *testing.T) {
	if _, err := dest.NewS3(dest.S3Config{Bucket: "b"}, dest.Net{}); err == nil {
		t.Error("keys are needed")
	}
	if _, err := dest.NewS3(dest.S3Config{Endpoint: "http://minio.example.org:9000", Bucket: "b", AccessKey: "a", SecretKey: "s"}, dest.Net{}); err == nil {
		t.Error("http only with the switch")
	}
	if _, err := dest.NewS3(dest.S3Config{Endpoint: "https://192.168.1.5:9000", Bucket: "b", AccessKey: "a", SecretKey: "s"}, dest.Net{}); err == nil {
		t.Error("a private address needs the switch")
	}
	if _, err := dest.NewS3(dest.S3Config{Endpoint: "http://192.168.1.5:9000", Bucket: "b", AccessKey: "a", SecretKey: "s"}, dest.Net{AllowPrivate: true}); err != nil {
		t.Errorf("allowed: %v", err)
	}
	if _, err := dest.NewS3(dest.S3Config{Bucket: "b", AccessKey: "a", SecretKey: "s", Region: "eu-west-3"}, dest.Net{}); err != nil {
		t.Errorf("Amazon's own: %v", err)
	}
}

func TestAnS3BucketAsAReceiverGetsThePhotoSidecarRecordAndClipAndTakeBackDeletesThemAll(t *testing.T) {
	s := newS3(t)
	d := s.dest(t, nil)
	a, id := photo("IMG_3.HEIC")
	rating := 4
	a.ExifInfo = &immich.ExifResponseDto{Rating: &rating}
	clip, cid := photo("IMG_3.MOV")
	api := immichWith(t, map[string][]byte{id: []byte("still"), cid: []byte("clip")}, false)
	r := &dest.Remote{Ident: "bucket", Name: "the bucket", Service: "family", D: d}
	cp, err := r.Deliver(context.Background(), api, a, &clip, syncer.RemoteJob{Sender: "Bob", Details: true})
	if err != nil || cp.Ref != "IMG_3.HEIC" || cp.Clip != "IMG_3.MOV" {
		t.Fatalf("%+v %v", cp, err)
	}
	if got := keys(s.objects); len(got) != 4 {
		t.Fatalf("the photo, sidecar, record and clip: %v", got)
	}
	if rec := string(s.objects["IMG_3.HEIC.handoff.json"]); !strings.Contains(rec, `"verified": "size and checksum"`) {
		t.Errorf("the record says how it was checked:\n%s", rec)
	}
	if err := r.TakeBackRefs(context.Background(), []string{cp.Ref, cp.Clip}); err != nil || len(s.objects) != 0 {
		t.Errorf("%v %v", err, keys(s.objects))
	}
}

func TestAListingOfABucketFollowsEveryPage(t *testing.T) {
	s := newS3(t)
	d := s.dest(t, nil)
	for i := 0; i < 2500; i++ {
		s.objects[fmt.Sprintf("a/%05d.jpg", i)] = []byte("x")
	}
	all, err := d.List(context.Background(), "a")
	if err != nil || len(all) != 2500 || all[0].Ref != "a/00000.jpg" || all[2499].Ref != "a/02499.jpg" {
		t.Errorf("%d %v", len(all), err)
	}
	if s.calls["GET"] != 3 {
		t.Errorf("three pages of up to a thousand: %d requests", s.calls["GET"])
	}
}

// TestAgainstARealS3 runs the same steps against a real S3-compatible store, when one is given (it is skipped otherwise):
//
//	S3_TEST_ENDPOINT=http://localhost:8333 S3_TEST_BUCKET=photos S3_TEST_KEY=... S3_TEST_SECRET=... go test ./internal/dest -run RealS3
//
// It was run against SeaweedFS with signature checking on (a wrong key is refused), which is what proves the signing.
func TestAgainstARealS3(t *testing.T) {
	ep, bucket, key, secret := os.Getenv("S3_TEST_ENDPOINT"), os.Getenv("S3_TEST_BUCKET"), os.Getenv("S3_TEST_KEY"), os.Getenv("S3_TEST_SECRET")
	if ep == "" {
		t.Skip("S3_TEST_ENDPOINT is not set")
	}
	prefix := "handoff-test-" + hex.EncodeToString(func() []byte { b := make([]byte, 4); rand.Read(b); return b }())
	d, err := dest.NewS3(dest.S3Config{Endpoint: ep, Bucket: bucket, AccessKey: key, SecretKey: secret, Prefix: prefix, PartSize: 5 << 20, SingleMax: 6 << 20}, dest.Net{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	defer func() {
		all, _ := d.List(ctx, "")
		for _, e := range all {
			d.Delete(ctx, e.Ref)
		}
	}()
	if msg, err := d.Check(ctx); err != nil {
		t.Fatalf("check: %v", err)
	} else {
		t.Log(msg)
	}
	bad, _ := dest.NewS3(dest.S3Config{Endpoint: ep, Bucket: bucket, AccessKey: key, SecretKey: "wrong", Prefix: prefix}, dest.Net{AllowPrivate: true})
	if _, err := bad.Check(ctx); err == nil || !strings.Contains(err.Error(), "access key") {
		t.Errorf("a wrong secret is refused in words: %v", err)
	}
	a := mustPut(t, d, "family/Bob/2026/03/IMG 1 (é).jpg", []byte("one"))
	if a.Existed || a.Verified != "size and checksum" {
		t.Errorf("%+v", a)
	}
	if same := mustPut(t, d, "family/Bob/2026/03/IMG 1 (é).jpg", []byte("one")); !same.Existed {
		t.Errorf("%+v", same)
	}
	if other := mustPut(t, d, "family/Bob/2026/03/IMG 1 (é).jpg", []byte("two")); other.Existed || other.Ref != "family/Bob/2026/03/IMG 1 (é) (2).jpg" {
		t.Errorf("%+v", other)
	}
	big := make([]byte, 12<<20+17)
	rand.Read(big)
	st := mustPut(t, d, "big.mov", big)
	if st.Size != int64(len(big)) || st.Verified != "size and checksum" {
		t.Errorf("%+v", st)
	}
	if same := mustPut(t, d, "big.mov", big); !same.Existed {
		t.Errorf("a multipart object is recognised by reading it: %+v", same)
	}
	r, err := d.Open(ctx, "big.mov")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if !bytes.Equal(got, big) {
		t.Error("the large file reads back the same")
	}
	all, err := d.List(ctx, "")
	if err != nil || len(all) != 3 {
		t.Errorf("%+v %v", all, err)
	}
}
