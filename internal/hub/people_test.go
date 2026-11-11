package hub_test

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
)

type fakeMailer struct {
	mu    sync.Mutex
	ready bool
	err   error
	mails []notify.PersonalMail
}

func (f *fakeMailer) PeopleMailReady() bool { return f.ready }
func (f *fakeMailer) SendPersonal(m notify.PersonalMail) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.mails = append(f.mails, m)
	return nil
}
func (f *fakeMailer) all() []notify.PersonalMail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]notify.PersonalMail(nil), f.mails...)
}
func (f *fakeMailer) to(addr string) []notify.PersonalMail {
	var out []notify.PersonalMail
	for _, m := range f.all() {
		if m.To == addr {
			out = append(out, m)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func withPeople(t *testing.T, e *env, window time.Duration) (*hub.People, *fakeMailer) {
	t.Helper()
	v, _ := vault.New("a-test-store-key-0123456789")
	m := &fakeMailer{ready: true}
	p := &hub.People{Store: e.st, Sender: m, Vault: v, AppURL: "https://app.example", Window: window}
	e.h.People = p
	t.Cleanup(p.Close)
	return p, m
}

func optIn(t *testing.T, e *env, id uuid.UUID, name, email, lang, mode string) {
	t.Helper()
	if err := e.st.SetPersonMail(context.Background(), store.PersonMail{UserID: id, Name: name, Email: email, Lang: lang, Mode: mode}); err != nil {
		t.Fatal(err)
	}
}

func TestPeopleGetTheLinesTheirPageShowsInTheirLanguageAndOnlyWhatTheyAskedFor(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	_, m := withPeople(t, e, 250*time.Millisecond)
	optIn(t, e, annID, "Ann", "ann@x.test", "de", store.MailAll)
	optIn(t, e, bobID, "Bob", "bob@x.test", "en", store.MailNeeds)
	stranger := uuid.New()
	optIn(t, e, stranger, "Eve", "eve@x.test", "en", store.MailAll) // takes part in nothing

	e.h.CreateService(ctx, e.ann, ann, hub.CreateParams{Name: "party", Tag: "party", Policy: "open", Visibility: ""})
	if err := e.h.Join(ctx, e.sess, bob, "party", "", ""); err != nil {
		t.Fatal(err)
	}
	e.f.deleteKeyOf("bob")
	e.h.CheckKeys(ctx)

	waitFor(t, "both mails", func() bool { return len(m.to("ann@x.test")) == 1 && len(m.to("bob@x.test")) == 1 })
	time.Sleep(400 * time.Millisecond)
	if len(m.all()) != 2 {
		t.Fatalf("one mail each, nothing for the stranger: %d mails", len(m.all()))
	}

	a := m.to("ann@x.test")[0]
	if !strings.HasPrefix(a.Text, "Hallo Ann,") || !strings.Contains(a.Text, "Bob") || !strings.Contains(a.HTML, "Hallo Ann,") {
		t.Errorf("Ann reads German and everything (joined and cut): %s", a.Text)
	}
	if !strings.Contains(a.Subject, "Immich Handoff") || a.Subject == "Immich Handoff: news about your hand-overs" {
		t.Errorf("the subject is translated: %q", a.Subject)
	}
	b := m.to("bob@x.test")[0]
	if !strings.HasPrefix(b.Text, "Hi Bob,") || strings.Contains(b.Text, "joined") {
		t.Errorf("Bob asked for what needs him only, in English: %s", b.Text)
	}
	if !strings.Contains(b.Text, "cut") && !strings.Contains(b.Text, "key was deleted") {
		t.Errorf("Bob is told his connection was cut: %s", b.Text)
	}
	if b.Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" || !strings.HasPrefix(b.Headers["List-Unsubscribe"], "<https://app.example/unsubscribe/") {
		t.Errorf("headers: %v", b.Headers)
	}
	if !strings.Contains(b.Text, "https://app.example/") || !strings.Contains(b.Text, "/unsubscribe/") {
		t.Errorf("the mail has the page and the way out: %s", b.Text)
	}
}

func TestAPersonWhoTurnsMailOffWhileLinesWaitGetsNothing(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	p, m := withPeople(t, e, 150*time.Millisecond)
	optIn(t, e, annID, "Ann", "ann@x.test", "en", store.MailAll)
	p.Tell(ctx, []uuid.UUID{annID}, "joined", "x")
	e.st.DeletePersonMail(ctx, annID)
	time.Sleep(400 * time.Millisecond)
	if len(m.all()) != 0 {
		t.Errorf("mail went to someone who turned it off: %v", m.all())
	}
	p.Tell(ctx, []uuid.UUID{annID}, "joined", "y") // nor does a later line queue anything
	time.Sleep(300 * time.Millisecond)
	if len(m.all()) != 0 {
		t.Errorf("%v", m.all())
	}
}

func TestNoMoreThanSixMailsAnHourAndTheRestWaitsForTheNext(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	p, m := withPeople(t, e, 10*time.Millisecond)
	p.PerHour = 2
	optIn(t, e, annID, "Ann", "ann@x.test", "en", store.MailAll)
	for i := 0; i < 3; i++ {
		p.Tell(ctx, []uuid.UUID{annID}, "joined", "line")
		time.Sleep(80 * time.Millisecond)
	}
	if n := len(m.all()); n != 2 {
		t.Fatalf("the third mail waits: %d sent", n)
	}
	p.Close() // shutting down sends what waits, once
	if n := len(m.all()); n != 3 {
		t.Errorf("after close: %d sent", n)
	}
}

func TestTheUnsubscribeLinkIsSignedForOnePerson(t *testing.T) {
	e := setup(t, false)
	p, _ := withPeople(t, e, time.Minute)
	tok := p.Token(annID)
	if id, ok := p.Verify(tok); !ok || id != annID {
		t.Fatalf("round trip: %v %v", id, ok)
	}
	other := p.Token(bobID)
	forged := strings.Split(tok, ".")[0] + "." + strings.Split(other, ".")[1] // Ann's name, Bob's signature
	for _, bad := range []string{"", "x", "a.b", tok + "A", forged, strings.ToUpper(tok)} {
		if _, ok := p.Verify(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	v2, _ := vault.New("another-store-key-0123456789")
	p2 := &hub.People{Vault: v2}
	if _, ok := p2.Verify(tok); ok {
		t.Error("a link from another install must not work")
	}
	if got := p.UnsubscribeURL(annID); got != "https://app.example/unsubscribe/"+tok {
		t.Errorf("%s", got)
	}
}

func TestTheCardTurnsMailOnChecksTheChoiceAndKeepsTheAddressCurrent(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	if c := e.h.MailCard(ctx, bob, "en"); c.Available {
		t.Fatal("no card without a mail server for people")
	}
	if err := e.h.TurnMailOn(ctx, bob, "en", store.MailAll); err != hub.ErrMailOff {
		t.Errorf("%v", err)
	}
	_, m := withPeople(t, e, time.Minute)
	if c := e.h.MailCard(ctx, bob, "en"); !c.Available || c.On || c.Email != "bob@x.test" {
		t.Errorf("%+v", c)
	}
	if err := e.h.TurnMailOn(ctx, bob, "en", "sometimes"); err != hub.ErrBadMailMode {
		t.Errorf("%v", err)
	}
	noMail := bob
	noMail.Email = " "
	if err := e.h.TurnMailOn(ctx, noMail, "en", store.MailAll); err != hub.ErrNoMailAddress {
		t.Errorf("%v", err)
	}
	if err := e.h.SendTestMail(ctx, bob, "en"); err != hub.ErrMailNotOn {
		t.Errorf("%v", err)
	}
	if err := e.h.TurnMailOn(ctx, bob, "en", store.MailNeeds); err != nil {
		t.Fatal(err)
	}
	moved := bob
	moved.Email = "robert@x.test"
	if c := e.h.MailCard(ctx, moved, "fr"); !c.On || c.Mode != store.MailNeeds || c.Email != "robert@x.test" {
		t.Errorf("the address follows Immich: %+v", c)
	}
	if pm, _, _ := e.st.PersonMail(ctx, bobID); pm.Email != "robert@x.test" || pm.Lang != "fr" {
		t.Errorf("%+v", pm)
	}

	if err := e.h.SendTestMail(ctx, moved, "fr"); err != nil {
		t.Fatal(err)
	}
	got := m.to("robert@x.test")
	if len(got) != 1 || !strings.Contains(got[0].Text, "Bonjour Bob") || !strings.Contains(got[0].Text, "test") {
		t.Errorf("the test mail is in French: %+v", got)
	}
	if err := e.h.SendTestMail(ctx, moved, "fr"); err != hub.ErrMailTestSoon {
		t.Errorf("one a minute: %v", err)
	}

	if err := e.h.TurnMailOff(ctx, bobID); err != nil {
		t.Fatal(err)
	}
	if c := e.h.MailCard(ctx, moved, "fr"); c.On {
		t.Error("still on")
	}
}

func TestAFailedTestMailSaysSo(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	_, m := withPeople(t, e, time.Minute)
	e.h.TurnMailOn(ctx, bob, "en", store.MailAll)
	m.err = errBoom
	err := e.h.SendTestMail(ctx, bob, "en")
	if err == nil || !hub.IsUserError(err) || !strings.Contains(err.Error(), "boom") {
		t.Errorf("%v", err)
	}
}

func TestUnsubscribingNeedsAValidLinkAndSaysNothingAboutWhoHadMailOn(t *testing.T) {
	e, ctx := setup(t, false), context.Background()
	p, _ := withPeople(t, e, time.Minute)
	optIn(t, e, annID, "Ann", "ann@x.test", "en", store.MailAll)
	if e.h.Unsubscribe(ctx, "nonsense") || e.h.ValidUnsubscribe("nonsense") {
		t.Fatal("accepted nonsense")
	}
	if !e.h.ValidUnsubscribe(p.Token(annID)) {
		t.Fatal("a real link is valid")
	}
	if _, ok, _ := e.st.PersonMail(ctx, annID); !ok {
		t.Fatal("looking at the page must not unsubscribe")
	}
	if !e.h.Unsubscribe(ctx, p.Token(annID)) {
		t.Fatal("refused")
	}
	if _, ok, _ := e.st.PersonMail(ctx, annID); ok {
		t.Error("still on")
	}
	if !e.h.Unsubscribe(ctx, p.Token(bobID)) { // Bob never had it on: the same answer
		t.Error("a valid link answers the same whether or not mail was on")
	}
}

var errBoom = errorString("the mail server answered 550: boom")

type errorString string

func (e errorString) Error() string { return string(e) }
