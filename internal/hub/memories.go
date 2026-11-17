package hub

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/immich"
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
)

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
	p.UserID, p.Name = user.ID, user.Name
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
