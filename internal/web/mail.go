package web

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
)

// myMail turns the signed-in person's email on, changes it, turns it off, or sends them a test.
func (w *Server) myMail(rw http.ResponseWriter, r *http.Request) {
	me := w.guarded(rw, r)
	if me == nil {
		return
	}
	ctx := r.Context()
	if w.Hub == nil {
		http.Error(rw, t(ctx, "Joining from this page is not switched on."), http.StatusNotFound)
		return
	}
	lang := string(loc(ctx).Lang)
	var err error
	switch r.PostFormValue("action") {
	case "on":
		err = w.Hub.TurnMailOn(ctx, me.user, lang, r.PostFormValue("mode"))
	case "off":
		err = w.Hub.TurnMailOff(ctx, uuid.UUID(me.user.ID))
	case "test":
		if err = w.Hub.SendTestMail(ctx, me.user, lang); err == nil {
			fmt.Fprint(rw, t(ctx, "A test email is on its way to {email}.", "email", me.user.Email))
			return
		}
	default:
		http.Error(rw, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		status := http.StatusInternalServerError
		if hub.IsUserError(err) {
			status = http.StatusConflict
		}
		w.Log.Warn("changing a person's email settings failed", "action", r.PostFormValue("action"), "err", err)
		rw.Header().Set("HX-Reswap", "innerHTML")
		rw.WriteHeader(status)
		fmt.Fprint(rw, t(ctx, "Could not do that: {reason}", "reason", t(ctx, err.Error())))
		return
	}
	rw.Header().Set("HX-Refresh", "true")
	rw.WriteHeader(http.StatusNoContent)
}

// unsubscribePage is where the link in a mail leads. It stops nothing by itself (a program that follows links, such as
// a mail scanner, must not unsubscribe anyone); the button does. A link that was not made by this app is "not found".
func (w *Server) unsubscribePage(rw http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if w.Hub == nil || !w.Hub.ValidUnsubscribe(token) {
		w.unsubscribeUnknown(rw, r)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	UnsubscribePage(token, false).Render(r.Context(), rw)
}

// unsubscribe turns the person's email off: from the button, or from a mail program's one-click unsubscribe. It needs
// no sign-in (the link is the proof) and says the same whether or not they had email on.
func (w *Server) unsubscribe(rw http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if w.Hub == nil || !w.Hub.Unsubscribe(r.Context(), token) {
		w.unsubscribeUnknown(rw, r)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	UnsubscribePage(token, true).Render(r.Context(), rw)
}

func (w *Server) unsubscribeUnknown(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(http.StatusNotFound)
	Notice(t(r.Context(), "This link is not valid"), t(r.Context(), "It may be damaged or not from this app. To stop the emails, turn them off on your page."), true).Render(r.Context(), rw)
}
