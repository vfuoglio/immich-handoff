package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// Sharing photos with another person, both ways (docs/design/pairing-design.md): the card on the Overview, the form that offers it,
// and the page the other person opens to say yes. The pool and the rules of a pairing are not shown anywhere else.

// PairingsView is the card on the Overview.
type PairingsView struct {
	Available bool
	Items     []PairingView
}

// PairingView is one sharing, as one of its two people sees it.
type PairingView struct {
	ID, Other, State string
	Starter          bool   // this person offered it (and owns the pool)
	Mine, Theirs     string // the people each way, in words (names only)
	Note             string // why it stopped, in the reader's language
	Expires          string // while it waits for a yes: when the offer lapses
	Incoming         bool   // an offer to this person that waits for their yes (it shows in Needs you)
	Link             string // for the starter, while it waits: the page the other person opens
	WaitMin          int    // the person's own wait before their rules offer a photo, when they chose one (0: the usual)
	Auto             bool   // the person's own rules here send by themselves
	Rules            []PairRuleView
}

// PairRuleView is one of the person's own rules in a pairing, for the look-back button.
type PairRuleView struct{ ID, Name string }

// PairingPageView is what the other person sees before they say yes.
type PairingPageView struct {
	ID, Starter, Theirs, Expires string
	People                       []PersonOption // the people in the reader's own library, to choose from
	WaitMin                      int            // the app's usual wait before a rule offers a photo, in minutes, for the form to say
}

func peopleWords(names []string) string { return strings.Join(names, ", ") }

// pairingPools is the names of the pools that belong to the person's pairings: they are shown on the pairing's card, nowhere else.
func (w *Server) pairingPools(ctx context.Context, user uuid.UUID) map[string]bool {
	if w.Hub == nil || w.Hub.Store == nil {
		return nil
	}
	list, err := w.Hub.PairingsFor(ctx, user)
	if err != nil || len(list) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, p := range list {
		out[p.Service] = true
	}
	return out
}

// visibleServices is the current services without the person's pairing pools.
func (w *Server) visibleServices(ctx context.Context, m *member) []*syncer.Service {
	pools := w.pairingPools(ctx, m.user.ID)
	var out []*syncer.Service
	for _, s := range w.S.Current() {
		if !pools[s.Label()] {
			out = append(out, s)
		}
	}
	return out
}

func (w *Server) pairingViews(ctx context.Context, me *member) PairingsView {
	v := PairingsView{Available: w.Hub != nil && w.Hub.SelfService()}
	if !v.Available {
		return v
	}
	list, err := w.Hub.PairingsFor(ctx, me.user.ID)
	if err != nil {
		return v
	}
	for _, p := range list {
		starter := p.AID == me.user.ID
		it := PairingView{ID: p.ID, State: p.State, Starter: starter, Other: p.AName, Note: unpack(ctx, p.Note)}
		if starter {
			it.Other = p.BName
		}
		mine, theirs, _ := w.Hub.PairingPeople(ctx, me.user.ID, p.ID)
		it.Mine, it.Theirs = peopleWords(mine), peopleWords(theirs)
		if p.State == store.PairingInvited {
			it.Expires = loc(ctx).Stamp(p.ExpiresAt.Local())
			if starter {
				it.Link = "pair/" + p.ID
			} else if time.Now().Before(p.ExpiresAt) {
				it.Incoming = true
			}
		}
		if p.State != store.PairingStopped {
			if rules, err := w.Hub.PairingRules(ctx, me.user.ID, p.ID); err == nil {
				for _, r := range rules {
					it.WaitMin = r.SettleMinutes // each person makes their rules with one wait
					it.Auto = r.Mode == store.RuleAuto
					if p.State == store.PairingInvited {
						continue // nothing to look back at before the yes
					}
					var names []string
					for _, x := range r.Cond.People {
						names = append(names, x.Name)
					}
					it.Rules = append(it.Rules, PairRuleView{ID: r.ID, Name: peopleWords(names)})
				}
			}
		}
		v.Items = append(v.Items, it)
	}
	return v
}

// pairForm is the form that offers a sharing: who with, and which of the person's own people go to them.
func (w *Server) pairForm(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	if w.Hub == nil || !w.Hub.SelfService() {
		http.NotFound(rw, r)
		return
	}
	people, err := me.sess.People(r.Context())
	if err != nil {
		http.Error(rw, "could not list your people", http.StatusBadGateway)
		return
	}
	var opts []PersonOption
	for _, p := range people {
		opts = append(opts, PersonOption{ID: uuid.UUID(p.ID).String(), Name: p.Name})
	}
	rw.Header().Set("Cache-Control", "no-store")
	PairForm(opts, w.waitMinutes()).Render(r.Context(), rw)
}

func (w *Server) startPairing(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "sharing offered", func(ctx context.Context, me *member, _ string) error {
		if err := r.ParseForm(); err != nil {
			return err
		}
		other, ok, err := me.sess.FindUserByEmail(ctx, strings.TrimSpace(r.PostFormValue("email")))
		if err != nil {
			return err
		}
		if !ok {
			return hub.ErrNoSuchPerson
		}
		settle, err := settleOf(r)
		if err != nil {
			return err
		}
		_, _, err = w.Hub.StartPairing(ctx, me.sess, me.user, hub.PairingParams{With: other, People: ids(r, "person"), SettleMinutes: settle})
		return err
	})
}

func (w *Server) acceptPairing(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "sharing accepted", func(ctx context.Context, me *member, _ string) error {
		if err := r.ParseForm(); err != nil {
			return err
		}
		settle, err := settleOf(r)
		if err != nil {
			return err
		}
		return w.Hub.AcceptPairing(ctx, me.sess, me.user, r.PostFormValue("pairing"), ids(r, "person"), settle)
	})
}

func (w *Server) pausePairing(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "sharing paused", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.PausePairing(ctx, me.user, r.PostFormValue("pairing"), r.PostFormValue("paused") == "1")
	})
}

func (w *Server) autoPairing(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "sharing automatic", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.SetPairingAuto(ctx, me.user, r.PostFormValue("pairing"), r.PostFormValue("on") == "1")
	})
}

func (w *Server) stopPairing(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "sharing stopped", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.StopPairing(ctx, me.sess, me.user, r.PostFormValue("pairing"))
	})
}

// pairingPage is what the other person opens from the link: the offer, and what saying yes means.
func (w *Server) pairingPage(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	me, code, _ := w.authenticate(r)
	if me == nil {
		rw.WriteHeader(code)
		Notice(t(ctx, "Sign in to Immich first"),
			t(ctx, "This invitation needs your Immich login. Sign in to Immich in this browser, then open the link again."), false).Render(ctx, rw)
		return
	}
	gone := func() {
		rw.WriteHeader(http.StatusNotFound)
		Notice(t(ctx, "Invitation"), t(ctx, "This invitation is not valid any more. Ask the person who sent it for a new link."), true).Render(ctx, rw)
	}
	if w.Hub == nil || !w.Hub.SelfService() {
		gone()
		return
	}
	p, err := w.Hub.PairingFor(ctx, me.user.ID, r.PathValue("id"))
	if err != nil {
		gone()
		return
	}
	home := func() { // relative, so it also works behind a path prefix (http.Redirect would make it absolute)
		rw.Header().Set("Location", "../")
		rw.WriteHeader(http.StatusSeeOther)
	}
	if p.AID == me.user.ID || p.State == store.PairingActive || p.State == store.PairingPaused {
		home() // the starter has the card, and the other person has said yes already
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	switch {
	case p.State == store.PairingStopped:
		Notice(t(ctx, "Photo sharing"), t(ctx, "This sharing has ended: {reason}", "reason", unpack(ctx, p.Note)), true).Render(ctx, rw)
		return
	case time.Now().After(p.ExpiresAt):
		Notice(t(ctx, "Invitation"), t(ctx, "This invitation has lapsed. Ask {name} for a new one.", "name", p.AName), true).Render(ctx, rw)
		return
	}
	_, theirs, _ := w.Hub.PairingPeople(ctx, me.user.ID, p.ID)
	v := PairingPageView{ID: p.ID, Starter: p.AName, Theirs: peopleWords(theirs), Expires: loc(ctx).Stamp(p.ExpiresAt.Local()), WaitMin: w.waitMinutes()}
	if people, err := me.sess.People(ctx); err == nil {
		for _, x := range people {
			v.People = append(v.People, PersonOption{ID: uuid.UUID(x.ID).String(), Name: x.Name})
		}
	}
	PairingPage(v, me.user.Name).Render(ctx, rw)
}

// pairingOther is the name of the other person of a pairing, for a card that would otherwise show the pool's generated name.
func (w *Server) pairingOther(ctx context.Context, user uuid.UUID, pairing, fallback string) string {
	if p, err := w.Hub.PairingFor(ctx, user, pairing); err == nil {
		if p.AID == user {
			return p.BName
		}
		return p.AName
	}
	return fallback
}
