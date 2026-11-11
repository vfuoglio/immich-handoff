package notify

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type got struct {
	header http.Header
	body   string
}

type sink struct {
	*httptest.Server
	mu   sync.Mutex
	hits []got
}

func newSink(t *testing.T, status func(n int) int) *sink {
	t.Helper()
	s := &sink{}
	var n int32
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.hits = append(s.hits, got{r.Header.Clone(), string(b)})
		s.mu.Unlock()
		code := 200
		if status != nil {
			code = status(int(atomic.AddInt32(&n, 1)))
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *sink) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.hits) }

func (s *sink) first(t *testing.T) got {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.count() > 0 {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.hits[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("nothing arrived")
	return got{}
}

func quick(n *Notifier) *Notifier {
	n.wait = []time.Duration{time.Millisecond, time.Millisecond}
	return n
}

func logTo(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, nil))
}

func TestNtfyGetsTheTextAsBodyAndTheRestAsHeaders(t *testing.T) {
	s := newSink(t, nil)
	n := New([]Destination{{Name: "a", Format: FormatNtfy, URL: s.URL, Auth: "tok"}}, "https://app.example/", nil)
	n.Send(Message{Kind: Problem, Text: "a key stopped working", Link: "admin"})
	g := s.first(t)
	if g.body != "a key stopped working" {
		t.Errorf("body %q", g.body)
	}
	for k, want := range map[string]string{"Title": "immich-handoff: needs attention", "Priority": "4", "Tags": "problem",
		"Click": "https://app.example/admin", "Authorization": "Bearer tok"} {
		if g.header.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, g.header.Get(k), want)
		}
	}
	n.Close()
}

func TestNoLinkWithoutTheAppAddress(t *testing.T) {
	s := newSink(t, nil)
	n := New([]Destination{{Name: "a", Format: FormatNtfy, URL: s.URL}}, "", nil)
	n.Send(Message{Kind: Info, Text: "x", Link: "admin"})
	if g := s.first(t); g.header.Get("Click") != "" || g.header.Get("Priority") != "2" {
		t.Errorf("headers %v", g.header)
	}
	n.Close()
}

func TestJSONCarriesKindTitleTextAndLink(t *testing.T) {
	s := newSink(t, nil)
	n := New([]Destination{{Name: "ha", Format: FormatJSON, URL: s.URL}}, "https://app.example", nil)
	n.Send(Message{Kind: Backup, Text: "backup failed", Link: "admin"})
	g := s.first(t)
	if g.header.Get("Content-Type") != "application/json" {
		t.Errorf("content type %q", g.header.Get("Content-Type"))
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(g.body), &m); err != nil {
		t.Fatal(err, g.body)
	}
	if m["source"] != "immich-handoff" || m["kind"] != "backup" || m["text"] != "backup failed" ||
		m["link"] != "https://app.example/admin" || m["title"] == "" {
		t.Errorf("json %v", m)
	}
	if _, err := time.Parse(time.RFC3339, m["time"]); err != nil {
		t.Errorf("time %q", m["time"])
	}
	n.Close()
}

func TestADestinationTakesOnlyTheKindsItAskedFor(t *testing.T) {
	all, only := newSink(t, nil), newSink(t, nil)
	n := New([]Destination{
		{Name: "all", Format: FormatNtfy, URL: all.URL},
		{Name: "problems", Format: FormatJSON, URL: only.URL, Kinds: map[Kind]bool{Problem: true}},
	}, "", nil)
	n.Send(Message{Kind: Backup, Text: "one"})
	n.Send(Message{Kind: Problem, Text: "two"})
	n.Close()
	if all.count() != 2 || only.count() != 1 || !strings.Contains(only.hits[0].body, "two") {
		t.Errorf("all=%d only=%d", all.count(), only.count())
	}
}

func TestAServerErrorIsTriedAgainButARefusalIsNot(t *testing.T) {
	var buf bytes.Buffer
	flaky := newSink(t, func(n int) int {
		if n < 3 {
			return 503
		}
		return 200
	})
	n := quick(New([]Destination{{Name: "flaky", Format: FormatNtfy, URL: flaky.URL}}, "", logTo(&buf)))
	n.Send(Message{Kind: Info, Text: "x"})
	n.Close()
	if flaky.count() != 3 || strings.Contains(buf.String(), "failed") {
		t.Errorf("hits %d, log %s", flaky.count(), buf.String())
	}

	buf.Reset()
	deny := newSink(t, func(int) int { return 401 })
	n = quick(New([]Destination{{Name: "deny", Format: FormatNtfy, URL: deny.URL}}, "", logTo(&buf)))
	n.Send(Message{Kind: Info, Text: "x"})
	n.Close()
	if deny.count() != 1 || !strings.Contains(buf.String(), "answered 401") || !strings.Contains(buf.String(), "deny") {
		t.Errorf("hits %d, log %s", deny.count(), buf.String())
	}
}

func TestARedirectIsAFailureNotAQuietGET(t *testing.T) {
	var buf bytes.Buffer
	target := newSink(t, nil)
	redir := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusMovedPermanently))
	defer redir.Close()
	n := quick(New([]Destination{{Name: "r", Format: FormatNtfy, URL: redir.URL}}, "", logTo(&buf)))
	n.Send(Message{Kind: Info, Text: "x"})
	n.Close()
	if target.count() != 0 || !strings.Contains(buf.String(), "redirects") {
		t.Errorf("target hits %d, log %s", target.count(), buf.String())
	}
}

func TestTheAddressNeverReachesTheLog(t *testing.T) {
	var buf bytes.Buffer
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL + "/secret-topic-name"
	dead.Close()
	n := quick(New([]Destination{{Name: "gone", Format: FormatNtfy, URL: url}}, "", logTo(&buf)))
	n.Send(Message{Kind: Info, Text: "x"})
	n.Close()
	if !strings.Contains(buf.String(), "notification failed") || strings.Contains(buf.String(), "secret-topic-name") ||
		strings.Contains(buf.String(), dead.URL) {
		t.Errorf("log: %s", buf.String())
	}
}

func TestASlowDestinationDoesNotBlockTheCaller(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer slow.Close()
	defer close(release)
	var buf bytes.Buffer
	n := New([]Destination{{Name: "slow", Format: FormatNtfy, URL: slow.URL}}, "", logTo(&buf))
	start := time.Now()
	for i := 0; i < 200; i++ {
		n.Send(Message{Kind: Info, Text: "x"})
	}
	if time.Since(start) > time.Second {
		t.Errorf("Send took %s", time.Since(start))
	}
	time.Sleep(50 * time.Millisecond)
	if c := strings.Count(buf.String(), "piling up"); c != 1 {
		t.Errorf("the full queue was logged %d times: %s", c, buf.String())
	}
}

func TestWithoutDestinationsNothingHappens(t *testing.T) {
	var n *Notifier
	n.Send(Message{Text: "x"})
	n.Close()
	New(nil, "", nil).Send(Message{Text: "x"})
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestNotifyURLStaysOneNtfyDestinationForEveryKind(t *testing.T) {
	d, err := FromEnv(env(map[string]string{"NOTIFY_URL": "https://ntfy.example/topic"}))
	if err != nil || len(d) != 1 || d[0].Format != FormatNtfy || len(d[0].Kinds) != 0 {
		t.Fatalf("%+v %v", d, err)
	}
	if d, err := FromEnv(env(nil)); err != nil || len(d) != 0 {
		t.Errorf("%+v %v", d, err)
	}
	if _, err := FromEnv(env(map[string]string{"NOTIFY_URL": "ntfy.sh/topic"})); err == nil {
		t.Error("an address with no scheme was accepted")
	}
}

func writeFile(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "notify.yaml")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTheFileAddsDestinationsAndReadsSecretsFromTheEnvironment(t *testing.T) {
	p := writeFile(t, `destinations:
  - name: home
    format: json
    url: https://ha.example/api/webhook/${HA_ID}
    auth: ${HA_TOKEN}
    kinds: [problem, backup]
  - name: phone
    url: https://ntfy.example/mine
`)
	d, err := FromEnv(env(map[string]string{"NOTIFY_URL": "https://ntfy.example/t", "NOTIFY_FILE": p, "HA_ID": "abc", "HA_TOKEN": "tok"}))
	if err != nil || len(d) != 3 {
		t.Fatalf("%+v %v", d, err)
	}
	home := d[1]
	if home.URL != "https://ha.example/api/webhook/abc" || home.Auth != "tok" || home.Format != FormatJSON ||
		!home.Kinds[Problem] || !home.Kinds[Backup] || home.Kinds[Info] {
		t.Errorf("%+v", home)
	}
	if d[2].Format != FormatNtfy {
		t.Errorf("the default format is ntfy: %+v", d[2])
	}
}

func TestTheFileIsCheckedAndEveryProblemIsNamed(t *testing.T) {
	p := writeFile(t, `destinations:
  - name: a
    format: xml
    url: https://x.example
  - name: a
    url: ftp://x.example
    kinds: [loud]
  - url: https://x.example
  - name: b
    url: https://x.example/${NOPE}
`)
	_, err := FromEnv(env(map[string]string{"NOTIFY_FILE": p}))
	if err == nil {
		t.Fatal("accepted")
	}
	for _, want := range []string{`format "xml"`, "named twice", "http or https", `kind "loud"`, "has no name", "NOPE is not set"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
	if _, err := FromEnv(env(map[string]string{"NOTIFY_FILE": writeFile(t, "destinations:\n  - name: a\n    colour: red\n    url: https://x.example\n")})); err == nil {
		t.Error("an unknown field was accepted")
	}
	if _, err := FromEnv(env(map[string]string{"NOTIFY_FILE": "/nonexistent/notify.yaml"})); err == nil {
		t.Error("a missing file was accepted")
	}
}
