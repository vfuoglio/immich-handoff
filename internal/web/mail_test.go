package web_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/notify"
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
