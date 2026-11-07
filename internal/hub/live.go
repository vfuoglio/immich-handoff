package hub

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/live"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// audience is who a change to a service concerns: its owner, whoever receives from it, any extra people, and, when
// senders is set, every sender that has joined it. It is worked out from the data file and the configuration, so it
// also holds for a service whose connection was just cut and is no longer in the live view.
func (h *Hub) audience(ctx context.Context, service string, senders bool, extra ...uuid.UUID) []uuid.UUID {
	to := append([]uuid.UUID(nil), extra...)
	for _, svc := range h.Static {
		if svc.Label() == service {
			to = append(to, svc.OwnerID)
			for _, r := range svc.Receivers {
				to = append(to, r.UserID)
			}
			if senders {
				for _, s := range svc.Senders {
					to = append(to, s.UserID)
				}
			}
		}
	}
	if h.Store != nil {
		if owned, err := h.Store.Services(ctx); err == nil {
			for _, v := range owned {
				if v.Name == service {
					to = append(to, v.OwnerID)
				}
			}
		}
		if senders {
			if links, err := h.Store.Links(ctx); err == nil {
				for _, l := range links {
					if l.Service == service && l.Role == store.RoleSender {
						to = append(to, l.ImmichUserID)
					}
				}
			}
		}
	}
	return to
}

// show tells the open pages that something changed in a service.
func (h *Hub) show(ctx context.Context, kind live.Kind, service, sender, reason string, senders bool, extra ...uuid.UUID) {
	h.Bus.Publish(live.Event{Kind: kind, Service: service, Sender: sender, Reason: reason}.To(h.audience(ctx, service, senders, extra...)...))
}

func (h *Hub) showUntil(ctx context.Context, kind live.Kind, service, sender, reason string, count int, until time.Time, extra ...uuid.UUID) {
	e := live.Event{Kind: kind, Service: service, Sender: sender, Reason: reason, Count: count}
	if !until.IsZero() {
		e.Until = &until
	}
	h.Bus.Publish(e.To(h.audience(ctx, service, false, extra...)...))
}
