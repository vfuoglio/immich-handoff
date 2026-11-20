package dest

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/vfuoglio/immich-handoff/internal/bridge"
)

// CheckAddress says whether the network address in a person's destination is one the app may connect to, as far as can be told without
// connecting: a WebDAV address or an S3 endpoint must be an https address that passes the same rules as a bridge's (http, and an address
// on a private network, only when the administrator allows private addresses), and an SFTP host a name or address with a port. Every kind
// checks again when it connects, after the name has been resolved: this is the answer given while the person types.
func CheckAddress(s Spec, n Net) error {
	p := bridge.Policy{AllowPrivate: n.AllowPrivate}
	switch s.Kind {
	case "webdav":
		_, err := p.Address(s.Settings["url"])
		return err
	case "s3":
		if ep := strings.TrimSpace(s.Settings["endpoint"]); ep != "" {
			_, err := p.Address(ep)
			return err
		}
		return nil // Amazon's own
	case "sftp":
		host := strings.TrimSpace(s.Settings["host"])
		if host == "" || strings.ContainsAny(host, "/@ \t") {
			return errors.New("the host is a name or an address, without a scheme or a user")
		}
		if port := strings.TrimSpace(s.Settings["port"]); port != "" {
			if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
				return errors.New("the port is a number from 1 to 65535")
			}
		}
		if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil && !n.AllowPrivate && !bridge.Public(ip) {
			return fmt.Errorf("%s is an address on a private network, which the administrator has not allowed", host)
		}
		if _, _, err := net.SplitHostPort(host); err == nil {
			return errors.New("put the port in its own setting, not in the host")
		}
		return nil
	}
	return fmt.Errorf("the kind %q cannot be added by a person", s.Kind)
}
