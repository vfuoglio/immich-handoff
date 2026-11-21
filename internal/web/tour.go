package web

import (
	"context"
	"net/http"

	"github.com/vfuoglio/immich-handoff/internal/docs"
)

// The first-run tour: coach-marks on the real page. What it says lives in the front matter of docs/start/the-page.md (one source with the
// guides); what each stop points at is named there by a key, and this table says where that thing is on the page, and which view,
// segment or category of Settings has to be showing for it to be seen. The same keys are used by the "Show me" of the Get started card.

type tourTarget struct {
	Selector string `json:"selector"`
	View     string `json:"view,omitempty"` // overview, services or settings: the view to show first
	Seg      string `json:"seg,omitempty"`  // the part of Services
	Cat      string `json:"cat,omitempty"`  // the category of Settings
}

var tourTargets = map[string]tourTarget{
	"needs":             {Selector: "#v-overview .ov-needs", View: "overview"},
	"scene":             {Selector: "#scene", View: "overview"},
	"services-tab":      {Selector: `nav.tabs a[data-view="services"]`},
	"offer":             {Selector: "#v-services details.offer", View: "services", Seg: "services"},
	"invite":            {Selector: "#v-services", View: "services", Seg: "services"},
	"send":              {Selector: "a.send-btn, nav.tabs a.send-tab"},
	"settings-tab":      {Selector: `nav.tabs a[data-view="settings"]`},
	"notifications":     {Selector: "#set-notifications", View: "settings", Cat: "notifications"},
	"palette":           {Selector: "#palette"},
	"help":              {Selector: "a.help-btn"},
	"tour":              {},
	"admin-share":       {Selector: "#adm-share + *"},
	"admin-connections": {Selector: "#adm-connections + *"},
	"admin-services":    {Selector: "#adm-services + *"},
	"admin-backups":     {Selector: "#adm-backups + *"},
	"admin-pet":         {Selector: "#adm-pet + *"},
}

// TourStopView is one stop, in the person's language.
type TourStopView struct {
	Target string `json:"target"`
	Title  string `json:"title"`
	Text   string `json:"text"`
	URL    string `json:"url,omitempty"` // the guide that explains more
}

// TourView is what the page hands its script: the stops of this page's tour, where things are, and whether to start by itself.
type TourView struct {
	Kind    string                `json:"kind"` // user or admin
	Auto    bool                  `json:"auto"` // the person has not seen it yet
	Stops   []TourStopView        `json:"stops"`
	Targets map[string]tourTarget `json:"targets"`
}

func (w *Server) tourView(ctx context.Context, me *member, kind string, gated bool) TourView {
	v := TourView{Kind: kind, Targets: tourTargets}
	for _, s := range docs.Tour(kind) {
		stop := TourStopView{Target: s.Target, Title: t(ctx, s.Title), Text: t(ctx, s.Text)}
		if _, _, _, ok := docs.Tip(s.Feature); ok && s.Feature != "" {
			if e, ok := helpEntry(s.Feature, me.user.IsAdmin); ok {
				stop.URL = "help/" + e.Feature
			}
		}
		v.Stops = append(v.Stops, stop)
	}
	v.Auto = !gated && w.Hub != nil && !w.Hub.TourSeen(ctx, me.user.ID, kind)
	return v
}

// tourSeen remembers that the person has been through, or skipped, a tour.
func (w *Server) tourSeen(rw http.ResponseWriter, r *http.Request) {
	me := w.guarded(rw, r)
	if me == nil {
		return
	}
	kind := r.FormValue("kind")
	if kind != "user" && !(kind == "admin" && me.user.IsAdmin) {
		http.Error(rw, "no such tour", http.StatusBadRequest)
		return
	}
	if w.Hub != nil {
		if err := w.Hub.SetTourSeen(r.Context(), me.user.ID, kind); err != nil {
			w.Log.Warn("could not remember the tour", "err", err)
			http.Error(rw, "could not remember it", http.StatusInternalServerError)
			return
		}
	}
	rw.WriteHeader(http.StatusNoContent)
}
