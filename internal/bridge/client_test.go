package bridge

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublic(t *testing.T) {
	refused := map[string]string{
		"127.0.0.1": "loopback", "::1": "loopback v6", "10.1.2.3": "private", "172.16.0.9": "private", "192.168.1.1": "private",
		"169.254.169.254": "cloud metadata", "169.254.1.1": "link-local", "fe80::1": "link-local v6", "fd00::1": "unique-local v6",
		"fc00::5": "unique-local v6", "0.0.0.0": "unspecified", "::": "unspecified v6", "100.64.0.1": "carrier-grade NAT",
		"224.0.0.1": "multicast", "255.255.255.255": "broadcast", "::ffff:10.0.0.1": "private written as IPv6",
		"::ffff:127.0.0.1": "loopback written as IPv6", "::ffff:169.254.169.254": "metadata written as IPv6", "64:ff9b::a00:1": "NAT64",
	}
	for s, why := range refused {
		if Public(netip.MustParseAddr(s)) {
			t.Errorf("%s (%s) must be refused", s, why)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "93.184.216.35", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if !Public(netip.MustParseAddr(s)) {
			t.Errorf("%s must be allowed", s)
		}
	}
}

func TestAddress(t *testing.T) {
	strict, open := Policy{}, Policy{AllowPrivate: true}
	good := map[string]string{
		"https://photos.example.org":      "https://photos.example.org",
		"photos.example.org":              "https://photos.example.org",
		"  https://photos.example.org/  ": "https://photos.example.org",
		"https://photos.example.org:2283": "https://photos.example.org:2283",
		"https://photos.example.org/api":  "https://photos.example.org", // a path is dropped: the app adds /api itself
		"https://8.8.8.8":                 "https://8.8.8.8",
	}
	for in, want := range good {
		got, err := strict.Address(in)
		if err != nil || got != want {
			t.Errorf("Address(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "   ", "http://photos.example.org", "ftp://photos.example.org", "https://", "https://user:pw@photos.example.org",
		"https://127.0.0.1", "https://[::1]", "https://10.0.0.5:2283", "https://169.254.169.254", "https://192.168.1.10",
		"https://photos.example.org:0", "https://photos.example.org:99999", "https://[fd00::1]", "javascript:alert(1)",
	} {
		if got, err := strict.Address(in); !errors.Is(err, ErrBadAddress) {
			t.Errorf("Address(%q) = %q, %v; must be refused", in, got, err)
		}
	}
	// the opt-in: private numbers and plain http are accepted, but userinfo and other schemes still are not
	for _, in := range []string{"http://192.168.1.10:2283", "https://10.0.0.5", "http://immich.lan:2283", "http://127.0.0.1:2283"} {
		if _, err := open.Address(in); err != nil {
			t.Errorf("with the opt-in, Address(%q): %v", in, err)
		}
	}
	for _, in := range []string{"ftp://x.lan", "http://u:p@x.lan"} {
		if _, err := open.Address(in); err == nil {
			t.Errorf("with the opt-in, Address(%q) must still be refused", in)
		}
	}
}

// fakeResolver answers each lookup from a script and counts them.
func fakeResolver(answers ...[]string) (func(context.Context, string) ([]netip.Addr, error), *int32) {
	var n int32
	return func(context.Context, string) ([]netip.Addr, error) {
		i := int(atomic.AddInt32(&n, 1)) - 1
		if i >= len(answers) {
			i = len(answers) - 1
		}
		var out []netip.Addr
		for _, s := range answers[i] {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}, &n
}

// server is a plain server on localhost that a fake dialer connects to whatever address the client chose.
func server(t *testing.T, h http.Handler) (addr string, dialed *[]string, dial func(context.Context, *net.Dialer, string, string) (net.Conn, error)) {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	var got []string
	return s.Listener.Addr().String(), &got, func(ctx context.Context, d *net.Dialer, network, address string) (net.Conn, error) {
		got = append(got, address)
		return d.DialContext(ctx, "tcp", s.Listener.Addr().String())
	}
}

func get(c *http.Client, host string) (*http.Response, error) {
	return c.Get("http://" + host + "/ping")
}

func TestClientRefusesNamesThatPointInside(t *testing.T) {
	for name, answer := range map[string][]string{
		"loopback": {"127.0.0.1"}, "private": {"10.0.0.2"}, "metadata": {"169.254.169.254"}, "unique-local": {"fd12::1"},
		"mixed (one private answer among public ones)": {"8.8.8.8", "192.168.0.7"},
	} {
		t.Run(name, func(t *testing.T) {
			resolve, _ := fakeResolver(answer)
			_, _, dial := server(t, http.NotFoundHandler())
			var dialed int
			p := Policy{resolve: resolve, dial: func(ctx context.Context, d *net.Dialer, n, a string) (net.Conn, error) {
				dialed++
				return dial(ctx, d, n, a)
			}}
			_, err := get(p.HTTPClient(), "photos.example.org")
			if !errors.Is(err, ErrBadAddress) {
				t.Fatalf("err = %v; want ErrBadAddress", err)
			}
			if dialed != 0 {
				t.Fatal("the client connected before refusing")
			}
		})
	}
}

func TestClientIsNotFooledByANameThatChanges(t *testing.T) {
	// DNS rebinding: the first lookup (the check when the address is added) is public, the next (the real
	// connection) is private. Every connection looks the name up and judges the answer again.
	resolve, n := fakeResolver([]string{"8.8.8.8"}, []string{"127.0.0.1"})
	addr, dialed, dial := server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	_ = addr
	p := Policy{resolve: resolve, dial: dial}
	c := p.HTTPClient()
	c.Transport.(*http.Transport).DisableKeepAlives = true
	resp, err := get(c, "photos.example.org")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	resp.Body.Close()
	if _, err := get(c, "photos.example.org"); !errors.Is(err, ErrBadAddress) {
		t.Fatalf("after the name moved inside, err = %v; want ErrBadAddress", err)
	}
	if *n != 2 || len(*dialed) != 1 || !strings.HasPrefix((*dialed)[0], "8.8.8.8:") {
		t.Fatalf("lookups %d, dialled %v", *n, *dialed)
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	resolve, _ := fakeResolver([]string{"8.8.8.8"})
	_, _, dial := server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example.org/api/ping", http.StatusFound)
	}))
	resp, err := get((Policy{resolve: resolve, dial: dial}).HTTPClient(), "photos.example.org")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !IsRedirect(resp) || RedirectTarget(resp) != "https://elsewhere.example.org" {
		t.Fatalf("status %d, target %q: the redirect must be handed back, not followed", resp.StatusCode, RedirectTarget(resp))
	}
}

func TestClientOptInAllowsPrivateAddresses(t *testing.T) {
	resolve, _ := fakeResolver([]string{"192.168.1.50"})
	_, dialed, dial := server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	resp, err := get((Policy{AllowPrivate: true, resolve: resolve, dial: dial}).HTTPClient(), "immich.lan")
	if err != nil {
		t.Fatalf("with the opt-in a private name must work: %v", err)
	}
	resp.Body.Close()
	if len(*dialed) != 1 || !strings.HasPrefix((*dialed)[0], "192.168.1.50:") {
		t.Fatalf("dialled %v", *dialed)
	}
}

func TestClientRefusesANumberedPrivateAddressWithoutResolving(t *testing.T) {
	resolve, n := fakeResolver([]string{"8.8.8.8"})
	p := Policy{resolve: resolve}
	if _, err := p.HTTPClient().Get("http://10.0.0.5/ping"); !errors.Is(err, ErrBadAddress) {
		t.Fatalf("err = %v", err)
	}
	if *n != 0 {
		t.Fatal("a numbered address needs no lookup")
	}
}

func TestClientIgnoresProxyFromEnvironment(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	if (Policy{}).HTTPClient().Transport.(*http.Transport).Proxy != nil {
		t.Fatal("a proxy from the environment is a way around the address rules")
	}
}

// The real dialer (no test hook) must refuse a private address as well: this is the check on the actual connection.
func TestRealDialerRefusesLoopback(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	defer s.Close()
	strict, open := Policy{}, Policy{AllowPrivate: true}
	if _, err := strict.HTTPClient().Get(s.URL); !errors.Is(err, ErrBadAddress) {
		t.Fatalf("err = %v; a server on 127.0.0.1 must be refused", err)
	}
	if resp, err := open.HTTPClient().Get(s.URL); err != nil {
		t.Fatalf("with the opt-in: %v", err)
	} else {
		resp.Body.Close()
	}
}

func TestSettingsFrom(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if s := SettingsFrom(env(nil)); s.Enabled || s.AllowPrivate || s.RetryBase != time.Minute {
		t.Fatalf("both are off by default and the first pause is a minute: %+v", s)
	}
	if s := SettingsFrom(env(map[string]string{"BRIDGE_RETRY_MINUTES": "0.05"})); s.RetryBase != 3*time.Second {
		t.Fatalf("%v", s.RetryBase)
	}
	if s := SettingsFrom(env(map[string]string{"BRIDGE_RETRY_MINUTES": "nonsense"})); s.RetryBase != time.Minute {
		t.Fatalf("%v", s.RetryBase)
	}
	if s := SettingsFrom(env(map[string]string{"BRIDGE_ENABLE": "true", "BRIDGE_ALLOW_PRIVATE": " ON "})); !s.Enabled || !s.AllowPrivate {
		t.Fatalf("%+v", s)
	}
	if s := SettingsFrom(env(map[string]string{"BRIDGE_ALLOW_PRIVATE": "true"})); s.Enabled {
		t.Fatal("allowing private addresses does not turn bridges on")
	}
}
