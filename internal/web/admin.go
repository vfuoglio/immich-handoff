package web

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net/http"
	"os"
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
	d := AdminData{User: me.user.Name, SelfOn: w.Hub.SelfService(), Backup: w.backupSummary(r.Context())}
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

// backupSummary is what the administrator's page says about backups.
func (w *Server) backupSummary(ctx context.Context) AdminBackup {
	b := AdminBackup{}
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
