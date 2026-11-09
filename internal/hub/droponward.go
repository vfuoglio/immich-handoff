package hub

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

var (
	ErrOnwardService = errors.New("you do not send to that service: join it first")
	ErrNoOnward      = errors.New("choose a service to hand the photos on to first")
	ErrBadScope      = errors.New("hand over everything or the best-of")
)

// OnwardOption is a service a person sends to, which a drop box's photos can be handed on to.
type OnwardOption struct {
	Service string
	Copy    bool // the person keeps their originals when they hand something over to it; otherwise the originals go to their trash
}

// OnwardOptions lists the services the person has joined as a sender from the page.
func (h *Hub) OnwardOptions(ctx context.Context, user uuid.UUID) []OnwardOption {
	if h.Store == nil {
		return nil
	}
	links, err := h.Store.Links(ctx)
	if err != nil {
		return nil
	}
	var out []OnwardOption
	for _, l := range links {
		if l.ImmichUserID == user && l.Role == store.RoleSender && l.State == store.StateActive && h.service(l.Service) != nil {
			out = append(out, OnwardOption{Service: l.Service, Copy: l.Mode == "copy"})
		}
	}
	return out
}

// SetDropOnward chooses the service a box's photos are handed on to, and whether that happens by itself when the box
// closes (everything, never the best-of: that needs the owner to look at it first). An empty service clears it. Only the
// owner may, and only for a service they send to.
func (h *Hub) SetDropOnward(ctx context.Context, owner immich.User, id, service string, auto bool) error {
	b, _, err := h.dropBoxFor(ctx, owner.ID, id, "owner")
	if err != nil {
		return err
	}
	if service != "" {
		if _, err := h.senderLink(ctx, owner.ID, service); err != nil || h.service(service) == nil {
			return ErrOnwardService
		}
	}
	return h.Store.SetDropOnward(ctx, b.ID, service, auto)
}

// HandOnward hands a box's photos on to the box's chosen service now, as the owner: everything in the box's inbox tag,
// or the photos the best-of kept. It is the hand-over that exists, so the service's limits, quiet hours, rules, expiry
// and take-back all apply, and the owner's choice of moving or keeping originals for that service decides what happens to
// them. Only the owner may. It returns how many photos were handed on.
func (h *Hub) HandOnward(ctx context.Context, owner immich.User, id, scope string) (int, error) {
	b, _, err := h.dropBoxFor(ctx, owner.ID, id, "owner")
	if err != nil {
		return 0, err
	}
	if b.OnwardService == "" {
		return 0, ErrNoOnward
	}
	return h.handOnward(ctx, owner.ID, owner.Name, b, b.OnwardService, scope)
}

func (h *Hub) handOnward(ctx context.Context, ownerID uuid.UUID, ownerName string, b store.DropBox, service, scope string) (int, error) {
	if scope != "all" && scope != "best" {
		return 0, ErrBadScope
	}
	link, err := h.senderLink(ctx, ownerID, service)
	svc := h.service(service)
	if err != nil || svc == nil {
		return 0, ErrOnwardService
	}
	api, err := h.Connect(link.Key)
	if err != nil {
		return 0, err
	}
	var ids []uuid.UUID
	if scope == "best" {
		vs, err := h.Store.DropVerdicts(ctx, b.ID, false, 100000)
		if err != nil {
			return 0, err
		}
		for _, v := range vs {
			if id, err := uuid.Parse(v.AssetID); err == nil {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return 0, ErrNoHighlights
		}
	} else {
		tag, err := api.FindTag(ctx, b.InboxTag, false)
		if err != nil {
			return 0, err
		}
		if tag == nil {
			return 0, ErrNothingToShare
		}
		assets, err := api.Search(ctx, immich.MetadataSearchDto{TagIds: &[]uuid.UUID{*tag}})
		if err != nil {
			return 0, err
		}
		for _, a := range assets {
			ids = append(ids, a.Id)
		}
		if len(ids) == 0 {
			return 0, ErrNothingToShare
		}
	}
	tag, err := api.FindTag(ctx, svc.Tag, true)
	if err != nil {
		return 0, err
	}
	n := len(ids)
	for len(ids) > 0 {
		k := min(len(ids), 200)
		if err := api.TagAssets(ctx, *tag, ids[:k]...); err != nil {
			return 0, err
		}
		ids = ids[k:]
	}
	_ = h.Store.AddDropOnward(context.WithoutCancel(ctx), b.ID, store.DropOnward{Service: service, Scope: scope, Count: n, By: ownerName})
	h.dropEvent(ctx, b.ID, store.DropCreated, i18n.PackN("{n} photo handed on to {service}", "{n} photos handed on to {service}", n, "service", service), "")
	if h.Wake != nil {
		h.Wake(fmt.Sprintf("a drop box handed on to %s", service))
	}
	return n, nil
}
