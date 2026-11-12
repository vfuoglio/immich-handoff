// Package mailin reads a mailbox over IMAP and picks the photos and videos out of the messages in it: the way in for
// "send photos to this address". It speaks only the few IMAP commands it needs, with the standard library, and knows
// nothing else of the app.
package mailin

import (
	"fmt"
	"net"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"
)

// How the connection to the mail server is protected.
const (
	TLSImplicit = "tls"      // TLS from the first byte (port 993)
	TLSStart    = "starttls" // plain start, then an upgrade that must succeed (port 143)
	TLSNone     = "none"     // no protection: only for a mail server on this machine (loopback)
)

// After says what happens to a message once it has been handled.
const (
	AfterDelete = "delete" // removed from the mailbox
	AfterSeen   = "seen"   // kept, marked read
)

// Settings is how to reach the mailbox and what it is for.
type Settings struct {
	Host     string
	Port     int
	TLS      string
	User     string
	Password string
	Address  string        // the address of the mailbox, e.g. drop@example.org; a box's address is drop+<code>@example.org
	Folder   string        // INBOX
	After    string        // delete or seen
	Every    time.Duration // how often the mailbox is looked at
	MaxBytes int64         // the largest message that is read; a bigger one is left unread and counts as handled
}

// SettingsFrom reads the settings from the environment. configured is false when MAIL_IN_HOST is not set: the way in is
// off, and the other variables are not looked at. Secrets may also come from a file (MAIL_IN_PASSWORD_FILE).
func SettingsFrom(getenv func(string) string) (s Settings, configured bool, err error) {
	s.Host = strings.TrimSpace(getenv("MAIL_IN_HOST"))
	if s.Host == "" {
		return s, false, nil
	}
	bad := func(format string, a ...any) (Settings, bool, error) {
		return Settings{}, true, fmt.Errorf("MAIL_IN: "+format, a...)
	}
	if strings.ContainsAny(s.Host, " /\r\n") {
		return bad("MAIL_IN_HOST is not a host name")
	}
	s.TLS = strings.ToLower(strings.TrimSpace(getenv("MAIL_IN_TLS")))
	switch s.TLS {
	case "":
		s.TLS = TLSImplicit
	case TLSImplicit, TLSStart:
	case TLSNone:
		if !isLoopback(s.Host) {
			return bad("MAIL_IN_TLS=none is only for a mail server on this machine; use tls or starttls")
		}
	default:
		return bad("MAIL_IN_TLS %q is not %s, %s or %s", s.TLS, TLSImplicit, TLSStart, TLSNone)
	}
	s.Port = map[string]int{TLSImplicit: 993, TLSStart: 143, TLSNone: 143}[s.TLS]
	if v := strings.TrimSpace(getenv("MAIL_IN_PORT")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return bad("MAIL_IN_PORT %q is not a port", v)
		}
		s.Port = n
	}
	s.User = getenv("MAIL_IN_USER")
	s.Password = getenv("MAIL_IN_PASSWORD")
	if f := getenv("MAIL_IN_PASSWORD_FILE"); f != "" && s.Password == "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return bad("MAIL_IN_PASSWORD_FILE: %v", err)
		}
		s.Password = strings.TrimRight(string(b), "\r\n")
	}
	if s.User == "" || s.Password == "" {
		return bad("MAIL_IN_USER and MAIL_IN_PASSWORD are both needed")
	}
	if strings.ContainsAny(s.User+s.Password, "\r\n\x00") {
		return bad("the user and the password cannot contain line breaks")
	}
	a, perr := mail.ParseAddress(strings.TrimSpace(getenv("MAIL_IN_ADDRESS")))
	if perr != nil || strings.Count(a.Address, "@") != 1 || strings.Contains(a.Address, "+") {
		return bad("MAIL_IN_ADDRESS must be the mailbox's address without a plus sign, such as drop@example.org")
	}
	s.Address = strings.ToLower(a.Address)
	s.Folder = strings.TrimSpace(getenv("MAIL_IN_FOLDER"))
	if s.Folder == "" {
		s.Folder = "INBOX"
	}
	if strings.ContainsAny(s.Folder, "\r\n\x00\"\\") {
		return bad("MAIL_IN_FOLDER is not a folder name")
	}
	switch strings.ToLower(strings.TrimSpace(getenv("MAIL_IN_AFTER"))) {
	case "", AfterDelete:
		s.After = AfterDelete
	case AfterSeen:
		s.After = AfterSeen
	default:
		return bad("MAIL_IN_AFTER is %s or %s", AfterDelete, AfterSeen)
	}
	s.Every = time.Minute
	if v := strings.TrimSpace(getenv("MAIL_IN_EVERY")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 5*time.Second || d > time.Hour {
			return bad("MAIL_IN_EVERY must be a time between 5s and 1h, such as 1m")
		}
		s.Every = d
	}
	mb := 25
	if v := strings.TrimSpace(getenv("MAIL_IN_MAX_MB")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			return bad("MAIL_IN_MAX_MB must be a whole number from 1 to 200")
		}
		mb = n
	}
	s.MaxBytes = int64(mb) << 20
	return s, true, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// AddressFor is the address of a box: the mailbox's local part, a plus sign and the box's code, at the mailbox's domain.
func (s Settings) AddressFor(code string) string {
	local, domain, _ := strings.Cut(s.Address, "@")
	return local + "+" + code + "@" + domain
}
