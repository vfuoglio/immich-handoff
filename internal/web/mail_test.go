package web_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/notify"
	"github.com/vfuoglio/immich-handoff/internal/push"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/vault"
	"github.com/vfuoglio/immich-handoff/internal/web"
)

type mailSink struct {
	mu    sync.Mutex
	mails []notify.PersonalMail
}

func (m *mailSink) PeopleMailReady() bool { return true }
func (m *mailSink) SendPersonal(p notify.PersonalMail) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mails = append(m.mails, p)
	return nil
}
func (m *mailSink) count() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.mails) }

// withPeopleMail is withHub, and this install can write to people.
func withPeopleMail(t *testing.T, st **store.Store, people **hub.People, sink *mailSink) func(*web.Server) {
	return func(srv *web.Server) {
		withHub(t, st)(srv)
		v, _ := vault.New("0123456789abcdef0123")
		p := &hub.People{Store: *st, Sender: sink, Vault: v, AppURL: "https://app.example", Window: time.Minute, Log: srv.Log}
		t.Cleanup(p.Close)
		srv.Hub.People = p
		*people = p
	}
}

func TestTheEmailCardShowsOnlyWhereMailIsSetUp(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withHub(t, &st))
	if _, body := do(t, "GET", app.URL+"/", "sess=alice", nil); strings.Contains(body, "Email me") {
		t.Error("the card is there without a mail server for people")
	}
	var st2 *store.Store
	var p *hub.People
	app2, _ := setup(t, withPeopleMail(t, &st2, &p, &mailSink{}))
	code, body := do(t, "GET", app2.URL+"/", "sess=alice", nil)
	if code != 200 || !strings.Contains(body, "Email me") || !strings.Contains(body, "Turn on email") || strings.Contains(body, "Send a test email") {
		t.Errorf("%d: the card should offer to turn mail on", code)
	}
}

func TestAPersonTurnsEmailOnTestsItAndTurnsItOff(t *testing.T) {
	var st *store.Store
	var p *hub.People
	sink := &mailSink{}
	app, _ := setup(t, withPeopleMail(t, &st, &p, sink))
	ctx := context.Background()
	alice := uuid.MustParse(aliceID)

	if code, _ := postForm(t, app.URL+"/me/mail", "", "action=on&mode=all"); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, _ := do(t, "POST", app.URL+"/me/mail", "sess=alice", nil); code != 400 {
		t.Errorf("without the CSRF header: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/me/mail", "sess=alice", "action=on&mode=sometimes"); code != 409 {
		t.Errorf("a bad choice: %d", code)
	}
	if _, ok, _ := st.PersonMail(ctx, alice); ok {
		t.Fatal("a refused request turned mail on")
	}
	if code, _ := postForm(t, app.URL+"/me/mail", "sess=alice", "action=on&mode=needs"); code != 204 {
		t.Fatalf("turning on: %d", code)
	}
	pm, ok, _ := st.PersonMail(ctx, alice)
	if !ok || pm.Email != "alice@x.test" || pm.Mode != store.MailNeeds || pm.Name != "Alice" || pm.Lang != "en" {
		t.Fatalf("%+v", pm)
	}
	if _, ok, _ := st.PersonMail(ctx, uuid.MustParse(otherID)); ok {
		t.Error("someone else's row appeared")
	}
	_, body := do(t, "GET", app.URL+"/", "sess=alice", nil)
	for _, want := range []string{"alice@x.test", "Send a test email", "Turn off"} {
		if !strings.Contains(body, want) {
			t.Errorf("the card lacks %q", want)
		}
	}
	if code, msg := postForm(t, app.URL+"/me/mail", "sess=alice", "action=test"); code != 200 || !strings.Contains(msg, "alice@x.test") || sink.count() != 1 {
		t.Errorf("test mail: %d %q (%d sent)", code, msg, sink.count())
	}
	if code, _ := postForm(t, app.URL+"/me/mail", "sess=alice", "action=test"); code != 409 {
		t.Errorf("a second test at once: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/me/mail", "sess=alice", "action=off"); code != 204 {
		t.Errorf("turning off: %d", code)
	}
	if _, ok, _ := st.PersonMail(ctx, alice); ok {
		t.Error("still on")
	}
	if code, _ := postForm(t, app.URL+"/me/mail", "sess=alice", "action=explode"); code != 400 {
		t.Errorf("unknown action: %d", code)
	}
}

func TestTheUnsubscribeLinkAsksFirstAndNeedsNoSignIn(t *testing.T) {
	var st *store.Store
	var p *hub.People
	app, _ := setup(t, withPeopleMail(t, &st, &p, &mailSink{}))
	ctx := context.Background()
	alice := uuid.MustParse(aliceID)
	st.SetPersonMail(ctx, store.PersonMail{UserID: alice, Name: "Alice", Email: "alice@x.test", Lang: "en", Mode: store.MailAll})
	link := app.URL + "/unsubscribe/" + p.Token(alice)

	if code, _ := do(t, "GET", app.URL+"/unsubscribe/nonsense", "", nil); code != 404 {
		t.Errorf("a link we did not make: %d", code)
	}
	if code, _ := do(t, "POST", app.URL+"/unsubscribe/nonsense", "", nil); code != 404 {
		t.Errorf("posting to a link we did not make: %d", code)
	}
	code, body := do(t, "GET", link, "", nil)
	if code != 200 || !strings.Contains(body, "Stop the emails?") || !strings.Contains(body, `action="unsubscribe/`) {
		t.Fatalf("%d: the page should ask first", code)
	}
	if _, ok, _ := st.PersonMail(ctx, alice); !ok {
		t.Fatal("opening the link, as a mail scanner does, stopped the mail")
	}
	if code, body := do(t, "POST", link, "", nil); code != 200 || !strings.Contains(body, "No more emails") {
		t.Errorf("%d", code)
	}
	if _, ok, _ := st.PersonMail(ctx, alice); ok {
		t.Error("the button did not stop the mail")
	}
	if code, body := do(t, "POST", link, "", nil); code != 200 || !strings.Contains(body, "No more emails") {
		t.Errorf("a second click gives the same answer: %d", code)
	}
}

// --- notifications on devices ---

type pushSink struct {
	mu   sync.Mutex
	sent int
}

func (p *pushSink) Send(context.Context, push.Subscription, []byte, push.Options) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent++
	return nil
}

func withPush(t *testing.T, st **store.Store, sink *pushSink) func(*web.Server) {
	return func(srv *web.Server) {
		withHub(t, st)(srv)
		p := &hub.Push{Store: *st, Sender: sink, Key: "PUBLIC-KEY-FOR-THE-BROWSER", Log: srv.Log}
		t.Cleanup(p.Close)
		srv.Hub.Push = p
	}
}

func subscriptionJSON(t *testing.T, endpoint, mode string) string {
	t.Helper()
	k, _ := ecdh.P256().GenerateKey(rand.Reader)
	b, _ := json.Marshal(map[string]any{"endpoint": endpoint, "mode": mode, "keys": map[string]string{
		"p256dh": base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), "auth": base64.RawURLEncoding.EncodeToString(make([]byte, 16))}})
	return string(b)
}

func postJSON(t *testing.T, url, cookie, body string, header bool) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/130.0 Safari/537.36 Edg/130.0")
	if header {
		req.Header.Set("X-Requested-With", "immich-handoff")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestTheDevicesCardShowsOnlyWhereNotificationsAreSetUpAndTheKeyIsForSignedInPeople(t *testing.T) {
	var st *store.Store
	app, _ := setup(t, withHub(t, &st))
	if _, body := do(t, "GET", app.URL+"/", "sess=alice", nil); strings.Contains(body, "Notifications on this device") {
		t.Error("the card is there without push set up")
	}
	if code, _ := do(t, "GET", app.URL+"/me/push/key", "sess=alice", nil); code != 404 {
		t.Errorf("no key when off: %d", code)
	}
	var st2 *store.Store
	app2, _ := setup(t, withPush(t, &st2, &pushSink{}))
	if code, _ := do(t, "GET", app2.URL+"/me/push/key", "", nil); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	code, key := do(t, "GET", app2.URL+"/me/push/key", "sess=alice", nil)
	if code != 200 || key != "PUBLIC-KEY-FOR-THE-BROWSER" {
		t.Errorf("%d %q", code, key)
	}
	_, body := do(t, "GET", app2.URL+"/", "sess=alice", nil)
	for _, want := range []string{"Notifications on this device", `id="pushcard"`, `data-key="PUBLIC-KEY-FOR-THE-BROWSER"`, "Turn on notifications here"} {
		if !strings.Contains(body, want) {
			t.Errorf("the card lacks %q", want)
		}
	}
	if strings.Contains(body, "Your devices") {
		t.Error("no devices yet")
	}
}

func TestAPersonAddsTestsAndRemovesTheirOwnDevicesOnly(t *testing.T) {
	var st *store.Store
	sink := &pushSink{}
	app, _ := setup(t, withPush(t, &st, sink))
	ctx := context.Background()
	alice, other := uuid.MustParse(aliceID), uuid.MustParse(otherID)
	const endpoint = "https://fcm.googleapis.com/fcm/send/alices-edge"

	if code, _ := postJSON(t, app.URL+"/me/push", "sess=alice", subscriptionJSON(t, endpoint, "all"), false); code != 400 {
		t.Errorf("without the CSRF header: %d", code)
	}
	if code, _ := postJSON(t, app.URL+"/me/push", "", subscriptionJSON(t, endpoint, "all"), true); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	if code, _ := postJSON(t, app.URL+"/me/push", "sess=alice", "not json", true); code != 400 {
		t.Errorf("not json: %d", code)
	}
	if code, body := postJSON(t, app.URL+"/me/push", "sess=alice", subscriptionJSON(t, "https://evil.test/x", "all"), true); code != 409 || !strings.Contains(body, "push service") {
		t.Errorf("a host that is not a push service: %d %q", code, body)
	}
	if code, _ := postJSON(t, app.URL+"/me/push", "sess=alice", `{"endpoint":"`+endpoint+`","mode":"all","keys":{"p256dh":"AAAA","auth":"AAAA"}}`, true); code != 409 {
		t.Errorf("bad keys: %d", code)
	}
	if l, _ := st.PushSubs(ctx, alice); len(l) != 0 {
		t.Fatal("a refused device was stored")
	}

	if code, _ := postJSON(t, app.URL+"/me/push", "sess=alice", subscriptionJSON(t, endpoint, "needs"), true); code != 204 {
		t.Fatalf("subscribing: %d", code)
	}
	subs, _ := st.PushSubs(ctx, alice)
	if len(subs) != 1 || subs[0].Mode != store.MailNeeds || subs[0].Label != "Edge on Windows" || subs[0].Lang != "en" {
		t.Fatalf("%+v", subs)
	}
	id := subs[0].ID
	_, body := do(t, "GET", app.URL+"/", "sess=alice", nil)
	for _, want := range []string{"Your devices", "Edge on Windows", `data-device="` + id + `"`, "Send a test notification"} {
		if !strings.Contains(body, want) {
			t.Errorf("the card lacks %q", want)
		}
	}
	if _, theirs := do(t, "GET", app.URL+"/", "sess=other", nil); strings.Contains(theirs, id) || strings.Contains(theirs, "Edge on Windows") {
		t.Error("someone else sees alice's device")
	}

	// somebody else's device is "not in your list" for everything
	for _, path := range []string{"/me/push/remove", "/me/push/mode", "/me/push/test"} {
		if code, msg := postForm(t, app.URL+path, "sess=other", "id="+id+"&mode=all"); code != 409 || !strings.Contains(msg, "not in your list") {
			t.Errorf("POST %s as someone else: %d %q", path, code, msg)
		}
	}
	if l, _ := st.PushSubs(ctx, alice); len(l) != 1 {
		t.Fatal("someone else changed alice's devices")
	}
	if code, _ := postForm(t, app.URL+"/me/push/mode", "sess=alice", "id="+id+"&mode=loud"); code != 409 {
		t.Errorf("a bad mode: %d", code)
	}
	if code, _ := postForm(t, app.URL+"/me/push/mode", "sess=alice", "id="+id+"&mode=all"); code != 204 {
		t.Errorf("mode: %d", code)
	}
	if l, _ := st.PushSubs(ctx, alice); l[0].Mode != store.MailAll {
		t.Error("the mode did not change")
	}
	if code, _ := postForm(t, app.URL+"/me/push/test", "sess=alice", "id="+id); code != 204 || sink.sent != 1 {
		t.Errorf("test: %d (%d sent)", code, sink.sent)
	}
	if code, msg := postForm(t, app.URL+"/me/push/test", "sess=alice", "id="+id); code != 409 || !strings.Contains(msg, "a minute") {
		t.Errorf("a second test at once: %d %q", code, msg)
	}
	if code, _ := postForm(t, app.URL+"/me/push/remove", "sess=alice", "id="+id); code != 204 {
		t.Errorf("remove: %d", code)
	}
	if l, _ := st.PushSubs(ctx, alice); len(l) != 0 {
		t.Error("still there")
	}
	_ = other
}

func TestTheServiceWorkerIsServedAndCanOnlyShowAndOpen(t *testing.T) {
	app, _ := setup(t)
	res, err := http.Get(app.URL + "/static/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Type"), "javascript") || !strings.Contains(string(b), "showNotification") {
		t.Errorf("%d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if !strings.Contains(res.Header.Get("Content-Security-Policy"), "worker-src 'self'") {
		t.Error("the policy names the worker source")
	}
}
