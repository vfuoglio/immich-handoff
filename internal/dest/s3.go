package dest

import (
	"bytes"
	"context"
	"crypto/md5"
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
	"time"

	"github.com/vfuoglio/immich-handoff/internal/bridge"
)

func init() {
	Register("s3", func(s Spec, n Net) (Destination, error) {
		return NewS3(S3Config{Endpoint: s.Settings["endpoint"], Region: s.Settings["region"], Bucket: s.Settings["bucket"], Prefix: s.Settings["prefix"],
			AccessKey: s.Settings["access_key"], SecretKey: s.Settings["secret_key"], PathStyle: on(s.Settings["path_style"])}, n)
	})
}

// S3Config is where an S3 destination is and how it logs in. With no Endpoint it is Amazon's (addressed by bucket name); with one it is a
// store of another make (MinIO, Backblaze B2, Wasabi, Cloudflare R2, Garage), addressed by path unless the name of the bucket is part of its
// address (set virtual addressing by leaving PathStyle false and giving the endpoint without the bucket: it then also uses the bucket as a
// sub-domain only when PathStyle is false and Endpoint is empty).
type S3Config struct {
	Endpoint  string // empty: Amazon's
	Region    string // empty: us-east-1
	Bucket    string
	Prefix    string // a "folder" in the bucket that everything goes under
	AccessKey string
	SecretKey string
	PathStyle bool // force bucket-in-the-path for Amazon too
	// PartSize and SingleMax are the size of a part of a multipart upload and the size above which a file is sent in parts; zero is the default
	// (16 MiB and 100 MiB). They are settable so that tests need not send 100 MiB.
	PartSize, SingleMax int64
}

// S3 is a Destination in a bucket of an S3-compatible store, written on the standard library: SigV4 signing, PUT (and multipart for big
// files), HEAD, GET, DELETE, server-side copy and ListObjectsV2, through the hardened client of bridges. S3 has no folders and no rename:
// a file is written once to its place, and checked by its size and by the MD5 the store returns for it (the "ETag" of a single upload and
// of each part of a multipart one; the ETag of a finished multipart upload is not a hash of the file, and is not used as one).
type S3 struct {
	cfg  S3Config
	base *url.URL
	hc   *http.Client
}

// The default size of a part of a multipart upload and the size from which a file is sent in parts (Amazon's smallest part is 5 MiB and a
// file has at most 10,000 parts, so 16 MiB reaches 160 GB).
const (
	defaultPartSize  = 16 << 20
	defaultSingleMax = 100 << 20
)

// NewS3 makes a Destination of a bucket. The endpoint is judged like a bridge's address.
func NewS3(cfg S3Config, n Net) (*S3, error) {
	p := bridge.Policy{AllowPrivate: n.AllowPrivate}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.PartSize <= 0 {
		cfg.PartSize = defaultPartSize
	}
	if cfg.SingleMax <= 0 {
		cfg.SingleMax = defaultSingleMax
	}
	if cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("an S3 destination needs a bucket, an access key and a secret key")
	}
	cfg.Prefix = strings.Trim(strings.ReplaceAll(cfg.Prefix, "\\", "/"), "/")
	ep := strings.TrimSpace(cfg.Endpoint)
	if ep == "" {
		ep = "https://s3." + cfg.Region + ".amazonaws.com"
	} else {
		cfg.PathStyle = true // another make of store: the bucket is in the path
	}
	if _, err := p.Address(ep); err != nil {
		return nil, err
	}
	if !strings.Contains(ep, "://") {
		ep = "https://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil {
		return nil, fmt.Errorf("%w: it cannot be read", bridge.ErrBadAddress)
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment = "", "", "", ""
	return &S3{cfg: cfg, base: u, hc: p.HTTPClient()}, nil
}

func (s *S3) clean(err error) error {
	msg := err.Error()
	for _, x := range []string{s.base.String(), s.base.Host, s.cfg.Bucket + "." + s.base.Host} {
		msg = strings.ReplaceAll(msg, x, "the S3 server")
	}
	return errors.New(msg)
}

// key is the object key of a name under the root: prefix, then the cleaned name.
func (s *S3) key(name string) (string, error) {
	name = path.Clean("/" + strings.ReplaceAll(name, "\\", "/"))
	if name == "/" {
		return "", errors.New("a file needs a name")
	}
	name = strings.TrimPrefix(name, "/")
	if s.cfg.Prefix != "" {
		return s.cfg.Prefix + "/" + name, nil
	}
	return name, nil
}

func (s *S3) ref(key string) string {
	if s.cfg.Prefix != "" {
		return strings.TrimPrefix(strings.TrimPrefix(key, s.cfg.Prefix), "/")
	}
	return key
}

// request builds a signed request for an object key ("" for the bucket itself).
func (s *S3) request(ctx context.Context, method, key string, q url.Values, body io.Reader, size int64, payload string, hdr map[string]string) (*http.Request, error) {
	u := *s.base
	rawPath := ""
	host := s.base.Host
	if s.cfg.PathStyle {
		rawPath = "/" + uriEncode(s.cfg.Bucket, false)
	} else {
		host = s.cfg.Bucket + "." + s.base.Host
	}
	if key != "" {
		rawPath += "/" + uriEncode(key, true)
	}
	if rawPath == "" {
		rawPath = "/"
	}
	u.Host = host
	if dec, err := url.PathUnescape(rawPath); err == nil {
		u.Path = dec
	}
	u.RawPath = rawPath
	u.RawQuery = canonicalQuery(q)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if body != nil && size >= 0 {
		req.ContentLength = size
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	signV4(req, s.cfg.AccessKey, s.cfg.SecretKey, s.cfg.Region, payload, time.Now())
	return req, nil
}

func (s *S3) do(ctx context.Context, method, key string, q url.Values, body io.Reader, size int64, payload string, hdr map[string]string) (*http.Response, error) {
	req, err := s.request(ctx, method, key, q, body, size, payload, hdr)
	if err != nil {
		return nil, err
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return nil, s.clean(err)
	}
	return resp, nil
}

type s3Error struct {
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
	Region  string `xml:"Region"`
}

// failure says in words what an answer means; it never carries the keys or the address.
func (s *S3) failure(resp *http.Response, doing string) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var e s3Error
	xml.Unmarshal(b, &e)
	region := resp.Header.Get("x-amz-bucket-region")
	if region == "" {
		region = e.Region
	}
	switch {
	case e.Code == "InvalidAccessKeyId" || e.Code == "SignatureDoesNotMatch" || e.Code == "AuthorizationHeaderMalformed" && region == "":
		return fmt.Errorf("the S3 server did not accept the access key and secret key (while %s)", doing)
	case e.Code == "AccessDenied" || e.Code == "AllAccessDisabled" || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("the S3 server does not allow this key to do that (while %s): it needs to list, read, write and delete in the bucket", doing)
	case e.Code == "NoSuchBucket":
		return fmt.Errorf("the S3 server has no bucket by that name (while %s): check the name and the region", doing)
	case region != "" && (resp.StatusCode == 301 || resp.StatusCode == 400 || e.Code == "PermanentRedirect" || e.Code == "AuthorizationHeaderMalformed"):
		return fmt.Errorf("the bucket is in the region %s, not the one that was set (while %s)", region, doing)
	case e.Code == "EntityTooLarge":
		return fmt.Errorf("the S3 server refuses a file this large (while %s)", doing)
	case e.Code == "SlowDown" || resp.StatusCode == 503:
		return fmt.Errorf("the S3 server asks to slow down or is not available (while %s): it is tried again", doing)
	case e.Code == "QuotaExceeded" || e.Code == "InsufficientStorage" || resp.StatusCode == http.StatusInsufficientStorage:
		return fmt.Errorf("the S3 server is out of room (while %s)", doing)
	case resp.StatusCode == 301 || resp.StatusCode == 302 || resp.StatusCode == 307:
		return fmt.Errorf("the S3 server answers with a redirect, which the app does not follow: check the endpoint and the region (while %s)", doing)
	}
	msg := e.Message
	if len(msg) > 120 {
		msg = msg[:120]
	}
	if msg != "" {
		msg = " (" + msg + ")"
	}
	return fmt.Errorf("the S3 server answered %d%s while %s", resp.StatusCode, msg, doing)
}

// head is what the store says about a key: whether it is there, its size, its ETag and its time.
type s3Head struct {
	Exists   bool
	Size     int64
	ETag     string
	Modified time.Time
}

func (s *S3) head(ctx context.Context, key string) (s3Head, error) {
	resp, err := s.do(ctx, "HEAD", key, nil, nil, -1, emptyPayloadSum, nil)
	if err != nil {
		return s3Head{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return s3Head{}, nil
	}
	if !ok(resp) {
		return s3Head{}, s.failure(resp, "looking at a file")
	}
	h := s3Head{Exists: true, ETag: strings.Trim(resp.Header.Get("ETag"), `"`)}
	h.Size, _ = strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	h.Modified, _ = http.ParseTime(resp.Header.Get("Last-Modified"))
	return h, nil
}

// plainMD5 says whether an ETag is the MD5 of the object (a single upload) and not a multipart or an encrypted one's.
func plainMD5(etag string) bool {
	if len(etag) != 32 {
		return false
	}
	_, err := hex.DecodeString(etag)
	return err == nil
}

func md5hex(b []byte) string {
	s := md5.Sum(b)
	return hex.EncodeToString(s[:])
}

// Put implements Destination. A name that is free is written directly (an upload appears whole or not at all); one that is taken is
// compared by the store's MD5 (or, for an object that was sent in parts, by reading it), and a different file is written under
// "name (2).ext" by way of a temporary object and a server-side copy.
func (s *S3) Put(ctx context.Context, name string, r io.Reader, size int64) (Stored, error) {
	key, err := s.key(name)
	if err != nil {
		return Stored{}, err
	}
	h, err := s.head(ctx, key)
	if err != nil {
		return Stored{}, err
	}
	target := key
	if h.Exists {
		target = path.Dir(key) + "/" + tmpPrefix + randName()
		if path.Dir(key) == "." {
			target = tmpPrefix + randName()
		}
	}
	up, err := s.upload(ctx, target, r, size)
	if err != nil {
		return Stored{}, err
	}
	st := Stored{Size: up.n, Verified: up.verified}
	if !h.Exists {
		st.Ref = s.ref(key)
		return st, nil
	}
	// the name is taken: the same file?
	cleanup := func() { s.rawDelete(context.WithoutCancel(ctx), target) }
	for i := 1; i < 10000; i++ {
		cand := key
		if i > 1 {
			cand = suffixedRef(key, i)
		}
		ch := h
		if i > 1 {
			if ch, err = s.head(ctx, cand); err != nil {
				cleanup()
				return Stored{}, err
			}
		}
		if ch.Exists {
			same := ch.Size == up.n
			if same {
				if plainMD5(ch.ETag) {
					same = ch.ETag == up.md5
				} else if same, err = s.sameContent(ctx, cand, up); err != nil {
					cleanup()
					return Stored{}, err
				}
			}
			if same {
				cleanup()
				st.Ref, st.Existed = s.ref(cand), true
				return st, nil
			}
			continue
		}
		if err := s.copy(ctx, target, cand, up.n); err != nil {
			cleanup()
			return Stored{}, err
		}
		cleanup()
		st.Ref = s.ref(cand)
		return st, nil
	}
	cleanup()
	return Stored{}, errors.New("too many files with that name")
}

// sameContent reads an object that was sent in parts and compares its SHA-1 with the upload's.
func (s *S3) sameContent(ctx context.Context, key string, up upload) (bool, error) {
	resp, err := s.do(ctx, "GET", key, nil, nil, -1, emptyPayloadSum, nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if !ok(resp) {
		return false, s.failure(resp, "reading a file")
	}
	h := sha1.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == up.sha1, nil
}

type upload struct {
	n        int64
	md5      string // of the whole file
	sha1     string
	verified string
}

// upload writes r to key: one request for a file up to singleMax, parts of partSize for a bigger one or one of unknown size. It checks
// what the store holds afterwards by its size and by the MD5 it returned (of the file, or of each part).
func (s *S3) upload(ctx context.Context, key string, r io.Reader, size int64) (upload, error) {
	sh, mh := sha1.New(), md5.New() // of the whole file, however it is sent
	src := io.TeeReader(r, io.MultiWriter(sh, mh))
	if size >= 0 && size <= s.cfg.SingleMax {
		return s.single(ctx, key, src, size, sh, mh)
	}
	return s.multipart(ctx, key, src, size, sh, mh)
}

func (s *S3) single(ctx context.Context, key string, src io.Reader, size int64, sh, mh interface{ Sum([]byte) []byte }) (upload, error) {
	counted := &countingReader{r: src}
	resp, err := s.do(ctx, "PUT", key, nil, counted, size, unsignedPayload, nil)
	if err != nil {
		return upload{}, err
	}
	defer resp.Body.Close()
	if !ok(resp) {
		return upload{}, s.failure(resp, "writing a file")
	}
	io.Copy(io.Discard, resp.Body)
	if counted.n != size {
		s.rawDelete(context.WithoutCancel(ctx), key)
		return upload{}, fmt.Errorf("%d bytes arrived, %d were expected", counted.n, size)
	}
	up := upload{n: counted.n, md5: hex.EncodeToString(mh.Sum(nil)), sha1: hex.EncodeToString(sh.Sum(nil))}
	etag := strings.Trim(resp.Header.Get("ETag"), `"`)
	if plainMD5(etag) {
		if etag != up.md5 {
			s.rawDelete(context.WithoutCancel(ctx), key)
			return upload{}, errors.New("the S3 server's checksum of the file differs from what was sent")
		}
		up.verified = "size and checksum"
	} else {
		up.verified = "size only" // an encrypted object's ETag is not its MD5
	}
	h, err := s.head(ctx, key)
	if err != nil {
		return upload{}, err
	}
	if !h.Exists || h.Size != up.n {
		s.rawDelete(context.WithoutCancel(ctx), key)
		return upload{}, fmt.Errorf("the S3 server holds %d bytes of the file, %d were sent", h.Size, up.n)
	}
	return up, nil
}

type partETag struct {
	XMLName    xml.Name `xml:"Part"`
	PartNumber int      `xml:"PartNumber"`
	ETag       string   `xml:"ETag"`
}

func (s *S3) multipart(ctx context.Context, key string, src io.Reader, size int64, sh, mh interface{ Sum([]byte) []byte }) (upload, error) {
	resp, err := s.do(ctx, "POST", key, url.Values{"uploads": {""}}, nil, -1, emptyPayloadSum, nil)
	if err != nil {
		return upload{}, err
	}
	var created struct {
		UploadID string `xml:"UploadId"`
	}
	if !ok(resp) {
		err := s.failure(resp, "starting a large upload")
		resp.Body.Close()
		return upload{}, err
	}
	xml.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&created)
	resp.Body.Close()
	if created.UploadID == "" {
		return upload{}, errors.New("the S3 server did not start a large upload")
	}
	abort := func() {
		if r, err := s.do(context.WithoutCancel(ctx), "DELETE", key, url.Values{"uploadId": {created.UploadID}}, nil, -1, emptyPayloadSum, nil); err == nil {
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
		}
	}
	var parts []partETag
	var total int64
	buf := make([]byte, s.cfg.PartSize)
	for n := 1; ; n++ {
		read, rerr := io.ReadFull(src, buf)
		if read == 0 && rerr != nil && n > 1 {
			break
		}
		if rerr != nil && rerr != io.ErrUnexpectedEOF && rerr != io.EOF {
			abort()
			return upload{}, rerr
		}
		part := buf[:read]
		pr, err := s.do(ctx, "PUT", key, url.Values{"partNumber": {strconv.Itoa(n)}, "uploadId": {created.UploadID}}, bytes.NewReader(part), int64(read), unsignedPayload, nil)
		if err != nil {
			abort()
			return upload{}, err
		}
		if !ok(pr) {
			err := s.failure(pr, "writing a part of a large file")
			pr.Body.Close()
			abort()
			return upload{}, err
		}
		io.Copy(io.Discard, pr.Body)
		pr.Body.Close()
		etag := strings.Trim(pr.Header.Get("ETag"), `"`)
		if plainMD5(etag) && etag != md5hex(part) {
			abort()
			return upload{}, fmt.Errorf("the S3 server's checksum of part %d differs from what was sent", n)
		}
		parts = append(parts, partETag{PartNumber: n, ETag: etag})
		total += int64(read)
		if rerr != nil { // the last, short part
			break
		}
	}
	if size >= 0 && total != size {
		abort()
		return upload{}, fmt.Errorf("%d bytes arrived, %d were expected", total, size)
	}
	body, _ := xml.Marshal(struct {
		XMLName xml.Name `xml:"CompleteMultipartUpload"`
		Parts   []partETag
	}{Parts: parts})
	cr, err := s.do(ctx, "POST", key, url.Values{"uploadId": {created.UploadID}}, bytes.NewReader(body), int64(len(body)), unsignedPayload, nil)
	if err != nil {
		abort()
		return upload{}, err
	}
	cb, _ := io.ReadAll(io.LimitReader(cr.Body, 1<<16))
	cr.Body.Close()
	var ce s3Error
	xml.Unmarshal(cb, &ce)
	if !ok(cr) {
		abort()
		return upload{}, s.failure(&http.Response{StatusCode: cr.StatusCode, Header: cr.Header, Body: io.NopCloser(bytes.NewReader(cb))}, "finishing a large upload")
	}
	if ce.Code != "" { // S3 can answer 200 with an error in the body
		abort()
		return upload{}, fmt.Errorf("the S3 server could not finish a large upload: %s", ce.Code)
	}
	up := upload{n: total, md5: hex.EncodeToString(mh.Sum(nil)), sha1: hex.EncodeToString(sh.Sum(nil)), verified: "size and checksum"} // each part's MD5 was checked above
	h, err := s.head(ctx, key)
	if err != nil {
		return upload{}, err
	}
	if !h.Exists || h.Size != total {
		s.rawDelete(context.WithoutCancel(ctx), key)
		return upload{}, fmt.Errorf("the S3 server holds %d bytes of the file, %d were sent", h.Size, total)
	}
	return up, nil
}

// copy makes dst a server-side copy of src (up to 5 GiB, which is S3's limit for one copy).
func (s *S3) copy(ctx context.Context, src, dst string, size int64) error {
	if size > 5<<30 {
		return errors.New("a different file already has that name and this one is too large to be renamed on S3")
	}
	source := "/" + uriEncode(s.cfg.Bucket, false) + "/" + uriEncode(src, true)
	resp, err := s.do(ctx, "PUT", dst, nil, nil, -1, emptyPayloadSum, map[string]string{"x-amz-copy-source": source})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var e s3Error
	if !ok(resp) || xml.Unmarshal(b, &e) == nil && e.Code != "" {
		return s.failure(&http.Response{StatusCode: resp.StatusCode, Header: resp.Header, Body: io.NopCloser(bytes.NewReader(b))}, "putting the file in place")
	}
	return nil
}

func (s *S3) rawDelete(ctx context.Context, key string) error {
	resp, err := s.do(ctx, "DELETE", key, nil, nil, -1, emptyPayloadSum, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if ok(resp) || resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return s.failure(resp, "deleting a file")
}

// Stat implements Destination. S3 does not give a SHA-1, so Sum is empty.
func (s *S3) Stat(ctx context.Context, ref string) (Info, error) {
	key, err := s.key(ref)
	if err != nil {
		return Info{}, err
	}
	h, err := s.head(ctx, key)
	if err != nil {
		return Info{}, err
	}
	if !h.Exists {
		return Info{}, ErrNotFound
	}
	return Info{Size: h.Size, Modified: h.Modified}, nil
}

// Delete implements Destination (a key that is not there is not an error).
func (s *S3) Delete(ctx context.Context, ref string) error {
	key, err := s.key(ref)
	if err != nil {
		return err
	}
	return s.rawDelete(ctx, key)
}

// List implements Destination: ListObjectsV2 under a prefix, a page of up to 1000 at a time.
func (s *S3) List(ctx context.Context, prefix string) ([]Entry, error) {
	prefix = strings.Trim(strings.ReplaceAll(prefix, "\\", "/"), "/")
	full := s.cfg.Prefix
	if prefix != "" {
		if full != "" {
			full += "/"
		}
		full += prefix
	}
	if full != "" {
		full += "/"
	}
	var out []Entry
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "max-keys": {"1000"}}
		if full != "" {
			q.Set("prefix", full)
		}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := s.do(ctx, "GET", "", q, nil, -1, emptyPayloadSum, nil)
		if err != nil {
			return nil, err
		}
		if !ok(resp) {
			err := s.failure(resp, "listing the bucket")
			resp.Body.Close()
			return nil, err
		}
		var page struct {
			Truncated bool   `xml:"IsTruncated"`
			Next      string `xml:"NextContinuationToken"`
			Contents  []struct {
				Key      string `xml:"Key"`
				Size     int64  `xml:"Size"`
				Modified string `xml:"LastModified"`
			} `xml:"Contents"`
		}
		err = xml.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("the S3 server's listing could not be read: %w", err)
		}
		for _, c := range page.Contents {
			if strings.HasPrefix(path.Base(c.Key), tmpPrefix) || strings.HasSuffix(c.Key, "/") {
				continue
			}
			t, _ := time.Parse(time.RFC3339, c.Modified)
			out = append(out, Entry{Ref: s.ref(c.Key), Size: c.Size, Modified: t})
			if len(out) > listMaxEntries {
				return nil, errors.New("the destination holds too many files to list")
			}
		}
		if !page.Truncated || page.Next == "" {
			break
		}
		token = page.Next
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, nil
}

// Open implements Destination.
func (s *S3) Open(ctx context.Context, ref string) (io.ReadCloser, error) {
	key, err := s.key(ref)
	if err != nil {
		return nil, err
	}
	resp, err := s.do(ctx, "GET", key, nil, nil, -1, emptyPayloadSum, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, ErrNotFound
	}
	if !ok(resp) {
		err := s.failure(resp, "reading a file")
		resp.Body.Close()
		return nil, err
	}
	return resp.Body, nil
}

// Check implements Destination: the bucket answers, the keys are accepted, and the app can write a small object and delete it.
func (s *S3) Check(ctx context.Context) (string, error) {
	resp, err := s.do(ctx, "GET", "", url.Values{"list-type": {"2"}, "max-keys": {"1"}}, nil, -1, emptyPayloadSum, nil)
	if err != nil {
		return "", err
	}
	if !ok(resp) {
		err := s.failure(resp, "looking at the bucket")
		resp.Body.Close()
		return "", err
	}
	resp.Body.Close()
	key, _ := s.key(tmpPrefix + "check-" + randName())
	body := []byte(time.Now().UTC().Format(time.RFC3339))
	pr, err := s.do(ctx, "PUT", key, nil, bytes.NewReader(body), int64(len(body)), unsignedPayload, nil)
	if err != nil {
		return "", err
	}
	if !ok(pr) {
		err := s.failure(pr, "writing a test file")
		pr.Body.Close()
		return "", err
	}
	pr.Body.Close()
	if err := s.rawDelete(ctx, key); err != nil {
		return "", fmt.Errorf("the app wrote a test file but could not delete it: %w", err)
	}
	return "the S3 bucket answers, the keys are accepted and the app can write and delete in it", nil
}
