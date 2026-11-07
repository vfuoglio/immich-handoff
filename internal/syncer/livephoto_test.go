package syncer_test

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// twoAccounts is a sender's Immich and a receiver's in one server, told apart by the API key ("src", "dest"), that
// keeps what it is given: the assets, their tags and links, what was uploaded and how, and what was deleted. It is
// there to follow a live photo: a still, and a separate hidden motion clip it is linked to.
type twoAccounts struct {
	mu       sync.Mutex
	assets   map[string]map[string]*fakeAsset // account -> id -> asset
	tags     map[string]map[string]string     // account -> tag name -> id
	uploads  []fakeUpload
	deleted  [][]string // each delete request, as the ids it named
	next     int
	dropLink bool   // the receiver ignores the link to the motion clip
	noClip   string // the sender cannot read this asset (its motion clip)
}

type fakeAsset struct {
	id, name string
	data     []byte
	motion   string
	hidden   bool
	trashed  bool
	tags     map[string]bool
}

type fakeUpload struct {
	id, name, status string
	hidden           bool
	motion           string
}

const (
	liveStill = "b0000000-0000-4000-8000-000000000001"
	liveClip  = "b0000000-0000-4000-8000-000000000002"
)

func newTwoAccounts() *twoAccounts {
	return &twoAccounts{
		assets: map[string]map[string]*fakeAsset{"src": {}, "dest": {}},
		tags:   map[string]map[string]string{"src": {"share": shareTag}, "dest": {"inbox": inboxTag}},
	}
}

func (f *twoAccounts) addSender(a *fakeAsset) { f.assets["src"][a.id] = a }

func (f *twoAccounts) handler() http.Handler {
	j := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	sum := func(b []byte) string { h := sha1.Sum(b); return base64.StdEncoding.EncodeToString(h[:]) }
	dto := func(who string, a *fakeAsset) map[string]any {
		d := map[string]any{"id": a.id, "originalFileName": a.name, "fileCreatedAt": ts, "fileModifiedAt": ts, "isFavorite": false,
			"isTrashed": a.trashed, "checksum": sum(a.data), "hasMetadata": true, "exifInfo": map[string]any{"fileSizeInByte": len(a.data)}}
		if a.motion != "" {
			d["livePhotoVideoId"] = a.motion
		}
		if a.hidden {
			d["visibility"] = "hidden"
		}
		var tags []any
		for name := range a.tags {
			tags = append(tags, map[string]any{"id": f.tags[who][name], "name": name, "value": name, "createdAt": ts, "updatedAt": ts})
		}
		d["tags"] = tags
		return d
	}
	who := func(r *http.Request) string { return r.Header.Get("x-api-key") }
	tagName := func(w string, id string) string {
		for n, i := range f.tags[w] {
			if i == id {
				return n
			}
		}
		return ""
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/users/me", func(w http.ResponseWriter, r *http.Request) {
		h := sha1.Sum([]byte(who(r)))
		j(w, 200, map[string]any{"id": fmt.Sprintf("aaaaaaaa-0000-0000-0000-%012x", h[:6]), "name": "n", "email": "a@b.c"})
	})
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []any{}
		for n, id := range f.tags[who(r)] {
			out = append(out, map[string]any{"id": id, "name": n, "value": n, "createdAt": ts, "updatedAt": ts})
		}
		j(w, 200, out)
	})
	mux.HandleFunc("PUT /api/tags", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Tags []string }
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []any{}
		for _, n := range body.Tags {
			if _, ok := f.tags[who(r)][n]; !ok {
				f.next++
				f.tags[who(r)][n] = fmt.Sprintf("c0000000-0000-4000-8000-%012d", f.next)
			}
			out = append(out, map[string]any{"id": f.tags[who(r)][n], "name": n, "value": n, "createdAt": ts, "updatedAt": ts})
		}
		j(w, 200, out)
	})
	tagAssets := func(add bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var body struct{ Ids []string }
			json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			defer f.mu.Unlock()
			name := tagName(who(r), r.PathValue("id"))
			out := []any{}
			for _, id := range body.Ids {
				if a := f.assets[who(r)][id]; a != nil {
					if a.tags == nil {
						a.tags = map[string]bool{}
					}
					if add {
						a.tags[name] = true
					} else {
						delete(a.tags, name)
					}
				}
				out = append(out, map[string]any{"id": id, "success": true})
			}
			j(w, 200, out)
		}
	}
	mux.HandleFunc("PUT /api/tags/{id}/assets", tagAssets(true))
	mux.HandleFunc("DELETE /api/tags/{id}/assets", tagAssets(false))
	mux.HandleFunc("POST /api/search/metadata", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			TagIds []string `json:"tagIds"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		items := []any{}
		if len(body.TagIds) > 0 {
			name := tagName(who(r), body.TagIds[0])
			for _, a := range f.assets[who(r)] {
				if a.tags[name] && !a.trashed {
					items = append(items, dto(who(r), a))
				}
			}
		}
		j(w, 200, map[string]any{"assets": map[string]any{"items": items, "count": len(items), "total": len(items), "facets": []any{}}})
	})
	mux.HandleFunc("GET /api/assets/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		a := f.assets[who(r)][r.PathValue("id")]
		if a == nil || (who(r) == "src" && r.PathValue("id") == f.noClip) {
			j(w, 404, map[string]any{"message": "not found"})
			return
		}
		j(w, 200, dto(who(r), a))
	})
	mux.HandleFunc("GET /api/assets/{id}/original", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if a := f.assets[who(r)][r.PathValue("id")]; a != nil {
			w.Write(a.data)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("POST /api/assets", func(w http.ResponseWriter, r *http.Request) {
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		up, a := fakeUpload{}, &fakeAsset{tags: map[string]bool{}}
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(p)
			switch p.FormName() {
			case "assetData":
				a.name, a.data = p.FileName(), b
			case "visibility":
				a.hidden = string(b) == "hidden"
			case "livePhotoVideoId":
				if !f.dropLink {
					a.motion = string(b)
				}
				up.motion = string(b)
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, have := range f.assets["dest"] { // the same bytes are never stored twice
			if string(have.data) == string(a.data) {
				up.id, up.name, up.status, up.hidden = have.id, a.name, "duplicate", a.hidden
				f.uploads = append(f.uploads, up)
				j(w, 200, map[string]any{"id": have.id, "status": "duplicate"})
				return
			}
		}
		f.next++
		a.id = fmt.Sprintf("d0000000-0000-4000-8000-%012d", f.next)
		f.assets["dest"][a.id] = a
		up.id, up.name, up.status, up.hidden = a.id, a.name, "created", a.hidden
		f.uploads = append(f.uploads, up)
		j(w, 201, map[string]any{"id": a.id, "status": "created"})
	})
	mux.HandleFunc("DELETE /api/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Ids []string }
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.deleted = append(f.deleted, body.Ids)
		for _, id := range body.Ids {
			if a := f.assets[who(r)][id]; a != nil {
				a.trashed = true
			}
		}
		w.WriteHeader(204)
	})
	return mux
}

func (f *twoAccounts) hand(t *testing.T, tune func(*syncer.Service)) syncer.Counts {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	svc := service(
		[]*syncer.Account{{Name: "alice", API: newAPI(t, srv.URL, "src")}},
		[]*syncer.Account{{Name: "family", API: newAPI(t, srv.URL, "dest")}})
	if tune != nil {
		tune(svc)
	}
	s := &syncer.Syncer{Services: []*syncer.Service{svc}, Log: quiet()}
	s.Run(context.Background(), "test", false)
	return s.Last().Counts
}

func livePhoto() *twoAccounts {
	f := newTwoAccounts()
	f.addSender(&fakeAsset{id: liveStill, name: "IMG_1.HEIC", data: []byte("the still"), motion: liveClip, tags: map[string]bool{"share": true}})
	f.addSender(&fakeAsset{id: liveClip, name: "IMG_1.MOV", data: []byte("the motion clip"), hidden: true})
	return f
}

// destStill is the receiver's copy of the still and its motion clip, if they arrived.
func (f *twoAccounts) destCopies() (still, clip *fakeAsset) {
	for _, a := range f.assets["dest"] {
		if a.name == "IMG_1.HEIC" {
			still = a
		}
		if a.name == "IMG_1.MOV" {
			clip = a
		}
	}
	return
}

func TestALivePhotoArrivesWithItsMotionClipHiddenAndLinked(t *testing.T) {
	f := livePhoto()
	c := f.hand(t, nil)
	if c.Imported != 1 || c.Errors != 0 {
		t.Fatalf("counts: %+v", c)
	}
	still, clip := f.destCopies()
	if still == nil || clip == nil {
		t.Fatalf("the receiver has still=%v clip=%v", still != nil, clip != nil)
	}
	if !clip.hidden {
		t.Error("the motion clip must arrive hidden, as it is at the sender")
	}
	if still.hidden {
		t.Error("the still must not be hidden")
	}
	if still.motion != clip.id {
		t.Errorf("the still is linked to %q, want the receiver's copy of the clip %q", still.motion, clip.id)
	}
	if !still.tags["inbox"] || clip.tags["inbox"] {
		t.Errorf("only the still is tagged for the inbox (still %v, clip %v)", still.tags, clip.tags)
	}
	if len(f.uploads) != 2 || f.uploads[0].name != "IMG_1.MOV" {
		t.Errorf("the clip goes first, so the still can be uploaded linked to it: %+v", f.uploads)
	}
}

func TestMovingALivePhotoTrashesTheStillAndItsClipTogether(t *testing.T) {
	f := livePhoto()
	f.hand(t, nil) // the default is to move
	if len(f.deleted) != 1 || len(f.deleted[0]) != 2 || !slices.Contains(f.deleted[0], liveStill) || !slices.Contains(f.deleted[0], liveClip) {
		t.Errorf("one request must remove the still and its clip together, got %v", f.deleted)
	}
}

func TestCopyingALivePhotoLeavesTheStillAndItsClipWithTheSender(t *testing.T) {
	f := livePhoto()
	f.hand(t, func(s *syncer.Service) { s.Mode, s.SentTag = syncer.ModeCopy, "handed-over" })
	if len(f.deleted) != 0 {
		t.Errorf("copy mode removed %v", f.deleted)
	}
	sent := f.assets["src"][liveStill]
	if !sent.tags["handed-over"] || sent.tags["share"] {
		t.Errorf("the original still must be marked handed over: %v", sent.tags)
	}
	if clip := f.assets["src"][liveClip]; clip.trashed || len(clip.tags) != 0 {
		t.Errorf("the sender's clip is left exactly as it was: %+v", clip)
	}
	if _, clipCopy := f.destCopies(); clipCopy == nil {
		t.Error("the clip is handed over in copy mode as well")
	}
}

func TestAClipThatCannotBeReadMeansOnlyTheStillIsHandedOver(t *testing.T) {
	f := livePhoto()
	f.noClip = liveClip
	c := f.hand(t, nil)
	if c.Imported != 1 || c.Errors != 0 {
		t.Fatalf("the photo must still be handed over: %+v", c)
	}
	still, clip := f.destCopies()
	if still == nil || clip != nil || still.motion != "" {
		t.Errorf("only the still arrives, unlinked: still=%v clip=%v", still != nil, clip != nil)
	}
	if len(f.deleted) != 1 || len(f.deleted[0]) != 1 || f.deleted[0][0] != liveStill {
		t.Errorf("only the still is removed: %v", f.deleted)
	}
}

func TestAClipTheReceiverDidNotLinkIsNotRemovedFromTheSender(t *testing.T) {
	f := livePhoto()
	f.dropLink = true // the receiver's Immich keeps the still but not the link
	c := f.hand(t, nil)
	if c.Imported != 1 {
		t.Fatalf("counts: %+v", c)
	}
	if len(f.deleted) != 1 || len(f.deleted[0]) != 1 || f.deleted[0][0] != liveStill {
		t.Errorf("a clip that did not arrive linked must stay with the sender, got %v", f.deleted)
	}
}

func TestAPlainPhotoIsUnchanged(t *testing.T) {
	f := newTwoAccounts()
	f.addSender(&fakeAsset{id: liveStill, name: "plain.jpg", data: []byte("plain"), tags: map[string]bool{"share": true}})
	f.hand(t, nil)
	if len(f.uploads) != 1 || f.uploads[0].hidden || f.uploads[0].motion != "" {
		t.Errorf("a photo without a clip is one plain upload: %+v", f.uploads)
	}
	if len(f.deleted) != 1 || len(f.deleted[0]) != 1 {
		t.Errorf("deleted %v", f.deleted)
	}
}

func TestHandingALivePhotoOverAgainDoesNotMakeASecondCopy(t *testing.T) {
	f := livePhoto()
	f.hand(t, func(s *syncer.Service) { s.Mode, s.SentTag = syncer.ModeCopy, "handed-over" })
	f.assets["src"][liveStill].tags = map[string]bool{"share": true} // tagged again, for example after a failure
	f.hand(t, func(s *syncer.Service) { s.Mode, s.SentTag = syncer.ModeCopy, "handed-over" })
	n := 0
	for _, a := range f.assets["dest"] {
		if strings.HasPrefix(a.name, "IMG_1") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("the receiver has %d files from the live photo, want the still and the clip once each", n)
	}
	_, clip := f.destCopies()
	still, _ := f.destCopies()
	if still.motion != clip.id {
		t.Errorf("the link survived the second hand-over: %q vs %q", still.motion, clip.id)
	}
}
