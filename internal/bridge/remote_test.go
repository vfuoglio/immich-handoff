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
	if err := r.Deliver(context.Background(), source(t), asset("IMG_1.jpg"), nil, syncer.RemoteJob{Sender: "Bob"}); err != nil {
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
		if err := r.Deliver(context.Background(), source(t), asset("IMG_1.jpg"), nil, syncer.RemoteJob{Sender: "Bob"}); err != nil {
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
	if err := r.Deliver(context.Background(), source(t), asset("IMG_1.jpg"), &clip, syncer.RemoteJob{Sender: "Bob"}); err != nil {
		t.Fatal(err)
	}
	if len(d.uploads) != 2 || len(d.motion) != 1 {
		t.Fatalf("uploads %v motion %v: the still must be linked to the clip", d.uploads, d.motion)
	}
}

func TestDeliverSaysRevokedAndUnreachable(t *testing.T) {
	d := newDestination(t)
	if err := remote(t, d, "revoked").Deliver(context.Background(), source(t), asset("a.jpg"), nil, syncer.RemoteJob{Sender: "Bob"}); !errors.Is(err, ErrRevoked) {
		t.Errorf("a 401: %v", err)
	}
	d.down = 502
	if err := remote(t, d, "good-key").Deliver(context.Background(), source(t), asset("a.jpg"), nil, syncer.RemoteJob{Sender: "Bob"}); !errors.Is(err, ErrUnreachable) {
		t.Errorf("a 5xx: %v", err)
	}
	d.Close()
	if err := remote(t, d, "good-key").Deliver(context.Background(), source(t), asset("a.jpg"), nil, syncer.RemoteJob{Sender: "Bob"}); !errors.Is(err, ErrUnreachable) {
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
	if len(got) != 2 || got[0] != nil || !errors.Is(got[1], ErrUnreachable) {
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
