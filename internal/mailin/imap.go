package mailin

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Client is one connection to a mailbox. It is not safe for use by more than one goroutine, and it is made for one pass:
// connect, log in, look, mark, log out.
type Client struct {
	conn    net.Conn
	r       *bufio.Reader
	n       int
	timeout time.Duration
	max     int64
	tls     *tls.Config
}

// Errors the mailbox can answer with.
var (
	// ErrLogin: the server did not accept the user and password. The text never holds the password.
	ErrLogin = errors.New("the mail server did not accept the login")
	// ErrNoTLS: the server does not offer the upgrade to TLS that was asked for, so nothing is sent without protection.
	ErrNoTLS = errors.New("the mail server does not offer STARTTLS; refusing to log in without protection")
	// ErrProtocol: the server said something this client does not understand.
	ErrProtocol = errors.New("the mail server answered in a way this program does not understand")
)

// Dial connects to the mailbox and, as the settings say, protects the connection. tlsConfig is for tests (the server's
// certificate); nil means the system's.
func Dial(ctx context.Context, s Settings, tlsConfig *tls.Config) (*Client, error) {
	cfg := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	if tlsConfig != nil {
		cfg = tlsConfig.Clone()
		cfg.ServerName = s.Host
	}
	d := &net.Dialer{Timeout: 15 * time.Second}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	var conn net.Conn
	var err error
	if s.TLS == TLSImplicit {
		conn, err = (&tls.Dialer{NetDialer: d, Config: cfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("could not reach the mail server (%s)", rootCause(err))
	}
	c := &Client{conn: conn, r: bufio.NewReaderSize(conn, 64<<10), timeout: 60 * time.Second, max: s.MaxBytes, tls: cfg}
	if _, err := c.line(); err != nil { // the greeting
		conn.Close()
		return nil, err
	}
	if s.TLS == TLSStart {
		if err := c.startTLS(); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return c, nil
}

func rootCause(err error) string {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok || u.Unwrap() == nil {
			return err.Error()
		}
		err = u.Unwrap()
	}
}

func (c *Client) startTLS() error {
	caps, err := c.command("CAPABILITY")
	if err != nil {
		return err
	}
	has := false
	for _, l := range caps {
		if strings.Contains(strings.ToUpper(l.text), "STARTTLS") {
			has = true
		}
	}
	if !has {
		return ErrNoTLS
	}
	if _, err := c.command("STARTTLS"); err != nil {
		return err
	}
	t := tls.Client(c.conn, c.tls)
	_ = c.conn.SetDeadline(time.Now().Add(c.timeout))
	if err := t.Handshake(); err != nil {
		return fmt.Errorf("the mail server's certificate or TLS was not accepted (%s)", rootCause(err))
	}
	c.conn, c.r = t, bufio.NewReaderSize(t, 64<<10)
	return nil
}

// Close ends the connection politely, if it still works.
func (c *Client) Close() {
	if c == nil || c.conn == nil {
		return
	}
	_, _ = c.command("LOGOUT")
	c.conn.Close()
}

func (c *Client) deadline() { _ = c.conn.SetDeadline(time.Now().Add(c.timeout)) }

// line reads one line, without its line break. A line is never longer than 64 KB.
func (c *Client) line() (string, error) {
	c.deadline()
	var out []byte
	for {
		part, isPrefix, err := c.r.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", fmt.Errorf("the mail server closed the connection")
			}
			return "", fmt.Errorf("the mail connection failed (%s)", rootCause(err))
		}
		out = append(out, part...)
		if len(out) > 64<<10 {
			return "", ErrProtocol
		}
		if !isPrefix {
			return string(out), nil
		}
	}
}

// response is one untagged answer: its text, and the literals (the {n} blocks of bytes) that came inside it, in order.
type response struct {
	text     string
	literals [][]byte
}

var literalRE = regexp.MustCompile(`\{(\d+)\}$`)

// command sends one command and reads until its tagged answer. NO and BAD are errors (with the server's words, which
// hold no secret). An answer's literal is read only if it is within the limit.
func (c *Client) command(format string, args ...any) ([]response, error) {
	c.n++
	tag := fmt.Sprintf("a%03d", c.n)
	c.deadline()
	if _, err := io.WriteString(c.conn, tag+" "+fmt.Sprintf(format, args...)+"\r\n"); err != nil {
		return nil, fmt.Errorf("the mail connection failed (%s)", rootCause(err))
	}
	var out []response
	for {
		l, err := c.line()
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(l, tag+" ") {
			status, rest, _ := strings.Cut(l[len(tag)+1:], " ")
			switch strings.ToUpper(status) {
			case "OK":
				return out, nil
			case "NO", "BAD":
				return out, fmt.Errorf("the mail server refused a command (%s %s)", strings.ToUpper(status), sanitize(rest))
			}
			return out, ErrProtocol
		}
		resp := response{text: l}
		for {
			m := literalRE.FindStringSubmatch(resp.text)
			if m == nil {
				break
			}
			n, _ := strconv.ParseInt(m[1], 10, 64)
			if n < 0 || n > c.max+(1<<20) {
				return nil, ErrProtocol // a literal this big was not asked for
			}
			c.deadline()
			buf := make([]byte, n)
			if _, err := io.ReadFull(c.r, buf); err != nil {
				return nil, fmt.Errorf("the mail connection failed (%s)", rootCause(err))
			}
			resp.literals = append(resp.literals, buf)
			more, err := c.line() // the rest of the line after the literal
			if err != nil {
				return nil, err
			}
			resp.text = resp.text[:len(resp.text)-len(m[0])] + "<literal>" + more
		}
		out = append(out, resp)
	}
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 32 && r < 127 {
			b.WriteRune(r)
		}
	}
	if b.Len() > 80 {
		return b.String()[:80]
	}
	return b.String()
}

func quote(s string) (string, error) {
	if strings.ContainsAny(s, "\r\n\x00") {
		return "", errors.New("a line break in a name")
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`, nil
}

// Login logs in. The password is sent once and is never part of an error.
func (c *Client) Login(user, password string) error {
	u, err := quote(user)
	if err != nil {
		return ErrLogin
	}
	p, err := quote(password)
	if err != nil {
		return ErrLogin
	}
	if _, err := c.command("LOGIN %s %s", u, p); err != nil {
		return ErrLogin
	}
	return nil
}

// Select opens a folder.
func (c *Client) Select(folder string) error {
	q, err := quote(folder)
	if err != nil {
		return err
	}
	_, err = c.command("SELECT %s", q)
	return err
}

// Unseen lists the unique ids of the messages not yet read.
func (c *Client) Unseen() ([]uint32, error) {
	rs, err := c.command("UID SEARCH UNSEEN")
	if err != nil {
		return nil, err
	}
	var out []uint32
	for _, r := range rs {
		f := strings.Fields(r.text)
		if len(f) >= 2 && strings.EqualFold(f[1], "SEARCH") {
			for _, x := range f[2:] {
				if n, err := strconv.ParseUint(x, 10, 32); err == nil {
					out = append(out, uint32(n))
				}
			}
		}
	}
	return out, nil
}

var sizeRE = regexp.MustCompile(`(?i)RFC822\.SIZE (\d+)`)

// Size asks how large a message is without reading it.
func (c *Client) Size(uid uint32) (int64, error) {
	rs, err := c.command("UID FETCH %d (RFC822.SIZE)", uid)
	if err != nil {
		return 0, err
	}
	for _, r := range rs {
		if m := sizeRE.FindStringSubmatch(r.text); m != nil {
			n, _ := strconv.ParseInt(m[1], 10, 64)
			return n, nil
		}
	}
	return 0, ErrProtocol
}

// Fetch reads a whole message without marking it read (BODY.PEEK). The caller has checked Size.
func (c *Client) Fetch(uid uint32) ([]byte, error) {
	rs, err := c.command("UID FETCH %d (BODY.PEEK[])", uid)
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		if len(r.literals) > 0 {
			return r.literals[0], nil
		}
	}
	return nil, ErrProtocol
}

// MarkSeen marks a message read.
func (c *Client) MarkSeen(uid uint32) error {
	_, err := c.command(`UID STORE %d +FLAGS.SILENT (\Seen)`, uid)
	return err
}

// MarkDeleted marks a message for removal; Expunge removes what is marked.
func (c *Client) MarkDeleted(uid uint32) error {
	_, err := c.command(`UID STORE %d +FLAGS.SILENT (\Deleted \Seen)`, uid)
	return err
}

// Expunge removes the messages marked for removal.
func (c *Client) Expunge() error {
	_, err := c.command("EXPUNGE")
	return err
}
