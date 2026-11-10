// Package bridge connects the app to an Immich server it does not run, for a receiver who lives on another server
// (see docs/bridges-design.md). Everything here treats the address as hostile: it was typed by someone else.
package bridge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Errors that say why an address was not used. They are written for the person who typed it.
var (
	ErrBadAddress = errors.New("that is not an address the app may connect to")
	ErrRedirect   = errors.New("that server answers with a redirect, which the app does not follow")
)

// Timeouts for a connection to a server someone else runs. Variables so that tests can shorten them.
var (
	dialTimeout   = 10 * time.Second
	headerTimeout = 30 * time.Second
	callTimeout   = 30 * time.Second
)

// Policy says which addresses may be reached.
type Policy struct {
	// AllowPrivate lets private addresses through, and plain http://: for people who run both servers themselves,
	// behind a VPN. Off by default.
	AllowPrivate bool

	// resolve and dial are replaced by tests.
	resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	dial    func(ctx context.Context, d *net.Dialer, network, address string) (net.Conn, error)
}

// refused ranges beyond what netip can tell on its own.
var refusedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8",     // "this network"
		"100.64.0.0/10", // carrier-grade NAT (shared address space)
		"192.0.0.0/24",  // IETF protocol assignments
		"192.0.2.0/24",  // documentation
		"198.18.0.0/15", // benchmarking
		"198.51.100.0/24",
		"203.0.113.0/24",
		"224.0.0.0/4",  // multicast
		"240.0.0.0/4",  // reserved, and the broadcast address
		"64:ff9b::/96", // NAT64: would reach an IPv4 address by another name
		"2001:db8::/32",
		"fec0::/10", // old site-local
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// Public reports whether an address is a public one: not loopback, link-local (which holds the cloud metadata
// address 169.254.169.254), private, unique-local, multicast, unspecified or in a reserved range. An IPv4 address
// written as IPv6 (::ffff:10.0.0.1) is judged as the IPv4 address it is.
func Public(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range refusedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// Address checks what a person typed and returns the base URL (scheme and host, no path) the app will use.
// It looks only at the text; the name is resolved, and judged again, when the connection is made.
func (p Policy) Address(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: it is empty", ErrBadAddress)
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("%w: it cannot be read", ErrBadAddress)
	}
	if u.User != nil {
		return "", fmt.Errorf("%w: it must not carry a user name or password", ErrBadAddress)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !p.AllowPrivate {
			return "", fmt.Errorf("%w: it must start with https://", ErrBadAddress)
		}
	default:
		return "", fmt.Errorf("%w: it must start with https://", ErrBadAddress)
	}
	if port := u.Port(); port != "" {
		if n := atoi(port); n < 1 || n > 65535 {
			return "", fmt.Errorf("%w: the port is not valid", ErrBadAddress)
		}
	}
	// an address written as a number is judged now, so the refusal can say so; a name is judged on connecting
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !p.AllowPrivate && !Public(ip) {
		return "", fmt.Errorf("%w: it points inside a private network", ErrBadAddress)
	}
	return u.Scheme + "://" + u.Host, nil
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' || n > 1<<20 {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// HTTPClient is the only way the app reaches a remote server. It resolves the name itself, refuses a name that
// has any non-public address (unless the policy allows private ones), connects to an address it has judged and
// judges it again at the moment of connecting (in the dialer's Control, so a name that changes between the
// check and the connection cannot slip through), never follows a redirect, and gives up quickly on silence.
// There is no total time limit: a large file may take as long as it likes while bytes move; callers bound a call
// with a context.
func (p Policy) HTTPClient() *http.Client {
	resolve := p.resolve
	if resolve == nil {
		resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	d := &net.Dialer{Timeout: dialTimeout}
	if !p.AllowPrivate && p.dial == nil { // tests that replace the dialler connect to a local server on purpose
		d.Control = func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return ErrBadAddress
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || !Public(ip) {
				return fmt.Errorf("%w: it points inside a private network", ErrBadAddress)
			}
			return nil
		}
	}
	dial := p.dial
	if dial == nil {
		dial = func(ctx context.Context, d *net.Dialer, network, address string) (net.Conn, error) {
			return d.DialContext(ctx, network, address)
		}
	}
	tr := &http.Transport{
		Proxy:                 nil, // a proxy from the environment would be a way around the rules
		ResponseHeaderTimeout: headerTimeout,
		TLSHandshakeTimeout:   dialTimeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, ErrBadAddress
			}
			ips, err := addrsOf(ctx, host, resolve)
			if err != nil {
				return nil, err
			}
			var last error
			for _, ip := range ips {
				if !p.AllowPrivate && !Public(ip) {
					// one private answer among public ones is enough to refuse: the name is not trusted
					return nil, fmt.Errorf("%w: its name points inside a private network", ErrBadAddress)
				}
			}
			for _, ip := range ips {
				c, err := dial(ctx, d, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return c, nil
				}
				last = err
			}
			return nil, last
		},
	}
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func addrsOf(ctx context.Context, host string, resolve func(context.Context, string) ([]netip.Addr, error)) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	ips, err := resolve(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("cannot find that server: %w", err)
	}
	if len(ips) == 0 {
		return nil, errors.New("cannot find that server")
	}
	return ips, nil
}

// IsRedirect reports whether a response is one the app refuses to follow.
func IsRedirect(resp *http.Response) bool {
	return resp != nil && resp.StatusCode >= 300 && resp.StatusCode < 400
}

// RedirectTarget is where a redirect points, for the doctor's message.
func RedirectTarget(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	if loc, err := resp.Location(); err == nil {
		return loc.Scheme + "://" + loc.Host
	}
	return ""
}

// Settings are the administrator's switches for bridges.
type Settings struct {
	Enabled bool // BRIDGE_ENABLE: bridges to other servers exist at all (off by default)
	Policy       // BRIDGE_ALLOW_PRIVATE: private addresses and plain http:// (off by default)
}

// SettingsFrom reads the switches from the environment.
func SettingsFrom(env func(string) string) Settings {
	on := func(name string) bool {
		switch strings.ToLower(strings.TrimSpace(env(name))) {
		case "1", "true", "on", "yes":
			return true
		}
		return false
	}
	return Settings{Enabled: on("BRIDGE_ENABLE"), Policy: Policy{AllowPrivate: on("BRIDGE_ALLOW_PRIVATE")}}
}
