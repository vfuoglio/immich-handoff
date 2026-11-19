package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// destinationViews fills in a service's destinations for its owner's card, and what the owner can add.
func (w *Server) destinationViews(ctx context.Context, ov *OwnedView, svc *syncer.Service) {
	ov.UserDests = w.Hub.UserDests
	ov.DestOnly = len(svc.Receivers) == 0 && !svc.Pool
	ownerID := svc.OwnerID
	used := map[string]bool{}
	for _, d := range w.Hub.DestinationsOf(ctx, svc.Label()) {
		v := DestView{ID: d.ID, Name: d.Name, Kind: d.Kind, Template: d.Template, Example: d.Example, CapPhotos: d.CapPhotos, CapMB: d.CapMB,
			Paused: d.Paused, Failing: d.State == "failing" && !d.Paused, Own: d.Own, Note: d.Note}
		if d.CapPhotos > 0 || d.CapMB > 0 || d.UsedPhotos > 0 {
			v.Used = t(ctx, "{photos}, {mb} MB in the last 24 hours", "photos", n(ctx, "{n} photo", "{n} photos", d.UsedPhotos), "mb", d.UsedMB)
		}
		if !d.Since.IsZero() {
			v.Since = loc(ctx).Stamp(d.Since.Local())
		}
		ov.Dests = append(ov.Dests, v)
		used[d.Name] = true
	}
	for _, c := range w.Hub.DestinationChoices(ctx, ownerID) {
		if used[c.Name] {
			continue
		}
		ov.DestChoices = append(ov.DestChoices, DestChoiceView{Ref: c.Ref, Name: c.Name, Kind: c.Kind})
	}
	for _, c := range w.Hub.DestinationChoices(ctx, ownerID) { // every destination the person entered can be forgotten once no service uses it
		if c.Own {
			ov.OwnDests = append(ov.OwnDests, DestChoiceView{Ref: c.Ref, Name: c.Name, Kind: c.Kind})
		}
	}
}

func intField(r *http.Request, name string) (int, error) {
	v := strings.TrimSpace(r.PostFormValue(name))
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, hub.ErrBadDestLimit
	}
	return n, nil
}

func destLimits(r *http.Request) (photos, mb int, err error) {
	if photos, err = intField(r, "cap_photos"); err != nil {
		return
	}
	mb, err = intField(r, "cap_mb")
	return
}

func (w *Server) attachDestination(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "destination added", func(ctx context.Context, me *member, service string) error {
		photos, mb, err := destLimits(r)
		if err != nil {
			return err
		}
		return w.Hub.AttachDestination(ctx, me.user, service, r.PostFormValue("ref"), r.PostFormValue("template"), photos, mb)
	})
}

func (w *Server) editDestination(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "destination changed", func(ctx context.Context, me *member, service string) error {
		photos, mb, err := destLimits(r)
		if err != nil {
			return err
		}
		id := r.PostFormValue("id")
		if err := w.Hub.SetDestinationTemplate(ctx, me.user, service, id, r.PostFormValue("template")); err != nil {
			return err
		}
		return w.Hub.SetDestinationLimit(ctx, me.user, service, id, photos, mb)
	})
}

func (w *Server) pauseDestination(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "destination paused", func(ctx context.Context, me *member, service string) error {
		return w.Hub.SetDestinationPaused(ctx, me.user, service, r.PostFormValue("id"), r.PostFormValue("paused") == "1")
	})
}

func (w *Server) removeDestination(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "destination removed", func(ctx context.Context, me *member, service string) error {
		return w.Hub.RemoveDestination(ctx, me.user, service, r.PostFormValue("id"))
	})
}

// addOwnDestination keeps a network destination the person entered: the settings that are not secret and the ones that are come in one
// form, and the hub keeps them apart.
func (w *Server) addOwnDestination(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "own destination added", func(ctx context.Context, me *member, _ string) error {
		p := hub.OwnDestParams{Name: r.PostFormValue("name"), Kind: r.PostFormValue("kind"), Settings: map[string]string{}}
		for _, k := range []string{"url", "endpoint", "bucket", "region", "host", "port", "user", "password", "access_key", "secret_key", "private_key"} {
			if v := strings.TrimSpace(r.PostFormValue(k)); v != "" {
				p.Settings[k] = v
			}
		}
		_, err := w.Hub.AddOwnDestination(ctx, me.user, p)
		return err
	})
}

func (w *Server) forgetOwnDestination(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "own destination forgotten", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.ForgetOwnDestination(ctx, me.user, r.PostFormValue("ref"))
	})
}
