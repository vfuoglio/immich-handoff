package web

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
)

// memoriesView is the person's "On this day" card.
func (w *Server) memoriesView(ctx context.Context, me *member) MemoriesView {
	if w.Hub == nil {
		return MemoriesView{}
	}
	c := w.Hub.MemoriesCard(ctx, me.user.ID)
	v := MemoriesView{Available: c.Available, Push: c.Push, Email: c.Email, On: c.On, At: "08:00", ByPush: true, Pictures: true, Address: me.user.Email}
	if c.On {
		s := c.Settings
		v.At, v.TZ, v.ByPush, v.ByEmail, v.Pictures, v.Broken = s.At, s.TZ, s.Push, s.Email, s.Pictures, s.Broken
	}
	v.Devices = w.Hub.PushDevices(ctx, uuid.UUID(me.user.ID))
	return v
}

// myMemories turns the person's "On this day" messages on, changes them or turns them off.
func (w *Server) myMemories(rw http.ResponseWriter, r *http.Request) {
	me := w.guarded(rw, r)
	if me == nil {
		return
	}
	ctx := r.Context()
	if w.Hub == nil {
		http.Error(rw, t(ctx, "Joining from this page is not switched on."), http.StatusNotFound)
		return
	}
	var err error
	switch r.PostFormValue("action") {
	case "on":
		err = w.Hub.SetMemories(ctx, me.sess, me.user, hub.MemoryInput{
			At: strings.TrimSpace(r.PostFormValue("at")), TZ: strings.TrimSpace(r.PostFormValue("tz")), Lang: string(loc(ctx).Lang),
			Push: r.PostFormValue("push") == "1", Email: r.PostFormValue("email") == "1", Pictures: r.PostFormValue("pictures") == "1"})
	case "off":
		err = w.Hub.StopMemories(ctx, me.sess, me.user)
	default:
		http.Error(rw, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		status := http.StatusInternalServerError
		if hub.IsUserError(err) {
			status = http.StatusConflict
		}
		w.Log.Warn("changing a person's On this day settings failed", "action", r.PostFormValue("action"), "err", err)
		rw.Header().Set("HX-Reswap", "innerHTML")
		rw.WriteHeader(status)
		fmt.Fprint(rw, t(ctx, "Could not do that: {reason}", "reason", t(ctx, err.Error())))
		return
	}
	rw.Header().Set("HX-Refresh", "true")
	rw.WriteHeader(http.StatusNoContent)
}

// memoriesPage is where "Send to someone" in a message leads: the photos of the day's memories, with the person's own session.
func (w *Server) memoriesPage(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	if w.Hub == nil {
		http.NotFound(rw, r)
		return
	}
	day, mems, err := w.Hub.MemoriesOfDay(r.Context(), me.sess, r.PathValue("day"))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	ctx := r.Context()
	d := MemoriesPageView{User: me.user.Name, Day: day.Format(hub.MemoryDayLayout)}
	for _, m := range mems {
		g := MemoryGroupView{Label: n(ctx, "{n} year ago", "{n} years ago", day.Year()-m.Year)}
		for _, p := range m.Photos {
			g.Photos = append(g.Photos, p.String())
		}
		d.Groups = append(d.Groups, g)
	}
	for _, s := range w.buttonServices(ctx, me) {
		d.Services = append(d.Services, s.Name)
	}
	sort.Strings(d.Services)
	rw.Header().Set("Cache-Control", "no-store")
	MemoriesPage(d).Render(ctx, rw)
}

// memoriesPhoto shows a photo of the day's memories, as the person's own Immich has it, and no other photo.
func (w *Server) memoriesPhoto(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	if w.Hub == nil {
		http.NotFound(rw, r)
		return
	}
	id, err := w.Hub.MemoryPhoto(r.Context(), me.sess, r.PathValue("day"), r.PathValue("asset"))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	data, ct, err := me.sess.Thumbnail(r.Context(), id, r.URL.Query().Get("size") == "large")
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	if !strings.HasPrefix(ct, "image/") {
		ct = "image/jpeg"
	}
	rw.Header().Set("Content-Type", ct)
	rw.Header().Set("Cache-Control", "private, max-age=3600")
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.Write(data)
}

// memoriesSend hands the ticked photos over to a service the person sends to, as the button in Immich does.
func (w *Server) memoriesSend(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "memories handed over", func(ctx context.Context, me *member, service string) error {
		r.ParseForm()
		_, err := w.Hub.HandOver(ctx, me.sess, me.user, service, r.PostForm["photo"])
		return err
	})
}
