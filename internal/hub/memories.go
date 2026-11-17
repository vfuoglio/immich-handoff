package hub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/notify"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// "On this day" messages (#199): each person who turns them on gets one message a day, at the time they chose, about the
// memories Immich made for that day from their own old photos. The app reads them with a key the person made for it,
// from their own session, that can read memories and look at pictures and nothing else.

// memoriesKeyName is what the key is called in the person's Immich, where they can see it and delete it.
const memoriesKeyName = "handoff: memories"

var (
	ErrBadMemoryTime    = errors.New("the time must be written like 08:00")
	ErrBadMemoryZone    = errors.New("that time zone is not known")
	ErrMemoryNoChannel  = errors.New("choose push or email, or there is nobody to tell")
	ErrMemoriesNotOn    = errors.New("the messages are not turned on for you")
	ErrMemoriesNoSender = errors.New("neither push notifications nor email are set up on this app")
	ErrBadMemoryDay     = errors.New("that is not a day")
)

// memoryKind is the kind of line a message is sent as; devices take it whatever their mode.
const memoryKind = "memory"

var memoryTime = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// MemoryInput is what a person fills in for their "On this day" messages.
type MemoryInput struct {
	At       string // "08:00"
	TZ       string // an IANA zone such as "Europe/Rome"; empty is the server's
	Lang     string // the language of their page
	Push     bool
	Email    bool
	Pictures bool // thumbnails in the email
}

func (in MemoryInput) check() error {
	if !memoryTime.MatchString(in.At) {
		return ErrBadMemoryTime
	}
	if in.TZ != "" {
		if _, err := time.LoadLocation(in.TZ); err != nil || len(in.TZ) > 64 {
			return ErrBadMemoryZone
		}
	}
	if !in.Push && !in.Email {
		return ErrMemoryNoChannel
	}
	return nil
}

// memoriesSender says whether anything can carry the message at all.
func (h *Hub) memoriesChannels() (push, email bool) {
	push = h.Push.Ready()
	email = h.People.Ready()
	return
}

// MemoriesCard is what a person's page shows about their "On this day" messages.
type MemoriesCard struct {
	Available bool // the app can send them at all: self-service and push or email are set up
	Push      bool // push can carry it
	Email     bool // email can carry it
	On        bool
	Settings  store.MemoryPref
	KeyOK     bool // the key in their Immich still works (only asked when the page is opened)
}

// MemoriesCard says what the card shows. sess, when given, is used to see whether the key still exists in their Immich.
func (h *Hub) MemoriesCard(ctx context.Context, user uuid.UUID) MemoriesCard {
	var c MemoriesCard
	if h.Store == nil || !h.SelfService() {
		return c
	}
	c.Push, c.Email = h.memoriesChannels()
	c.Available = c.Push || c.Email
	p, ok, err := h.Store.MemoryPref(ctx, user)
	if err != nil || !ok {
		return c
	}
	c.On, c.Settings, c.KeyOK = true, p, !p.Broken
	return c
}

// SetMemories turns the messages on, or changes their settings. The first time the person's own session makes the key.
func (h *Hub) SetMemories(ctx context.Context, sess *immich.API, user immich.User, in MemoryInput) error {
	if !h.SelfService() {
		return ErrNotJoined
	}
	if err := in.check(); err != nil {
		return err
	}
	if push, email := h.memoriesChannels(); !push && !email {
		return ErrMemoriesNoSender
	}
	p, ok, err := h.Store.MemoryPref(ctx, user.ID)
	if err != nil {
		return err
	}
	if !ok || p.Broken {
		if ok && p.KeyID != uuid.Nil && sess != nil { // a broken key may still be in their Immich: do not leave it behind
			_ = sess.DeleteAPIKey(ctx, p.KeyID)
		}
		id, secret, err := sess.CreateAPIKey(ctx, memoriesKeyName, immich.MemoriesPermissions)
		if err != nil {
			return fmt.Errorf("could not make the app a key to read your memories: %w", err)
		}
		p.KeyID, p.Key = id, secret
		p.Broken = false
	}
	if in.Email && strings.TrimSpace(user.Email) == "" {
		return ErrNoMailAddress
	}
	p.UserID, p.Name, p.Address = user.ID, user.Name, strings.TrimSpace(user.Email)
	p.At, p.TZ, p.Lang = in.At, in.TZ, in.Lang
	p.Push, p.Email, p.Pictures = in.Push, in.Email, in.Pictures
	if err := h.Store.SetMemoryPref(ctx, p); err != nil {
		if !ok {
			_ = sess.DeleteAPIKey(ctx, p.KeyID)
		}
		return err
	}
	h.Log.Info("on this day messages are on", "user", user.Email)
	return nil
}

// StopMemories turns the messages off: the key is deleted in the person's Immich (with their session) and forgotten.
func (h *Hub) StopMemories(ctx context.Context, sess *immich.API, user immich.User) error {
	if h.Store == nil {
		return ErrNotJoined
	}
	p, ok, err := h.Store.MemoryPref(ctx, user.ID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrMemoriesNotOn
	}
	if sess != nil && p.KeyID != uuid.Nil {
		if err := sess.DeleteAPIKey(ctx, p.KeyID); err != nil && !immich.IsNotFound(err) {
			h.Log.Debug("could not delete the key for the on this day messages", "err", err)
		}
	}
	return h.Store.DeleteMemoryPref(ctx, user.ID)
}

// memoriesAPI is the client that acts with the person's key for their memories.
func (h *Hub) memoriesAPI(p store.MemoryPref) (*immich.API, error) {
	return h.Connect(p.Key)
}

// --- the daily pass ---

// memoriesEvery is how often the messages are looked at (MEMORIES_CHECK_MINUTES, default 10).
func (h *Hub) memoriesEvery() time.Duration {
	if h.MemoriesEvery > 0 {
		return h.MemoriesEvery
	}
	return 10 * time.Minute
}

// memoryPictures is the most thumbnails one message carries.
const memoryPictures = 3

// MemoriesLoop looks at everyone's messages every few minutes until the context ends.
func (h *Hub) MemoriesLoop(ctx context.Context) {
	t := time.NewTicker(h.memoriesEvery())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.MemoriesMaintenance(ctx)
		}
	}
}

// MemoriesMaintenance sends, to each person whose time has come today, the message about today's memories.
func (h *Hub) MemoriesMaintenance(ctx context.Context) {
	if h.Store == nil || !h.SelfService() {
		return
	}
	list, err := h.Store.MemoryPrefs(ctx)
	if err != nil {
		return
	}
	for _, p := range list {
		if ctx.Err() != nil {
			return
		}
		h.memoryFor(ctx, p)
	}
}

// memoryZone is the person's time zone, or the server's when theirs is empty or unknown.
func memoryZone(name string) *time.Location {
	if name != "" {
		if l, err := time.LoadLocation(name); err == nil {
			return l
		}
	}
	return time.Local
}

// memoryDue says which day it is for the person and whether their time has come.
func memoryDue(p store.MemoryPref, now time.Time) (day string, due bool) {
	n := now.In(memoryZone(p.TZ))
	day = n.Format("2006-01-02")
	var hh, mm int
	if _, err := fmt.Sscanf(p.At, "%d:%d", &hh, &mm); err != nil {
		hh, mm = 8, 0
	}
	at := time.Date(n.Year(), n.Month(), n.Day(), hh, mm, 0, 0, n.Location())
	return day, !n.Before(at)
}

// memoryFor does one person's day: when their time has come and nothing was sent today, it asks Immich for today's
// memories, leaves out the ones they already watched, and tells them about the rest. A day with nothing to tell is
// done as well; a day that failed is tried again on the next pass.
func (h *Hub) memoryFor(ctx context.Context, p store.MemoryPref) {
	if p.Broken {
		return
	}
	now := h.now()
	day, due := memoryDue(p, now)
	if !due || p.LastDay == day {
		return
	}
	api, err := h.memoriesAPI(p)
	if err != nil {
		h.Log.Debug("on this day: no client", "err", err)
		return
	}
	local := now.In(memoryZone(p.TZ))
	mems, err := api.MemoriesFor(ctx, local)
	if err != nil {
		if immich.IsAuthError(err) { // the key was deleted or revoked: say so once, then leave them alone
			_ = h.Store.MarkMemoryBroken(ctx, p.UserID, true)
			h.event(ctx, "", p.UserID, "broken", i18n.Pack("Your On this day messages stopped: the key the app made in your Immich is gone. Turn them off and on again to make a new one."))
			return
		}
		h.Log.Debug("on this day: could not read the memories", "err", err)
		return
	}
	var fresh []immich.Memory
	for _, m := range mems {
		if !m.Seen && len(m.Photos) > 0 && local.Year()-m.Year > 0 {
			fresh = append(fresh, m)
		}
	}
	if len(fresh) == 0 {
		_ = h.Store.MarkMemorySent(ctx, p.UserID, day)
		return
	}
	if h.tellMemories(ctx, api, p, local, fresh) {
		_ = h.Store.MarkMemorySent(ctx, p.UserID, day)
	}
}

// yearsAgo is "3 years ago, 5 years ago", as a message that is put in the reader's language when it is read.
func yearsAgo(day time.Time, mems []immich.Memory) string {
	parts := make([]string, 0, len(mems))
	for _, m := range mems {
		n := day.Year() - m.Year
		parts = append(parts, i18n.PackN("{n} year ago", "{n} years ago", n))
	}
	return i18n.PackJoin(", ", parts...)
}

// pickPhotos chooses up to n photos, the oldest year first, one from each year before a second from any.
func pickPhotos(mems []immich.Memory, n int) []uuid.UUID {
	var out []uuid.UUID
	for round := 0; len(out) < n; round++ {
		more := false
		for _, m := range mems {
			if round < len(m.Photos) {
				more = true
				if len(out) < n {
					out = append(out, uuid.UUID(m.Photos[round]))
				}
			}
		}
		if !more {
			break
		}
	}
	return out
}

// MemoryDayLayout is how a day is written in the address of the page that lists a day's memories.
const MemoryDayLayout = "2006-01-02"

// memoryLinks are the two things a message offers: opening the memory in Immich (when the app knows Immich's address) and
// sending its photos on from this app's page (when it knows its own).
func (h *Hub) memoryLinks(first immich.Memory, day time.Time) []ActionLink {
	var out []ActionLink
	if base := strings.TrimRight(h.ImmichURL, "/"); base != "" {
		out = append(out, ActionLink{Label: "Open the memory", URL: base + "/memory?id=" + uuid.UUID(first.ID).String()})
	}
	if base := strings.TrimRight(h.AppURL, "/"); base != "" {
		out = append(out, ActionLink{Label: "Send to someone", URL: base + "/memories/" + day.Format(MemoryDayLayout)})
	}
	return out
}

// tellMemories sends the message on the channels the person chose and says whether it counts as sent.
func (h *Hub) tellMemories(ctx context.Context, api *immich.API, p store.MemoryPref, day time.Time, mems []immich.Memory) bool {
	years := yearsAgo(day, mems)
	links := h.memoryLinks(mems[0], day)
	sent := false
	if p.Push && h.Push.Ready() {
		if devs, err := h.Store.PushSubs(ctx, p.UserID); err == nil && len(devs) > 0 { // with no device the message would go nowhere
			text := i18n.PackJoin(" ", i18n.Pack("On this day:"), years)
			var forPush func(uuid.UUID) []ActionLink
			if len(links) > 0 {
				forPush = func(uuid.UUID) []ActionLink { return links }
			}
			h.Push.TellLinks(ctx, []uuid.UUID{p.UserID}, memoryKind, text, forPush)
			sent = true
		}
	}
	if p.Email && p.Address != "" && h.People.Ready() {
		if err := h.People.Sender.SendPersonal(h.composeMemoryMail(ctx, api, p, years, links, pickPhotos(mems, memoryPictures))); err != nil {
			h.Log.Warn("on this day: could not send the email", "err", err) // never the address
		} else {
			sent = true
		}
	}
	if sent { // a line in Activity, and no second message about it
		_ = h.Store.AddEvent(context.WithoutCancel(ctx), store.Event{UserID: p.UserID, Kind: memoryKind, Text: i18n.PackJoin(" ", i18n.Pack("On this day:"), years)})
	}
	return sent
}

var memoryHTML = template.Must(template.New("memory").Parse(`<!doctype html><html lang="{{.Lang}}"><body style="margin:0;background:#f4f3fb;font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;color:#1c1b2e">
<div style="max-width:560px;margin:0 auto;padding:24px 16px">
<p style="margin:0 0 12px">{{.Greeting}}</p>
<p style="margin:0 0 16px;font-size:18px"><strong>{{.Intro}}</strong></p>
{{range .Pictures}}<p style="margin:0 0 12px"><img src="cid:{{.}}" alt="" style="max-width:100%;border-radius:8px"></p>
{{end}}{{if .Buttons}}<p style="margin:16px 0">{{range .Buttons}}<a href="{{.URL}}" style="background:#5b4bd6;color:#fff;text-decoration:none;padding:10px 16px;border-radius:8px;display:inline-block;margin:0 8px 8px 0">{{.Label}}</a>{{end}}</p>
{{end}}<p style="margin:16px 0 0;color:#6b6894;font-size:12px">{{.Why}} <a href="{{.Manage}}" style="color:#6b6894">{{.Stop}}</a></p>
</div></body></html>
`))

// composeMemoryMail writes one person's message in their language, with the thumbnails inside it when they want them.
func (h *Hub) composeMemoryMail(ctx context.Context, api *immich.API, p store.MemoryPref, years string, links []ActionLink, photos []uuid.UUID) notify.PersonalMail {
	lang, _ := i18n.Supported(p.Lang)
	l := i18n.For(lang)
	when := l.Unpack(years)
	manage := strings.TrimRight(h.AppURL, "/") + "/"

	var inline []notify.Inline
	var cids []string
	if p.Pictures {
		for i, id := range photos {
			data, typ, err := api.Thumbnail(ctx, id, false)
			if err != nil || len(data) == 0 {
				continue // the message goes without that picture
			}
			cid := fmt.Sprintf("memory-%d@handoff", i+1)
			inline = append(inline, notify.Inline{CID: cid, Type: typ, Data: data})
			cids = append(cids, cid)
		}
	}
	type button struct{ Label, URL string }
	var buttons []button
	for _, a := range links {
		buttons = append(buttons, button{l.T(a.Label), a.URL})
	}
	d := struct {
		Lang, Greeting, Intro, Why, Stop, Manage string
		Pictures                                 []string
		Buttons                                  []button
	}{
		Lang: string(lang), Greeting: l.T("Hi {name},", "name", p.Name), Intro: l.T("Your Immich has memories for today: {years}.", "years", when),
		Buttons: buttons,
		Why:     l.T("You get this because you turned on On this day messages in Immich Handoff."), Stop: l.T("Change or stop them"), Manage: manage, Pictures: cids,
	}
	var htmlBody bytes.Buffer
	_ = memoryHTML.Execute(&htmlBody, d)
	var text strings.Builder
	text.WriteString(d.Greeting + "\n\n" + d.Intro + "\n")
	for _, b := range d.Buttons {
		text.WriteString("\n" + b.Label + ": " + b.URL + "\n")
	}
	text.WriteString("\n-- \n" + d.Why + "\n" + d.Stop + ": " + manage + "\n")
	return notify.PersonalMail{To: p.Address, Name: p.Name, Subject: l.T("On this day: {years}", "years", when), Text: text.String(), HTML: htmlBody.String(), Inline: inline}
}

// --- the page a message leads to ---

// MemoriesOfDay lists the "on this day" memories Immich has for a day, with the person's own session: what the page
// "Send to someone" shows. The day is the one in the message.
func (h *Hub) MemoriesOfDay(ctx context.Context, sess *immich.API, day string) (time.Time, []immich.Memory, error) {
	d, err := time.Parse(MemoryDayLayout, day)
	if err != nil {
		return time.Time{}, nil, ErrBadMemoryDay
	}
	mems, err := sess.MemoriesFor(ctx, d)
	if err != nil {
		return d, nil, err
	}
	out := mems[:0]
	for _, m := range mems {
		if len(m.Photos) > 0 {
			out = append(out, m)
		}
	}
	return d, out, nil
}

// MemoryPhoto checks that a photo is in one of the person's memories for the day, so the page shows no other.
func (h *Hub) MemoryPhoto(ctx context.Context, sess *immich.API, day, asset string) (uuid.UUID, error) {
	id, err := uuid.Parse(asset)
	if err != nil {
		return uuid.Nil, ErrNotYourPhotos
	}
	_, mems, err := h.MemoriesOfDay(ctx, sess, day)
	if err != nil {
		return uuid.Nil, err
	}
	for _, m := range mems {
		for _, p := range m.Photos {
			if p == id {
				return id, nil
			}
		}
	}
	return uuid.Nil, ErrNotYourPhotos
}

// PushDevices is how many devices of the person can be pushed to.
func (h *Hub) PushDevices(ctx context.Context, user uuid.UUID) int {
	if h.Store == nil || !h.Push.Ready() {
		return 0
	}
	subs, err := h.Store.PushSubs(ctx, user)
	if err != nil {
		return 0
	}
	return len(subs)
}
