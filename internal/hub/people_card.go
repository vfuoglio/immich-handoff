package hub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

var (
	// ErrMailOff: this install has no mail server set aside for people.
	ErrMailOff = errors.New("email is not set up on this install")
	// ErrBadMailMode: the choice is not one of the two.
	ErrBadMailMode = errors.New("choose everything, or only what needs you")
	// ErrNoMailAddress: Immich shows no email address for the account.
	ErrNoMailAddress = errors.New("Immich has no email address for your account")
	// ErrMailNotOn: a test was asked for before mail was turned on.
	ErrMailNotOn = errors.New("turn email on first")
	// ErrMailTestSoon: one test a minute is enough.
	ErrMailTestSoon = errors.New("a test email was sent a moment ago; try again in a minute")
	// ErrMailSend: the mail server did not take the message (the reason follows).
	ErrMailSend = errors.New("the test email could not be sent")
)

// MailCard is what a person sees about their email.
type MailCard struct {
	Available bool // mail is set up on this install
	On        bool
	Mode      string
	Email     string // where it goes: the address Immich shows for their account
}

// MailCard says what the person's card should show. When mail is on, the address, name and language are brought up to
// date with what Immich and the page say now.
func (h *Hub) MailCard(ctx context.Context, me immich.User, lang string) MailCard {
	if h.Store == nil || !h.People.Ready() {
		return MailCard{}
	}
	c := MailCard{Available: true, Mode: store.MailAll, Email: me.Email}
	pm, ok, err := h.Store.PersonMail(ctx, uuid.UUID(me.ID))
	if err != nil || !ok {
		return c
	}
	c.On, c.Mode = true, pm.Mode
	if me.Email != "" && (pm.Email != me.Email || pm.Name != me.Name || pm.Lang != lang) {
		pm.Email, pm.Name, pm.Lang = me.Email, me.Name, lang
		if err := h.Store.SetPersonMail(ctx, pm); err != nil {
			h.Log.Warn("could not update a person's mail settings", "err", err)
		}
	}
	c.Email = pm.Email
	return c
}

// TurnMailOn turns email on for the signed-in person (or changes what they get).
func (h *Hub) TurnMailOn(ctx context.Context, me immich.User, lang, mode string) error {
	if h.Store == nil || !h.People.Ready() {
		return ErrMailOff
	}
	if mode != store.MailAll && mode != store.MailNeeds {
		return ErrBadMailMode
	}
	if strings.TrimSpace(me.Email) == "" {
		return ErrNoMailAddress
	}
	return h.Store.SetPersonMail(ctx, store.PersonMail{UserID: uuid.UUID(me.ID), Name: me.Name, Email: me.Email, Lang: lang, Mode: mode})
}

// TurnMailOff turns email off and forgets the address.
func (h *Hub) TurnMailOff(ctx context.Context, user uuid.UUID) error {
	if h.Store == nil {
		return ErrMailOff
	}
	return h.Store.DeletePersonMail(ctx, user)
}

// SendTestMail sends the person one mail now, so they know it arrives.
func (h *Hub) SendTestMail(ctx context.Context, me immich.User, lang string) error {
	if h.Store == nil || !h.People.Ready() {
		return ErrMailOff
	}
	pm, ok, err := h.Store.PersonMail(ctx, uuid.UUID(me.ID))
	if err != nil {
		return err
	}
	if !ok {
		return ErrMailNotOn
	}
	if !h.People.allowTest(pm.UserID) {
		return ErrMailTestSoon
	}
	pm.Lang = lang
	if err := h.People.SendTest(pm); err != nil {
		return fmt.Errorf("%w: %v", ErrMailSend, err)
	}
	return nil
}

func (p *People) allowTest(user uuid.UUID) bool {
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

// Unsubscribe turns mail off for the person a signed link was made for. It says nothing about whether they had it on.
func (h *Hub) Unsubscribe(ctx context.Context, token string) bool {
	if h.Store == nil {
		return false
	}
	id, ok := h.People.Verify(token)
	if !ok {
		return false
	}
	_ = h.Store.DeletePersonMail(ctx, id)
	return true
}

// ValidUnsubscribe reports whether a link in a mail is one we made.
func (h *Hub) ValidUnsubscribe(token string) bool {
	_, ok := h.People.Verify(token)
	return ok
}
