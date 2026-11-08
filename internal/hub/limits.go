package hub

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/live"
	"github.com/vfuoglio/immich-handoff/internal/store"
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
	text := i18n.PackN("{name} reached the daily limit for {service}: {n} photo will wait until {when}", "{name} reached the daily limit for {service}: {n} photos will wait until {when}", waiting,
		"name", snd.Name, "service", svc.Label(), "when", until)
	h.event(context.Background(), svc.Label(), snd.UserID, "limit", text)
	h.showUntil(context.Background(), live.Limit, svc.Label(), snd.Name, "", waiting, until, snd.UserID)
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
	var text string
	switch {
	case photos > 0 && megabytes > 0:
		text = i18n.PackN("{name} set the daily limit of {service} to {n} photo and {mb} MB per sender", "{name} set the daily limit of {service} to {n} photos and {mb} MB per sender", photos, "name", owner.Name, "service", service, "mb", megabytes)
	case photos > 0:
		text = i18n.PackN("{name} set the daily limit of {service} to {n} photo per sender", "{name} set the daily limit of {service} to {n} photos per sender", photos, "name", owner.Name, "service", service)
	case megabytes > 0:
		text = i18n.Pack("{name} set the daily limit of {service} to {mb} MB per sender", "name", owner.Name, "service", service, "mb", megabytes)
	default:
		text = i18n.Pack("{name} removed the daily limit of {service}", "name", owner.Name, "service", service)
	}
	h.event(ctx, service, owner.ID, "limits", text)
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
	if paused {
		h.event(ctx, service, owner.ID, word, i18n.Pack("{name} paused the service {service}", "name", owner.Name, "service", service))
	} else {
		h.event(ctx, service, owner.ID, word, i18n.Pack("{name} resumed the service {service}", "name", owner.Name, "service", service))
	}
	h.show(ctx, live.Kind(word), service, "", "service", true)
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
	if paused {
		h.event(ctx, service, user.ID, word, i18n.Pack("{name} paused sending to {service}", "name", user.Name, "service", service))
	} else {
		h.event(ctx, service, user.ID, word, i18n.Pack("{name} resumed sending to {service}", "name", user.Name, "service", service))
	}
	h.show(ctx, live.Kind(word), service, user.Name, "sender", false, user.ID)
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

// Week is what moved through the person's services in each of the last seven days (UTC days, oldest first, the
// last being today): the photos they handed over as a sender and the ones that arrived in services they own, both
// as far as the data file recorded them (senders from the configuration are not recorded).
func (h *Hub) Week(ctx context.Context, user uuid.UUID, now time.Time) (days [7]time.Time, counts [7]int, err error) {
	today := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	for i := range days {
		days[i] = today.AddDate(0, 0, i-6)
	}
	if !h.SelfService() {
		return days, counts, nil
	}
	links, err := h.Store.Links(ctx)
	if err != nil {
		return days, counts, err
	}
	owned := map[string]bool{}
	for _, svc := range h.Services() {
		if svc.OwnerID == user {
			owned[svc.Label()] = true
		}
	}
	var ids []string
	for _, l := range links {
		if l.Role == store.RoleSender && (l.ImmichUserID == user || owned[l.Service]) {
			ids = append(ids, l.ID)
		}
	}
	per, err := h.Store.HandoverDays(ctx, ids, days[0])
	if err != nil {
		return days, counts, err
	}
	for i, d := range days {
		counts[i] = per[d.Format("2006-01-02")]
	}
	return days, counts, nil
}

// GuideDone reports whether the person has finished or hidden the first-run guide. Without a data file there is
// nowhere to remember it, so the guide is not offered (true).
func (h *Hub) GuideDone(ctx context.Context, user uuid.UUID) bool {
	if !h.SelfService() {
		return true
	}
	done, err := h.Store.GuideDone(ctx, user)
	return err != nil || done
}

// DismissGuide remembers that the person finished or hid the first-run guide.
func (h *Hub) DismissGuide(ctx context.Context, user uuid.UUID) error {
	if !h.SelfService() {
		return nil
	}
	return h.Store.SetGuideDone(ctx, user)
}

// ErrBadRules is quiet hours or accept rules that make no sense (the form keeps to sensible values, so this is rare).
var ErrBadRules = errors.New("those rules do not make sense: the hours are times like 01:00, the rating is from 0 to 5 and the sizes are whole numbers of megabytes")

// SetRules sets a service's quiet hours (when it hands photos over) and what it accepts. Only the owner may. Photos
// that waited are looked at again at once, since new rules may let them go.
func (h *Hub) SetRules(ctx context.Context, owner immich.User, service string, q syncer.Quiet, a syncer.Accept) error {
	if q.Validate() != nil || a.Validate() != nil {
		return ErrBadRules
	}
	v, err := h.ownedService(ctx, owner, service)
	if err != nil {
		return err
	}
	v.QuietFrom, v.QuietTo, v.QuietLargeMB = q.From, q.To, q.LargeMB
	v.AcceptRating, v.AcceptPhotos, v.AcceptOrient, v.AcceptMaxMB = a.MinRating, a.PhotosOnly, a.Orientation, a.MaxMB
	if err := h.Store.SetServiceRules(ctx, v.Name, *v); err != nil {
		return err
	}
	h.event(ctx, service, owner.ID, "rules", i18n.Pack("{name} changed the rules of {service}", "name", owner.Name, "service", service))
	if err := h.Reload(ctx); err != nil {
		return err
	}
	h.wake("rules changed")
	return nil
}

// NotAccepted implements syncer.AcceptReporter: the sender's photos that the service does not take in were left
// where they are, with the refused tag, and both sides can see it.
func (h *Hub) NotAccepted(svc *syncer.Service, snd *syncer.Account, n int) {
	text := i18n.PackN("{name}: {n} photo was not handed over to {service}: it does not match what the service accepts", "{name}: {n} photos were not handed over to {service}: they do not match what the service accepts", n,
		"name", snd.Name, "service", svc.Label())
	h.event(context.Background(), svc.Label(), snd.UserID, "notaccepted", text)
}
