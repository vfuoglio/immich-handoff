package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Stories (#272). The public side needs no sign-in (the long link is its only protection), is never kept, sends no referrer and shows
// only what the story's switches allow. The owner's side is a card in Settings and an editor page.

func (w *Server) storyOn() bool { return w.Hub != nil && w.Hub.SelfService() && w.Hub.Store != nil }

// storyFor finds the story a link opens. Unknown, being made, ended and past its date all look the same.
func (w *Server) storyFor(rw http.ResponseWriter, r *http.Request) (store.Story, bool) {
	if !w.storyOn() {
		http.NotFound(rw, r)
		return store.Story{}, false
	}
	s, err := w.Hub.StoryFor(r.Context(), r.PathValue("token"))
	if err != nil {
		http.NotFound(rw, r)
		return s, false
	}
	return s, true
}

func (w *Server) storyPage(rw http.ResponseWriter, r *http.Request) {
	s, ok := w.storyFor(rw, r)
	if !ok {
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	StoryPage(StoryPageView{Title: s.Title, Token: s.Token, Preview: s.Preview, AppURL: strings.TrimRight(w.Hub.AppURL, "/")}).Render(r.Context(), rw)
}

func (w *Server) storyData(rw http.ResponseWriter, r *http.Request) {
	s, ok := w.storyFor(rw, r)
	if !ok {
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(rw).Encode(hub.StoryDocOf(s))
}

func (w *Server) storyImage(rw http.ResponseWriter, r *http.Request, data []byte, kind string, err error) {
	if err != nil {
		if errors.Is(err, hub.ErrDropGone) {
			http.NotFound(rw, r)
		} else {
			http.Error(rw, "try again", http.StatusBadGateway)
		}
		return
	}
	rw.Header().Set("Content-Type", kind)
	rw.Header().Set("Cache-Control", "private, max-age=3600")
	rw.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.Write(data)
}

func (w *Server) storyPhoto(rw http.ResponseWriter, r *http.Request) {
	if !w.storyOn() {
		http.NotFound(rw, r)
		return
	}
	data, kind, err := w.Hub.StoryImage(r.Context(), r.PathValue("token"), r.PathValue("id"))
	w.storyImage(rw, r, data, kind, err)
}

func (w *Server) storyCover(rw http.ResponseWriter, r *http.Request) {
	if !w.storyOn() {
		http.NotFound(rw, r)
		return
	}
	data, kind, err := w.Hub.StoryCover(r.Context(), r.PathValue("token"))
	w.storyImage(rw, r, data, kind, err)
}

// --- the owner's side ---

func storySourceWords(ctx context.Context, s store.Story) string {
	switch s.Kind {
	case store.StoryAlbum:
		return t(ctx, "From the album {name}", "name", s.SourceName)
	case store.StoryPerson:
		return t(ctx, "From the photos of {name}", "name", s.SourceName)
	case store.StoryBox:
		return t(ctx, "From the drop box {name}", "name", s.SourceName)
	}
	if s.From != "" && s.To != "" {
		return t(ctx, "From {from} to {to}", "from", s.From, "to", s.To)
	}
	return t(ctx, "From everything")
}

func (w *Server) storyItemView(ctx context.Context, s store.Story) StoryItemView {
	v := StoryItemView{ID: s.ID, Token: s.Token, Title: s.Title, State: s.State, Problem: s.Problem, Source: storySourceWords(ctx, s), Done: s.Done, Total: s.Total,
		Photos: len(s.Picks), Chapters: len(s.Chapters), Preview: s.Preview}
	if !s.EndsAt.IsZero() {
		v.Ends = loc(ctx).Stamp(s.EndsAt.Local())
	}
	return v
}

func (w *Server) storyViews(ctx context.Context, me *member) StoriesView {
	if !w.storyOn() {
		return StoriesView{}
	}
	v := StoriesView{Available: true}
	list, err := w.Hub.StoriesOf(ctx, me.user.ID)
	if err != nil {
		return v
	}
	for _, s := range list {
		v.Items = append(v.Items, w.storyItemView(ctx, s))
	}
	return v
}

func (w *Server) storyForm(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	if !w.storyOn() {
		http.NotFound(rw, r)
		return
	}
	ctx := r.Context()
	var v StoryFormView
	if own, err := me.sess.OwnedAlbums(ctx); err == nil {
		for _, a := range own {
			v.Albums = append(v.Albums, AlbumOption{ID: a.ID.String(), Label: albumLabel(ctx, a.Name, a.Count)})
		}
	}
	if shared, err := me.sess.AlbumsSharedWithMe(ctx); err == nil {
		for _, a := range shared {
			v.Albums = append(v.Albums, AlbumOption{ID: a.ID.String(), Label: albumLabel(ctx, a.Name, a.Count)})
		}
	}
	if people, err := me.sess.People(ctx); err == nil {
		for _, p := range people {
			v.People = append(v.People, PersonOption{ID: p.ID.String(), Name: p.Name})
		}
	}
	if boxes, err := w.Hub.DropBoxes(ctx, me.user.ID); err == nil {
		for _, b := range boxes {
			if b.Role == "owner" && b.Box.State != store.DropRevoked {
				v.Boxes = append(v.Boxes, AlbumOption{ID: b.Box.ID, Label: b.Box.Name})
			}
		}
	}
	rw.Header().Set("Cache-Control", "no-store")
	StoryForm(v).Render(ctx, rw)
}

func (w *Server) storyItem(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	if !w.storyOn() {
		http.NotFound(rw, r)
		return
	}
	s, err := w.Hub.StoryOfOwner(r.Context(), me.user.ID, r.URL.Query().Get("story"))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	StoryItem(w.storyItemView(r.Context(), s)).Render(r.Context(), rw)
}

func (w *Server) makeStory(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "story made", func(ctx context.Context, me *member, _ string) error {
		p := hub.StoryParams{Title: r.PostFormValue("title"), Kind: r.PostFormValue("kind"), From: r.PostFormValue("from"), To: r.PostFormValue("to")}
		p.EndsInDays, _ = strconv.Atoi(r.PostFormValue("ends"))
		switch p.Kind {
		case store.StoryAlbum:
			p.Source, _ = uuid.Parse(r.PostFormValue("album"))
		case store.StoryPerson:
			p.Source, _ = uuid.Parse(r.PostFormValue("person"))
		case store.StoryBox:
			p.Box = r.PostFormValue("box")
		}
		_, err := w.Hub.CreateStory(ctx, me.sess, me.user, p)
		return err
	})
}

func (w *Server) rebuildStory(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "story picked again", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.RebuildStory(ctx, me.sess, me.user, r.PostFormValue("story"))
	})
}

func (w *Server) endStory(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "story removed", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.EndStory(ctx, me.sess, me.user, r.PostFormValue("story"))
	})
}

func (w *Server) removeStoryPhoto(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "story photo removed", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.RemoveStoryPhoto(ctx, me.user, r.PostFormValue("story"), r.PostFormValue("asset"))
	})
}

func (w *Server) editStory(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "story edited", func(ctx context.Context, me *member, _ string) error {
		r.ParseForm()
		e := hub.StoryEdit{Title: r.PostFormValue("title"), ShowDates: r.PostFormValue("dates") == "1", ShowPlaces: r.PostFormValue("places") == "1",
			ShowPeople: r.PostFormValue("people") == "1", Preview: r.PostFormValue("preview") == "1", EndsInDays: -1, ChapterTitles: map[int]string{}}
		if v := r.PostFormValue("ends"); v != "keep" && v != "" {
			e.EndsInDays, _ = strconv.Atoi(v)
		}
		for k, v := range r.PostForm {
			if n, ok := strings.CutPrefix(k, "chapter"); ok && len(v) > 0 {
				if i, err := strconv.Atoi(n); err == nil {
					e.ChapterTitles[i] = v[0]
				}
			}
		}
		return w.Hub.EditStory(ctx, me.user, r.PostFormValue("story"), e)
	})
}

func (w *Server) storyYear(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "story of the year made", func(ctx context.Context, me *member, _ string) error {
		year, _ := strconv.Atoi(r.PostFormValue("year"))
		_, err := w.Hub.MakeStoryOfYear(ctx, me.sess, me.user, year)
		return err
	})
}

func (w *Server) storyYearDismiss(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "story of the year dismissed", func(ctx context.Context, me *member, _ string) error {
		year, _ := strconv.Atoi(r.PostFormValue("year"))
		return w.Hub.DismissStoryYear(ctx, me.user.ID, year)
	})
}

func datesWords(from, to string) string {
	switch {
	case from != "" && to != "" && from != to:
		return from + " – " + to
	case from != "":
		return from
	}
	return ""
}

func (w *Server) storyEditPage(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	if !w.storyOn() {
		http.NotFound(rw, r)
		return
	}
	ctx := r.Context()
	s, err := w.Hub.StoryOfOwner(ctx, me.user.ID, r.PathValue("id"))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	v := StoryEditView{User: me.user.Name, Story: w.storyItemView(ctx, s), ShowDates: s.ShowDates, ShowPlaces: s.ShowPlaces, ShowPeople: s.ShowPeople, Preview: s.Preview,
		CanPreview: w.Hub.AppURL != ""}
	if !s.EndsAt.IsZero() {
		v.Ends = " (" + loc(ctx).Stamp(s.EndsAt.Local()) + ")"
	}
	counts := map[int]int{}
	for _, p := range s.Picks {
		counts[p.Chapter]++
	}
	for i, c := range s.Chapters {
		v.Chapters = append(v.Chapters, StoryChapterView{N: i, Title: c.Title, Dates: firstNonEmpty(datesWords(c.From, c.To), t(ctx, "Chapter {n}", "n", i+1)), Photos: counts[i]})
	}
	for _, p := range s.Picks {
		words := strings.Join(nonEmpty(p.Date, p.Place), " · ")
		v.Photos = append(v.Photos, StoryPhotoView{Asset: p.Asset, Words: words})
	}
	rw.Header().Set("Cache-Control", "no-store")
	StoryEditPage(v).Render(ctx, rw)
}

func nonEmpty(l ...string) []string {
	var out []string
	for _, s := range l {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func firstNonEmpty(l ...string) string {
	for _, s := range l {
		if s != "" {
			return s
		}
	}
	return ""
}

func (w *Server) storyThumb(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	if !w.storyOn() {
		http.NotFound(rw, r)
		return
	}
	data, ct, err := w.Hub.StoryThumb(r.Context(), me.sess, me.user.ID, r.PathValue("id"), r.PathValue("asset"))
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
