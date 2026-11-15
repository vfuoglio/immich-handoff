package web

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
)

// RuleView is one rule on a service's row.
type RuleView struct {
	ID, Sentence, Mode string
	Always             int
	Paused             bool
	Last, Problem      string
	Phrase             bool
}

func (w *Server) ruleViews(ctx context.Context, me *member, service string) []RuleView {
	if w.Hub == nil {
		return nil
	}
	rules, err := w.Hub.RulesFor(ctx, me.user.ID, service)
	if err != nil {
		return nil
	}
	l := loc(ctx)
	out := make([]RuleView, 0, len(rules))
	for _, r := range rules {
		v := RuleView{ID: r.ID, Sentence: hub.RuleSentence(r.Cond), Mode: r.Mode, Always: r.Always, Paused: r.Paused, Problem: r.Problem, Phrase: r.Cond.Phrase != ""}
		if !r.LastLook.IsZero() {
			v.Last = l.Stamp(r.LastLook.Local())
		}
		out = append(out, v)
	}
	return out
}

// RuleChoicesView is what the form offers.
type RuleChoicesView struct {
	Service string
	People  []PersonOption
	Albums  []AlbumOption
	Country []string
	City    []string
	Picked  string // the chosen country, when the cities are listed
}

// PersonOption is a named person in the form.
type PersonOption struct{ ID, Name string }

func (w *Server) ruleForm(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	if w.Hub == nil {
		http.NotFound(rw, r)
		return
	}
	c, err := w.Hub.RuleChoicesFor(r.Context(), me.sess, "")
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	v := RuleChoicesView{Service: r.URL.Query().Get("service"), Country: c.Countries}
	for _, p := range c.People {
		v.People = append(v.People, PersonOption{ID: p.ID.String(), Name: p.Name})
	}
	for _, a := range c.Albums {
		v.Albums = append(v.Albums, AlbumOption{ID: a.ID.String(), Label: albumLabel(r.Context(), a.Name, a.Count)})
	}
	rw.Header().Set("Cache-Control", "no-store")
	RuleForm(v).Render(r.Context(), rw)
}

// ruleCities lists the cities of a country, for the form's second menu.
func (w *Server) ruleCities(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	country := r.URL.Query().Get("country")
	var cities []string
	if country != "" {
		cities, _ = me.sess.Places(r.Context(), country)
	}
	rw.Header().Set("Cache-Control", "no-store")
	RuleCities(cities).Render(r.Context(), rw)
}

// rulePerson shows a person's face, as the person's own Immich has it, for the form's picker.
func (w *Server) rulePerson(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	data, ct, err := me.sess.PersonThumbnail(r.Context(), id)
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

func ids(r *http.Request, name string) []uuid.UUID {
	var out []uuid.UUID
	for _, v := range r.PostForm[name] {
		if id, err := uuid.Parse(v); err == nil {
			out = append(out, id)
		}
	}
	return out
}

func (w *Server) addRule(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "rule made", func(ctx context.Context, me *member, service string) error {
		r.ParseForm()
		p := hub.RuleParams{Service: service, People: ids(r, "person"), NotPeople: ids(r, "notperson"), After: r.PostFormValue("after"), Before: r.PostFormValue("before"),
			Country: r.PostFormValue("country"), City: r.PostFormValue("city"), NotAlbums: ids(r, "notalbum"), Phrase: r.PostFormValue("phrase")}
		if id, err := uuid.Parse(r.PostFormValue("album")); err == nil {
			p.Album = id
		}
		_, err := w.Hub.AddRule(ctx, me.sess, me.user, p)
		return err
	})
}

func (w *Server) pauseRule(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "rule paused", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.PauseRule(ctx, me.user.ID, r.PostFormValue("rule"), r.PostFormValue("paused") == "1")
	})
}

func (w *Server) deleteRule(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "rule ended", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.DeleteRule(ctx, me.user, r.PostFormValue("rule"))
	})
}
