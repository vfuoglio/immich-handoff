package syncer_test

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

const (
	srcAsset   = "11111111-1111-1111-1111-111111111111"
	dstAsset   = "22222222-2222-2222-2222-222222222222"
	shareTag   = "33333333-3333-3333-3333-333333333333"
	inboxTag   = "44444444-4444-4444-4444-444444444444"
	refusedTag = "66666666-6666-6666-6666-666666666666"
	albumID    = "99999999-9999-4999-8999-999999999999"
	sentTagID  = "77777777-7777-7777-7777-777777777777"
	fromTagID  = "88888888-8888-4888-8888-888888888888"
	payload    = "pretend these are JPEG bytes"
	ts         = "2025-01-02T03:04:05.000Z"
)

// fakeImmich implements just enough of the Immich v3 API to exercise a full import.
type fakeImmich struct {
	mu                sync.Mutex
	destTrashed       bool   // the destination copy sits in the trash
	uploadStatus      string // created | duplicate
	storedChecksum    string // what the destination reports
	deleted, uploaded []string
	tagged            []string
	refusedTagged     []string
	dropInboxTags     int // how many inbox tag requests Immich "accepts" but silently loses
	untaggedShare     []string
	uploadedBytes     string
	gotChecksumHeader string

	// albums: the receiver's own album of that name exists already, the key may not touch albums at all,
	// and what the app asked for
	existingAlbum string
	albumDenied   bool
	albumCreated  []string
	albumAdded    []string

	// a sender's linked album: what the album search returns, whose photos they are, which photos already
	// carry the "handed over" tag, and whether anything is tagged for the service at all
	watchItems       []string // asset ids in the album
	watchOwnerOther  bool     // the album's photos belong to somebody else
	watchSearchFails bool     // the album cannot be read
	sentTagged       []string // photos that already carry the sender's "handed over" tag
	noTagged         bool     // nothing carries the service tag

	fromTagged []string // names of the "from/<name>" tags that were put on a copy
	lastFrom   string   // the last "from/<name>" tag that was made
}

func (f *fakeImmich) handler() http.Handler {
	sum := sha1.Sum([]byte(payload))
	good := base64.StdEncoding.EncodeToString(sum[:])
	asset := func(id string, trashed bool, checksum string) map[string]any {
		return map[string]any{"id": id, "originalFileName": "IMG_1.jpg", "fileCreatedAt": ts, "fileModifiedAt": ts,
			"isFavorite": true, "isTrashed": trashed, "checksum": checksum, "exifInfo": map[string]any{"fileSizeInByte": 2_000_000}}
	}
	tag := func(id, name string) map[string]any {
		return map[string]any{"id": id, "name": name, "value": name, "createdAt": ts, "updatedAt": ts}
	}
	j := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/users/me", func(w http.ResponseWriter, r *http.Request) {
		sum := sha1.Sum([]byte(r.Header.Get("x-api-key")))
		id := fmt.Sprintf("aaaaaaaa-0000-0000-0000-%012x", sum[:6])
		j(w, 200, map[string]any{"id": id, "name": "n", "email": "a@b.c"})
	})
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("x-api-key"), "dest") {
			j(w, 200, []any{tag(inboxTag, "inbox")})
		} else if len(f.sentTagged) > 0 {
			j(w, 200, []any{tag(shareTag, "share"), tag(sentTagID, "handed-over")})
		} else {
			j(w, 200, []any{tag(shareTag, "share")})
		}
	})
	mux.HandleFunc("POST /api/search/metadata", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AlbumIds []string `json:"albumIds"`
			TagIds   []string `json:"tagIds"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		page := func(items ...any) map[string]any {
			return map[string]any{"assets": map[string]any{"items": items, "count": len(items), "total": len(items), "facets": []any{}}}
		}
		switch {
		case len(body.AlbumIds) > 0:
			if f.watchSearchFails {
				j(w, 403, map[string]any{"message": "Missing required permission: album.read"})
				return
			}
			srcSum := sha1.Sum([]byte("src")) // the same id GET /api/users/me gives the "src" key
			owner := fmt.Sprintf("aaaaaaaa-0000-0000-0000-%012x", srcSum[:6])
			if f.watchOwnerOther {
				owner = "bbbbbbbb-0000-0000-0000-000000000001"
			}
			var items []any
			for _, id := range f.watchItems {
				a := asset(id, false, good)
				a["ownerId"] = owner
				items = append(items, a)
			}
			j(w, 200, page(items...))
		case len(body.TagIds) > 0 && body.TagIds[0] == sentTagID:
			var items []any
			for _, id := range f.sentTagged {
				items = append(items, asset(id, false, good))
			}
			j(w, 200, page(items...))
		case f.noTagged:
			j(w, 200, page())
		default:
			j(w, 200, page(asset(srcAsset, false, good)))
		}
	})
	mux.HandleFunc("GET /api/assets/"+srcAsset+"/original", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	mux.HandleFunc("POST /api/assets", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.gotChecksumHeader = r.Header.Get("x-immich-checksum")
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(p)
			if p.FormName() == "assetData" {
				f.uploadedBytes = string(b)
			}
		}
		f.uploaded = append(f.uploaded, dstAsset)
		j(w, 201, map[string]any{"id": dstAsset, "status": f.uploadStatus})
	})
	mux.HandleFunc("GET /api/assets/"+srcAsset, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		a := asset(srcAsset, false, good)
		var tags []any
		if len(f.refusedTagged) > 0 { // after a mark the original carries the sent / refused tag...
			tags = append(tags, tag("77777777-7777-7777-7777-777777777777", "handed-over"), tag("88888888-8888-8888-8888-888888888888", "share-duplicate"))
		}
		if len(f.untaggedShare) == 0 { // ...until the service tag is removed
			tags = append(tags, tag(shareTag, "share"))
		}
		a["tags"] = tags
		j(w, 200, a)
	})
	mux.HandleFunc("GET /api/assets/"+dstAsset, func(w http.ResponseWriter, r *http.Request) {
		cs := f.storedChecksum
		if cs == "" {
			cs = good
		}
		a := asset(dstAsset, f.destTrashed, cs)
		a["hasMetadata"], a["exifInfo"] = true, map[string]any{} // Immich has finished reading it
		f.mu.Lock()
		if len(f.tagged) > 0 { // once tagged, the copy carries the inbox tag
			a["tags"] = []any{tag(inboxTag, "inbox")}
		}
		if n := len(f.fromTagged); n > 0 { // and the "from" tag, once that is put on
			tags, _ := a["tags"].([]any)
			a["tags"] = append(tags, tag(fromTagID, f.fromTagged[n-1]))
		}
		f.mu.Unlock()
		j(w, 200, a)
	})
	mux.HandleFunc("PUT /api/tags/"+inboxTag+"/assets", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		if f.dropInboxTags > 0 {
			f.dropInboxTags-- // reported as success, but the tag never lands
		} else {
			f.tagged = append(f.tagged, dstAsset)
		}
		f.mu.Unlock()
		j(w, 200, []any{map[string]any{"id": dstAsset, "success": true}})
	})
	mux.HandleFunc("PUT /api/tags", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Tags []string }
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.Tags) == 1 && strings.HasPrefix(body.Tags[0], "from/") {
			f.mu.Lock()
			f.lastFrom = body.Tags[0]
			f.mu.Unlock()
			j(w, 200, []any{tag(fromTagID, body.Tags[0])}) // a "from" tag is its own tag, named as asked
			return
		}
		j(w, 200, []any{tag(refusedTag, "share-duplicate")})
	})
	mux.HandleFunc("PUT /api/tags/"+fromTagID+"/assets", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.fromTagged = append(f.fromTagged, f.lastFrom)
		f.mu.Unlock()
		j(w, 200, []any{map[string]any{"id": dstAsset, "success": true}})
	})
	album := func(name string) map[string]any {
		return map[string]any{"id": albumID, "albumName": name, "assetCount": 3}
	}
	mux.HandleFunc("GET /api/albums", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.albumDenied {
			j(w, 403, map[string]any{"message": "Missing required permission: album.read"})
			return
		}
		if r.URL.Query().Get("isOwned") != "true" {
			j(w, 400, map[string]any{"message": "the app must only look at albums the account owns"})
			return
		}
		if n := r.URL.Query().Get("name"); n != "" && n == f.existingAlbum {
			j(w, 200, []any{album(n)})
			return
		}
		j(w, 200, []any{})
	})
	mux.HandleFunc("POST /api/albums", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.albumDenied {
			j(w, 403, map[string]any{"message": "Missing required permission: album.create"})
			return
		}
		var body struct{ AlbumName string }
		json.NewDecoder(r.Body).Decode(&body)
		f.albumCreated = append(f.albumCreated, body.AlbumName)
		j(w, 201, album(body.AlbumName))
	})
	mux.HandleFunc("PUT /api/albums/"+albumID+"/assets", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.albumDenied {
			j(w, 403, map[string]any{"message": "Missing required permission: albumAsset.create"})
			return
		}
		var body struct{ Ids []string }
		json.NewDecoder(r.Body).Decode(&body)
		f.albumAdded = append(f.albumAdded, body.Ids...)
		out := []any{}
		for _, id := range body.Ids {
			out = append(out, map[string]any{"id": id, "success": true})
		}
		j(w, 200, out)
	})
	mux.HandleFunc("PUT /api/tags/"+refusedTag+"/assets", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.refusedTagged = append(f.refusedTagged, srcAsset)
		f.mu.Unlock()
		j(w, 200, []any{map[string]any{"id": srcAsset, "success": true}})
	})
	mux.HandleFunc("DELETE /api/tags/"+shareTag+"/assets", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.untaggedShare = append(f.untaggedShare, srcAsset)
		f.mu.Unlock()
		j(w, 200, []any{map[string]any{"id": srcAsset, "success": true}})
	})
	mux.HandleFunc("DELETE /api/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Ids []string }
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.deleted = append(f.deleted, body.Ids...)
		f.mu.Unlock()
		w.WriteHeader(204)
	})
	return mux
}

func init() { syncer.SettleDelay = 0 }

func newAPI(t *testing.T, url, key string) *immich.API {
	t.Helper()
	a, err := immich.New(url, immich.Auth{APIKey: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func quiet() *slog.Logger {
	if os.Getenv("TEST_LOG") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func service(snd, rcv []*syncer.Account) *syncer.Service {
	return &syncer.Service{Tag: "share", InboxTag: "inbox", RefusedTag: "share-duplicate", ExpiryDays: 30, Senders: snd, Receivers: rcv}
}

func run(t *testing.T, f *fakeImmich) syncer.Counts {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	svc := service(
		[]*syncer.Account{{Name: "alice", API: newAPI(t, srv.URL, "src")}},
		[]*syncer.Account{{Name: "family", API: newAPI(t, srv.URL, "dest")}})
	s := &syncer.Syncer{Services: []*syncer.Service{svc}, Log: quiet()}
	s.Run(context.Background(), "test", false)
	return s.Last().Counts
}

// runAlbum is run for a service that puts arrivals in an album.
func runAlbum(t *testing.T, f *fakeImmich, albumName string) syncer.Counts {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	svc := service(
		[]*syncer.Account{{Name: "alice", API: newAPI(t, srv.URL, "src")}},
		[]*syncer.Account{{Name: "family", API: newAPI(t, srv.URL, "dest")}})
	svc.Album = albumName
	s := &syncer.Syncer{Services: []*syncer.Service{svc}, Log: quiet()}
	s.Run(context.Background(), "test", false)
	return s.Last().Counts
}

func TestArrivalIsPutInANewAlbumInsteadOfTaggedInbox(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created"}
	c := runAlbum(t, f, "Holiday 2026")
	if c.Imported != 1 || c.Errors != 0 {
		t.Fatalf("counts = %+v", c)
	}
	if len(f.albumCreated) != 1 || f.albumCreated[0] != "Holiday 2026" {
		t.Errorf("the album must be made once, under the configured name: %v", f.albumCreated)
	}
	if len(f.albumAdded) != 1 || f.albumAdded[0] != dstAsset {
		t.Errorf("the copy must be put in the album: %v", f.albumAdded)
	}
	if len(f.tagged) != 0 {
		t.Errorf("a photo that is in the album does not also get the inbox tag: %v", f.tagged)
	}
	if len(f.deleted) != 1 {
		t.Errorf("the original is still removed once the copy is verified: %v", f.deleted)
	}
}

func TestAnExistingAlbumIsUsedNotDuplicated(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created", existingAlbum: "Holiday 2026"}
	runAlbum(t, f, "Holiday 2026")
	if len(f.albumCreated) != 0 || len(f.albumAdded) != 1 {
		t.Errorf("created %v, added %v", f.albumCreated, f.albumAdded)
	}
}

func TestACopyThatAlreadyExistedIsNotPutInTheAlbum(t *testing.T) {
	f := &fakeImmich{uploadStatus: "duplicate"}
	c := runAlbum(t, f, "Holiday 2026")
	if c.Duplicate != 1 || len(f.albumAdded) != 0 || len(f.albumCreated) != 0 {
		t.Errorf("a photo the receiver already had must be left where they put it: counts %+v, created %v, added %v", c, f.albumCreated, f.albumAdded)
	}
}

func TestWhenTheAlbumCannotBeUsedTheCopyIsTaggedInboxAndNothingFails(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created", albumDenied: true}
	c := runAlbum(t, f, "Holiday 2026")
	if c.Imported != 1 || c.Errors != 0 {
		t.Fatalf("the hand-over must not fail because of the album: %+v", c)
	}
	if len(f.tagged) != 1 {
		t.Errorf("the copy must still be findable, so it gets the inbox tag: %v", f.tagged)
	}
	if len(f.deleted) != 1 {
		t.Errorf("and the original is removed as usual: %v", f.deleted)
	}
}

func TestNoAlbumMeansNoAlbumCalls(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created", albumDenied: true} // would fail loudly if the app touched albums
	c := run(t, f)
	if c.Imported != 1 || c.Errors != 0 || len(f.albumCreated) != 0 || len(f.albumAdded) != 0 || len(f.tagged) != 1 {
		t.Errorf("a service without an album must behave as before: %+v tagged %v", c, f.tagged)
	}
}

func TestImportStreamsVerifiesTagsAndDeletesOriginal(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created"}
	c := run(t, f)
	if c.Imported != 1 || c.Errors != 0 {
		t.Fatalf("counts = %+v", c)
	}
	if f.uploadedBytes != payload {
		t.Errorf("uploaded %q", f.uploadedBytes)
	}
	if len(f.tagged) != 1 || len(f.deleted) != 1 || f.deleted[0] != srcAsset {
		t.Errorf("tagged=%v deleted=%v", f.tagged, f.deleted)
	}
}

func TestDuplicateIsNotTaggedButOriginalIsRemoved(t *testing.T) {
	f := &fakeImmich{uploadStatus: "duplicate"}
	c := run(t, f)
	if c.Duplicate != 1 || len(f.tagged) != 0 || len(f.deleted) != 1 {
		t.Errorf("counts=%+v tagged=%v deleted=%v", c, f.tagged, f.deleted)
	}
}

func TestTrashedCopyRefusesAndKeepsOriginal(t *testing.T) {
	f := &fakeImmich{uploadStatus: "duplicate", destTrashed: true}
	c := run(t, f)
	if c.Refused != 1 || len(f.deleted) != 0 {
		t.Errorf("counts=%+v deleted=%v", c, f.deleted)
	}
	if len(f.refusedTagged) != 1 || len(f.untaggedShare) != 1 {
		t.Errorf("original must be retagged: refusedTagged=%v untaggedShare=%v", f.refusedTagged, f.untaggedShare)
	}
}

func TestChecksumMismatchKeepsOriginal(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created", storedChecksum: "AAAA"}
	c := run(t, f)
	if c.Errors != 1 || len(f.deleted) != 0 || len(f.tagged) != 0 {
		t.Errorf("counts=%+v deleted=%v tagged=%v", c, f.deleted, f.tagged)
	}
}

// One-to-many: the sender's server also hosts receiver 1; receiver 2 is a second server.
func twoReceivers(t *testing.T, a, b *fakeImmich) syncer.Counts {
	t.Helper()
	sa, sb := httptest.NewServer(a.handler()), httptest.NewServer(b.handler())
	t.Cleanup(sa.Close)
	t.Cleanup(sb.Close)
	svc := service(
		[]*syncer.Account{{Name: "alice", API: newAPI(t, sa.URL, "src")}},
		[]*syncer.Account{{Name: "family", API: newAPI(t, sa.URL, "dest")}, {Name: "grandma", API: newAPI(t, sb.URL, "dest2")}})
	s := &syncer.Syncer{Services: []*syncer.Service{svc}, Log: quiet()}
	s.Run(context.Background(), "test", false)
	return s.Last().Counts
}

func TestOneToManyRemovesOriginalOnlyAfterEveryReceiverHasACopy(t *testing.T) {
	a, b := &fakeImmich{uploadStatus: "created"}, &fakeImmich{uploadStatus: "created"}
	c := twoReceivers(t, a, b)
	if c.Imported != 1 || c.Errors != 0 {
		t.Fatalf("counts = %+v", c)
	}
	if len(a.uploaded) != 1 || len(b.uploaded) != 1 || len(a.tagged) != 1 || len(b.tagged) != 1 {
		t.Errorf("both receivers must get and tag a copy: a=%v/%v b=%v/%v", a.uploaded, a.tagged, b.uploaded, b.tagged)
	}
	if len(a.deleted) != 1 {
		t.Errorf("original must be removed once, got %v", a.deleted)
	}
}

func TestOneToManyKeepsOriginalWhenOneReceiverRefuses(t *testing.T) {
	a, b := &fakeImmich{uploadStatus: "created"}, &fakeImmich{uploadStatus: "duplicate", destTrashed: true}
	c := twoReceivers(t, a, b)
	if c.Refused != 1 || len(a.deleted) != 0 {
		t.Fatalf("counts=%+v deleted=%v", c, a.deleted)
	}
	if len(a.refusedTagged) != 1 || len(a.untaggedShare) != 1 {
		t.Errorf("original must be retagged: %v %v", a.refusedTagged, a.untaggedShare)
	}
}

func TestOneToManyKeepsOriginalWhenOneReceiverFails(t *testing.T) {
	a, b := &fakeImmich{uploadStatus: "created"}, &fakeImmich{uploadStatus: "created", storedChecksum: "AAAA"}
	c := twoReceivers(t, a, b)
	if c.Errors != 1 || len(a.deleted) != 0 {
		t.Errorf("counts=%+v deleted=%v", c, a.deleted)
	}
}

// A paused service has no receivers. Running it must never delete an original with no copy anywhere.
func TestServiceWithoutAHealthyReceiverTouchesNothing(t *testing.T) {
	for _, broken := range []string{"", "receiver key deleted"} {
		f := &fakeImmich{uploadStatus: "created"}
		srv := httptest.NewServer(f.handler())
		svc := service([]*syncer.Account{{Name: "alice", API: newAPI(t, srv.URL, "src")}}, nil)
		svc.Broken = broken
		s := &syncer.Syncer{Services: []*syncer.Service{svc}, Log: quiet()}
		s.Run(context.Background(), "test", false)
		srv.Close()
		if len(f.deleted) != 0 || len(f.uploaded) != 0 {
			t.Errorf("broken=%q: deleted=%v uploaded=%v", broken, f.deleted, f.uploaded)
		}
	}
}

func TestCopyModeKeepsTheOriginalAndMarksItHandedOver(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created"}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	svc := service(
		[]*syncer.Account{{Name: "alice", API: newAPI(t, srv.URL, "src")}},
		[]*syncer.Account{{Name: "family", API: newAPI(t, srv.URL, "dest")}})
	svc.Mode, svc.SentTag = syncer.ModeCopy, "handed-over"
	s := &syncer.Syncer{Services: []*syncer.Service{svc}, Log: quiet()}
	s.Run(context.Background(), "test", false)
	c := s.Last().Counts
	if c.Imported != 1 || c.Errors != 0 {
		t.Fatalf("counts = %+v", c)
	}
	if len(f.deleted) != 0 {
		t.Errorf("copy mode must never delete the original: %v", f.deleted)
	}
	if len(f.uploaded) != 1 || len(f.tagged) != 1 {
		t.Errorf("the receiver still gets a tagged copy: %v %v", f.uploaded, f.tagged)
	}
	if len(f.refusedTagged) != 1 || len(f.untaggedShare) != 1 {
		t.Errorf("the original must lose the service tag and get the sent tag: sent=%v untagged=%v", f.refusedTagged, f.untaggedShare)
	}
}

// Immich's search can keep listing a photo for a while after its tag was removed. It must not be handed
// over again (downloaded and uploaded once more) while that lasts.
func TestAPhotoJustHandedOverIsNotHandedOverAgain(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created"} // this fake keeps listing the photo, like a lagging search
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	svc := service(
		[]*syncer.Account{{Name: "alice", API: newAPI(t, srv.URL, "src"), Mode: syncer.ModeCopy}},
		[]*syncer.Account{{Name: "family", API: newAPI(t, srv.URL, "dest")}})
	svc.SentTag = "handed-over"
	s := &syncer.Syncer{Services: []*syncer.Service{svc}, Log: quiet()}
	for i := 0; i < 3; i++ {
		s.Run(context.Background(), "test", false)
	}
	if len(f.uploaded) != 1 {
		t.Errorf("the photo was uploaded %d times; once is enough", len(f.uploaded))
	}
	if c := s.Totals().Counts; c.Imported != 1 || c.Duplicate != 0 {
		t.Errorf("later passes must skip it: %+v", c)
	}
}

// Immich can accept a tag on a fresh upload and then lose it. A copy without its inbox tag would arrive
// unnoticed, so the app must check the tag is there and add it again.
func TestAReceiversCopyThatLosesItsInboxTagIsTaggedAgain(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created", dropInboxTags: 2}
	c := run(t, f)
	if c.Imported != 1 || c.Errors != 0 {
		t.Fatalf("counts = %+v", c)
	}
	if len(f.tagged) != 1 {
		t.Errorf("the copy must end up tagged (after the dropped attempts), got %d", len(f.tagged))
	}
	if len(f.deleted) != 1 {
		t.Errorf("the original goes to the trash once the copy is tagged: %v", f.deleted)
	}
}

func TestACopyThatNeverKeepsItsTagLeavesTheOriginalAlone(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created", dropInboxTags: 99}
	c := run(t, f)
	if c.Errors != 1 || len(f.deleted) != 0 {
		t.Errorf("counts=%+v deleted=%v: if the tag cannot be made to stick the original must be kept", c, f.deleted)
	}
}

// runWatch runs a service whose sender "alice" has an album linked.
func runWatch(t *testing.T, f *fakeImmich, tune func(*syncer.Service), p ...syncer.Provider) (syncer.Counts, *syncer.Syncer) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	alice := &syncer.Account{Name: "alice", API: newAPI(t, srv.URL, "src"), WatchAlbum: uuid.MustParse(albumID), WatchAlbumName: "To send"}
	svc := service([]*syncer.Account{alice}, []*syncer.Account{{Name: "family", API: newAPI(t, srv.URL, "dest")}})
	svc.SentTag = "handed-over"
	if tune != nil {
		tune(svc)
	}
	s := &syncer.Syncer{Services: []*syncer.Service{svc}, Log: quiet()}
	if len(p) > 0 {
		s.Provider = p[0]
	}
	s.Run(context.Background(), "test", false)
	return s.Last().Counts, s
}

func TestAPhotoInTheLinkedAlbumIsHandedOverWithoutATag(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created", noTagged: true, watchItems: []string{srcAsset}}
	c, _ := runWatch(t, f, nil)
	if c.Imported != 1 || c.Errors != 0 {
		t.Fatalf("counts = %+v", c)
	}
	if len(f.deleted) != 1 || f.deleted[0] != srcAsset {
		t.Errorf("move mode: the original goes to the trash once the copy is verified: %v", f.deleted)
	}
	if len(f.untaggedShare) != 0 {
		t.Errorf("it never had the service tag, so none may be taken off: %v", f.untaggedShare)
	}
}

func TestInCopyModeAnAlbumPhotoIsMarkedHandedOverAndKept(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created", noTagged: true, watchItems: []string{srcAsset}}
	c, _ := runWatch(t, f, func(s *syncer.Service) { s.Mode = syncer.ModeCopy })
	if c.Imported != 1 || c.Errors != 0 || len(f.deleted) != 0 {
		t.Fatalf("copy mode keeps the original: %+v deleted %v", c, f.deleted)
	}
	if len(f.refusedTagged) != 1 { // the fake records every tag it is asked to add to the original here
		t.Errorf("the original must be marked as handed over so it is not handed over again: %v", f.refusedTagged)
	}
	if len(f.untaggedShare) != 0 {
		t.Errorf("no service tag to take off: %v", f.untaggedShare)
	}
}

func TestAPhotoThatCarriesTheHandedOverTagIsLeftAlone(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created", noTagged: true, watchItems: []string{srcAsset}, sentTagged: []string{srcAsset}}
	c, _ := runWatch(t, f, func(s *syncer.Service) { s.Mode = syncer.ModeCopy })
	if c.Imported != 0 || len(f.uploaded) != 0 {
		t.Errorf("a photo already handed over must not be handed over again: %+v uploaded %v", c, f.uploaded)
	}
}

func TestOtherPeoplesPhotosInTheAlbumAreNotHandedOver(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created", noTagged: true, watchItems: []string{srcAsset}, watchOwnerOther: true}
	c, _ := runWatch(t, f, nil)
	if c.Imported != 0 || len(f.uploaded) != 0 || len(f.deleted) != 0 {
		t.Errorf("only the sender's own photos may be used: %+v uploaded %v deleted %v", c, f.uploaded, f.deleted)
	}
}

func TestAPhotoBothTaggedAndInTheAlbumIsHandedOverOnce(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created", watchItems: []string{srcAsset}} // the default search also returns it as tagged
	c, _ := runWatch(t, f, func(s *syncer.Service) { s.Mode = syncer.ModeCopy })
	if c.Imported != 1 || len(f.uploaded) != 1 {
		t.Errorf("once: %+v uploaded %v", c, f.uploaded)
	}
	if len(f.untaggedShare) != 1 {
		t.Errorf("being tagged, it loses the service tag as usual: %v", f.untaggedShare)
	}
}

func TestAnUnreadableAlbumDoesNotStopTaggedPhotos(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created", watchSearchFails: true} // the tagged photo comes from the default search
	c, _ := runWatch(t, f, nil)
	if c.Imported != 1 || c.Errors != 0 {
		t.Errorf("tagged photos are still handed over and the album problem is not a hand-over error: %+v", c)
	}
}

type reportingProvider struct {
	svcs []*syncer.Service
	got  [][3]int
}

func (p *reportingProvider) Services() []*syncer.Service { return p.svcs }
func (p *reportingProvider) AlbumHandedOver(_ *syncer.Service, _ *syncer.Account, handed, refused, failed int) {
	p.got = append(p.got, [3]int{handed, refused, failed})
}

func TestWhatWasHandedOverFromTheAlbumIsReported(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created", noTagged: true, watchItems: []string{srcAsset}}
	rp := &reportingProvider{}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	alice := &syncer.Account{Name: "alice", API: newAPI(t, srv.URL, "src"), WatchAlbum: uuid.MustParse(albumID), WatchAlbumName: "To send"}
	rp.svcs = []*syncer.Service{service([]*syncer.Account{alice}, []*syncer.Account{{Name: "family", API: newAPI(t, srv.URL, "dest")}})}
	s := &syncer.Syncer{Provider: rp, Log: quiet()}
	s.Run(context.Background(), "test", false)
	if len(rp.got) != 1 || rp.got[0] != [3]int{1, 0, 0} {
		t.Errorf("the pass must report one photo handed over from the album: %v", rp.got)
	}
	if !s.HasWatchers() {
		t.Error("with an album linked the timer has something to look at")
	}
}

func TestNoLinkedAlbumMeansNothingToWatch(t *testing.T) {
	f := &fakeImmich{uploadStatus: "created"}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	svc := service([]*syncer.Account{{Name: "alice", API: newAPI(t, srv.URL, "src")}}, []*syncer.Account{{Name: "family", API: newAPI(t, srv.URL, "dest")}})
	if (&syncer.Syncer{Services: []*syncer.Service{svc}, Log: quiet()}).HasWatchers() {
		t.Error("no album linked, nothing to watch")
	}
}
