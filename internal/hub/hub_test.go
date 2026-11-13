package hub_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/live"
	"github.com/vfuoglio/immich-handoff/internal/notify"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
	"github.com/vfuoglio/immich-handoff/internal/vault"
)

const ts = "2025-01-02T03:04:05.000Z"

var (
	bobID    = uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000001")
	annID    = uuid.MustParse("cccccccc-0000-0000-0000-000000000001")
	familyID = uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000002")

	people = map[string]struct {
		id          uuid.UUID
		name, email string
	}{"bob": {bobID, "Bob", "bob@x.test"}, "ann": {annID, "Ann", "ann@x.test"}}
)

// fakeImmich is a small Immich: one signed-in person (bob) who can create and delete keys and workflows.
type fakeImmich struct {
	mu            sync.Mutex
	failWorkflows bool
	keys          map[string]string // secret -> key id
	owners        map[string]string // secret -> person
	deletedKeys   []string
	workflows     map[string]string // id -> name
	deletedFlows  []string
	lastPerms     []string                   // permissions of the most recent key created
	keyPerms      map[string][]string        // key id -> permissions
	keyUpdates    int                        // how many times a key's permissions were changed
	albums        []map[string]any           // bob's own albums
	uploads       []fakeUpload               // files stored through an upload
	tagged        []string                   // asset ids that were tagged
	tagNames      []string                   // tags that were made
	albumAdds     []string                   // "album name: asset id" for each photo put in an album
	tagIDs        map[string]string          // tag id -> name
	assetTags     map[string][]string        // asset id -> names of its tags
	trashed       map[string]bool            // asset ids moved to the trash
	links         map[string]map[string]any  // shared links made, by id
	assetName     map[string]string          // asset id -> the name it was stored under
	created       map[string]string          // asset id -> its capture time (RFC 3339), when a test sets one
	thumbs        map[string][]byte          // name prefix -> the picture the thumbnail of such a photo is
	members       map[string]map[string]bool // album id -> asset ids in it
	removedLinks  []string
	videos        map[string]bool // asset ids that are videos (the rest are photos)
	archived      map[string]bool // asset ids the owner archived
}

type fakeUpload struct {
	person, name string
	size         int
}

// person is who a request is from: a session cookie ("sess=bob") or one of the keys created for them.
// The caller holds f.mu.
func (f *fakeImmich) person(r *http.Request) string {
	if c := r.Header.Get("Cookie"); strings.HasPrefix(c, "sess=") {
		return strings.TrimPrefix(c, "sess=")
	}
	key := r.Header.Get("x-api-key")
	if _, alive := f.keys[key]; !alive {
		return "" // a deleted key is no longer accepted
	}
	return f.owners[key]
}

func (f *fakeImmich) handler() http.Handler {
	j := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/users/me", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		who := f.person(r)
		if p, ok := people[who]; ok {
			j(w, 200, map[string]any{"id": p.id, "name": p.name, "email": p.email})
			return
		}
		http.Error(w, "no", 401)
	})
	mux.HandleFunc("GET /api/api-keys/me", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		_, alive := f.keys[r.Header.Get("x-api-key")]
		f.mu.Unlock()
		if !alive {
			http.Error(w, "no", 401)
			return
		}
		j(w, 200, map[string]any{"id": uuid.NewString(), "name": "k", "permissions": []string{"asset.read"}, "createdAt": ts, "updatedAt": ts})
	})
	mux.HandleFunc("POST /api/api-keys", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body struct {
			Name        string   `json:"name"`
			Permissions []string `json:"permissions"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.lastPerms = body.Permissions
		id, secret := uuid.NewString(), "secret-"+uuid.NewString()
		if f.keyPerms == nil {
			f.keyPerms = map[string][]string{}
		}
		f.keyPerms[id] = body.Permissions
		f.keys[secret] = id
		f.owners[secret] = f.person(r)
		j(w, 201, map[string]any{"id": id, "name": body.Name, "secret": secret, "permissions": body.Permissions, "createdAt": ts, "updatedAt": ts,
			"apiKey": map[string]any{"id": id, "name": body.Name, "permissions": body.Permissions, "createdAt": ts, "updatedAt": ts}})
	})
	mux.HandleFunc("GET /api/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := r.PathValue("id")
		perms, ok := f.keyPerms[id]
		if !ok {
			http.Error(w, "no", 404)
			return
		}
		j(w, 200, map[string]any{"id": id, "name": "k", "permissions": perms, "createdAt": ts, "updatedAt": ts})
	})
	mux.HandleFunc("PUT /api/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body struct {
			Permissions []string `json:"permissions"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		id := r.PathValue("id")
		if _, ok := f.keyPerms[id]; !ok {
			http.Error(w, "no", 404)
			return
		}
		f.keyPerms[id] = body.Permissions
		f.keyUpdates++
		j(w, 200, map[string]any{"id": id, "name": "k", "permissions": body.Permissions, "createdAt": ts, "updatedAt": ts})
	})
	mux.HandleFunc("GET /api/albums", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.person(r) != "bob" || r.URL.Query().Get("isOwned") != "true" {
			j(w, 200, []any{})
			return
		}
		out := f.albums
		if out == nil {
			out = []map[string]any{}
		}
		j(w, 200, out)
	})
	mux.HandleFunc("DELETE /api/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.deletedKeys = append(f.deletedKeys, r.PathValue("id"))
		for s, id := range f.keys {
			if id == r.PathValue("id") {
				delete(f.keys, s)
			}
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /api/assets", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		who := f.person(r)
		f.mu.Unlock()
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "no", 400)
			return
		}
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			if part.FormName() == "assetData" {
				n, err := io.Copy(io.Discard, part)
				if err != nil {
					http.Error(w, "broken", 400)
					return
				}
				f.mu.Lock()
				f.uploads = append(f.uploads, fakeUpload{who, part.FileName(), int(n)})
				f.mu.Unlock()
				status, code := "created", 201
				if strings.HasPrefix(part.FileName(), "dupe") {
					status, code = "duplicate", 200
				}
				id := uuid.NewString()
				f.mu.Lock()
				if f.assetTags == nil {
					f.assetTags = map[string][]string{}
				}
				f.assetTags[id] = nil
				if f.assetName == nil {
					f.assetName = map[string]string{}
				}
				f.assetName[id] = part.FileName()
				f.mu.Unlock()
				j(w, code, map[string]any{"id": id, "status": status})
				return
			}
		}
		http.Error(w, "no file", 400)
	})
	mux.HandleFunc("GET /api/users", func(w http.ResponseWriter, r *http.Request) {
		out := []any{}
		for _, p := range people {
			out = append(out, map[string]any{"id": p.id, "name": p.name, "email": p.email, "avatarColor": "primary", "profileChangedAt": ts, "profileImagePath": ""})
		}
		j(w, 200, out)
	})
	mux.HandleFunc("POST /api/albums", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AlbumName string `json:"albumName"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		id := uuid.NewString()
		f.albums = append(f.albums, map[string]any{"id": id, "albumName": body.AlbumName, "assetCount": 0})
		f.mu.Unlock()
		j(w, 201, map[string]any{"id": id, "albumName": body.AlbumName, "assetCount": 0})
	})
	mux.HandleFunc("DELETE /api/albums/{id}/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Ids []string }
		json.NewDecoder(r.Body).Decode(&body)
		out := []any{}
		f.mu.Lock()
		for _, id := range body.Ids {
			delete(f.members[r.PathValue("id")], id)
			out = append(out, map[string]any{"id": id, "success": true})
		}
		f.mu.Unlock()
		j(w, 200, out)
	})
	mux.HandleFunc("GET /api/assets/{id}/thumbnail", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		name, ok := f.assetName[r.PathValue("id")]
		var data []byte
		for prefix, d := range f.thumbs {
			if strings.HasPrefix(name, prefix) {
				data = d
			}
		}
		f.mu.Unlock()
		if !ok || data == nil {
			http.Error(w, "no", 404)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(data)
	})
	mux.HandleFunc("GET /api/assets/{id}/original", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		name, ok := f.assetName[r.PathValue("id")]
		var data []byte
		for prefix, d := range f.thumbs {
			if strings.HasPrefix(name, prefix) {
				data = d
			}
		}
		f.mu.Unlock()
		if !ok || data == nil {
			http.Error(w, "no", 404)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Write(data)
	})
	mux.HandleFunc("PUT /api/albums/{id}/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Ids []string }
		json.NewDecoder(r.Body).Decode(&body)
		out := []any{}
		f.mu.Lock()
		name := r.PathValue("id")
		for _, a := range f.albums {
			if a["id"] == r.PathValue("id") {
				name, _ = a["albumName"].(string)
			}
		}
		for _, id := range body.Ids {
			f.albumAdds = append(f.albumAdds, name+": "+id)
			if f.members == nil {
				f.members = map[string]map[string]bool{}
			}
			if f.members[r.PathValue("id")] == nil {
				f.members[r.PathValue("id")] = map[string]bool{}
			}
			f.members[r.PathValue("id")][id] = true
			out = append(out, map[string]any{"id": id, "success": true})
		}
		f.mu.Unlock()
		j(w, 200, out)
	})
	mux.HandleFunc("PUT /api/tags", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Tags []string }
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.tagNames = append(f.tagNames, body.Tags...)
		if f.tagIDs == nil {
			f.tagIDs = map[string]string{}
		}
		out := []any{}
		for _, n := range body.Tags {
			id := uuid.NewString()
			f.tagIDs[id] = n
			out = append(out, map[string]any{"id": id, "name": n, "value": n, "createdAt": ts, "updatedAt": ts})
		}
		f.mu.Unlock()
		j(w, 200, out)
	})
	mux.HandleFunc("PUT /api/tags/{id}/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Ids []string }
		json.NewDecoder(r.Body).Decode(&body)
		out := []any{}
		f.mu.Lock()
		for _, id := range body.Ids {
			f.tagged = append(f.tagged, id)
			if f.assetTags == nil {
				f.assetTags = map[string][]string{}
			}
			f.assetTags[id] = append(f.assetTags[id], f.tagIDs[r.PathValue("id")])
			out = append(out, map[string]any{"id": id, "success": true})
		}
		f.mu.Unlock()
		j(w, 200, out)
	})
	mux.HandleFunc("GET /api/assets/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		names, ok := f.assetTags[r.PathValue("id")]
		if !ok {
			http.Error(w, "no", 404)
			return
		}
		tags := []any{}
		for _, n := range names {
			tags = append(tags, map[string]any{"id": uuid.NewString(), "name": n, "value": n, "createdAt": ts, "updatedAt": ts})
		}
		name := f.assetName[r.PathValue("id")]
		if name == "" {
			name = "x.jpg"
		}
		created := ts
		if c := f.created[r.PathValue("id")]; c != "" {
			created = c
		}
		j(w, 200, map[string]any{"id": r.PathValue("id"), "isTrashed": f.trashed[r.PathValue("id")], "tags": tags, "originalFileName": name, "fileCreatedAt": created, "fileModifiedAt": created})
	})
	mux.HandleFunc("DELETE /api/assets", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Ids   []string
			Force bool
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		if f.trashed == nil {
			f.trashed = map[string]bool{}
		}
		for _, id := range body.Ids {
			f.trashed[id] = !body.Force
		}
		f.mu.Unlock()
		w.WriteHeader(204)
	})
	tag := map[string]any{"id": "33333333-3333-3333-3333-333333333333", "name": "share", "value": "share", "createdAt": ts, "updatedAt": ts}
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []any{tag}
		for id, n := range f.tagIDs {
			out = append(out, map[string]any{"id": id, "name": n, "value": n, "createdAt": ts, "updatedAt": ts})
		}
		j(w, 200, out)
	})
	mux.HandleFunc("POST /api/search/metadata", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			TagIds   []string `json:"tagIds"`
			AlbumIds []string `json:"albumIds"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		items := []any{}
		add := func(id string) {
			created := ts
			if c := f.created[id]; c != "" {
				created = c
			}
			name := f.assetName[id]
			if name == "" {
				name = "x.jpg"
			}
			kind := "IMAGE"
			if f.videos[id] {
				kind = "VIDEO"
			}
			items = append(items, map[string]any{"id": id, "originalFileName": name, "fileCreatedAt": created, "fileModifiedAt": created, "type": kind, "isTrashed": f.trashed[id], "isArchived": f.archived[id]})
		}
		if len(body.AlbumIds) > 0 {
			for _, aid := range body.AlbumIds {
				for id := range f.members[aid] {
					add(id)
				}
			}
		} else {
			for id, names := range f.assetTags {
				if f.trashed[id] {
					continue
				}
				for _, tid := range body.TagIds {
					for _, n := range names {
						if f.tagIDs[tid] == n {
							add(id)
							goto next
						}
					}
				}
			next:
			}
		}
		j(w, 200, map[string]any{"albums": map[string]any{"count": 0, "facets": []any{}, "items": []any{}, "total": 0}, "assets": map[string]any{"count": len(items), "facets": []any{}, "items": items, "total": len(items)}})
	})
	mux.HandleFunc("POST /api/shared-links", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		id := uuid.NewString()
		f.mu.Lock()
		if f.links == nil {
			f.links = map[string]map[string]any{}
		}
		body["key"] = "key-" + id[:8]
		f.links[id] = body
		f.mu.Unlock()
		j(w, 201, map[string]any{"id": id, "key": "key-" + id[:8], "type": body["type"], "allowDownload": true, "allowUpload": false, "assets": []any{}, "createdAt": ts, "description": body["description"], "password": body["password"], "showMetadata": false, "slug": nil, "userId": bobID.String(), "expiresAt": body["expiresAt"]})
	})
	mux.HandleFunc("DELETE /api/shared-links/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		delete(f.links, r.PathValue("id"))
		f.removedLinks = append(f.removedLinks, r.PathValue("id"))
		f.mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /api/workflows", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []any{}
		for id, name := range f.workflows {
			if n := r.URL.Query().Get("name"); n == "" || n == name {
				out = append(out, map[string]any{"id": id, "name": name, "enabled": true, "trigger": "AssetTagged", "steps": []any{}, "createdAt": ts, "updatedAt": ts, "description": "", "logging": true})
			}
		}
		j(w, 200, out)
	})
	mux.HandleFunc("POST /api/workflows", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failWorkflows {
			http.Error(w, `{"message":"Unknown method"}`, 400)
			return
		}
		var body struct{ Name string }
		json.NewDecoder(r.Body).Decode(&body)
		id := uuid.NewString()
		f.workflows[id] = body.Name
		j(w, 201, map[string]any{"id": id, "name": body.Name, "enabled": true, "trigger": "AssetTagged", "steps": []any{}, "createdAt": ts, "updatedAt": ts, "description": "", "logging": true})
	})
	mux.HandleFunc("DELETE /api/workflows/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.deletedFlows = append(f.deletedFlows, r.PathValue("id"))
		delete(f.workflows, r.PathValue("id"))
		w.WriteHeader(204)
	})
	return mux
}

type env struct {
	h     *hub.Hub
	f     *fakeImmich
	sess  *immich.API
	ann   *immich.API // the owner of services created in the tests
	woken []string
	st    *store.Store
}

func setup(t *testing.T, allowJoin bool) *env {
	t.Helper()
	f := &fakeImmich{keys: map[string]string{}, owners: map[string]string{}, workflows: map[string]string{}}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	connect := func(key string) (*immich.API, error) { return immich.New(srv.URL, immich.Auth{APIKey: key}, nil) }
	sess, _ := immich.New(srv.URL, immich.Auth{Cookie: "sess=bob"}, nil)
	annSess, _ := immich.New(srv.URL, immich.Auth{Cookie: "sess=ann"}, nil)
	family, _ := connect("k-family")

	v, _ := vault.New("a-test-store-key-0123456789")
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	svc := &syncer.Service{Name: "family", Tag: "share", InboxTag: "inbox", RefusedTag: "share-duplicate", ExpiryDays: 30, AllowJoin: allowJoin,
		Receivers: []*syncer.Account{{Name: "family", API: family, UserID: familyID}}}
	e := &env{f: f, sess: sess, ann: annSess, st: st}
	e.h = &hub.Hub{Static: []*syncer.Service{svc}, Store: st, Connect: connect, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Options: hub.Options{InboxTag: "inbox", RefusedTag: "share-duplicate", SentTag: "handed-over", ExpiryDays: 30},
		Hook:    syncer.Webhook{URL: "http://app/webhook/immich", Header: "X-Webhook-Secret", Secret: "s"},
		Wake:    func(reason string) { e.woken = append(e.woken, reason) }}
	if err := e.h.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e
}

var (
	bob = immich.User{ID: bobID, Name: "Bob", Email: "bob@x.test"}
	ann = immich.User{ID: annID, Name: "Ann", Email: "ann@x.test"}
)

func senders(e *env) int { return len(e.h.Services()[0].Senders) }

func TestJoinCreatesKeyWorkflowAndMembership(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	if j := e.h.Joinable(bobID); len(j) != 1 {
		t.Fatalf("bob should be offered the service: %v", j)
	}
	if err := e.h.Join(ctx, e.sess, bob, "family", "", ""); err != nil {
		t.Fatal(err)
	}
	if len(e.f.keys) != 1 || len(e.f.deletedKeys) != 0 {
		t.Errorf("one live key expected: keys=%d deleted=%v", len(e.f.keys), e.f.deletedKeys)
	}
	if len(e.f.workflows) != 1 {
		t.Errorf("workflow expected: %v", e.f.workflows)
	}
	if senders(e) != 1 || e.h.Services()[0].Senders[0].UserID != bobID {
		t.Errorf("bob must now be a sender")
	}
	links, _ := e.st.Links(ctx)
	if len(links) != 1 || !strings.HasPrefix(links[0].Key, "secret-") {
		t.Errorf("link not stored: %+v", links)
	}
	if len(e.woken) != 1 {
		t.Errorf("a pass must be requested after joining: %v", e.woken)
	}
	if j := e.h.Joinable(bobID); len(j) != 0 {
		t.Errorf("bob must no longer be offered the service")
	}
	if err := e.h.Join(ctx, e.sess, bob, "family", "", ""); !errors.Is(err, hub.ErrAlready) {
		t.Errorf("joining twice: %v", err)
	}
}

func TestJoinKeepsMembershipWhenTheWorkflowIsNotReadyYet(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	retried := make(chan struct{}, 1)
	e.h.Retry = func() { retried <- struct{}{} }
	e.f.failWorkflows = true // Immich has not finished loading its plugins
	if err := e.h.Join(ctx, e.sess, bob, "family", "", ""); err != nil {
		t.Fatalf("joining must still succeed: %v", err)
	}
	if len(e.f.keys) != 1 || len(e.f.deletedKeys) != 0 || senders(e) != 1 {
		t.Errorf("key and membership must be kept: keys=%d deleted=%v senders=%d", len(e.f.keys), e.f.deletedKeys, senders(e))
	}
	select {
	case <-retried:
	case <-time.After(2 * time.Second):
		t.Error("the workflow setup must be retried in the background")
	}
}

func TestJoinRollsBackWhenTheNewKeyDoesNotWork(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	other := immich.User{ID: uuid.New(), Name: "Someone else", Email: "o@x.test"} // the new key belongs to bob, not to this user
	if err := e.h.Join(ctx, e.sess, other, "family", "", ""); err == nil {
		t.Fatal("expected an error")
	}
	if len(e.f.keys) != 0 || len(e.f.deletedKeys) != 1 {
		t.Errorf("the key created for the failed join must be deleted again: keys=%d deleted=%v", len(e.f.keys), e.f.deletedKeys)
	}
	if links, _ := e.st.Links(ctx); len(links) != 0 || senders(e) != 0 {
		t.Errorf("no membership may remain")
	}
}

func TestJoinRefusals(t *testing.T) {
	ctx := context.Background()
	if err := setup(t, false).h.Join(ctx, setup(t, false).sess, bob, "family", "", ""); !errors.Is(err, hub.ErrNotJoinable) {
		t.Errorf("closed service: %v", err)
	}
	e := setup(t, true)
	if err := e.h.Join(ctx, e.sess, bob, "nope", "", ""); !errors.Is(err, hub.ErrNotJoinable) {
		t.Errorf("unknown service: %v", err)
	}
	familyUser := immich.User{ID: familyID, Name: "Family", Email: "f@x.test"}
	if err := e.h.Join(ctx, e.sess, familyUser, "family", "", ""); !errors.Is(err, hub.ErrAlready) {
		t.Errorf("a receiver must not also join as sender: %v", err)
	}
	if len(e.f.keys) != 0 {
		t.Errorf("a refused join must not create a key")
	}
}

func TestLeaveRemovesWorkflowKeyAndMembership(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	if err := e.h.Leave(ctx, e.sess, bob, "family"); !errors.Is(err, hub.ErrNotJoined) {
		t.Errorf("leaving without joining: %v", err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "family", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Leave(ctx, e.sess, bob, "family"); err != nil {
		t.Fatal(err)
	}
	if len(e.f.workflows) != 0 || len(e.f.deletedFlows) != 1 {
		t.Errorf("workflow must be deleted: %v", e.f.workflows)
	}
	if len(e.f.keys) != 0 || len(e.f.deletedKeys) != 1 {
		t.Errorf("key must be deleted: keys=%d deleted=%v", len(e.f.keys), e.f.deletedKeys)
	}
	if links, _ := e.st.Links(ctx); len(links) != 0 || senders(e) != 0 {
		t.Errorf("membership must be gone")
	}
	if j := e.h.Joinable(bobID); len(j) != 1 {
		t.Errorf("bob can join again after leaving")
	}
}

func TestReloadKeepsMembersAcrossARestart(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	if err := e.h.Join(ctx, e.sess, bob, "family", "", ""); err != nil {
		t.Fatal(err)
	}
	fresh := &hub.Hub{Static: e.h.Static, Store: e.st, Connect: e.h.Connect, Log: e.h.Log}
	if err := fresh.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fresh.Services()[0].Senders; len(got) != 1 || got[0].UserID != bobID {
		t.Errorf("members must be restored from the store: %+v", got)
	}
}

func service(e *env, name string) *syncer.Service {
	for _, s := range e.h.Services() {
		if s.Label() == name {
			return s
		}
	}
	return nil
}

func TestOwnerCreatesAServiceAndReceiverKeyStaysHidden(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "holiday", Tag: "hol", Policy: "approval", Visibility: ""}); err != nil {
		t.Fatal(err)
	}
	svc := service(e, "holiday")
	if svc == nil || svc.OwnerID != annID || !svc.NeedsApproval || svc.AllowJoin || len(svc.Receivers) != 1 || svc.Receivers[0].UserID != annID {
		t.Fatalf("service: %+v", svc)
	}
	if len(e.f.keys) != 1 {
		t.Errorf("one receiver key expected, got %d", len(e.f.keys))
	}
	owned, _ := e.h.Owned(ctx, annID)
	if len(owned) != 1 || owned[0].Service.Label() != "holiday" {
		t.Errorf("owner view: %+v", owned)
	}
	for _, bad := range [][2]string{{"x", "t"}, {"ok name", "a/b"}, {"default", "t"}, {"family", "t"}, {"holiday", "t"}} {
		if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: bad[0], Tag: bad[1], Policy: "open", Visibility: ""}); err == nil {
			t.Errorf("service %q with tag %q must be refused", bad[0], bad[1])
		}
	}
	if len(e.f.keys) != 1 {
		t.Errorf("refused services must not leave keys behind: %d", len(e.f.keys))
	}
}

func TestApprovalFlowBeforeASenderCanActivate(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "holiday", Tag: "hol", Policy: "approval", Visibility: ""}); err != nil {
		t.Fatal(err)
	}
	if offers, _ := e.h.Offers(ctx, bobID); len(offers) != 1 || offers[0].State != "request" {
		t.Fatalf("bob should be able to ask: %+v", offers)
	}
	if err := e.h.Join(ctx, e.sess, bob, "holiday", "", ""); !errors.Is(err, hub.ErrNeedsApproval) {
		t.Fatalf("joining without approval: %v", err)
	}
	if len(e.f.keys) != 1 {
		t.Fatalf("no sender key may be made before approval (keys=%d)", len(e.f.keys))
	}
	if err := e.h.Ask(ctx, bob, "holiday", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Ask(ctx, bob, "holiday", ""); err != nil {
		t.Errorf("asking twice is harmless: %v", err)
	}
	owned, _ := e.h.Owned(ctx, annID)
	if len(owned[0].Requests) != 1 {
		t.Fatalf("owner must see one pending request: %+v", owned[0].Requests)
	}
	if err := e.h.Decide(ctx, bob, "holiday", owned[0].Requests[0].ID, true); !errors.Is(err, hub.ErrNotOwner) {
		t.Errorf("a non-owner must not decide: %v", err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "holiday", "", ""); !errors.Is(err, hub.ErrNeedsApproval) {
		t.Errorf("still pending: %v", err)
	}
	if err := e.h.Decide(ctx, ann, "holiday", owned[0].Requests[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if offers, _ := e.h.Offers(ctx, bobID); offers[0].State != "approved" {
		t.Errorf("bob should be able to activate: %+v", offers)
	}
	if err := e.h.Join(ctx, e.sess, bob, "holiday", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := service(e, "holiday").Senders; len(got) != 1 || got[0].UserID != bobID {
		t.Errorf("bob must be a sender now")
	}
	if left, _ := e.st.Requests(ctx); len(left) != 0 {
		t.Errorf("the used approval must be gone")
	}
}

func TestDeclinedRequestCannotActivate(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "holiday", Tag: "hol", Policy: "approval", Visibility: ""})
	e.h.Ask(ctx, bob, "holiday", "")
	owned, _ := e.h.Owned(ctx, annID)
	if err := e.h.Decide(ctx, ann, "holiday", owned[0].Requests[0].ID, false); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "holiday", "", ""); !errors.Is(err, hub.ErrNeedsApproval) {
		t.Errorf("declined: %v", err)
	}
	if offers, _ := e.h.Offers(ctx, bobID); offers[0].State != "declined" {
		t.Errorf("state: %+v", offers)
	}
}

func TestOpenServiceNeedsNoApprovalAndSameTagIsRefused(t *testing.T) {
	e, ctx := setup(t, true), context.Background() // the static "family" service is open and uses tag "share"
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "everyone", Tag: "SHARE", Policy: "open", Visibility: ""}); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "family", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "everyone", "", ""); !errors.Is(err, hub.ErrTagInUse) {
		t.Errorf("bob already sends with the tag 'share' to another service: %v", err)
	}
}

func TestDeletingAServiceLeavesMembersAbleToCleanUp(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "party", Tag: "party", Policy: "open", Visibility: ""})
	if err := e.h.Join(ctx, e.sess, bob, "party", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.h.DeleteService(ctx, e.sess, bob, "party"); !errors.Is(err, hub.ErrNotOwner) {
		t.Errorf("only the owner may delete: %v", err)
	}
	if err := e.h.DeleteService(ctx, e.ann, ann, "party"); err != nil {
		t.Fatal(err)
	}
	if service(e, "party") != nil {
		t.Errorf("the service must be gone")
	}
	if len(e.f.deletedKeys) != 1 {
		t.Errorf("the receiver key must be deleted: %v", e.f.deletedKeys)
	}
	if o, _ := e.h.Orphaned(ctx, bobID); len(o) != 1 || o[0] != "party" {
		t.Fatalf("bob must be told the service is gone: %v", o)
	}
	if err := e.h.Leave(ctx, e.sess, bob, "party"); err != nil {
		t.Fatalf("bob can still clean up: %v", err)
	}
	if len(e.f.keys) != 0 {
		t.Errorf("every key must be deleted after cleaning up, %d left", len(e.f.keys))
	}
	if o, _ := e.h.Orphaned(ctx, bobID); len(o) != 0 {
		t.Errorf("nothing left to clean up: %v", o)
	}
}

// deleteKeyOf simulates a person deleting their handoff key in Immich.
func (f *fakeImmich) deleteKeyOf(person string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for secret, who := range f.owners {
		if who == person {
			delete(f.keys, secret)
		}
	}
}

func TestDeletedSenderKeyPausesTheLinkAndTellsPeople(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	var told []string
	e.h.Notify = func(m notify.Message) { told = append(told, m.Text) }
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "party", Tag: "party", Policy: "open", Visibility: ""})
	if err := e.h.Join(ctx, e.sess, bob, "party", "", ""); err != nil {
		t.Fatal(err)
	}
	e.h.CheckKeys(ctx)
	if senders := len(service(e, "party").Senders); senders != 1 {
		t.Fatalf("healthy keys must not be touched, senders=%d", senders)
	}

	e.f.deleteKeyOf("bob") // bob deletes his key in Immich (his session still works; only the key is gone)
	e.h.CheckKeys(ctx)
	if got := service(e, "party").Senders; len(got) != 0 {
		t.Errorf("a broken link must stop being used: %d senders left", len(got))
	}
	if b, _ := e.h.BrokenFor(ctx, bobID); len(b) != 1 || b[0].Service != "party" {
		t.Errorf("bob must be told: %+v", b)
	}
	if len(told) != 1 || !strings.Contains(told[0], "Bob") {
		t.Errorf("an admin notification is expected: %v", told)
	}
	for _, who := range []uuid.UUID{bobID, annID} {
		acts, _ := e.h.Activity(ctx, who, 10)
		if len(acts) == 0 || !strings.Contains(en(acts[0].Text), "cut") {
			t.Errorf("both sides see it in the activity (%s): %+v", who, acts)
		}
	}
	members, _ := e.h.MembersOf(ctx, "party")
	if len(members) != 1 || members[0].State != "broken" {
		t.Errorf("the owner sees bob as disconnected: %+v", members)
	}
	// nothing more is attempted, and checking again changes nothing
	e.h.CheckKeys(ctx)
	if len(told) != 1 {
		t.Errorf("a broken link must not be reported over and over: %v", told)
	}
	// bob cleans up and can join again
	if err := e.h.Leave(ctx, e.sess, bob, "party"); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "party", "", ""); err != nil {
		t.Fatalf("rejoining after clean up: %v", err)
	}
	if len(service(e, "party").Senders) != 1 {
		t.Error("bob is a sender again")
	}
}

func TestDeletedReceiverKeyPausesTheServiceUntilTheOwnerReconnects(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "party", Tag: "party", Policy: "open", Visibility: ""})
	e.h.Join(ctx, e.sess, bob, "party", "", "")

	e.f.deleteKeyOf("ann")
	e.h.CheckKeys(ctx)
	svc := service(e, "party")
	if svc.Broken == "" || len(svc.Receivers) != 0 {
		t.Fatalf("the service must be paused with no receiver: %+v", svc)
	}
	if len(svc.Senders) != 1 {
		t.Errorf("senders stay members while the service is paused")
	}
	if offers, _ := e.h.Offers(ctx, annID); len(offers) != 0 {
		t.Errorf("the owner must not be offered their own paused service: %+v", offers)
	}
	if err := e.h.ReconnectService(ctx, e.sess, bob, "party"); !errors.Is(err, hub.ErrNotOwner) {
		t.Errorf("only the owner reconnects: %v", err)
	}
	woken := len(e.woken)
	if err := e.h.ReconnectService(ctx, e.ann, ann, "party"); err != nil {
		t.Fatal(err)
	}
	svc = service(e, "party")
	if svc.Broken != "" || len(svc.Receivers) != 1 || len(svc.Senders) != 1 {
		t.Errorf("after reconnecting the service works again: %+v", svc)
	}
	if len(e.woken) != woken+1 {
		t.Errorf("a pass must run right after reconnecting")
	}
}

func TestRejectedKeyFromTheConfigurationIsLeftToThePass(t *testing.T) {
	e := setup(t, false)
	e.h.KeyRejected(e.h.Static[0].Receivers[0]) // not one of the stored accounts: must not panic or change anything
	if len(e.h.Services()) != 1 {
		t.Error("services must be unchanged")
	}
}

func TestInviteOnlyServiceIsHiddenAndReachedWithTheLink(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "secret club", Tag: "club", Policy: "open", Visibility: "invite"}); err != nil {
		t.Fatal(err)
	}
	svc := service(e, "secret club")
	token := e.h.InviteOf(svc)
	if token == "" || !svc.Unlisted {
		t.Fatalf("an invite-only service needs a link: %+v", svc)
	}
	if offers, _ := e.h.Offers(ctx, bobID); len(offers) != 0 {
		t.Errorf("it must not appear in the directory: %+v", offers)
	}
	if err := e.h.Join(ctx, e.sess, bob, "secret club", "", ""); !errors.Is(err, hub.ErrNotJoinable) {
		t.Errorf("joining by name without the link: %v", err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "secret club", "wrong", ""); !errors.Is(err, hub.ErrNotJoinable) {
		t.Errorf("a wrong link: %v", err)
	}
	if len(e.f.keys) != 1 {
		t.Errorf("a refused join must not create a key, got %d", len(e.f.keys))
	}
	if o, err := e.h.InviteOffer(ctx, bobID, token); err != nil || o.Service.Label() != "secret club" || o.State != "join" {
		t.Fatalf("the link leads to the service: %+v %v", o, err)
	}
	if _, err := e.h.InviteOffer(ctx, bobID, "wrong"); !errors.Is(err, hub.ErrNoInvite) {
		t.Errorf("a wrong link: %v", err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "secret club", token, ""); err != nil {
		t.Fatalf("joining with the link: %v", err)
	}
	if _, err := e.h.InviteOffer(ctx, bobID, token); !errors.Is(err, hub.ErrAlready) {
		t.Errorf("a member following the link again: %v", err)
	}
}

func TestNewInviteRevokesTheOldLinkAndListingRemovesTheNeedForOne(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "club", Tag: "club", Policy: "open", Visibility: "invite"})
	old := e.h.InviteOf(service(e, "club"))
	if err := e.h.NewInvite(ctx, bob, "club"); !errors.Is(err, hub.ErrNotOwner) {
		t.Errorf("only the owner makes links: %v", err)
	}
	if err := e.h.NewInvite(ctx, ann, "club"); err != nil {
		t.Fatal(err)
	}
	fresh := e.h.InviteOf(service(e, "club"))
	if fresh == "" || fresh == old {
		t.Fatalf("a new link is expected: %q vs %q", fresh, old)
	}
	if _, err := e.h.InviteOffer(ctx, bobID, old); !errors.Is(err, hub.ErrNoInvite) {
		t.Errorf("the old link must stop working: %v", err)
	}
	if err := e.h.SetVisibility(ctx, ann, "club", "listed"); err != nil {
		t.Fatal(err)
	}
	if offers, _ := e.h.Offers(ctx, bobID); len(offers) != 1 {
		t.Errorf("a listed service appears in the directory: %+v", offers)
	}
	if err := e.h.Join(ctx, e.sess, bob, "club", "", ""); err != nil {
		t.Errorf("a listed service needs no link: %v", err)
	}
}

func TestInviteWithApprovalNeedsTheLinkOnlyToAsk(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "club", Tag: "club", Policy: "approval", Visibility: "invite"})
	token := e.h.InviteOf(service(e, "club"))
	if err := e.h.Ask(ctx, bob, "club", ""); !errors.Is(err, hub.ErrNotJoinable) {
		t.Errorf("asking without the link: %v", err)
	}
	if err := e.h.Ask(ctx, bob, "club", token); err != nil {
		t.Fatal(err)
	}
	owned, _ := e.h.Owned(ctx, annID)
	if err := e.h.Decide(ctx, ann, "club", owned[0].Requests[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if err := e.h.NewInvite(ctx, ann, "club"); err != nil { // the link changes, the approval stays valid
		t.Fatal(err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "club", "", ""); err != nil {
		t.Errorf("an approved person activates without the link: %v", err)
	}
}

func has(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestTheSenderChoosesWhatHappensToTheirOriginals(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	for _, name := range []string{"first", "second", "third"} {
		if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: name, Tag: name, Policy: "open"}); err != nil {
			t.Fatal(err)
		}
	}
	// no choice made: originals are kept, and the key has no delete permission at all
	if err := e.h.Join(ctx, e.sess, bob, "first", "", ""); err != nil {
		t.Fatal(err)
	}
	if has(e.f.lastPerms, "asset.delete") || !has(e.f.lastPerms, "asset.download") {
		t.Errorf("the default must keep originals, key permissions: %v", e.f.lastPerms)
	}
	if err := e.h.Join(ctx, e.sess, bob, "second", "", "copy"); err != nil {
		t.Fatal(err)
	}
	if has(e.f.lastPerms, "asset.delete") {
		t.Errorf("an explicit copy has no delete permission: %v", e.f.lastPerms)
	}
	// the same person can choose differently for another service
	if err := e.h.Join(ctx, e.sess, bob, "third", "", "move"); err != nil {
		t.Fatal(err)
	}
	if !has(e.f.lastPerms, "asset.delete") {
		t.Errorf("moving needs the delete permission: %v", e.f.lastPerms)
	}
	for name, copies := range map[string]bool{"first": true, "second": true, "third": false} {
		svc := service(e, name)
		if len(svc.Senders) != 1 || svc.CopiesFor(svc.Senders[0]) != copies {
			t.Errorf("%s: the sender's choice must apply to their originals (want copy=%v)", name, copies)
		}
	}
	members, _ := e.h.MembersOf(ctx, "third")
	if len(members) != 1 || members[0].Mode != "move" {
		t.Errorf("the owner sees the sender's choice: %+v", members)
	}
}

func TestTheOwnerHasNoSayOverTheSendersOriginals(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "plain", Tag: "plain", Policy: "open"})
	if svc := service(e, "plain"); svc.Mode != "" {
		t.Errorf("a service created on the page carries no mode of its own: %q", svc.Mode)
	}
}

func TestCreateOptionsAreValidated(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "opts", Tag: "opts", InboxTag: "from-opts", ExpiryDays: 7}); err != nil {
		t.Fatal(err)
	}
	if v := service(e, "opts"); v.InboxTag != "from-opts" || v.ExpiryDays != 7 || v.SentTag != "handed-over" {
		t.Errorf("options: %+v", v)
	}
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "bad1", Tag: "t", InboxTag: "a/b"}); !errors.Is(err, hub.ErrBadTag) {
		t.Errorf("bad inbox tag: %v", err)
	}
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "bad2", Tag: "t", ExpiryDays: 99999}); !errors.Is(err, hub.ErrBadExpiry) {
		t.Errorf("bad expiry: %v", err)
	}
}

func TestEachCreatedServiceGetsItsOwnInboxTag(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "Holiday 2026", Tag: "holiday", Policy: "open"})
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "Photo club", Tag: "club", Policy: "open", InboxTag: "club-inbox"})
	if got := service(e, "Holiday 2026").InboxTag; got != "inbox/Holiday 2026" {
		t.Errorf("default inbox tag = %q", got)
	}
	if got := service(e, "Photo club").InboxTag; got != "club-inbox" {
		t.Errorf("an owner's choice wins: %q", got)
	}
}

func TestAServiceWithAnAlbumGetsAlbumPermissionsAndKeepsTheName(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "filed", Tag: "filed", Policy: "open", Album: "  Holiday 2026 "}); err != nil {
		t.Fatal(err)
	}
	if got := service(e, "filed").Album; got != "Holiday 2026" {
		t.Errorf("album = %q", got)
	}
	has := func(name string) bool {
		for _, p := range e.f.lastPerms {
			if p == name {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"album.read", "album.create", "albumAsset.create"} {
		if !has(want) {
			t.Errorf("a service with an album needs %s on its key: %v", want, e.f.lastPerms)
		}
	}
	for _, never := range []string{"album.update", "album.delete", "albumAsset.delete", "album.share"} {
		if has(never) {
			t.Errorf("the key must not be able to change or delete albums (%s): %v", never, e.f.lastPerms)
		}
	}
	// a replacement key made when reconnecting keeps the album permissions
	if err := e.h.ReconnectService(ctx, e.ann, ann, "filed"); err != nil {
		t.Fatal(err)
	}
	if !has("albumAsset.create") {
		t.Errorf("a reconnected service still files into its album: %v", e.f.lastPerms)
	}
	// a service without one does not ask for them
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "plainer", Tag: "plainer", Policy: "open"}); err != nil {
		t.Fatal(err)
	}
	if service(e, "plainer").Album != "" || has("album.read") || has("albumAsset.create") {
		t.Errorf("no album, so no album permissions: %v", e.f.lastPerms)
	}
}

func TestABadAlbumNameIsRefused(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	for _, name := range []string{strings.Repeat("x", 101), "two\nlines"} {
		err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "bada", Tag: "bada", Album: name})
		if !errors.Is(err, hub.ErrBadAlbum) || !hub.IsUserError(err) {
			t.Errorf("album %q: %v", name, err)
		}
	}
}

func TestLinkingAnAlbumGivesTheKeyAlbumReadAndTheSenderAWatch(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album := uuid.MustParse("dddddddd-0000-4000-8000-000000000001")
	e.f.albums = []map[string]any{{"id": album.String(), "albumName": "To send", "assetCount": 3}}
	if err := e.h.Join(ctx, e.sess, bob, "family", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := e.h.Services()[0].Senders[0].WatchAlbum; got != uuid.Nil {
		t.Fatalf("nothing is linked yet: %v", got)
	}
	before := len(e.f.keyPerms)
	if err := e.h.LinkAlbum(ctx, e.sess, bob, "family", album); err != nil {
		t.Fatal(err)
	}
	if !has(e.woken, "album linked: bob@x.test") {
		t.Errorf("linking an album must start a pass at once, not wait for the timer or for a tagged photo: %v", e.woken)
	}
	snd := e.h.Services()[0].Senders[0]
	if snd.WatchAlbum != album || snd.WatchAlbumName != "To send" {
		t.Errorf("the sender's account must carry the album: %v %q", snd.WatchAlbum, snd.WatchAlbumName)
	}
	links, _ := e.st.Links(ctx)
	if len(links) != 1 || links[0].AlbumID != album || links[0].AlbumName != "To send" {
		t.Errorf("the link must be stored: %+v", links)
	}
	perms := e.f.keyPerms[links[0].KeyID.String()]
	has := func(name string) bool {
		for _, p := range perms {
			if p == name {
				return true
			}
		}
		return false
	}
	if !has("album.read") || !has("tag.asset") || !has("asset.download") {
		t.Errorf("the key keeps what it had and gains album.read: %v", perms)
	}
	if has("album.update") || has("album.delete") || has("albumAsset.delete") || has("album.create") {
		t.Errorf("reading albums is all it needs: %v", perms)
	}
	if len(e.f.keyPerms) != before {
		t.Error("no new key may be made: linking changes the one that exists")
	}
	// linking again changes nothing about the key
	updates := e.f.keyUpdates
	if err := e.h.LinkAlbum(ctx, e.sess, bob, "family", album); err != nil {
		t.Fatal(err)
	}
	if e.f.keyUpdates != updates {
		t.Errorf("the key already has album.read, so it must not be changed again")
	}
	if err := e.h.UnlinkAlbum(ctx, bob, "family"); err != nil {
		t.Fatal(err)
	}
	if e.h.Services()[0].Senders[0].WatchAlbum != uuid.Nil {
		t.Error("unlinking must stop the watch")
	}
	if links, _ := e.st.Links(ctx); links[0].AlbumID != uuid.Nil || links[0].AlbumName != "" {
		t.Errorf("and clear it from the store: %+v", links)
	}
}

func TestOnlyYourOwnAlbumAndOnlyAServiceYouJoinedCanBeLinked(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album := uuid.MustParse("dddddddd-0000-4000-8000-000000000001")
	e.f.albums = []map[string]any{{"id": album.String(), "albumName": "Mine", "assetCount": 1}}
	if err := e.h.LinkAlbum(ctx, e.sess, bob, "family", album); !errors.Is(err, hub.ErrNotJoined) {
		t.Errorf("not a member: %v", err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "family", "", ""); err != nil {
		t.Fatal(err)
	}
	someoneElses := uuid.MustParse("dddddddd-0000-4000-8000-000000000002")
	if err := e.h.LinkAlbum(ctx, e.sess, bob, "family", someoneElses); !errors.Is(err, hub.ErrNoAlbum) || !hub.IsUserError(err) {
		t.Errorf("an album that is not in the person's own list: %v", err)
	}
	if links, _ := e.st.Links(ctx); links[0].AlbumID != uuid.Nil {
		t.Errorf("nothing may be linked after a refusal: %+v", links)
	}
}

func TestAHandOverFromTheAlbumLeavesALineInTheSendersRecentEvents(t *testing.T) {
	e, ctx := setup(t, true), context.Background()
	album := uuid.MustParse("dddddddd-0000-4000-8000-000000000001")
	e.f.albums = []map[string]any{{"id": album.String(), "albumName": "To send", "assetCount": 3}}
	e.h.Join(ctx, e.sess, bob, "family", "", "")
	e.h.LinkAlbum(ctx, e.sess, bob, "family", album)
	snd := e.h.Services()[0].Senders[0]
	e.h.AlbumHandedOver(e.h.Services()[0], snd, 12, 1, 0)
	evs, err := e.st.Events(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if ev.Kind == "album" && strings.Contains(en(ev.Text), "12 photos from the album To send handed over to family") && strings.Contains(en(ev.Text), "1 refused") {
			found = true
		}
	}
	if !found {
		t.Errorf("recent events: %+v", evs)
	}
}

func TestAnAdministratorRemovingAMemberStopsHandOversAndTellsThePerson(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	var told []string
	e.h.Notify = func(m notify.Message) { told = append(told, m.Text) }
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "party", Tag: "party", Policy: "open", Visibility: ""})
	if err := e.h.Join(ctx, e.sess, bob, "party", "", ""); err != nil {
		t.Fatal(err)
	}
	admin := immich.User{ID: uuid.New(), Name: "Root", Email: "root@x.test", IsAdmin: true}
	links, _ := e.st.Links(ctx)
	var id string
	for _, l := range links {
		if l.Role == "sender" {
			id = l.ID
		}
	}
	if err := e.h.AdminRemoveMember(ctx, admin, "nope"); !errors.Is(err, hub.ErrNoSuchEntry) {
		t.Errorf("an unknown member: %v", err)
	}
	if err := e.h.AdminRemoveMember(ctx, admin, id); err != nil {
		t.Fatal(err)
	}
	if got := service(e, "party").Senders; len(got) != 0 {
		t.Errorf("a removed member must stop being used: %d senders", len(got))
	}
	if len(e.f.deletedKeys) != 0 {
		t.Errorf("the app cannot delete another person's key, and must not pretend to: %v", e.f.deletedKeys)
	}
	b, _ := e.h.BrokenFor(ctx, bobID)
	if len(b) != 1 || !strings.Contains(en(b[0].Note), "Root") || !strings.Contains(en(b[0].Note), "still valid") {
		t.Errorf("bob must be told on his page: %+v", b)
	}
	acts, _ := e.h.Activity(ctx, bobID, 10)
	if len(acts) == 0 || !strings.Contains(en(acts[0].Text), "removed from party by an administrator") {
		t.Errorf("bob's recent events: %+v", acts)
	}
	if len(told) != 1 || !strings.Contains(told[0], "Root") {
		t.Errorf("the operator must be told: %v", told)
	}
	// the person can still clean up, which deletes what the app made in their account
	if err := e.h.Leave(ctx, e.sess, bob, "party"); err != nil {
		t.Fatalf("clean up after a removal: %v", err)
	}
	if len(e.f.keys) != 1 { // only the owner's receiver key is left, which only the owner can delete
		t.Errorf("keys left after cleaning up: %d", len(e.f.keys))
	}
}

func TestAnAdministratorRemovingAServiceTellsOwnerAndMembers(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	var told []string
	e.h.Notify = func(m notify.Message) { told = append(told, m.Text) }
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "party", Tag: "party", Policy: "open", Visibility: ""})
	if err := e.h.Join(ctx, e.sess, bob, "party", "", ""); err != nil {
		t.Fatal(err)
	}
	admin := immich.User{ID: uuid.New(), Name: "Root", Email: "root@x.test", IsAdmin: true}
	if err := e.h.AdminRemoveService(ctx, admin, "family"); !errors.Is(err, hub.ErrNotRemovable) {
		t.Errorf("a service from the configuration: %v", err)
	}
	if err := e.h.AdminRemoveService(ctx, admin, "nope"); !errors.Is(err, hub.ErrNoSuchEntry) {
		t.Errorf("an unknown service: %v", err)
	}
	if err := e.h.AdminRemoveService(ctx, admin, "party"); err != nil {
		t.Fatal(err)
	}
	if service(e, "party") != nil {
		t.Error("the service must be gone")
	}
	if len(e.f.deletedKeys) != 0 {
		t.Errorf("the app cannot delete the owner's key: %v", e.f.deletedKeys)
	}
	if o, _ := e.h.Orphaned(ctx, bobID); len(o) != 1 {
		t.Errorf("bob must be told the service is gone: %v", o)
	}
	owner, _ := e.h.Activity(ctx, annID, 10)
	if len(owner) == 0 || !strings.Contains(en(owner[0].Text), "still valid in your Immich account") {
		t.Errorf("the owner must be told their key stays: %+v", owner)
	}
	if len(told) != 1 {
		t.Errorf("the operator must be told: %v", told)
	}
	// both can still clean up
	if err := e.h.Leave(ctx, e.sess, bob, "party"); err != nil {
		t.Fatalf("bob cleans up: %v", err)
	}
	_, left, _ := e.h.Overview(ctx)
	if len(left) != 0 {
		t.Errorf("nothing should be waiting to clean up: %+v", left)
	}
}

func TestTheOverviewListsEveryServiceWithItsOwnerAndMembers(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "party", Tag: "party", Policy: "open", Visibility: ""})
	if err := e.h.Join(ctx, e.sess, bob, "party", "", ""); err != nil {
		t.Fatal(err)
	}
	services, left, err := e.h.Overview(ctx)
	if err != nil || len(left) != 0 || len(services) != 2 {
		t.Fatalf("overview: %d services, %d leftover, %v", len(services), len(left), err)
	}
	var party *hub.AdminService
	for i := range services {
		if services[i].Name == "party" {
			party = &services[i]
		}
	}
	if party == nil || party.FromConfig || party.OwnerEmail != "ann@x.test" || len(party.Members) != 1 || party.Members[0].Email != "bob@x.test" || party.Members[0].LinkID == "" {
		t.Errorf("party: %+v", party)
	}
	if services[0].Name != "family" || !services[0].FromConfig {
		t.Errorf("the configured service must be marked: %+v", services[0])
	}
}

func TestOnlyTheOwnerCanPauseAServiceAndResumingWakesAPass(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "party", Tag: "party", Policy: "open", Visibility: ""})
	if err := e.h.SetServicePaused(ctx, bob, "party", true); !errors.Is(err, hub.ErrNotOwner) {
		t.Errorf("someone else pausing: %v", err)
	}
	if err := e.h.SetServicePaused(ctx, ann, "nope", true); err == nil {
		t.Error("an unknown service must be refused")
	}
	if service(e, "party").Paused {
		t.Fatal("nothing was paused yet")
	}
	woken := len(e.woken)
	if err := e.h.SetServicePaused(ctx, ann, "party", true); err != nil {
		t.Fatal(err)
	}
	if !service(e, "party").Paused || len(e.woken) != woken {
		t.Errorf("paused: %v, woken %d (pausing needs no pass)", service(e, "party").Paused, len(e.woken))
	}
	if err := e.h.SetServicePaused(ctx, ann, "party", false); err != nil {
		t.Fatal(err)
	}
	if service(e, "party").Paused || len(e.woken) != woken+1 || e.woken[len(e.woken)-1] != "service resumed" {
		t.Errorf("resumed: paused %v, woken %v", service(e, "party").Paused, e.woken)
	}
	acts, _ := e.h.Activity(ctx, annID, 5)
	if len(acts) < 2 || !strings.Contains(en(acts[0].Text), "resumed") || !strings.Contains(en(acts[1].Text), "paused") {
		t.Errorf("both must be in the activity log: %+v", acts)
	}
}

func TestASenderPausesAndResumesTheirOwnMembership(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "party", Tag: "party", Policy: "open", Visibility: ""})
	if err := e.h.SetMemberPaused(ctx, bob, "party", true); !errors.Is(err, hub.ErrNotJoined) {
		t.Errorf("pausing without having joined: %v", err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "party", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.h.SetMemberPaused(ctx, bob, "party", true); err != nil {
		t.Fatal(err)
	}
	snd := service(e, "party").Senders
	if len(snd) != 1 || !snd[0].Paused {
		t.Fatalf("bob must be paused: %+v", snd)
	}
	members, _ := e.h.MembersOf(ctx, "party")
	if len(members) != 1 || !members[0].Paused {
		t.Errorf("the owner must see that bob paused: %+v", members)
	}
	woken := len(e.woken)
	if err := e.h.SetMemberPaused(ctx, bob, "party", false); err != nil {
		t.Fatal(err)
	}
	if service(e, "party").Senders[0].Paused || len(e.woken) != woken+1 {
		t.Errorf("resumed: %v woken %v", service(e, "party").Senders[0].Paused, e.woken)
	}
	if err := e.h.SetMemberPaused(ctx, ann, "party", true); !errors.Is(err, hub.ErrNotJoined) {
		t.Errorf("the owner pausing someone else's membership: %v", err)
	}
}

func TestOnlyTheOwnerSetsLimitsAndTheyAreChecked(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "bad", Tag: "bad", LimitPhotos: -1}); !errors.Is(err, hub.ErrBadLimit) {
		t.Errorf("a negative limit at creation: %v", err)
	}
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "party", Tag: "party", Policy: "open", LimitPhotos: 5, LimitMB: 200}); err != nil {
		t.Fatal(err)
	}
	if s := service(e, "party"); s.LimitPhotos != 5 || s.LimitMB != 200 {
		t.Errorf("limits at creation: %d %d", s.LimitPhotos, s.LimitMB)
	}
	if err := e.h.SetLimits(ctx, bob, "party", 1, 1); !errors.Is(err, hub.ErrNotOwner) {
		t.Errorf("someone else setting limits: %v", err)
	}
	for _, bad := range [][2]int{{-1, 0}, {0, -5}, {2_000_000, 0}} {
		if err := e.h.SetLimits(ctx, ann, "party", bad[0], bad[1]); !errors.Is(err, hub.ErrBadLimit) {
			t.Errorf("limits %v: %v", bad, err)
		}
	}
	woken := len(e.woken)
	if err := e.h.SetLimits(ctx, ann, "party", 0, 0); err != nil {
		t.Fatal(err)
	}
	if s := service(e, "party"); s.LimitPhotos != 0 || s.LimitMB != 0 || len(e.woken) != woken+1 {
		t.Errorf("limits removed: %d %d, woken %v (waiting photos must be looked at again)", s.LimitPhotos, s.LimitMB, e.woken)
	}
}

func TestWhatASenderHandsOverIsCountedForTheirMembershipOnly(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "party", Tag: "party", Policy: "open", LimitPhotos: 2})
	if err := e.h.Join(ctx, e.sess, bob, "party", "", ""); err != nil {
		t.Fatal(err)
	}
	snd := service(e, "party").Senders[0]
	snap, err := e.h.Handed(ctx, snd, time.Now().Add(-time.Hour))
	if err != nil || !snap.Tracked || snap.Photos != 0 {
		t.Fatalf("nothing yet: %+v %v", snap, err)
	}
	e.h.Record(ctx, snd, 3<<20)
	e.h.Record(ctx, snd, 1<<20)
	snap, _ = e.h.Handed(ctx, snd, time.Now().Add(-time.Hour))
	if snap.Photos != 2 || snap.Bytes != 4<<20 || snap.Oldest.IsZero() {
		t.Errorf("after two: %+v", snap)
	}
	if snap, _ = e.h.Handed(ctx, snd, time.Now().Add(time.Minute)); snap.Photos != 0 {
		t.Errorf("only what is inside the window counts: %+v", snap)
	}
	members, _ := e.h.MembersOf(ctx, "party")
	if len(members) != 1 || members[0].Photos != 2 || members[0].MB != 4 {
		t.Errorf("the owner sees what bob handed over: %+v", members)
	}
	// an account from the configuration has no stored membership: nothing to count, nothing to cap
	cfg := service(e, "family")
	if cfg.Receivers == nil {
		t.Fatal("setup")
	}
	if snap, _ := e.h.Handed(ctx, cfg.Receivers[0], time.Now().Add(-time.Hour)); snap.Tracked {
		t.Error("an account from the configuration must not be tracked")
	}
	// leaving forgets the record
	if err := e.h.Leave(ctx, e.sess, bob, "party"); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "party", "", ""); err != nil {
		t.Fatal(err)
	}
	snd = service(e, "party").Senders[0]
	if snap, _ := e.h.Handed(ctx, snd, time.Now().Add(-time.Hour)); snap.Photos != 0 {
		t.Errorf("a new membership starts with nothing counted: %+v", snap)
	}
}

func TestPausedStateSurvivesARestart(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "party", Tag: "party", Policy: "open", LimitMB: 7})
	e.h.Join(ctx, e.sess, bob, "party", "", "")
	e.h.SetMemberPaused(ctx, bob, "party", true)
	e.h.SetServicePaused(ctx, ann, "party", true)
	if err := e.h.Reload(ctx); err != nil { // what a restart does: rebuild everything from the data file
		t.Fatal(err)
	}
	s := service(e, "party")
	if !s.Paused || s.LimitMB != 7 || !s.Senders[0].Paused {
		t.Errorf("after a reload: service paused %v, limit %d, bob paused %v", s.Paused, s.LimitMB, s.Senders[0].Paused)
	}
}

func TestTheWeekCountsWhatSendersHandedOverAndWhatArrivedForOwners(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "party", Tag: "party", Policy: "open"})
	if err := e.h.Join(ctx, e.sess, bob, "party", "", ""); err != nil {
		t.Fatal(err)
	}
	snd := service(e, "party").Senders[0]
	for i := 0; i < 3; i++ {
		e.h.Record(ctx, snd, 1<<20)
	}
	now := time.Now()
	days, counts, err := e.h.Week(ctx, bobID, now)
	if err != nil {
		t.Fatal(err)
	}
	if counts[6] != 3 || counts[0]+counts[1]+counts[2]+counts[3]+counts[4]+counts[5] != 0 {
		t.Errorf("bob sent three today: %v", counts)
	}
	if days[6].Format("2006-01-02") != now.UTC().Format("2006-01-02") || days[0].Format("2006-01-02") != now.UTC().AddDate(0, 0, -6).Format("2006-01-02") {
		t.Errorf("seven days, oldest first, the last being today: %v", days)
	}
	if _, owner, _ := e.h.Week(ctx, annID, now); owner[6] != 3 {
		t.Errorf("ann owns the service they arrived in: %v", owner)
	}
	if _, other, _ := e.h.Week(ctx, uuid.New(), now); other != [7]int{} {
		t.Errorf("someone with no part in it sees nothing: %v", other)
	}
}

func TestTheGuideIsFinishedOnceAndOnlyWhereThereIsADataFile(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	if e.h.GuideDone(ctx, bobID) {
		t.Fatal("a new person has not finished the guide")
	}
	if err := e.h.DismissGuide(ctx, bobID); err != nil {
		t.Fatal(err)
	}
	if !e.h.GuideDone(ctx, bobID) || e.h.GuideDone(ctx, annID) {
		t.Error("it is remembered for bob, and only for bob")
	}
	noStore := &hub.Hub{}
	if !noStore.GuideDone(ctx, bobID) {
		t.Error("with nowhere to remember it the guide must not be offered")
	}
}

// en is stored text (a line of the log, the reason a connection is paused) in English, as a reader of English sees it.
func en(s string) string { return i18n.For(i18n.English).Unpack(s) }

// feeds opens one page for each kind of person who might be looking.
type feeds struct{ owner, sender, other, root, receiver *live.Sub }

func listen(e *env) *feeds {
	bus := &live.Bus{Window: 10 * time.Millisecond}
	e.h.Bus = bus
	sub := func(id uuid.UUID, all bool) *live.Sub { s, _ := bus.Subscribe(id, all); return s }
	return &feeds{owner: sub(annID, false), sender: sub(bobID, false), other: sub(uuid.New(), false), root: sub(uuid.New(), true), receiver: sub(familyID, false)}
}

func heard(s *live.Sub) []live.Event {
	var out []live.Event
	for {
		select {
		case ev := <-s.C:
			out = append(out, ev)
		case <-time.After(150 * time.Millisecond):
			return out
		}
	}
}

func told(evs []live.Event, kind live.Kind, reason string) bool {
	for _, ev := range evs {
		if ev.Kind == kind && ev.Reason == reason {
			return true
		}
	}
	return false
}

func TestPausingAndResumingAreToldToTheRightPeople(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "party", Tag: "party", Policy: "open"})
	e.h.Join(ctx, e.sess, bob, "party", "", "")
	f := listen(e)

	e.h.SetServicePaused(ctx, ann, "party", true)
	for name, s := range map[string]*live.Sub{"owner": f.owner, "sender": f.sender, "administrator": f.root} {
		if !told(heard(s), live.Paused, "service") {
			t.Errorf("%s must hear that the service was paused", name)
		}
	}
	if evs := heard(f.other); len(evs) != 0 {
		t.Errorf("a stranger must hear nothing: %v", evs)
	}

	e.h.SetMemberPaused(ctx, bob, "party", true)
	e.h.SetMemberPaused(ctx, bob, "party", false)
	evs := heard(f.owner)
	if !told(evs, live.Paused, "sender") || !told(evs, live.Resumed, "sender") {
		t.Errorf("the owner must hear bob pause and resume: %v", evs)
	}
	for _, ev := range evs {
		if ev.Reason == "sender" && ev.Sender != "Bob" {
			t.Errorf("the event names the sender: %+v", ev)
		}
	}
	if evs := heard(f.other); len(evs) != 0 {
		t.Errorf("a stranger must hear nothing: %v", evs)
	}
}

func TestALostConnectionAndItsRestoreAreTold(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "party", Tag: "party", Policy: "open"})
	e.h.Join(ctx, e.sess, bob, "party", "", "")
	f := listen(e)

	e.f.deleteKeyOf("bob") // bob deletes his key in Immich
	e.h.CheckKeys(ctx)
	for name, s := range map[string]*live.Sub{"owner": f.owner, "sender": f.sender, "administrator": f.root} {
		if !told(heard(s), live.Lost, "sender") {
			t.Errorf("%s must hear bob's connection was cut", name)
		}
	}
	if evs := heard(f.other); len(evs) != 0 {
		t.Errorf("a stranger must hear nothing: %v", evs)
	}

	// the owner's own key is deleted: the service cannot receive, and every sender is told too
	e.h.Join(ctx, e.sess, bob, "party", "", "") // bob joins again after cleaning up
	e.h.Leave(ctx, e.sess, bob, "party")
	e.h.Join(ctx, e.sess, bob, "party", "", "")
	f = listen(e)
	e.f.deleteKeyOf("ann")
	e.h.CheckKeys(ctx)
	if !told(heard(f.sender), live.Lost, "service") {
		t.Error("a sender must hear that the service lost its receiver")
	}
	e.h.ReconnectService(ctx, e.ann, ann, "party")
	if !told(heard(f.owner), live.Restored, "service") || !told(heard(f.sender), live.Restored, "service") {
		t.Error("owner and senders must hear that the service was restored")
	}
}

func TestAReachedLimitIsToldToTheOwnerAndTheSender(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "party", Tag: "party", Policy: "open", LimitPhotos: 1})
	e.h.Join(ctx, e.sess, bob, "party", "", "")
	f := listen(e)
	snd := service(e, "party").Senders[0]
	until := time.Now().Add(3 * time.Hour)
	e.h.LimitReached(service(e, "party"), snd, 2, until)
	for name, s := range map[string]*live.Sub{"owner": f.owner, "sender": f.sender, "administrator": f.root} {
		var got live.Event
		for _, ev := range heard(s) {
			if ev.Kind == live.Limit {
				got = ev
			}
		}
		if got.Count != 2 || got.Until == nil || got.Sender != "Bob" {
			t.Errorf("%s: %+v", name, got)
		}
	}
	if evs := heard(f.other); len(evs) != 0 {
		t.Errorf("a stranger must hear nothing: %v", evs)
	}
}

func TestKeysMadeBeforeStacksGetTheStackPermissionsOnceTheirOwnerOpensThePage(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "holiday", Tag: "hol", Policy: "open", Visibility: "listed"}); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Join(ctx, e.sess, bob, "holiday", "", ""); err != nil {
		t.Fatal(err)
	}
	// make the keys look like the ones made before stacks were handed over
	for id, perms := range e.f.keyPerms {
		var kept []string
		for _, p := range perms {
			if p != "stack.read" && p != "stack.create" {
				kept = append(kept, p)
			}
		}
		e.f.keyPerms[id] = kept
	}
	links, _ := e.st.Links(ctx)
	svcs, _ := e.st.Services(ctx)
	if len(links) != 1 || len(svcs) != 1 {
		t.Fatalf("setup: %d links, %d services", len(links), len(svcs))
	}
	has := func(key, perm string) bool {
		for _, p := range e.f.keyPerms[key] {
			if p == perm {
				return true
			}
		}
		return false
	}
	e.h.EnsureKeyPermissions(ctx, e.sess, bob)
	if !has(links[0].KeyID.String(), "stack.read") {
		t.Errorf("the sender's key must be able to read stacks: %v", e.f.keyPerms[links[0].KeyID.String()])
	}
	if has(svcs[0].KeyID.String(), "stack.create") {
		t.Errorf("a person's session must not touch the key of a service someone else owns")
	}
	e.h.EnsureKeyPermissions(ctx, e.ann, ann)
	if !has(svcs[0].KeyID.String(), "stack.create") {
		t.Errorf("the owner's receiver key must be able to make stacks: %v", e.f.keyPerms[svcs[0].KeyID.String()])
	}
	updates := e.f.keyUpdates
	e.h.EnsureKeyPermissions(ctx, e.sess, bob)
	e.h.EnsureKeyPermissions(ctx, e.ann, ann)
	if e.f.keyUpdates != updates {
		t.Errorf("the second visit must change nothing")
	}
}

func TestTheOwnerSetsQuietHoursAndWhatTheServiceAcceptsAndNobodyElseCan(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "frame", Tag: "frame", Policy: "open", Visibility: "listed"}); err != nil {
		t.Fatal(err)
	}
	q, a := syncer.Quiet{From: 60, To: 360, LargeMB: 200}, syncer.Accept{MinRating: 4, PhotosOnly: true, Orientation: "landscape", MaxMB: 20}
	if err := e.h.SetRules(ctx, ann, "frame", q, a); err != nil {
		t.Fatal(err)
	}
	svc := service(e, "frame")
	if svc.Quiet != q || svc.Accept != a {
		t.Errorf("the running service must carry the rules: %+v %+v", svc.Quiet, svc.Accept)
	}
	if err := e.h.SetRules(ctx, bob, "frame", syncer.Quiet{}, syncer.Accept{}); err == nil {
		t.Error("only the owner may set rules")
	}
	if err := e.h.SetRules(ctx, ann, "frame", syncer.Quiet{}, syncer.Accept{MinRating: 9}); !errors.Is(err, hub.ErrBadRules) {
		t.Errorf("a rating of 9 must be refused: %v", err)
	}
	if err := e.h.SetRules(ctx, ann, "frame", syncer.Quiet{}, syncer.Accept{}); err != nil {
		t.Fatal(err)
	}
	if svc := service(e, "frame"); svc.Quiet.Set() || svc.Accept.Active() {
		t.Errorf("clearing the rules clears them: %+v %+v", svc.Quiet, svc.Accept)
	}
}

func hasPerm(perms []string, name string) bool {
	for _, p := range perms {
		if p == name {
			return true
		}
	}
	return false
}

func TestANewReceiverKeyCanDeleteOnlyWhenTheAdministratorAllowsIt(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		e, ctx := setup(t, false), context.Background()
		e.h.ReceiverDelete = allowed
		if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "frame", Tag: "frame", Policy: "open", Visibility: "listed"}); err != nil {
			t.Fatal(err)
		}
		if got := hasPerm(e.f.lastPerms, "asset.delete"); got != allowed {
			t.Errorf("RECEIVER_DELETE=%v: the new receiver key has asset.delete = %v (%v)", allowed, got, e.f.lastPerms)
		}
	}
}

func TestKeysGetOrLoseTheDeletePermissionWhenTheirOwnerOpensThePage(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.ReceiverDelete = false
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "frame", Tag: "frame", Policy: "open", Visibility: "listed"}); err != nil {
		t.Fatal(err)
	}
	svcs, _ := e.st.Services(ctx)
	key := svcs[0].KeyID.String()
	if hasPerm(e.f.keyPerms[key], "asset.delete") {
		t.Fatal("a key made while deleting is off has no delete permission")
	}
	// the administrator switches it on and restarts: the key is upgraded when its owner opens the page
	e.h.ReceiverDelete = true
	e.h.EnsureKeyPermissions(ctx, e.ann, ann)
	if !hasPerm(e.f.keyPerms[key], "asset.delete") {
		t.Errorf("the key must get asset.delete: %v", e.f.keyPerms[key])
	}
	// and switched off again after another restart: it is taken away, and the key keeps what else it had
	e.h.ReceiverDelete = false
	e.h.ForgetGranted()
	e.h.EnsureKeyPermissions(ctx, e.ann, ann)
	if hasPerm(e.f.keyPerms[key], "asset.delete") || !hasPerm(e.f.keyPerms[key], "asset.upload") {
		t.Errorf("the key loses asset.delete and keeps the rest: %v", e.f.keyPerms[key])
	}
}

func TestTheOwnerSetsHowLongCopiesLiveAndNobodyElseCan(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	e.h.ReceiverDelete = true
	if err := e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "frame", Tag: "frame", Policy: "open", Visibility: "listed"}); err != nil {
		t.Fatal(err)
	}
	if err := e.h.SetLifetime(ctx, e.ann, ann, "frame", 7); err != nil {
		t.Fatal(err)
	}
	if got := service(e, "frame").LifetimeDays; got != 7 {
		t.Errorf("the running service carries the lifetime: %d", got)
	}
	svcs, _ := e.st.Services(ctx)
	if !hasPerm(e.f.keyPerms[svcs[0].KeyID.String()], "asset.delete") {
		t.Error("setting a lifetime gives the receiver key the permission it needs, with the owner's own session")
	}
	if err := e.h.SetLifetime(ctx, e.sess, bob, "frame", 3); err == nil {
		t.Error("only the owner may set a lifetime")
	}
	for _, bad := range []int{-1, 5000} {
		if err := e.h.SetLifetime(ctx, e.ann, ann, "frame", bad); !errors.Is(err, hub.ErrBadLifetime) {
			t.Errorf("%d days must be refused: %v", bad, err)
		}
	}
	if err := e.h.SetLifetime(ctx, e.ann, ann, "frame", 0); err != nil || service(e, "frame").LifetimeDays != 0 {
		t.Errorf("0 turns it off: %v", err)
	}
	e.h.ReceiverDelete = false
	if err := e.h.SetLifetime(ctx, e.ann, ann, "frame", 7); !errors.Is(err, hub.ErrDeleteOff) {
		t.Errorf("with deleting switched off a lifetime cannot be set: %v", err)
	}
}
