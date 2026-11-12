package hub

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/push"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// PushSender is what Push needs from the push client.
type PushSender interface {
	Send(ctx context.Context, sub push.Subscription, payload []byte, o push.Options) error
}

var (
	// ErrPushOff: this install does not send pushes (no contact for the push services, or switched off).
	ErrPushOff = errors.New("notifications on devices are not set up on this install")
	// ErrBadPushDevice: what the browser gave is not a usable subscription.
	ErrBadPushDevice = errors.New("that device cannot be used for notifications")
	// ErrBadPushHost: the browser's push service is not one we send to.
	ErrBadPushHost = errors.New("this browser uses a push service that is not supported")
	// ErrTooManyDevices: the person already allowed as many devices as there can be.
	ErrTooManyDevices = errors.New("you already have 10 devices; remove one first")
	// ErrNoDevice: the device is not one of the person's.
	ErrNoDevice = errors.New("that device is not in your list")
	// ErrPushTestSoon: one test a minute is enough.
	ErrPushTestSoon = errors.New("a test notification was sent a moment ago; try again in a minute")
	// ErrPushSend: the push service did not take the test (the reason follows).
	ErrPushSend = errors.New("the test notification could not be sent")
)

// Push sends notifications to the devices people allowed. What a person is told is what the page writes to their recent
// events (the same lines, for the same people, in the language each device was set up in) plus short summaries of
// photos that arrived or were refused. Lines are gathered for a few seconds, so a burst is one notification.
type Push struct {
	Store  *store.Store
	Sender PushSender
	Key    string // the public half of the signing key, as the browser wants it
	Log    *slog.Logger
	Window time.Duration // how long lines wait for company (default 10 seconds)
	Now    func() time.Time

	mu     sync.Mutex
	queues map[uuid.UUID]*pushQueue
	hourly map[string][]time.Time
	tested map[uuid.UUID]time.Time
	closed bool
}

type pushLine struct {
	kind, text string
	links      []ActionLink
}

type pushQueue struct {
	lines []pushLine
	timer *time.Timer
}

// newDeviceGrace is how long a new device is kept even if its push service says it is gone: a subscription made a moment
// ago is sometimes not known to the service for a few seconds.
const newDeviceGrace = 2 * time.Minute

// perDeviceHour is the most pushes one device gets in an hour, whatever happens.
const perDeviceHour = 30

// pushNeeds are the kinds a device set to "only what needs me" is told.
func pushNeeds(kind string) bool { return needsKinds[kind] || kind == "refused" }

// Ready reports whether pushes can be sent at all.
func (p *Push) Ready() bool { return p != nil && p.Store != nil && p.Sender != nil && p.Key != "" }

func (p *Push) window() time.Duration {
	if p.Window > 0 {
		return p.Window
	}
	return 10 * time.Second
}

func (p *Push) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Push) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

// Tell is called for every line written for a service, with the people who may read it, and for arrival summaries.
func (p *Push) Tell(ctx context.Context, audience []uuid.UUID, kind, text string) {
	p.TellLinks(ctx, audience, kind, text, nil)
}

// TellLinks is Tell with one-tap links: links(user) is asked only for the people who have a device that wants the line.
func (p *Push) TellLinks(ctx context.Context, audience []uuid.UUID, kind, text string, links func(uuid.UUID) []ActionLink) {
	if !p.Ready() {
		return
	}
	for _, id := range audience {
		subs, err := p.Store.PushSubs(ctx, id)
		if err != nil {
			continue
		}
		wanted := false
		for _, s := range subs {
			if s.Mode == store.MailAll || pushNeeds(kind) {
				wanted = true
			}
		}
		if wanted {
			line := pushLine{kind: kind, text: text}
			if links != nil {
				line.links = links(id)
			}
			p.queue(id, line)
		}
	}
}

func (p *Push) queue(id uuid.UUID, l pushLine) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if p.queues == nil {
		p.queues = map[uuid.UUID]*pushQueue{}
	}
	q := p.queues[id]
	if q == nil {
		q = &pushQueue{}
		p.queues[id] = q
	}
	q.lines = append(q.lines, l)
	if q.timer == nil {
		q.timer = time.AfterFunc(p.window(), func() { p.flush(id) })
	}
}

func (p *Push) flush(id uuid.UUID) {
	p.mu.Lock()
	q := p.queues[id]
	if q == nil || len(q.lines) == 0 {
		p.mu.Unlock()
		return
	}
	lines := q.lines
	delete(p.queues, id)
	p.mu.Unlock()

	ctx := context.Background()
	subs, err := p.Store.PushSubs(ctx, id)
	if err != nil {
		p.log().Warn("could not read a person's devices", "err", err)
		return
	}
	for _, s := range subs {
		var mine []pushLine
		for _, l := range lines {
			if s.Mode == store.MailAll || pushNeeds(l.kind) {
				mine = append(mine, l)
			}
		}
		if len(mine) == 0 || !p.underCap(s.ID) {
			continue
		}
		p.deliver(ctx, s, composePush(s.Lang, mine))
	}
}

// underCap counts a push against the device's hour and says whether there is room.
func (p *Push) underCap(deviceID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hourly == nil {
		p.hourly = map[string][]time.Time{}
	}
	now := p.now()
	recent := p.hourly[deviceID][:0:0]
	for _, t := range p.hourly[deviceID] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if len(recent) >= perDeviceHour {
		p.hourly[deviceID] = recent
		return false
	}
	p.hourly[deviceID] = append(recent, now)
	return true
}

// deliver sends one notification to one device and keeps the device list honest: a device the push service says is
// gone is forgotten, and one that keeps failing is given up on.
func (p *Push) deliver(ctx context.Context, s store.PushSub, payload []byte) error {
	err := p.Sender.Send(ctx, push.Subscription{Endpoint: s.Endpoint, P256dh: s.P256dh, Auth: s.Auth}, payload, push.Options{TTL: 12 * time.Hour})
	switch {
	case err == nil:
		_ = p.Store.PushMarkOK(ctx, s.ID, p.now())
	case errors.Is(err, push.ErrGone) && p.now().Sub(s.CreatedAt) < newDeviceGrace:
		// a subscription made a moment ago may not be known to the push service yet: do not forget the device for that
		p.log().Debug("a new device was not known to its push service yet")
	case errors.Is(err, push.ErrGone):
		_ = p.Store.PushRemoveID(ctx, s.ID)
	default:
		p.log().Warn("a push did not get through", "err", err) // never the address
		if n, e := p.Store.PushMarkFailed(ctx, s.ID); e == nil && n >= 3 {
			_ = p.Store.PushRemoveID(ctx, s.ID)
		}
	}
	return err
}

// Close sends what is waiting, once, and takes no more.
func (p *Push) Close() {
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

// composePush writes the notification in the device's language: the line itself, or how many things happened and the
// first few. Nothing in it names a photo.
func composePush(lang string, lines []pushLine) []byte {
	code, _ := i18n.Supported(lang)
	l := i18n.For(code)
	var body string
	if len(lines) == 1 {
		body = l.Unpack(lines[0].text)
	} else {
		parts := []string{l.T("{n} things happened:", "n", len(lines))}
		for i, ln := range lines {
			if i == 3 {
				parts = append(parts, "…")
				break
			}
			parts = append(parts, l.Unpack(ln.text))
		}
		body = strings.Join(parts, "\n")
	}
	var links []ActionLink
	if len(lines) == 1 { // buttons belong to one thing; a gathered notification just opens the page
		links = lines[0].links
	}
	return pushPayload(body, links, l)
}

// pushPayload is what the service worker is given. links become buttons (a browser that cannot show buttons, such as
// Safari, still opens the page when the notification is tapped); l translates their labels.
func pushPayload(body string, links []ActionLink, l *i18n.Locale) []byte {
	if r := []rune(body); len(r) > 300 {
		body = string(r[:299]) + "…"
	}
	m := map[string]any{"title": "Immich Handoff", "body": body, "tag": "handoff", "url": "./"}
	if len(links) > 2 {
		links = links[:2] // a notification shows at most two buttons
	}
	if len(links) > 0 && l != nil {
		var actions []map[string]string
		urls := map[string]string{}
		for i, a := range links {
			id := fmt.Sprintf("a%d", i)
			actions = append(actions, map[string]string{"action": id, "title": l.T(a.Label)})
			urls[id] = a.URL
		}
		m["actions"], m["urls"] = actions, urls
	}
	b, _ := json.Marshal(m)
	return b
}

// --- arrivals ---

var _ syncer.ArrivalReporter = (*Hub)(nil)

// HandedOver implements syncer.ArrivalReporter: the receivers and the owner hear that photos arrived, the sender that
// some were refused. These go to devices only; they are not lines of the activity list.
func (h *Hub) HandedOver(svc *syncer.Service, snd *syncer.Account, imported, refused int) {
	if h.Push == nil {
		return
	}
	ctx := context.Background()
	if imported > 0 {
		seen := map[uuid.UUID]bool{snd.UserID: true}
		var who []uuid.UUID
		add := func(id uuid.UUID) {
			if id != uuid.Nil && !seen[id] {
				seen[id] = true
				who = append(who, id)
			}
		}
		for _, r := range svc.Receivers {
			add(r.UserID)
		}
		add(svc.OwnerID)
		h.Push.Tell(ctx, who, "arrived", i18n.PackN("{n} photo arrived from {name} in {service}", "{n} photos arrived from {name} in {service}", imported,
			"name", snd.Name, "service", svc.Label()))
	}
	if refused > 0 && snd.UserID != uuid.Nil {
		h.Push.Tell(ctx, []uuid.UUID{snd.UserID}, "refused", i18n.PackN("{n} photo was not handed over to {service}: the receiver has it in the trash",
			"{n} photos were not handed over to {service}: the receiver has them in the trash", refused, "service", svc.Label()))
	}
}

// --- the card ---

// PushDevice is one of a person's devices as the card shows it.
type PushDevice struct {
	ID, Label, Mode string
	Created, LastOK time.Time
}

// PushCard is what a person sees about their devices.
type PushCard struct {
	Available bool
	Key       string
	Devices   []PushDevice
}

// PushCard says what the card shows.
func (h *Hub) PushCard(ctx context.Context, user uuid.UUID) PushCard {
	if h.Store == nil || !h.Push.Ready() {
		return PushCard{}
	}
	c := PushCard{Available: true, Key: h.Push.Key}
	subs, err := h.Store.PushSubs(ctx, user)
	if err != nil {
		return c
	}
	for _, s := range subs {
		c.Devices = append(c.Devices, PushDevice{ID: s.ID, Label: s.Label, Mode: s.Mode, Created: s.CreatedAt, LastOK: s.LastOK})
	}
	return c
}

// PushKey is the public key a browser needs to subscribe, or "" when pushes are off.
func (h *Hub) PushKey() string {
	if !h.Push.Ready() {
		return ""
	}
	return h.Push.Key
}

// PushDeviceInput is what a browser gives when it agrees to notifications.
type PushDeviceInput struct {
	Endpoint, P256dh, Auth string // the keys in base64url
	Lang, Mode, UserAgent  string
}

// PushSubscribe stores a device for the person.
func (h *Hub) PushSubscribe(ctx context.Context, user uuid.UUID, in PushDeviceInput) (string, error) {
	if h.Store == nil || !h.Push.Ready() {
		return "", ErrPushOff
	}
	if in.Mode != store.MailAll && in.Mode != store.MailNeeds {
		return "", ErrBadMailMode
	}
	if len(in.Endpoint) > 2048 || push.AllowedEndpoint(in.Endpoint) != nil {
		return "", ErrBadPushHost
	}
	key, err1 := base64.RawURLEncoding.DecodeString(strings.TrimRight(in.P256dh, "="))
	auth, err2 := base64.RawURLEncoding.DecodeString(strings.TrimRight(in.Auth, "="))
	if err1 != nil || err2 != nil || len(auth) != 16 {
		return "", ErrBadPushDevice
	}
	if _, err := ecdh.P256().NewPublicKey(key); err != nil {
		return "", ErrBadPushDevice
	}
	lang, _ := i18n.Supported(in.Lang)
	id, err := h.Store.PushSubscribe(ctx, store.PushSub{UserID: user, Endpoint: in.Endpoint, P256dh: key, Auth: auth,
		Lang: string(lang), Mode: in.Mode, Label: deviceLabel(in.UserAgent)})
	if errors.Is(err, store.ErrTooManyDevices) {
		return "", ErrTooManyDevices
	}
	return id, err
}

// PushRemove forgets one of the person's devices.
func (h *Hub) PushRemove(ctx context.Context, user uuid.UUID, id string) error {
	if h.Store == nil {
		return ErrPushOff
	}
	if ok, err := h.Store.PushRemove(ctx, user, id); err != nil {
		return err
	} else if !ok {
		return ErrNoDevice
	}
	return nil
}

// PushSetMode changes what one of the person's devices is told.
func (h *Hub) PushSetMode(ctx context.Context, user uuid.UUID, id, mode string) error {
	if h.Store == nil {
		return ErrPushOff
	}
	if mode != store.MailAll && mode != store.MailNeeds {
		return ErrBadMailMode
	}
	if ok, err := h.Store.PushSetMode(ctx, user, id, mode); err != nil {
		return err
	} else if !ok {
		return ErrNoDevice
	}
	return nil
}

// PushTest sends a notification to one of the person's devices now, in the language it was set up in.
func (h *Hub) PushTest(ctx context.Context, me immich.User, id string) error {
	if h.Store == nil || !h.Push.Ready() {
		return ErrPushOff
	}
	user := uuid.UUID(me.ID)
	subs, err := h.Store.PushSubs(ctx, user)
	if err != nil {
		return err
	}
	for _, s := range subs {
		if s.ID != id {
			continue
		}
		if !h.Push.allowTest(user) {
			return ErrPushTestSoon
		}
		code, _ := i18n.Supported(s.Lang)
		if err := h.Push.deliver(ctx, s, pushPayload(i18n.For(code).T("Notifications work on this device."), nil, nil)); err != nil {
			return errors.Join(ErrPushSend, err)
		}
		return nil
	}
	return ErrNoDevice
}

func (p *Push) allowTest(user uuid.UUID) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tested == nil {
		p.tested = map[uuid.UUID]time.Time{}
	}
	now := p.now()
	if t, ok := p.tested[user]; ok && now.Sub(t) < time.Minute {
		return false
	}
	p.tested[user] = now
	return true
}

// deviceLabel is a short name for a device, made from what the browser says about itself: which browser, on what.
func deviceLabel(ua string) string {
	browser := "Browser"
	for _, b := range []struct{ marker, name string }{{"Edg/", "Edge"}, {"SamsungBrowser", "Samsung Internet"}, {"OPR/", "Opera"}, {"Firefox/", "Firefox"},
		{"Chrome/", "Chrome"}, {"CriOS/", "Chrome"}, {"FxiOS/", "Firefox"}, {"Safari/", "Safari"}} {
		if strings.Contains(ua, b.marker) {
			browser = b.name
			break
		}
	}
	system := ""
	for _, o := range []struct{ marker, name string }{{"Android", "Android"}, {"iPhone", "iPhone"}, {"iPad", "iPad"}, {"Windows", "Windows"},
		{"Mac OS X", "Mac"}, {"CrOS", "ChromeOS"}, {"Linux", "Linux"}} {
		if strings.Contains(ua, o.marker) {
			system = o.name
			break
		}
	}
	if system == "" {
		return browser
	}
	return browser + " on " + system
}
