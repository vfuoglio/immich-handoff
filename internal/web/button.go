package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/vfuoglio/immich-handoff/internal/hub"
)

// The hand-over button in Immich's own pages (internal/web/ts/immich-button.ts, docs/immich-button-design.md) asks the
// app for three things, from Immich's address under /handoff/, so the person's Immich cookie comes with each request:
// the services they send to with the button's words in their language, and then to hand photos over, and to undo that.
// The page never holds a key: the app checks the person, checks the photos are theirs and does the tagging itself.

// ButtonService is a service the person can hand photos over to.
type ButtonService struct {
	Name string `json:"name"`
	Copy bool   `json:"copy"`           // their originals stay; otherwise they go to the trash once the other side has a copy
	Note string `json:"note,omitempty"` // why photos will wait (paused, daily limit), in words
}

// ButtonWords are what the button says, in the person's language.
func buttonWords(ctx context.Context) map[string]string {
	ids := []string{
		"Hand over...", "Hand over to", "Undo", "Open Handoff", "Handing over...", "Could not hand over",
		"Your original stays in your library", "Your original goes to your trash once the other side has a copy",
		"You do not send to any service yet.", "Taken off again", "Immich Handoff",
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
	writeJSON(rw, map[string]any{"words": buttonWords(r.Context()), "services": w.buttonServices(r.Context(), me)})
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
