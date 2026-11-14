package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/hub"
)

// The endpoints a phone sends photos to (docs/using.md, "Send photos from a phone"). They are the page's own: the person's
// Immich cookie and the cross-site header, like every change on the page, and nothing else is let in. A file is started,
// sent in pieces, and finished; the pieces and the checks are the hub's (internal/hub/phoneup.go).

// phoneAuth is guarded without the limit on changes a minute: a video is hundreds of pieces. How much may wait is what
// limits a person here.
func (w *Server) phoneAuth(rw http.ResponseWriter, r *http.Request) *member {
	if r.Header.Get("X-Requested-With") != "immich-handoff" {
		http.Error(rw, "missing X-Requested-With", http.StatusBadRequest)
		return nil
	}
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return nil
	}
	if w.Hub == nil || !w.Hub.PhoneOn() {
		http.Error(rw, t(r.Context(), hub.ErrPhoneOff.Error()), http.StatusNotFound)
		return nil
	}
	return me
}

func (w *Server) phoneJSON(rw http.ResponseWriter, status int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(status)
	json.NewEncoder(rw).Encode(v)
}

// phoneError answers a refusal. A piece that does not follow says where the server stands (JSON, status 409), so the phone
// carries on from there; other refusals are text (422); anything else is a fault (500).
func (w *Server) phoneError(rw http.ResponseWriter, r *http.Request, what string, err error) {
	var off *hub.PhoneOffsetError
	if errors.As(err, &off) {
		w.phoneJSON(rw, http.StatusConflict, map[string]any{"offset": off.At})
		return
	}
	if errors.Is(err, hub.ErrPhoneChecksum) { // the server threw the pieces away: the phone starts the file again
		w.phoneJSON(rw, http.StatusConflict, map[string]any{"offset": 0})
		return
	}
	if errors.Is(err, hub.ErrPhoneNoUpload) {
		http.Error(rw, t(r.Context(), err.Error()), http.StatusNotFound)
		return
	}
	if hub.IsUserError(err) || errors.Is(err, errPhoneService) {
		http.Error(rw, t(r.Context(), err.Error()), http.StatusUnprocessableEntity)
		return
	}
	w.Log.Warn("phone upload failed", "step", what, "err", err)
	http.Error(rw, t(r.Context(), "Could not do that: {reason}", "reason", err.Error()), http.StatusInternalServerError)
}

var errPhoneService = errors.New("you do not send to that service any more; send to your library only, or pick another")

// phoneService is the tag a service the person sends to puts on photos ("" for the library only).
func (w *Server) phoneService(me *member, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	for _, svc := range w.S.Current() {
		if svc.Label() == name && me.account(svc, "sender") != nil {
			return svc.Tag, nil
		}
	}
	return "", errPhoneService
}

func (w *Server) phoneStart(rw http.ResponseWriter, r *http.Request) {
	me := w.phoneAuth(rw, r)
	if me == nil {
		return
	}
	var in struct {
		Name    string `json:"name"`
		Size    int64  `json:"size"`
		Service string `json:"service"`
		Taken   int64  `json:"taken"` // when the photo was taken, in milliseconds since 1970
		Owner   string `json:"owner"` // who chose the file, if the phone says: a file waiting in a browser is only ever sent as that person
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}
	if in.Owner != "" && in.Owner != me.user.ID.String() {
		http.Error(rw, t(r.Context(), "These files were chosen by someone else; sign in as that person to send them."), http.StatusForbidden)
		return
	}
	if _, err := w.phoneService(me, in.Service); err != nil {
		w.phoneError(rw, r, "start", err)
		return
	}
	var at time.Time
	if in.Taken > 0 {
		at = time.UnixMilli(in.Taken)
	}
	u, err := w.Hub.PhoneStart(me.user.ID, in.Name, in.Size, in.Service, at)
	if err != nil {
		w.phoneError(rw, r, "start", err)
		return
	}
	w.phoneJSON(rw, http.StatusCreated, map[string]any{"id": u.ID, "chunk": hub.PhoneChunk, "offset": 0})
}

func (w *Server) phoneStatus(rw http.ResponseWriter, r *http.Request) {
	me := w.phoneAuth(rw, r)
	if me == nil {
		return
	}
	u, have, err := w.Hub.PhoneStatus(me.user.ID, r.PathValue("id"))
	if err != nil {
		w.phoneError(rw, r, "status", err)
		return
	}
	w.phoneJSON(rw, http.StatusOK, map[string]any{"offset": have, "size": u.Size, "done": u.Done != ""})
}

func (w *Server) phoneChunk(rw http.ResponseWriter, r *http.Request) {
	me := w.phoneAuth(rw, r)
	if me == nil {
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		http.Error(rw, "bad offset", http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, hub.PhoneChunk*2+1)
	got, err := w.Hub.PhoneChunk(me.user.ID, r.PathValue("id"), offset, r.Body)
	if err != nil {
		var off *hub.PhoneOffsetError
		if !errors.As(err, &off) && !hub.IsUserError(err) {
			// a connection that went away in the middle of a piece: what arrived is kept; the phone asks where it stands
			w.Log.Debug("a piece was cut off", "err", err)
		}
		w.phoneError(rw, r, "chunk", err)
		return
	}
	w.phoneJSON(rw, http.StatusOK, map[string]any{"offset": got})
}

func (w *Server) phoneDone(rw http.ResponseWriter, r *http.Request) {
	me := w.phoneAuth(rw, r)
	if me == nil {
		return
	}
	var in struct {
		Sum string `json:"sum"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	id := r.PathValue("id")
	u, _, err := w.Hub.PhoneStatus(me.user.ID, id)
	if err != nil {
		w.phoneError(rw, r, "done", err)
		return
	}
	tag, err := w.phoneService(me, u.Service)
	if err != nil {
		w.phoneError(rw, r, "done", err)
		return
	}
	photo, dup, err := w.Hub.PhoneFinish(r.Context(), me.sess, me.user.ID, id, tag, in.Sum)
	if err != nil {
		w.phoneError(rw, r, "done", err)
		return
	}
	if tag != "" {
		w.phoneWake(me.user.Email)
	}
	w.phoneJSON(rw, http.StatusOK, map[string]any{"photo": photo, "duplicate": dup})
}

func (w *Server) phoneCancel(rw http.ResponseWriter, r *http.Request) {
	me := w.phoneAuth(rw, r)
	if me == nil {
		return
	}
	if err := w.Hub.PhoneCancel(me.user.ID, r.PathValue("id")); err != nil {
		w.phoneError(rw, r, "cancel", err)
		return
	}
	rw.WriteHeader(http.StatusNoContent)
}

// phoneWaker starts one hand-over pass a few seconds after the last photo arrived, so a batch of fifty is one pass.
type phoneWaker struct {
	mu    sync.Mutex
	timer *time.Timer
}

func (w *Server) phoneWake(who string) {
	w.waker.mu.Lock()
	defer w.waker.mu.Unlock()
	if w.waker.timer != nil {
		w.waker.timer.Reset(4 * time.Second)
		return
	}
	w.waker.timer = time.AfterFunc(4*time.Second, func() {
		w.waker.mu.Lock()
		w.waker.timer = nil
		w.waker.mu.Unlock()
		w.S.Run(w.BackgroundCtx, "phone: "+who, false)
	})
}

// sendPage is "Send photos". Anyone signed in to Immich may open it: with no service to send to, photos go to their own
// library only.
func (w *Server) sendPage(rw http.ResponseWriter, r *http.Request) {
	me, code, _ := w.authenticate(r)
	if me == nil {
		rw.WriteHeader(code)
		Notice(t(r.Context(), "Sign in to Immich first"),
			t(r.Context(), "This page uses your Immich login. Sign in to Immich in this browser, then reload this page."), false).Render(r.Context(), rw)
		return
	}
	d := SendPageView{User: me.user.Name, ID: me.user.ID.String(), On: w.Hub != nil && w.Hub.PhoneOn()}
	if d.On {
		file, waiting, keep := w.Hub.PhoneLimits()
		d.FileMB, d.WaitMB, d.KeepHours = int(file>>20), int(waiting>>20), int(keep/time.Hour)
	}
	for _, s := range w.servicesFor(r.Context(), me) {
		if s.Role == "sender" {
			d.Targets = append(d.Targets, SendTarget{Name: s.Name, Hold: s.Hold})
		}
	}
	rw.Header().Set("Cache-Control", "no-store")
	ctx := context.WithValue(r.Context(), adminKey{}, me.user.IsAdmin)
	SendPage(d).Render(ctx, rw)
}

// shareFallback is where the phone's Share menu lands when the service worker did not take the share (it is not running,
// or this is not the installed app): there is nothing to receive the files, so the person is sent to the page to choose
// them there.
func (w *Server) shareFallback(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Location", "../send?share=none") // relative, so it also works behind a path prefix
	rw.WriteHeader(http.StatusSeeOther)
}
