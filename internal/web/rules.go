package web

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// RuleView is one rule on a service's row.
type RuleView struct {
	ID, Sentence, Mode string
	Always             int
	Paused             bool
	Last, Problem      string
	Phrase             bool
	CanAuto            bool             // answered Always often enough, and may be let go on by itself
	WaitMin            int              // the rule's own wait before it offers a photo, in minutes (0: the app's usual wait)
	PetExamples        []PetExampleView // a pet rule that uses the pet helper: the animal found in each example
}

// PetExampleView is an example photo of a pet rule with the box round the animal the pet helper found in it, as percentages.
type PetExampleView struct {
	Asset      string
	X, Y, W, H int
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
		v := RuleView{ID: r.ID, Sentence: hub.RuleSentence(r.Cond), Mode: r.Mode, Always: r.Always, Paused: r.Paused, Problem: r.Problem, Phrase: r.Cond.NeverAuto(), CanAuto: hub.CanAuto(r), WaitMin: r.SettleMinutes}
		if !r.LastLook.IsZero() {
			v.Last = l.Stamp(r.LastLook.Local())
		}
		if r.Cond.Pet() && w.Hub.Store != nil {
			if prints, err := w.Hub.Store.PetPrints(ctx, r.ID); err == nil {
				pct := func(f float64) int { return int(f*100 + 0.5) }
				for _, p := range prints {
					v.PetExamples = append(v.PetExamples, PetExampleView{Asset: p.Asset, X: pct(p.Box[0]), Y: pct(p.Box[1]), W: pct(p.Box[2]), H: pct(p.Box[3])})
				}
			}
		}
		out = append(out, v)
	}
	return out
}

// FollowedView is an album shared with the person that a rule follows, for their page.
type FollowedView struct {
	ID, Album, Owner, Dest, Mode string
	Always                       int
	Paused, CanAuto              bool
	Last, Problem                string
}

// destWords says where a rule's copies go, in the person's language.
func destWords(ctx context.Context, dest, album string) string {
	switch dest {
	case "plain":
		return t(ctx, "In my library")
	case "album":
		return t(ctx, "In the album {album}", "album", album)
	}
	return t(ctx, "In my library, tagged with who shared it")
}

func (w *Server) followedViews(ctx context.Context, me *member) []FollowedView {
	if w.Hub == nil {
		return nil
	}
	rules, err := w.Hub.RulesFor(ctx, me.user.ID, "")
	if err != nil {
		return nil
	}
	l := loc(ctx)
	var out []FollowedView
	for _, r := range rules {
		if r.Kind != "library" || r.Cond.Album == nil {
			continue
		}
		v := FollowedView{ID: r.ID, Album: r.Cond.Album.Name, Owner: r.Cond.AlbumOwner, Dest: destWords(ctx, r.Dest, r.DestAlbum), Mode: r.Mode, Always: r.Always, Paused: r.Paused, CanAuto: hub.CanAuto(r), Problem: r.Problem}
		if !r.LastLook.IsZero() {
			v.Last = l.Stamp(r.LastLook.Local())
		}
		out = append(out, v)
	}
	return out
}

// FollowOption is an album shared with the person, in the form that follows one.
type FollowOption struct{ ID, Label string }

// followAlbums lists the albums other people shared with the person, for the form that follows one; it is asked for when the
// form is opened.
func (w *Server) followAlbums(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	if w.Hub == nil {
		http.NotFound(rw, r)
		return
	}
	albums, err := w.Hub.SharedAlbumsFor(r.Context(), me.sess)
	if err != nil {
		w.Log.Warn("could not list the albums shared with a person", "user", me.user.Email, "err", err)
		http.Error(rw, t(r.Context(), "Could not list the albums shared with you"), http.StatusBadGateway)
		return
	}
	opts := make([]FollowOption, 0, len(albums))
	for _, a := range albums {
		opts = append(opts, FollowOption{ID: a.ID.String(), Label: albumLabel(r.Context(), a.Name, a.Count) + " \u00b7 " + a.Owner})
	}
	rw.Header().Set("Cache-Control", "no-store")
	FollowForm(opts).Render(r.Context(), rw)
}

func (w *Server) followAlbum(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "album followed", func(ctx context.Context, me *member, _ string) error {
		id, err := uuid.Parse(r.PostFormValue("album_id"))
		if err != nil {
			return hub.ErrAlbumNotShared
		}
		album := strings.TrimSpace(r.PostFormValue("album"))
		if album == "" {
			album = r.PostFormValue("album_pick")
		}
		_, err = w.Hub.FollowAlbum(ctx, me.sess, me.user, hub.FollowParams{Album: id, History: r.PostFormValue("history"), Dest: hub.LibraryDest{Kind: r.PostFormValue("dest"), Album: album}})
		return err
	})
}

// SuggestionView is a rule with photos waiting for the person's answer.
type SuggestionView struct {
	RuleID, Service, Sentence string
	Stack                     bool     // the rule stacks photos: Service is empty; Count is the number of groups
	Signals                   string   // for such a rule: how many groups of each kind, in words
	Folder                    bool     // the rule makes albums from folders: Service is empty
	Smart                     bool     // the rule keeps an album of the person's up to date: Service is empty, Album is its name
	Place                     bool     // the rule gives photos places: Service is empty
	Albums                    []string // the albums the waiting photos would go in, for such a rule
	Library                   bool     // the rule follows an album shared with the person: Service is empty
	Import                    bool     // the rule follows a destination: Owner is its name, Album the folder
	Album, Owner              string   // for such a rule
	Count                     int
	Thumbs                    []NeedThumb
	Always                    int
	Phrase                    bool
}

func (w *Server) suggestionViews(ctx context.Context, me *member) []SuggestionView {
	if w.Hub == nil {
		return nil
	}
	list, err := w.Hub.Suggestions(ctx, me.user.ID)
	if err != nil {
		return nil
	}
	var out []SuggestionView
	for _, s := range list {
		v := SuggestionView{RuleID: s.Rule.ID, Service: s.Rule.Service, Sentence: hub.RuleSentence(s.Rule.Cond), Count: len(s.Photos), Always: s.Rule.Always, Phrase: s.Rule.Cond.NeverAuto()}
		if s.Rule.Pairing != "" { // the pool has a generated name: the card says who the photos are for
			v.Service = w.pairingOther(ctx, me.user.ID, s.Rule.Pairing, v.Service)
		}
		if s.Rule.Kind == "library" && s.Rule.Cond.Album != nil {
			v.Library, v.Album, v.Owner = true, s.Rule.Cond.Album.Name, s.Rule.Cond.AlbumOwner
		}
		if s.Rule.Kind == store.RuleKindImport {
			v.Import, v.Owner, v.Album = true, s.Rule.Cond.SourceName, s.Rule.Cond.Prefix
			for i, p := range s.Photos {
				if i == 8 {
					break
				}
				v.Thumbs = append(v.Thumbs, NeedThumb{URL: "rules/import-photo/" + s.Rule.ID + "/" + hub.EncodeImportRef(p.AssetID), Why: p.Why})
			}
			out = append(out, v)
			continue
		}
		if s.Rule.Kind == store.RuleKindAlbum {
			v.Smart, v.Album = true, s.Rule.DestAlbum
		}
		if s.Rule.Kind == store.RuleKindPlace {
			v.Place = true
		}
		if s.Rule.Kind == "folder" {
			v.Folder, v.Sentence, v.Album = true, s.Rule.Cond.Folder, s.Rule.Cond.Folder
			v.Albums = albumsOfPhotos(s.Photos)
		}
		if s.Rule.Kind == store.RuleKindStack { // a card of groups: one cover for each
			groups := hub.StackGroupsOf(s.Photos)
			v.Stack, v.Sentence, v.Count, v.Signals = true, stackWords(ctx, s.Rule.Cond), len(groups), stackSignalsOf(ctx, groups)
			for i, g := range groups {
				if i == 8 {
					break
				}
				v.Thumbs = append(v.Thumbs, NeedThumb{URL: "rules/photo/" + s.Rule.ID + "/" + g.Cover, Why: g.Signal})
			}
			out = append(out, v)
			continue
		}
		for i, p := range s.Photos {
			if i == 8 {
				break
			}
			v.Thumbs = append(v.Thumbs, NeedThumb{URL: "rules/photo/" + s.Rule.ID + "/" + p.AssetID, Why: p.Why})
		}
		out = append(out, v)
	}
	return out
}

// rulePhoto shows a photo a rule is suggesting, as the person's own Immich has it.
func (w *Server) rulePhoto(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	id, err := w.Hub.RulePhotoOf(r.Context(), me.user.ID, r.PathValue("rule"), r.PathValue("asset"))
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

func (w *Server) answerRule(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "rule answered", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.AnswerSuggestion(ctx, me.user, r.PostFormValue("rule"), r.PostFormValue("do"), nil)
	})
}

// RuleChoicesView is what the form offers.
type RuleChoicesView struct {
	Service string
	People  []PersonOption
	Albums  []AlbumOption
	Country []string
	City    []string
	Picked  string   // the chosen country, when the cities are listed
	Others  []string // the person's other services, to make the same rule for
	Makes   []string // camera makes the library holds photos from
	Tags    []PersonOption

	WaitMin int // the app's usual wait before a rule offers a photo, in minutes, for the form to say
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
	v := w.choicesView(r.Context(), c, r.URL.Query().Get("service"))
	if joined, err := w.Hub.Joined(r.Context(), me.user.ID); err == nil {
		for name, ok := range joined {
			if ok && name != v.Service {
				v.Others = append(v.Others, name)
			}
		}
		sort.Strings(v.Others)
	}
	rw.Header().Set("Cache-Control", "no-store")
	RuleForm(v).Render(r.Context(), rw)
}

// choicesView turns what the person can choose from into what a form offers.
func (w *Server) choicesView(ctx context.Context, c hub.RuleChoices, service string) RuleChoicesView {
	v := RuleChoicesView{Service: service, Country: c.Countries, Makes: c.Makes, WaitMin: w.waitMinutes()}
	for _, tg := range c.Tags {
		v.Tags = append(v.Tags, PersonOption{ID: tg.ID.String(), Name: tg.Value})
	}
	for _, p := range c.People {
		v.People = append(v.People, PersonOption{ID: p.ID.String(), Name: p.Name})
	}
	for _, a := range c.Albums {
		v.Albums = append(v.Albums, AlbumOption{ID: a.ID.String(), Label: albumLabel(ctx, a.Name, a.Count)})
	}
	return v
}

// ruleModels lists the models of a camera make, for the form's second camera menu.
func (w *Server) ruleModels(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	var models []string
	if make := r.URL.Query().Get("make"); make != "" {
		models, _ = me.sess.Cameras(r.Context(), make)
	}
	rw.Header().Set("Cache-Control", "no-store")
	RuleModels(models).Render(r.Context(), rw)
}

// albumForm is the form that makes a smart album.
func (w *Server) albumForm(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	if w.Hub == nil || !w.Hub.SelfService() {
		http.NotFound(rw, r)
		return
	}
	c, err := w.Hub.RuleChoicesFor(r.Context(), me.sess, "")
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	AlbumForm(w.choicesView(r.Context(), c, "")).Render(r.Context(), rw)
}

// makeSmartAlbum makes a smart album.
func (w *Server) makeSmartAlbum(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "smart album made", func(ctx context.Context, me *member, _ string) error {
		r.ParseForm()
		n, err := settleOf(r)
		if err != nil {
			return err
		}
		p := hub.AlbumParams{RuleParams: hub.RuleParams{People: ids(r, "person"), NotPeople: ids(r, "notperson"), After: r.PostFormValue("after"), Before: r.PostFormValue("before"),
			Country: r.PostFormValue("country"), City: r.PostFormValue("city"), NotAlbums: ids(r, "notalbum"), Phrase: r.PostFormValue("phrase"),
			Make: r.PostFormValue("make"), Model: r.PostFormValue("model"), Tags: ids(r, "tag"), SettleMinutes: n}}
		if id, err := uuid.Parse(r.PostFormValue("album")); err == nil {
			p.Album = id
		}
		if id, err := uuid.Parse(r.PostFormValue("target")); err == nil {
			p.Existing = id
		}
		p.New = r.PostFormValue("newname")
		p.RemoveStale = r.PostFormValue("stale") == "1"
		_, err = w.Hub.FollowAlbumRule(ctx, me.sess, me.user, p)
		return err
	})
}

// albumViews are the person's smart albums, for their page.
func (w *Server) albumViews(ctx context.Context, me *member) AlbumsView {
	if w.Hub == nil || !w.Hub.SelfService() {
		return AlbumsView{}
	}
	v := AlbumsView{Available: true}
	rules, err := w.Hub.RulesFor(ctx, me.user.ID, "")
	if err != nil {
		return v
	}
	l := loc(ctx)
	for _, r := range rules {
		if r.Kind != store.RuleKindAlbum {
			continue
		}
		av := AlbumRuleView{ID: r.ID, Album: r.DestAlbum, Words: hub.RuleSentence(r.Cond), Mode: r.Mode, Always: r.Always, Paused: r.Paused, CanAuto: hub.CanAuto(r), RemoveStale: r.Cond.RemoveStale, Problem: r.Problem}
		if !r.LastLook.IsZero() {
			av.Last = l.Stamp(r.LastLook.Local())
		}
		v.Rules = append(v.Rules, av)
	}
	return v
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
			Country: r.PostFormValue("country"), City: r.PostFormValue("city"), NotAlbums: ids(r, "notalbum"), Phrase: r.PostFormValue("phrase"), Also: r.PostForm["also"]}
		n, err := settleOf(r)
		if err != nil {
			return err
		}
		p.SettleMinutes = n
		if id, err := uuid.Parse(r.PostFormValue("album")); err == nil {
			p.Album = id
		}
		_, err = w.Hub.AddRule(ctx, me.sess, me.user, p)
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
		return w.Hub.DeleteRuleWith(ctx, me.sess, me.user, r.PostFormValue("rule"))
	})
}

// SuggestionReview is the page that shows everything a rule found, one photo at a time.
type SuggestionReview struct {
	User, RuleID, Service, Sentence string
	Photos                          []RuleReviewPhoto
	Phrase                          bool
	Library                         bool   // the rule follows an album shared with the person
	Import                          bool   // the rule follows a destination (Owner is its name)
	Album, Owner                    string // for such a rule
	Folder                          bool   // the rule makes albums from folders (Album is the folder)
	Smart                           bool   // the rule keeps an album up to date (Album is its name)
	Place                           bool   // the rule gives photos places
	Stack                           bool   // the rule stacks photos: the page shows Groups
	Groups                          []StackReviewGroup
}

// StackReviewGroup is a group of photos the rule would stack: how it was found, the photos with the cover first, and the id to tick it by.
type StackReviewGroup struct {
	Signal, Cover string
	Photos        []RuleReviewPhoto
}

// RuleReviewPhoto is one photo on the review page.
// RuleReviewPhoto is one photo on the review page; for a place, Near is the photo its place comes from and Map a link to the place.
type RuleReviewPhoto struct{ ID, URL, Why, Near, Map string }

func (w *Server) ruleReview(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	sg, err := w.Hub.SuggestionOf(r.Context(), me.user.ID, r.PathValue("id"))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	d := SuggestionReview{User: me.user.Name, RuleID: sg.Rule.ID, Service: sg.Rule.Service, Sentence: hub.RuleSentence(sg.Rule.Cond), Phrase: sg.Rule.Cond.NeverAuto()}
	if sg.Rule.Pairing != "" {
		d.Service = w.pairingOther(r.Context(), me.user.ID, sg.Rule.Pairing, d.Service)
	}
	if sg.Rule.Kind == "library" && sg.Rule.Cond.Album != nil {
		d.Library, d.Album, d.Owner = true, sg.Rule.Cond.Album.Name, sg.Rule.Cond.AlbumOwner
	}
	if sg.Rule.Kind == store.RuleKindImport {
		d.Import, d.Owner = true, sg.Rule.Cond.SourceName
	}
	if sg.Rule.Kind == store.RuleKindAlbum {
		d.Smart, d.Album = true, sg.Rule.DestAlbum
	}
	if sg.Rule.Kind == store.RuleKindPlace {
		d.Place = true
	}
	if sg.Rule.Kind == "folder" {
		d.Folder, d.Album = true, sg.Rule.Cond.Folder
	}
	if sg.Rule.Kind == store.RuleKindStack {
		d.Stack = true
		for _, g := range hub.StackGroupsOf(sg.Photos) {
			rg := StackReviewGroup{Signal: g.Signal, Cover: g.Cover}
			for _, id := range g.Members {
				rg.Photos = append(rg.Photos, RuleReviewPhoto{ID: id, URL: "rules/photo/" + sg.Rule.ID + "/" + id, Why: g.Signal})
			}
			d.Groups = append(d.Groups, rg)
		}
	}
	for _, p := range sg.Photos {
		if sg.Rule.Kind == store.RuleKindStack { // shown by group, below
			break
		}
		url := "rules/photo/" + sg.Rule.ID + "/" + p.AssetID
		if sg.Rule.Kind == store.RuleKindImport {
			url = "rules/import-photo/" + sg.Rule.ID + "/" + hub.EncodeImportRef(p.AssetID)
		}
		photo := RuleReviewPhoto{ID: p.AssetID, URL: url, Why: p.Why}
		if reason, ok := hub.ParsePlaceWhy(p.Why); sg.Rule.Kind == store.RuleKindPlace && ok {
			photo.Why = strings.TrimSpace(reason.Place + " · " + t(r.Context(), "same camera, {min} min away, a photo with a place {sides}", "min", reason.Minutes, "sides", placeSides(r.Context(), reason)))
			photo.Near, photo.Map = "rules/photo/"+sg.Rule.ID+"/"+reason.Near, hub.PlaceMapLink(reason.Lat, reason.Lon)
		}
		d.Photos = append(d.Photos, photo)
	}
	rw.Header().Set("Cache-Control", "no-store")
	SuggestionReviewPage(d).Render(r.Context(), rw)
}

// reviewRule settles a whole batch from the review page: the ticked photos are answered yes, the others are left out.
func (w *Server) reviewRule(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "rule reviewed", func(ctx context.Context, me *member, _ string) error {
		r.ParseForm()
		do := r.PostFormValue("do")
		only := []string{}
		for _, id := range r.PostForm["photo"] {
			only = append(only, id)
		}
		return w.Hub.AnswerSuggestion(ctx, me.user, r.PostFormValue("rule"), do, only)
	})
}

func (w *Server) autoRule(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "rule automatic", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.SetRuleAuto(ctx, me.user, r.PostFormValue("rule"), r.PostFormValue("on") == "1")
	})
}

func (w *Server) lookBackRule(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "rule looked back", func(ctx context.Context, me *member, _ string) error {
		_, err := w.Hub.LookBack(ctx, me.user, r.PostFormValue("rule"))
		return err
	})
}

// albumsOfPhotos lists, once each and in order, the albums a rule's waiting photos would go in (the reason each carries).
func albumsOfPhotos(photos []store.RulePhoto) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range photos {
		if p.Why != "" && !seen[p.Why] {
			seen[p.Why] = true
			out = append(out, p.Why)
		}
	}
	sort.Strings(out)
	return out
}

// folderViews are the person's rules that make albums from folders, for their page.
func (w *Server) folderViews(ctx context.Context, me *member) FoldersView {
	if w.Hub == nil || !w.Hub.SelfService() {
		return FoldersView{}
	}
	v := FoldersView{Available: true}
	rules, err := w.Hub.RulesFor(ctx, me.user.ID, "")
	if err != nil {
		return v
	}
	l := loc(ctx)
	for _, r := range rules {
		if r.Kind != "folder" {
			continue
		}
		fv := FolderRuleView{ID: r.ID, Folder: r.Cond.Folder, Words: folderWords(ctx, r.Cond), Mode: r.Mode, Always: r.Always, Paused: r.Paused, CanAuto: hub.CanAuto(r), Problem: r.Problem}
		if !r.LastLook.IsZero() {
			fv.Last = l.Stamp(r.LastLook.Local())
		}
		v.Rules = append(v.Rules, fv)
	}
	return v
}

// folderWords says how a folder rule reads its folders, in the person's language.
func folderWords(ctx context.Context, c store.RuleCond) string {
	var level string
	switch c.Level {
	case 0:
		level = t(ctx, "An album for the folder each photo is in")
	case 1:
		level = t(ctx, "An album for the first folder below it")
	case 2:
		level = t(ctx, "An album for the second folder below it")
	default:
		level = t(ctx, "An album for the third folder below it")
	}
	if c.Naming == hub.NameParent {
		return level + ", " + t(ctx, "named after the folder and the one above it")
	}
	return level + ", " + t(ctx, "named after the folder")
}

// folderCheck tells the form how many of the person's photos are under the folder it has so far, and the albums that would make.
func (w *Server) folderCheck(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	ctx := r.Context()
	if w.Hub == nil || !w.Hub.SelfService() {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	q := r.URL.Query()
	if strings.TrimSpace(q.Get("folder")) == "" {
		return
	}
	level, _ := strconv.Atoi(q.Get("level"))
	count, plan, err := w.Hub.CountFolderPhotos(ctx, me.sess, me.user, hub.FolderSpec{Prefix: q.Get("folder"), Depth: level, Naming: q.Get("naming")})
	switch {
	case err != nil && hub.IsUserError(err):
		FolderCheck(t(ctx, err.Error()), nil, 0).Render(ctx, rw)
	case err != nil:
		w.Log.Warn("could not look under a folder", "user", me.user.Email, "err", err)
		FolderCheck(t(ctx, "Could not look under that folder"), nil, 0).Render(ctx, rw)
	case count == 0:
		FolderCheck(t(ctx, hub.ErrNoFolderPh.Error()), nil, 0).Render(ctx, rw)
	default:
		var names []string
		for _, a := range plan.Albums {
			names = append(names, t(ctx, "{name} ({photos})", "name", a.Name, "photos", n(ctx, "{n} photo", "{n} photos", len(a.Photos))))
		}
		more := 0
		if len(names) > 12 {
			more, names = len(names)-12, names[:12]
		}
		FolderCheck(t(ctx, "Found {n} of your photos under that folder, making {albums} albums:", "n", count, "albums", len(plan.Albums)), names, more).Render(ctx, rw)
	}
}

// followFolder makes a rule that makes albums from a folder.
func (w *Server) followFolder(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "folder rule made", func(ctx context.Context, me *member, _ string) error {
		level, _ := strconv.Atoi(r.PostFormValue("level"))
		_, err := w.Hub.FollowFolder(ctx, me.sess, me.user, hub.FolderSpec{Prefix: r.PostFormValue("folder"), Depth: level, Naming: r.PostFormValue("naming")})
		return err
	})
}

// settleOf reads the wait a person chose for the rules they are making: empty is the usual wait (0), anything else must be a whole
// number of minutes (the hub says whether it is in range).
func settleOf(r *http.Request) (int, error) {
	v := strings.TrimSpace(r.PostFormValue("settle"))
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, hub.ErrBadSettle
	}
	return n, nil
}

// waitMinutes is the app's usual wait before a rule offers a photo, in whole minutes, for a form to say.
func (w *Server) waitMinutes() int { return max(1, int((w.Hub.RuleWait()+30*time.Second)/time.Minute)) }

// PetPhoto is one of the person's recent photos, to pick as an example of a pet.
type PetPhoto struct{ ID string }

// petForm is the form that makes a rule for a pet: the person's newest photos, to pick three to eight of the animal from.
func (w *Server) petForm(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	if w.Hub == nil || !w.Hub.SelfService() {
		http.NotFound(rw, r)
		return
	}
	items, err := me.sess.Recent(r.Context(), 60)
	if err != nil {
		http.Error(rw, "could not list your photos", http.StatusBadGateway)
		return
	}
	var photos []PetPhoto
	for _, a := range items {
		if a.OwnerId == me.user.ID && !a.IsTrashed && !a.IsArchived {
			photos = append(photos, PetPhoto{ID: a.Id.String()})
		}
	}
	rw.Header().Set("Cache-Control", "no-store")
	PetForm(r.URL.Query().Get("service"), photos, w.Hub.Pets != nil).Render(r.Context(), rw)
}

// petPick is the photos to choose a pet's examples from, for the forms that offer a sharing and say yes to one (their own
// checkboxes are named "pet").
func (w *Server) petPick(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	if w.Hub == nil || !w.Hub.SelfService() {
		http.NotFound(rw, r)
		return
	}
	items, err := me.sess.Recent(r.Context(), 60)
	if err != nil {
		http.Error(rw, "could not list your photos", http.StatusBadGateway)
		return
	}
	var photos []PetPhoto
	for _, a := range items {
		if a.OwnerId == me.user.ID && !a.IsTrashed && !a.IsArchived {
			photos = append(photos, PetPhoto{ID: a.Id.String()})
		}
	}
	rw.Header().Set("Cache-Control", "no-store")
	PetPick(photos, w.Hub.Pets != nil).Render(r.Context(), rw)
}

// petPhoto shows one of the person's own photos while they pick the examples of a pet. Only their own are shown.
func (w *Server) petPhoto(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	id, err := uuid.Parse(r.PathValue("asset"))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	if a, err := me.sess.Asset(r.Context(), id); err != nil || a.OwnerId != me.user.ID || a.IsTrashed {
		http.NotFound(rw, r)
		return
	}
	data, ct, err := me.sess.Thumbnail(r.Context(), id, false)
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

func (w *Server) addPet(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "pet rule made", func(ctx context.Context, me *member, service string) error {
		r.ParseForm()
		_, err := w.Hub.AddRule(ctx, me.sess, me.user, hub.RuleParams{Service: service, Examples: ids(r, "photo")})
		return err
	})
}

// ImportFollowViews are the destinations the person's rules follow.
func (w *Server) importViews(ctx context.Context, me *member) []ImportFollowView {
	if w.Hub == nil {
		return nil
	}
	rules, err := w.Hub.RulesFor(ctx, me.user.ID, "")
	if err != nil {
		return nil
	}
	l := loc(ctx)
	var out []ImportFollowView
	for _, r := range rules {
		if r.Kind != store.RuleKindImport {
			continue
		}
		v := ImportFollowView{ID: r.ID, Source: r.Cond.SourceName, Prefix: r.Cond.Prefix, Dest: importDestWords(ctx, r.Dest, r.DestAlbum), Mode: r.Mode, Always: r.Always, Paused: r.Paused, CanAuto: hub.CanAuto(r), Problem: r.Problem}
		if !r.LastLook.IsZero() {
			v.Last = l.Stamp(r.LastLook.Local())
		}
		out = append(out, v)
	}
	return out
}

func importDestWords(ctx context.Context, dest, album string) string {
	if dest == "tag" || dest == "" {
		return t(ctx, "In my library, tagged with where they came from")
	}
	return destWords(ctx, dest, album)
}

// importSources lists the destinations the person may follow, for the form; it is asked for when the form is opened.
func (w *Server) importSources(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	if w.Hub == nil || !w.Hub.SelfService() {
		http.NotFound(rw, r)
		return
	}
	var opts []ImportOption
	for _, c := range w.Hub.ImportSources(r.Context(), me.user.ID) {
		opts = append(opts, ImportOption{Ref: c.Ref, Label: c.Name + " (" + destKindLabel(r.Context(), c.Kind) + ")"})
	}
	rw.Header().Set("Cache-Control", "no-store")
	ImportForm(opts).Render(r.Context(), rw)
}

func (w *Server) followDestination(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "destination followed", func(ctx context.Context, me *member, _ string) error {
		album := strings.TrimSpace(r.PostFormValue("album"))
		_, err := w.Hub.FollowDestination(ctx, me.sess, me.user, hub.ImportParams{Source: r.PostFormValue("source"), Prefix: r.PostFormValue("prefix"), History: r.PostFormValue("history"), Dest: hub.LibraryDest{Kind: r.PostFormValue("dest"), Album: album}})
		return err
	})
}

// importPhoto shows a small picture of a file a followed destination offers; only files the rule offered are ever read.
func (w *Server) importPhoto(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	if w.Hub == nil {
		http.NotFound(rw, r)
		return
	}
	b, err := w.Hub.ImportThumbnail(r.Context(), me.user.ID, r.PathValue("rule"), r.PathValue("asset"))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Content-Type", "image/jpeg")
	rw.Header().Set("Cache-Control", "private, max-age=3600")
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.Write(b)
}

// placeSides says on how many sides the photo had a photo with a place.
func placeSides(ctx context.Context, r hub.PlaceReason) string {
	if r.TwoSided() {
		return t(ctx, "on both sides")
	}
	return t(ctx, "on one side")
}

// placeViews is the person's rule that gives places, for their page.
func (w *Server) placeViews(ctx context.Context, me *member) PlacesView {
	if w.Hub == nil || !w.Hub.SelfService() {
		return PlacesView{}
	}
	v := PlacesView{Available: true}
	rules, err := w.Hub.RulesFor(ctx, me.user.ID, "")
	if err != nil {
		return v
	}
	for _, r := range rules {
		if r.Kind != store.RuleKindPlace {
			continue
		}
		pv := PlaceRuleView{ID: r.ID, Mode: r.Mode, Always: r.Always, Paused: r.Paused, CanAuto: hub.CanAuto(r), Problem: r.Problem}
		if !r.LastLook.IsZero() {
			pv.Last = loc(ctx).Stamp(r.LastLook.Local())
		}
		v.Rule = &pv
	}
	return v
}

// followPlaces makes the rule that gives photos places.
func (w *Server) followPlaces(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "places rule made", func(ctx context.Context, me *member, _ string) error {
		_, err := w.Hub.FollowPlaces(ctx, me.sess, me.user)
		return err
	})
}

// petViews is the person's "Pets" card: whether the pet helper is on, and the pet rules they have.
func (w *Server) petViews(ctx context.Context, me *member) PetsView {
	v := PetsView{Available: w.Hub != nil && w.Hub.SelfService()}
	if !v.Available {
		return v
	}
	v.Helper = w.Hub.Pets != nil
	rules, err := w.Hub.RulesFor(ctx, me.user.ID, "")
	if err != nil {
		return v
	}
	for _, r := range rules {
		if len(r.Cond.Examples) == 0 {
			continue
		}
		where := r.Service
		if hub.IsPairingName(where) {
			where = t(ctx, "a photo sharing")
		}
		v.Rules = append(v.Rules, PetRuleView{Where: where})
	}
	return v
}
