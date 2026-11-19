package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// A pairing is two people sharing the photos of chosen people with each other, both ways, in one step (docs/design/pairing-design.md,
// #229). It is made of a private two-member event pool and a rule "photos of this person" for each person chosen on either side;
// a small record (store.Pairing) ties them together, and the pool and the rules carry its id so that they start, pause and stop
// together and cannot be changed one by one. The transfer is the one that always was: the sender's key tags, the receiver's pass
// copies. Nothing here deletes a photo from anyone's library.

var (
	ErrLockedByPairing = errors.New("that is part of your photo sharing with someone: change it or stop it from the sharing's own card")
	ErrNoPairing       = errors.New("that is not one of your photo sharings")
	ErrPairingSelf     = errors.New("you cannot share your photos with yourself")
	ErrPairingPeople   = errors.New("choose between one and ten named people, each once")
	ErrTooManyPairings = errors.New("you share with as many people as you can at once: stop one first")
	ErrPairingState    = errors.New("that cannot be done to this sharing as it is now")
	ErrPairingLapsed   = errors.New("that invitation has lapsed: ask for a new one")
)

const (
	maxPairingPeople = 10 // people that go one way
	maxPairings      = 5  // sharings a person can be in at once
)

// pairingOp marks a call made by the pairing itself, which may touch its own locked pieces.
type pairingOp struct{}

func asPairing(ctx context.Context) context.Context { return context.WithValue(ctx, pairingOp{}, true) }
func isPairingOp(ctx context.Context) bool          { return ctx.Value(pairingOp{}) != nil }

// lockedService refuses a change to a service that belongs to a pairing, unless the pairing itself makes it.
func (h *Hub) lockedService(ctx context.Context, service string) error {
	if h.Store == nil || isPairingOp(ctx) {
		return nil
	}
	if _, err := h.Store.PairingOfService(ctx, service); err == nil {
		return ErrLockedByPairing
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}

// PairingParams is what the person who starts a pairing chooses.
type PairingParams struct {
	With   immich.User // the other person (id, name and email, as the server lists them)
	People []uuid.UUID // the people in the starter's own library whose photos go to the other person
	// SettleMinutes is how long a photo waits before the starter's rules offer it: 0 is the usual wait, otherwise 1 to 60.
	SettleMinutes int
}

// StartPairing makes the private pool and the starter's rules, and an invitation for the other person. Nothing is made for the
// other person, and nothing is handed over, until they say yes (AcceptPairing). It returns the pairing and the invitation's token.
func (h *Hub) StartPairing(ctx context.Context, sess *immich.API, a immich.User, p PairingParams) (store.Pairing, string, error) {
	var none store.Pairing
	if !h.SelfService() {
		return none, "", ErrNotJoinable
	}
	if p.With.ID == uuid.Nil || p.With.ID == a.ID {
		return none, "", ErrPairingSelf
	}
	if err := h.checkPairingPeople(ctx, sess, p.People); err != nil {
		return none, "", err
	}
	if p.SettleMinutes < 0 || p.SettleMinutes > maxSettleMinutes { // before anything is made
		return none, "", ErrBadSettle
	}
	if err := h.pairingRoom(ctx, a.ID); err != nil {
		return none, "", err
	}
	name := newPairingName()
	ctx = asPairing(ctx)
	if err := h.CreateService(ctx, sess, a, CreateParams{Name: name, Tag: name, Policy: store.PolicyOpen, Visibility: store.VisInvite, Pool: true}); err != nil {
		return none, "", err
	}
	var pr store.Pairing
	undo := func() { // leaves nothing behind
		if pr.ID != "" {
			_ = h.Store.RemovePairing(ctx, pr.ID)
		}
		_ = h.Store.DeleteRulesOf(ctx, a.ID, name)
		if err := h.DeleteService(ctx, sess, a, name); err != nil {
			h.Log.Warn("pairing: could not undo the pool", "service", name, "err", err)
		}
	}
	svc, err := h.storedService(ctx, name)
	if err != nil {
		undo()
		return none, "", err
	}
	if pr, err = h.Store.AddPairing(ctx, store.Pairing{Service: name, AID: a.ID, AName: a.Name, BID: p.With.ID, BName: p.With.Name}); err != nil {
		undo()
		return none, "", err
	}
	if err := h.Store.SetServicePairing(ctx, name, pr.ID); err != nil {
		undo()
		return none, "", err
	}
	if err := h.addPairingRules(ctx, sess, a, pr, p.People, p.SettleMinutes); err != nil {
		undo()
		return none, "", err
	}
	if err := h.Store.SetPairingRulesPaused(ctx, pr.ID, true); err != nil { // they start when the other person says yes
		undo()
		return none, "", err
	}
	h.event(ctx, name, a.ID, "pairing", i18n.Pack("{name} offered to share photos with {other}, both ways", "name", a.Name, "other", p.With.Name))
	h.Log.Info("pairing offered", "pairing", pr.ID, "by", a.Email)
	return pr, svc.Invite, nil
}

// AcceptPairing is the other person's yes: they join the pool with their own key, choose which of their own people go back, and
// both sides' rules start looking from this moment on. Only the person the pairing was offered to can accept it.
func (h *Hub) AcceptPairing(ctx context.Context, sess *immich.API, b immich.User, id string, people []uuid.UUID, settle int) error {
	if !h.SelfService() {
		return ErrNotJoinable
	}
	p, err := h.pairingOf(ctx, b.ID, id)
	if err != nil {
		return err
	}
	if p.BID != b.ID {
		return ErrNoPairing // only the invited person can say yes
	}
	if p.State != store.PairingInvited {
		return ErrPairingState
	}
	if h.now().After(p.ExpiresAt) {
		h.markStopped(ctx, p, i18n.Pack("The invitation lapsed before {name} said yes", "name", p.BName))
		return ErrPairingLapsed
	}
	if err := h.checkPairingPeople(ctx, sess, people); err != nil {
		return err
	}
	if settle < 0 || settle > maxSettleMinutes { // before they join anything
		return ErrBadSettle
	}
	if err := h.pairingRoom(ctx, b.ID); err != nil {
		return err
	}
	svc, err := h.storedService(ctx, p.Service)
	if err != nil {
		return ErrPairingState
	}
	ctx = asPairing(ctx)
	if err := h.JoinWith(ctx, sess, b, p.Service, svc.Invite, syncer.ModeCopy, true); err != nil {
		return err
	}
	if err := h.addPairingRules(ctx, sess, b, p, people, settle); err != nil {
		if lerr := h.Leave(ctx, sess, b, p.Service); lerr != nil { // takes the key and the link back, and the rules made so far
			h.Log.Warn("pairing: could not undo the join", "pairing", p.ID, "err", lerr)
		}
		return err
	}
	if err := h.Store.StartPairingRules(ctx, p.ID, h.now()); err != nil { // both sides, from now on
		return err
	}
	if err := h.Store.SetPairingState(ctx, p.ID, store.PairingActive, ""); err != nil {
		return err
	}
	h.event(ctx, p.Service, b.ID, "pairing", i18n.Pack("{name} said yes: {other} and {name} now share photos, both ways", "name", b.Name, "other", p.AName))
	h.Log.Info("pairing accepted", "pairing", p.ID, "by", b.Email)
	return h.Reload(ctx)
}

// PausePairing stops (or restarts) what is handed over, both ways, until someone resumes it. Either person can.
func (h *Hub) PausePairing(ctx context.Context, user immich.User, id string, paused bool) error {
	p, err := h.pairingOf(ctx, user.ID, id)
	if err != nil {
		return err
	}
	want, from := store.PairingActive, store.PairingPaused
	if paused {
		want, from = store.PairingPaused, store.PairingActive
	}
	if p.State == want {
		return nil
	}
	if p.State != from {
		return ErrPairingState
	}
	if err := h.Store.SetServicePaused(ctx, p.Service, paused); err != nil {
		return err
	}
	if err := h.Store.SetPairingRulesPaused(ctx, p.ID, paused); err != nil {
		return err
	}
	if err := h.Store.SetPairingState(ctx, p.ID, want, ""); err != nil {
		return err
	}
	word := "resumed"
	if paused {
		word = "paused"
	}
	h.event(ctx, p.Service, user.ID, "pairing", i18n.Pack("{name} "+word+" the sharing of photos with {other}", "name", user.Name, "other", pairingOther(p, user.ID)))
	return h.Reload(ctx)
}

// SetPairingAuto switches the person's own rules of a sharing between sending by themselves and asking first. The switch is the
// person's own: each side chooses for their own rules, and it covers only photos that reach Immich after it is switched on.
func (h *Hub) SetPairingAuto(ctx context.Context, user immich.User, id string, on bool) error {
	p, err := h.pairingOf(ctx, user.ID, id)
	if err != nil {
		return err
	}
	if p.State != store.PairingActive && p.State != store.PairingPaused {
		return ErrPairingState
	}
	rules, err := h.PairingRules(ctx, user.ID, p.ID)
	if err != nil {
		return err
	}
	changed := false
	for _, r := range rules {
		if (r.Mode == store.RuleAuto) == on {
			continue
		}
		if err := h.SetRuleAuto(asPairing(ctx), user, r.ID, on); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		word := "made the sharing of photos with {other} ask first again"
		if on {
			word = "let the sharing of photos with {other} send by itself"
		}
		h.event(ctx, p.Service, user.ID, "pairing", i18n.Pack("{name} "+word, "name", user.Name, "other", pairingOther(p, user.ID)))
	}
	return nil
}

// StopPairing ends the sharing. Every copy stays where it is and nothing is deleted from anyone's library.
//   - The starter's own side is taken apart at once: the pool, its key and workflow in their Immich, every rule on both sides and
//     the record. The other person's key in their own Immich can only be deleted by them: it shows in their list of services
//     that were removed, and Clean up deletes it, as for any removed service.
//   - The other person's stop leaves their own side clean (their key, workflow and rules) and stops what is handed over at once;
//     the starter's pieces are paused and wait for the starter to take them apart on their next visit, because only the
//     starter's own sign-in can delete their key.
func (h *Hub) StopPairing(ctx context.Context, sess *immich.API, user immich.User, id string) error {
	p, err := h.pairingOf(ctx, user.ID, id)
	if err != nil {
		return err
	}
	ctx = asPairing(ctx)
	if user.ID == p.AID {
		if id, err := sess.FindWorkflow(ctx, workflowName(p.Service)); err != nil {
			h.Log.Warn("pairing: could not look up the workflow", "err", err)
		} else if id != nil {
			if err := sess.DeleteWorkflow(ctx, *id); err != nil {
				h.Log.Warn("pairing: could not delete the workflow", "err", err)
			}
		}
		if h.service(p.Service) != nil {
			if err := h.DeleteService(ctx, sess, user, p.Service); err != nil {
				return err
			}
		}
		if err := h.Store.RemovePairing(ctx, p.ID); err != nil { // both sides' rules go with it
			return err
		}
		h.event(ctx, p.Service, user.ID, "pairing", i18n.Pack("{name} stopped sharing photos with {other}", "name", user.Name, "other", p.BName))
		h.Log.Info("pairing stopped", "pairing", p.ID, "by", user.Email)
		return h.Reload(ctx)
	}
	if p.State == store.PairingStopped {
		return nil // already stopped; the starter takes their side apart
	}
	note := i18n.Pack("{name} stopped sharing photos with you", "name", user.Name)
	if p.State == store.PairingInvited {
		note = i18n.Pack("{name} declined the invitation", "name", user.Name)
	} else if err := h.Leave(ctx, sess, user, p.Service); err != nil && !errors.Is(err, ErrNotJoined) {
		return err
	}
	h.markStopped(ctx, p, note)
	h.Log.Info("pairing stopped by the other person", "pairing", p.ID, "by", user.Email)
	return nil
}

// ExpirePairings stops the invitations nobody answered in time. The starter's pieces wait to be taken apart by them.
func (h *Hub) ExpirePairings(ctx context.Context) {
	if h.Store == nil || !h.SelfService() {
		return
	}
	all, err := h.Store.Pairings(ctx)
	if err != nil {
		return
	}
	for _, p := range all {
		if p.State == store.PairingInvited && h.now().After(p.ExpiresAt) {
			h.markStopped(ctx, p, i18n.Pack("The invitation lapsed before {name} said yes", "name", p.BName))
		}
	}
}

// PairingsFor lists the pairings a person is one of the two in.
func (h *Hub) PairingsFor(ctx context.Context, user uuid.UUID) ([]store.Pairing, error) {
	return h.Store.PairingsOf(ctx, user)
}

// PairingFor finds a pairing the person is one of the two in (ErrNoPairing for anyone else).
func (h *Hub) PairingFor(ctx context.Context, user uuid.UUID, id string) (store.Pairing, error) {
	return h.pairingOf(ctx, user, id)
}

// PairingPeople names the people that go each way, as the two chose them: mine are the ones the person sends, theirs the ones the other
// person sends. Names only; a face is shown to the person it belongs to and nobody else.
func (h *Hub) PairingPeople(ctx context.Context, user uuid.UUID, id string) (mine, theirs []string, err error) {
	p, err := h.pairingOf(ctx, user, id)
	if err != nil {
		return nil, nil, err
	}
	rules, err := h.Store.RulesOfPairing(ctx, p.ID)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range rules {
		for _, x := range r.Cond.People {
			if r.OwnerID == user {
				mine = append(mine, x.Name)
			} else {
				theirs = append(theirs, x.Name)
			}
		}
	}
	return mine, theirs, nil
}

// PairingRules lists the rules of a pairing that belong to the person: their own side.
func (h *Hub) PairingRules(ctx context.Context, user uuid.UUID, id string) ([]store.Rule, error) {
	p, err := h.pairingOf(ctx, user, id)
	if err != nil {
		return nil, err
	}
	all, err := h.Store.RulesOfPairing(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	var mine []store.Rule
	for _, r := range all {
		if r.OwnerID == user {
			mine = append(mine, r)
		}
	}
	return mine, nil
}

// markStopped records that the sharing ended and stops everything at once, without needing anyone's sign-in.
func (h *Hub) markStopped(ctx context.Context, p store.Pairing, note string) {
	if err := h.Store.SetPairingState(ctx, p.ID, store.PairingStopped, note); err != nil {
		h.Log.Warn("pairing: could not mark it stopped", "pairing", p.ID, "err", err)
	}
	if err := h.Store.SetServicePaused(ctx, p.Service, true); err != nil {
		h.Log.Warn("pairing: could not pause the pool", "pairing", p.ID, "err", err)
	}
	if err := h.Store.SetPairingRulesPaused(ctx, p.ID, true); err != nil {
		h.Log.Warn("pairing: could not pause the rules", "pairing", p.ID, "err", err)
	}
	h.event(ctx, p.Service, p.AID, "pairing", note)
	_ = h.Reload(ctx)
}

// pairingStoppedByAdmin is for an administrator removing a member of a pool that belongs to a pairing: the sharing stops.
func (h *Hub) pairingStoppedByAdmin(ctx context.Context, service string) {
	if p, err := h.Store.PairingOfService(ctx, service); err == nil && p.State != store.PairingStopped {
		h.markStopped(ctx, p, i18n.Pack("An administrator removed someone from this sharing"))
	}
}

// pairingOf finds a pairing the person is one of the two in; for anyone else it is not found.
func (h *Hub) pairingOf(ctx context.Context, user uuid.UUID, id string) (store.Pairing, error) {
	p, err := h.Store.PairingByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Pairing{}, ErrNoPairing
		}
		return store.Pairing{}, err
	}
	if !p.Takes(user) {
		return store.Pairing{}, ErrNoPairing
	}
	return p, nil
}

// pairingRoom refuses a person who is in as many sharings as allowed (a stopped one does not count).
func (h *Hub) pairingRoom(ctx context.Context, user uuid.UUID) error {
	mine, err := h.Store.PairingsOf(ctx, user)
	if err != nil {
		return err
	}
	n := 0
	for _, p := range mine {
		if p.State != store.PairingStopped {
			n++
		}
	}
	if n >= maxPairings {
		return ErrTooManyPairings
	}
	return nil
}

// checkPairingPeople checks the chosen people against the person's own Immich: named, and each once.
func (h *Hub) checkPairingPeople(ctx context.Context, sess *immich.API, ids []uuid.UUID) error {
	if len(ids) < 1 || len(ids) > maxPairingPeople {
		return ErrPairingPeople
	}
	known, err := sess.People(ctx)
	if err != nil {
		return fmt.Errorf("could not list the people in your library: %w", err)
	}
	named := map[uuid.UUID]bool{}
	for _, x := range known {
		if x.Name != "" {
			named[uuid.UUID(x.ID)] = true
		}
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range ids {
		if seen[id] {
			return ErrPairingPeople
		}
		seen[id] = true
		if !named[id] {
			return ErrRulePerson
		}
	}
	return nil
}

// addPairingRules makes one rule per chosen person for the person's own side, marks each as the pairing's, and sends nothing yet.
func (h *Hub) addPairingRules(ctx context.Context, sess *immich.API, user immich.User, p store.Pairing, people []uuid.UUID, settle int) error {
	for _, id := range people {
		r, err := h.addRule(ctx, sess, user, RuleParams{Service: p.Service, People: []uuid.UUID{id}, SettleMinutes: settle})
		if err != nil {
			return err
		}
		if err := h.Store.SetRulePairing(ctx, r.ID, p.ID); err != nil {
			return err
		}
	}
	return nil
}

func (h *Hub) storedService(ctx context.Context, name string) (*store.Service, error) {
	all, err := h.Store.Services(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, ErrNotOwner
}

// pairingOther is the name of the other person of the two.
func pairingOther(p store.Pairing, user uuid.UUID) string {
	if user == p.AID {
		return p.BName
	}
	return p.AName
}

func newPairingName() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system random source failing is not recoverable
	}
	return "pair-" + hex.EncodeToString(b)
}

// poolOwnerLink stands in for the sender link the owner of a pairing's pool does not have: their own key is the pool's key, and it
// can search, tag and read, which is all a rule needs. Only a pairing's pool is treated so; an ordinary pool's owner is not.
func (h *Hub) poolOwnerLink(ctx context.Context, user uuid.UUID, service string) (*store.Link, error) {
	all, err := h.Store.Services(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range all {
		if v.Name == service && v.Pool && v.Pairing != "" && v.OwnerID == user {
			return &store.Link{Service: service, Role: store.RoleSender, ImmichUserID: user, Name: v.OwnerName, Email: v.OwnerEmail, KeyID: v.KeyID, Key: v.Key, State: store.StateActive, Mode: syncer.ModeCopy}, nil
		}
	}
	return nil, ErrNotJoined
}

// IsPairingName says whether a service name is one a pairing generated for its pool ("pair-" and eight hexadecimal digits), which no
// person ever typed and no page should show as it is.
func IsPairingName(name string) bool {
	rest, ok := strings.CutPrefix(name, "pair-")
	if !ok || len(rest) != 8 {
		return false
	}
	_, err := hex.DecodeString(rest)
	return err == nil
}
