package hub

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/notify"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// What an Immich administrator may not do from the admin page.
var (
	ErrNotRemovable = errors.New("this entry comes from the configuration (.env or the routes file): change it there")
	ErrNoSuchEntry  = errors.New("there is no such service or member (it may already have been removed)")
)

// AdminMember is a sender of a service, as the administrator sees it.
type AdminMember struct {
	LinkID     string // empty for senders from the configuration, which cannot be removed here
	Name       string
	Email      string
	State      string // active | broken | orphaned
	Note       string
	Copy       bool // their originals are kept
	Album      string
	FromConfig bool
	Paused     bool // they paused their membership
}

// AdminService is one service with its owner, members and health, for the administrator's page.
type AdminService struct {
	Name, Tag, InboxTag   string
	OwnerName, OwnerEmail string // empty for services from the configuration
	Policy                string // approval | open
	InviteOnly            bool
	Copy                  bool
	Album                 string
	Receivers             int
	Broken                string // why it cannot work right now, if so
	Paused                bool   // the owner paused it
	LimitPhotos           int    // daily cap per sender, 0: none
	LimitMB               int
	FromConfig            bool
	Members               []AdminMember
	Created               time.Time
}

// Overview lists every service and who takes part in it. It includes people whose service was removed and who
// have not cleaned up yet, as Leftover.
func (h *Hub) Overview(ctx context.Context) (services []AdminService, leftover []AdminMember, err error) {
	var owned map[string]store.Service
	var links []store.Link
	if h.Store != nil {
		list, err := h.Store.Services(ctx)
		if err != nil {
			return nil, nil, err
		}
		owned = map[string]store.Service{}
		for _, v := range list {
			owned[v.Name] = v
		}
		if links, err = h.Store.Links(ctx); err != nil {
			return nil, nil, err
		}
	}
	live := map[string]bool{}
	for _, svc := range h.Services() {
		live[svc.Label()] = true
		v, isOwned := owned[svc.Label()]
		a := AdminService{
			Name: svc.Label(), Tag: svc.Tag, InboxTag: svc.InboxTag, Copy: svc.Copies(), Album: svc.Album,
			Receivers: len(svc.Receivers), Broken: svc.Broken, FromConfig: !isOwned,
			Paused: svc.Paused, LimitPhotos: svc.LimitPhotos, LimitMB: svc.LimitMB,
			Policy: "approval", InviteOnly: svc.Unlisted,
		}
		if svc.AllowJoin {
			a.Policy = "open"
		}
		if isOwned {
			a.OwnerName, a.OwnerEmail, a.Created = v.OwnerName, v.OwnerEmail, v.CreatedAt
		} else {
			for _, s := range svc.Senders {
				a.Members = append(a.Members, AdminMember{Name: s.Name, State: store.StateActive, Copy: svc.CopiesFor(s), FromConfig: true})
			}
		}
		for _, l := range links {
			if l.Service == svc.Label() && l.Role == store.RoleSender {
				a.Members = append(a.Members, AdminMember{LinkID: l.ID, Name: l.Name, Email: l.Email, State: l.State, Note: l.Note,
					Copy: l.Mode != syncer.ModeMove, Album: l.AlbumName, Paused: l.Paused})
			}
		}
		services = append(services, a)
	}
	for _, l := range links {
		if l.Role == store.RoleSender && !live[l.Service] {
			leftover = append(leftover, AdminMember{LinkID: l.ID, Name: l.Name + " (" + l.Service + ")", Email: l.Email, State: l.State, Note: l.Note, Copy: l.Mode != syncer.ModeMove})
		}
	}
	return services, leftover, nil
}

// AdminActivity is one line of the whole activity log.
type AdminActivity struct {
	At                  time.Time
	Service, Text, Kind string
}

// RecentEvents returns the newest entries of the activity log across every service.
func (h *Hub) RecentEvents(ctx context.Context, limit int) ([]AdminActivity, error) {
	if h.Store == nil {
		return nil, nil
	}
	events, err := h.Store.Events(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]AdminActivity, 0, len(events))
	for _, e := range events {
		out = append(out, AdminActivity{e.At, e.Service, e.Text, e.Kind})
	}
	return out, nil
}

// AdminRemoveMember ends a sender's membership on an administrator's say-so. Immich lets only the owner of an API
// key delete it, and the app holds no credential that can act for another person, so the person's key and
// workflow stay in their Immich account: the app forgets the key and stops using it, and the person is told on
// their page (where Clean up deletes both) and in the activity log. Their photos are never touched.
func (h *Hub) AdminRemoveMember(ctx context.Context, admin immich.User, linkID string) error {
	if !h.SelfService() {
		return ErrNotRemovable
	}
	links, err := h.Store.Links(ctx)
	if err != nil {
		return err
	}
	var link *store.Link
	for i := range links {
		if links[i].ID == linkID && links[i].Role == store.RoleSender {
			link = &links[i]
		}
	}
	if link == nil {
		return ErrNoSuchEntry
	}
	note := i18n.Pack("An administrator ({admin}) removed you from this service on {date}. The API key named \"{key}\" is still valid in your Immich account until you delete it; Clean up deletes it, and the workflow, for you.",
		"admin", admin.Name, "date", i18n.Day(time.Now()), "key", keyName(link.Service))
	if ok, err := h.Store.SetLinkState(ctx, link.ID, store.StateBroken, note); err != nil {
		return err
	} else if !ok {
		return ErrNoSuchEntry
	}
	if err := h.Store.ForgetLinkKey(ctx, link.ID); err != nil {
		return err
	}
	h.event(ctx, link.Service, link.ImmichUserID, "removed", i18n.Pack("{name} was removed from {service} by an administrator; their key in Immich stays until they clean up", "name", link.Name, "service", link.Service))
	h.tell(notify.Info, fmt.Sprintf("%s was removed from %q by an administrator (%s).", link.Name, link.Service, admin.Name))
	h.Log.Info("member removed by an administrator", "service", link.Service, "member", link.Email, "admin", admin.Email)
	return h.Reload(ctx)
}

// AdminRemoveService removes a service someone created on the page, on an administrator's say-so. The owner's
// receiver key stays in the owner's Immich account (only they can delete it); the members are told their service
// is gone and can clean up their own keys, as when an owner removes a service.
func (h *Hub) AdminRemoveService(ctx context.Context, admin immich.User, name string) error {
	if !h.SelfService() {
		return ErrNotRemovable
	}
	owned, err := h.Store.Services(ctx)
	if err != nil {
		return err
	}
	var svc *store.Service
	for i := range owned {
		if owned[i].Name == name {
			svc = &owned[i]
		}
	}
	if svc == nil {
		if h.service(name) != nil {
			return ErrNotRemovable
		}
		return ErrNoSuchEntry
	}
	links, err := h.Store.Links(ctx)
	if err != nil {
		return err
	}
	if err := h.Store.RemoveService(ctx, name); err != nil { // also marks every member's link orphaned
		return err
	}
	note := i18n.Pack("An administrator ({admin}) removed the service {service} on {date}. Nothing is handed over any more and your photos were not touched.", "admin", admin.Name, "service", name, "date", i18n.Day(time.Now()))
	for _, l := range links {
		if l.Service != name || l.Role != store.RoleSender {
			continue
		}
		if _, err := h.Store.SetLinkState(ctx, l.ID, store.StateOrphaned, note); err != nil {
			h.Log.Warn("could not note the removal on a member", "member", l.Email, "err", err)
		}
		h.event(ctx, name, l.ImmichUserID, "removed", i18n.Pack("The service {service} was removed by an administrator", "service", name))
	}
	h.event(ctx, name, svc.OwnerID, "removed", i18n.Pack("Your service {service} was removed by an administrator. The receiver key \"{key}\" is still valid in your Immich account until you delete it (Account Settings, API Keys).", "service", name, "key", keyName(name)+" (receiving)"))
	h.tell(notify.Info, fmt.Sprintf("The service %q (owner %s) was removed by an administrator (%s).", name, svc.OwnerName, admin.Name))
	h.Log.Info("service removed by an administrator", "service", name, "owner", svc.OwnerEmail, "admin", admin.Email)
	return h.Reload(ctx)
}
