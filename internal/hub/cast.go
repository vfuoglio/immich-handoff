package hub

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Casting to a TV (#269): a TV opens a short address, shows a code, and the signed-in person types the code on their page and chooses
// what to show. The TV holds a long secret (its session) that only it and the app know; the code is the short-lived way for the person
// to say "that TV". Photos reach the TV through the app, with a key of the person's own, by opaque ids, as for a frame. See
// docs/contributing/design/cast-design.md.

const (
	castCodeLife    = 10 * time.Minute // how long a code can be used
	castWaitingMax  = 500              // TVs waiting for a code, at most
	castPerAddress  = 20               // new codes one address can ask for in an hour
	castWrongTries  = 5                // wrong codes one person can type in ten minutes before they are silenced
	castWrongWindow = 10 * time.Minute
	castCooldown    = 10 * time.Minute
	castWrongAll    = 100 // wrong codes typed by anyone in ten minutes before every claim waits
	castIdle        = 30 * time.Minute
	castMaxLife     = 24 * time.Hour
	castNewDays     = 30
	castMax         = 500 // photos a TV is given, the newest
	castMostTVs     = 5   // TVs one person can have paired at once
	castKeyName     = "handoff: cast"
)

var (
	ErrBadCastCode   = errors.New("that code is not (or no longer) one a TV is showing: look at the TV for a new one")
	ErrCastBusy      = errors.New("too many wrong codes: wait ten minutes, then try again")
	ErrCastBadSrc    = errors.New("choose an album, a person, everything new or a day of memories")
	ErrCastEmpty     = errors.New("there are no photos to show in that")
	ErrNoCast        = errors.New("that is not one of your TVs")
	ErrCastTooMany   = errors.New("you already have 5 TVs showing photos; stop one first")
	ErrCastAddress   = errors.New("this address has asked for many codes; try again later")
	ErrCastFull      = errors.New("many TVs are waiting for a code; try again in a few minutes")
	ErrNoCastKey     = errors.New("the key the app uses for casting is gone: cast again")
	ErrBadCastDay    = errors.New("give the day of the memories as a date")
	ErrBadCastCmd    = errors.New("that is not something a TV can be told")
	ErrBadCastHold   = errors.New("a photo can stay 5 to 120 seconds")
	ErrBadCastEffect = errors.New("choose fade, zoom, slide or rise")
)

type waitingTV struct {
	code string
	at   time.Time
}

// castHub holds what a TV and a person's guesses need to remember between requests, in memory: a restart only makes TVs ask for
// a new code.
type castHub struct {
	mu       sync.Mutex
	waiting  map[string]waitingTV // by the TV's secret
	byCode   map[string]string    // code -> secret
	perAddr  map[string][]time.Time
	wrong    map[uuid.UUID][]time.Time
	until    map[uuid.UUID]time.Time
	wrongAll []time.Time
	lists    map[string]*castList
}

type castList struct {
	at   time.Time
	list []openapi_types.UUID
}

func (c *castHub) init() {
	if c.waiting == nil {
		c.waiting, c.byCode, c.perAddr = map[string]waitingTV{}, map[string]string{}, map[string][]time.Time{}
		c.wrong, c.until, c.lists = map[uuid.UUID][]time.Time{}, map[uuid.UUID]time.Time{}, map[string]*castList{}
	}
}

func recent(l []time.Time, now time.Time, window time.Duration) []time.Time {
	out := l[:0]
	for _, t := range l {
		if now.Sub(t) < window {
			out = append(out, t)
		}
	}
	return out
}

func castCode() string {
	const n = 6
	out := make([]byte, 0, n)
	buf := make([]byte, 1)
	limit := 256 - 256%len(shortAlphabet) // no modulo bias
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			panic(err)
		}
		if int(buf[0]) < limit {
			out = append(out, shortAlphabet[int(buf[0])%len(shortAlphabet)])
		}
	}
	return string(out)
}

func normCode(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z':
			return r + 32
		case r == ' ' || r == '-':
			return -1
		}
		return r
	}, s)
}

// NewTV makes what a TV needs: a secret that is its session, and a short code to show. addr is where it asked from, so that one
// address cannot use up every code.
func (h *Hub) NewTV(addr string) (secret, code string, expires time.Time, err error) {
	c := &h.casts
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	now := h.now()
	for s, w := range c.waiting { // codes that nobody typed in time
		if now.Sub(w.at) >= castCodeLife {
			delete(c.byCode, w.code)
			delete(c.waiting, s)
		}
	}
	if len(c.waiting) >= castWaitingMax {
		return "", "", time.Time{}, ErrCastFull
	}
	if len(c.perAddr) > 20000 {
		c.perAddr = map[string][]time.Time{}
	}
	asked := recent(c.perAddr[addr], now, time.Hour)
	if len(asked) >= castPerAddress {
		c.perAddr[addr] = asked
		return "", "", time.Time{}, ErrCastAddress
	}
	c.perAddr[addr] = append(asked, now)
	for {
		code = castCode()
		if _, taken := c.byCode[code]; !taken {
			break
		}
	}
	secret = newToken() + newToken()
	c.waiting[secret] = waitingTV{code: code, at: now}
	c.byCode[code] = secret
	return secret, code, now.Add(castCodeLife), nil
}

// TVState is what a TV is told each time it asks.
type TVState struct {
	State   string // "waiting" (still showing a code), "expired" (the code ran out: ask for a new one), "live" or "ended"
	Paused  bool
	Seconds int
	FX      string
	Seq     int
	Cmd     string
	Refresh int // how often to look at the feed again, in seconds
}

// CastState is what a TV is told. An unknown secret is "ended": a TV that was cleared or timed out starts over.
func (h *Hub) CastState(ctx context.Context, secret string) TVState {
	if len(secret) < 32 || len(secret) > 128 {
		return TVState{State: "ended"}
	}
	c := &h.casts
	c.mu.Lock()
	c.init()
	if w, ok := c.waiting[secret]; ok {
		c.mu.Unlock()
		if h.now().Sub(w.at) >= castCodeLife {
			return TVState{State: "expired"}
		}
		return TVState{State: "waiting"}
	}
	c.mu.Unlock()
	if h.Store == nil {
		return TVState{State: "ended"}
	}
	s, err := h.Store.CastSessionByID(ctx, secret)
	if err != nil || s.Ended || h.now().Sub(s.PairedAt) >= castMaxLife {
		return TVState{State: "ended"}
	}
	if h.now().Sub(s.LastSeen) > 10*time.Second {
		h.Store.TouchCast(ctx, s.ID, h.now())
	}
	return TVState{State: "live", Paused: s.Paused, Seconds: s.Seconds, FX: s.FX, Seq: s.Seq, Cmd: s.Cmd, Refresh: int(h.FrameRefreshEvery().Seconds())}
}

// CastSource is what a person chose to show.
type CastSource struct {
	Kind    string    // store.CastAlbum, CastPerson, CastNew or CastMemory
	ID      uuid.UUID // the album or person
	Day     string    // for a memory, YYYY-MM-DD
	Seconds int       // how long each photo stays (0: 15)
	FX      string    // 0 value: fade
}

func castPlay(src CastSource) (int, string, error) {
	seconds := src.Seconds
	if seconds == 0 {
		seconds = 15
	}
	if seconds < 5 || seconds > 120 {
		return 0, "", ErrBadCastHold
	}
	fx := src.FX
	if fx == "" {
		fx = "fade"
	}
	switch fx {
	case "fade", "zoom", "slide", "rise":
	default:
		return 0, "", ErrBadCastEffect
	}
	return seconds, fx, nil
}

// castSourceOf checks a choice against the person's own Immich, with their own session, and names it.
func (h *Hub) castSourceOf(ctx context.Context, sess *immich.API, me uuid.UUID, src CastSource) (kind, id, name, day string, err error) {
	switch src.Kind {
	case store.CastAlbum:
		albums, err := sess.OwnedAlbums(ctx)
		if err != nil {
			return "", "", "", "", err
		}
		for _, a := range albums {
			if uuid.UUID(a.ID) == src.ID {
				return src.Kind, src.ID.String(), a.Name, "", nil
			}
		}
		shared, err := sess.AlbumsSharedWithMe(ctx)
		if err != nil {
			return "", "", "", "", err
		}
		for _, a := range shared {
			if uuid.UUID(a.ID) == src.ID {
				return src.Kind, src.ID.String(), a.Name, "", nil
			}
		}
		return "", "", "", "", ErrNoAlbum
	case store.CastPerson:
		people, err := sess.People(ctx)
		if err != nil {
			return "", "", "", "", err
		}
		for _, p := range people {
			if uuid.UUID(p.ID) == src.ID {
				return src.Kind, src.ID.String(), p.Name, "", nil
			}
		}
		return "", "", "", "", ErrRulePerson
	case store.CastStory:
		st, err := h.Store.StoryByID(ctx, me, src.ID.String())
		if err != nil || st.State != store.StoryReady {
			return "", "", "", "", ErrNoStory
		}
		return src.Kind, st.ID, st.Title, "", nil
	case store.CastNew:
		return src.Kind, "", "", "", nil
	case store.CastMemory:
		d, perr := time.Parse("2006-01-02", src.Day)
		if perr != nil {
			return "", "", "", "", ErrBadCastDay
		}
		return src.Kind, "", "", d.Format("2006-01-02"), nil
	}
	return "", "", "", "", ErrCastBadSrc
}

// ensureCastKey makes the person's key for casting the first time (with their own session) and returns a client that acts with it.
// A memory needs memory.read too, which their session adds when they first cast one.
func (h *Hub) ensureCastKey(ctx context.Context, sess *immich.API, user immich.User, memory bool) (*immich.API, error) {
	k, ok, err := h.Store.CastKey(ctx, uuid.UUID(user.ID))
	if err != nil {
		return nil, err
	}
	if !ok {
		perms := immich.CastPermissions
		if memory {
			perms = append(append(perms[:0:0], perms...), "memory.read")
		}
		id, secret, err := sess.CreateAPIKey(ctx, castKeyName, perms)
		if err != nil {
			return nil, fmt.Errorf("could not make the app a key for casting: %w", err)
		}
		if err := h.Store.SetCastKey(ctx, uuid.UUID(user.ID), store.CastKey{KeyID: id, Key: secret}); err != nil {
			_ = sess.DeleteAPIKey(ctx, id)
			return nil, err
		}
		return h.Connect(secret)
	}
	if memory {
		if err := sess.AddKeyPermission(ctx, k.KeyID, "memory.read"); err != nil {
			return nil, fmt.Errorf("could not allow the app to read your memories: %w", err)
		}
	}
	return h.Connect(k.Key)
}

func (h *Hub) castAPI(ctx context.Context, user uuid.UUID) (*immich.API, error) {
	k, ok, err := h.Store.CastKey(ctx, user)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNoCastKey
	}
	return h.Connect(k.Key)
}

// wrongCode records a wrong code typed by a person and says whether they are now silenced.
func (h *Hub) wrongCode(user uuid.UUID) {
	c := &h.casts
	now := h.now()
	c.wrong[user] = append(recent(c.wrong[user], now, castWrongWindow), now)
	c.wrongAll = append(recent(c.wrongAll, now, castWrongWindow), now)
	if len(c.wrong[user]) >= castWrongTries {
		c.until[user] = now.Add(castCooldown)
		delete(c.wrong, user)
	}
}

// ClaimCast pairs the TV that shows the code with a source the person chose, and starts it.
func (h *Hub) ClaimCast(ctx context.Context, sess *immich.API, user immich.User, code string, src CastSource) (store.CastSession, error) {
	if !h.SelfService() {
		return store.CastSession{}, ErrNotJoinable
	}
	me := uuid.UUID(user.ID)
	seconds, fx, err := castPlay(src)
	if err != nil {
		return store.CastSession{}, err
	}
	code = normCode(code)
	c := &h.casts
	c.mu.Lock()
	c.init()
	now := h.now()
	if now.Before(c.until[me]) || len(recent(c.wrongAll, now, castWrongWindow)) >= castWrongAll {
		c.mu.Unlock()
		return store.CastSession{}, ErrCastBusy
	}
	secret, ok := c.byCode[code]
	if ok && now.Sub(c.waiting[secret].at) >= castCodeLife {
		ok = false
	}
	if !ok {
		h.wrongCode(me)
		c.mu.Unlock()
		return store.CastSession{}, ErrBadCastCode
	}
	c.mu.Unlock()
	if n, err := h.Store.CountActiveCasts(ctx, me); err != nil {
		return store.CastSession{}, err
	} else if n >= castMostTVs {
		return store.CastSession{}, ErrCastTooMany
	}
	kind, id, name, day, err := h.castSourceOf(ctx, sess, me, src)
	if err != nil {
		return store.CastSession{}, err
	}
	api, err := h.ensureCastKey(ctx, sess, user, kind == store.CastMemory)
	if err != nil {
		return store.CastSession{}, err
	}
	s := store.CastSession{ID: secret, UserID: me, Kind: kind, SourceID: id, SourceName: name, Day: day, Seconds: seconds, FX: fx}
	if list, err := h.castList(ctx, api, s, false); err != nil {
		h.dropCastKeyIfUnused(ctx, sess, me)
		return store.CastSession{}, fmt.Errorf("could not read the photos: %w", err)
	} else if len(list) == 0 {
		h.dropCastKeyIfUnused(ctx, sess, me)
		return store.CastSession{}, ErrCastEmpty
	}
	c.mu.Lock()
	if w, still := c.waiting[secret]; !still || w.code != code { // another person typed it first
		c.mu.Unlock()
		return store.CastSession{}, ErrBadCastCode
	}
	delete(c.waiting, secret)
	delete(c.byCode, code)
	delete(c.wrong, me)
	c.mu.Unlock()
	s, err = h.Store.AddCastSession(ctx, s)
	if err != nil {
		return store.CastSession{}, err
	}
	h.event(ctx, "", me, "cast", i18n.Pack("{name} started a TV showing {source}", "name", user.Name, "source", castWords(s)))
	return s, nil
}

// castWords says what a TV shows, in a few words.
func castWords(s store.CastSession) string {
	switch s.Kind {
	case store.CastAlbum:
		return "the album " + s.SourceName
	case store.CastPerson:
		return "photos of " + s.SourceName
	case store.CastMemory:
		return "the memories of " + s.Day
	case store.CastStory:
		return "the story " + s.SourceName
	}
	return "everything new in the last 30 days"
}

// CastWords is castWords for a page.
func CastWords(s store.CastSession) string { return castWords(s) }

// castList reads a source's photos with the person's key: only photos (no videos), not trashed or archived, newest first, at most
// castMax. It is kept for FRAME_REFRESH, by source, so several TVs on one source ask Immich once.
func (h *Hub) castList(ctx context.Context, api *immich.API, s store.CastSession, useCache bool) ([]openapi_types.UUID, error) {
	key := s.UserID.String() + "|" + s.Kind + "|" + s.SourceID + "|" + s.Day
	c := &h.casts
	c.mu.Lock()
	c.init()
	if l := c.lists[key]; useCache && l != nil && h.now().Sub(l.at) < h.FrameRefreshEvery() {
		c.mu.Unlock()
		return l.list, nil
	}
	c.mu.Unlock()
	if s.Kind == store.CastStory { // a story is played in the order it was made, and is not listed again from the library
		st, err := h.Store.StoryByID(ctx, s.UserID, s.SourceID)
		if err != nil {
			return nil, ErrNoStory
		}
		var list []openapi_types.UUID
		for _, p := range st.Picks {
			if u, err := uuid.Parse(p.Asset); err == nil {
				list = append(list, u)
			}
		}
		return list, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var found []immich.AssetResponseDto
	var err error
	switch s.Kind {
	case store.CastAlbum:
		album, _ := uuid.Parse(s.SourceID)
		found, err = api.AlbumAssets(cctx, album)
	case store.CastPerson:
		person, _ := uuid.Parse(s.SourceID)
		found, err = api.SearchUpTo(cctx, immich.MetadataSearchDto{PersonIds: &[]openapi_types.UUID{person}}, castMax)
	case store.CastNew:
		since := h.now().AddDate(0, 0, -castNewDays)
		found, err = api.SearchUpTo(cctx, immich.MetadataSearchDto{CreatedAfter: &since}, castMax)
	case store.CastMemory:
		day, perr := time.Parse("2006-01-02", s.Day)
		if perr != nil {
			return nil, ErrBadCastDay
		}
		mems, merr := api.MemoriesFor(cctx, day)
		if merr != nil {
			return nil, merr
		}
		for _, m := range mems {
			for _, p := range m.Photos {
				found = append(found, immich.AssetResponseDto{Id: p, Type: immich.IMAGE, FileCreatedAt: m.At})
			}
		}
	}
	if err != nil {
		return nil, err
	}
	var keep []immich.AssetResponseDto
	seen := map[openapi_types.UUID]bool{}
	for _, a := range found {
		if a.Type == immich.IMAGE && !a.IsTrashed && !a.IsArchived && !seen[a.Id] {
			seen[a.Id] = true
			keep = append(keep, a)
		}
	}
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].FileCreatedAt.After(keep[j].FileCreatedAt) })
	if len(keep) > castMax {
		keep = keep[:castMax]
	}
	list := make([]openapi_types.UUID, len(keep))
	for i, a := range keep {
		list[i] = a.Id
	}
	c.mu.Lock()
	if len(c.lists) > 200 {
		c.lists = map[string]*castList{}
	}
	c.lists[key] = &castList{at: h.now(), list: list}
	c.mu.Unlock()
	return list, nil
}

// live finds a TV that is showing and the client that reads its source.
func (h *Hub) live(ctx context.Context, secret string) (store.CastSession, *immich.API, error) {
	if len(secret) < 32 || len(secret) > 128 || h.Store == nil {
		return store.CastSession{}, nil, ErrDropGone
	}
	s, err := h.Store.CastSessionByID(ctx, secret)
	if err != nil || s.Ended || h.now().Sub(s.PairedAt) >= castMaxLife {
		return store.CastSession{}, nil, ErrDropGone // unknown, ended and expired answer the same
	}
	api, err := h.castAPI(ctx, s.UserID)
	if err != nil {
		return store.CastSession{}, nil, ErrDropGone
	}
	return s, api, nil
}

// CastItem is a photo of a TV's feed: its opaque id and, for a story, the caption it is shown with.
type CastItem struct{ ID, Caption string }

// CastFeed lists the TV's photos by opaque ids. ordered says that they are to be shown in this order (a story), not shuffled.
func (h *Hub) CastFeed(ctx context.Context, secret string) (items []CastItem, ordered bool, err error) {
	s, api, err := h.live(ctx, secret)
	if err != nil {
		return nil, false, err
	}
	list, err := h.castList(ctx, api, s, true)
	if err != nil {
		return nil, false, err
	}
	var captions map[string]string
	if s.Kind == store.CastStory {
		ordered = true
		if st, err := h.Store.StoryByID(ctx, s.UserID, s.SourceID); err == nil {
			captions = map[string]string{}
			for _, p := range st.Picks {
				captions[p.Asset] = StoryCaption(st, p)
			}
		}
	}
	items = make([]CastItem, len(list))
	for i, a := range list {
		items[i] = CastItem{ID: frameID(secret, a), Caption: captions[uuid.UUID(a).String()]}
	}
	return items, ordered, nil
}

// CastImage fetches the picture of one photo of the TV's source, by its opaque id, with the person's key.
func (h *Hub) CastImage(ctx context.Context, secret, id string) ([]byte, string, error) {
	s, api, err := h.live(ctx, secret)
	if err != nil {
		return nil, "", err
	}
	list, err := h.castList(ctx, api, s, true)
	if err != nil {
		return nil, "", err
	}
	var asset openapi_types.UUID
	found := false
	for _, a := range list {
		if frameID(secret, a) == id {
			asset, found = a, true
			break
		}
	}
	if !found {
		return nil, "", ErrDropGone
	}
	key := "cast/" + s.UserID.String() + "/" + asset.String()
	if p, ok := h.wall.get(key); ok {
		return p.data, p.kind, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, kind, err := api.Thumbnail(cctx, asset, true)
	if err != nil {
		if immich.IsNotFound(err) {
			return nil, "", ErrDropGone
		}
		return nil, "", err
	}
	h.wall.put(key, wallPic{data, kind})
	return data, kind, nil
}

// CastsOf lists a person's TVs that are showing.
func (h *Hub) CastsOf(ctx context.Context, user uuid.UUID) ([]store.CastSession, error) {
	if h.Store == nil {
		return nil, nil
	}
	return h.Store.CastSessionsOf(ctx, user)
}

func (h *Hub) castOf(ctx context.Context, user uuid.UUID, id string) (store.CastSession, error) {
	s, err := h.Store.CastSessionByID(ctx, id)
	if err != nil || s.UserID != user || s.Ended {
		return store.CastSession{}, ErrNoCast
	}
	return s, nil
}

// Commands a person can give a TV.
const (
	CastPause  = "pause"
	CastResume = "resume"
	CastNext   = "next"
	CastPrev   = "prev"
	CastStop   = "stop"
)

// CastControl tells a TV to pause, go on, show the next or the previous photo, or stop.
func (h *Hub) CastControl(ctx context.Context, sess *immich.API, user immich.User, id, do string) error {
	me := uuid.UUID(user.ID)
	s, err := h.castOf(ctx, me, id)
	if err != nil {
		return err
	}
	switch do {
	case CastPause, CastResume:
		return h.Store.SetCastPlayback(ctx, s.ID, do == CastPause, s.Seconds, s.FX)
	case CastNext, CastPrev:
		return h.Store.CommandCast(ctx, s.ID, do)
	case CastStop:
		if err := h.Store.EndCast(ctx, s.ID); err != nil {
			return err
		}
		h.dropCastKeyIfUnused(ctx, sess, me)
		h.event(ctx, "", me, "cast", i18n.Pack("{name} stopped the TV that showed {source}", "name", user.Name, "source", castWords(s)))
		return nil
	}
	return ErrBadCastCmd
}

// ChangeCast shows something else on a TV that is already paired, and, if asked, changes how long a photo stays and the effect.
func (h *Hub) ChangeCast(ctx context.Context, sess *immich.API, user immich.User, id string, src CastSource) error {
	me := uuid.UUID(user.ID)
	s, err := h.castOf(ctx, me, id)
	if err != nil {
		return err
	}
	seconds, fx, err := castPlay(src)
	if err != nil {
		return err
	}
	kind, source, name, day, err := h.castSourceOf(ctx, sess, me, src)
	if err != nil {
		return err
	}
	api, err := h.ensureCastKey(ctx, sess, user, kind == store.CastMemory)
	if err != nil {
		return err
	}
	n := store.CastSession{ID: s.ID, UserID: me, Kind: kind, SourceID: source, SourceName: name, Day: day}
	if list, err := h.castList(ctx, api, n, false); err != nil {
		return fmt.Errorf("could not read the photos: %w", err)
	} else if len(list) == 0 {
		return ErrCastEmpty
	}
	if err := h.Store.SetCastSource(ctx, s.ID, kind, source, name, day); err != nil {
		return err
	}
	if err := h.Store.SetCastPlayback(ctx, s.ID, s.Paused, seconds, fx); err != nil {
		return err
	}
	h.event(ctx, "", me, "cast", i18n.Pack("{name} changed a TV to show {source}", "name", user.Name, "source", castWords(n)))
	return nil
}

// dropCastKeyIfUnused deletes the person's key (in their Immich, with their session) when no TV of theirs is showing.
func (h *Hub) dropCastKeyIfUnused(ctx context.Context, sess *immich.API, user uuid.UUID) {
	if n, err := h.Store.CountActiveCasts(ctx, user); err != nil || n > 0 {
		return
	}
	k, ok, err := h.Store.CastKey(ctx, user)
	if err != nil || !ok {
		return
	}
	if sess != nil && k.KeyID != uuid.Nil {
		if err := sess.DeleteAPIKey(ctx, k.KeyID); err != nil && !immich.IsNotFound(err) {
			h.Log.Debug("could not delete the key for casting", "err", err)
			return
		}
	}
	_ = h.Store.DeleteCastKey(ctx, user)
}

// CastHousekeeping runs when the person opens their page: a TV that went quiet is ended, and the key goes with the last TV, now that
// their own session can delete it.
func (h *Hub) CastHousekeeping(ctx context.Context, sess *immich.API, user uuid.UUID) {
	if h.Store == nil || !h.SelfService() {
		return
	}
	h.CastMaintenance(ctx)
	h.dropCastKeyIfUnused(ctx, sess, user)
}

// CastMaintenance ends TVs that have not asked for half an hour or were paired a day ago, and forgets old waiting codes.
func (h *Hub) CastMaintenance(ctx context.Context) {
	if h.Store == nil {
		return
	}
	now := h.now()
	if _, err := h.Store.EndStaleCasts(ctx, now.Add(-castIdle), now.Add(-castMaxLife)); err != nil {
		h.Log.Debug("could not end quiet TVs", "err", err)
	}
	c := &h.casts
	c.mu.Lock()
	c.init()
	for s, w := range c.waiting {
		if now.Sub(w.at) >= castCodeLife {
			delete(c.byCode, w.code)
			delete(c.waiting, s)
		}
	}
	c.mu.Unlock()
}
