package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

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

// --- notifications on devices ---

func (w *Server) pushView(ctx context.Context, me *member) PushView {
	if w.Hub == nil {
		return PushView{}
	}
	c := w.Hub.PushCard(ctx, uuid.UUID(me.user.ID))
	v := PushView{Available: c.Available, Key: c.Key}
	l := loc(ctx)
	for _, d := range c.Devices {
		dv := PushDeviceView{ID: d.ID, Label: d.Label, Mode: d.Mode, Added: l.Stamp(d.Created.Local())}
		if !d.LastOK.IsZero() {
			dv.LastOK = l.Stamp(d.LastOK.Local())
		}
		v.Devices = append(v.Devices, dv)
	}
	return v
}

// pushKey gives a signed-in browser the public key it subscribes with. It is not a secret (the push services see it
// too), but only people who can use it are told.
func (w *Server) pushKey(rw http.ResponseWriter, r *http.Request) {
	if me, code, msg := w.authenticate(r); me == nil {
		http.Error(rw, msg, code)
		return
	}
	key := ""
	if w.Hub != nil {
		key = w.Hub.PushKey()
	}
	if key == "" {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(rw, key)
}

// pushError answers a refused change the way the other handlers do: the reason as text, 409 for a refusal.
func (w *Server) pushError(rw http.ResponseWriter, r *http.Request, what string, err error) {
	ctx := r.Context()
	status := http.StatusInternalServerError
	if hub.IsUserError(err) {
		status = http.StatusConflict
	}
	w.Log.Warn("changing a person's notification devices failed", "action", what, "err", err)
	rw.Header().Set("HX-Reswap", "innerHTML")
	rw.WriteHeader(status)
	reason := err.Error()
	if !hub.IsUserError(err) {
		reason = "something went wrong"
	}
	fmt.Fprint(rw, t(ctx, "Could not do that: {reason}", "reason", t(ctx, reason)))
}

// pushSubscribe stores the subscription a browser just made. The body is JSON, as the browser writes it.
func (w *Server) pushSubscribe(rw http.ResponseWriter, r *http.Request) {
	me := w.guarded(rw, r)
	if me == nil {
		return
	}
	if w.Hub == nil {
		http.NotFound(rw, r)
		return
	}
	var in struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 16<<10)).Decode(&in); err != nil {
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}
	_, err := w.Hub.PushSubscribe(r.Context(), uuid.UUID(me.user.ID), hub.PushDeviceInput{
		Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth, Mode: in.Mode,
		Lang: string(loc(r.Context()).Lang), UserAgent: r.Header.Get("User-Agent")})
	if err != nil {
		w.pushError(rw, r, "subscribe", err)
		return
	}
	rw.WriteHeader(http.StatusNoContent)
}

func (w *Server) pushRemove(rw http.ResponseWriter, r *http.Request) {
	w.pushChange(rw, r, "remove", func(ctx context.Context, me *member, id string) error {
		return w.Hub.PushRemove(ctx, uuid.UUID(me.user.ID), id)
	})
}

func (w *Server) pushMode(rw http.ResponseWriter, r *http.Request) {
	w.pushChange(rw, r, "mode", func(ctx context.Context, me *member, id string) error {
		return w.Hub.PushSetMode(ctx, uuid.UUID(me.user.ID), id, r.PostFormValue("mode"))
	})
}

func (w *Server) pushTest(rw http.ResponseWriter, r *http.Request) {
	w.pushChange(rw, r, "test", func(ctx context.Context, me *member, id string) error {
		return w.Hub.PushTest(ctx, me.user, id)
	})
}

func (w *Server) pushChange(rw http.ResponseWriter, r *http.Request, what string, act func(context.Context, *member, string) error) {
	me := w.guarded(rw, r)
	if me == nil {
		return
	}
	if w.Hub == nil {
		http.NotFound(rw, r)
		return
	}
	if err := act(r.Context(), me, r.PostFormValue("id")); err != nil {
		w.pushError(rw, r, what, err)
		return
	}
	rw.WriteHeader(http.StatusNoContent)
}

// --- one-tap action links ---

// actionSentence says what a link will do, in the reader's words.
func actionSentence(ctx context.Context, info hub.ActionInfo) (question, button string) {
	switch info.Kind {
	case hub.ActionApprove:
		return t(ctx, "Approve {name} to send photos to {service}?", "name", info.Name, "service", info.Service), t(ctx, "Approve")
	case hub.ActionDecline:
		return t(ctx, "Decline {name}'s request to send photos to {service}?", "name", info.Name, "service", info.Service), t(ctx, "Decline")
	case hub.ActionResumeService:
		return t(ctx, "Resume the service {service}?", "service", info.Service), t(ctx, "Resume service")
	}
	return t(ctx, "Resume your sending to {service}?", "service", info.Service), t(ctx, "Resume sending")
}

func (w *Server) actionNoStore(rw http.ResponseWriter) {
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
}

// actionPage is where a link in a message leads. It does nothing: it says what the link would do and asks. A program
// that follows links (a mail scanner, a preview) must not be able to act. A link that is unknown, used, expired or about
// something already done gets one and the same answer.
func (w *Server) actionPage(rw http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if w.Hub == nil {
		w.actionInvalid(rw, r)
		return
	}
	info, ok := w.Hub.ActionPage(r.Context(), token)
	if !ok {
		w.actionInvalid(rw, r)
		return
	}
	w.actionNoStore(rw)
	question, button := actionSentence(r.Context(), info)
	ActionPage(ActionPageView{Token: token, Question: question, Button: button}).Render(r.Context(), rw)
}

// actionRun does what the link says, once. The link is the proof, so no sign-in and no cross-site header are asked: a
// page on another site cannot know the token.
func (w *Server) actionRun(rw http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if w.Hub == nil {
		w.actionInvalid(rw, r)
		return
	}
	info, err := w.Hub.RunAction(r.Context(), token)
	if errors.Is(err, hub.ErrActionInvalid) {
		w.actionInvalid(rw, r)
		return
	}
	w.actionNoStore(rw)
	if err != nil {
		reason := "something went wrong"
		status := http.StatusInternalServerError
		if hub.IsUserError(err) {
			reason, status = err.Error(), http.StatusConflict
		} else {
			w.Log.Warn("an action link failed", "kind", info.Kind, "err", err)
		}
		rw.WriteHeader(status)
		ActionPage(ActionPageView{Failed: t(r.Context(), "Could not do that: {reason}", "reason", t(r.Context(), reason))}).Render(r.Context(), rw)
		return
	}
	ActionPage(ActionPageView{Done: true}).Render(r.Context(), rw)
}

func (w *Server) actionInvalid(rw http.ResponseWriter, r *http.Request) {
	w.actionNoStore(rw)
	rw.WriteHeader(http.StatusNotFound)
	Notice(t(r.Context(), "This link is no longer valid"), t(r.Context(), "It was already used, has expired, or was not made by this app. Do it from your page instead."), true).Render(r.Context(), rw)
}

// --- the weekly digest of a service ---

// digestWhy says in words why a photo was left out of a digest.
func digestWhy(ctx context.Context, l hub.DigestLeft) string {
	switch l.Reason {
	case hub.VerdictDark:
		return t(ctx, "too dark")
	case hub.VerdictBright:
		return t(ctx, "too bright")
	case hub.VerdictBlurry:
		return t(ctx, "blurred")
	case hub.VerdictDuplicate:
		return t(ctx, "the same moment as a better shot")
	case hub.VerdictMore:
		return t(ctx, "the week had more good ones than fit")
	}
	return l.Reason
}

// digestView is a service's digest for its owner's card. The link is the address of Immich's shared page when the app
// knows where Immich is (PUBLIC_IMMICH_URL); otherwise only its key is shown.
func (w *Server) digestView(ctx context.Context, service string) DigestView {
	info := w.Hub.DigestFor(ctx, service)
	l := loc(ctx)
	v := DigestView{On: info.On, Running: info.Running, Album: info.Album, Key: info.LinkKey, Photos: info.Photos, People: info.People, Left: info.Left, Note: info.Note}
	if !info.Made.IsZero() {
		v.Made = l.Stamp(info.Made.Local())
	}
	if !info.Ends.IsZero() {
		v.Ends = l.Stamp(info.Ends.Local())
	}
	if !info.Next.IsZero() {
		v.Next = l.Stamp(info.Next.Local())
	}
	if base := strings.TrimRight(w.PublicImmichURL, "/"); base != "" && info.LinkKey != "" {
		v.URL = base + "/share/" + info.LinkKey
	}
	for _, left := range info.LeftList {
		why := digestWhy(ctx, left)
		if left.Detail != "" && left.Reason != hub.VerdictMore {
			why += " (" + left.Detail + ")"
		}
		v.LeftList = append(v.LeftList, DigestLeftView{Name: left.Name, Why: why})
	}
	return v
}

// setDigest turns a service's weekly digest on or off, or makes one now. Only the service's owner may.
func (w *Server) setDigest(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "digest", func(ctx context.Context, me *member, service string) error {
		switch r.PostFormValue("do") {
		case "on":
			return w.Hub.EnableDigest(ctx, me.sess, me.user, service)
		case "off":
			return w.Hub.DisableDigest(ctx, me.sess, me.user, service)
		case "now":
			return w.Hub.MakeDigestNow(ctx, me.user, service)
		}
		return errors.New("unknown action")
	})
}

// setPool changes when an event pool ends, or ends it now. Only the pool's owner may.
func (w *Server) setPool(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "pool", func(ctx context.Context, me *member, service string) error {
		switch r.PostFormValue("do") {
		case "end":
			return w.Hub.EndPoolNow(ctx, me.user, service)
		case "set":
			days, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("days")))
			return w.Hub.SetPoolEnd(ctx, me.user, service, days)
		}
		return errors.New("unknown action")
	})
}
