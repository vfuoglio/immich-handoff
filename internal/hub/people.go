package hub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/base64"
	"errors"
	"html/template"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/notify"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
	"github.com/vfuoglio/immich-handoff/internal/vault"
)

// PersonSender is what People needs from the notifier: whether a mail server is set aside for the people taking part,
// and a way to send one mail to one person.
type PersonSender interface {
	PeopleMailReady() bool
	SendPersonal(notify.PersonalMail) error
}

// People writes to the people who asked for it. What a person hears is what the page writes to their recent events
// (the same lines, chosen by the same rule, in the language of their page), collected for a few minutes so a burst is
// one mail, and never more than a few mails an hour.
type People struct {
	Store   *store.Store
	Sender  PersonSender
	Vault   *vault.Vault
	AppURL  string // how people reach the page; the links in a mail need it
	Log     *slog.Logger
	Window  time.Duration // how long lines wait for company (default 5 minutes)
	PerHour int           // at most this many mails to one person in an hour (default 6)
	Now     func() time.Time

	mu     sync.Mutex
	queues map[uuid.UUID]*personQueue
	sent   map[uuid.UUID][]time.Time
	tested map[uuid.UUID]time.Time
	closed bool
}

type personLine struct {
	at         time.Time
	kind, text string
}

type personQueue struct {
	lines []personLine
	timer *time.Timer
}

// needsKinds are the events that ask something of a person or tell them something stopped; the rest is news.
var needsKinds = map[string]bool{"broken": true, "removed": true, "asked": true, "limit": true}

// Ready reports whether people can be written to at all.
func (p *People) Ready() bool {
	return p != nil && p.Store != nil && p.Vault != nil && p.AppURL != "" && p.Sender != nil && p.Sender.PeopleMailReady()
}

func (p *People) window() time.Duration {
	if p.Window > 0 {
		return p.Window
	}
	return 5 * time.Minute
}

func (p *People) perHour() int {
	if p.PerHour > 0 {
		return p.PerHour
	}
	return 6
}

func (p *People) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Tell is called for every line written to the activity log, with the people who may read it. Those who asked for mail
// (and whose choice lets this line through) will get it.
func (p *People) Tell(ctx context.Context, audience []uuid.UUID, kind, text string) {
	if !p.Ready() {
		return
	}
	for _, id := range audience {
		pm, ok, err := p.Store.PersonMail(ctx, id)
		if err != nil || !ok || (pm.Mode == store.MailNeeds && !needsKinds[kind]) {
			continue
		}
		p.queue(id, personLine{p.now(), kind, text})
	}
}

func (p *People) queue(id uuid.UUID, l personLine) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if p.queues == nil {
		p.queues = map[uuid.UUID]*personQueue{}
	}
	q := p.queues[id]
	if q == nil {
		q = &personQueue{}
		p.queues[id] = q
	}
	q.lines = append(q.lines, l)
	if q.timer == nil {
		q.timer = time.AfterFunc(p.window(), func() { p.flush(id) })
	}
}

// flush sends what is waiting for one person, unless they already had their share of mail this hour (then it waits).
func (p *People) flush(id uuid.UUID) {
	p.mu.Lock()
	q := p.queues[id]
	if q == nil || len(q.lines) == 0 {
		p.mu.Unlock()
		return
	}
	now := p.now()
	recent := p.sent[id][:0:0]
	for _, t := range p.sent[id] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if p.sent == nil {
		p.sent = map[uuid.UUID][]time.Time{}
	}
	p.sent[id] = recent
	if len(recent) >= p.perHour() && !p.closed {
		wait := time.Hour - now.Sub(recent[0]) + time.Second
		q.timer = time.AfterFunc(wait, func() { p.flush(id) })
		p.mu.Unlock()
		return
	}
	lines := q.lines
	delete(p.queues, id)
	p.sent[id] = append(recent, now)
	p.mu.Unlock()

	ctx := context.Background()
	pm, ok, err := p.Store.PersonMail(ctx, id)
	if err != nil || !ok {
		return // they turned it off while the lines waited
	}
	if err := p.Sender.SendPersonal(p.compose(pm, lines)); err != nil {
		p.log().Warn("could not send a person's mail", "err", err) // never the address
		return
	}
	_ = p.Store.MarkPersonMailSent(ctx, id, now)
}

func (p *People) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

// Close sends what is waiting, once, and takes no more.
func (p *People) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.closed = true
	var ids []uuid.UUID
	for id, q := range p.queues {
		if q.timer != nil {
			q.timer.Stop()
		}
		ids = append(ids, id)
	}
	p.mu.Unlock()
	for _, id := range ids {
		p.flush(id)
	}
}

// SendTest sends one mail now to say whether mail works, and returns why not.
func (p *People) SendTest(pm store.PersonMail) error {
	if !p.Ready() {
		return errors.New("mail to people is not set up")
	}
	return p.Sender.SendPersonal(p.compose(pm, []personLine{{p.now(), "test", ""}}))
}

// --- the unsubscribe link ---

// Token is the signed link part that lets a person stop their mail without signing in.
func (p *People) Token(user uuid.UUID) string {
	id := user.String()
	return base64.RawURLEncoding.EncodeToString([]byte(id)) + "." + base64.RawURLEncoding.EncodeToString(p.Vault.MAC("unsubscribe", []byte(id)))
}

// Verify returns the person a token was made for.
func (p *People) Verify(token string) (uuid.UUID, bool) {
	if p == nil || p.Vault == nil {
		return uuid.Nil, false
	}
	a, b, ok := strings.Cut(token, ".")
	if !ok {
		return uuid.Nil, false
	}
	idRaw, err1 := base64.RawURLEncoding.DecodeString(a)
	sig, err2 := base64.RawURLEncoding.DecodeString(b)
	if err1 != nil || err2 != nil {
		return uuid.Nil, false
	}
	if !hmac.Equal(sig, p.Vault.MAC("unsubscribe", idRaw)) {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(string(idRaw))
	return id, err == nil
}

// UnsubscribeURL is the link in a mail.
func (p *People) UnsubscribeURL(user uuid.UUID) string {
	return strings.TrimRight(p.AppURL, "/") + "/unsubscribe/" + p.Token(user)
}

// --- the mail ---

var peopleHTML = template.Must(template.New("people").Parse(`<!doctype html>
<html lang="{{.Lang}}"><body style="margin:0;padding:24px;background:#f4f3fa;font-family:-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#1b1936">
<div style="max-width:560px;margin:0 auto;background:#ffffff;border-radius:12px;padding:24px">
<p style="margin:0 0 12px">{{.Greeting}}</p>
<p style="margin:0 0 16px">{{.Intro}}</p>
{{range .Lines}}<p style="margin:0 0 12px;line-height:1.45"><span style="color:#6b6894;font-size:12px">{{.Time}}</span><br>{{.Text}}</p>
{{end}}<p style="margin:20px 0 0"><a href="{{.Link}}" style="display:inline-block;background:#5b4bdb;color:#ffffff;text-decoration:none;padding:10px 18px;border-radius:8px">{{.Open}}</a></p>
<p style="margin:24px 0 0;color:#6b6894;font-size:12px">{{.Why}} <a href="{{.Unsub}}" style="color:#6b6894">{{.Stop}}</a></p>
</div></body></html>
`))

type peopleLine struct{ Time, Text string }

// compose writes one person's mail in their language.
func (p *People) compose(pm store.PersonMail, lines []personLine) notify.PersonalMail {
	lang, _ := i18n.Supported(pm.Lang)
	l := i18n.For(lang)
	unsub := p.UnsubscribeURL(pm.UserID)
	link := strings.TrimRight(p.AppURL, "/") + "/"

	var shown []peopleLine
	for _, ln := range lines {
		text := l.T("This is a test email from Immich Handoff. If you can read it, the emails work.")
		if ln.kind != "test" {
			text = l.Unpack(ln.text)
		}
		shown = append(shown, peopleLine{l.Stamp(ln.at.Local()), text})
	}
	d := struct {
		Lang, Greeting, Intro, Open, Why, Stop, Link, Unsub string
		Lines                                               []peopleLine
	}{
		Lang: string(lang), Greeting: l.T("Hi {name},", "name", pm.Name), Intro: l.T("This is what happened in Immich Handoff:"),
		Open: l.T("Open Immich Handoff"), Why: l.T("You get this email because you turned it on in Immich Handoff."), Stop: l.T("Stop these emails"),
		Link: link, Unsub: unsub, Lines: shown,
	}
	var htmlBody bytes.Buffer
	_ = peopleHTML.Execute(&htmlBody, d)

	var text strings.Builder
	text.WriteString(d.Greeting + "\n\n" + d.Intro + "\n\n")
	for _, s := range shown {
		text.WriteString(s.Time + "  " + s.Text + "\n")
	}
	text.WriteString("\n" + d.Open + ": " + link + "\n\n-- \n" + d.Why + "\n" + d.Stop + ": " + unsub + "\n")

	return notify.PersonalMail{
		To: pm.Email, Name: pm.Name, Subject: l.T("Immich Handoff: news about your hand-overs"),
		Text: text.String(), HTML: htmlBody.String(),
		Headers: map[string]string{"List-Unsubscribe": "<" + unsub + ">", "List-Unsubscribe-Post": "List-Unsubscribe=One-Click"},
	}
}

// activityAudience is everyone who may read a line of the activity log written for a service: the people who take part in it
// or own it, and the person it is about (the same rule as Activity).
func (h *Hub) activityAudience(ctx context.Context, service string, about uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	add := func(id uuid.UUID) {
		if id != uuid.Nil && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	add(about)
	for _, svc := range h.Services() {
		if svc.Label() != service {
			continue
		}
		add(svc.OwnerID)
		for _, group := range [][]*syncer.Account{svc.Senders, svc.Receivers} {
			for _, a := range group {
				add(a.UserID)
			}
		}
	}
	if h.Store != nil {
		if links, err := h.Store.Links(ctx); err == nil {
			for _, l := range links {
				if l.Service == service {
					add(l.ImmichUserID)
				}
			}
		}
	}
	return out
}
