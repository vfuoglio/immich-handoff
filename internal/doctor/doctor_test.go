package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

const hookURL = "http://handoff.test/webhook/immich"

// fakeImmich answers what the checks ask, per API key.
type fakeImmich struct {
	mu       sync.Mutex
	major    int
	minor    int
	perms    map[string][]string       // key -> permissions; a key not listed is rejected
	tagsOff  map[string]bool           // key -> Tags switched off
	noTag    map[string]bool           // key -> the share tag does not exist
	workflow map[string]map[string]any // key -> stored workflow body
	runs     map[string][]map[string]any
}

func newFake() *fakeImmich {
	return &fakeImmich{major: 3, minor: 2, perms: map[string][]string{}, tagsOff: map[string]bool{}, noTag: map[string]bool{},
		workflow: map[string]map[string]any{}, runs: map[string][]map[string]any{}}
}

func (f *fakeImmich) handler() http.Handler {
	j := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/server/version", func(w http.ResponseWriter, r *http.Request) {
		j(w, 200, map[string]any{"major": f.major, "minor": f.minor, "patch": 0})
	})
	mux.HandleFunc("GET /api/api-keys/me", func(w http.ResponseWriter, r *http.Request) {
		p, ok := f.perms[r.Header.Get("x-api-key")]
		if !ok {
			http.Error(w, "no", 401)
			return
		}
		j(w, 200, map[string]any{"id": "11111111-1111-4111-8111-111111111111", "name": "k", "permissions": p, "createdAt": time.Now(), "updatedAt": time.Now()})
	})
	mux.HandleFunc("GET /api/users/me/preferences", func(w http.ResponseWriter, r *http.Request) {
		on := !f.tagsOff[r.Header.Get("x-api-key")]
		j(w, 200, map[string]any{"tags": map[string]any{"enabled": on, "sidebarWeb": on}})
	})
	tag := map[string]any{"id": "22222222-2222-4222-8222-222222222222", "name": "share", "value": "share", "createdAt": time.Now(), "updatedAt": time.Now()}
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		if f.noTag[r.Header.Get("x-api-key")] {
			j(w, 200, []any{})
			return
		}
		j(w, 200, []any{tag})
	})
	mux.HandleFunc("PUT /api/tags", func(w http.ResponseWriter, r *http.Request) {
		delete(f.noTag, r.Header.Get("x-api-key"))
		j(w, 200, []any{tag})
	})
	const wid = "33333333-3333-4333-8333-333333333333"
	mux.HandleFunc("POST /api/workflows", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		b["id"], b["createdAt"], b["updatedAt"], b["logging"] = wid, time.Now().Format(time.RFC3339), time.Now().Format(time.RFC3339), true
		f.mu.Lock()
		f.workflow[r.Header.Get("x-api-key")] = b
		f.mu.Unlock()
		j(w, 201, b)
	})
	mux.HandleFunc("GET /api/workflows", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if b := f.workflow[r.Header.Get("x-api-key")]; b != nil {
			j(w, 200, []any{b})
			return
		}
		j(w, 200, []any{})
	})
	mux.HandleFunc("GET /api/workflows/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(f.perms[r.Header.Get("x-api-key")], "workflow.logs") {
			http.Error(w, `{"message":"Missing required permission: workflow.logs"}`, 403)
			return
		}
		out := f.runs[r.Header.Get("x-api-key")]
		if out == nil {
			out = []map[string]any{}
		}
		j(w, 200, out)
	})
	return mux
}

// scenario is one install: Immich, a webhook target, and one service with a receiver and a sender.
type scenario struct {
	f        *fakeImmich
	immich   *httptest.Server
	hookCode int // what the webhook address answers
	env      map[string]string
	hook     syncer.Webhook
	copy     bool
	album    string
}

func (s *scenario) api(t *testing.T, key string) *immich.API {
	a, err := immich.New(s.immich.URL, immich.Auth{APIKey: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

var good = map[string][]string{
	"recv":   {"user.read", "tag.read", "tag.create", "tag.asset", "asset.read", "asset.upload", "stack.create", "asset.delete"},
	"sender": {"user.read", "tag.read", "tag.create", "tag.asset", "asset.read", "asset.download", "stack.read", "workflow.read", "workflow.create", "workflow.update", "workflow.logs"},
}

// newScenario builds a healthy install; a test then breaks one thing and runs the check.
func newScenario(t *testing.T) *scenario {
	t.Helper()
	f := newFake()
	for k, v := range good {
		f.perms[k] = v
	}
	s := &scenario{f: f, hookCode: 202, copy: true,
		env:  map[string]string{"PUBLIC_IMMICH_URL": "https://photos.example"},
		hook: syncer.Webhook{URL: hookURL, Header: "X-Webhook-Secret", Secret: "0123456789abcdef0123"}}
	s.immich = httptest.NewServer(f.handler())
	t.Cleanup(s.immich.Close)
	return s
}

// run provisions the sender's workflow (as the app does at startup) unless the test removed it, then checks.
func (s *scenario) run(t *testing.T, provision bool, edit func(*Input)) (string, int, int) {
	t.Helper()
	hookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(s.hook.Header) != s.hook.Secret && s.hookCode == 202 {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(s.hookCode)
	}))
	t.Cleanup(hookSrv.Close)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, n, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, n, hookSrv.Listener.Addr().String())
	}}}
	snd, rcv := s.api(t, "sender"), s.api(t, "recv")
	svc := &syncer.Service{Name: "team", Tag: "share", Album: s.album,
		Senders: []*syncer.Account{{Name: "alice", API: snd}}, Receivers: []*syncer.Account{{Name: "bob", API: rcv}}}
	if s.copy {
		svc.Mode = syncer.ModeCopy
	}
	ctx := context.Background()
	if provision {
		tag, err := snd.FindTag(ctx, "share", true)
		if err != nil || tag == nil {
			t.Fatal("tag", err)
		}
		if _, err := snd.EnsureWorkflow(ctx, immich.WorkflowSpec{Name: svc.WorkflowName(), TagID: *tag, WebhookURL: s.hook.URL, HeaderName: s.hook.Header, HeaderSecret: s.hook.Secret}); err != nil {
			t.Fatal("workflow", err)
		}
	}
	in := Input{Env: func(k string) string { return s.env[k] }, BaseURL: s.immich.URL, Probe: s.api(t, ""),
		Services: []*syncer.Service{svc}, Hook: s.hook, Client: client}
	if edit != nil {
		edit(&in)
	}
	var out bytes.Buffer
	fails, warns := Run(ctx, in, &out)
	return out.String(), fails, warns
}

func want(t *testing.T, out string, fails int, wantFails int, needles ...string) {
	t.Helper()
	if fails != wantFails {
		t.Errorf("fails = %d, want %d\n%s", fails, wantFails, out)
	}
	for _, n := range needles {
		if !strings.Contains(out, n) {
			t.Errorf("output lacks %q\n%s", n, out)
		}
	}
}

func TestAHealthySetupHasNothingToFix(t *testing.T) {
	s := newScenario(t)
	out, fails, warns := s.run(t, true, nil)
	want(t, out, fails, 0, "reached Immich 3.2", "the webhook address answers and accepts the secret", "has not run yet", "Everything checked is fine")
	if warns != 0 {
		t.Errorf("warnings: %d\n%s", warns, out)
	}
}

func TestShortSecretsAreCaught(t *testing.T) {
	s := newScenario(t)
	s.hook.Secret = "short"
	out, fails, _ := s.run(t, true, nil)
	want(t, out, fails, 1, "WEBHOOK_SECRET is 5 characters", "openssl rand -hex 24")
	s = newScenario(t)
	s.env["HANDOFF_STORE_KEY"] = "tiny"
	out, fails, _ = s.run(t, true, nil)
	want(t, out, fails, 1, "HANDOFF_STORE_KEY is 4 characters")
}

func TestAWebhookAddressImmichCannotUseIsCaught(t *testing.T) {
	for _, u := range []string{"http://localhost:8080/webhook/immich", "http://127.0.0.1:8080/webhook/immich"} {
		s := newScenario(t)
		s.hook.URL = u
		out, fails, _ := s.run(t, true, nil)
		want(t, out, fails, 1, "which for Immich's server is Immich itself")
	}
	s := newScenario(t)
	s.hook.URL = "not a url"
	out, fails, _ := s.run(t, true, nil)
	want(t, out, fails, 1, "is not a web address")
}

func TestAMissingWebhookAddressIsAWarning(t *testing.T) {
	s := newScenario(t)
	s.hook.URL = ""
	out, fails, warns := s.run(t, false, nil)
	want(t, out, fails, 0, "PUBLIC_WEBHOOK_URL is not set")
	if warns != 1 {
		t.Errorf("warnings: %d\n%s", warns, out)
	}
}

func TestUnreachableImmichStopsTheOtherChecks(t *testing.T) {
	s := newScenario(t)
	s.immich.Close()
	out, fails, _ := s.run(t, false, nil)
	want(t, out, fails, 1, "cannot reach Immich", "IMMICH_NETWORK", "were skipped")
	if strings.Contains(out, "Service team") {
		t.Errorf("went on to check services\n%s", out)
	}
}

func TestOldImmichIsCaught(t *testing.T) {
	s := newScenario(t)
	s.f.minor = 1
	out, fails, _ := s.run(t, true, nil)
	want(t, out, fails, 1, "Immich is 3.1", "3.2.0 or newer")
}

func TestKeyProblems(t *testing.T) {
	s := newScenario(t)
	delete(s.f.perms, "sender") // deleted in Immich
	out, fails, _ := s.run(t, false, nil)
	want(t, out, fails, 1, "sender alice: Immich does not accept the API key", "Clean up")

	s = newScenario(t)
	s.f.perms["recv"] = []string{"user.read", "tag.read"}
	out, fails, _ = s.run(t, true, nil)
	want(t, out, fails, 1, "receiver bob: the key lacks", "asset.upload", "fixed-accounts.md")

	// originals kept: a sender key without asset.delete is right; originals moved: it is not
	s = newScenario(t)
	out, fails, _ = s.run(t, true, nil)
	want(t, out, fails, 0)
	s = newScenario(t)
	s.copy = false
	out, fails, _ = s.run(t, true, nil)
	want(t, out, fails, 1, "sender alice: the key lacks asset.delete")

	// an album on the service needs the receiver's album permissions
	s = newScenario(t)
	s.album = "Arrivals"
	out, fails, _ = s.run(t, true, nil)
	want(t, out, fails, 1, "album.read", "albumAsset.create")

	// a key with every permission is fine
	s = newScenario(t)
	s.f.perms["recv"] = []string{"all"}
	out, fails, _ = s.run(t, true, nil)
	want(t, out, fails, 0, "receiver bob: key works (all permissions)")
}

// Keys made before workflow.logs existed work; the check just cannot see Immich's runs, and says how to.
func TestAKeyWithoutTheRunLogPermissionOnlyWarns(t *testing.T) {
	s := newScenario(t)
	s.f.perms["sender"] = slices.DeleteFunc(slices.Clone(good["sender"]), func(p string) bool { return p == "workflow.logs" })
	out, fails, warns := s.run(t, true, nil)
	want(t, out, fails, 0, "may not read its runs", "workflow.logs")
	if warns != 1 {
		t.Errorf("warnings: %d\n%s", warns, out)
	}
	if strings.Contains(out, "lacks") {
		t.Errorf("a missing optional permission must not be reported as a missing permission\n%s", out)
	}
}

func TestTagsSwitchedOffIsAWarning(t *testing.T) {
	s := newScenario(t)
	s.f.tagsOff["sender"] = true
	out, fails, warns := s.run(t, true, nil)
	want(t, out, fails, 0, "sender alice: Tags is switched off", "Account Settings")
	if warns != 1 {
		t.Errorf("warnings: %d\n%s", warns, out)
	}
}

func TestWorkflowProblems(t *testing.T) {
	s := newScenario(t)
	out, fails, _ := s.run(t, false, nil)
	want(t, out, fails, 1, "does not exist", "Restart the app")

	s = newScenario(t)
	s.f.noTag["sender"] = true
	out, fails, _ = s.run(t, false, nil)
	want(t, out, fails, 1, "the tag share does not exist")

	// the workflow still points at the old address or secret
	s = newScenario(t)
	out, fails, _ = s.run(t, true, func(in *Input) { in.Hook.URL = "http://elsewhere.test/webhook/immich" })
	want(t, out, fails, 1, "disabled or does not match")
}

func TestRecentWorkflowRunsTellWhetherImmichReachesTheApp(t *testing.T) {
	run := func(result, step string, ago time.Duration) map[string]any {
		m := map[string]any{"id": "44444444-4444-4444-8444-444444444444", "at": time.Now().Add(-ago).UTC().Format(time.RFC3339), "result": result}
		if step != "" {
			m["lastStep"] = map[string]any{"method": step, "index": 1}
		}
		return m
	}
	s := newScenario(t)
	s.f.runs["sender"] = []map[string]any{run("completed", "", 2*time.Minute), run("completed", "", time.Hour)}
	out, fails, _ := s.run(t, true, nil)
	want(t, out, fails, 0, "last 2 workflow run(s) worked")

	s = newScenario(t)
	s.f.runs["sender"] = []map[string]any{run("completed", "", time.Hour), run("error", "immich-plugin-core#webhook", 2*time.Minute)}
	out, fails, _ = s.run(t, true, nil)
	want(t, out, fails, 1, "last run of the workflow failed at immich-plugin-core#webhook", "1 of the last 2", "SERVER container")

	s = newScenario(t)
	s.f.runs["sender"] = []map[string]any{run("completed", "", time.Minute), run("error", "immich-plugin-core#webhook", time.Hour)}
	out, fails, warns := s.run(t, true, nil)
	want(t, out, fails, 0, "the last run worked", "1 of the last 2 runs failed")
	if warns != 1 {
		t.Errorf("warnings: %d\n%s", warns, out)
	}
}

func TestTheWebhookAddressAnswering(t *testing.T) {
	s := newScenario(t)
	s.hookCode = 404
	out, fails, _ := s.run(t, true, nil)
	want(t, out, fails, 1, "answered 404 Not Found, not the app", "/webhook/immich")

	s = newScenario(t)
	s.hook.Secret = "0123456789abcdef0123"
	out, fails, _ = s.run(t, true, func(in *Input) {
		// the address answers, but wants a different secret than the app sends
		in.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 401, Body: http.NoBody, Header: http.Header{}}, nil
		})}
	})
	want(t, out, fails, 1, "answered 401", "X-Webhook-Secret")

	s = newScenario(t)
	_, fails, warns := s.run(t, true, func(in *Input) {
		in.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, net.ErrClosed })}
	})
	if fails != 0 || warns != 1 {
		t.Errorf("an unreachable address (from here) should only warn: fails %d warns %d", fails, warns)
	}
}

func TestAConfigurationThatDoesNotLoadIsReported(t *testing.T) {
	s := newScenario(t)
	out, fails, _ := s.run(t, false, func(in *Input) {
		in.Services = nil
		in.LoadErr = context.DeadlineExceeded
	})
	want(t, out, fails, 1, "cannot load the configuration or the stored keys", "HANDOFF_STORE_KEY")
}

func TestAPausedServiceIsReported(t *testing.T) {
	s := newScenario(t)
	out, fails, _ := s.run(t, true, func(in *Input) { in.Services[0].Broken = "its owner's key was deleted" })
	want(t, out, fails, 1, "the service is paused", "Reconnect")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBackupSettingsAreReported(t *testing.T) {
	s := newScenario(t)
	s.env["HANDOFF_STORE_KEY"] = "0123456789abcdef0123"
	out, fails, warns := s.run(t, true, nil)
	want(t, out, fails, 0, "automatic backups: every 24h0m0s, keeping the newest 7")
	if warns != 0 || strings.Contains(out, "HANDOFF_ADMIN_BACKUP_DOWNLOAD") {
		t.Errorf("by default nothing is said about the download and nothing warns (%d warnings)\n%s", warns, out)
	}

	s = newScenario(t)
	s.env["HANDOFF_STORE_KEY"] = "0123456789abcdef0123"
	s.env["BACKUP_EVERY"] = "off"
	s.env["HANDOFF_ADMIN_BACKUP_DOWNLOAD"] = "true"
	out, fails, warns = s.run(t, true, nil)
	want(t, out, fails, 0, "automatic backups are off", "an Immich administrator can download a backup")
	if warns != 1 {
		t.Errorf("warnings %d, want 1 (automatic backups off)\n%s", warns, out)
	}

	s = newScenario(t)
	s.env["HANDOFF_STORE_KEY"] = "0123456789abcdef0123"
	s.env["BACKUP_EVERY"] = "5m"
	out, fails, _ = s.run(t, true, nil)
	want(t, out, fails, 1, `BACKUP_EVERY="5m"`)

	s = newScenario(t)
	s.env["HANDOFF_ADMIN_BACKUP_DOWNLOAD"] = "true"
	out, _, warns = s.run(t, true, nil)
	want(t, out, 0, 0, "nothing is stored and there is nothing to download")
	if warns != 1 {
		t.Errorf("a download switched on without a store key must warn once, got %d\n%s", warns, out)
	}

	s = newScenario(t)
	s.env["HANDOFF_STORE_KEY"] = "0123456789abcdef0123"
	s.env["HANDOFF_ADMIN_BACKUP_DOWNLOAD"] = "yes"
	out, _, warns = s.run(t, true, nil)
	want(t, out, 0, 0, "only the value true switches the download on")
	if warns != 1 {
		t.Errorf("warnings %d, want 1\n%s", warns, out)
	}
}

func TestAKeyWithoutTheStackPermissionsWorksAndIsOnlyAWarning(t *testing.T) {
	s := newScenario(t)
	s.f.perms["sender"] = slices.DeleteFunc(slices.Clone(good["sender"]), func(p string) bool { return p == "stack.read" })
	s.f.perms["recv"] = slices.DeleteFunc(slices.Clone(good["recv"]), func(p string) bool { return p == "stack.create" })
	out, fails, warns := s.run(t, true, nil)
	want(t, out, fails, 0, "sender alice: the key works but lacks stack.read (without it: only the tagged photo of a stack is handed over",
		"receiver bob: the key works but lacks stack.create (without it: photos that arrive as a stack arrive as separate photos")
	if warns != 2 {
		t.Errorf("warnings %d, want one for each key\n%s", warns, out)
	}
}

func TestTheDeletePermissionOfAReceiverKeyIsCheckedAgainstReceiverDelete(t *testing.T) {
	without := slices.DeleteFunc(slices.Clone(good["recv"]), func(p string) bool { return p == "asset.delete" })
	cases := []struct {
		name  string
		env   string
		perms []string
		warn  string // what the warning says; empty: none
	}{
		{"on and the key can", "", good["recv"], ""},
		{"on and the key cannot", "", without, "may not move photos to the trash"},
		{"off and the key cannot", "off", without, ""},
		{"off and the key can", "off", good["recv"], "although RECEIVER_DELETE=off"},
	}
	for _, c := range cases {
		s := newScenario(t)
		s.f.perms["recv"] = c.perms
		out, fails, warns := s.run(t, true, func(in *Input) {
			prev := in.Env
			in.Env = func(k string) string {
				if k == "RECEIVER_DELETE" {
					return c.env
				}
				if prev != nil {
					return prev(k)
				}
				return ""
			}
		})
		if fails != 0 {
			t.Errorf("%s: a mismatch is never a failure\n%s", c.name, out)
		}
		if (c.warn == "") != (warns == 0) || (c.warn != "" && !strings.Contains(out, c.warn)) {
			t.Errorf("%s: warnings %d, want %q\n%s", c.name, warns, c.warn, out)
		}
	}
}
