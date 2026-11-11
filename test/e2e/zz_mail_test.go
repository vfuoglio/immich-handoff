//go:build e2e

package e2e

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// mailSink is a mail server that keeps every message it is given.
type mailSink struct {
	ln    net.Listener
	mu    sync.Mutex
	mails []sunkMail
}

type sunkMail struct{ to, data string }

func newMailSink(t *testing.T) *mailSink {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m := &mailSink{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go m.serve(c)
		}
	}()
	return m
}

func (m *mailSink) port() int { return m.ln.Addr().(*net.TCPAddr).Port }

func (m *mailSink) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(s string) { io.WriteString(c, s+"\r\n") }
	say("220 sink")
	var to string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		up := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(up, "RCPT TO:"):
			to = strings.ToLower(strings.Trim(strings.TrimSpace(line)[len("RCPT TO:"):], "<> "))
			say("250 ok")
		case up == "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			m.mu.Lock()
			m.mails = append(m.mails, sunkMail{to, b.String()})
			m.mu.Unlock()
			say("250 queued")
		case up == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

// to returns the plain text of every mail sent to an address.
func (m *mailSink) to(addr string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, x := range m.mails {
		if x.to == addr {
			out = append(out, decodeText(x.data))
		}
	}
	return out
}

// decodeText is the readable part of a mail made by the app: the quoted-printable text part.
func decodeText(raw string) string {
	i := strings.Index(raw, "text/plain")
	if i < 0 {
		return raw
	}
	rest := raw[i:]
	if j := strings.Index(rest, "\r\n\r\n"); j >= 0 {
		rest = rest[j+4:]
	}
	if bm := regexp.MustCompile(`boundary="([^"]+)"`).FindStringSubmatch(raw); bm != nil { // not the "-- " before the signature
		if k := strings.Index(rest, "\r\n--"+bm[1]); k >= 0 {
			rest = rest[:k]
		}
	}
	rest = strings.ReplaceAll(rest, "=\r\n", "")
	return strings.NewReplacer("=3D", "=", "=20", " ").Replace(rest)
}

// TestMailToPeople: a person turns email on from their page, what happens to their services reaches them in one
// mail with a link that stops the mail, and the link asks before it does.
func TestMailToPeople(t *testing.T) {
	bootstrap(t) // alice owns the service, bob asks to send
	sink := newMailSink(t)
	notifyFile := filepath.Join(t.TempDir(), "notify.yaml")
	if err := os.WriteFile(notifyFile, []byte(fmt.Sprintf(`destinations:
  - name: people
    format: email
    host: 127.0.0.1
    port: %d
    tls: none
    from: Immich Handoff <handoff@e2e.test>
    people: true
    group: 2s
`, sink.port())), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := buildApp(t)
	port := fmt.Sprint(atoi(appPort) + 60)
	launchApp(t, bin, port, "HANDOFF_STORE_KEY=an-e2e-store-key-0123456789", "DATA_DIR="+t.TempDir(),
		"NOTIFY_FILE="+notifyFile, "PUBLIC_APP_URL=http://handoff.test")
	owner, sender := loginCookie(t, "alice@e2e.test"), loginCookie(t, "bob@e2e.test")
	must := func(res *http.Response, want int, what string) {
		t.Helper()
		if res.StatusCode != want {
			t.Fatalf("%s: HTTP %d, want %d", what, res.StatusCode, want)
		}
	}
	if body := page(t, port, owner); !strings.Contains(body, "Turn on email") {
		t.Fatalf("the card is missing: %.300s", body)
	}
	must(post(t, port, "/me/mail", owner, url.Values{"action": {"on"}, "mode": {"all"}}), 204, "owner turns email on")
	must(post(t, port, "/me/mail", sender, url.Values{"action": {"on"}, "mode": {"needs"}}), 204, "sender turns email on")
	if body := page(t, port, owner); !strings.Contains(body, "Send a test email") || !strings.Contains(body, "alice@e2e.test") {
		t.Errorf("the card does not show it is on: %.300s", body)
	}

	must(post(t, port, "/services", owner, url.Values{"name": {"mailed"}, "tag": {"mailed-tag"}, "policy": {"approval"}}), 204, "create")
	must(post(t, port, "/ask", sender, url.Values{"service": {"mailed"}}), 204, "ask to join")

	// the request is something the owner must decide (and the sender asked for): both hear about it, in one mail each
	eventually(t, "the owner is mailed", 60*time.Second, func() bool { return len(sink.to("alice@e2e.test")) >= 1 })
	eventually(t, "the sender is mailed", 60*time.Second, func() bool { return len(sink.to("bob@e2e.test")) >= 1 })
	text := sink.to("alice@e2e.test")[0]
	if !strings.Contains(text, "mailed") || !strings.Contains(text, "http://handoff.test/") {
		t.Errorf("the owner's mail: %q", text)
	}
	link := regexp.MustCompile(`http://handoff\.test(/unsubscribe/[A-Za-z0-9_.-]+)`).FindStringSubmatch(text)
	if link == nil {
		t.Fatalf("no way to stop the mail: %q", text)
	}

	// a test mail arrives on demand
	before := len(sink.to("bob@e2e.test"))
	must(post(t, port, "/me/mail", sender, url.Values{"action": {"test"}}), 200, "test mail")
	eventually(t, "the test mail arrives", 30*time.Second, func() bool { return len(sink.to("bob@e2e.test")) > before })

	// the link asks first and needs no sign-in
	get := func(path string) (int, string) {
		res, err := http.Get("http://localhost:" + port + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, body := get(link[1]); code != 200 || !strings.Contains(body, "Stop the emails?") {
		t.Fatalf("%d %.200s", code, body)
	}
	if body := page(t, port, owner); !strings.Contains(body, "Send a test email") {
		t.Fatal("opening the link stopped the mail")
	}
	res, err := http.Post("http://localhost:"+port+link[1], "application/x-www-form-urlencoded", strings.NewReader("List-Unsubscribe=One-Click"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("one-click: %d", res.StatusCode)
	}
	if body := page(t, port, owner); strings.Contains(body, "Send a test email") || !strings.Contains(body, "Turn on email") {
		t.Errorf("the mail was not stopped: %.300s", body)
	}
}
