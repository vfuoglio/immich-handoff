package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeImmich answers the few calls a bridge makes, and records every request so a test can say what was not asked.
type fakeImmich struct {
	*httptest.Server
	mu          sync.Mutex
	Version     [3]int
	Permissions []string
	ValidKey    string // "" accepts any key
	Down        int    // answer with this status to everything (0: work)
	Redirect    string // answer the version call with a redirect to this address
	Paths       []string
}

func newFake(t *testing.T) *fakeImmich {
	t.Helper()
	f := &fakeImmich{Version: [3]int{3, 2, 0}, Permissions: append([]string{}, RequiredPermissions...), ValidKey: "good-key"}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.Paths = append(f.Paths, r.Method+" "+r.URL.Path)
		if f.Down != 0 {
			http.Error(w, "down", f.Down)
			return
		}
		switch r.URL.Path {
		case "/api/server/version":
			if f.Redirect != "" {
				http.Redirect(w, r, f.Redirect+"/api/server/version", http.StatusMovedPermanently)
				return
			}
			json.NewEncoder(w).Encode(map[string]int{"major": f.Version[0], "minor": f.Version[1], "patch": f.Version[2]})
		case "/api/api-keys/me":
			if f.ValidKey != "" && r.Header.Get("x-api-key") != f.ValidKey {
				http.Error(w, `{"message":"Invalid API key"}`, http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"id": "k", "name": "handoff", "permissions": f.Permissions})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeImmich) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.Paths...)
}

// client reaches the fake on localhost, which only the opt-in allows.
func (f *fakeImmich) client() *http.Client { return Policy{AllowPrivate: true}.HTTPClient() }
