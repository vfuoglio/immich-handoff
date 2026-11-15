package hub

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
)

// HandOverMax is how many photos one press may hand over: each is looked at in the person's own Immich first.
const HandOverMax = 200

var (
	ErrNoPhotosChosen = errors.New("no photos were chosen")
	ErrTooManyChosen  = errors.New("too many photos at once: choose fewer")
	ErrNotYourPhotos  = errors.New("some of these photos are not yours")
)

// HandOver is the button in Immich's own pages: it hands the chosen photos over to a service the person sends to, in
// the same way as putting the service's tag on them in Immich does. sess is the person's own session; the photos
// must be theirs. The tag goes on with the key the person made when they joined, so everything that rules the hand-over
// (pause, limits, quiet hours, taking back) applies as it does to a tag put on by hand. It returns how many photos it
// tagged.
func (h *Hub) HandOver(ctx context.Context, sess *immich.API, user immich.User, service string, assets []string) (int, error) {
	ids, err := h.ownPhotos(ctx, sess, user, assets)
	if err != nil {
		return 0, err
	}
	if err := h.tagFor(ctx, user.ID, service, ids, true, "photos handed over from Immich"); err != nil {
		return 0, err
	}
	h.event(ctx, service, user.ID, "button", i18n.PackN("{n} photo handed over to {service} from Immich", "{n} photos handed over to {service} from Immich", len(ids), "service", service))
	return len(ids), nil
}

// UndoHandOver takes the service's tag off photos the button tagged, so a pass that has not yet taken them leaves them
// alone. Photos already handed over are not brought back: that is taking back, which has its own button.
func (h *Hub) UndoHandOver(ctx context.Context, sess *immich.API, user immich.User, service string, assets []string) error {
	ids, err := h.ownPhotos(ctx, sess, user, assets)
	if err != nil {
		return err
	}
	return h.tagFor(ctx, user.ID, service, ids, false, "")
}

// ownPhotos checks that the ids are photos in the person's own library, once each.
func (h *Hub) ownPhotos(ctx context.Context, sess *immich.API, user immich.User, assets []string) ([]string, error) {
	seen := map[uuid.UUID]bool{}
	var ids []string
	for _, a := range assets {
		id, err := uuid.Parse(a)
		if err != nil {
			return nil, ErrNotYourPhotos
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id.String())
		}
	}
	switch {
	case len(ids) == 0:
		return nil, ErrNoPhotosChosen
	case len(ids) > HandOverMax:
		return nil, ErrTooManyChosen
	}
	for _, a := range ids {
		info, err := sess.Asset(ctx, uuid.MustParse(a))
		if err != nil || info.OwnerId != user.ID {
			return nil, ErrNotYourPhotos
		}
	}
	return ids, nil
}
