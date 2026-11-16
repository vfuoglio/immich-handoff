package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/vfuoglio/immich-handoff/internal/hub"
)

// The hand-over button in Immich's own pages (internal/web/ts/immich-button.ts, docs/design/immich-button-design.md) asks the
// app for three things, from Immich's address under /handoff/, so the person's Immich cookie comes with each request:
// the services they send to with the button's words in their language, and then to hand photos over, and to undo that.
// The page never holds a key: the app checks the person, checks the photos are theirs and does the tagging itself.

// ButtonService is a service the person can hand photos over to.
type ButtonService struct {
	Name string `json:"name"`
	Copy bool   `json:"copy"`           // their originals stay; otherwise they go to the trash once the other side has a copy
	Note string `json:"note,omitempty"` // why photos will wait (paused, daily limit), in words
}

// ButtonOffer is a service the person does not send to yet but may ask to join.
type ButtonOffer struct {
	Name    string `json:"name"`
	Pending bool   `json:"pending"` // they asked already and the owner has not answered
}

// ButtonWords are what the button says, in the person's language.
func buttonWords(ctx context.Context) map[string]string {
	ids := []string{
		"Hand over...", "Hand over to", "Undo", "Open Handoff", "Handing over...", "Could not hand over", "Ask to join", "Waiting for approval", "Last used", "Forget my choice",
		"Your original stays in your library", "Your original goes to your trash once the other side has a copy",
		"You do not send to any service yet.", "Taken off again", "Immich Handoff",
		"Add to my library", "Adding to your library...", "Could not add", "Where should it go?", "In my library, tagged with who shared it",
		"In my library", "In an album", "Remember this choice", "Choose an album", "or the name of a new album", "Add here", "Could not list your albums", "Try again",
	}
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		out[id] = t(ctx, id)
	}
	return out
}

// buttonServices lists the services the person sends to from this page: the others have no key of the person's to tag
// with, so the button does not offer them.
func (w *Server) buttonServices(ctx context.Context, me *member) []ButtonService {
	out := []ButtonService{}
	for _, v := range w.servicesFor(ctx, me) {
		if v.Role != "sender" || !v.CanWatch || v.Paused != "" {
			continue
		}
		note := v.Hold
		if note == "" {
			note = v.Waiting
		}
		out = append(out, ButtonService{Name: v.Name, Copy: v.Copy, Note: note})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// buttonOffers lists the services the person could ask to join from Immich's pages: those whose owner decides.
func (w *Server) buttonOffers(ctx context.Context, me *member) []ButtonOffer {
	out := []ButtonOffer{}
	if w.Hub == nil {
		return out
	}
	offers, err := w.Hub.Offers(ctx, me.user.ID)
	if err != nil {
		return out
	}
	for _, o := range offers {
		if o.State == "request" || o.State == "pending" {
			out = append(out, ButtonOffer{Name: o.Service.Label(), Pending: o.State == "pending"})
		}
	}
	return out
}

func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	json.NewEncoder(rw).Encode(v)
}

// buttonInfo is what the button needs when it first shows. A person Immich does not know gets 401, and the button
// stays away.
func (w *Server) buttonInfo(rw http.ResponseWriter, r *http.Request) {
	if w.ButtonOff {
		http.NotFound(rw, r)
		return
	}
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	def, chosen := w.Hub.LibraryChoice(r.Context(), me.user.ID)
	writeJSON(rw, map[string]any{"words": buttonWords(r.Context()), "services": w.buttonServices(r.Context(), me), "offers": w.buttonOffers(r.Context(), me),
		"library": map[string]any{"dest": def.Kind, "album": def.Album, "chosen": chosen}})
}

func (w *Server) buttonSend(rw http.ResponseWriter, r *http.Request) {
	w.buttonAct(rw, r, func(ctx context.Context, me *member, service string, assets []string) (any, error) {
		sent, err := w.Hub.HandOver(ctx, me.sess, me.user, service, assets)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sent": sent, "service": service,
			"message": n(ctx, "{n} photo handed over to {service}", "{n} photos handed over to {service}", sent, "service", service)}, nil
	})
}

func (w *Server) buttonUndo(rw http.ResponseWriter, r *http.Request) {
	w.buttonAct(rw, r, func(ctx context.Context, me *member, service string, assets []string) (any, error) {
		if err := w.Hub.UndoHandOver(ctx, me.sess, me.user, service, assets); err != nil {
			return nil, err
		}
		return map[string]any{"message": t(ctx, "Taken off again")}, nil
	})
}

// buttonOwned says which of the photos the person looks at are theirs and which are someone else's that they can see
// (a shared album, a partner), so the button offers Add to my library for the second kind. Unreadable ids are left out.
func (w *Server) buttonOwned(rw http.ResponseWriter, r *http.Request) {
	if w.ButtonOff || w.Hub == nil {
		http.NotFound(rw, r)
		return
	}
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	mine, others, err := w.Hub.Ownership(r.Context(), me.sess, me.user, r.URL.Query()["asset"])
	if err != nil {
		http.Error(rw, t(r.Context(), "Could not do that: {reason}", "reason", t(r.Context(), err.Error())), http.StatusBadRequest)
		return
	}
	if mine == nil {
		mine = []string{}
	}
	if others == nil {
		others = []string{}
	}
	writeJSON(rw, map[string]any{"mine": mine, "others": others})
}

// buttonAlbums lists the person's own albums for the choice of an album to put copies in; it is asked for when that
// choice is opened.
func (w *Server) buttonAlbums(rw http.ResponseWriter, r *http.Request) {
	if w.ButtonOff || w.Hub == nil {
		http.NotFound(rw, r)
		return
	}
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	albums, err := me.sess.OwnedAlbums(r.Context())
	if err != nil {
		w.Log.Warn("could not list albums", "user", me.user.Email, "err", err)
		http.Error(rw, t(r.Context(), "Could not list your albums"), http.StatusBadGateway)
		return
	}
	type album struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	out := make([]album, 0, len(albums))
	for _, a := range albums {
		out = append(out, album{ID: a.ID.String(), Name: a.Name, Count: a.Count})
	}
	writeJSON(rw, map[string]any{"albums": out})
}

// buttonAdd copies photos the person can see but does not own into their own library.
func (w *Server) buttonAdd(rw http.ResponseWriter, r *http.Request) {
	w.buttonAct(rw, r, func(ctx context.Context, me *member, _ string, assets []string) (any, error) {
		dest := hub.LibraryDest{Kind: r.PostFormValue("dest"), Album: r.PostFormValue("album")}
		res, err := w.Hub.AddToLibrary(ctx, me.sess, me.user, assets, dest)
		if err != nil {
			return nil, err
		}
		if r.PostFormValue("remember") == "1" {
			if err := w.Hub.SetLibraryDefault(ctx, me.user.ID, dest); err != nil {
				w.Log.Warn("could not remember where copies go", "user", me.user.Email, "err", err)
			}
		}
		copies := make([]string, 0, len(res.Copies))
		for _, c := range res.Copies {
			copies = append(copies, c.String())
		}
		msg := ""
		switch {
		case res.Added > 0:
			msg = n(ctx, "{n} photo added to your library", "{n} photos added to your library", res.Added)
			if res.Note != "" {
				msg += " " + t(ctx, res.Note)
			}
		case res.Already > 0:
			msg = t(ctx, "Already in your library")
		default:
			msg = t(ctx, "These photos are already yours")
		}
		return map[string]any{"added": res.Added, "already": res.Already, "skipped": res.Skipped, "copies": copies, "message": msg}, nil
	})
}

// buttonAddUndo takes copies that were just added back out of the library (into Immich's trash).
func (w *Server) buttonAddUndo(rw http.ResponseWriter, r *http.Request) {
	w.buttonAct(rw, r, func(ctx context.Context, me *member, _ string, copies []string) (any, error) {
		gone, err := w.Hub.UndoAddToLibrary(ctx, me.sess, me.user, copies, r.PostFormValue("album"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"removed": gone, "message": n(ctx, "{n} photo taken out of your library again", "{n} photos taken out of your library again", gone)}, nil
	})
}

// buttonAct is what the two changes have in common: the cross-site header, the person, the switch, the form.
func (w *Server) buttonAct(rw http.ResponseWriter, r *http.Request, act func(context.Context, *member, string, []string) (any, error)) {
	if w.ButtonOff || w.Hub == nil {
		http.NotFound(rw, r)
		return
	}
	me := w.guarded(rw, r)
	if me == nil {
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(rw, t(r.Context(), "Could not do that: {reason}", "reason", t(r.Context(), "no photos were chosen")), http.StatusBadRequest)
		return
	}
	service := r.PostFormValue("service")
	out, err := act(r.Context(), me, service, r.PostForm["asset"])
	if err != nil {
		status := http.StatusInternalServerError
		if hub.IsUserError(err) {
			status = http.StatusConflict
		}
		if errors.Is(err, hub.ErrNotJoined) {
			status = http.StatusForbidden
		}
		w.Log.Warn("hand-over from Immich failed", "user", me.user.Email, "service", service, "err", err)
		http.Error(rw, t(r.Context(), "Could not do that: {reason}", "reason", t(r.Context(), err.Error())), status)
		return
	}
	writeJSON(rw, out)
}

// buttonAsk is a request to join a service whose owner decides, made from the button's list.
func (w *Server) buttonAsk(rw http.ResponseWriter, r *http.Request) {
	if w.ButtonOff || w.Hub == nil {
		http.NotFound(rw, r)
		return
	}
	me := w.guarded(rw, r)
	if me == nil {
		return
	}
	service := r.PostFormValue("service")
	if err := w.Hub.Ask(r.Context(), me.user, service, ""); err != nil {
		status := http.StatusInternalServerError
		if hub.IsUserError(err) {
			status = http.StatusConflict
		}
		w.Log.Warn("asking to join from Immich failed", "user", me.user.Email, "service", service, "err", err)
		http.Error(rw, t(r.Context(), "Could not do that: {reason}", "reason", t(r.Context(), err.Error())), status)
		return
	}
	writeJSON(rw, map[string]any{"message": t(r.Context(), "You asked to join {service}", "service", service)})
}
