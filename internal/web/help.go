package web

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/vfuoglio/immich-handoff/internal/docs"
)

// Help: the guides of docs/, read inside the app. The pages come from the bundle embedded by internal/docs (one source for the
// public site and for this), for people who are signed in; an administrator's pages are for administrators. A page lives at
// help/<feature>, so its address does not depend on how the files are laid out.

const (
	docsSiteURL = "https://vfuoglio.github.io/immich-handoff"
	repoURL     = "https://github.com/vfuoglio/immich-handoff"
)

// HelpEntry is a page in the lists of Help.
type HelpEntry struct{ Feature, Title, Tip string }

// HelpGroup is the pages of one goal.
type HelpGroup struct {
	Title   string
	Entries []HelpEntry
}

// HelpIndexView is the first page of Help.
type HelpIndexView struct {
	User   string
	Groups []HelpGroup
}

// HelpPageView is one guide.
type HelpPageView struct {
	User, Feature, Title, Tip, Body string
	Note                            string // what the person should know about this page: it is in English, or its translation may be out of date
}

var helpLinks = docs.Links{Base: "", Site: docsSiteURL, Repo: repoURL}

type helpKey struct{ lang, path string }

var helpCache sync.Map // helpKey -> rendered HTML: the pages never change while the app runs

// helpHTML is a page of the bundle rendered for Help, in the person's language when there is a translation.
func helpHTML(lang, path string) (string, error) {
	k := helpKey{lang, path}
	if v, ok := helpCache.Load(k); ok {
		return v.(string), nil
	}
	used := docs.Used(lang, path)
	body, err := docs.Source(used, path)
	if err != nil {
		return "", err
	}
	pagePath := path
	if used != "en" {
		pagePath = used + "/" + path
	}
	out, err := docs.RenderHelp(pagePath, body, helpLinks)
	if err != nil {
		return "", err
	}
	helpCache.Store(k, out)
	return out, nil
}

// helpView is a guide ready to show: its title and tip in the person's language, and a note when the page is not in it.
func helpView(ctx context.Context, user string, e docs.Entry, body string) HelpPageView {
	lang := string(loc(ctx).Lang)
	title, tip, _ := docs.Meta(lang, e.Feature)
	v := HelpPageView{User: user, Feature: e.Feature, Title: title, Tip: tip, Body: body}
	if lang != "en" {
		switch {
		case docs.Used(lang, e.Path) == "en":
			v.Note = t(ctx, "This guide is in English.")
		case docs.Stale(lang, e.Path):
			v.Note = t(ctx, "The English page has changed since this translation was made, so this page may be out of date.")
		}
	}
	return v
}

// helpMember is the signed-in person, or a note that says to sign in.
func (w *Server) helpMember(rw http.ResponseWriter, r *http.Request) (*member, context.Context) {
	me, code, _ := w.authenticate(r)
	if me == nil {
		rw.WriteHeader(code)
		Notice(t(r.Context(), "Sign in to Immich first"),
			t(r.Context(), "This page uses your Immich login. Sign in to Immich in this browser, then reload this page."), false).Render(r.Context(), rw)
		return nil, nil
	}
	rw.Header().Set("Cache-Control", "no-store")
	return me, context.WithValue(r.Context(), adminKey{}, me.user.IsAdmin)
}

// helpEntry is the page of a feature when the person may read it.
func helpEntry(feature string, admin bool) (docs.Entry, bool) {
	entries, _ := docs.Index()
	for _, e := range entries {
		if e.Feature == feature && (e.Audience == "user" || admin && e.Audience == "admin") {
			return e, true
		}
	}
	return docs.Entry{}, false
}

func goalTitle(ctx context.Context, goal string) string {
	switch goal {
	case "start":
		return t(ctx, "Start here")
	case "share":
		return t(ctx, "Share")
	case "collect":
		return t(ctx, "Collect")
	case "automate":
		return t(ctx, "Automate")
	case "show":
		return t(ctx, "Show and be told")
	}
	return t(ctx, "Run it and look things up")
}

func (w *Server) helpIndex(rw http.ResponseWriter, r *http.Request) {
	me, ctx := w.helpMember(rw, r)
	if me == nil {
		return
	}
	entries, _ := docs.Index()
	v := HelpIndexView{User: me.user.Name}
	for _, e := range entries {
		if e.Audience != "user" && !(me.user.IsAdmin && e.Audience == "admin") {
			continue
		}
		if n := len(v.Groups); n == 0 || v.Groups[n-1].Title != goalTitle(ctx, e.Goal) {
			v.Groups = append(v.Groups, HelpGroup{Title: goalTitle(ctx, e.Goal)})
		}
		g := &v.Groups[len(v.Groups)-1]
		title, tip, _ := docs.Meta(string(loc(ctx).Lang), e.Feature)
		g.Entries = append(g.Entries, HelpEntry{e.Feature, title, tip})
	}
	HelpIndex(v).Render(ctx, rw)
}

func (w *Server) helpPage(rw http.ResponseWriter, r *http.Request) {
	me, ctx := w.helpMember(rw, r)
	if me == nil {
		return
	}
	e, ok := helpEntry(r.PathValue("feature"), me.user.IsAdmin)
	if !ok {
		http.NotFound(rw, r)
		return
	}
	body, err := helpHTML(string(loc(ctx).Lang), e.Path)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	HelpPage(helpView(ctx, me.user.Name, e, body)).Render(ctx, rw)
}

// helpDrawer is what the "?" on a card opens in place: the guide, without the page around it.
func (w *Server) helpDrawer(rw http.ResponseWriter, r *http.Request) {
	me, ctx := w.helpMember(rw, r)
	if me == nil {
		return
	}
	e, ok := helpEntry(r.PathValue("feature"), me.user.IsAdmin)
	if !ok {
		http.NotFound(rw, r)
		return
	}
	body, err := helpHTML(string(loc(ctx).Lang), e.Path)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	HelpDrawer(helpView(ctx, "", e, body)).Render(ctx, rw)
}

// helpSearch answers the search box of Help and the command palette.
func (w *Server) helpSearch(rw http.ResponseWriter, r *http.Request) {
	me, _ := w.helpMember(rw, r)
	if me == nil {
		return
	}
	type hit struct {
		Feature string `json:"feature"`
		Title   string `json:"title"`
		Tip     string `json:"tip"`
		URL     string `json:"url"`
	}
	out := []hit{}
	for i, h := range docs.Search(r.URL.Query().Get("q"), me.user.IsAdmin) {
		if i == 8 {
			break
		}
		title, tip, _ := docs.Meta(string(loc(r.Context()).Lang), h.Feature)
		out = append(out, hit{h.Feature, title, tip, "help/" + h.Feature})
	}
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(out)
}

// helpImage serves a slimmed picture of the guides from the same address as the page, so the page's rules about pictures hold.
func (w *Server) helpImage(rw http.ResponseWriter, r *http.Request) {
	if me, _, _ := w.authenticate(r); me == nil {
		http.Error(rw, "sign in to Immich first", http.StatusUnauthorized)
		return
	}
	b, typ, ok := docs.Image(r.PathValue("name"))
	if !ok {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Content-Type", typ)
	rw.Header().Set("Cache-Control", "private, max-age=3600")
	rw.Write(b)
}
