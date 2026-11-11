package notify

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FormatEmail sends the messages as mail through an SMTP server.
const FormatEmail = "email"

// How the connection to the mail server is protected.
const (
	TLSImplicit = "tls"      // TLS from the first byte (port 465)
	TLSStart    = "starttls" // plain start, then an upgrade that must succeed (port 587)
	TLSNone     = "none"     // no protection: only for a relay on a trusted network, and never with a password
)

// DefaultGroup is how long messages for one mail destination wait for company before they go out together.
const DefaultGroup = 2 * time.Minute

// DefaultPeopleGroup is how long what happens to one person waits for company before it is mailed to them.
const DefaultPeopleGroup = 5 * time.Minute

// maxLines is how many lines one grouped mail lists before it says how many more there were.
const maxLines = 50

// Mail is how to reach a mail server, and who to write to.
type Mail struct {
	Host     string
	Port     int
	TLS      string
	User     string
	Password string
	From     string
	To       []string
	Group    time.Duration // 0 sends one mail per message
	People   bool          // this server is the one used to write to the people taking part; it takes no administrator messages
}

type stamped struct {
	at  time.Time
	msg Message
}

// mailer collects the messages of one mail destination and sends them as one mail when the window ends.
type mailer struct {
	n *Notifier
	d Destination

	mu      sync.Mutex
	pending []stamped
	timer   *time.Timer

	sendMu sync.Mutex // one mail at a time, so a flush at shutdown waits for one in flight
}

func (m *mailer) add(msg Message) {
	m.mu.Lock()
	m.pending = append(m.pending, stamped{time.Now(), msg})
	start := m.d.Mail.Group > 0 && m.timer == nil
	if start {
		m.timer = time.AfterFunc(m.d.Mail.Group, m.flush)
	}
	m.mu.Unlock()
	if m.d.Mail.Group <= 0 {
		go m.flush()
	}
}

func (m *mailer) flush() {
	m.sendMu.Lock()
	defer m.sendMu.Unlock()
	m.mu.Lock()
	batch := m.pending
	if m.d.Mail.Group <= 0 && len(batch) > 1 {
		batch = batch[:1] // no grouping: each message is its own mail (each add starts its own flush)
		m.pending = m.pending[1:]
	} else {
		m.pending = nil
	}
	m.timer = nil
	m.mu.Unlock()
	if len(batch) > 0 {
		m.n.sendMail(m.d, batch)
	}
}

// flushNow sends what is waiting without waiting for the window (at shutdown).
func (m *mailer) flushNow() {
	m.mu.Lock()
	if m.timer != nil {
		m.timer.Stop()
	}
	m.mu.Unlock()
	m.flush()
}

func (n *Notifier) sendMail(d Destination, batch []stamped) {
	body, err := n.buildMail(d, batch)
	if err != nil {
		n.log.Warn("notification failed", "destination", d.Name, "err", err)
		return
	}
	for attempt := 0; ; attempt++ {
		retry, err := n.smtpSend(d.Mail, body)
		if err == nil {
			return
		}
		if !retry || attempt >= len(n.wait) {
			n.log.Warn("notification failed", "destination", d.Name, "err", err)
			return
		}
		time.Sleep(n.wait[attempt])
	}
}

// smtpSend makes one attempt. retry says whether trying again could help.
func (n *Notifier) smtpSend(c *Mail, msg []byte) (retry bool, err error) {
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	cfg := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
	if n.tlsBase != nil {
		cfg = n.tlsBase.Clone()
		cfg.ServerName = c.Host
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	if c.TLS == TLSImplicit {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, cfg)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return true, fmt.Errorf("could not reach the mail server (%s)", reason(err))
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return true, mailErr(err)
	}
	defer cl.Close()
	if c.TLS == TLSStart {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return false, errors.New("the mail server does not offer STARTTLS; refusing to send without protection")
		}
		if err := cl.StartTLS(cfg); err != nil {
			return true, mailErr(err)
		}
	}
	if c.User != "" {
		if err := cl.Auth(smtp.PlainAuth("", c.User, c.Password, c.Host)); err != nil {
			return isTemporary(err), mailErr(err)
		}
	}
	from, _ := mail.ParseAddress(c.From)
	if err := cl.Mail(from.Address); err != nil {
		return isTemporary(err), mailErr(err)
	}
	for _, to := range c.To {
		a, _ := mail.ParseAddress(to)
		if err := cl.Rcpt(a.Address); err != nil {
			return isTemporary(err), mailErr(err)
		}
	}
	w, err := cl.Data()
	if err != nil {
		return isTemporary(err), mailErr(err)
	}
	if _, err := w.Write(msg); err != nil {
		return true, mailErr(err)
	}
	if err := w.Close(); err != nil {
		return isTemporary(err), mailErr(err)
	}
	_ = cl.Quit()
	return false, nil
}

// isTemporary is true for a 4xx reply (try again later) and for anything that was not a reply at all.
func isTemporary(err error) bool {
	var te *textproto.Error
	if errors.As(err, &te) {
		return te.Code >= 400 && te.Code < 500
	}
	return true
}

// mailErr says what went wrong without repeating addresses or the server's wording.
func mailErr(err error) error {
	var te *textproto.Error
	if errors.As(err, &te) {
		return fmt.Errorf("the mail server answered %d", te.Code)
	}
	return fmt.Errorf("the mail connection failed (%s)", reason(err))
}

var serious = map[Kind]int{Problem: 5, Bridge: 4, Backup: 3, DropBox: 2, Info: 1}

var htmlMail = template.Must(template.New("mail").Parse(`<!doctype html>
<html><body style="margin:0;padding:24px;background:#f4f3fa;font-family:-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#1b1936">
<div style="max-width:560px;margin:0 auto;background:#ffffff;border-radius:12px;padding:24px">
<h1 style="font-size:18px;margin:0 0 16px">{{.Title}}</h1>
{{range .Lines}}<p style="margin:0 0 12px;line-height:1.45"><span style="color:#6b6894;font-size:12px">{{.Time}} &middot; {{.Kind}}</span><br>{{.Text}}</p>
{{end}}{{if .More}}<p style="margin:0 0 12px;color:#6b6894">and {{.More}} more.</p>{{end}}
{{if .Link}}<p style="margin:20px 0 0"><a href="{{.Link}}" style="display:inline-block;background:#5b4bdb;color:#ffffff;text-decoration:none;padding:10px 18px;border-radius:8px">Open the page</a></p>{{end}}
<p style="margin:24px 0 0;color:#6b6894;font-size:12px">Sent by immich-handoff.</p>
</div></body></html>
`))

type htmlLine struct{ Time, Kind, Text string }

// buildMail makes the whole message: headers, a text part and an HTML part.
func (n *Notifier) buildMail(d Destination, batch []stamped) ([]byte, error) {
	worst := batch[0].msg
	for _, b := range batch[1:] {
		if serious[b.msg.Kind] > serious[worst.Kind] {
			worst = b.msg
		}
	}
	subject := title(worst.Kind)
	if len(batch) > 1 {
		subject += fmt.Sprintf(" (+%d)", len(batch)-1)
	}
	link := n.linkFor(worst)

	shown := batch
	more := 0
	if len(shown) > maxLines {
		shown, more = batch[:maxLines], len(batch)-maxLines
	}
	var text strings.Builder
	var lines []htmlLine
	if len(batch) == 1 {
		text.WriteString(batch[0].msg.Text + "\n")
		lines = append(lines, htmlLine{batch[0].at.Format("15:04"), string(batch[0].msg.Kind), batch[0].msg.Text})
	} else {
		fmt.Fprintf(&text, "%d things happened:\n\n", len(batch))
		for _, b := range shown {
			fmt.Fprintf(&text, "%s  [%s]  %s\n", b.at.Format("15:04"), b.msg.Kind, b.msg.Text)
			lines = append(lines, htmlLine{b.at.Format("15:04"), string(b.msg.Kind), b.msg.Text})
		}
		if more > 0 {
			fmt.Fprintf(&text, "... and %d more.\n", more)
		}
	}
	if link != "" {
		fmt.Fprintf(&text, "\nOpen the page: %s\n", link)
	}
	text.WriteString("\n-- \nSent by immich-handoff.\n")

	var htmlBody bytes.Buffer
	if err := htmlMail.Execute(&htmlBody, map[string]any{"Title": subject, "Lines": lines, "More": more, "Link": link}); err != nil {
		return nil, err
	}

	return assemble(d.Mail.From, d.Mail.To, subject, text.String(), htmlBody.String(), nil)
}

// assemble writes the whole message: headers, a text part and an HTML part. extra are more headers, in order.
func assemble(from string, to []string, subject, text, html string, extra [][2]string) ([]byte, error) {
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	boundary := hex.EncodeToString(id) + "handoff"
	domain := "localhost"
	if a, err := mail.ParseAddress(from); err == nil {
		if i := strings.LastIndex(a.Address, "@"); i >= 0 {
			domain = a.Address[i+1:]
		}
	}
	var out bytes.Buffer
	h := func(k, v string) { out.WriteString(k + ": " + v + "\r\n") }
	h("From", from)
	h("To", strings.Join(to, ", "))
	h("Subject", mime.QEncoding.Encode("utf-8", subject))
	h("Date", time.Now().Format(time.RFC1123Z))
	h("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	h("MIME-Version", "1.0")
	h("Auto-Submitted", "auto-generated")
	for _, e := range extra {
		if strings.ContainsAny(e[0]+e[1], "\r\n") {
			return nil, errors.New("a header cannot hold a line break")
		}
		h(e[0], e[1])
	}
	h("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	out.WriteString("\r\n")
	mw := multipart.NewWriter(&out)
	if err := mw.SetBoundary(boundary); err != nil {
		return nil, err
	}
	for _, part := range []struct{ typ, body string }{{"text/plain", text}, {"text/html", html}} {
		pw, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.typ + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qw := quotedprintable.NewWriter(pw)
		if _, err := qw.Write([]byte(part.body)); err != nil {
			return nil, err
		}
		if err := qw.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// PersonalMail is a mail to one person, written by the caller (in the person's language).
type PersonalMail struct {
	To, Name      string
	Subject, Text string
	HTML          string
	Headers       map[string]string // more headers, such as List-Unsubscribe
}

// PeopleMailReady reports whether a mail server is set aside for writing to the people taking part.
func (n *Notifier) PeopleMailReady() bool { return n.peopleMail() != nil }

// PeopleGroup is how long lines wait for company before they are mailed to one person.
func (n *Notifier) PeopleGroup() time.Duration {
	if d := n.peopleMail(); d != nil {
		return d.Mail.Group
	}
	return DefaultPeopleGroup
}

func (n *Notifier) peopleMail() *Destination {
	if n == nil {
		return nil
	}
	for i := range n.dests {
		if n.dests[i].Mail != nil && n.dests[i].Mail.People {
			return &n.dests[i]
		}
	}
	return nil
}

// SendPersonal sends one mail to one person now (it waits for the server and tries again as other mail does) and
// says whether it worked. The caller decides when and how often.
func (n *Notifier) SendPersonal(p PersonalMail) error {
	d := n.peopleMail()
	if d == nil {
		return errors.New("no mail server is set up for the people taking part")
	}
	rcpt := mail.Address{Name: p.Name, Address: p.To}
	if a, err := mail.ParseAddress(p.To); err != nil || strings.ContainsAny(p.To+p.Name, "\r\n") {
		return errors.New("that is not an email address")
	} else {
		rcpt.Address = a.Address
	}
	var extra [][2]string
	for k, v := range p.Headers {
		extra = append(extra, [2]string{k, v})
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i][0] < extra[j][0] })
	body, err := assemble(d.Mail.From, []string{rcpt.String()}, p.Subject, p.Text, p.HTML, extra)
	if err != nil {
		return err
	}
	c := *d.Mail
	c.To = []string{rcpt.Address}
	var last error
	for attempt := 0; ; attempt++ {
		retry, err := n.smtpSend(&c, body)
		if err == nil {
			return nil
		}
		last = err
		if !retry || attempt >= len(n.wait) {
			return last
		}
		time.Sleep(n.wait[attempt])
	}
}

// parseMail checks the settings of a mail destination and fills in the defaults.
func parseMail(name string, fd fileDest) (*Mail, []string) {
	var problems []string
	bad := func(format string, a ...any) {
		problems = append(problems, "destination "+name+": "+fmt.Sprintf(format, a...))
	}
	m := &Mail{Host: fd.Host, Port: fd.Port, TLS: fd.TLS, User: fd.User, Password: fd.Password, From: fd.From, To: fd.To, People: fd.People}
	if fd.URL != "" {
		bad("a mail destination has no url; use host, port and from")
	}
	if m.Host == "" || strings.ContainsAny(m.Host, " /\r\n") {
		bad("host is missing or not a host name")
	}
	switch m.TLS {
	case "":
		m.TLS = TLSStart
	case TLSImplicit, TLSStart, TLSNone:
	default:
		bad("tls %q is not %s, %s or %s", m.TLS, TLSImplicit, TLSStart, TLSNone)
	}
	if m.Port == 0 {
		m.Port = map[string]int{TLSImplicit: 465, TLSStart: 587, TLSNone: 25}[m.TLS]
	}
	if m.Port < 1 || m.Port > 65535 {
		bad("port %d is not a port", m.Port)
	}
	if m.TLS == TLSNone && (m.User != "" || m.Password != "") {
		bad("a password is not sent without protection; use tls or starttls")
	}
	if (m.User == "") != (m.Password == "") {
		bad("user and password go together")
	}
	if a, err := mail.ParseAddress(m.From); err != nil || strings.ContainsAny(m.From, "\r\n") || a.Address == "" {
		bad("from is not an email address")
	}
	if len(m.To) == 0 && !m.People {
		bad("to needs at least one address")
	}
	if m.People && (len(m.To) > 0 || len(fd.Kinds) > 0) {
		bad("a server for the people taking part writes to them, not to a list: leave out to and kinds")
	}
	for _, t := range m.To {
		if a, err := mail.ParseAddress(t); err != nil || strings.ContainsAny(t, "\r\n") || a.Address == "" {
			bad("%q is not an email address", t)
		}
	}
	switch strings.TrimSpace(fd.Group) {
	case "":
		m.Group = DefaultGroup
		if m.People {
			m.Group = DefaultPeopleGroup
		}
	default:
		g, err := time.ParseDuration(fd.Group)
		if err != nil || g < 0 || g > time.Hour {
			bad("group %q is not a time between 0 and 1h (0 sends one mail per message)", fd.Group)
		}
		if m.People && g < time.Second {
			bad("for the people taking part, group is how long lines wait for company: at least 1s")
		}
		m.Group = g
	}
	return m, problems
}

// linkFor is the address a message points at, or "".
func (n *Notifier) linkFor(m Message) string {
	if n.appURL == "" {
		return ""
	}
	path := m.Link
	if path == "" && (m.Kind == Problem || m.Kind == Backup || m.Kind == Bridge) {
		path = "admin" // what needs attention is on the administrator's page
	}
	return n.appURL + "/" + strings.TrimLeft(path, "/")
}
