package web

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
)

// circleViews are the person's circles and the albums waiting for their answer, for their page.
func (w *Server) circleViews(ctx context.Context, me *member) (CirclesView, []CircleAskView) {
	if w.Hub == nil || !w.Hub.SelfService() || w.Hub.Store == nil {
		return CirclesView{}, nil
	}
	v := CirclesView{Available: true}
	list, err := w.Hub.Store.CirclesOf(ctx, me.user.ID)
	if err != nil {
		return v, nil
	}
	l := loc(ctx)
	for _, c := range list {
		cv := CircleView{ID: c.ID, Name: c.Name, Role: c.Role, Prefix: c.Prefix, Mode: c.Mode, Always: c.Always, Paused: c.Paused, CanAuto: hub.CanAutoCircle(c), Problem: c.Problem}
		if !c.LastLook.IsZero() {
			cv.Last = l.Stamp(c.LastLook.Local())
		}
		members, _ := w.Hub.Store.CircleMembers(ctx, c.ID)
		for _, m := range members {
			cv.Members = append(cv.Members, CircleMemberView{ID: m.UserID.String(), Name: m.Name, Email: m.Email})
		}
		v.Items = append(v.Items, cv)
	}
	var asks []CircleAskView
	if sug, err := w.Hub.CircleSuggestions(ctx, me.user.ID); err == nil {
		for _, s := range sug {
			asks = append(asks, CircleAskView{ID: s.Circle.ID, Name: s.Circle.Name, Albums: s.Albums, People: strings.Join(s.People, ", "), Count: s.Pending})
		}
	}
	return v, asks
}

// circleFormView reads what the form offers: the accounts to pick from when the administrator allows it.
func (w *Server) circleFormView(ctx context.Context, me *member, circle string) (CircleFormView, error) {
	v := CircleFormView{Circle: circle, ByUsers: w.Hub.CircleMethod(ctx) == 2}
	if v.ByUsers {
		users, err := w.Hub.CircleUsers(ctx, me.sess, me.user.ID)
		if err != nil {
			return v, err
		}
		for _, u := range users {
			v.Users = append(v.Users, PersonOption{ID: u.ID.String(), Name: u.Name})
		}
	}
	return v, nil
}

func (w *Server) circleForm(rw http.ResponseWriter, r *http.Request) {
	w.circleFragment(rw, r, "", false)
}

func (w *Server) circleAddForm(rw http.ResponseWriter, r *http.Request) {
	w.circleFragment(rw, r, r.URL.Query().Get("circle"), true)
}

func (w *Server) circleFragment(rw http.ResponseWriter, r *http.Request, circle string, add bool) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	if w.Hub == nil || !w.Hub.SelfService() {
		http.NotFound(rw, r)
		return
	}
	v, err := w.circleFormView(r.Context(), me, circle)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	if add {
		CircleAddForm(v).Render(r.Context(), rw)
		return
	}
	CircleForm(v).Render(r.Context(), rw)
}

// emailsOf reads the addresses a form gave: separated by commas, spaces or new lines.
func emailsOf(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t' })
}

func (w *Server) makeCircle(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "circle made", func(ctx context.Context, me *member, _ string) error {
		r.ParseForm()
		_, err := w.Hub.AddCircle(ctx, me.sess, me.user, hub.CircleParams{Name: r.PostFormValue("name"), Role: r.PostFormValue("role"), Prefix: r.PostFormValue("prefix"),
			Emails: emailsOf(r.PostFormValue("emails")), Users: ids(r, "user")})
		return err
	})
}

func (w *Server) addCircleMember(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "circle member added", func(ctx context.Context, me *member, _ string) error {
		r.ParseForm()
		id, _ := uuid.Parse(r.PostFormValue("user"))
		email := ""
		if e := emailsOf(r.PostFormValue("emails")); len(e) > 0 {
			email = e[0]
		}
		return w.Hub.AddCircleMemberTo(ctx, me.sess, me.user, r.PostFormValue("circle"), email, id)
	})
}

func (w *Server) removeCircleMember(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "circle member removed", func(ctx context.Context, me *member, _ string) error {
		id, err := uuid.Parse(r.PostFormValue("member"))
		if err != nil {
			return hub.ErrNoCircle
		}
		return w.Hub.RemoveCircleMemberFrom(ctx, me.user, r.PostFormValue("circle"), id)
	})
}

func (w *Server) answerCircle(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "circle answered", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.AnswerCircle(ctx, me.user, r.PostFormValue("circle"), r.PostFormValue("do"))
	})
}

func (w *Server) autoCircle(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "circle automatic", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.SetCircleAuto(ctx, me.user, r.PostFormValue("circle"), r.PostFormValue("on") == "1")
	})
}

func (w *Server) pauseCircle(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "circle paused", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.PauseCircle(ctx, me.user.ID, r.PostFormValue("circle"), r.PostFormValue("paused") == "1")
	})
}

func (w *Server) deleteCircle(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "circle ended", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.DeleteCircleWith(ctx, me.sess, me.user, r.PostFormValue("circle"))
	})
}

func (w *Server) catchUpCircle(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "circle caught up", func(ctx context.Context, me *member, _ string) error {
		_, err := w.Hub.CatchUpCircle(ctx, me.user, r.PostFormValue("circle"))
		return err
	})
}
