// Package mailtest is a small IMAP server for tests: a mailbox that keeps what is delivered to it and answers the
// commands the client of package mailin sends. It is not part of the program.
package mailtest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Options say how the server behaves.
type Options struct {
	User, Password string
	Implicit       bool // TLS from the first byte
	StartTLS       bool // offers STARTTLS (and requires it before a login)
	Hang           bool // says hello and then nothing at all
	Garbage        bool // answers every command with nonsense
	Huge           uint32
}

// Msg is a message in the mailbox.
type Msg struct {
	UID     uint32
	Raw     []byte
	Seen    bool
	Deleted bool
}

// Server is the mailbox.
type Server struct {
	opts Options
	ln   net.Listener
	cert tls.Certificate
	pool *x509.CertPool

	mu       sync.Mutex
	msgs     []*Msg
	next     uint32
	logins   int
	badLogin int
	commands []string
}

// Start listens on a free port of the loopback address and stops when the test ends.
func Start(t testing.TB, o Options) *Server {
	t.Helper()
	s, err := Listen("127.0.0.1:0", o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Listen starts a server on an address, for tests that need a fixed one and for the demonstration environment.
func Listen(addr string, o Options) (*Server, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &Server{opts: o, ln: ln, cert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool: pool, next: 1}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s, nil
}

// Close stops listening.
func (s *Server) Close() { s.ln.Close() }

// Addr is where to connect.
func (s *Server) Addr() (host string, port int) {
	a := s.ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", a.Port
}

// TLSConfig is what a client needs to trust this server's certificate.
func (s *Server) TLSConfig() *tls.Config { return &tls.Config{RootCAs: s.pool} }

// Deliver puts a message in the mailbox and returns its unique id.
func (s *Server) Deliver(raw []byte) uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := &Msg{UID: s.next, Raw: append([]byte(nil), raw...)}
	s.next++
	s.msgs = append(s.msgs, m)
	return m.UID
}

// Messages are the messages still in the mailbox (those marked deleted are until an EXPUNGE).
func (s *Server) Messages() []Msg {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Msg, 0, len(s.msgs))
	for _, m := range s.msgs {
		out = append(out, *m)
	}
	return out
}

// SetPassword changes the password the server accepts, so a client that has the old one is refused from now on.
func (s *Server) SetPassword(p string) {
	s.mu.Lock()
	s.opts.Password = p
	s.mu.Unlock()
}

// Logins counts the successful and the refused logins.
func (s *Server) Logins() (ok, refused int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logins, s.badLogin
}

// Commands lists what clients sent, with passwords hidden.
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

var (
	uidFetchRE = regexp.MustCompile(`(?i)^UID FETCH (\d+) \((.+)\)$`)
	uidStoreRE = regexp.MustCompile(`(?i)^UID STORE (\d+) \+FLAGS(?:\.SILENT)? \((.*)\)$`)
	loginRE    = regexp.MustCompile(`(?i)^LOGIN "((?:[^"\\]|\\.)*)" "((?:[^"\\]|\\.)*)"$`)
)

func (s *Server) serve(raw net.Conn) {
	defer raw.Close()
	var c net.Conn = raw
	if s.opts.Implicit {
		c = tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{s.cert}})
	}
	r := bufio.NewReader(c)
	say := func(f string, a ...any) { fmt.Fprintf(c, f+"\r\n", a...) }
	say("* OK fake IMAP ready")
	if s.opts.Hang {
		time.Sleep(time.Minute)
		return
	}
	secure := s.opts.Implicit
	authed, selected := false, false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		tag, cmd, _ := strings.Cut(line, " ")
		s.mu.Lock()
		shown := cmd
		if loginRE.MatchString(cmd) {
			shown = "LOGIN <hidden>"
		}
		s.commands = append(s.commands, shown)
		s.mu.Unlock()
		if s.opts.Garbage {
			say("this is not IMAP at all {99999999999}")
			continue
		}
		up := strings.ToUpper(cmd)
		switch {
		case up == "CAPABILITY":
			caps := "IMAP4rev1"
			if s.opts.StartTLS && !secure {
				caps += " STARTTLS LOGINDISABLED"
			}
			say("* CAPABILITY %s", caps)
			say("%s OK done", tag)
		case up == "STARTTLS" && s.opts.StartTLS && !secure:
			say("%s OK begin TLS", tag)
			c = tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{s.cert}})
			r = bufio.NewReader(c)
			secure = true
		case loginRE.MatchString(cmd):
			m := loginRE.FindStringSubmatch(cmd)
			unq := func(x string) string { return strings.NewReplacer(`\\`, `\`, `\"`, `"`).Replace(x) }
			s.mu.Lock()
			good := secure || !s.opts.StartTLS
			good = good && unq(m[1]) == s.opts.User && unq(m[2]) == s.opts.Password
			if good {
				s.logins++
			} else {
				s.badLogin++
			}
			s.mu.Unlock()
			if good {
				authed = true
				say("%s OK logged in", tag)
			} else {
				say("%s NO [AUTHENTICATIONFAILED] wrong user or password", tag)
			}
		case !authed:
			say("%s BAD log in first", tag)
		case strings.HasPrefix(up, "SELECT "):
			selected = true
			s.mu.Lock()
			n := len(s.msgs)
			s.mu.Unlock()
			say("* %d EXISTS", n)
			say("%s OK [READ-WRITE] selected", tag)
		case !selected:
			say("%s BAD select a folder first", tag)
		case up == "UID SEARCH UNSEEN":
			s.mu.Lock()
			var ids []string
			for _, m := range s.msgs {
				if !m.Seen && !m.Deleted {
					ids = append(ids, strconv.Itoa(int(m.UID)))
				}
			}
			s.mu.Unlock()
			say("* SEARCH %s", strings.Join(ids, " "))
			say("%s OK done", tag)
		case uidFetchRE.MatchString(cmd):
			m := uidFetchRE.FindStringSubmatch(cmd)
			uid, _ := strconv.Atoi(m[1])
			s.mu.Lock()
			var msg *Msg
			for _, x := range s.msgs {
				if int(x.UID) == uid && !x.Deleted {
					msg = x
				}
			}
			s.mu.Unlock()
			if msg == nil {
				say("%s OK nothing", tag)
				continue
			}
			switch strings.ToUpper(m[2]) {
			case "RFC822.SIZE":
				size := len(msg.Raw)
				if s.opts.Huge > 0 {
					size = int(s.opts.Huge)
				}
				say("* 1 FETCH (UID %d RFC822.SIZE %d)", uid, size)
			case "BODY.PEEK[]":
				fmt.Fprintf(c, "* 1 FETCH (UID %d BODY[] {%d}\r\n", uid, len(msg.Raw))
				c.Write(msg.Raw)
				io.WriteString(c, ")\r\n")
			default:
				say("%s BAD unsupported", tag)
				continue
			}
			say("%s OK done", tag)
		case uidStoreRE.MatchString(cmd):
			m := uidStoreRE.FindStringSubmatch(cmd)
			uid, _ := strconv.Atoi(m[1])
			s.mu.Lock()
			for _, x := range s.msgs {
				if int(x.UID) == uid {
					if strings.Contains(m[2], `\Seen`) {
						x.Seen = true
					}
					if strings.Contains(m[2], `\Deleted`) {
						x.Deleted = true
					}
				}
			}
			s.mu.Unlock()
			say("%s OK stored", tag)
		case up == "EXPUNGE":
			s.mu.Lock()
			keep := s.msgs[:0]
			for _, x := range s.msgs {
				if !x.Deleted {
					keep = append(keep, x)
				}
			}
			s.msgs = keep
			s.mu.Unlock()
			say("%s OK expunged", tag)
		case up == "LOGOUT":
			say("* BYE")
			say("%s OK bye", tag)
			return
		default:
			say("%s BAD unknown command", tag)
		}
	}
}
