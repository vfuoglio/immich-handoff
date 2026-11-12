package doctor

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/bridge"
)

// other is the receiver's server: what a check of the key asks.
func other(t *testing.T, major int, perms string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server/version":
			w.Write([]byte(`{"major":` + string(rune('0'+major)) + `,"minor":2,"patch":0}`))
		case "/api/api-keys/me":
			if r.Header.Get("x-api-key") != "FAR" {
				http.Error(w, "no", 401)
				return
			}
			w.Write([]byte(`{"permissions":` + perms + `}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

const four = `["user.read","tag.create","tag.asset","asset.upload"]`

func withRemote(t *testing.T, s *scenario, address, key string, p bridge.Policy) func(*Input) {
	return func(in *Input) {
		r, err := bridge.NewRemote("Aunt Lia", address, key, p)
		if err != nil {
			t.Fatal(err)
		}
		in.Services[0].Remote = r
	}
}

func TestADoctorChecksTheReceiverOnAnotherServer(t *testing.T) {
	open := bridge.Policy{AllowPrivate: true}
	for name, c := range map[string]struct {
		srv     *httptest.Server
		key     string
		policy  bridge.Policy
		address string // empty: the server's
		fails   int
		needles []string
	}{
		"healthy":                             {srv: other(t, 3, four), key: "FAR", policy: open, needles: []string{"receiver on another server (Aunt Lia): the address is allowed", "Immich v3.2.0", "user.read, tag.create, tag.asset, asset.upload; taking a hand-over back does not reach it"}},
		"with the optional delete permission": {srv: other(t, 3, `["user.read","tag.create","tag.asset","asset.upload","asset.delete"]`), key: "FAR", policy: open, needles: []string{"taking a hand-over back reaches it"}},
		"too broad":                           {srv: other(t, 3, `["user.read","tag.create","tag.asset","asset.upload","asset.read"]`), key: "FAR", policy: open, fails: 1, needles: []string{"can do more than it should (asset.read)", "can read the receiver's library", "Give the key exactly these permissions"}},
		"too small":                           {srv: other(t, 3, `["user.read","asset.upload"]`), key: "FAR", policy: open, fails: 1, needles: []string{"lacks tag.create, tag.asset", "new key"}},
		"revoked":                             {srv: other(t, 3, four), key: "WRONG", policy: open, fails: 1, needles: []string{"does not accept the key", "Photos wait, still tagged"}},
		"old version":                         {srv: other(t, 2, four), key: "FAR", policy: open, fails: 1, needles: []string{"v2.2.0", "not one this app was tested with"}},
		"unreachable":                         {srv: other(t, 3, four), key: "FAR", policy: open, address: "http://127.0.0.1:1", fails: 1, needles: []string{"cannot reach the server", "growing pauses"}},
		"private":                             {srv: other(t, 3, four), key: "FAR", policy: bridge.Policy{}, fails: 1, needles: []string{"not one the app may connect to", "BRIDGE_ALLOW_PRIVATE=true"}},
	} {
		t.Run(name, func(t *testing.T) {
			s := newScenario(t)
			address := c.address
			if address == "" {
				address = c.srv.URL
			}
			out, fails, _ := s.run(t, true, withRemote(t, s, address, c.key, c.policy))
			want(t, out, fails, c.fails, c.needles...)
		})
	}
}

func TestADoctorChangesNothingAboutTheBridge(t *testing.T) {
	// no Report or Gate is called: the doctor only reads
	s := newScenario(t)
	srv := other(t, 3, four)
	reported := false
	out, fails, _ := s.run(t, true, func(in *Input) {
		withRemote(t, s, srv.URL, "FAR", bridge.Policy{AllowPrivate: true})(in)
		in.Services[0].Remote.(*bridge.Remote).Report = func(error, string) { reported = true }
		in.Services[0].Remote.(*bridge.Remote).Gate = func() error { reported = true; return nil }
	})
	want(t, out, fails, 0)
	if reported {
		t.Fatal("the doctor must not change what the app knows about the connection")
	}
}

func TestAServiceStoppedForAReasonThatIsNotItsKeyIsNotToldToReconnect(t *testing.T) {
	s := newScenario(t)
	out, fails, _ := s.run(t, true, func(in *Input) {
		in.Services[0].Broken = "bridges to other servers are switched off"
		in.Services[0].BrokenFix = "Set BRIDGE_ENABLE=true again."
	})
	want(t, out, fails, 1, "bridges to other servers are switched off", "Set BRIDGE_ENABLE=true again.")
	if contains := len(out) > 0 && (indexOf(out, "presses Reconnect") >= 0); contains {
		t.Errorf("reconnecting a receiver key would not help:\n%s", out)
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
