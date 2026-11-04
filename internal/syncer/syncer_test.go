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
	"strings"
	"sync"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

const (
	srcAsset   = "11111111-1111-1111-1111-111111111111"
	dstAsset   = "22222222-2222-2222-2222-222222222222"
	shareTag   = "33333333-3333-3333-3333-333333333333"
	inboxTag   = "44444444-4444-4444-4444-444444444444"
	refusedTag = "66666666-6666-6666-6666-666666666666"
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
	untaggedShare     []string
	uploadedBytes     string
	gotChecksumHeader string
}

func (f *fakeImmich) handler() http.Handler {
	sum := sha1.Sum([]byte(payload))
	good := base64.StdEncoding.EncodeToString(sum[:])
	asset := func(id string, trashed bool, checksum string) map[string]any {
		return map[string]any{"id": id, "originalFileName": "IMG_1.jpg", "fileCreatedAt": ts, "fileModifiedAt": ts,
			"isFavorite": true, "isTrashed": trashed, "checksum": checksum}
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
		} else {
			j(w, 200, []any{tag(shareTag, "share")})
		}
	})
	mux.HandleFunc("POST /api/search/metadata", func(w http.ResponseWriter, r *http.Request) {
		j(w, 200, map[string]any{"assets": map[string]any{"items": []any{asset(srcAsset, false, good)}, "count": 1, "total": 1, "facets": []any{}}})
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
	mux.HandleFunc("GET /api/assets/"+dstAsset, func(w http.ResponseWriter, r *http.Request) {
		cs := f.storedChecksum
		if cs == "" {
			cs = good
		}
		j(w, 200, asset(dstAsset, f.destTrashed, cs))
	})
	mux.HandleFunc("PUT /api/tags/"+inboxTag+"/assets", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.tagged = append(f.tagged, dstAsset)
		f.mu.Unlock()
		j(w, 200, []any{map[string]any{"id": dstAsset, "success": true}})
	})
	mux.HandleFunc("PUT /api/tags", func(w http.ResponseWriter, r *http.Request) {
		j(w, 200, []any{tag(refusedTag, "share-duplicate")})
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

func newAPI(t *testing.T, url, key string) *immich.API {
	t.Helper()
	a, err := immich.New(url, immich.Auth{APIKey: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

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
