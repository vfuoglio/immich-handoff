package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// maxDropFilesPerRequest keeps one form from carrying an endless list of files.
const maxDropFilesPerRequest = 100

type guestKey struct{}

// isGuest is whether the page is one a guest sees (no account, no tabs, nothing about the library).
func isGuest(ctx context.Context) bool { v, _ := ctx.Value(guestKey{}).(bool); return v }

// dropRoute is whether the path belongs to a drop box's guest pages, which need no Immich login.
func dropRoute(path string) bool { return strings.HasPrefix(path, "/drop/") }

// clientAddr is the address a guest comes from. Behind a reverse proxy every request seems to come from the proxy,
// so the administrator may name the header in which it puts the real one (DROPBOX_CLIENT_IP_HEADER, for example
// X-Forwarded-For or X-Real-IP); the last address in it is used, which is the one the nearest proxy added.
func (w *Server) clientAddr(r *http.Request) string {
	if h := w.ClientIPHeader; h != "" {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			if i := strings.LastIndex(v, ","); i >= 0 {
				v = strings.TrimSpace(v[i+1:])
			}
			return v
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// dropClosed is the one answer for a link that is unknown, revoked, paused or past its end.
func (w *Server) dropClosed(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(http.StatusNotFound)
	Notice(t(r.Context(), "This link is not open"), t(r.Context(), "This link does not take photos. It may have ended, or it may not be right. Ask the person who sent it."), true).Render(r.Context(), rw)
}

func (w *Server) dropPage(rw http.ResponseWriter, r *http.Request) {
	if w.Hub == nil {
		w.dropClosed(rw, r)
		return
	}
	b, err := w.Hub.OpenDropBox(r.Context(), r.PathValue("token"))
	if err != nil {
		w.dropClosed(rw, r)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	DropPage(DropPageView{Token: b.Token, Name: b.Name, Message: b.Message, Password: b.Password != "", PhotosOnly: b.PhotosOnly, Approve: b.Approve, Limits: dropLimitText(r.Context(), b)}).Render(r.Context(), rw)
}

// dropUpload takes what a guest sent. The files are read from the request one by one and streamed on to Immich, so
// nothing is kept in memory or on disk; a password, when there is one, must come before the files in the form.
func (w *Server) dropUpload(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if w.Hub == nil {
		w.dropClosed(rw, r)
		return
	}
	token := r.PathValue("token")
	b, err := w.Hub.OpenDropBox(ctx, token)
	if err != nil {
		w.dropClosed(rw, r)
		return
	}
	who := hub.DropWho(token, w.clientAddr(r))
	if err := w.Hub.AllowDrop(ctx, b, who); err != nil {
		rw.Header().Set("Retry-After", "600")
		rw.WriteHeader(http.StatusTooManyRequests)
		Notice(t(ctx, "Please wait a little"), t(ctx, "Too many photos or requests came from your connection. Try again in ten minutes."), true).Render(ctx, rw)
		return
	}
	defer w.Hub.DoneDrop(b)

	if b.MaxMB > 0 { // what was asked can not be more than the room left, whatever the form says
		r.Body = http.MaxBytesReader(rw, r.Body, int64(b.MaxMB)<<20-b.Bytes+(8<<20))
	}
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(rw, "send the form as multipart/form-data", http.StatusBadRequest)
		return
	}
	unlocked := b.Password == ""
	var lines []DropResultLine
	for len(lines) < maxDropFilesPerRequest {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		switch {
		case part.FormName() == "password":
			given, _ := io.ReadAll(io.LimitReader(part, 200))
			unlocked = w.Hub.CheckDropPassword(ctx, b, who, string(given))
		case part.FormName() == "files" && part.FileName() != "":
			if !unlocked {
				rw.WriteHeader(http.StatusForbidden)
				Notice(t(ctx, "Wrong password"), t(ctx, "The password is not right. Go back and try again."), true).Render(ctx, rw)
				return
			}
			outcome, err := w.Hub.ReceiveDropFile(ctx, b.ID, part.FileName(), part, who)
			text := dropReason(ctx, err)
			if err == nil && outcome == hub.DropHeld {
				text = t(ctx, "Received, waiting for approval")
			} else if err == nil && outcome == hub.DropDuplicate {
				text = t(ctx, "Already there, thank you")
			}
			lines = append(lines, DropResultLine{Name: part.FileName(), Text: text, OK: err == nil})
			if errors.Is(err, hub.ErrDropFull) {
				for len(lines) < maxDropFilesPerRequest { // the rest cannot be taken either; do not read them
					if part, err = mr.NextPart(); err != nil {
						break
					}
					if part.FileName() != "" {
						lines = append(lines, DropResultLine{Name: part.FileName(), Text: dropReason(ctx, hub.ErrDropFull)})
					}
				}
			}
		}
	}
	if len(lines) == 0 && !unlocked {
		rw.WriteHeader(http.StatusForbidden)
		Notice(t(ctx, "Wrong password"), t(ctx, "The password is not right. Go back and try again."), true).Render(ctx, rw)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	DropDone(b.Name, "drop/"+token, lines).Render(ctx, rw)
}

// dropReason is what a guest is told about one file.
func dropReason(ctx context.Context, err error) string {
	switch {
	case err == nil:
		return t(ctx, "Received")
	case errors.Is(err, hub.ErrDropType):
		return t(ctx, "Not a photo or video this box takes")
	case errors.Is(err, hub.ErrDropTooBig):
		return t(ctx, "Too large")
	case errors.Is(err, hub.ErrDropFull):
		return t(ctx, "This box is full")
	case errors.Is(err, hub.ErrDropEmpty):
		return t(ctx, "Empty file")
	case errors.Is(err, hub.ErrDropGone):
		return t(ctx, "This box is closed")
	}
	return t(ctx, "Could not be saved, please try again")
}

func dropSummary(ctx context.Context, lines []DropResultLine) string {
	ok := 0
	for _, l := range lines {
		if l.OK {
			ok++
		}
	}
	switch {
	case len(lines) == 0:
		return t(ctx, "No file was sent.")
	case ok == len(lines):
		return n(ctx, "Your {n} file arrived.", "Your {n} files arrived.", ok)
	}
	return t(ctx, "{ok} of {all} files arrived. The others are marked below.", "ok", ok, "all", len(lines))
}

func dropChoose(ctx context.Context, photosOnly bool) string {
	if photosOnly {
		return t(ctx, "Choose your photos")
	}
	return t(ctx, "Choose your photos and videos")
}

func dropAccept(photosOnly bool) string {
	if photosOnly {
		return "image/*"
	}
	return "image/*,video/*"
}

func megabytes(b int64) string { return fmt.Sprintf("%.1f MB", float64(b)/(1<<20)) }

// dropLimitText says in words what a box takes.
func dropLimitText(ctx context.Context, b store.DropBox) string {
	var parts []string
	if b.MaxFileMB > 0 {
		parts = append(parts, t(ctx, "up to {mb} MB each", "mb", b.MaxFileMB))
	}
	if b.MaxFiles > 0 {
		parts = append(parts, n(ctx, "up to {n} file in all", "up to {n} files in all", b.MaxFiles))
	}
	if b.MaxMB > 0 {
		parts = append(parts, t(ctx, "up to {mb} MB in all", "mb", b.MaxMB))
	}
	if b.PhotosOnly {
		parts = append(parts, t(ctx, "photos only"))
	}
	if !b.ExpiresAt.IsZero() {
		parts = append(parts, t(ctx, "open until {when}", "when", loc(ctx).Stamp(b.ExpiresAt.Local())))
	}
	if b.Password != "" {
		parts = append(parts, t(ctx, "needs a password"))
	}
	if b.Approve {
		parts = append(parts, t(ctx, "files are looked at before they are kept"))
	}
	if len(parts) == 0 {
		return t(ctx, "There are no limits.")
	}
	return t(ctx, "Takes {limits}.", "limits", strings.Join(parts, ", "))
}

func dropState(b store.DropBox) string {
	switch {
	case b.State == store.DropRevoked:
		return "revoked"
	case b.Expired(time.Now()):
		return "expired"
	case b.State == store.DropPaused:
		return "paused"
	}
	return "open"
}

func dropStateLabel(ctx context.Context, state string) string {
	switch state {
	case "paused":
		return t(ctx, "Paused")
	case "expired":
		return t(ctx, "Ended")
	case "revoked":
		return t(ctx, "Revoked")
	}
	return t(ctx, "Taking photos")
}

func dropChips(ctx context.Context, b DropBoxView) []Chip {
	switch b.State {
	case "paused":
		return []Chip{{"warn", t(ctx, "Paused")}}
	case "expired":
		return []Chip{{"", t(ctx, "Ended")}}
	case "revoked":
		return []Chip{{"bad", t(ctx, "Revoked")}}
	}
	c := []Chip{{"ok", t(ctx, "Open")}}
	if b.Locked {
		c = append(c, Chip{"", t(ctx, "Password")})
	}
	return c
}

func dropCount(ctx context.Context, b DropBoxView) string {
	return n(ctx, "{n} file", "{n} files", b.Files) + " · " + b.Size
}

func dropTone(kind string) string {
	switch kind {
	case store.DropBlocked, store.DropRejected, store.DropPausedOn:
		return "warn"
	case store.DropRevokedK:
		return "bad"
	case store.DropCreated, store.DropResumed:
		return "ok"
	}
	return ""
}

// dropBoxViews is the person's drop boxes as the page shows them: their own, and the ones they help with.
func (w *Server) dropBoxViews(ctx context.Context, me *member) []DropBoxView {
	boxes, err := w.Hub.DropBoxes(ctx, me.user.ID)
	if err != nil {
		w.Log.Warn("could not list drop boxes", "err", err)
		return nil
	}
	var out []DropBoxView
	for _, a := range boxes {
		b := a.Box
		v := DropBoxView{ID: b.ID, Name: b.Name, Message: b.Message, State: dropState(b), Note: b.Note, Role: a.Role,
			Files: b.Files, Size: megabytes(b.Bytes), Limits: dropLimitText(ctx, b), Locked: b.Password != "",
			Album: b.Album, ExtraTag: b.ExtraTag, Approve: b.Approve, Owner: a.Role == "owner", Manage: a.Role != "view"}
		if v.State != "revoked" && v.Manage {
			v.Token = b.Token // a co-host who only views does not get the link
		}
		v.CanExtend = v.Manage && v.State != "revoked" && !b.ExpiresAt.IsZero()
		if !b.StartsAt.IsZero() && b.NotStarted(time.Now()) {
			v.Opens = loc(ctx).Stamp(b.StartsAt.Local())
		}
		if !b.ExpiresAt.IsZero() {
			v.Ends = loc(ctx).Stamp(b.ExpiresAt.Local())
		}
		if v.State != "revoked" {
			if sum, err := w.Hub.Store.DropSummaryOf(ctx, b.ID); err == nil {
				v.Summary = dropSummaryText(ctx, b, sum)
			}
		}
		if v.Manage && v.State != "revoked" {
			if list, n, err := w.Hub.DropPending(ctx, me.user.ID, b.ID, 24); err == nil {
				v.PendingN = n
				for _, p := range list {
					v.Pending = append(v.Pending, PendingView{ID: p.ID, Name: p.Name, Size: megabytes(p.Size), Image: p.Kind == "image"})
				}
			}
		} else if v.State != "revoked" {
			v.PendingN = w.Hub.DropPendingCount(ctx, b.ID)
		}
		for _, h := range a.Hosts {
			v.Hosts = append(v.Hosts, HostView{ID: h.UserID.String(), Name: h.Name, Email: h.Email, Manage: h.Role == store.HostManage})
		}
		if evs, err := w.Hub.DropEvents(ctx, me.user.ID, b.ID, 15); err == nil {
			for _, e := range evs {
				v.Log = append(v.Log, ActivityView{loc(ctx).Stamp(e.At.Local()), dropEventText(ctx, e), dropTone(e.Kind)})
			}
		}
		out = append(out, v)
	}
	return out
}

// dropSummaryText is what a box took, in a sentence: files, guests and the busiest hour.
func dropSummaryText(ctx context.Context, b store.DropBox, sum store.DropSummary) string {
	text := t(ctx, "{files} from {guests}, {size} in all.", "files", n(ctx, "{n} file", "{n} files", b.Files),
		"guests", n(ctx, "{n} guest", "{n} guests", sum.Guests), "size", megabytes(b.Bytes))
	if sum.BusiestFiles > 0 {
		if h, err := time.Parse("2006-01-02T15", sum.BusiestHour); err == nil {
			text += " " + t(ctx, "Busiest hour: {hour} ({files}).", "hour", loc(ctx).Stamp(h.Local()), "files", n(ctx, "{n} file", "{n} files", sum.BusiestFiles))
		}
	}
	return text
}

func dropEventText(ctx context.Context, e store.DropEvent) string {
	text := unpack(ctx, e.Text)
	if e.Who != "" {
		text += " (" + t(ctx, "guest {mark}", "mark", e.Who) + ")"
	}
	return text
}

// --- the owner's side ---

// clockField reads a date and time from a form ("2006-01-02T15:04", as a browser sends it), in the app's time zone.
func clockField(r *http.Request, name string) (time.Time, error) {
	v := strings.TrimSpace(r.PostFormValue(name))
	if v == "" {
		return time.Time{}, nil
	}
	tm, err := time.ParseInLocation("2006-01-02T15:04", v, time.Local)
	if err != nil {
		return tm, hub.ErrBadDropWhen
	}
	return tm, nil
}

func (w *Server) createDropBox(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "drop box created", func(ctx context.Context, me *member, _ string) error {
		num := func(name string) (int, error) {
			v := strings.TrimSpace(r.PostFormValue(name))
			if v == "" {
				return 0, nil
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return 0, hub.ErrBadDropLimit
			}
			return n, nil
		}
		var p hub.DropParams
		var err error
		for _, f := range []struct {
			name string
			into *int
		}{{"opendays", &p.Days}, {"filemb", &p.MaxFileMB}, {"mb", &p.MaxMB}, {"files", &p.MaxFiles}} {
			if *f.into, err = num(f.name); err != nil {
				return err
			}
		}
		if p.Opens, err = clockField(r, "opens"); err != nil {
			return err
		}
		if p.Closes, err = clockField(r, "closes"); err != nil {
			return err
		}
		p.Name, p.Message, p.Password = r.PostFormValue("name"), r.PostFormValue("message"), r.PostFormValue("password")
		p.PhotosOnly = r.PostFormValue("photos") == "1"
		p.Approve = r.PostFormValue("approve") == "1"
		p.Album, p.ExtraTag = r.PostFormValue("album"), r.PostFormValue("tag")
		_, err = w.Hub.CreateDropBox(ctx, me.sess, me.user, p)
		return err
	})
}

func (w *Server) pauseDropBox(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "drop box paused", func(ctx context.Context, me *member, id string) error {
		return w.Hub.SetDropBoxPaused(ctx, me.user, id, r.PostFormValue("paused") == "1")
	})
}

func (w *Server) revokeDropBox(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "drop box revoked", func(ctx context.Context, me *member, id string) error {
		return w.Hub.RevokeDropBox(ctx, me.sess, me.user, id)
	})
}

func (w *Server) extendDropBox(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "drop box extended", func(ctx context.Context, me *member, id string) error {
		days, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("days")))
		if err != nil {
			return hub.ErrBadDropLimit
		}
		return w.Hub.ExtendDropBox(ctx, me.user, id, days)
	})
}

// decideDropBox approves or rejects the files a person chose (or, with "all", every waiting file of the box).
func (w *Server) decideDropBox(approve bool) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		w.membership(rw, r, "drop box decided", func(ctx context.Context, me *member, id string) error {
			r.ParseForm()
			only := r.PostForm["item"]
			if len(only) == 0 && r.PostFormValue("all") != "1" {
				return hub.ErrNothingChosen
			}
			_, failed, err := w.Hub.DecideDrop(ctx, me.user, id, only, approve)
			if err == nil && failed > 0 {
				err = hub.ErrSomeNotStored
			}
			return err
		})
	}
}

func (w *Server) addDropHost(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "drop box co-host added", func(ctx context.Context, me *member, id string) error {
		return w.Hub.AddDropHost(ctx, me.sess, me.user, id, r.PostFormValue("email"), r.PostFormValue("role"))
	})
}

func (w *Server) removeDropHost(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "drop box co-host removed", func(ctx context.Context, me *member, id string) error {
		target := me.user.ID // no one named: the person asking stops helping
		if v := r.PostFormValue("user"); v != "" {
			var err error
			if target, err = uuid.Parse(v); err != nil {
				return hub.ErrNoDropBox
			}
		}
		return w.Hub.RemoveDropHost(ctx, me.user, id, target)
	})
}

// pendingFile shows a host a file that waits for approval. It is an image the app has checked, sent as an image and
// nothing else, with no way to run anything.
func (w *Server) pendingFile(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	p, err := w.Hub.DropPendingFile(r.Context(), me.user.ID, r.PathValue("id"))
	if err != nil || p.Kind != "image" {
		http.NotFound(rw, r)
		return
	}
	f, err := os.Open(p.Path)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	f.Seek(0, io.SeekStart)
	rw.Header().Set("Content-Type", http.DetectContentType(head[:n]))
	rw.Header().Set("Cache-Control", "private, no-store")
	rw.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	io.Copy(rw, f)
}
