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
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/backup"
	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/live"
	"github.com/vfuoglio/immich-handoff/internal/notify"
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

	// PublicImmichURL is the address of Immich as the browser sees it. Optional: with it the page shows
	// thumbnails of photos that are waiting to be handed over.
	PublicImmichURL string
	Version         string
	WebhookURL      string          // PUBLIC_WEBHOOK_URL, for the administrator's connection check
	Backups         *backup.Manager // nil without a store: nothing to back up
	BackupDownload  bool            // HANDOFF_ADMIN_BACKUP_DOWNLOAD: the administrator's page may hand out a backup (off by default)
	DownloadGap     time.Duration   // the least time between two downloads (default 10 minutes)
	Bus             *live.Bus       // what is happening, for the open pages (nil: only the "run" signal is sent)
	ClientIPHeader  string          // DROPBOX_CLIENT_IP_HEADER: the header in which a reverse proxy puts a guest's real address (empty: the connection's)
	Theme           string          // THEME: the colour scheme people start with, twilight (default) or immich; each person can change it
	ThemeMode       string          // THEME_MODE: auto (default, the browser decides), light or dark

	Notifier *notify.Notifier // where messages for whoever runs the app go; for the administrator's list and test (nil: none)

	limiter  limiter
	waker    phoneWaker // the hand-over pass that follows photos sent from a phone
	gate     downloadGate
	testGate downloadGate // one test of the notification destinations at a time, and not too often
}

func init() {
	// the web app manifest is served by the file server below; not every system's table knows its type
	mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// LanguageCookie is the cookie in which the page remembers the language its person chose.
const LanguageCookie = "handoff-lang"

func (w *Server) Handler() http.Handler {
	return w.secure(w.routes())
}

func (w *Server) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheFor(http.FileServerFS(static), time.Hour)))
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) { rw.Write([]byte("ok")) })
	mux.HandleFunc("POST /webhook/immich", w.webhook)
	mux.HandleFunc("GET /metrics", w.metrics)
	mux.HandleFunc("POST /backup", w.queueBackup)
	mux.HandleFunc("GET /backup", w.backupStatus)
	mux.HandleFunc("GET /backup/{id}", w.backupStatus)
	mux.HandleFunc("GET /{$}", w.dashboard)
	mux.HandleFunc("GET /invite/{token}", w.invite)
	mux.HandleFunc("GET /admin", w.adminPage)
	mux.HandleFunc("POST /admin/check", w.adminCheck)
	mux.HandleFunc("POST /admin/notify/test", w.adminNotifyTest)
	mux.HandleFunc("POST /admin/backup/download", w.adminBackupDownload)
	mux.HandleFunc("POST /admin/services/remove", w.adminRemoveService)
	mux.HandleFunc("POST /admin/members/remove", w.adminRemoveMember)
	mux.HandleFunc("POST /services/visibility", w.setVisibility)
	mux.HandleFunc("POST /services/invite", w.newInvite)
	mux.HandleFunc("POST /services/rules", w.setRules)
	mux.HandleFunc("POST /services/lifetime", w.setLifetime)
	mux.HandleFunc("POST /services/digest", w.setDigest)
	mux.HandleFunc("POST /services/pool", w.setPool)
	mux.HandleFunc("POST /services/takeback", w.takeBack)
	mux.HandleFunc("POST /send/start", w.phoneStart)
	mux.HandleFunc("GET /send/{id}", w.phoneStatus)
	mux.HandleFunc("PUT /send/{id}/chunk", w.phoneChunk)
	mux.HandleFunc("POST /send/{id}/done", w.phoneDone)
	mux.HandleFunc("POST /send/{id}/cancel", w.phoneCancel)
	mux.HandleFunc("POST /qr", w.qrCode)
	mux.HandleFunc("POST /services/bridge-invite", w.newBridgeInvite)
	mux.HandleFunc("POST /services/bridge/remove", w.removeBridge)
	mux.HandleFunc("GET /bridge/{token}", w.bridgePage)
	mux.HandleFunc("POST /bridge/{token}", w.bridgeConnect)
	mux.HandleFunc("GET /drop/{token}", w.dropPage)
	mux.HandleFunc("POST /drop/{token}/upload", w.dropUpload)
	mux.HandleFunc("POST /drop/{token}/unlock", w.dropUnlock)
	mux.HandleFunc("POST /d/{token}/unlock", w.dropUnlock)
	mux.HandleFunc("GET /d/{token}", w.dropPage)
	mux.HandleFunc("POST /d/{token}/upload", w.dropUpload)
	mux.HandleFunc("GET /drop/{token}/theme.css", w.dropTheme)
	mux.HandleFunc("GET /drop/{token}/cover", w.dropCover)
	mux.HandleFunc("GET /drop/{token}/logo", w.dropLogo)
	mux.HandleFunc("GET /drop/{token}/manifest.webmanifest", w.dropManifest)
	mux.HandleFunc("GET /drop/{token}/gallery", w.galleryFeed)
	mux.HandleFunc("GET /drop/{token}/photo/{id}", w.galleryPhoto)
	mux.HandleFunc("GET /drop/{token}/original/{id}", w.galleryOriginal)
	mux.HandleFunc("GET /drop/{token}/zip", w.galleryZip)
	mux.HandleFunc("POST /drop/{token}/heart/{id}", w.galleryHeart)
	mux.HandleFunc("GET /drop/{token}/mine", w.dropMine)
	mux.HandleFunc("POST /drop/{token}/mine/delete", w.dropMineDelete)
	mux.HandleFunc("GET /d/{token}/mine", w.dropMine)
	mux.HandleFunc("POST /d/{token}/mine/delete", w.dropMineDelete)
	mux.HandleFunc("POST /drop/{token}/message", w.dropMessage)
	mux.HandleFunc("POST /d/{token}/message", w.dropMessage)
	mux.HandleFunc("POST /dropbox/extras", w.extrasDropBox)
	mux.HandleFunc("POST /dropbox/message/delete", w.deleteMessage)
	mux.HandleFunc("GET /dropbox/voice/{id}", w.voiceNote)
	mux.HandleFunc("GET /dropbox/guestbook/{id}", w.exportGuestBook)
	mux.HandleFunc("GET /dropbox/zip/{id}", w.dropZip)
	mux.HandleFunc("POST /dropbox/share", w.shareDropBox)
	mux.HandleFunc("POST /dropbox/unshare", w.unshareDropBox)
	mux.HandleFunc("GET /frame/{token}", w.framePage)
	mux.HandleFunc("GET /frame/{token}/feed", w.frameFeed)
	mux.HandleFunc("GET /frame/{token}/photo/{id}", w.framePhoto)
	mux.HandleFunc("GET /fragments/frame-albums", w.frameAlbums)
	mux.HandleFunc("POST /frames", w.createFrame)
	mux.HandleFunc("POST /frames/end", w.endFrame)
	mux.HandleFunc("GET /mod/{token}", w.modPage)
	mux.HandleFunc("GET /mod/{token}/pending/{id}", w.modFile)
	mux.HandleFunc("POST /mod/{token}/approve", w.modDecide(true))
	mux.HandleFunc("POST /mod/{token}/reject", w.modDecide(false))
	mux.HandleFunc("GET /mod/{token}/photo/{id}", w.modImage)
	mux.HandleFunc("POST /mod/{token}/mark", w.modMark)
	mux.HandleFunc("POST /dropbox/loved", w.lovedDropBox)
	mux.HandleFunc("POST /dropbox/moderator", w.moderatorDropBox)
	mux.HandleFunc("GET /wall/{token}", w.wallPage)
	mux.HandleFunc("GET /wall/{token}/feed", w.wallFeed)
	mux.HandleFunc("GET /wall/{token}/logo", w.wallLogo)
	mux.HandleFunc("GET /wall/{token}/photo/{id}", w.wallPhoto)
	mux.HandleFunc("POST /dropbox/wall", w.startWall)
	mux.HandleFunc("POST /dropbox/wall/stop", w.stopWall)
	mux.HandleFunc("POST /dropbox/message/wall", w.messageOnWall)
	mux.HandleFunc("POST /dropbox/highlights", w.startHighlights)
	mux.HandleFunc("POST /dropbox/onward", w.setOnward)
	mux.HandleFunc("POST /dropbox/handon", w.handOn)
	mux.HandleFunc("POST /dropbox/short", w.shortDropBox)
	mux.HandleFunc("POST /dropbox/mail", w.mailDropBox)
	mux.HandleFunc("POST /dropbox/renew", w.renewDropBox)
	mux.HandleFunc("POST /dropbox/look", w.lookDropBox)
	mux.HandleFunc("POST /dropbox/event", w.eventDropBox)
	mux.HandleFunc("POST /dropbox/template", w.saveDropTemplate)
	mux.HandleFunc("POST /dropbox/template/delete", w.deleteDropTemplate)
	mux.HandleFunc("POST /dropbox/fromtemplate", w.dropFromTemplate)
	mux.HandleFunc("POST /dropbox/duplicate", w.duplicateDropBox)
	mux.HandleFunc("GET /dropbox/card/{id}", w.dropCard)
	mux.HandleFunc("POST /dropbox", w.createDropBox)
	mux.HandleFunc("POST /dropbox/pause", w.pauseDropBox)
	mux.HandleFunc("POST /dropbox/revoke", w.revokeDropBox)
	mux.HandleFunc("POST /dropbox/extend", w.extendDropBox)
	mux.HandleFunc("POST /dropbox/approve", w.decideDropBox(true))
	mux.HandleFunc("POST /dropbox/reject", w.decideDropBox(false))
	mux.HandleFunc("POST /dropbox/host", w.addDropHost)
	mux.HandleFunc("POST /dropbox/host/remove", w.removeDropHost)
	mux.HandleFunc("GET /dropbox/pending/{id}", w.pendingFile)
	mux.HandleFunc("GET /fragments/count", w.countFragment)
	mux.HandleFunc("GET /fragments/numbers", w.numbersFragment)
	mux.HandleFunc("GET /fragments/scene", w.sceneFragment)
	mux.HandleFunc("POST /guide/dismiss", w.dismissGuide)
	mux.HandleFunc("POST /me/mail", w.myMail)
	mux.HandleFunc("GET /me/push/key", w.pushKey)
	mux.HandleFunc("POST /me/push", w.pushSubscribe)
	mux.HandleFunc("POST /me/push/remove", w.pushRemove)
	mux.HandleFunc("POST /me/push/mode", w.pushMode)
	mux.HandleFunc("POST /me/push/test", w.pushTest)
	mux.HandleFunc("GET /a/{token}", w.actionPage)
	mux.HandleFunc("POST /a/{token}", w.actionRun)
	mux.HandleFunc("GET /unsubscribe/{token}", w.unsubscribePage)
	mux.HandleFunc("POST /unsubscribe/{token}", w.unsubscribe)
	mux.HandleFunc("GET /fragments/lastrun", w.lastRunFragment)
	mux.HandleFunc("GET /events", w.events)
	mux.HandleFunc("POST /sync", w.syncNow)
	mux.HandleFunc("POST /preferences/tags", w.enableTags)
	mux.HandleFunc("GET /fragments/albums", w.albumChoice)
	mux.HandleFunc("POST /services/album", w.linkAlbum)
	mux.HandleFunc("POST /services/album/stop", w.unlinkAlbum)
	mux.HandleFunc("POST /join", w.join)
	mux.HandleFunc("POST /leave", w.leave)
	mux.HandleFunc("POST /services", w.createService)
	mux.HandleFunc("POST /services/delete", w.deleteService)
	mux.HandleFunc("POST /services/limits", w.setLimits)
	mux.HandleFunc("POST /services/pause", w.pauseService)
	mux.HandleFunc("POST /pause", w.pauseSending)
	mux.HandleFunc("POST /ask", w.ask)
	mux.HandleFunc("POST /reconnect", w.reconnect)
	mux.HandleFunc("POST /decide", w.decide)
	return mux
}

// secure adds the headers that keep the page from being framed, sniffed or fed scripts from elsewhere,
// and bounds the size of what the page itself may post.
func (w *Server) secure(next http.Handler) http.Handler {
	img := "'self' data: blob:" // blob: is for the previews a guest's own browser makes of the files they chose
	if u, err := url.Parse(w.PublicImmichURL); err == nil && u.Scheme != "" && u.Host != "" {
		img += " " + u.Scheme + "://" + u.Host
	}
	csp := "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; manifest-src 'self'; worker-src 'self'; img-src " + img +
		"; media-src 'self' blob:; connect-src 'self'; form-action 'self'; base-uri 'self'; frame-ancestors 'none'"
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		h := rw.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		upload := dropRoute(r.URL.Path) && (strings.HasSuffix(r.URL.Path, "/upload") || strings.HasSuffix(r.URL.Path, "/message"))
		if r.Method == http.MethodPost && !upload && !strings.HasPrefix(r.URL.Path, "/dropbox/look") { // a guest's upload is bounded by its drop box instead
			r.Body = http.MaxBytesReader(rw, r.Body, 1<<20)
		}
		// the person's language: their own choice (a cookie the page sets), else what the browser asks for, else English
		var chosen string
		if c, err := r.Cookie(LanguageCookie); err == nil {
			chosen = c.Value
		}
		lang := i18n.Negotiate(chosen, r.Header.Get("Accept-Language"))
		h.Set("Content-Language", string(lang))
		h.Add("Vary", "Accept-Language")
		h.Add("Vary", "Cookie")
		r = r.WithContext(withTheme(context.WithValue(i18n.With(r.Context(), i18n.For(lang)), versionKey{}, w.Version), w.Theme, w.ThemeMode))
		// Pages use relative links so they work under a path prefix; the invitation page is one level
		// deeper than the others, so it points them back at the root.
		if strings.HasPrefix(r.URL.Path, "/invite/") {
			r = r.WithContext(context.WithValue(r.Context(), baseKey{}, "../"))
		}
		if dropRoute(r.URL.Path) { // /drop/<token> is as deep as an invitation; each part after it is one level deeper
			base := strings.Repeat("../", len(strings.Split(strings.Trim(r.URL.Path, "/"), "/"))-1)
			r = r.WithContext(context.WithValue(context.WithValue(r.Context(), baseKey{}, base), guestKey{}, &guestInfo{}))
		}
		if strings.HasPrefix(r.URL.Path, "/dropbox/card/") { // the printable card is two levels down
			r = r.WithContext(context.WithValue(r.Context(), baseKey{}, "../../"))
		}
		next.ServeHTTP(rw, r)
	})
}

// limiter allows each person a limited number of changes per minute.
type limiter struct {
	mu   sync.Mutex
	seen map[uuid.UUID][]time.Time
}

const changesPerMinute = 30

func (l *limiter) allow(user uuid.UUID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[uuid.UUID][]time.Time{}
	}
	now, keep := time.Now(), time.Now().Add(-time.Minute)
	recent := l.seen[user][:0]
	for _, t := range l.seen[user] {
		if t.After(keep) {
			recent = append(recent, t)
		}
	}
	if len(recent) >= changesPerMinute {
		l.seen[user] = recent
		return false
	}
	l.seen[user] = append(recent, now)
	return true
}

// metrics is a small Prometheus-style text page. It needs the webhook secret, since it is not meant for browsers.
func (w *Server) metrics(rw http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(w.WebhookHeader)), []byte(w.WebhookSecret)) != 1 {
		http.Error(rw, "bad secret", http.StatusUnauthorized)
		return
	}
	t := w.S.Totals()
	rw.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(rw, "# TYPE handoff_passes_total counter\nhandoff_passes_total %d\n", t.Passes)
	fmt.Fprint(rw, "# TYPE handoff_photos_total counter\n")
	for _, c := range []struct {
		result string
		n      int
	}{{"imported", t.Counts.Imported}, {"duplicate", t.Counts.Duplicate}, {"refused", t.Counts.Refused}, {"error", t.Counts.Errors}} {
		fmt.Fprintf(rw, "handoff_photos_total{result=%q} %d\n", c.result, c.n)
	}
	fmt.Fprintf(rw, "# TYPE handoff_services gauge\nhandoff_services %d\n", len(w.S.Current()))
	fmt.Fprintf(rw, "# TYPE handoff_build_info gauge\nhandoff_build_info{version=%q} 1\n", w.Version)
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
			// set by `immich-handoff doctor` to test the address and the secret without starting a pass
			Test bool `json:"handoffTest"`
			Data struct {
				Asset struct {
					ID uuid.UUID `json:"id"`
				} `json:"asset"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &ev) == nil && ev.Test {
			rw.WriteHeader(http.StatusOK)
			return
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
		if a := m.account(svc, "sender"); a != nil {
			v := ServiceView{Name: svc.Label(), Role: "sender", Tag: svc.Tag, From: len(svc.Senders), To: len(svc.Receivers),
				CanLeave: joined[svc.Label()], Paused: svc.Broken, Copy: svc.CopiesFor(a), CanWatch: joined[svc.Label()], Watch: a.WatchAlbumName,
				CanPause: joined[svc.Label()], MinePaused: a.Paused, Limit: limitText(ctx, svc.LimitPhotos, svc.LimitMB), Account: a.Name, Rules: w.rulesText(ctx, svc)}
			switch {
			case svc.Paused:
				v.Hold = t(ctx, "The owner paused this service.")
			case a.Paused:
				v.Hold = t(ctx, "You paused sending to this service.")
			}
			w.limitsFor(ctx, &v, svc, a)
			if w.Hub != nil && w.Hub.ReceiverDelete {
				if svc.LifetimeDays > 0 {
					v.Lifetime = n(ctx, "Photos you hand over to this service are moved to the receiver's trash after {n} day unless they file them in an album.", "Photos you hand over to this service are moved to the receiver's trash after {n} days unless they file them in an album.", svc.LifetimeDays)
				}
				if count, at := w.Hub.TakeBackOffer(ctx, m.user.ID, svc.Label()); count > 0 {
					v.TakeBack = n(ctx, "{n} photo handed over {when}", "{n} photos handed over {when}", count, "when", loc(ctx).Stamp(at.Local()))
				}
			}
			if qw, ok := w.S.QuietFor(a); ok {
				v.QuietWait = n(ctx, "The quiet hours are not open, so {n} photo waits, still tagged. It is handed over from {when}.", "The quiet hours are not open, so {n} photos wait, still tagged. They are handed over from {when}.", qw.Waiting, "when", loc(ctx).Stamp(qw.Until.Local()))
			}
			out = append(out, v)
		}
		if m.account(svc, "receiver") != nil {
			out = append(out, ServiceView{Name: svc.Label(), Role: "receiver", Tag: svc.InboxTag, From: len(svc.Senders), To: len(svc.Receivers),
				Paused: svc.Broken, Copy: svc.Copies(), Album: svc.Album, Stopped: svc.Paused, Owned: svc.OwnerID == m.user.ID})
		}
	}
	return out
}

// limitsFor fills in how much of the daily limit a sender has used, and what is waiting because of it.
func (w *Server) limitsFor(ctx context.Context, v *ServiceView, svc *syncer.Service, a *syncer.Account) {
	if svc.LimitPhotos == 0 && svc.LimitMB == 0 || w.Hub == nil {
		return
	}
	snap, err := w.Hub.Handed(ctx, a, time.Now().Add(-syncer.LimitWindow))
	if err != nil || !snap.Tracked {
		return
	}
	var parts []string
	if svc.LimitPhotos > 0 {
		parts = append(parts, n(ctx, "{used} of {n} photo", "{used} of {n} photos", svc.LimitPhotos, "used", snap.Photos))
	}
	if svc.LimitMB > 0 {
		parts = append(parts, t(ctx, "{used} of {max} MB", "used", snap.Bytes>>20, "max", svc.LimitMB))
	}
	v.Used = t(ctx, "Used in the last 24 hours: {parts}.", "parts", strings.Join(parts, ", "))
	if h, ok := w.S.HeldFor(a); ok && syncer.Capped(svc, snap) {
		v.Waiting = n(ctx, "The daily limit is reached, so {n} photo waits, still tagged. It is handed over from {when}.", "The daily limit is reached, so {n} photos wait, still tagged. They are handed over from {when}.", h.Waiting, "when", loc(ctx).Stamp(h.Until.Local()))
	}
}

// ownedViews are the services the person offers, with their senders, limits and waiting requests.
func (w *Server) ownedViews(ctx context.Context, me *member) []OwnedView {
	var out []OwnedView
	owned, err := w.Hub.Owned(ctx, me.user.ID)
	if err != nil {
		return nil
	}
	for _, o := range owned {
		ov := OwnedView{Name: o.Service.Label(), Tag: o.Service.Tag, Policy: "approval", Broken: o.Service.Broken, Invite: w.Hub.InviteOf(o.Service), Album: o.Service.Album,
			Paused: o.Service.Paused, LimitPhotos: o.Service.LimitPhotos, LimitMB: o.Service.LimitMB,
			LifetimeDays: o.Service.LifetimeDays, DeleteOK: w.Hub.ReceiverDelete, QuietLargeMB: o.Service.Quiet.LargeMB, AcceptRating: o.Service.Accept.MinRating, AcceptPhotos: o.Service.Accept.PhotosOnly,
			AcceptOrient: o.Service.Accept.Orientation, AcceptMaxMB: o.Service.Accept.MaxMB,
			BridgeOn: w.Hub.Bridge.Enabled, BridgeInvite: w.Hub.BridgeInviteOf(ctx, o.Service.Label())}
		if o.Service.BrokenFix != "" { // not a receiver key that stopped working: reconnecting would not help
			ov.Broken, ov.BridgeStopped = "", o.Service.Broken+" "+o.Service.BrokenFix
		}
		if st, ok := w.Hub.BridgeStatus(ctx, o.Service.Label()); ok {
			ov.HasBridge, ov.BridgeName, ov.BridgeState, ov.BridgeNote, ov.BridgeVersion, ov.BridgeTakeBack = true, st.Name, st.State, st.Note, st.Version, st.TakeBack
			if !st.Checked.IsZero() {
				ov.BridgeChecked = loc(ctx).Stamp(st.Checked.Local())
			}
			if !st.RetryAt.IsZero() {
				ov.BridgeRetry = st.RetryAt.Local().Format("15:04")
			}
		}
		ov.Digest = w.digestView(ctx, o.Service.Label())
		if o.Service.Pool {
			ov.Pool, ov.PoolEnded = true, o.Service.Ended(time.Now())
			if !o.Service.EndsAt.IsZero() {
				ov.PoolEnds = loc(ctx).Stamp(o.Service.EndsAt.Local())
			}
		}
		if o.Service.Quiet.Set() {
			ov.QuietFrom, ov.QuietTo = syncer.Clock(o.Service.Quiet.From), syncer.Clock(o.Service.Quiet.To)
		}
		if members, err := w.Hub.MembersOf(ctx, o.Service.Label()); err == nil {
			for _, m := range members {
				mv := MemberView{Name: m.Name, State: m.State, Copy: m.Mode != "move", Paused: m.Paused,
					AtLimit: (o.Service.LimitPhotos > 0 && m.Photos >= o.Service.LimitPhotos) || (o.Service.LimitMB > 0 && m.MB >= int64(o.Service.LimitMB))}
				if o.Service.LimitPhotos > 0 || o.Service.LimitMB > 0 {
					mv.Used = t(ctx, "{photos}, {mb} MB in the last 24 hours", "photos", n(ctx, "{n} photo", "{n} photos", m.Photos), "mb", m.MB)
				}
				ov.Members = append(ov.Members, mv)
			}
		}
		if o.Service.AllowJoin {
			ov.Policy = "open"
		}
		for _, r := range o.Requests {
			ov.Requests = append(ov.Requests, RequestView{ID: r.ID, Name: r.Name, Email: r.Email})
		}
		out = append(out, ov)
	}
	return out
}

func (w *Server) dashboardData(ctx context.Context, me *member) DashboardData {
	d := DashboardData{User: me.user.Name, Services: w.servicesFor(ctx, me), Last: w.S.Last()}
	// Tags are off by default in Immich, and without them a person cannot add or see tags, which is the whole
	// of using this. If Immich cannot tell, say nothing rather than guess.
	if on, err := me.sess.TagsEnabled(ctx); err == nil {
		d.TagsOff = !on
	}
	if w.Hub == nil || !w.Hub.SelfService() {
		w.overview(ctx, me, &d) // an install with only fixed accounts has the greeting, the numbers and the scene too
		return d
	}
	d.CanOffer = true
	d.DeleteOK = w.Hub != nil && w.Hub.ReceiverDelete
	if offers, err := w.Hub.Offers(ctx, me.user.ID); err == nil {
		for _, o := range offers {
			owner := ""
			if o.Service.OwnerID != uuid.Nil {
				owner = ownerName(o.Service)
			}
			ov := OfferView{Name: o.Service.Label(), Tag: o.Service.Tag, Owner: owner, State: o.State, Pool: o.Service.Pool}
			if o.Service.Pool && !o.Service.EndsAt.IsZero() {
				ov.Ends = loc(ctx).Stamp(o.Service.EndsAt.Local())
			}
			d.Offers = append(d.Offers, ov)
		}
	}
	d.Owned = w.ownedViews(ctx, me)
	d.DropBoxes = w.dropBoxViews(ctx, me)
	d.Frames = w.frameViews(ctx, me)
	d.Templates = w.templateViews(ctx, me)
	d.Orphaned, _ = w.Hub.Orphaned(ctx, me.user.ID)
	if broken, err := w.Hub.BrokenFor(ctx, me.user.ID); err == nil {
		for _, b := range broken {
			d.Broken = append(d.Broken, BrokenView{b.Service, b.Note})
		}
	}
	if acts, err := w.Hub.Activity(ctx, me.user.ID, 12); err == nil {
		for _, a := range acts {
			d.Activity = append(d.Activity, ActivityView{loc(ctx).Stamp(a.At.Local()), unpack(ctx, a.Text), activityTone(a.Kind)})
		}
	}
	mc := w.Hub.MailCard(ctx, me.user, string(loc(ctx).Lang))
	d.Mail = MailView{Available: mc.Available, On: mc.On, Mode: mc.Mode, Email: mc.Email}
	d.Push = w.pushView(ctx, me)
	w.overview(ctx, me, &d)
	return d
}

// overview fills in what the top of the page shows: the greeting, what needs the person, whether there are
// numbers to show, and the first-run guide.
func (w *Server) overview(ctx context.Context, me *member, d *DashboardData) {
	first := strings.Fields(me.user.Name)
	d.Headline = t(ctx, "Hello.")
	if len(first) > 0 {
		d.Headline = t(ctx, "Hello, {name}.", "name", first[0])
	}
	d.Needs = needsOf(ctx, d)
	d.Gist = needsGist(ctx, len(d.Needs))
	d.Scene = sceneOf(ctx, d, w.S.Current())
	d.ShowNumber = len(d.Services) > 0
	if d.TagsOff {
		return // the page is switched off until Tags are on, and so is the guide
	}
	if w.Hub == nil || w.Hub.GuideDone(ctx, me.user.ID) {
		return
	}
	handed := 0
	if _, counts, err := w.Hub.Week(ctx, me.user.ID, time.Now()); err == nil {
		for _, c := range counts {
			handed += c
		}
	}
	steps := []GuideStep{
		{t(ctx, "Turn on Tags in Immich"), true}, // the page is switched off while they are off, so reaching here means they are on
		{t(ctx, "Join a service, or offer one of your own"), len(d.Services) > 0 || len(d.Owned) > 0},
		{t(ctx, "Hand over your first photo: add a service's tag to a photo in Immich"), handed > 0},
	}
	g := &GuideView{Steps: steps}
	for _, s := range steps {
		if s.Done {
			g.Done++
		}
	}
	if g.Done == len(steps) {
		// finished: remember it, so the guide never comes back, and do not show it
		if err := w.Hub.DismissGuide(ctx, me.user.ID); err != nil {
			w.Log.Warn("could not remember that the guide was finished", "err", err)
		}
		return
	}
	for _, s := range steps {
		if !s.Done {
			g.NextStep = s.Text
			break
		}
	}
	d.Guide = g
}

// needsOf lists what waits on the person, most urgent first: connections that are lost, then requests to answer,
// then pauses and limits that hold photos back.
func needsOf(ctx context.Context, d *DashboardData) []NeedView {
	var lost, ask, held []NeedView
	for _, b := range d.Broken {
		lost = append(lost, NeedView{Tone: "bad", Chip: t(ctx, "Connection lost"), Title: b.Service,
			Text:    t(ctx, "{reason} Nothing is handed over and your photos were not touched. Clean up removes what this app made in your Immich account; then you can join again.", "reason", unpack(ctx, b.Note)),
			Actions: []NeedAction{{Label: t(ctx, "Clean up"), Path: "leave", Vals: vals("service", b.Service), Primary: true}}})
	}
	for _, name := range d.Orphaned {
		lost = append(lost, NeedView{Tone: "bad", Chip: t(ctx, "Removed by its owner"), Title: name,
			Text:    t(ctx, "Nothing is handed over any more. Clean up to delete the API key and workflow this app made in your Immich account."),
			Actions: []NeedAction{{Label: t(ctx, "Clean up"), Path: "leave", Vals: vals("service", name), Primary: true}}})
	}
	for _, o := range d.Owned {
		if o.Broken != "" {
			lost = append(lost, NeedView{Tone: "bad", Chip: t(ctx, "Disconnected"), Title: o.Name,
				Text: t(ctx, "{reason} Senders are paused until you reconnect.", "reason", unpack(ctx, o.Broken)),
				Actions: []NeedAction{{Label: t(ctx, "Reconnect"), Path: "reconnect", Vals: vals("service", o.Name), Primary: true,
					Confirm: t(ctx, "Reconnecting creates a new receiver key in your Immich account.")}}})
		}
		if o.BridgeStopped != "" {
			lost = append(lost, NeedView{Tone: "bad", Chip: t(ctx, "Stopped"), Title: o.Name, Text: o.BridgeStopped})
		}
		if o.HasBridge && o.BridgeState != "ok" {
			tone, chip := "warn", t(ctx, "Other server")
			if o.BridgeState == "revoked" {
				tone = "bad"
			}
			lost = append(lost, NeedView{Tone: tone, Chip: chip, Title: t(ctx, "{service}: {name} on another server", "service", o.Name, "name", o.BridgeName),
				Text: o.BridgeNote + " " + t(ctx, "Photos wait, still tagged, until it works again."),
				Actions: []NeedAction{{Label: t(ctx, "Remove"), Path: "services/bridge/remove", Vals: vals("service", o.Name),
					Confirm: t(ctx, "Stop sending {name} to the receiver on another server? Photos waiting for it then go to you alone. What already arrived there stays there.", "name", o.Name)}}})
		}
		for _, r := range o.Requests {
			ask = append(ask, NeedView{Tone: "warn", Chip: t(ctx, "Request"), Title: t(ctx, "{name} wants to send to {service}", "name", r.Name, "service", o.Name),
				Text: t(ctx, "{email} asks to send. You decide.", "email", r.Email),
				Actions: []NeedAction{
					{Label: t(ctx, "Approve"), Path: "decide", Vals: decideVals(o.Name, r.ID, "approve"), Primary: true, Key: "A"},
					{Label: t(ctx, "Decline"), Path: "decide", Vals: decideVals(o.Name, r.ID, "decline"), Key: "D"}}})
		}
		if o.Paused {
			held = append(held, NeedView{Tone: "warn", Chip: t(ctx, "Paused"), Title: o.Name,
				Text:    t(ctx, "You paused this service: nothing is handed over to you, and photos senders tag stay tagged, until you resume it."),
				Actions: []NeedAction{{Label: t(ctx, "Resume service"), Path: "services/pause", Vals: pauseVals(o.Name, false), Primary: true}}})
		}
		for _, m := range o.Members {
			if m.AtLimit {
				held = append(held, NeedView{Tone: "warn", Chip: t(ctx, "Limit"), Title: t(ctx, "{name} reached the daily limit for {service}", "name", m.Name, "service", o.Name),
					Text: t(ctx, "Their next photos wait, still tagged, and go on their own as the day moves on. You can raise the limit in the service's card below.")})
			}
		}
	}
	for _, s := range d.Services {
		if s.Role != "sender" {
			continue
		}
		switch {
		case s.MinePaused:
			held = append(held, NeedView{Tone: "warn", Chip: t(ctx, "Paused"), Title: s.Name,
				Text:    t(ctx, "You paused sending. Photos you tag stay tagged and are handed over when you resume."),
				Actions: []NeedAction{{Label: t(ctx, "Resume sending"), Path: "pause", Vals: pauseVals(s.Name, false), Primary: true}}})
		case s.Hold != "":
			held = append(held, NeedView{Tone: "warn", Chip: t(ctx, "Paused"), Title: s.Name,
				Text: t(ctx, "{hold} Photos you tag stay tagged and are handed over when it resumes.", "hold", s.Hold)})
		}
		if s.Waiting != "" {
			held = append(held, NeedView{Tone: "warn", Chip: t(ctx, "Limit"), Title: s.Name, Text: s.Waiting})
		}
	}
	return append(append(lost, ask...), held...)
}

func (w *Server) dismissGuide(rw http.ResponseWriter, r *http.Request) {
	me := w.guarded(rw, r)
	if me == nil {
		return
	}
	if w.Hub == nil {
		http.Error(rw, t(r.Context(), "Nothing to remember it in."), http.StatusNotFound)
		return
	}
	if err := w.Hub.DismissGuide(r.Context(), me.user.ID); err != nil {
		w.Log.Warn("could not hide the guide", "err", err)
		http.Error(rw, t(r.Context(), "Could not do that: {reason}", "reason", t(r.Context(), err.Error())), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("HX-Refresh", "true")
	rw.WriteHeader(http.StatusNoContent)
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
		Notice(t(r.Context(), "Sign in to Immich first"),
			t(r.Context(), "This invitation needs your Immich login. Sign in to Immich in this browser, then open the link again."), false).Render(r.Context(), rw)
		return
	}
	token := r.PathValue("token")
	var offer *hub.Offer
	err := hub.ErrNoInvite
	if w.Hub != nil {
		offer, err = w.Hub.InviteOffer(r.Context(), me.user.ID, token)
	}
	if errors.Is(err, hub.ErrAlready) {
		// Joining refreshes this very page, so the page must lead to the dashboard rather than an error.
		// The link is relative so it also works behind a path prefix.
		// (http.Redirect would turn it into an absolute path and lose the prefix, so set the header directly.)
		rw.Header().Set("Location", "../")
		rw.WriteHeader(http.StatusSeeOther)
		return
	}
	if err != nil {
		rw.WriteHeader(http.StatusNotFound)
		Notice(t(r.Context(), "Invitation"), t(r.Context(), "This invitation is not valid any more. Ask the person who sent it for a new link."), true).Render(r.Context(), rw)
		return
	}
	d := w.dashboardData(r.Context(), me)
	d.Invited = &OfferView{Name: offer.Service.Label(), Tag: offer.Service.Tag, Owner: ownerName(offer.Service), State: offer.State, Invite: token, Pool: offer.Service.Pool}
	if offer.Service.Pool && !offer.Service.EndsAt.IsZero() {
		d.Invited.Ends = loc(r.Context()).Stamp(offer.Service.EndsAt.Local())
	}
	rw.Header().Set("Cache-Control", "no-store")
	InvitePage(d).Render(r.Context(), rw)
}

func (w *Server) dashboard(rw http.ResponseWriter, r *http.Request) {
	me, code, _ := w.authenticate(r)
	if me == nil {
		rw.WriteHeader(code)
		Notice(t(r.Context(), "Sign in to Immich first"),
			t(r.Context(), "This page uses your Immich login. Sign in to Immich in this browser, then reload this page."), false).Render(r.Context(), rw)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	if w.Hub != nil && w.Hub.SelfService() { // keys made before stacks were handed over get the two permissions they need
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			w.Hub.EnsureKeyPermissions(ctx, me.sess, me.user)
		}()
	}
	ctx := context.WithValue(context.WithValue(r.Context(), adminKey{}, me.user.IsAdmin), tabsKey{}, true)
	Dashboard(w.dashboardData(ctx, me)).Render(ctx, rw)
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
// pendingIDs are the photos a sender has waiting for a service: the ones tagged for it, and any in the album they
// linked (each counted once even if it is also tagged).
func (w *Server) pendingIDs(ctx context.Context, svc *syncer.Service, acct *syncer.Account, user uuid.UUID) []string {
	ids := w.tagged(ctx, acct, svc.Tag)
	if acct.WatchAlbum != uuid.Nil {
		seen := map[string]bool{}
		for _, id := range ids {
			seen[id] = true
		}
		if pending, err := syncer.AlbumPending(ctx, svc, acct, user); err == nil {
			for _, a := range pending {
				if id := a.Id.String(); !seen[id] {
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}

// arrivedCount is what a receiver holds from a service. When arrivals go to an album, it counts what the album
// holds (album = true); if the album cannot be found (the key may not be allowed to see albums), arrivals are
// tagged inbox instead, so those are counted.
func (w *Server) arrivedCount(ctx context.Context, svc *syncer.Service, acct *syncer.Account) (n int, album bool) {
	if svc.Album != "" {
		if id, count, err := acct.API.FindAlbum(ctx, svc.Album); err == nil && id != nil {
			return count, true
		}
	}
	return len(w.tagged(ctx, acct, svc.InboxTag)), false
}

var sumID = regexp.MustCompile(`^sum-[a-z]+-[0-9]+$`)

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
		rw.Header().Set("Cache-Control", "no-store")
		sum := r.URL.Query().Get("sum")
		if !sumID.MatchString(sum) {
			sum = "" // only the ids this page makes are accepted
		}
		if role == "receiver" {
			n, album := w.arrivedCount(r.Context(), svc, acct)
			if album {
				Count(n, "album", nil, sum).Render(r.Context(), rw)
			} else {
				Count(n, role, nil, sum).Render(r.Context(), rw)
			}
			return
		}
		ids := w.pendingIDs(r.Context(), svc, acct, me.user.ID)
		var thumbs []string
		if w.PublicImmichURL != "" {
			for _, id := range ids[:min(len(ids), 6)] {
				thumbs = append(thumbs, strings.TrimRight(w.PublicImmichURL, "/")+"/api/assets/"+id+"/thumbnail?size=thumbnail")
			}
		}
		Count(len(ids), role, thumbs, sum).Render(r.Context(), rw)
		return
	}
	http.Error(rw, "no such service for you", http.StatusNotFound)
}

// sceneFragment is the live scene again, asked for when the page learns it missed something or the stream came back.
func (w *Server) sceneFragment(rw http.ResponseWriter, r *http.Request) {
	me := w.requireMember(rw, r)
	if me == nil {
		return
	}
	d := DashboardData{Services: w.servicesFor(r.Context(), me)}
	if w.Hub != nil && w.Hub.SelfService() {
		d.Owned = w.ownedViews(r.Context(), me)
	}
	rw.Header().Set("Cache-Control", "no-store")
	Scene(sceneOf(r.Context(), &d, w.S.Current())).Render(r.Context(), rw)
}

// numbersFragment is the Overview's three numbers and the week's chart, asked for when the page opens and again
// whenever a pass finishes.
func (w *Server) numbersFragment(rw http.ResponseWriter, r *http.Request) {
	me := w.requireMember(rw, r)
	if me == nil {
		return
	}
	n := NumbersView{}
	for _, svc := range w.S.Current() {
		if a := me.account(svc, "sender"); a != nil {
			n.Sender = true
			n.Waiting += len(w.pendingIDs(r.Context(), svc, a, me.user.ID))
		}
		if a := me.account(svc, "receiver"); a != nil {
			n.Receiver = true
			c, _ := w.arrivedCount(r.Context(), svc, a)
			n.Inbox += c
		}
	}
	if w.Hub != nil {
		if days, counts, err := w.Hub.Week(r.Context(), me.user.ID, time.Now()); err == nil {
			for i := range days {
				n.Days[i] = DayBar{Label: loc(r.Context()).Weekday(days[i]), N: counts[i]}
				n.Week += counts[i]
			}
		}
	}
	rw.Header().Set("Cache-Control", "no-store")
	Numbers(n).Render(r.Context(), rw)
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
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	// people who take part in a service, and the Immich administrator (who sees everything), may listen
	if !me.user.IsAdmin && len(w.servicesFor(r.Context(), me)) == 0 {
		http.Error(rw, "you do not take part in any service", http.StatusForbidden)
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
	feed, closeFeed := w.Bus.Subscribe(me.user.ID, me.user.IsAdmin)
	defer closeFeed()
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
		case ev := <-feed.C:
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(rw, "event: live\ndata: %s\n\n", b)
		case <-keepalive.C:
			fmt.Fprint(rw, ": keepalive\n\n")
		}
		fl.Flush()
	}
}

// tagged lists the photos that carry the tag in the account (the app's own key does the looking).
func (w *Server) tagged(ctx context.Context, a *syncer.Account, tag string) []string {
	id, err := a.API.FindTag(ctx, tag, false)
	if err != nil || id == nil {
		return nil
	}
	assets, err := a.API.Search(ctx, immich.MetadataSearchDto{TagIds: &[]uuid.UUID{*id}})
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(assets))
	for _, x := range assets {
		ids = append(ids, x.Id.String())
	}
	return ids
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
	if !w.limiter.allow(me.user.ID) {
		http.Error(rw, t(r.Context(), "Too many changes in a minute; try again shortly."), http.StatusTooManyRequests)
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
	rw.Write([]byte(t(r.Context(), "Sync started")))
}

// albumChoice lists the person's own albums for linking one to a service they send to. It is loaded when they
// ask, so the page does not list albums every time it opens.
func (w *Server) albumChoice(rw http.ResponseWriter, r *http.Request) {
	me := w.requireMember(rw, r)
	if me == nil {
		return
	}
	name := r.URL.Query().Get("service")
	var svc *syncer.Service
	for _, s := range w.S.Current() {
		if s.Label() == name && me.account(s, "sender") != nil {
			svc = s
		}
	}
	if svc == nil {
		http.Error(rw, "you do not send to that service", http.StatusNotFound)
		return
	}
	albums, err := me.sess.OwnedAlbums(r.Context())
	if err != nil {
		w.Log.Warn("could not list albums", "user", me.user.Email, "err", err)
		http.Error(rw, "could not list your albums: "+err.Error(), http.StatusBadGateway)
		return
	}
	opts := make([]AlbumOption, 0, len(albums))
	for _, a := range albums {
		opts = append(opts, AlbumOption{ID: a.ID.String(), Label: albumLabel(r.Context(), a.Name, a.Count)})
	}
	rw.Header().Set("Cache-Control", "no-store")
	AlbumChoice(name, opts, svc.CopiesFor(me.account(svc, "sender"))).Render(r.Context(), rw)
}

// linkAlbum and unlinkAlbum answer like joining and leaving: the page reloads on success.
func (w *Server) linkAlbum(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "linked an album", func(ctx context.Context, me *member, service string) error {
		id, err := uuid.Parse(r.PostFormValue("album"))
		if err != nil {
			return hub.ErrNoAlbum
		}
		return w.Hub.LinkAlbum(ctx, me.sess, me.user, service, id)
	})
}

func (w *Server) unlinkAlbum(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "unlinked an album", func(ctx context.Context, me *member, service string) error {
		return w.Hub.UnlinkAlbum(ctx, me.user, service)
	})
}

// enableTags switches Tags on in the person's own Immich account, when they press the button for it. It
// acts with their session and changes nothing but that one setting.
func (w *Server) enableTags(rw http.ResponseWriter, r *http.Request) {
	me := w.guarded(rw, r)
	if me == nil {
		return
	}
	if err := me.sess.EnableTags(r.Context()); err != nil {
		w.Log.Warn("could not switch tags on", "user", me.user.Email, "err", err)
		rw.Header().Set("HX-Retarget", "#flash") // keep the button, show the problem in the message line
		rw.Header().Set("HX-Reswap", "innerHTML")
		rw.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(rw, t(r.Context(), "Could not switch Tags on: {reason}. You can do it in Immich: Account Settings, Features, Tags.", "reason", err.Error()))
		return
	}
	w.Log.Info("tags switched on", "user", me.user.Email)
	rw.Header().Set("HX-Trigger", "tags-on") // the page unlocks itself (app.js)
	rw.Write([]byte(t(r.Context(), "Tags are on. Now refresh the Immich tab or window (on the phone, close and reopen the app); until then the tag options do not appear.")))
}

func (w *Server) join(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "joined", func(ctx context.Context, me *member, service string) error {
		return w.Hub.JoinWith(ctx, me.sess, me.user, service, r.PostFormValue("invite"), r.PostFormValue("mode"), r.PostFormValue("pool") == "1")
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
		http.Error(rw, t(r.Context(), "Joining from this page is not switched on."), http.StatusNotFound)
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
		fmt.Fprint(rw, t(r.Context(), "Could not do that: {reason}", "reason", t(r.Context(), err.Error())))
		return
	}
	rw.Header().Set("HX-Refresh", "true")
	rw.WriteHeader(http.StatusNoContent)
}

func (w *Server) createService(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "created", func(ctx context.Context, me *member, _ string) error {
		days, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("expiry")))
		photos, mb, err := limitFields(r)
		if err != nil {
			return err
		}
		poolDays, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("poolends")))
		return w.Hub.CreateService(ctx, me.sess, me.user, hub.CreateParams{
			Name: r.PostFormValue("name"), Tag: r.PostFormValue("tag"), Policy: r.PostFormValue("policy"),
			Visibility: r.PostFormValue("visibility"), InboxTag: r.PostFormValue("inbox"), ExpiryDays: days, Album: r.PostFormValue("album"),
			LimitPhotos: photos, LimitMB: mb, Pool: r.PostFormValue("pool") == "1", EndsInDays: poolDays,
		})
	})
}

// limitFields reads the daily limits of a form: empty means none, anything else must be a whole number.
func limitFields(r *http.Request) (photos, mb int, err error) {
	read := func(name string) (int, error) {
		v := strings.TrimSpace(r.PostFormValue(name))
		if v == "" {
			return 0, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, hub.ErrBadLimit
		}
		return n, nil
	}
	if photos, err = read("photos"); err != nil {
		return
	}
	mb, err = read("mb")
	return
}

func (w *Server) setLimits(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "limits", func(ctx context.Context, me *member, service string) error {
		photos, mb, err := limitFields(r)
		if err != nil {
			return err
		}
		return w.Hub.SetLimits(ctx, me.user, service, photos, mb)
	})
}

// setRules reads the owner's form of quiet hours and what the service accepts.
func (w *Server) setRules(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "rules", func(ctx context.Context, me *member, service string) error {
		num := func(name string) int {
			n, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue(name)))
			if err != nil {
				return 0
			}
			return n
		}
		var q syncer.Quiet
		from, to := strings.TrimSpace(r.PostFormValue("from")), strings.TrimSpace(r.PostFormValue("to"))
		if from != "" || to != "" {
			var err1, err2 error
			q.From, err1 = syncer.ParseClock(from)
			q.To, err2 = syncer.ParseClock(to)
			if err1 != nil || err2 != nil {
				return hub.ErrBadRules
			}
		}
		q.LargeMB = num("largemb")
		a := syncer.Accept{MinRating: num("rating"), PhotosOnly: r.PostFormValue("photos") == "1",
			Orientation: strings.TrimSpace(r.PostFormValue("orientation")), MaxMB: num("maxmb")}
		return w.Hub.SetRules(ctx, me.user, service, q, a)
	})
}

// setLifetime reads how many days the copies that arrive live.
func (w *Server) setLifetime(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "lifetime", func(ctx context.Context, me *member, service string) error {
		v := strings.TrimSpace(r.PostFormValue("days"))
		days := 0
		if v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return hub.ErrBadLifetime
			}
			days = n
		}
		return w.Hub.SetLifetime(ctx, me.sess, me.user, service, days)
	})
}

// takeBack undoes the signed-in sender's last hand-over to a service. When the receiver had already filed some of the
// photos in an album, those stay, and the page says so instead of reloading.
func (w *Server) takeBack(rw http.ResponseWriter, r *http.Request) {
	me := w.guarded(rw, r)
	if me == nil {
		return
	}
	if w.Hub == nil || !w.Hub.SelfService() {
		http.Error(rw, t(r.Context(), "Joining from this page is not switched on."), http.StatusNotFound)
		return
	}
	service := r.PostFormValue("service")
	res, err := w.Hub.TakeBack(r.Context(), me.user, service)
	if err != nil {
		status := http.StatusInternalServerError
		if hub.IsUserError(err) {
			status = http.StatusConflict
		}
		w.Log.Warn("taking a hand-over back failed", "user", me.user.Email, "service", service, "err", err)
		rw.Header().Set("HX-Reswap", "innerHTML")
		rw.WriteHeader(status)
		fmt.Fprint(rw, t(r.Context(), "Could not do that: {reason}", "reason", t(r.Context(), err.Error())))
		return
	}
	if res.Kept > 0 || res.RemoteCannot > 0 {
		rw.Header().Set("HX-Reswap", "innerHTML")
		msg := t(r.Context(), "{n} taken back.", "n", res.Taken)
		if res.Kept > 0 {
			msg += " " + t(r.Context(), "{kept} stayed with the receiver, who had filed them in an album.", "kept", res.Kept)
		}
		if res.RemoteCannot > 0 {
			msg += " " + t(r.Context(), "{n} on another server stayed there: the receiver's key cannot move photos to their trash.", "n", res.RemoteCannot)
		}
		fmt.Fprint(rw, msg)
		return
	}
	rw.Header().Set("HX-Refresh", "true")
	rw.WriteHeader(http.StatusNoContent)
}

func (w *Server) pauseService(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "paused", func(ctx context.Context, me *member, service string) error {
		return w.Hub.SetServicePaused(ctx, me.user, service, r.PostFormValue("paused") == "1")
	})
}

func (w *Server) pauseSending(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "paused", func(ctx context.Context, me *member, service string) error {
		return w.Hub.SetMemberPaused(ctx, me.user, service, r.PostFormValue("paused") == "1")
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
