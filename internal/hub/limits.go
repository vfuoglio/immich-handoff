package hub

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// ErrBadLimit is a daily cap that is not a sensible number.
var ErrBadLimit = errors.New("a daily limit must be a whole number from 0 (no limit) to 1000000")

func checkLimits(photos, megabytes int) error {
	if photos < 0 || photos > 1_000_000 || megabytes < 0 || megabytes > 1_000_000 {
		return ErrBadLimit
	}
	return nil
}

// linkOf finds the stored membership behind a sender's account, if it came from the page.
func (h *Hub) linkOf(acct *syncer.Account) (string, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for id, a := range h.accounts {
		if a == acct {
			return id, true
		}
	}
	return "", false
}

// Handed implements syncer.Usage: what the sender handed over to their service within the window.
// Accounts from the configuration have no stored membership, so nothing is recorded or capped for them.
func (h *Hub) Handed(ctx context.Context, acct *syncer.Account, since time.Time) (syncer.Snapshot, error) {
	id, ok := h.linkOf(acct)
	if !ok || h.Store == nil {
		return syncer.Snapshot{}, nil
	}
	photos, bytes, oldest, err := h.Store.Handovers(ctx, id, since)
	if err != nil {
		return syncer.Snapshot{}, err
	}
	return syncer.Snapshot{Tracked: true, Photos: photos, Bytes: bytes, Oldest: oldest}, nil
}

// Record implements syncer.Usage: one more photo handed over.
func (h *Hub) Record(ctx context.Context, acct *syncer.Account, bytes int64) error {
	id, ok := h.linkOf(acct)
	if !ok || h.Store == nil {
		return nil
	}
	return h.Store.AddHandover(ctx, id, bytes)
}

// LimitReached implements syncer.Reporter: the first time a sender's photos wait for the cap, both sides can see it.
func (h *Hub) LimitReached(svc *syncer.Service, snd *syncer.Account, waiting int, until time.Time) {
	text := fmt.Sprintf("%s reached the daily limit for %s: %d %s will wait until %s", snd.Name, svc.Label(), waiting, plural(waiting), until.Local().Format("2 Jan 15:04"))
	h.event(context.Background(), svc.Label(), snd.UserID, "limit", text)
}

// SetLimits sets what each sender may hand over to the service in a day (photos, megabytes; 0 for none). Only the
// owner may. Waiting photos are looked at again at once, since a higher cap may let them go.
func (h *Hub) SetLimits(ctx context.Context, owner immich.User, service string, photos, megabytes int) error {
	if err := checkLimits(photos, megabytes); err != nil {
		return err
	}
	v, err := h.ownedService(ctx, owner, service)
	if err != nil {
		return err
	}
	if err := h.Store.SetServiceLimits(ctx, v.Name, photos, megabytes); err != nil {
		return err
	}
	text := "No daily limit for senders"
	if photos > 0 || megabytes > 0 {
		text = "Daily limit for each sender:"
		if photos > 0 {
			text += fmt.Sprintf(" %d %s", photos, plural(photos))
		}
		if megabytes > 0 {
			text += fmt.Sprintf(" %d MB", megabytes)
		}
	}
	h.event(ctx, service, owner.ID, "limits", owner.Name+" changed the limits of "+service+": "+text)
	if err := h.Reload(ctx); err != nil {
		return err
	}
	h.wake("limits changed")
	return nil
}

func (h *Hub) wake(reason string) {
	if h.Wake != nil {
		h.Wake(reason)
	}
}

// SetServicePaused pauses or resumes a service the person owns. While it is paused nothing is handed over and
// tagged photos stay tagged; resuming hands them over.
func (h *Hub) SetServicePaused(ctx context.Context, owner immich.User, service string, paused bool) error {
	v, err := h.ownedService(ctx, owner, service)
	if err != nil {
		return err
	}
	if err := h.Store.SetServicePaused(ctx, v.Name, paused); err != nil {
		return err
	}
	word := "resumed"
	if paused {
		word = "paused"
	}
	h.event(ctx, service, owner.ID, word, owner.Name+" "+word+" the service "+service)
	if err := h.Reload(ctx); err != nil {
		return err
	}
	if !paused {
		h.wake("service resumed")
	}
	return nil
}

// SetMemberPaused pauses or resumes the person's own membership of a service they joined from the page.
func (h *Hub) SetMemberPaused(ctx context.Context, user immich.User, service string, paused bool) error {
	if !h.SelfService() {
		return ErrNotJoined
	}
	link, err := h.senderLink(ctx, user.ID, service)
	if err != nil {
		return err
	}
	if err := h.Store.SetLinkPaused(ctx, link.ID, paused); err != nil {
		return err
	}
	word := "resumed"
	if paused {
		word = "paused"
	}
	h.event(ctx, service, user.ID, word, user.Name+" "+word+" sending to "+service)
	if err := h.Reload(ctx); err != nil {
		return err
	}
	if !paused {
		h.wake("sender resumed")
	}
	return nil
}

// Window is how far back the daily cap looks, for showing "today" numbers.
func Window() time.Duration { return syncer.LimitWindow }
