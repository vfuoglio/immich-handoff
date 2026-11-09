package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	DropPage(DropPageView{Token: b.Token, Name: b.Name, Message: b.Message, Password: b.Password != "", PhotosOnly: b.PhotosOnly, Limits: dropLimitText(r.Context(), b)}).Render(r.Context(), rw)
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
			_, err := w.Hub.ReceiveDropFile(ctx, b.ID, part.FileName(), part, who)
			lines = append(lines, DropResultLine{Name: part.FileName(), Text: dropReason(ctx, err), OK: err == nil})
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

// dropBoxViews is the person's drop boxes as the page shows them.
func (w *Server) dropBoxViews(ctx context.Context, me *member) []DropBoxView {
	boxes, err := w.Hub.DropBoxes(ctx, me.user.ID)
	if err != nil {
		w.Log.Warn("could not list drop boxes", "err", err)
		return nil
	}
	var out []DropBoxView
	for _, b := range boxes {
		v := DropBoxView{ID: b.ID, Name: b.Name, Message: b.Message, Token: b.Token, State: dropState(b), Note: b.Note,
			Files: b.Files, Size: megabytes(b.Bytes), Limits: dropLimitText(ctx, b), Locked: b.Password != ""}
		if v.State == "revoked" {
			v.Token = ""
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

func dropEventText(ctx context.Context, e store.DropEvent) string {
	text := unpack(ctx, e.Text)
	if e.Who != "" {
		text += " (" + t(ctx, "guest {mark}", "mark", e.Who) + ")"
	}
	return text
}

// --- the owner's side ---

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
		p.Name, p.Message, p.Password = r.PostFormValue("name"), r.PostFormValue("message"), r.PostFormValue("password")
		p.PhotosOnly = r.PostFormValue("photos") == "1"
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
