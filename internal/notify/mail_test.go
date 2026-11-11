package notify

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type received struct {
	from string
	rcpt []string
	data string
	auth string
}

// fakeSMTP is a small mail server that keeps what it is given.
type fakeSMTP struct {
	ln       net.Listener
	cfg      *tls.Config
	implicit bool // TLS from the first byte
	startTLS bool // offers STARTTLS
	dataFail func(call int) string

	mu    sync.Mutex
	mails []received
	calls int
}

func tlsPair(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return &tls.Config{Certificates: srv.TLS.Certificates}, pool
}

func newFakeSMTP(t *testing.T, cfg *tls.Config, implicit, startTLS bool) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, cfg: cfg, implicit: implicit, startTLS: startTLS}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	if f.implicit {
		c = tls.Server(c, f.cfg)
	}
	r := bufio.NewReader(c)
	say := func(s string) { io.WriteString(c, s+"\r\n") }
	say("220 fake ESMTP")
	var cur received
	secure := f.implicit
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "EHLO"), strings.HasPrefix(up, "HELO"):
			io.WriteString(c, "250-fake\r\n")
			if f.startTLS && !secure {
				io.WriteString(c, "250-STARTTLS\r\n")
			}
			say("250 AUTH PLAIN")
		case up == "STARTTLS":
			say("220 go ahead")
			c = tls.Server(c, f.cfg)
			r = bufio.NewReader(c)
			secure = true
		case strings.HasPrefix(up, "AUTH PLAIN"):
			b, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH PLAIN"):]))
			cur.auth = strings.ReplaceAll(string(b), "\x00", "|")
			say("235 ok")
		case strings.HasPrefix(up, "MAIL FROM:"):
			cur.from = strings.Trim(line[len("MAIL FROM:"):], "<> ")
			say("250 ok")
		case strings.HasPrefix(up, "RCPT TO:"):
			cur.rcpt = append(cur.rcpt, strings.Trim(line[len("RCPT TO:"):], "<> "))
			say("250 ok")
		case up == "DATA":
			say("354 go on")
			var buf bytes.Buffer
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				buf.WriteString(strings.TrimPrefix(l, "."))
			}
			f.mu.Lock()
			f.calls++
			call := f.calls
			f.mu.Unlock()
			if f.dataFail != nil {
				if reply := f.dataFail(call); reply != "" {
					say(reply)
					cur = received{auth: cur.auth}
					continue
				}
			}
			cur.data = buf.String()
			f.mu.Lock()
			f.mails = append(f.mails, cur)
			f.mu.Unlock()
			say("250 queued")
			cur = received{auth: cur.auth}
		case up == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func (f *fakeSMTP) count() int     { f.mu.Lock(); defer f.mu.Unlock(); return len(f.mails) }
func (f *fakeSMTP) dataCalls() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func (f *fakeSMTP) wait(t *testing.T, n int) []received {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f.count() >= n {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.mails) < n {
		t.Fatalf("%d mails arrived, wanted %d", len(f.mails), n)
	}
	return append([]received(nil), f.mails...)
}

type parsedMail struct {
	header     mail.Header
	text, html string
}

func parseReceived(t *testing.T, r received) parsedMail {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(r.data))
	if err != nil {
		t.Fatal(err, r.data)
	}
	typ, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || typ != "multipart/alternative" {
		t.Fatalf("content type %q %v", typ, err)
	}
	p := parsedMail{header: m.Header}
	mr := multipart.NewReader(m.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(quotedprintable.NewReader(part))
		switch ct, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type")); ct {
		case "text/plain":
			p.text = string(b)
		case "text/html":
			p.html = string(b)
		}
	}
	return p
}

func mailDest(f *fakeSMTP, mode string, group time.Duration) Destination {
	return Destination{Name: "mail", Format: FormatEmail, Mail: &Mail{Host: "127.0.0.1", Port: f.port(), TLS: mode,
		From: "Handoff <handoff@example.com>", To: []string{"admin@example.com", "Second <second@example.com>"}, Group: group}}
}

func newMailNotifier(t *testing.T, pool *x509.CertPool, log *bytes.Buffer, dests ...Destination) *Notifier {
	t.Helper()
	n := quick(New(dests, "https://app.example", logTo(log)))
	n.tlsBase = &tls.Config{RootCAs: pool}
	return n
}

func TestAMessageArrivesAsMailWithBothPartsAndALink(t *testing.T) {
	cfg, pool := tlsPair(t)
	f := newFakeSMTP(t, cfg, true, false)
	var log bytes.Buffer
	n := newMailNotifier(t, pool, &log, mailDest(f, TLSImplicit, 0))
	n.Send(Message{Kind: Problem, Text: "a key stopped working"})
	got := f.wait(t, 1)[0]
	n.Close()
	if got.from != "handoff@example.com" || len(got.rcpt) != 2 || got.rcpt[1] != "second@example.com" {
		t.Errorf("envelope %+v", got)
	}
	p := parseReceived(t, got)
	subj, _ := new(mime.WordDecoder).DecodeHeader(p.header.Get("Subject"))
	if subj != "immich-handoff: needs attention" || p.header.Get("Auto-Submitted") != "auto-generated" || p.header.Get("Message-ID") == "" {
		t.Errorf("headers %v", p.header)
	}
	if !strings.Contains(p.text, "a key stopped working") || !strings.Contains(p.text, "https://app.example/admin") {
		t.Errorf("text %q", p.text)
	}
	if !strings.Contains(p.html, "a key stopped working") || !strings.Contains(p.html, `href="https://app.example/admin"`) {
		t.Errorf("html %q", p.html)
	}
}

func TestStartTLSIsUsedAndTheLoginIsSent(t *testing.T) {
	cfg, pool := tlsPair(t)
	f := newFakeSMTP(t, cfg, false, true)
	var log bytes.Buffer
	d := mailDest(f, TLSStart, 0)
	d.Mail.User, d.Mail.Password = "robot", "s3cret"
	n := newMailNotifier(t, pool, &log, d)
	n.Send(Message{Kind: Info, Text: "hello"})
	got := f.wait(t, 1)[0]
	n.Close()
	if got.auth != "|robot|s3cret" {
		t.Errorf("login %q", got.auth)
	}
	if strings.Contains(log.String(), "s3cret") || strings.Contains(log.String(), "example.com") {
		t.Errorf("log: %s", log.String())
	}
}

func TestWithoutStartTLSNothingIsSent(t *testing.T) {
	cfg, pool := tlsPair(t)
	f := newFakeSMTP(t, cfg, false, false) // offers no STARTTLS
	var log bytes.Buffer
	d := mailDest(f, TLSStart, 0)
	d.Mail.User, d.Mail.Password = "robot", "s3cret"
	n := newMailNotifier(t, pool, &log, d)
	n.Send(Message{Kind: Info, Text: "hello"})
	n.Close()
	time.Sleep(100 * time.Millisecond)
	if f.count() != 0 || !strings.Contains(log.String(), "STARTTLS") || strings.Contains(log.String(), "s3cret") {
		t.Errorf("mails %d, log %s", f.count(), log.String())
	}
}

func TestMessagesInOneWindowBecomeOneMailNamingTheWorstKind(t *testing.T) {
	f := newFakeSMTP(t, nil, false, false)
	var log bytes.Buffer
	n := newMailNotifier(t, nil, &log, mailDest(f, TLSNone, 150*time.Millisecond))
	n.Send(Message{Kind: Info, Text: "first"})
	n.Send(Message{Kind: Problem, Text: "second"})
	n.Send(Message{Kind: Backup, Text: "third"})
	got := f.wait(t, 1)
	time.Sleep(250 * time.Millisecond)
	if f.count() != 1 {
		t.Fatalf("%d mails, wanted one", f.count())
	}
	p := parseReceived(t, got[0])
	subj, _ := new(mime.WordDecoder).DecodeHeader(p.header.Get("Subject"))
	if subj != "immich-handoff: needs attention (+2)" {
		t.Errorf("subject %q", subj)
	}
	for _, want := range []string{"3 things happened", "first", "second", "third"} {
		if !strings.Contains(p.text, want) {
			t.Errorf("text lacks %q: %s", want, p.text)
		}
	}
	n.Close()
}

func TestAGroupIsCappedAndSaysHowManyMore(t *testing.T) {
	f := newFakeSMTP(t, nil, false, false)
	var log bytes.Buffer
	n := newMailNotifier(t, nil, &log, mailDest(f, TLSNone, 200*time.Millisecond))
	for i := 0; i < maxLines+7; i++ {
		n.Send(Message{Kind: Info, Text: "line " + strconv.Itoa(i)})
	}
	p := parseReceived(t, f.wait(t, 1)[0])
	if !strings.Contains(p.text, "and 7 more") || strings.Contains(p.text, "line "+strconv.Itoa(maxLines+3)) {
		t.Errorf("text: %s", p.text)
	}
	n.Close()
}

func TestWithoutAGroupEveryMessageIsItsOwnMail(t *testing.T) {
	f := newFakeSMTP(t, nil, false, false)
	var log bytes.Buffer
	n := newMailNotifier(t, nil, &log, mailDest(f, TLSNone, 0))
	for i := 0; i < 3; i++ {
		n.Send(Message{Kind: Info, Text: "m" + strconv.Itoa(i)})
	}
	f.wait(t, 3)
	n.Close()
}

func TestMailWaitingForCompanyGoesWhenTheAppStops(t *testing.T) {
	f := newFakeSMTP(t, nil, false, false)
	var log bytes.Buffer
	n := newMailNotifier(t, nil, &log, mailDest(f, TLSNone, time.Hour))
	n.Send(Message{Kind: Info, Text: "last words"})
	time.Sleep(50 * time.Millisecond)
	if f.count() != 0 {
		t.Fatal("sent before the window ended")
	}
	n.Close()
	f.wait(t, 1)
}

func TestTextIsEscapedInHTMLAndCannotMakeAHeader(t *testing.T) {
	f := newFakeSMTP(t, nil, false, false)
	var log bytes.Buffer
	n := newMailNotifier(t, nil, &log, mailDest(f, TLSNone, 0))
	n.Send(Message{Kind: Info, Text: "<script>alert(1)</script>\r\nBcc: evil@example.com\r\nSubject: hijacked"})
	p := parseReceived(t, f.wait(t, 1)[0])
	n.Close()
	if strings.Contains(p.html, "<script>") || !strings.Contains(p.html, "&lt;script&gt;") {
		t.Errorf("html: %s", p.html)
	}
	if p.header.Get("Bcc") != "" || strings.Contains(p.header.Get("Subject"), "hijacked") {
		t.Errorf("headers: %v", p.header)
	}
}

func TestOnlyTheKindsAskedForAreMailed(t *testing.T) {
	f := newFakeSMTP(t, nil, false, false)
	var log bytes.Buffer
	d := mailDest(f, TLSNone, 0)
	d.Kinds = map[Kind]bool{Backup: true}
	n := newMailNotifier(t, nil, &log, d)
	n.Send(Message{Kind: Info, Text: "no"})
	n.Send(Message{Kind: Backup, Text: "yes"})
	f.wait(t, 1)
	n.Close()
	time.Sleep(50 * time.Millisecond)
	if f.count() != 1 {
		t.Errorf("%d mails", f.count())
	}
}

func TestATemporaryReplyIsTriedAgainAndAPermanentOneIsNot(t *testing.T) {
	f := newFakeSMTP(t, nil, false, false)
	f.dataFail = func(call int) string {
		if call == 1 {
			return "451 try later"
		}
		return ""
	}
	var log bytes.Buffer
	n := newMailNotifier(t, nil, &log, mailDest(f, TLSNone, 0))
	n.Send(Message{Kind: Info, Text: "x"})
	f.wait(t, 1)
	n.Close()
	if f.dataCalls() != 2 || strings.Contains(log.String(), "failed") {
		t.Errorf("calls %d, log %s", f.dataCalls(), log.String())
	}

	g := newFakeSMTP(t, nil, false, false)
	g.dataFail = func(int) string { return "550 no such mailbox admin@example.com" }
	log.Reset()
	n = newMailNotifier(t, nil, &log, mailDest(g, TLSNone, 0))
	n.Send(Message{Kind: Info, Text: "x"})
	n.Close()
	time.Sleep(100 * time.Millisecond)
	if g.dataCalls() != 1 || !strings.Contains(log.String(), "answered 550") || strings.Contains(log.String(), "admin@example.com") {
		t.Errorf("calls %d, log %s", g.dataCalls(), log.String())
	}
}

func TestAMailServerThatCannotBeReachedIsLoggedWithoutItsAddress(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	var log bytes.Buffer
	d := Destination{Name: "mail", Format: FormatEmail, Mail: &Mail{Host: "127.0.0.1", Port: port, TLS: TLSNone, From: "a@example.com", To: []string{"b@example.com"}}}
	n := quick(New([]Destination{d}, "", logTo(&log)))
	n.Send(Message{Kind: Info, Text: "x"})
	n.Close()
	if !strings.Contains(log.String(), "notification failed") || strings.Contains(log.String(), "127.0.0.1") || strings.Contains(log.String(), "b@example.com") {
		t.Errorf("log: %s", log.String())
	}
}

func TestTheMailSettingsAreChecked(t *testing.T) {
	p := writeFile(t, `destinations:
  - name: ok
    format: email
    host: smtp.example.com
    from: Handoff <h@example.com>
    to: [admin@example.com]
    user: robot
    password: ${PW}
  - name: defaults
    format: email
    host: smtp.example.com
    tls: tls
    from: h@example.com
    to: [a@example.com]
    group: 0
`)
	d, err := FromEnv(env(map[string]string{"NOTIFY_FILE": p, "PW": "x"}))
	if err != nil || len(d) != 2 {
		t.Fatalf("%+v %v", d, err)
	}
	if m := d[0].Mail; m.Port != 587 || m.TLS != TLSStart || m.Group != DefaultGroup || m.Password != "x" {
		t.Errorf("%+v", m)
	}
	if m := d[1].Mail; m.Port != 465 || m.Group != 0 {
		t.Errorf("%+v", m)
	}

	bad := writeFile(t, `destinations:
  - name: a
    format: email
    url: https://x.example
    tls: maybe
    from: nonsense
    to: []
  - name: b
    format: email
    host: smtp.example.com
    tls: none
    user: robot
    password: pw
    from: h@example.com
    to: ["x@example.com\r\nBcc: y@example.com"]
    group: soon
  - name: c
    format: email
    host: smtp.example.com
    user: robot
    from: h@example.com
    to: [x@example.com]
    port: 70000
    group: 3h
`)
	_, err = FromEnv(env(map[string]string{"NOTIFY_FILE": bad}))
	if err == nil {
		t.Fatal("accepted")
	}
	for _, want := range []string{"has no url", `tls "maybe"`, "from is not an email", "at least one address", "not sent without protection",
		"is not an email address", `group "soon"`, "user and password go together", "70000 is not a port", `group "3h"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}
