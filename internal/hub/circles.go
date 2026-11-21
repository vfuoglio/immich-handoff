package hub

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// A circle (#270) is a named group of people that every new album of the owner's is shared with, through a key of the owner's own.
// Immich says nothing when an album is made (its triggers are about photos), so a look in the rules' round finds new albums. The
// owner is asked first, then can let the circle share by itself. What the app shared is recorded album by album and person by person
// (store.CircleShare), so that taking a person out undoes only that and nobody who left an album is added again. See
// docs/design/circles-design.md.

const (
	circleKeyName    = "handoff: circles"
	maxCircles       = 10
	maxCircleMembers = 20
)

var (
	ErrNoCircleKey     = errors.New("the key the app uses for your circles is gone: make the circle again")
	ErrCircleKeyMiss   = errors.New("the app could not read your albums with its key")
	ErrBadCircleName   = errors.New("name the circle, up to 60 characters")
	ErrBadCircleRole   = errors.New("people in a circle can be viewers or editors of an album")
	ErrBadCirclePrefix = errors.New("the start of an album's name can be up to 100 characters")
	ErrTooManyCircles  = errors.New("you already have 10 circles; end one first")
	ErrNoCircle        = errors.New("that is not one of your circles")
	ErrCircleSelf      = errors.New("you own the albums, so you cannot be in your own circle")
	ErrCircleNobody    = errors.New("put at least one person in the circle")
	ErrTooManyMembers  = errors.New("a circle can hold up to 20 people")
	ErrCircleMethod    = errors.New("choose a person from the list the administrator has allowed")
	ErrNothingToShare2 = errors.New("no album waits for your answer")
	ErrCircleAutoSoon  = errors.New("a circle can share by itself after you have answered Always three times")
	ErrCircleNoMore    = errors.New("no album is left to offer: every album that fits is already shared, offered or left out")
)

// CircleMethod says how people are chosen for a circle: 1, the owner types an email; 2, the owner picks from every account.
func (h *Hub) CircleMethod(ctx context.Context) int {
	if h.Store == nil {
		return 1
	}
	return h.Store.CircleMethod(ctx)
}

// SetCircleMethod is the administrator's choice (1 or 2).
func (h *Hub) SetCircleMethod(ctx context.Context, method int) error {
	if h.Store == nil {
		return ErrNotJoined
	}
	return h.Store.SetCircleMethod(ctx, method)
}

// CircleUsers lists the accounts a person may pick for a circle when the administrator has allowed it (method 2), without themselves.
func (h *Hub) CircleUsers(ctx context.Context, sess *immich.API, me uuid.UUID) ([]immich.User, error) {
	if h.CircleMethod(ctx) != 2 {
		return nil, ErrCircleMethod
	}
	all, err := sess.Users(ctx)
	if err != nil {
		return nil, err
	}
	var out []immich.User
	for _, u := range all {
		if uuid.UUID(u.ID) != me {
			out = append(out, u)
		}
	}
	return out, nil
}

// circleMember finds the person a form named: by email (method 1) or by account (method 2).
func (h *Hub) circleMember(ctx context.Context, sess *immich.API, me uuid.UUID, email string, id uuid.UUID) (store.CircleMember, error) {
	var u immich.User
	if h.CircleMethod(ctx) == 2 {
		if id == uuid.Nil {
			return store.CircleMember{}, ErrCircleMethod
		}
		all, err := sess.Users(ctx)
		if err != nil {
			return store.CircleMember{}, err
		}
		found := false
		for _, x := range all {
			if uuid.UUID(x.ID) == id {
				u, found = x, true
			}
		}
		if !found {
			return store.CircleMember{}, ErrNoSuchPerson
		}
	} else {
		var ok bool
		var err error
		if u, ok, err = sess.FindUserByEmail(ctx, email); err != nil {
			return store.CircleMember{}, err
		} else if !ok {
			return store.CircleMember{}, ErrNoSuchPerson
		}
	}
	if uuid.UUID(u.ID) == me {
		return store.CircleMember{}, ErrCircleSelf
	}
	return store.CircleMember{UserID: uuid.UUID(u.ID), Name: u.Name, Email: u.Email}, nil
}

func (h *Hub) ensureCircleKey(ctx context.Context, sess *immich.API, user immich.User) (*immich.API, error) {
	if !h.SelfService() {
		return nil, ErrNotJoined
	}
	k, ok, err := h.Store.CircleKey(ctx, uuid.UUID(user.ID))
	if err != nil {
		return nil, err
	}
	if ok {
		return h.Connect(k.Key)
	}
	id, secret, err := sess.CreateAPIKey(ctx, circleKeyName, immich.CirclePermissions)
	if err != nil {
		return nil, fmt.Errorf("could not make the app a key for your circles: %w", err)
	}
	if err := h.Store.SetCircleKey(ctx, uuid.UUID(user.ID), store.CircleKey{KeyID: id, Key: secret}); err != nil {
		_ = sess.DeleteAPIKey(ctx, id)
		return nil, err
	}
	return h.Connect(secret)
}

func (h *Hub) circleAPI(ctx context.Context, user uuid.UUID) (*immich.API, error) {
	if h.Store == nil {
		return nil, ErrNotJoined
	}
	k, ok, err := h.Store.CircleKey(ctx, user)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNoCircleKey
	}
	return h.Connect(k.Key)
}

// dropCircleKeyIfUnused deletes the person's key (in their Immich, with their session) when they have no circle left.
func (h *Hub) dropCircleKeyIfUnused(ctx context.Context, sess *immich.API, user uuid.UUID) {
	if list, err := h.Store.CirclesOf(ctx, user); err != nil || len(list) > 0 {
		return
	}
	k, ok, err := h.Store.CircleKey(ctx, user)
	if err != nil || !ok {
		return
	}
	if sess != nil && k.KeyID != uuid.Nil {
		if err := sess.DeleteAPIKey(ctx, k.KeyID); err != nil && !immich.IsNotFound(err) {
			h.Log.Debug("could not delete the key for circles", "err", err)
		}
	}
	_ = h.Store.DeleteCircleKey(ctx, user)
}

// CircleParams is what a person fills in to make a circle: who is in it is given by email (method 1) or by account (method 2).
type CircleParams struct {
	Name, Role, Prefix string
	Emails             []string
	Users              []uuid.UUID
}

// AddCircle makes a circle. It looks only at albums made from now on; what exists is offered only when the person asks.
func (h *Hub) AddCircle(ctx context.Context, sess *immich.API, user immich.User, p CircleParams) (store.Circle, error) {
	if !h.SelfService() {
		return store.Circle{}, ErrNotJoined
	}
	me := uuid.UUID(user.ID)
	name := strings.TrimSpace(p.Name)
	if name == "" || utf8.RuneCountInString(name) > 60 || strings.ContainsAny(name, "\r\n\x00") {
		return store.Circle{}, ErrBadCircleName
	}
	prefix := strings.TrimSpace(p.Prefix)
	if utf8.RuneCountInString(prefix) > 100 || strings.ContainsAny(prefix, "\r\n\x00") {
		return store.Circle{}, ErrBadCirclePrefix
	}
	role := p.Role
	if role == "" {
		role = "viewer"
	}
	if role != "viewer" && role != "editor" {
		return store.Circle{}, ErrBadCircleRole
	}
	if list, err := h.Store.CirclesOf(ctx, me); err != nil {
		return store.Circle{}, err
	} else if len(list) >= maxCircles {
		return store.Circle{}, ErrTooManyCircles
	}
	var members []store.CircleMember
	seen := map[uuid.UUID]bool{}
	add := func(m store.CircleMember) {
		if !seen[m.UserID] {
			seen[m.UserID] = true
			members = append(members, m)
		}
	}
	if h.CircleMethod(ctx) == 2 {
		for _, id := range p.Users {
			m, err := h.circleMember(ctx, sess, me, "", id)
			if err != nil {
				return store.Circle{}, err
			}
			add(m)
		}
	} else {
		for _, e := range p.Emails {
			if strings.TrimSpace(e) == "" {
				continue
			}
			m, err := h.circleMember(ctx, sess, me, e, uuid.Nil)
			if err != nil {
				return store.Circle{}, err
			}
			add(m)
		}
	}
	if len(members) == 0 {
		return store.Circle{}, ErrCircleNobody
	}
	if len(members) > maxCircleMembers {
		return store.Circle{}, ErrTooManyMembers
	}
	api, err := h.ensureCircleKey(ctx, sess, user)
	if err != nil {
		return store.Circle{}, err
	}
	if _, err := api.OwnedAlbums(ctx); err != nil { // shows that the key can read the albums
		h.dropCircleKeyIfUnused(ctx, sess, me)
		return store.Circle{}, fmt.Errorf("%w: %v", ErrCircleKeyMiss, trimNet(err))
	}
	c, err := h.Store.AddCircle(ctx, store.Circle{OwnerID: me, Name: name, Role: role, Prefix: prefix})
	if err != nil {
		h.dropCircleKeyIfUnused(ctx, sess, me)
		return store.Circle{}, err
	}
	for _, m := range members {
		if err := h.Store.AddCircleMember(ctx, c.ID, m); err != nil {
			h.Store.DeleteCircle(ctx, c.ID)
			return store.Circle{}, err
		}
	}
	h.event(ctx, "", me, "circle", i18n.Pack("{name} made the circle {circle}: new albums are offered to {people}", "name", user.Name, "circle", c.Name, "people", memberNames(members)))
	return c, nil
}

func memberNames(m []store.CircleMember) string {
	var n []string
	for _, x := range m {
		n = append(n, x.Name)
	}
	return joinAnd(n)
}

func (h *Hub) circleOf(ctx context.Context, user uuid.UUID, id string) (store.Circle, error) {
	c, err := h.Store.CircleByID(ctx, id)
	if err != nil || c.OwnerID != user {
		return store.Circle{}, ErrNoCircle
	}
	return c, nil
}

// AddCircleMemberTo puts one more person in a circle. The albums the circle has shared are not shared with them: a catch-up offers those.
func (h *Hub) AddCircleMemberTo(ctx context.Context, sess *immich.API, user immich.User, circle, email string, id uuid.UUID) error {
	c, err := h.circleOf(ctx, uuid.UUID(user.ID), circle)
	if err != nil {
		return err
	}
	members, err := h.Store.CircleMembers(ctx, c.ID)
	if err != nil {
		return err
	}
	if len(members) >= maxCircleMembers {
		return ErrTooManyMembers
	}
	m, err := h.circleMember(ctx, sess, uuid.UUID(user.ID), email, id)
	if err != nil {
		return err
	}
	if err := h.Store.AddCircleMember(ctx, c.ID, m); err != nil {
		return err
	}
	h.event(ctx, "", uuid.UUID(user.ID), "circle", i18n.Pack("{name} put {person} in the circle {circle}", "name", user.Name, "person", m.Name, "circle", c.Name))
	return nil
}

// RemoveCircleMemberFrom takes a person out of a circle and off every album the app shared with them. An album they were on before,
// or were put on by hand, is left alone.
func (h *Hub) RemoveCircleMemberFrom(ctx context.Context, user immich.User, circle string, member uuid.UUID) error {
	c, err := h.circleOf(ctx, uuid.UUID(user.ID), circle)
	if err != nil {
		return err
	}
	members, err := h.Store.CircleMembers(ctx, c.ID)
	if err != nil {
		return err
	}
	var who *store.CircleMember
	for i := range members {
		if members[i].UserID == member {
			who = &members[i]
		}
	}
	if who == nil {
		return ErrNoCircle
	}
	shared, err := h.Store.CircleShares(ctx, c.ID, store.CircleShared)
	if err != nil {
		return err
	}
	removed := 0
	var api *immich.API
	for _, s := range shared {
		if s.UserID != member {
			continue
		}
		if api == nil {
			if api, err = h.circleAPI(ctx, c.OwnerID); err != nil {
				return err
			}
		}
		if err := api.UnshareAlbum(ctx, s.AlbumID, s.UserID); err != nil {
			return fmt.Errorf("could not take %s off the album %s: %w", who.Name, s.AlbumName, err)
		}
		removed++
	}
	if err := h.Store.RemoveCircleMember(ctx, c.ID, member); err != nil {
		return err
	}
	h.event(ctx, "", c.OwnerID, "circle", i18n.PackN("{name} took {person} out of the circle {circle} and off {n} album", "{name} took {person} out of the circle {circle} and off {n} albums", removed, "name", user.Name, "person", who.Name, "circle", c.Name))
	return nil
}

// DeleteCircleWith ends a circle. What it shared stays shared.
func (h *Hub) DeleteCircleWith(ctx context.Context, sess *immich.API, user immich.User, id string) error {
	c, err := h.circleOf(ctx, uuid.UUID(user.ID), id)
	if err != nil {
		return err
	}
	if err := h.Store.DeleteCircle(ctx, c.ID); err != nil {
		return err
	}
	h.dropCircleKeyIfUnused(ctx, sess, c.OwnerID)
	h.event(ctx, "", c.OwnerID, "circle", i18n.Pack("{name} ended the circle {circle}", "name", user.Name, "circle", c.Name))
	return nil
}

// PauseCircle pauses or resumes a circle.
func (h *Hub) PauseCircle(ctx context.Context, user uuid.UUID, id string, paused bool) error {
	c, err := h.circleOf(ctx, user, id)
	if err != nil {
		return err
	}
	return h.Store.SetCirclePaused(ctx, c.ID, paused)
}

// CanAutoCircle says whether a circle may be let share by itself: it is not yet, and the owner has answered Always three times.
func CanAutoCircle(c store.Circle) bool {
	return c.Mode != store.CircleAuto && c.Always >= alwaysToAuto
}

// SetCircleAuto lets a circle share by itself, or takes that back. Only albums made after the switch are ever shared without asking.
func (h *Hub) SetCircleAuto(ctx context.Context, user immich.User, id string, on bool) error {
	c, err := h.circleOf(ctx, uuid.UUID(user.ID), id)
	if err != nil {
		return err
	}
	if !on {
		if c.Mode != store.CircleAuto {
			return nil
		}
		if err := h.Store.SetCircleMode(ctx, c.ID, store.CircleSuggest, h.now()); err != nil {
			return err
		}
		h.event(ctx, "", c.OwnerID, "circle", i18n.Pack("{name} made the circle {circle} ask first again", "name", user.Name, "circle", c.Name))
		return nil
	}
	if c.Always < alwaysToAuto {
		return ErrCircleAutoSoon
	}
	if err := h.Store.SetCircleMode(ctx, c.ID, store.CircleAuto, h.now()); err != nil {
		return err
	}
	h.event(ctx, "", c.OwnerID, "circle", i18n.Pack("{name} let the circle {circle} share new albums by itself", "name", user.Name, "circle", c.Name))
	return nil
}

// CircleSuggestion is a circle with albums waiting for the owner's answer.
type CircleSuggestion struct {
	Circle  store.Circle
	Albums  []string // the albums, by name, once each
	People  []string // the people they would be shared with, once each
	Pending int      // how many album-and-person shares wait
}

// CircleSuggestions lists the circles of a person that have albums waiting for them.
func (h *Hub) CircleSuggestions(ctx context.Context, user uuid.UUID) ([]CircleSuggestion, error) {
	if h.Store == nil {
		return nil, nil
	}
	list, err := h.Store.CirclesOf(ctx, user)
	if err != nil {
		return nil, err
	}
	var out []CircleSuggestion
	for _, c := range list {
		offered, err := h.Store.CircleShares(ctx, c.ID, store.CircleOffered)
		if err != nil || len(offered) == 0 {
			continue
		}
		members, _ := h.Store.CircleMembers(ctx, c.ID)
		name := map[uuid.UUID]string{}
		for _, m := range members {
			name[m.UserID] = m.Name
		}
		s := CircleSuggestion{Circle: c, Pending: len(offered)}
		seenA, seenP := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
		for _, o := range offered {
			if !seenA[o.AlbumID] {
				seenA[o.AlbumID] = true
				s.Albums = append(s.Albums, o.AlbumName)
			}
			if !seenP[o.UserID] {
				seenP[o.UserID] = true
				s.People = append(s.People, name[o.UserID])
			}
		}
		sort.Strings(s.Albums)
		sort.Strings(s.People)
		out = append(out, s)
	}
	return out, nil
}

// AnswerCircle answers the albums a circle is waiting about: share them, leave them out for good, or share and count an Always.
func (h *Hub) AnswerCircle(ctx context.Context, user immich.User, id, answer string) error {
	c, err := h.circleOf(ctx, uuid.UUID(user.ID), id)
	if err != nil {
		return err
	}
	offered, err := h.Store.CircleShares(ctx, c.ID, store.CircleOffered)
	if err != nil {
		return err
	}
	if len(offered) == 0 {
		return ErrNothingToShare2
	}
	switch answer {
	case AnswerDecline:
		for _, o := range offered {
			h.Store.SetCircleShareState(ctx, c.ID, o.AlbumID, o.UserID, store.CircleOffered, store.CircleDeclined)
		}
		h.event(ctx, "", c.OwnerID, "circle", i18n.PackN("{n} album left out of the circle {circle}", "{n} albums left out of the circle {circle}", countAlbums(offered), "circle", c.Name))
		return nil
	case AnswerSend, AnswerAlways:
	default:
		return ErrNoSuggestion
	}
	if err := h.shareOffered(ctx, c, offered); err != nil {
		return err
	}
	if answer == AnswerAlways {
		h.Store.AddCircleAlways(ctx, c.ID)
	}
	h.event(ctx, "", c.OwnerID, "circle", i18n.PackN("{n} album shared with the circle {circle}", "{n} albums shared with the circle {circle}", countAlbums(offered), "circle", c.Name))
	return nil
}

func countAlbums(l []store.CircleShare) int {
	seen := map[uuid.UUID]bool{}
	for _, s := range l {
		seen[s.AlbumID] = true
	}
	return len(seen)
}

// shareOffered adds the people to the albums, with the owner's key, and records it. An album that is gone is left out.
func (h *Hub) shareOffered(ctx context.Context, c store.Circle, offered []store.CircleShare) error {
	api, err := h.circleAPI(ctx, c.OwnerID)
	if err != nil {
		return err
	}
	for _, o := range offered {
		if err := api.ShareAlbum(ctx, o.AlbumID, o.UserID, c.Role); err != nil {
			if immich.IsNotFound(err) {
				h.Store.SetCircleShareState(ctx, c.ID, o.AlbumID, o.UserID, store.CircleOffered, store.CircleDeclined)
				continue
			}
			return fmt.Errorf("could not share the album %s: %w", o.AlbumName, err)
		}
		if err := h.Store.SetCircleShareState(ctx, c.ID, o.AlbumID, o.UserID, store.CircleOffered, store.CircleShared); err != nil {
			return err
		}
	}
	return nil
}

// CatchUpCircle offers the albums that already exist (matching the circle's name filter) to the people who are not on them. It only
// offers: nothing is shared before the owner answers.
func (h *Hub) CatchUpCircle(ctx context.Context, user immich.User, id string) (int, error) {
	c, err := h.circleOf(ctx, uuid.UUID(user.ID), id)
	if err != nil {
		return 0, err
	}
	n, err := h.sweepCircle(ctx, c, true)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrCircleNoMore
	}
	h.event(ctx, "", c.OwnerID, "circle", i18n.PackN("{n} existing album offered to the circle {circle}", "{n} existing albums offered to the circle {circle}", n, "circle", c.Name))
	return n, nil
}

// CirclesMaintenance looks at every circle that is not paused, one after the other.
func (h *Hub) CirclesMaintenance(ctx context.Context) {
	if h.Store == nil || !h.SelfService() {
		return
	}
	list, err := h.Store.AllCircles(ctx)
	if err != nil {
		return
	}
	for _, c := range list {
		if ctx.Err() != nil {
			return
		}
		if c.Paused {
			continue
		}
		if _, err := h.sweepCircle(ctx, c, false); err != nil {
			h.Log.Debug("a circle could not be looked at", "circle", c.ID, "err", err)
		}
	}
}

// sweepCircle looks at the owner's albums for one circle: every album made after the circle (or, for a catch-up, every album) that
// matches its name filter becomes an offer for each person who is not on it, or, when the owner let the circle share by itself, is
// shared at once. It returns how many albums are newly offered or shared.
func (h *Hub) sweepCircle(ctx context.Context, c store.Circle, catchUp bool) (int, error) {
	fail := func(problem string, err error) (int, error) {
		h.Store.MarkCircleLook(ctx, c.ID, h.now(), problem)
		return 0, err
	}
	api, err := h.circleAPI(ctx, c.OwnerID)
	if err != nil {
		return fail(ErrNoCircleKey.Error(), err)
	}
	albums, err := api.OwnedAlbums(ctx)
	if err != nil {
		return fail(trimNet(err), err)
	}
	members, err := h.Store.CircleMembers(ctx, c.ID)
	if err != nil {
		return 0, err
	}
	known, err := h.Store.CircleShares(ctx, c.ID, "")
	if err != nil {
		return 0, err
	}
	state := map[string]string{}
	for _, s := range known {
		state[s.AlbumID.String()+"|"+s.UserID.String()] = s.State
	}
	waiting := 0
	for _, s := range known {
		if s.State == store.CircleOffered {
			waiting++
		}
	}
	byID := map[uuid.UUID]immich.AlbumInfo{}
	for _, a := range albums {
		byID[uuid.UUID(a.ID)] = a
	}
	for _, s := range known { // a person the app added who has since left the album is not added again
		if s.State != store.CircleShared {
			continue
		}
		a, ok := byID[s.AlbumID]
		if !ok {
			continue
		}
		on := false
		for _, m := range a.Members {
			on = on || uuid.UUID(m) == s.UserID
		}
		if !on {
			h.Store.SetCircleShareState(ctx, c.ID, s.AlbumID, s.UserID, store.CircleShared, store.CircleLeft)
		}
	}
	settle := h.ruleSettle()
	touched := map[uuid.UUID]bool{}
	var auto []store.CircleShare
	fresh := 0
	for _, a := range albums {
		id := uuid.UUID(a.ID)
		if c.Prefix != "" && !strings.HasPrefix(strings.ToLower(a.Name), strings.ToLower(c.Prefix)) {
			continue
		}
		if !catchUp && !a.CreatedAt.After(c.Since) {
			continue
		}
		if h.now().Sub(a.CreatedAt) < settle { // a new album is often renamed and filled at once: wait a little
			continue
		}
		onAlbum := map[uuid.UUID]bool{}
		for _, m := range a.Members {
			onAlbum[uuid.UUID(m)] = true
		}
		for _, m := range members {
			if state[id.String()+"|"+m.UserID.String()] != "" {
				continue
			}
			sh := store.CircleShare{CircleID: c.ID, AlbumID: id, AlbumName: a.Name, UserID: m.UserID}
			switch {
			case onAlbum[m.UserID]:
				sh.State = store.CircleHad
			case !catchUp && c.Mode == store.CircleAuto && a.CreatedAt.After(c.AutoSince) && h.now().Sub(a.CreatedAt) >= 6*settle:
				sh.State = store.CircleOffered
				auto = append(auto, sh)
			default:
				sh.State = store.CircleOffered
			}
			if err := h.Store.PutCircleShare(ctx, sh); err != nil {
				return 0, err
			}
			if sh.State == store.CircleOffered {
				touched[id] = true
			}
		}
	}
	if len(auto) > 0 {
		if err := h.shareOffered(ctx, c, auto); err != nil {
			h.Store.MarkCircleLook(ctx, c.ID, h.now(), trimNet(err)) // they stay offers: nothing is lost
			return 0, err
		}
		autoAlbums := countAlbums(auto)
		h.event(ctx, "", c.OwnerID, "circle", i18n.PackN("{n} album shared with the circle {circle} on its own", "{n} albums shared with the circle {circle} on their own", autoAlbums, "circle", c.Name))
		for _, s := range auto {
			delete(touched, s.AlbumID)
		}
		fresh += autoAlbums
	}
	fresh += len(touched)
	h.Store.MarkCircleLook(ctx, c.ID, h.now(), "")
	if len(touched) > 0 && waiting == 0 && !c.Paused && !catchUp { // told once for a batch, when it begins
		h.event(ctx, "", c.OwnerID, "suggestion", i18n.PackN("{n} new album can be shared with the circle {circle}. Look at it on the Overview.", "{n} new albums can be shared with the circle {circle}. Look at them on the Overview.", len(touched), "circle", c.Name))
	}
	return fresh, nil
}
