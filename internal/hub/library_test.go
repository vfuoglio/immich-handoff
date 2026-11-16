package hub_test

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// libFake is a small Immich for "Add to my library": photos with owners, who can see what, uploads that are copies
// owned by whoever uploads them (a repeat of the same bytes is a duplicate), tags, albums and the trash.
type libFake struct {
	mu      sync.Mutex
	assets  map[string]*libAsset
	visible map[string]map[string]bool // asset id -> people besides the owner who can see it
	tags    map[string]string          // tag id -> name
	keys    map[string][]string        // key id -> permissions
	albums  map[string]*libAlbum
	uploads []libUpload
	deleted []string
}

type libAsset struct {
	id, owner, name string
	data            []byte
	typ             string
	trashed         bool
	created         time.Time
	live            string
	desc            string
	tags            []string
}

type libAlbum struct {
	owner, name string
	members     map[string]bool
	sharedWith  map[string]bool // people the album is shared with
}

type libUpload struct {
	person, name string
	fields       map[string]string
	sidecar      bool
	status       string
	id           string
}

func newLibFake() *libFake {
	return &libFake{assets: map[string]*libAsset{}, visible: map[string]map[string]bool{}, tags: map[string]string{}, keys: map[string][]string{}, albums: map[string]*libAlbum{}}
}

// shared adds a photo of ann's that bob can see.
func (f *libFake) shared(name string, data string) *libAsset {
	a := &libAsset{id: uuid.NewString(), owner: "ann", name: name, data: []byte(data), typ: "IMAGE", created: time.Now().Add(-48 * time.Hour), desc: "a description"}
	f.assets[a.id] = a
	f.visible[a.id] = map[string]bool{"bob": true}
	return a
}

func (f *libFake) own(person, name string, data string, age time.Duration) *libAsset {
	a := &libAsset{id: uuid.NewString(), owner: person, name: name, data: []byte(data), typ: "IMAGE", created: time.Now().Add(-age)}
	f.assets[a.id] = a
	return a
}

func (f *libFake) sum(a *libAsset) string {
	s := sha1.Sum(a.data)
	return base64.StdEncoding.EncodeToString(s[:])
}

func (f *libFake) can(person string, a *libAsset) bool {
	return a.owner == person || f.visible[a.id][person]
}

func (f *libFake) inAlbum(id string) bool {
	for _, al := range f.albums {
		if al.members[id] {
			return true
		}
	}
	return false
}

func (f *libFake) handler() http.Handler {
	j := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	who := func(r *http.Request) string { return strings.TrimPrefix(r.Header.Get("Cookie"), "sess=") }
	people := map[string]string{"bob": bobID.String(), "ann": annID.String()}
	names := map[string]string{"bob": "Bob", "ann": "Ann"}
	describe := func(a *libAsset) map[string]any {
		tags := []any{}
		for _, n := range a.tags {
			tags = append(tags, map[string]any{"id": uuid.NewString(), "name": n, "value": n, "createdAt": ts, "updatedAt": ts})
		}
		m := map[string]any{"id": a.id, "ownerId": people[a.owner], "owner": map[string]any{"id": people[a.owner], "name": names[a.owner], "email": a.owner + "@x.test", "avatarColor": "primary", "profileChangedAt": ts, "profileImagePath": ""},
			"originalFileName": a.name, "type": a.typ, "checksum": f.sum(a), "isTrashed": a.trashed, "hasMetadata": true, "tags": tags,
			"createdAt": a.created.UTC().Format(time.RFC3339), "fileCreatedAt": ts, "fileModifiedAt": ts}
		if a.desc != "" {
			m["exifInfo"] = map[string]any{"description": a.desc}
		} else {
			m["exifInfo"] = map[string]any{}
		}
		if a.live != "" {
			m["livePhotoVideoId"] = a.live
		}
		return m
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/assets/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		a := f.assets[r.PathValue("id")]
		if a == nil || !f.can(who(r), a) {
			http.Error(w, "no", 404)
			return
		}
		j(w, 200, describe(a))
	})
	mux.HandleFunc("GET /api/assets/{id}/original", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		a := f.assets[r.PathValue("id")]
		if a == nil || !f.can(who(r), a) {
			http.Error(w, "no", 404)
			return
		}
		w.Write(a.data)
	})
	mux.HandleFunc("POST /api/assets", func(w http.ResponseWriter, r *http.Request) {
		person := who(r)
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "no", 400)
			return
		}
		up := libUpload{person: person, fields: map[string]string{}}
		var data []byte
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(part)
			switch part.FormName() {
			case "assetData":
				data, up.name = b, part.FileName()
			case "sidecarData":
				up.sidecar = true
			default:
				up.fields[part.FormName()] = string(b)
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		probe := &libAsset{data: data}
		for _, a := range f.assets {
			if a.owner == person && f.sum(a) == f.sum(probe) {
				up.status, up.id = "duplicate", a.id
				f.uploads = append(f.uploads, up)
				j(w, 200, map[string]any{"id": a.id, "status": "duplicate"})
				return
			}
		}
		typ := "IMAGE"
		if strings.HasSuffix(up.name, ".mp4") {
			typ = "VIDEO"
		}
		a := &libAsset{id: uuid.NewString(), owner: person, name: up.name, data: data, typ: typ, created: time.Now(), live: up.fields["livePhotoVideoId"]}
		f.assets[a.id] = a
		up.status, up.id = "created", a.id
		f.uploads = append(f.uploads, up)
		j(w, 201, map[string]any{"id": a.id, "status": "created"})
	})
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []any{}
		for id, n := range f.tags {
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
			id := ""
			for tid, tn := range f.tags {
				if tn == n {
					id = tid
				}
			}
			if id == "" {
				id = uuid.NewString()
				f.tags[id] = n
			}
			out = append(out, map[string]any{"id": id, "name": n, "value": n, "createdAt": ts, "updatedAt": ts})
		}
		j(w, 200, out)
	})
	mux.HandleFunc("PUT /api/tags/{id}/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Ids []string }
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []any{}
		for _, id := range body.Ids {
			if a := f.assets[id]; a != nil && a.owner == who(r) {
				a.tags = append(a.tags, f.tags[r.PathValue("id")])
			}
			out = append(out, map[string]any{"id": id, "success": true})
		}
		j(w, 200, out)
	})
	mux.HandleFunc("GET /api/albums", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []any{}
		for id, al := range f.albums {
			mine := al.owner == who(r)
			if r.URL.Query().Get("isOwned") == "false" { // shared with me
				if !mine && al.sharedWith[who(r)] {
					out = append(out, map[string]any{"id": id, "albumName": al.name, "assetCount": len(al.members),
						"albumUsers": []any{map[string]any{"role": "editor", "user": map[string]any{"id": people[al.owner], "name": names[al.owner], "email": al.owner + "@x.test", "avatarColor": "primary", "profileChangedAt": ts, "profileImagePath": ""}}}})
				}
				continue
			}
			if mine && (r.URL.Query().Get("name") == "" || r.URL.Query().Get("name") == al.name) {
				out = append(out, map[string]any{"id": id, "albumName": al.name, "assetCount": len(al.members)})
			}
		}
		j(w, 200, out)
	})
	mux.HandleFunc("GET /api/users/me", func(w http.ResponseWriter, r *http.Request) {
		p := who(r)
		if people[p] == "" {
			http.Error(w, "no", 401)
			return
		}
		j(w, 200, map[string]any{"id": people[p], "name": names[p], "email": p + "@x.test"})
	})
	mux.HandleFunc("POST /api/api-keys", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name        string   `json:"name"`
			Permissions []string `json:"permissions"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		id := uuid.NewString()
		f.keys[id] = body.Permissions
		j(w, 201, map[string]any{"id": id, "name": body.Name, "secret": "secret-" + id, "permissions": body.Permissions, "createdAt": ts, "updatedAt": ts,
			"apiKey": map[string]any{"id": id, "name": body.Name, "permissions": body.Permissions, "createdAt": ts, "updatedAt": ts}})
	})
	mux.HandleFunc("GET /api/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		perms, ok := f.keys[r.PathValue("id")]
		if !ok {
			http.Error(w, "no", 404)
			return
		}
		j(w, 200, map[string]any{"id": r.PathValue("id"), "name": "k", "permissions": perms, "createdAt": ts, "updatedAt": ts})
	})
	mux.HandleFunc("PUT /api/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Permissions []string `json:"permissions"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.keys[r.PathValue("id")] = body.Permissions
		j(w, 200, map[string]any{"id": r.PathValue("id"), "name": "k", "permissions": body.Permissions, "createdAt": ts, "updatedAt": ts})
	})
	mux.HandleFunc("DELETE /api/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.keys, r.PathValue("id"))
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /api/albums", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AlbumName string `json:"albumName"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		id := uuid.NewString()
		f.albums[id] = &libAlbum{owner: who(r), name: body.AlbumName, members: map[string]bool{}}
		j(w, 201, map[string]any{"id": id, "albumName": body.AlbumName, "assetCount": 0})
	})
	mux.HandleFunc("PUT /api/albums/{id}/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Ids []string }
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []any{}
		for _, id := range body.Ids {
			f.albums[r.PathValue("id")].members[id] = true
			out = append(out, map[string]any{"id": id, "success": true})
		}
		j(w, 200, out)
	})
	mux.HandleFunc("POST /api/search/metadata", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AlbumIds     []string `json:"albumIds"`
			ID           string   `json:"id"`
			IsNotInAlbum bool     `json:"isNotInAlbum"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		items := []any{}
		add := func(a *libAsset) { items = append(items, describe(a)) }
		switch {
		case len(body.AlbumIds) > 0:
			for _, aid := range body.AlbumIds {
				if al := f.albums[aid]; al != nil {
					for id := range al.members {
						add(f.assets[id])
					}
				}
			}
		case body.ID != "":
			if a := f.assets[body.ID]; a != nil && a.owner == who(r) && !a.trashed && (!body.IsNotInAlbum || !f.inAlbum(a.id)) {
				add(a)
			}
		}
		j(w, 200, map[string]any{"albums": map[string]any{"count": 0, "facets": []any{}, "items": []any{}, "total": 0}, "assets": map[string]any{"count": len(items), "facets": []any{}, "items": items, "total": len(items)}})
	})
	mux.HandleFunc("POST /api/trash/restore/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Ids []string }
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, id := range body.Ids {
			if a := f.assets[id]; a != nil && a.owner == who(r) {
				a.trashed = false
			}
		}
		j(w, 200, map[string]any{"count": len(body.Ids)})
	})
	mux.HandleFunc("DELETE /api/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Ids   []string
			Force bool
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, id := range body.Ids {
			if a := f.assets[id]; a != nil && a.owner == who(r) {
				a.trashed = !body.Force
				f.deleted = append(f.deleted, id)
			}
		}
		w.WriteHeader(204)
	})
	return mux
}

func libSetup(t *testing.T) (*hub.Hub, *libFake, *immich.API) {
	t.Helper()
	f := newLibFake()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	sess, err := immich.New(srv.URL, immich.Auth{Cookie: "sess=bob"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hub.LibrarySettle, hub.LibraryPoll, hub.LibraryWait = time.Millisecond, time.Millisecond, time.Second
	return &hub.Hub{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, f, sess
}

func (f *libFake) bobCopies() []*libAsset {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*libAsset
	for _, a := range f.assets {
		if a.owner == "bob" && !strings.HasPrefix(a.name, "own") {
			out = append(out, a)
		}
	}
	return out
}

func TestAddToLibraryCopiesASharedPhotoIntoTheViewersOwnLibraryTaggedWithWhoSharedIt(t *testing.T) {
	h, f, sess := libSetup(t)
	a := f.shared("IMG_1.jpg", "bytes-of-a-photo")
	res, err := h.AddToLibrary(context.Background(), sess, bob, []string{a.id}, hub.LibraryDest{})
	if err != nil || res.Added != 1 || res.Already != 0 || res.Skipped != 0 || len(res.Copies) != 1 {
		t.Fatalf("result: %+v, err %v", res, err)
	}
	cp := f.assets[res.Copies[0].String()]
	if cp == nil || cp.owner != "bob" || string(cp.data) != "bytes-of-a-photo" {
		t.Fatalf("the copy must be bob's own, with the same bytes: %+v", cp)
	}
	if len(cp.tags) != 1 || cp.tags[0] != "from/Ann" {
		t.Errorf("tags of the copy: %v, want from/Ann", cp.tags)
	}
	if len(f.uploads) != 1 || !f.uploads[0].sidecar || f.uploads[0].person != "bob" {
		t.Errorf("the details travel as a sidecar in the upload: %+v", f.uploads)
	}
	if f.assets[a.id].owner != "ann" || len(f.assets[a.id].tags) != 0 {
		t.Error("the original is left exactly as it was")
	}
}

func TestAddToLibraryLeavesTheOwnPhotosAloneAndCountsARepeatAsAlreadyThere(t *testing.T) {
	h, f, sess := libSetup(t)
	mine := f.own("bob", "own.jpg", "mine", time.Hour)
	a := f.shared("IMG_2.jpg", "shared-bytes")
	ctx := context.Background()
	res, err := h.AddToLibrary(ctx, sess, bob, []string{mine.id, a.id}, hub.LibraryDest{Kind: hub.LibraryPlain})
	if err != nil || res.Added != 1 || res.Skipped != 1 {
		t.Fatalf("own photo is skipped, shared copied: %+v %v", res, err)
	}
	if got := f.bobCopies(); len(got) != 1 || len(got[0].tags) != 0 {
		t.Fatalf("plain destination adds nothing: %+v", got)
	}
	again, err := h.AddToLibrary(ctx, sess, bob, []string{a.id}, hub.LibraryDest{Kind: hub.LibraryPlain})
	if err != nil || again.Added != 0 || again.Already != 1 || len(again.Copies) != 0 {
		t.Fatalf("a second press is harmless: %+v %v", again, err)
	}
	if n := len(f.bobCopies()); n != 1 {
		t.Errorf("a repeat must not make a second copy, there are %d", n)
	}
}

func TestAddToLibraryRefusesWhatThePersonCannotSeeBeforeCopyingAnything(t *testing.T) {
	h, f, sess := libSetup(t)
	ok := f.shared("a.jpg", "one")
	hidden := f.own("ann", "private.jpg", "two", time.Hour) // ann's, not shared with bob
	_, err := h.AddToLibrary(context.Background(), sess, bob, []string{ok.id, hidden.id}, hub.LibraryDest{})
	if !errors.Is(err, hub.ErrNotVisible) || !hub.IsUserError(err) {
		t.Fatalf("err = %v, want ErrNotVisible shown as it is", err)
	}
	if len(f.uploads) != 0 {
		t.Error("nothing is copied when one photo cannot be seen")
	}
	if _, err := h.AddToLibrary(context.Background(), sess, bob, nil, hub.LibraryDest{}); !errors.Is(err, hub.ErrNoPhotosChosen) {
		t.Errorf("no photos: %v", err)
	}
	many := make([]string, hub.HandOverMax+1)
	for i := range many {
		many[i] = uuid.NewString()
	}
	if _, err := h.AddToLibrary(context.Background(), sess, bob, many, hub.LibraryDest{}); !errors.Is(err, hub.ErrTooManyChosen) {
		t.Errorf("too many: %v", err)
	}
	if _, err := h.AddToLibrary(context.Background(), sess, bob, []string{ok.id}, hub.LibraryDest{Kind: "elsewhere"}); !errors.Is(err, hub.ErrBadLibrary) {
		t.Errorf("bad destination: %v", err)
	}
	if _, err := h.AddToLibrary(context.Background(), sess, bob, []string{ok.id}, hub.LibraryDest{Kind: hub.LibraryAlbum}); !errors.Is(err, hub.ErrNoLibAlbum) {
		t.Errorf("album without a name: %v", err)
	}
}

func TestAddToLibraryCanFileTheCopiesInAnAlbumThatIsMadeWhenMissing(t *testing.T) {
	h, f, sess := libSetup(t)
	a, b := f.shared("a.jpg", "aaa"), f.shared("b.jpg", "bbb")
	res, err := h.AddToLibrary(context.Background(), sess, bob, []string{a.id, b.id}, hub.LibraryDest{Kind: hub.LibraryAlbum, Album: "From Ann"})
	if err != nil || res.Added != 2 || res.Note != "" {
		t.Fatalf("%+v %v", res, err)
	}
	if len(f.albums) != 1 {
		t.Fatalf("one album is made, not one per photo: %d", len(f.albums))
	}
	for _, al := range f.albums {
		if al.name != "From Ann" || al.owner != "bob" || len(al.members) != 2 {
			t.Errorf("album: %+v", al)
		}
	}
	if more, _ := h.AddToLibrary(context.Background(), sess, bob, []string{f.shared("c.jpg", "ccc").id}, hub.LibraryDest{Kind: hub.LibraryAlbum, Album: "From Ann"}); more.Added != 1 || len(f.albums) != 1 {
		t.Errorf("the album that exists is used, not made again: %+v, %d albums", more, len(f.albums))
	}
}

func TestAddToLibraryBringsALivePhotosClipFirstHiddenAndLinksTheStillToIt(t *testing.T) {
	h, f, sess := libSetup(t)
	clip := f.shared("clip.mp4", "motion-bytes")
	clip.typ = "VIDEO"
	still := f.shared("live.jpg", "still-bytes")
	still.live = clip.id
	res, err := h.AddToLibrary(context.Background(), sess, bob, []string{still.id}, hub.LibraryDest{})
	if err != nil || res.Added != 1 || len(res.Clips) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if len(f.uploads) != 2 || f.uploads[0].name != "clip.mp4" || f.uploads[0].fields["visibility"] != "hidden" {
		t.Fatalf("the clip goes first and hidden: %+v", f.uploads)
	}
	cp := f.assets[res.Copies[0].String()]
	if cp.live != res.Clips[0].String() {
		t.Errorf("the copy of the still must point at bob's own clip: %q vs %v", cp.live, res.Clips[0])
	}
	if f.assets[res.Clips[0].String()].owner != "bob" {
		t.Error("the clip copy is bob's")
	}
}

func TestAddToLibraryRestoresACopyThatWasTakenOutAndIsAddedAgain(t *testing.T) {
	h, f, sess := libSetup(t)
	a := f.shared("a.jpg", "same-bytes")
	res, _ := h.AddToLibrary(context.Background(), sess, bob, []string{a.id}, hub.LibraryDest{})
	f.assets[res.Copies[0].String()].trashed = true // Undo, or bob's own delete
	again, err := h.AddToLibrary(context.Background(), sess, bob, []string{a.id}, hub.LibraryDest{Kind: hub.LibraryPlain})
	if err != nil || again.Added != 1 || len(again.Copies) != 1 || again.Copies[0] != res.Copies[0] {
		t.Fatalf("%+v %v", again, err)
	}
	if f.assets[res.Copies[0].String()].trashed {
		t.Error("the copy in the trash is brought back, not left there")
	}
	if n := len(f.bobCopies()); n != 1 {
		t.Errorf("no second copy: %d", n)
	}
}

func TestUndoAddToLibraryTrashesOnlyRecentOwnCopiesNotFiledElsewhere(t *testing.T) {
	h, f, sess := libSetup(t)
	ctx := context.Background()
	clip := f.shared("clip.mp4", "motion")
	clip.typ = "VIDEO"
	still := f.shared("live.jpg", "still")
	still.live = clip.id
	plain := f.shared("p.jpg", "plain")
	res, err := h.AddToLibrary(ctx, sess, bob, []string{still.id, plain.id}, hub.LibraryDest{})
	if err != nil || res.Added != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	ids := []string{res.Copies[0].String(), res.Copies[1].String()}
	n, err := h.UndoAddToLibrary(ctx, sess, bob, ids, "")
	if err != nil || n != 2 {
		t.Fatalf("undo: %d %v", n, err)
	}
	for _, id := range ids {
		if !f.assets[id].trashed {
			t.Errorf("copy %s must be in the trash (Immich's, restorable)", id)
		}
	}
	if !f.assets[res.Clips[0].String()].trashed {
		t.Error("a live photo's clip goes with it")
	}
	if f.assets[still.id].trashed || f.assets[plain.id].trashed {
		t.Error("the originals are never touched")
	}
}

func TestUndoAddToLibraryRefusesWhatIsNotTheirsTooOldOrFiledInAnotherAlbum(t *testing.T) {
	h, f, sess := libSetup(t)
	ctx := context.Background()
	if _, err := h.UndoAddToLibrary(ctx, sess, bob, []string{uuid.NewString()}, ""); !errors.Is(err, hub.ErrNotYourPhotos) {
		t.Errorf("not theirs: %v", err)
	}
	theirs := f.shared("s.jpg", "x")
	f.visible[theirs.id]["bob"] = true
	if _, err := h.UndoAddToLibrary(ctx, sess, bob, []string{theirs.id}, ""); !errors.Is(err, hub.ErrNotYourPhotos) {
		t.Errorf("someone else's photo must never be trashed: %v", err)
	}
	old := f.own("bob", "old.jpg", "old", time.Hour)
	if _, err := h.UndoAddToLibrary(ctx, sess, bob, []string{old.id}, ""); !errors.Is(err, hub.ErrUndoLate) || !hub.IsUserError(err) {
		t.Errorf("older than the window: %v", err)
	}
	if f.assets[old.id].trashed {
		t.Error("an old copy stays")
	}
	fresh := f.own("bob", "fresh.jpg", "fresh", time.Minute)
	f.albums[uuid.NewString()] = &libAlbum{owner: "bob", name: "Mine", members: map[string]bool{fresh.id: true}}
	if _, err := h.UndoAddToLibrary(ctx, sess, bob, []string{fresh.id}, ""); !errors.Is(err, hub.ErrNoCopyKept) {
		t.Errorf("filed by the person in an album of their own: %v", err)
	}
	if n, err := h.UndoAddToLibrary(ctx, sess, bob, []string{fresh.id}, "Mine"); err != nil || n != 1 {
		t.Errorf("an album that is the destination does not protect the copy: %d %v", n, err)
	}
}

func TestTheDefaultPlaceForCopiesIsATagUntilThePersonChoosesAndIsRemembered(t *testing.T) {
	e, ctx := ruleEnv(t)
	if d := e.h.LibraryDefault(ctx, bobID); d.Kind != hub.LibraryTag || d.Album != "" {
		t.Fatalf("before any choice: %+v", d)
	}
	if err := e.h.SetLibraryDefault(ctx, bobID, hub.LibraryDest{Kind: hub.LibraryAlbum, Album: "  From Ann "}); err != nil {
		t.Fatal(err)
	}
	if d := e.h.LibraryDefault(ctx, bobID); d.Kind != hub.LibraryAlbum || d.Album != "From Ann" {
		t.Errorf("remembered, trimmed: %+v", d)
	}
	if err := e.h.SetLibraryDefault(ctx, bobID, hub.LibraryDest{Kind: hub.LibraryPlain, Album: "left over"}); err != nil {
		t.Fatal(err)
	}
	if d := e.h.LibraryDefault(ctx, bobID); d.Kind != hub.LibraryPlain || d.Album != "" {
		t.Errorf("an album name does not stay when the choice is not an album: %+v", d)
	}
	if err := e.h.SetLibraryDefault(ctx, bobID, hub.LibraryDest{Kind: "nowhere"}); !errors.Is(err, hub.ErrBadLibrary) {
		t.Errorf("a choice that does not exist: %v", err)
	}
	if d := e.h.LibraryDefault(ctx, annID); d.Kind != hub.LibraryTag {
		t.Errorf("someone else is not affected: %+v", d)
	}
}

func TestTheKeyForFollowingAlbumsIsMadeOnceWithTheSmallestPermissionsAndNamedForThePerson(t *testing.T) {
	e, ctx := ruleEnv(t)
	api, err := e.h.EnsureLibraryKeyForTest(ctx, e.sess, bob, false)
	if err != nil || api == nil {
		t.Fatalf("%v", err)
	}
	want := []string{"user.read", "album.read", "asset.read", "asset.download", "asset.upload", "tag.read", "tag.create", "tag.asset"}
	if got := e.f.lastPerms; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("permissions %v, want %v: it reads what is shared, adds to the person's own library and tags; it cannot delete or update", got, want)
	}
	if k, ok, err := e.st.LibraryKey(ctx, bobID); err != nil || !ok || k.Key == "" || k.KeyID == uuid.Nil {
		t.Errorf("the key is kept: %+v %v %v", k, ok, err)
	}
	made := e.f.keyUpdates
	n := len(e.f.keyPerms)
	if _, err := e.h.EnsureLibraryKeyForTest(ctx, e.sess, bob, false); err != nil || len(e.f.keyPerms) != n {
		t.Errorf("the second time no key is made: %v, %d keys", err, len(e.f.keyPerms))
	}
	// an album destination adds the two permissions to the key that exists, and makes no new key
	if _, err := e.h.EnsureLibraryKeyForTest(ctx, e.sess, bob, true); err != nil || len(e.f.keyPerms) != n || e.f.keyUpdates == made {
		t.Errorf("an album destination widens the same key: %v, %d keys, updates %d", err, len(e.f.keyPerms), e.f.keyUpdates)
	}
	if _, ok, _ := e.st.LibraryKey(ctx, annID); ok {
		t.Error("another person has no such key")
	}
}

func TestTheKeyForFollowingAlbumsIsDeletedWhenNoRuleUsesItAnyMore(t *testing.T) {
	e, ctx := ruleEnv(t)
	if _, err := e.h.EnsureLibraryKeyForTest(ctx, e.sess, bob, false); err != nil {
		t.Fatal(err)
	}
	rule, err := e.st.AddRule(ctx, store.Rule{OwnerID: bobID, Kind: store.RuleKindLibrary, Dest: "plain", Cond: store.RuleCond{Album: &store.RuleRef{ID: uuid.NewString(), Name: "Trip"}}})
	if err != nil {
		t.Fatal(err)
	}
	e.h.DropLibraryKeyForTest(ctx, e.sess, bob)
	if _, ok, _ := e.st.LibraryKey(ctx, bobID); !ok {
		t.Fatal("a rule still follows an album: the key stays")
	}
	e.st.DeleteRule(ctx, rule.ID)
	e.h.DropLibraryKeyForTest(ctx, e.sess, bob)
	if _, ok, _ := e.st.LibraryKey(ctx, bobID); ok {
		t.Error("the last rule is gone: the key goes")
	}
	if len(e.f.deletedKeys) != 1 {
		t.Errorf("the key is deleted in the person's Immich too: %v", e.f.deletedKeys)
	}
}
