package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

const photoBytes = "pretend these are JPEG bytes"

// source is the sender's server: it only has to hand out originals.
func source(t *testing.T) *immich.API {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/original") {
			io.WriteString(w, photoBytes)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(s.Close)
	api, err := immich.New(s.URL, immich.Auth{APIKey: "sender"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return api
}

// destination is the other server as a four-permission key sees it: upload and tags, and a 403 for the rest.
type destination struct {
	*httptest.Server
	mu        sync.Mutex
	status    string // created | duplicate
	down      int    // answer everything with this status
	uploads   []string
	tags      []string // names made
	tagged    []string // "tagname:assetid"
	forbidden []string // other paths asked for
	motion    []string
	perms     []string // what /api-keys/me answers; nil: the four
	trashed   []string // ids moved to the trash
	trashCode int      // answer DELETE /assets with this status instead (0: 204)
}

func newDestination(t *testing.T) *destination {
	t.Helper()
	d := &destination{status: "created"}
	tagID := uuid.NewString()
	assetID := uuid.NewString()
	d.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.down != 0 {
			http.Error(w, "no", d.down)
			return
		}
		if r.URL.Path == "/api/server/version" { // asked without a key
			io.WriteString(w, `{"major":3,"minor":2,"patch":0}`)
			return
		}
		if r.Header.Get("x-api-key") != "good-key" {
			http.Error(w, `{"message":"Invalid API key"}`, 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/assets":
			r.ParseMultipartForm(1 << 20)
			f, hdr, err := r.FormFile("assetData")
			if err != nil {
				http.Error(w, "no file", 400)
				return
			}
			b, _ := io.ReadAll(f)
			d.uploads = append(d.uploads, hdr.Filename+"="+string(b))
			if m := r.FormValue("livePhotoVideoId"); m != "" {
				d.motion = append(d.motion, m)
			}
			code := 201
			if d.status == "duplicate" {
				code = 200
			}
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(map[string]string{"id": assetID, "status": d.status})
		case r.Method == "PUT" && r.URL.Path == "/api/tags":
			var body struct{ Tags []string }
			json.NewDecoder(r.Body).Decode(&body)
			d.tags = append(d.tags, body.Tags...)
			json.NewEncoder(w).Encode([]map[string]any{{"id": tagID, "name": body.Tags[0], "value": body.Tags[0], "createdAt": "2025-01-01T00:00:00.000Z", "updatedAt": "2025-01-01T00:00:00.000Z"}})
		case r.Method == "PUT" && r.URL.Path == "/api/tags/"+tagID+"/assets":
			var body struct{ Ids []string }
			json.NewDecoder(r.Body).Decode(&body)
			var out []map[string]any
			for _, id := range body.Ids {
				d.tagged = append(d.tagged, d.tags[len(d.tags)-1]+":"+id)
				out = append(out, map[string]any{"id": id, "success": true})
			}
			json.NewEncoder(w).Encode(out)
		case r.Method == "GET" && r.URL.Path == "/api/api-keys/me":
			perms := d.perms
			if perms == nil {
				perms = RequiredPermissions
			}
			json.NewEncoder(w).Encode(map[string]any{"id": "k", "name": "handoff", "permissions": perms})
		case r.Method == "DELETE" && r.URL.Path == "/api/assets" && slicesContains(d.perms, "asset.delete"):
			var body struct {
				Ids   []string
				Force bool
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.Force {
				http.Error(w, `{"message":"never for good"}`, 400)
				return
			}
			if d.trashCode != 0 {
				http.Error(w, `{"message":"no"}`, d.trashCode)
				return
			}
			d.trashed = append(d.trashed, body.Ids...)
			w.WriteHeader(204)
		default:
			d.forbidden = append(d.forbidden, r.Method+" "+r.URL.Path)
			http.Error(w, `{"message":"Missing required permission"}`, 403)
		}
	}))
	t.Cleanup(d.Close)
	return d
}

func asset(name string) immich.AssetResponseDto {
	now := time.Now()
	return immich.AssetResponseDto{Id: uuid.New(), OriginalFileName: name, FileCreatedAt: now, FileModifiedAt: now}
}

func remote(t *testing.T, d *destination, key string) *Remote {
	t.Helper()
	r, err := NewRemote("Aunt Lia", d.URL, key, Policy{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func init() { retagAfter = 20 * time.Millisecond }

func TestDeliverUploadsAndTagsWithoutReadingAnything(t *testing.T) {
	d := newDestination(t)
	r := remote(t, d, "good-key")
	if _, err := r.Deliver(context.Background(), source(t), asset("IMG_1.jpg"), nil, syncer.RemoteJob{Sender: "Bob"}); err != nil {
		t.Fatal(err)
	}
	if len(d.uploads) != 1 || d.uploads[0] != "IMG_1.jpg="+photoBytes {
		t.Fatalf("uploads %v", d.uploads)
	}
	if len(d.tags) != 1 || d.tags[0] != "from/Bob" || len(d.tagged) != 1 {
		t.Fatalf("tags %v tagged %v", d.tags, d.tagged)
	}
	if len(d.forbidden) != 0 {
		t.Fatalf("the bridge asked for something its key cannot do: %v", d.forbidden)
	}
	// the tag is put on a second time a little later, because Immich now and then drops the first
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		n := len(d.tagged)
		d.mu.Unlock()
		if n == 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the tag was not put on again")
}

func TestADuplicateIsSuccessAndASafeRetry(t *testing.T) {
	d := newDestination(t)
	d.status = "duplicate"
	r := remote(t, d, "good-key")
	for i := 0; i < 2; i++ {
		if _, err := r.Deliver(context.Background(), source(t), asset("IMG_1.jpg"), nil, syncer.RemoteJob{Sender: "Bob"}); err != nil {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	if len(d.tagged) != 2 {
		t.Fatalf("a duplicate is tagged again, which is harmless: %v", d.tagged)
	}
}

func TestALivePhotoGoesWithItsHiddenClip(t *testing.T) {
	d := newDestination(t)
	r := remote(t, d, "good-key")
	clip := asset("IMG_1.mov")
	if _, err := r.Deliver(context.Background(), source(t), asset("IMG_1.jpg"), &clip, syncer.RemoteJob{Sender: "Bob"}); err != nil {
		t.Fatal(err)
	}
	if len(d.uploads) != 2 || len(d.motion) != 1 {
		t.Fatalf("uploads %v motion %v: the still must be linked to the clip", d.uploads, d.motion)
	}
}

func TestDeliverSaysRevokedAndUnreachable(t *testing.T) {
	d := newDestination(t)
	if _, err := remote(t, d, "revoked").Deliver(context.Background(), source(t), asset("a.jpg"), nil, syncer.RemoteJob{Sender: "Bob"}); !errors.Is(err, ErrRevoked) {
		t.Errorf("a 401: %v", err)
	}
	d.down = 502
	if _, err := remote(t, d, "good-key").Deliver(context.Background(), source(t), asset("a.jpg"), nil, syncer.RemoteJob{Sender: "Bob"}); !errors.Is(err, ErrUnreachable) {
		t.Errorf("a 5xx: %v", err)
	}
	d.Close()
	if _, err := remote(t, d, "good-key").Deliver(context.Background(), source(t), asset("a.jpg"), nil, syncer.RemoteJob{Sender: "Bob"}); !errors.Is(err, ErrUnreachable) {
		t.Errorf("a server that is gone: %v", err)
	}
}

func TestDeliverReportsHowItEnded(t *testing.T) {
	d := newDestination(t)
	r := remote(t, d, "good-key")
	var got []error
	r.Report = func(err error, _ string) { got = append(got, err) }
	r.Deliver(context.Background(), source(t), asset("a.jpg"), nil, syncer.RemoteJob{Sender: "Bob"})
	d.down = 500
	r.Deliver(context.Background(), source(t), asset("b.jpg"), nil, syncer.RemoteJob{Sender: "Bob"})
	if len(got) != 1 || !errors.Is(got[0], ErrUnreachable) { // only failures are reported; Ready reports the healthy checks
		t.Fatalf("%v", got)
	}
}

func TestTagFor(t *testing.T) {
	for in, want := range map[string]string{"Bob": "from/Bob", " Ann Lee ": "from/Ann Lee", "a/b": "from/a-b", "": "from/someone"} {
		if got := TagFor(in); got != want {
			t.Errorf("TagFor(%q) = %q, want %q", in, got, want)
		}
	}
}

// A connection that dies while the file is going over is an unreachable server, not a lost photo: nothing is tagged
// on the other side, and the retry, when the server is back, goes through and is the only copy.
func TestAHandOverInterruptedHalfwayIsRetriedWithoutLoss(t *testing.T) {
	d := newDestination(t)
	cut := true
	var mu sync.Mutex
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		c := cut
		mu.Unlock()
		if c && r.Method == "POST" && r.URL.Path == "/api/assets" {
			io.CopyN(io.Discard, r.Body, 5) // read a little of the file, then drop the connection
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		req, _ := http.NewRequest(r.Method, d.URL+r.URL.Path, r.Body)
		req.Header = r.Header
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	defer front.Close()
	r, err := NewRemote("Aunt Lia", front.URL, "good-key", Policy{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	src := source(t)
	if _, err := r.Deliver(context.Background(), src, asset("IMG_1.jpg"), nil, syncer.RemoteJob{Sender: "Bob"}); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v", err)
	}
	if len(d.uploads) != 0 || len(d.tagged) != 0 {
		t.Fatalf("a half-sent file must leave nothing behind: %v %v", d.uploads, d.tagged)
	}
	mu.Lock()
	cut = false
	mu.Unlock()
	if _, err := r.Deliver(context.Background(), src, asset("IMG_1.jpg"), nil, syncer.RemoteJob{Sender: "Bob"}); err != nil {
		t.Fatal(err)
	}
	if len(d.uploads) != 1 || len(d.tagged) < 1 {
		t.Fatalf("the retry must deliver exactly once: %v %v", d.uploads, d.tagged)
	}
}

func TestMessagesDoNotCarryTheOtherServersAddress(t *testing.T) {
	d := newDestination(t)
	d.down = 502
	r := remote(t, d, "good-key")
	_, err := r.Deliver(context.Background(), source(t), asset("a.jpg"), nil, syncer.RemoteJob{Sender: "Bob"})
	host := strings.TrimPrefix(d.URL, "http://")
	if err == nil || strings.Contains(err.Error(), host) {
		t.Fatalf("%v", err)
	}
	d.Close()
	_, err = r.Deliver(context.Background(), source(t), asset("a.jpg"), nil, syncer.RemoteJob{Sender: "Bob"})
	if err == nil || strings.Contains(err.Error(), host) {
		t.Fatalf("a server that is gone: %v", err)
	}
}

func slicesContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestDeliverTellsWhichCopyItMadeOverThere(t *testing.T) {
	d := newDestination(t)
	r := remote(t, d, "good-key")
	clip := asset("IMG_1.mov")
	cp, err := r.Deliver(context.Background(), source(t), asset("IMG_1.jpg"), &clip, syncer.RemoteJob{Sender: "Bob"})
	if err != nil || cp.ID == uuid.Nil || !cp.Created || cp.Clip == uuid.Nil || !cp.ClipCreated {
		t.Fatalf("%+v %v", cp, err)
	}
	d.status = "duplicate"
	cp, err = r.Deliver(context.Background(), source(t), asset("IMG_2.jpg"), nil, syncer.RemoteJob{Sender: "Bob"})
	if err != nil || cp.Created {
		t.Fatalf("a duplicate was there before and is not ours to take back: %+v %v", cp, err)
	}
}

func TestTakeBackMovesTheNamedCopiesToTheTrashAndNothingElse(t *testing.T) {
	d := newDestination(t)
	d.perms = append(append([]string{}, RequiredPermissions...), "asset.delete")
	r := remote(t, d, "good-key")
	ids := []uuid.UUID{uuid.New(), uuid.New()}
	if err := r.TakeBack(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	if len(d.trashed) != 2 || d.trashed[0] != ids[0].String() || d.trashed[1] != ids[1].String() {
		t.Errorf("trashed %v", d.trashed)
	}
	if !r.CanTakeBack() {
		t.Error("the check saw the permission")
	}
	if len(d.forbidden) != 0 {
		t.Errorf("it asked for something else: %v", d.forbidden)
	}
	if err := r.TakeBack(context.Background(), nil); err != nil || len(d.trashed) != 2 {
		t.Errorf("nothing to take back asks nothing: %v %v", err, d.trashed)
	}
}

func TestTakeBackNoticesAPermissionTheReceiverRemoved(t *testing.T) {
	d := newDestination(t)
	d.perms = append(append([]string{}, RequiredPermissions...), "asset.delete")
	r := remote(t, d, "good-key")
	if err := r.Ready(context.Background()); err != nil || !r.CanTakeBack() {
		t.Fatalf("ready with the permission: %v %v", err, r.CanTakeBack())
	}
	d.perms = nil // the receiver took asset.delete off their key
	err := r.TakeBack(context.Background(), []uuid.UUID{uuid.New()})
	if !errors.Is(err, ErrCannotTakeBack) || len(d.trashed) != 0 || r.CanTakeBack() {
		t.Fatalf("%v, trashed %v, can %v", err, d.trashed, r.CanTakeBack())
	}
}

func TestTakeBackWithAKeyThatNeverHadThePermissionAsksNothingToDelete(t *testing.T) {
	d := newDestination(t)
	r := remote(t, d, "good-key")
	if err := r.TakeBack(context.Background(), []uuid.UUID{uuid.New()}); !errors.Is(err, ErrCannotTakeBack) {
		t.Fatal(err)
	}
	for _, f := range d.forbidden {
		if strings.HasPrefix(f, "DELETE") {
			t.Errorf("a delete was tried: %v", d.forbidden)
		}
	}
}

func TestTakeBackReportsAServerThatIsDownAndARevokedKey(t *testing.T) {
	d := newDestination(t)
	d.perms = append(append([]string{}, RequiredPermissions...), "asset.delete")
	if err := remote(t, d, "revoked").TakeBack(context.Background(), []uuid.UUID{uuid.New()}); !errors.Is(err, ErrRevoked) {
		t.Errorf("revoked: %v", err)
	}
	d.trashCode = 503
	if err := remote(t, d, "good-key").TakeBack(context.Background(), []uuid.UUID{uuid.New()}); !errors.Is(err, ErrUnreachable) {
		t.Errorf("a server error: %v", err)
	}
}
