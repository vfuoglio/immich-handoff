package web

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/backup"
	"github.com/vfuoglio/immich-handoff/internal/doctor"
	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// The administrator's page. "Administrator" means what Immich says about the signed-in person's own session:
// the app keeps no list of its own. Everyone else, signed in or not, gets 404, so the page does not show itself.

func notFound(rw http.ResponseWriter) { http.NotFound(rw, nil) }

// adminOnly is for reading: the signed-in person must be an Immich administrator.
func (w *Server) adminOnly(rw http.ResponseWriter, r *http.Request) *member {
	me, _, _ := w.authenticate(r)
	if me == nil || !me.user.IsAdmin || w.Hub == nil {
		notFound(rw)
		return nil
	}
	return me
}

// adminGuarded is for changes: the same CSRF header and rate limit as every change, then the admin check.
func (w *Server) adminGuarded(rw http.ResponseWriter, r *http.Request) *member {
	me := w.guarded(rw, r)
	if me == nil {
		return nil
	}
	if !me.user.IsAdmin || w.Hub == nil {
		notFound(rw)
		return nil
	}
	return me
}

func (w *Server) adminPage(rw http.ResponseWriter, r *http.Request) {
	me := w.adminOnly(rw, r)
	if me == nil {
		return
	}
	d := AdminData{User: me.user.Name, SelfOn: w.Hub.SelfService(), Backup: w.backupSummary(r.Context()), Notify: w.notifyViews(r.Context())}
	services, leftover, err := w.Hub.Overview(r.Context())
	if err != nil {
		w.Log.Error("admin overview", "err", err)
		http.Error(rw, "could not read the services", http.StatusInternalServerError)
		return
	}
	member := func(m hub.AdminMember) AdminMemberView {
		return AdminMemberView{LinkID: m.LinkID, Name: m.Name, Email: m.Email, State: m.State, Note: m.Note, Album: m.Album, Copy: m.Copy, FromConfig: m.FromConfig, Paused: m.Paused}
	}
	for _, s := range services {
		d.Senders += len(s.Members)
		if s.Broken != "" || s.Paused {
			d.Attention++
		}
		for _, m := range s.Members {
			if m.State != "active" || m.Paused {
				d.Attention++
			}
		}
		v := AdminServiceView{Name: s.Name, Tag: s.Tag, Policy: s.Policy, Album: s.Album, InviteOnly: s.InviteOnly, Copy: s.Copy,
			FromConfig: s.FromConfig, Receivers: s.Receivers, Broken: s.Broken, Paused: s.Paused, Limit: limitText(r.Context(), s.LimitPhotos, s.LimitMB)}
		if s.OwnerName != "" {
			v.Owner = s.OwnerName + " (" + s.OwnerEmail + ")"
		}
		for _, m := range s.Members {
			v.Members = append(v.Members, member(m))
		}
		d.Services = append(d.Services, v)
	}
	for _, m := range leftover {
		d.Leftover = append(d.Leftover, member(m))
	}
	d.Attention += len(leftover)
	if events, err := w.Hub.RecentEvents(r.Context(), 30); err == nil {
		for _, e := range events {
			d.Activity = append(d.Activity, AdminActivityView{When: loc(r.Context()).Stamp(e.At.Local()), Service: e.Service, Text: unpack(r.Context(), e.Text), Tone: activityTone(e.Kind)})
		}
	}
	rw.Header().Set("Cache-Control", "no-store")
	ctx := context.WithValue(r.Context(), adminKey{}, true)
	AdminPage(d).Render(ctx, rw)
}

// downloadGate lets one backup out at a time and no more often than a set gap, for the whole app: this is the
// one action that takes data away, so a stolen session can take it only slowly.
type downloadGate struct {
	mu   sync.Mutex
	last time.Time
}

// take reserves the next download, or says how long until it is allowed. give returns the reservation when the
// copy could not be made, so a failure does not use up the turn.
func (g *downloadGate) take(gap time.Duration) (wait time.Duration, give func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if left := gap - time.Since(g.last); !g.last.IsZero() && left > 0 {
		return left, nil
	}
	before := g.last
	g.last = time.Now()
	return 0, func() { g.mu.Lock(); g.last = before; g.mu.Unlock() }
}

// adminBackupDownload hands an administrator a copy of the data file. It exists only when the person who runs the
// app switched it on (HANDOFF_ADMIN_BACKUP_DOWNLOAD=true); otherwise, and for everyone who is not an administrator,
// it is "not found". It is a POST with the CSRF header, never a link, and every download is written to the recent
// events and sent to NOTIFY_URL.
func (w *Server) adminBackupDownload(rw http.ResponseWriter, r *http.Request) {
	if !w.BackupDownload || w.Backups == nil {
		notFound(rw)
		return
	}
	me := w.adminGuarded(rw, r)
	if me == nil {
		return
	}
	gap := w.DownloadGap
	if gap <= 0 {
		gap = 10 * time.Minute
	}
	wait, give := w.gate.take(gap)
	if give == nil {
		rw.Header().Set("Retry-After", fmt.Sprint(int(wait.Seconds())+1))
		http.Error(rw, t(r.Context(), "A backup was downloaded a moment ago. To limit what a stolen session could take, only one is handed out every few minutes. Try again shortly."), http.StatusTooManyRequests)
		return
	}
	f, name, cleanup, err := w.Backups.Download(r.Context(), me.user.Name+" ("+me.user.Email+")")
	if err != nil {
		give()
		w.Log.Error("backup download", "err", err)
		http.Error(rw, t(r.Context(), "The backup could not be made. See the app's log."), http.StatusInternalServerError)
		return
	}
	defer cleanup()
	size := int64(-1)
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	}
	h := rw.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	h.Set("Cache-Control", "no-store")
	if size >= 0 {
		h.Set("Content-Length", fmt.Sprint(size))
	}
	if _, err := io.Copy(rw, f); err != nil {
		w.Log.Warn("backup download interrupted", "err", err)
	}
}

// backupSummary is what the administrator's page says about backups.
func (w *Server) backupSummary(ctx context.Context) AdminBackup {
	b := AdminBackup{Download: w.BackupDownload}
	m := w.Backups
	if m == nil {
		return b
	}
	b.Has, b.Folder, b.Keep = true, m.Dir, m.Keep
	if m.Every > 0 {
		b.Every = everyText(m.Every)
	}
	if files, err := backup.List(m.Dir); err == nil && len(files) > 0 {
		b.Count = len(files)
		b.Last = files[0].Name
		b.LastWhen = loc(ctx).Stamp(files[0].When.Local())
	}
	if j, ok := m.Latest(); ok && j.State == backup.Failed {
		b.Failed = j.Err
	}
	return b
}

// everyText is the schedule as it is written in BACKUP_EVERY ("24h"): the same in every language.
func everyText(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return d.String()
}

// adminCheck runs the setup check that `immich-handoff doctor` runs, against the services the app has now.
func (w *Server) adminCheck(rw http.ResponseWriter, r *http.Request) {
	if w.adminGuarded(rw, r) == nil {
		return
	}
	probe, err := immich.New(w.BaseURL, immich.Auth{}, nil)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	in := doctor.Input{
		Env: os.Getenv, BaseURL: w.BaseURL, Probe: probe, Services: w.S.Current(),
		Hook: syncer.Webhook{URL: w.WebhookURL, Header: w.WebhookHeader, Secret: w.WebhookSecret},
	}
	var out bytes.Buffer
	doctor.Run(ctx, in, &out)
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Write([]byte(html.EscapeString(out.String())))
}

// adminChange runs one removal and answers like the other changes: refresh on success, the reason on a refusal.
func (w *Server) adminChange(rw http.ResponseWriter, r *http.Request, what string, act func(context.Context, *member) error) {
	me := w.adminGuarded(rw, r)
	if me == nil {
		return
	}
	if err := act(r.Context(), me); err != nil {
		status := http.StatusInternalServerError
		if hub.IsUserError(err) || err == hub.ErrNotRemovable || err == hub.ErrNoSuchEntry {
			status = http.StatusConflict
		}
		w.Log.Warn("admin change failed", "action", what, "admin", me.user.Email, "err", err)
		rw.Header().Set("HX-Reswap", "innerHTML")
		rw.WriteHeader(status)
		fmt.Fprint(rw, t(r.Context(), "Could not do that: {reason}", "reason", t(r.Context(), err.Error())))
		return
	}
	rw.Header().Set("HX-Refresh", "true")
	rw.WriteHeader(http.StatusNoContent)
}

func (w *Server) adminRemoveService(rw http.ResponseWriter, r *http.Request) {
	w.adminChange(rw, r, "remove service", func(ctx context.Context, me *member) error {
		return w.Hub.AdminRemoveService(ctx, me.user, r.PostFormValue("service"))
	})
}

func (w *Server) adminRemoveMember(rw http.ResponseWriter, r *http.Request) {
	w.adminChange(rw, r, "remove member", func(ctx context.Context, me *member) error {
		return w.Hub.AdminRemoveMember(ctx, me.user, r.PostFormValue("link"))
	})
}

// notifyViews lists where messages for whoever runs the app go: names and kinds, never an address.
func (w *Server) notifyViews(ctx context.Context) []NotifyView {
	var out []NotifyView
	for _, d := range w.Notifier.Describe() {
		v := NotifyView{Name: d.Name, Format: d.Format, People: d.People, Kinds: t(ctx, "every kind")}
		if len(d.Kinds) > 0 {
			names := make([]string, len(d.Kinds))
			for i, k := range d.Kinds {
				names[i] = string(k)
			}
			v.Kinds = strings.Join(names, ", ")
		}
		out = append(out, v)
	}
	return out
}

// adminNotifyTest sends one test message to every destination and shows how each answered. It is a POST with the
// cross-site header, for administrators only, and not more than once every ten seconds.
func (w *Server) adminNotifyTest(rw http.ResponseWriter, r *http.Request) {
	if w.adminGuarded(rw, r) == nil {
		return
	}
	ctx := r.Context()
	if wait, _ := w.testGate.take(10 * time.Second); wait > 0 {
		rw.Header().Set("Retry-After", fmt.Sprint(int(wait.Seconds())+1))
		http.Error(rw, t(ctx, "A test was just sent. Try again in a few seconds."), http.StatusTooManyRequests)
		return
	}
	tctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var results []NotifyResultView
	for _, res := range w.Notifier.Test(tctx) {
		v := NotifyResultView{Name: res.Name, Format: res.Format, People: res.People, Err: res.Err}
		results = append(results, v)
	}
	rw.Header().Set("Cache-Control", "no-store")
	NotifyResults(results).Render(ctx, rw)
}
