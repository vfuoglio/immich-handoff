package hub

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// TakeBackWindow is how long after a hand-over a sender can still take it back.
const TakeBackWindow = 24 * time.Hour

var (
	// ErrDeleteOff is what is said when the administrator has switched off the receiver key's delete permission
	// (RECEIVER_DELETE=off): nothing can expire and nothing can be taken back.
	ErrDeleteOff = errors.New("this is switched off by the administrator (RECEIVER_DELETE)")
	// ErrNothingToTakeBack: there is no hand-over to take back.
	ErrNothingToTakeBack = errors.New("there is nothing to take back: nothing was handed over to this service in the last 24 hours, or it was already taken back")
	// ErrReceiverCannotDelete: the owner's key has not got the permission yet (it gets it when its owner opens the page).
	ErrReceiverCannotDelete = errors.New("the key of the service's owner cannot move photos to the trash yet: ask the owner to open this page once")
	// ErrBadLifetime is a lifetime that is not a number of days.
	ErrBadLifetime = errors.New("the lifetime is a whole number of days from 1 to 3650, or empty for none")
)

// Remember implements syncer.Ledger: which copy came from which photo, so the sender can take it back. Only people who
// joined from the page have a membership to keep it under; others are not recorded.
func (h *Hub) Remember(ctx context.Context, snd *syncer.Account, batch string, senderAsset, receiverAsset uuid.UUID, clip bool) {
	id, ok := h.linkOf(snd)
	if !ok || h.Store == nil {
		return
	}
	if err := h.Store.AddHandoverItem(context.WithoutCancel(ctx), id, batch, senderAsset, receiverAsset, clip); err != nil {
		h.Log.Warn("could not remember a hand-over", "err", err)
	}
}

// Expired implements syncer.ExpiryReporter: copies reached the end of their lifetime and went to the owner's trash.
func (h *Hub) Expired(service string, rcv *syncer.Account, n int) {
	text := i18n.PackN("{n} photo handed over to {service} reached the end of its lifetime and was moved to your trash", "{n} photos handed over to {service} reached the end of their lifetime and were moved to your trash", n,
		"service", service)
	h.event(context.Background(), service, rcv.UserID, "expired", text)
}

// SetLifetime sets how long the copies that arrive in the owner's library stay before they are moved to the owner's
// trash: unless the owner files them in an album or takes the inbox tag off (0: they stay for ever). It needs the
// receiver key to be allowed to delete, which the owner's own session gives it at this moment.
func (h *Hub) SetLifetime(ctx context.Context, sess *immich.API, owner immich.User, service string, days int) error {
	if days < 0 || days > 3650 {
		return ErrBadLifetime
	}
	if days > 0 && !h.ReceiverDelete {
		return ErrDeleteOff
	}
	v, err := h.ownedService(ctx, owner, service)
	if err != nil {
		return err
	}
	if days > 0 {
		if err := sess.AddKeyPermission(ctx, v.KeyID, "asset.delete"); err != nil {
			return fmt.Errorf("could not allow the app to move expired copies to your trash: %w", err)
		}
	}
	if err := h.Store.SetServiceLifetime(ctx, v.Name, days); err != nil {
		return err
	}
	if days > 0 {
		h.event(ctx, service, owner.ID, "lifetime", i18n.PackN("{name} set the copies handed over to {service} to expire after {n} day", "{name} set the copies handed over to {service} to expire after {n} days", days, "name", owner.Name, "service", service))
	} else {
		h.event(ctx, service, owner.ID, "lifetime", i18n.Pack("{name} turned off expiry for {service}", "name", owner.Name, "service", service))
	}
	return h.Reload(ctx)
}

// TakeBackOffer says how many photos the sender's last hand-over to the service holds that can still be taken back, and
// when it was; zero when there is nothing to offer (or taking back is switched off).
func (h *Hub) TakeBackOffer(ctx context.Context, user uuid.UUID, service string) (int, time.Time) {
	if !h.SelfService() || !h.ReceiverDelete {
		return 0, time.Time{}
	}
	link, err := h.senderLink(ctx, user, service)
	if err != nil {
		return 0, time.Time{}
	}
	items, err := h.Store.LastHandover(ctx, link.ID, time.Now().Add(-TakeBackWindow))
	if err != nil || len(items) == 0 {
		return 0, time.Time{}
	}
	n := 0
	for _, it := range items {
		if !it.Clip {
			n++
		}
	}
	return n, items[len(items)-1].At
}

// TakeBackResult is what taking a hand-over back did.
type TakeBackResult struct {
	Taken int // photos whose copy went to the trash (and whose original came back from the sender's trash)
	Kept  int // photos the receiver had already filed in an album: they stay where they are, copy and original alike
}

// TakeBack undoes the sender's last hand-over to a service, as far as it can be undone: the receiver's copies go to the
// receiver's trash, the sender's originals come back from the sender's trash (when they were moved) and lose the service
// tag so they are not handed over again. A copy the receiver has filed in an album is theirs now: it stays, and so does
// the original's state. Only the trash is used, nothing is deleted for good.
func (h *Hub) TakeBack(ctx context.Context, user immich.User, service string) (TakeBackResult, error) {
	var res TakeBackResult
	if !h.SelfService() {
		return res, ErrNotJoined
	}
	if !h.ReceiverDelete {
		return res, ErrDeleteOff
	}
	link, err := h.senderLink(ctx, user.ID, service)
	if err != nil {
		return res, err
	}
	items, err := h.Store.LastHandover(ctx, link.ID, time.Now().Add(-TakeBackWindow))
	if err != nil {
		return res, err
	}
	if len(items) == 0 {
		return res, ErrNothingToTakeBack
	}
	svc := h.service(service)
	if svc == nil || len(svc.Receivers) == 0 {
		return res, ErrNotJoined
	}
	rcv := svc.Receivers[0].API
	snd, err := h.Connect(link.Key)
	if err != nil {
		return res, err
	}

	// which copies can go: not the ones the receiver filed in an album, and not a clip whose still stays
	keepClip := map[uuid.UUID]bool{}
	var settled []int64 // the photos the receiver kept: nothing more is offered for them
	var handedBack []store.HandoverItem
	var gone []uuid.UUID
	for _, clip := range []bool{false, true} {
		for _, it := range items {
			if it.Clip != clip {
				continue
			}
			if clip && keepClip[it.ReceiverAsset] {
				settled = append(settled, it.ID)
				continue
			}
			a, err := rcv.Asset(ctx, it.ReceiverAsset)
			switch {
			case err != nil && immich.IsNotFound(err):
				handedBack = append(handedBack, it) // already gone: only the sender's side is left
				continue
			case err != nil:
				return res, err
			case a.IsTrashed:
				handedBack = append(handedBack, it)
				continue
			}
			if !clip {
				filed, err := rcv.InAnAlbum(ctx, it.ReceiverAsset)
				if err != nil {
					return res, err
				}
				if filed {
					res.Kept++
					settled = append(settled, it.ID)
					if a.LivePhotoVideoId != nil {
						if id, err := uuid.Parse(*a.LivePhotoVideoId); err == nil {
							keepClip[id] = true
						}
					}
					continue
				}
			}
			handedBack = append(handedBack, it)
			gone = append(gone, it.ReceiverAsset)
		}
	}
	if len(handedBack) == 0 {
		if err := h.Store.MarkTakenBack(ctx, settled); err != nil {
			h.Log.Warn("could not record what the receiver kept", "err", err)
		}
		return res, nil
	}
	if len(gone) > 0 {
		if err := rcv.DeleteAssets(ctx, gone, false); err != nil {
			if immich.IsForbidden(err) {
				return res, ErrReceiverCannotDelete
			}
			return res, fmt.Errorf("could not move the copies to the receiver's trash: %w", err)
		}
	}

	// the sender's side: the originals that were moved come back, and none is handed over again by its old tag
	var senderIDs, trashed []uuid.UUID
	var done []int64
	for _, it := range handedBack {
		senderIDs = append(senderIDs, it.SenderAsset)
		done = append(done, it.ID)
		if !it.Clip {
			res.Taken++
		}
		if a, err := snd.Asset(ctx, it.SenderAsset); err == nil && a.IsTrashed {
			trashed = append(trashed, it.SenderAsset)
		}
	}
	if err := h.Store.MarkTakenBack(ctx, append(done, settled...)); err != nil {
		h.Log.Warn("could not record that a hand-over was taken back", "err", err)
	}
	var restoreErr error
	if len(trashed) > 0 {
		restoreErr = snd.RestoreAssets(ctx, trashed)
	}
	for _, name := range []string{svc.Tag, svc.SentTag} {
		if name == "" {
			continue
		}
		if id, err := snd.FindTag(ctx, name, false); err == nil && id != nil {
			if err := snd.UntagAssets(ctx, *id, senderIDs); err != nil {
				h.Log.Warn("could not take a tag off a photo taken back", "tag", name, "err", err)
			}
		}
	}
	h.event(ctx, service, user.ID, "takenback", i18n.PackN("{name} took back {n} photo handed over to {service}", "{name} took back {n} photos handed over to {service}", res.Taken, "name", user.Name, "service", service))
	h.Log.Info("hand-over taken back", "service", service, "user", user.Email, "taken", res.Taken, "kept", res.Kept)
	if restoreErr != nil {
		return res, fmt.Errorf("the copies were moved to the receiver's trash, but your originals could not be restored from yours (restore them in Immich's trash): %w", restoreErr)
	}
	return res, nil
}
