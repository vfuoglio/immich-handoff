// Package web serves the webhook receiver and the web page.
// The page uses Immich's own sign-in: the browser's cookie is forwarded to /api/users/me. Any Immich
// user can open it; only people who take part in a service see its activity.
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

//go:embed static
var staticFS embed.FS

type Server struct {
	S             *syncer.Syncer
	Hub           *hub.Hub // nil: people cannot join or leave from the page
	BaseURL       string
	WebhookHeader string
	WebhookSecret string
	Log           *slog.Logger
	BackgroundCtx context.Context
}

func (w *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheFor(http.FileServerFS(static), time.Hour)))
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) { rw.Write([]byte("ok")) })
	mux.HandleFunc("POST /webhook/immich", w.webhook)
	mux.HandleFunc("GET /{$}", w.dashboard)
	mux.HandleFunc("GET /invite/{token}", w.invite)
	mux.HandleFunc("POST /services/visibility", w.setVisibility)
	mux.HandleFunc("POST /services/invite", w.newInvite)
	mux.HandleFunc("GET /fragments/count", w.countFragment)
	mux.HandleFunc("GET /fragments/lastrun", w.lastRunFragment)
	mux.HandleFunc("GET /events", w.events)
	mux.HandleFunc("POST /sync", w.syncNow)
	mux.HandleFunc("POST /join", w.join)
	mux.HandleFunc("POST /leave", w.leave)
	mux.HandleFunc("POST /services", w.createService)
	mux.HandleFunc("POST /services/delete", w.deleteService)
	mux.HandleFunc("POST /ask", w.ask)
	mux.HandleFunc("POST /reconnect", w.reconnect)
	mux.HandleFunc("POST /decide", w.decide)
	return mux
}

func cacheFor(h http.Handler, d time.Duration) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(d.Seconds())))
		h.ServeHTTP(rw, r)
	})
}

// The payload is deliberately not trusted: the webhook only wakes the importer, and the asset id it
// names is a hint that is checked against Immich before use.
func (w *Server) webhook(rw http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(w.WebhookHeader)), []byte(w.WebhookSecret)) != 1 {
		http.Error(rw, "bad webhook secret", http.StatusUnauthorized)
		return
	}
	if body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10)); err == nil {
		w.Log.Debug("webhook received", "body", string(body))
		var ev struct {
			Data struct {
				Asset struct {
					ID uuid.UUID `json:"id"`
				} `json:"asset"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &ev) == nil && ev.Data.Asset.ID != uuid.Nil {
			w.S.AddHint(ev.Data.Asset.ID)
		}
	}
	go w.S.Run(w.BackgroundCtx, "webhook", false)
	rw.WriteHeader(http.StatusAccepted)
}

// member is the signed-in Immich user, with a client that acts with their own session.
type member struct {
	user immich.User
	sess *immich.API
}

func (w *Server) authenticate(r *http.Request) (*member, int, string) {
	cookie := r.Header.Get("Cookie")
	if cookie == "" {
		return nil, http.StatusUnauthorized, "not signed in to Immich"
	}
	a, err := immich.New(w.BaseURL, immich.Auth{Cookie: cookie}, nil)
	if err != nil {
		return nil, http.StatusInternalServerError, err.Error()
	}
	u, err := a.Me(r.Context())
	if err != nil {
		return nil, http.StatusUnauthorized, "not signed in to Immich"
	}
	return &member{user: u, sess: a}, 0, ""
}

// takesPart is the account the user holds in the service in this role, if any.
func (m *member) account(svc *syncer.Service, role string) *syncer.Account {
	group := svc.Senders
	if role == "receiver" {
		group = svc.Receivers
	}
	for _, a := range group {
		if a.UserID == m.user.ID {
			return a
		}
	}
	return nil
}

func (w *Server) servicesFor(ctx context.Context, m *member) []ServiceView {
	joined := map[string]bool{}
	if w.Hub != nil {
		joined, _ = w.Hub.Joined(ctx, m.user.ID)
	}
	var out []ServiceView
	for _, svc := range w.S.Current() {
		if m.account(svc, "sender") != nil {
			out = append(out, ServiceView{svc.Label(), "sender", svc.Tag, len(svc.Senders), len(svc.Receivers), joined[svc.Label()], svc.Broken, svc.Copies()})
		}
		if m.account(svc, "receiver") != nil {
			out = append(out, ServiceView{svc.Label(), "receiver", svc.InboxTag, len(svc.Senders), len(svc.Receivers), false, svc.Broken, svc.Copies()})
		}
	}
	return out
}

func (w *Server) dashboardData(ctx context.Context, me *member) DashboardData {
	d := DashboardData{User: me.user.Name, Services: w.servicesFor(ctx, me), Last: w.S.Last()}
	if w.Hub == nil || !w.Hub.SelfService() {
		return d
	}
	d.CanOffer = true
	if offers, err := w.Hub.Offers(ctx, me.user.ID); err == nil {
		for _, o := range offers {
			owner := ""
			if o.Service.OwnerID != uuid.Nil {
				owner = ownerName(o.Service)
			}
			d.Offers = append(d.Offers, OfferView{Name: o.Service.Label(), Tag: o.Service.Tag, Owner: owner, State: o.State, Copy: o.Service.Copies()})
		}
	}
	if owned, err := w.Hub.Owned(ctx, me.user.ID); err == nil {
		for _, o := range owned {
			ov := OwnedView{Name: o.Service.Label(), Tag: o.Service.Tag, Policy: "approval", Broken: o.Service.Broken, Invite: w.Hub.InviteOf(o.Service), Copy: o.Service.Copies()}
			if members, err := w.Hub.MembersOf(ctx, o.Service.Label()); err == nil {
				for _, m := range members {
					ov.Members = append(ov.Members, MemberView{Name: m.Name, State: m.State})
				}
			}
			if o.Service.AllowJoin {
				ov.Policy = "open"
			}
			for _, r := range o.Requests {
				ov.Requests = append(ov.Requests, RequestView{ID: r.ID, Name: r.Name, Email: r.Email})
			}
			d.Owned = append(d.Owned, ov)
		}
	}
	d.Orphaned, _ = w.Hub.Orphaned(ctx, me.user.ID)
	if broken, err := w.Hub.BrokenFor(ctx, me.user.ID); err == nil {
		for _, b := range broken {
			d.Broken = append(d.Broken, BrokenView{b.Service, b.Note})
		}
	}
	if acts, err := w.Hub.Activity(ctx, me.user.ID, 12); err == nil {
		for _, a := range acts {
			d.Activity = append(d.Activity, ActivityView{a.At.Local().Format("2 Jan 15:04"), a.Text})
		}
	}
	return d
}

func ownerName(s *syncer.Service) string {
	if len(s.Receivers) > 0 {
		return s.Receivers[0].Name
	}
	return ""
}

// invite is the landing page of an invitation link: the dashboard with that one service on top.
func (w *Server) invite(rw http.ResponseWriter, r *http.Request) {
	me, code, _ := w.authenticate(r)
	if me == nil {
		rw.WriteHeader(code)
		Notice("Sign in to Immich first",
			"This invitation needs your Immich login. Sign in to Immich in this browser, then open the link again.", false).Render(r.Context(), rw)
		return
	}
	token := r.PathValue("token")
	var offer *hub.Offer
	err := hub.ErrNoInvite
	if w.Hub != nil {
		offer, err = w.Hub.InviteOffer(r.Context(), me.user.ID, token)
	}
	if err != nil {
		text := "This invitation is not valid any more. Ask the person who sent it for a new link."
		if errors.Is(err, hub.ErrAlready) {
			text = "You already take part in this service."
		}
		rw.WriteHeader(http.StatusNotFound)
		Notice("Invitation", text, true).Render(r.Context(), rw)
		return
	}
	d := w.dashboardData(r.Context(), me)
	d.Invited = &OfferView{Name: offer.Service.Label(), Tag: offer.Service.Tag, Owner: ownerName(offer.Service), State: offer.State, Invite: token, Copy: offer.Service.Copies()}
	rw.Header().Set("Cache-Control", "no-store")
	Dashboard(d).Render(r.Context(), rw)
}

func (w *Server) dashboard(rw http.ResponseWriter, r *http.Request) {
	me, code, _ := w.authenticate(r)
	if me == nil {
		rw.WriteHeader(code)
		Notice("Sign in to Immich first",
			"This page uses your Immich login. Sign in to Immich in this browser, then reload this page.", false).Render(r.Context(), rw)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	Dashboard(w.dashboardData(r.Context(), me)).Render(r.Context(), rw)
}

// requireMember is for fragments and streams: sign-in plus taking part in at least one service.
func (w *Server) requireMember(rw http.ResponseWriter, r *http.Request) *member {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return nil
	}
	if len(w.servicesFor(r.Context(), me)) == 0 {
		http.Error(rw, "you do not take part in any service", http.StatusForbidden)
		return nil
	}
	return me
}

// countFragment is how many photos wait (sender) or sit in the inbox (receiver) for one service.
func (w *Server) countFragment(rw http.ResponseWriter, r *http.Request) {
	me := w.requireMember(rw, r)
	if me == nil {
		return
	}
	name, role := r.URL.Query().Get("service"), r.URL.Query().Get("role")
	for _, svc := range w.S.Current() {
		if svc.Label() != name {
			continue
		}
		acct := me.account(svc, role)
		if acct == nil {
			break
		}
		tag := svc.Tag
		if role == "receiver" {
			tag = svc.InboxTag
		}
		rw.Header().Set("Cache-Control", "no-store")
		Count(w.count(r.Context(), acct, tag), role).Render(r.Context(), rw)
		return
	}
	http.Error(rw, "no such service for you", http.StatusNotFound)
}

func (w *Server) lastRunFragment(rw http.ResponseWriter, r *http.Request) {
	if w.requireMember(rw, r) == nil {
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	LastRun(w.S.Last()).Render(r.Context(), rw)
}

// events tells the open page when a pass has finished, so it can refresh its cards.
func (w *Server) events(rw http.ResponseWriter, r *http.Request) {
	if w.requireMember(rw, r) == nil {
		return
	}
	fl, ok := rw.(http.Flusher)
	if !ok {
		http.Error(rw, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("X-Accel-Buffering", "no") // do not let a reverse proxy hold the stream back
	ch, cancel := w.S.Subscribe()
	defer cancel()
	fmt.Fprint(rw, ": connected\n\n")
	fl.Flush()
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			fmt.Fprint(rw, "event: run\ndata: 1\n\n")
		case <-keepalive.C:
			fmt.Fprint(rw, ": keepalive\n\n")
		}
		fl.Flush()
	}
}

// count is how many photos carry the tag in the account (the app's own key does the looking).
func (w *Server) count(ctx context.Context, a *syncer.Account, tag string) int {
	id, err := a.API.FindTag(ctx, tag, false)
	if err != nil || id == nil {
		return 0
	}
	assets, err := a.API.Search(ctx, immich.MetadataSearchDto{TagIds: &[]uuid.UUID{*id}})
	if err != nil {
		return 0
	}
	return len(assets)
}

// guarded runs an action that changes something: it needs the cross-site header and a sign-in.
func (w *Server) guarded(rw http.ResponseWriter, r *http.Request) *member {
	// Cookie auth is ambient, so require a header a cross-site form cannot send (CSRF guard).
	if r.Header.Get("X-Requested-With") != "immich-handoff" {
		http.Error(rw, "missing X-Requested-With", http.StatusBadRequest)
		return nil
	}
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return nil
	}
	return me
}

func (w *Server) syncNow(rw http.ResponseWriter, r *http.Request) {
	me := w.guarded(rw, r)
	if me == nil {
		return
	}
	if len(w.servicesFor(r.Context(), me)) == 0 {
		http.Error(rw, "you do not take part in any service", http.StatusForbidden)
		return
	}
	go w.S.Run(w.BackgroundCtx, "manual: "+me.user.Email, false)
	rw.WriteHeader(http.StatusAccepted)
	rw.Write([]byte("Sync started"))
}

func (w *Server) join(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "joined", func(ctx context.Context, me *member, service string) error {
		return w.Hub.Join(ctx, me.sess, me.user, service, r.PostFormValue("invite"))
	})
}

func (w *Server) leave(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "left", func(ctx context.Context, me *member, service string) error {
		return w.Hub.Leave(ctx, me.sess, me.user, service)
	})
}

// membership runs a join or leave and answers htmx: a refresh on success, a short message on failure.
func (w *Server) membership(rw http.ResponseWriter, r *http.Request, done string, act func(context.Context, *member, string) error) {
	me := w.guarded(rw, r)
	if me == nil {
		return
	}
	if w.Hub == nil || !w.Hub.SelfService() {
		http.Error(rw, "joining from this page is not switched on", http.StatusNotFound)
		return
	}
	service := r.PostFormValue("service")
	if err := act(r.Context(), me, service); err != nil {
		status := http.StatusInternalServerError
		if hub.IsUserError(err) {
			status = http.StatusConflict
		}
		w.Log.Warn("membership change failed", "action", done, "user", me.user.Email, "service", service, "err", err)
		rw.Header().Set("HX-Reswap", "innerHTML")
		rw.WriteHeader(status)
		fmt.Fprint(rw, "Could not do that: ", err.Error())
		return
	}
	rw.Header().Set("HX-Refresh", "true")
	rw.WriteHeader(http.StatusNoContent)
}

func (w *Server) createService(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "created", func(ctx context.Context, me *member, _ string) error {
		days, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("expiry")))
		return w.Hub.CreateService(ctx, me.sess, me.user, hub.CreateParams{
			Name: r.PostFormValue("name"), Tag: r.PostFormValue("tag"), Policy: r.PostFormValue("policy"),
			Visibility: r.PostFormValue("visibility"), Mode: r.PostFormValue("mode"), InboxTag: r.PostFormValue("inbox"), ExpiryDays: days,
		})
	})
}

func (w *Server) deleteService(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "deleted", func(ctx context.Context, me *member, service string) error {
		return w.Hub.DeleteService(ctx, me.sess, me.user, service)
	})
}

func (w *Server) ask(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "asked", func(ctx context.Context, me *member, service string) error {
		return w.Hub.Ask(ctx, me.user, service, r.PostFormValue("invite"))
	})
}

func (w *Server) decide(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "decided", func(ctx context.Context, me *member, service string) error {
		return w.Hub.Decide(ctx, me.user, service, r.PostFormValue("request"), r.PostFormValue("decision") == "approve")
	})
}

func (w *Server) reconnect(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "reconnected", func(ctx context.Context, me *member, service string) error {
		return w.Hub.ReconnectService(ctx, me.sess, me.user, service)
	})
}

func (w *Server) setVisibility(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "changed", func(ctx context.Context, me *member, service string) error {
		return w.Hub.SetVisibility(ctx, me.user, service, r.PostFormValue("visibility"))
	})
}

func (w *Server) newInvite(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "changed", func(ctx context.Context, me *member, service string) error {
		return w.Hub.NewInvite(ctx, me.user, service)
	})
}
