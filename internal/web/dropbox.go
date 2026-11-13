package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// maxDropFilesPerRequest keeps one form from carrying an endless list of files.
const maxDropFilesPerRequest = 100

type guestKey struct{}

// guestInfo is what the layout needs to know about a guest's page: whose it is, so that it can link the box's own
// manifest and colours. The handler of a drop box fills it in; it is set, empty, for every page a guest can see.
type guestInfo struct {
	Token  string // the link the guest used, long or short
	Accent bool   // the box has a colour of its own
	Cover  bool
}

func guestOf(ctx context.Context) *guestInfo { g, _ := ctx.Value(guestKey{}).(*guestInfo); return g }

// isGuest is whether the page is one a guest sees (no account, no tabs, nothing about the library).
func isGuest(ctx context.Context) bool { return guestOf(ctx) != nil }

// dropRoute is whether the path belongs to a guest's pages, which need no Immich login: a drop box, its wall, or the
// page where someone on another server connects (/bridge/<token>).
func dropRoute(path string) bool {
	return strings.HasPrefix(path, "/drop/") || strings.HasPrefix(path, "/d/") || strings.HasPrefix(path, "/wall/") || strings.HasPrefix(path, "/bridge/") || strings.HasPrefix(path, "/unsubscribe/") || strings.HasPrefix(path, "/a/") || strings.HasPrefix(path, "/frame/")
}

// guestSecretCookie is the cookie in which a guest's browser keeps the secret that finds its own uploads again.
func guestSecretCookie(boxID string) string { return "handoff-drop-" + boxID[:8] }

// unlockCookie is the cookie in which a guest's browser keeps that it gave the right password to a box whose password
// guards the whole page.
func unlockCookie(b store.DropBox) string { return "handoff-unlock-" + b.ID[:8] }

// pageLocked is whether this guest is kept out of the box's page: its password guards the whole page and the guest has
// not given it.
func pageLocked(r *http.Request, b store.DropBox) bool {
	v := ""
	if c, err := r.Cookie(unlockCookie(b)); err == nil {
		v = c.Value
	}
	return hub.DropPageLocked(b, v)
}

// dropLocked asks for the password of a box that guards its whole page. It shows nothing of the box: not its name, its
// message, its cover or its colour.
func (w *Server) dropLocked(rw http.ResponseWriter, r *http.Request, b store.DropBox, token string, wrong bool) {
	ctx := r.Context()
	if b.Lang != "" {
		if _, err := r.Cookie(LanguageCookie); err != nil {
			ctx = i18n.With(ctx, i18n.For(i18n.Lang(b.Lang)))
		}
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	rw.WriteHeader(http.StatusUnauthorized)
	DropLocked(token, wrong).Render(ctx, rw)
}

// dropUnlock takes the password of a box that guards its whole page, and, if it is right, remembers that in the guest's
// browser. Wrong tries are counted and answered like wrong passwords for an upload.
func (w *Server) dropUnlock(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if w.Hub == nil {
		w.dropClosed(rw, r)
		return
	}
	token := r.PathValue("token")
	b, err := w.Hub.DropBoxForGuest(ctx, token)
	if err != nil || b.Password == "" || b.PasswordScope != store.DropScopePage {
		w.dropClosed(rw, r)
		return
	}
	who := hub.DropWho(b.Token, w.clientAddr(r))
	if err := w.Hub.AllowDrop(ctx, b, who); err != nil {
		rw.Header().Set("Retry-After", "600")
		rw.WriteHeader(http.StatusTooManyRequests)
		Notice(t(ctx, "Please wait a little"), t(ctx, "Too many photos or requests came from your connection. Try again in ten minutes."), true).Render(ctx, rw)
		return
	}
	defer w.Hub.DoneDrop(b)
	r.ParseForm()
	if !w.Hub.CheckDropPassword(ctx, b, who, r.PostFormValue("password")) {
		w.dropLocked(rw, r, b, token, true)
		return
	}
	http.SetCookie(rw, &http.Cookie{Name: unlockCookie(b), Value: hub.DropUnlockToken(b), Path: "/", MaxAge: 7 * 24 * 3600, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"})
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Location", "../"+token) // relative, so it works under a path prefix
	rw.WriteHeader(http.StatusSeeOther)
}

// forGuest prepares a request for a box's guest page: the box's language when the guest has not chosen one, and what
// the layout needs (the box's own manifest and colour).
func (w *Server) forGuest(r *http.Request, b store.DropBox, token string) *http.Request {
	ctx := r.Context()
	if g := guestOf(ctx); g != nil {
		g.Token, g.Accent = token, b.Accent != ""
	}
	if b.Lang != "" {
		if _, err := r.Cookie(LanguageCookie); err != nil {
			ctx = i18n.With(ctx, i18n.For(i18n.Lang(b.Lang)))
		}
	}
	return r.WithContext(ctx)
}

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
		// a box that has shared its photos back says so, to the people who have its link
		// a box that has ended but keeps its gallery shows it, with the thanks
		if gb, gerr := w.Hub.GalleryBox(r.Context(), r.PathValue("token")); gerr == nil {
			if pageLocked(r, gb) {
				w.dropLocked(rw, r, gb, r.PathValue("token"), false)
				return
			}
			r = w.forGuest(r, gb, r.PathValue("token"))
			w.guestSecretFor(rw, r, gb)
			var share *ShareView
			if sh, ok := w.Hub.DropShareFor(r.Context(), gb.ID); ok {
				sv := w.shareView(sh)
				share = &sv
			}
			rw.Header().Set("Cache-Control", "no-store")
			rw.Header().Set("Referrer-Policy", "no-referrer")
			DropAlbum(DropPageView{Token: r.PathValue("token"), Name: gb.Name, Cover: w.Hub.Store.HasDropCover(r.Context(), gb.ID), GalleryDownload: gb.GalleryDownload}, share).Render(r.Context(), rw)
			return
		}
		// a box that has shared its photos back says so, to the people who have its link
		if gb, gerr := w.Hub.DropBoxForGuest(r.Context(), r.PathValue("token")); gerr == nil {
			if sh, ok := w.Hub.DropShareFor(r.Context(), gb.ID); ok {
				if pageLocked(r, gb) {
					w.dropLocked(rw, r, gb, r.PathValue("token"), false)
					return
				}
				r = w.forGuest(r, gb, r.PathValue("token"))
				rw.Header().Set("Cache-Control", "no-store")
				rw.Header().Set("Referrer-Policy", "no-referrer")
				DropThanks(gb.Name, sh.Message, w.shareView(sh)).Render(r.Context(), rw)
				return
			}
		}
		w.dropClosed(rw, r)
		return
	}
	if pageLocked(r, b) {
		w.dropLocked(rw, r, b, r.PathValue("token"), false)
		return
	}
	r = w.forGuest(r, b, r.PathValue("token"))
	if b.Gallery {
		w.guestSecretFor(rw, r, b) // so a heart can be found again
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	DropPage(DropPageView{Token: r.PathValue("token"), Name: b.Name, Message: b.Message, Password: b.Password != "" && b.PasswordScope != store.DropScopePage, PhotosOnly: b.PhotosOnly,
		Approve: b.Approve, Limits: dropLimitText(r.Context(), b), MaxDim: b.MaxDim, Cover: w.Hub.Store.HasDropCover(r.Context(), b.ID),
		CanDelete: b.GuestDelete, Prompts: w.promptViews(r.Context(), b), PromptRequired: b.PromptRequired, GuestBook: b.GuestBook,
		Gallery: b.Gallery, GalleryDownload: b.GalleryDownload, GalleryNames: b.GalleryNames}).Render(r.Context(), rw)
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
	r = w.forGuest(r, b, token)
	ctx = r.Context()
	who := hub.DropWho(b.Token, w.clientAddr(r)) // the mark is made from the long token, so both ways in tell a guest by the same one
	asJSON := strings.Contains(r.Header.Get("Accept"), "application/json")
	secret := ""
	if b.GuestDelete || b.Gallery {
		secret = w.guestSecretFor(rw, r, b)
	}
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
	unlocked := b.Password == "" || (b.PasswordScope == store.DropScopePage && !pageLocked(r, b)) // given on the page, or with the files
	prompt, guestName := "", ""                                                                   // the prompt of the shot list the next file is for, and the name the guest gave (fields sent before it)
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
		case part.FormName() == "prompt":
			v, _ := io.ReadAll(io.LimitReader(part, 20))
			prompt = string(v)
		case part.FormName() == "guest":
			v, _ := io.ReadAll(io.LimitReader(part, 400))
			guestName = string(v)
		case part.FormName() == "files" && part.FileName() != "":
			if !unlocked {
				rw.WriteHeader(http.StatusForbidden)
				Notice(t(ctx, "Wrong password"), t(ctx, "The password is not right. Go back and try again."), true).Render(ctx, rw)
				return
			}
			outcome, err := w.Hub.ReceiveDropFileWith(ctx, b.ID, part.FileName(), part, who, hub.DropMeta{Secret: secret, Prompt: prompt, Name: guestName})
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
	if asJSON { // the page's own script sends one file at a time and reads the answer
		type line struct {
			Name string `json:"name"`
			Text string `json:"text"`
			OK   bool   `json:"ok"`
		}
		out := struct {
			Files []line `json:"files"`
		}{Files: []line{}}
		for _, l := range lines {
			out.Files = append(out.Files, line{l.Name, l.Text, l.OK})
		}
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(out)
		return
	}
	DropDone(b.Name, "drop/"+token, b.GuestDelete, lines).Render(ctx, rw)
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
	case errors.Is(err, hub.ErrDropPrompt):
		return t(ctx, "Choose what the photo is for")
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
	if b.Password != "" && b.PasswordScope != store.DropScopePage { // a guest who sees this has already given the page's password
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
	if b.LockedPage {
		c = append(c, Chip{"", t(ctx, "Password on the page")})
	} else if b.Locked {
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
			Files: b.Files, Size: megabytes(b.Bytes), Limits: dropLimitText(ctx, b), Locked: b.Password != "", LockedPage: b.Password != "" && b.PasswordScope == store.DropScopePage,
			Album: b.Album, ExtraTag: b.ExtraTag, Approve: b.Approve, Owner: a.Role == "owner", Manage: a.Role != "view",
			Accent: b.Accent, Lang: b.Lang, MaxDim: b.MaxDim, GuestDelete: b.GuestDelete}
		v.PromptRequired, v.GuestBook = b.PromptRequired, b.GuestBook
		v.KeepDays, v.Gallery, v.GalleryDownload, v.GalleryNames = b.KeepDays, b.Gallery, b.GalleryDownload, b.GalleryNames
		if until := b.KeepUntil(); !until.IsZero() {
			v.KeepUntil = loc(ctx).Stamp(until.Local())
		}
		if v.Manage {
			v.Short = b.Short
			v.MailOn, v.MailAddress, v.MailLocked = w.Hub.MailIn != nil, w.Hub.MailAddress(b.MailCode), b.Password != ""
			v.HasCover = w.Hub.Store.HasDropCover(ctx, b.ID)
			var lines []string
			for _, ps := range w.Hub.PromptStatuses(ctx, b) {
				lines = append(lines, ps.Text)
				v.Prompts = append(v.Prompts, PromptView{ID: ps.ID, Text: ps.Text, Files: ps.Files})
			}
			v.PromptsText = strings.Join(lines, "\n")
			if b.GuestBook {
				if msgs, err := w.Hub.GuestBook(ctx, me.user.ID, b.ID); err == nil {
					for _, m := range msgs {
						v.Messages = append(v.Messages, MessageView{ID: m.ID, Name: m.Name, Text: m.Text, When: loc(ctx).Stamp(m.At.Local()), HasAudio: m.HasAudio, OnWall: m.OnWall})
					}
				}
			}
		}
		if v.Manage {
			v.WallToken = b.Wall
			v.Highlight = w.highlightView(ctx, b)
		}
		if v.Owner {
			v.OnwardService, v.OnwardAuto = b.OnwardService, b.OnwardAuto
			for _, o := range w.Hub.OnwardOptions(ctx, me.user.ID) {
				v.OnwardOptions = append(v.OnwardOptions, OnwardOptionView{Service: o.Service, Copy: o.Copy})
			}
			v.HasBest = v.Highlight != nil && v.Highlight.Status == store.HighlightDone && v.Highlight.Kept > 0
			if log, err := w.Hub.Store.DropOnwardLog(ctx, b.ID, 10); err == nil {
				for _, o := range log {
					v.OnwardLog = append(v.OnwardLog, OnwardView{When: loc(ctx).Stamp(o.At.Local()), Service: o.Service, Scope: onwardScopeLabel(ctx, o.Scope), Count: o.Count, By: o.By})
				}
			}
		}
		if v.Owner {
			if sh, ok := w.Hub.DropShareFor(ctx, b.ID); ok {
				sv := w.shareView(sh)
				v.Share = &sv
			}
		}
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
	if b.Removed > 0 {
		text += " " + n(ctx, "{n} file taken back by guests.", "{n} files taken back by guests.", b.Removed)
	}
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
		switch secure := r.PostFormValue("secure"); secure {
		case "none":
			p.Password = "" // a password typed and then "No" chosen is not kept
		case store.DropScopeUpload, store.DropScopePage:
			p.PasswordScope = secure
		}
		p.PhotosOnly = r.PostFormValue("photos") == "1"
		p.Approve = r.PostFormValue("approve") == "1"
		p.GuestDelete = r.PostFormValue("guestdelete") == "1"
		p.Prompts = strings.Split(strings.ReplaceAll(r.PostFormValue("prompts"), "\r", ""), "\n")
		p.PromptRequired = r.PostFormValue("required") == "1"
		p.GuestBook = r.PostFormValue("guestbook") == "1"
		p.Gallery, p.GalleryDownload, p.GalleryNames = r.PostFormValue("gallery") == "1", r.PostFormValue("gallerydownload") == "1", r.PostFormValue("gallerynames") == "1"
		p.Album, p.ExtraTag = r.PostFormValue("album"), r.PostFormValue("tag")
		_, err = w.Hub.CreateDropBox(ctx, me.sess, me.user, p)
		return err
	})
}

// eventDropBox changes the settings that make a box an event.
func (w *Server) eventDropBox(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "drop box event settings", func(ctx context.Context, me *member, id string) error {
		keep := 0
		if v := strings.TrimSpace(r.PostFormValue("keepdays")); v != "" {
			var err error
			if keep, err = strconv.Atoi(v); err != nil {
				return hub.ErrBadDropLimit
			}
		}
		return w.Hub.SetDropEvent(ctx, me.sess, me.user, id, hub.EventSettings{KeepDays: keep, Gallery: r.PostFormValue("gallery") == "1",
			Download: r.PostFormValue("gallerydownload") == "1", Names: r.PostFormValue("gallerynames") == "1"})
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

// --- what makes a box's guest page its own: colour, cover, manifest ---

// guestBox finds the box a guest-side resource belongs to; closed boxes give no resources, like no page.
func (w *Server) guestBox(rw http.ResponseWriter, r *http.Request) (store.DropBox, bool) {
	if w.Hub == nil {
		http.NotFound(rw, r)
		return store.DropBox{}, false
	}
	b, err := w.Hub.OpenDropBox(r.Context(), r.PathValue("token"))
	if err != nil || pageLocked(r, b) {
		http.NotFound(rw, r)
		return b, false
	}
	return b, true
}

// onAccent is the text colour that reads on a colour: black or white, whichever has the better contrast.
func onAccent(hex string) string {
	var r, g, b int
	fmt.Sscanf(strings.TrimPrefix(hex, "#"), "%02x%02x%02x", &r, &g, &b)
	if (299*r+587*g+114*b)/1000 > 150 {
		return "#111111"
	}
	return "#ffffff"
}

func (w *Server) dropTheme(rw http.ResponseWriter, r *http.Request) {
	b, ok := w.guestBox(rw, r)
	if !ok {
		return
	}
	if b.Accent == "" {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Content-Type", "text/css; charset=utf-8")
	rw.Header().Set("Cache-Control", "private, max-age=300")
	// the accent is checked to be #rrggbb when it is saved, and again here before it goes into a style sheet
	if !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(b.Accent) {
		return
	}
	// :root three times over, so a box's own colour beats the colour scheme the page starts with (which sits on :root and :root[data-palette])
	fmt.Fprintf(rw, ":root:root:root{--accent:%s;--on-accent:%s}\n", b.Accent, onAccent(b.Accent))
}

func (w *Server) dropCover(rw http.ResponseWriter, r *http.Request) {
	b, ok := w.guestBox(rw, r)
	if !ok {
		return
	}
	kind, data, err := w.Hub.Store.DropCover(r.Context(), b.ID)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Content-Type", kind)
	rw.Header().Set("Cache-Control", "private, max-age=300")
	rw.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	rw.Write(data)
}

// dropManifest lets a guest install a box's page on their phone, under the box's own name.
func (w *Server) dropManifest(rw http.ResponseWriter, r *http.Request) {
	b, ok := w.guestBox(rw, r)
	if !ok {
		return
	}
	token := r.PathValue("token")
	short := []rune(b.Name)
	if len(short) > 12 {
		short = short[:12]
	}
	color := "#0d0b20"
	if b.Accent != "" {
		color = b.Accent
	}
	m := map[string]any{
		"name": b.Name, "short_name": string(short), "description": b.Message,
		"start_url": "../" + token, "scope": "../", "display": "standalone",
		"background_color": "#0d0b20", "theme_color": color,
		"icons": []map[string]string{
			{"src": "../../static/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
			{"src": "../../static/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
			{"src": "../../static/icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
		},
	}
	rw.Header().Set("Content-Type", "application/manifest+json")
	rw.Header().Set("Cache-Control", "private, max-age=300")
	json.NewEncoder(rw).Encode(m)
}

// --- a guest's own uploads ---

// guestSecret is the secret this guest's browser holds for the box ("" when it has none).
func guestSecret(r *http.Request, b store.DropBox) string {
	if c, err := r.Cookie(guestSecretCookie(b.ID)); err == nil && len(c.Value) == 32 {
		return c.Value
	}
	return ""
}

func (w *Server) dropMine(rw http.ResponseWriter, r *http.Request) {
	if w.Hub == nil {
		w.dropClosed(rw, r)
		return
	}
	token := r.PathValue("token")
	b, err := w.Hub.DropBoxForGuest(r.Context(), token)
	if err != nil || !b.GuestDelete || pageLocked(r, b) {
		w.dropClosed(rw, r)
		return
	}
	r = w.forGuest(r, b, token)
	files, _ := w.Hub.GuestUploads(r.Context(), b.ID, guestSecret(r, b))
	view := make([]GuestFileView, 0, len(files))
	for _, f := range files {
		view = append(view, GuestFileView{ID: f.ID, Name: f.Name, Size: megabytes(f.Size), Waiting: f.Waiting})
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	DropMine(b.Name, token, view, r.URL.Query().Get("msg")).Render(r.Context(), rw)
}

func (w *Server) dropMineDelete(rw http.ResponseWriter, r *http.Request) {
	if w.Hub == nil {
		w.dropClosed(rw, r)
		return
	}
	token := r.PathValue("token")
	b, err := w.Hub.DropBoxForGuest(r.Context(), token)
	if err != nil || pageLocked(r, b) {
		w.dropClosed(rw, r)
		return
	}
	r = w.forGuest(r, b, token)
	r.ParseForm()
	msg := "deleted"
	if err := w.Hub.DeleteGuestUpload(r.Context(), b.ID, guestSecret(r, b), r.PostFormValue("id")); err != nil {
		msg = "kept"
		if !errors.Is(err, hub.ErrDropKept) {
			msg = "failed"
		}
	}
	// back to the list, which says what happened; relative, so it works under a path prefix
	rw.Header().Set("Location", "../mine?msg="+msg)
	rw.WriteHeader(http.StatusSeeOther)
}

func guestMessage(ctx context.Context, msg string) string {
	switch msg {
	case "deleted":
		return t(ctx, "Taken back. It is in the owner's trash, not in their library.")
	case "kept":
		return t(ctx, "The owner has already filed this photo in their library. Ask them if you want it removed.")
	case "failed":
		return t(ctx, "That could not be taken back right now. Please try again.")
	}
	return ""
}

// --- more owner actions: ways in, look, templates ---

func (w *Server) shortDropBox(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "drop box short link", func(ctx context.Context, me *member, id string) error {
		return w.Hub.SetDropShort(ctx, me.user, id, r.PostFormValue("on") == "1")
	})
}

func (w *Server) mailDropBox(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "drop box mail address", func(ctx context.Context, me *member, id string) error {
		return w.Hub.SetDropMail(ctx, me.user, id, r.PostFormValue("on") == "1")
	})
}

func (w *Server) renewDropBox(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "drop box new link", func(ctx context.Context, me *member, id string) error {
		return w.Hub.RenewDropToken(ctx, me.user, id)
	})
}

// lookDropBox saves the colour, language, shrinking size and cover photo of a box's guest page. It is a multipart
// form (a cover photo comes with it); its body may be a little over the cover's limit.
func (w *Server) lookDropBox(rw http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(rw, r.Body, 1<<20+256<<10)
	w.membership(rw, r, "drop box look", func(ctx context.Context, me *member, id string) error {
		if err := r.ParseMultipartForm(1 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
			return hub.ErrBadCover
		}
		dim := 0
		if v := strings.TrimSpace(r.PostFormValue("maxdim")); v != "" {
			var err error
			if dim, err = strconv.Atoi(v); err != nil {
				return hub.ErrBadDropLook
			}
		}
		var cover []byte
		if f, _, err := r.FormFile("cover"); err == nil {
			defer f.Close()
			cover, _ = io.ReadAll(io.LimitReader(f, 1<<20))
		}
		return w.Hub.SetDropLook(ctx, me.user, id, r.PostFormValue("accent"), r.PostFormValue("lang"), dim, cover, r.PostFormValue("nocover") == "1")
	})
}

func (w *Server) saveDropTemplate(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "drop box template saved", func(ctx context.Context, me *member, id string) error {
		return w.Hub.SaveDropTemplate(ctx, me.user, id, r.PostFormValue("name"))
	})
}

func (w *Server) deleteDropTemplate(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "drop box template deleted", func(ctx context.Context, me *member, id string) error {
		return w.Hub.DeleteDropTemplate(ctx, me.user, id)
	})
}

// dropFromTemplate makes a box with the settings of a template and the name and times the person gives.
func (w *Server) dropFromTemplate(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "drop box from a template", func(ctx context.Context, me *member, id string) error {
		p, err := w.Hub.TemplateParams(ctx, me.user.ID, id, r.PostFormValue("name"))
		if err != nil {
			return err
		}
		return w.makeFrom(ctx, me, r, p)
	})
}

// duplicateDropBox makes a box with the settings of another one of the person's.
func (w *Server) duplicateDropBox(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "drop box duplicated", func(ctx context.Context, me *member, id string) error {
		p, err := w.Hub.DuplicateParams(ctx, me.user.ID, id, r.PostFormValue("name"))
		if err != nil {
			return err
		}
		return w.makeFrom(ctx, me, r, p)
	})
}

func (w *Server) makeFrom(ctx context.Context, me *member, r *http.Request, p hub.DropParams) error {
	var err error
	var opens, closes time.Time
	if opens, err = clockField(r, "opens"); err != nil {
		return err
	}
	if closes, err = clockField(r, "closes"); err != nil {
		return err
	}
	p.Opens, p.Closes = opens, closes
	_, err = w.Hub.CreateDropBox(ctx, me.sess, me.user, p)
	return err
}

// dropCard is a page to print: the box's name and message and a QR code of its link, for table cards and posters. The
// address is made in the browser (it depends on where the page is served), like the invitation links.
func (w *Server) dropCard(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	b, err := w.Hub.DropBoxAccess(r.Context(), me.user.ID, r.PathValue("id"))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	link := "drop/" + b.Token
	if r.URL.Query().Get("way") == "short" && b.Short != "" {
		link = "d/" + b.Short
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	DropCard(me.user.Name, b.Name, b.Message, link, w.Hub.MailAddress(b.MailCode)).Render(r.Context(), rw)
}

// templateViews is what the person can start a drop box from.
func (w *Server) templateViews(ctx context.Context, me *member) []TemplateView {
	list, err := w.Hub.DropTemplates(ctx, me.user.ID)
	if err != nil {
		return nil
	}
	var out []TemplateView
	for _, tp := range list {
		name := tp.Name
		if tp.Builtin {
			name = t(ctx, tp.Name)
		}
		out = append(out, TemplateView{ID: tp.ID, Name: name, Builtin: tp.Builtin, Summary: templateSummary(ctx, tp.Settings)})
	}
	return out
}

func templateSummary(ctx context.Context, s hub.DropSettings) string {
	var parts []string
	if s.Days > 0 {
		parts = append(parts, n(ctx, "{n} day", "{n} days", s.Days))
	}
	if s.MaxFileMB > 0 {
		parts = append(parts, t(ctx, "up to {mb} MB each", "mb", s.MaxFileMB))
	}
	if s.PhotosOnly {
		parts = append(parts, t(ctx, "photos only"))
	}
	if s.Approve {
		parts = append(parts, t(ctx, "files are looked at before they are kept"))
	}
	if s.Album != "" {
		parts = append(parts, t(ctx, "in the album {album}", "album", s.Album))
	}
	return strings.Join(parts, " \u00b7 ")
}

// --- the shot list, the guest book and sharing back: the guest's side ---

func (w *Server) promptViews(ctx context.Context, b store.DropBox) []PromptView {
	var out []PromptView
	for _, ps := range w.Hub.PromptStatuses(ctx, b) {
		out = append(out, PromptView{ID: ps.ID, Text: ps.Text, Files: ps.Files})
	}
	return out
}

// shareView is a box's shared link as the pages show it: the address of Immich's shared page when the administrator told
// the app where Immich is (PUBLIC_IMMICH_URL), and otherwise the key, which the page turns into an address in the browser.
func (w *Server) shareView(sh store.DropShare) ShareView {
	v := ShareView{Key: sh.Key, Expires: "", Message: sh.Message}
	if base := strings.TrimRight(w.PublicImmichURL, "/"); base != "" {
		v.URL = base + "/share/" + sh.Key
	}
	if !sh.ExpiresAt.IsZero() {
		v.Expires = sh.ExpiresAt.Local().Format("2006-01-02 15:04")
	}
	return v
}

// dropMessage takes a guest's message and voice note for the guest book.
func (w *Server) dropMessage(rw http.ResponseWriter, r *http.Request) {
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
	r = w.forGuest(r, b, token)
	ctx = r.Context()
	who := hub.DropWho(b.Token, w.clientAddr(r))
	asJSON := strings.Contains(r.Header.Get("Accept"), "application/json")
	answer := func(status int, err error) {
		text := ""
		if err != nil {
			text = t(ctx, "The message could not be kept: {reason}", "reason", t(ctx, err.Error()))
		}
		if asJSON {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(status)
			json.NewEncoder(rw).Encode(map[string]any{"ok": err == nil, "text": text})
			return
		}
		rw.Header().Set("Cache-Control", "no-store")
		rw.WriteHeader(status)
		if err != nil {
			Notice(t(ctx, "The message was not kept"), text, true).Render(ctx, rw)
			return
		}
		DropMessageDone(b.Name, "drop/"+token).Render(ctx, rw)
	}
	if pageLocked(r, b) {
		answer(http.StatusForbidden, hub.ErrDropPassword)
		return
	}
	if err := w.Hub.AllowDrop(ctx, b, who); err != nil {
		rw.Header().Set("Retry-After", "600")
		answer(http.StatusTooManyRequests, err)
		return
	}
	defer w.Hub.DoneDrop(b)
	r.Body = http.MaxBytesReader(rw, r.Body, 3<<20)
	if err := r.ParseMultipartForm(3 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		answer(http.StatusBadRequest, hub.ErrBadMessage)
		return
	}
	var voice []byte
	if f, _, err := r.FormFile("voice"); err == nil {
		defer f.Close()
		voice, _ = io.ReadAll(io.LimitReader(f, 2<<20+1))
	}
	if err := w.Hub.LeaveMessage(ctx, b, who, r.PostFormValue("name"), r.PostFormValue("text"), voice); err != nil {
		status := http.StatusInternalServerError
		if hub.IsUserError(err) {
			status = http.StatusUnprocessableEntity
		}
		answer(status, err)
		return
	}
	answer(http.StatusOK, nil)
}

// --- the owner's side of them ---

func (w *Server) extrasDropBox(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "drop box extras", func(ctx context.Context, me *member, id string) error {
		return w.Hub.SetDropExtras(ctx, me.user, id, strings.Split(strings.ReplaceAll(r.PostFormValue("prompts"), "\r", ""), "\n"),
			r.PostFormValue("required") == "1", r.PostFormValue("guestbook") == "1")
	})
}

func (w *Server) deleteMessage(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "message to the host removed", func(ctx context.Context, me *member, id string) error {
		return w.Hub.DeleteGuestMessage(ctx, me.user, id)
	})
}

// voiceNote plays a guest's voice note to the owner or a co-host who manages the box.
func (w *Server) voiceNote(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	kind, data, err := w.Hub.GuestBookAudio(r.Context(), me.user.ID, r.PathValue("id"))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Content-Type", kind)
	rw.Header().Set("Cache-Control", "private, no-store")
	rw.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	rw.Write(data)
}

// exportGuestBook hands the guest book to its owner as a zip.
func (w *Server) exportGuestBook(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	var buf bytes.Buffer
	if err := w.Hub.ExportGuestBook(r.Context(), me.user.ID, r.PathValue("id"), &buf); err != nil {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Content-Type", "application/zip")
	rw.Header().Set("Content-Disposition", `attachment; filename="guest-book.zip"`)
	rw.Header().Set("Cache-Control", "private, no-store")
	rw.Write(buf.Bytes())
}

func (w *Server) shareDropBox(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "drop box shared", func(ctx context.Context, me *member, id string) error {
		days, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("days")))
		if err != nil {
			return hub.ErrBadShare
		}
		_, err = w.Hub.ShareBack(ctx, me.sess, me.user, id, days, r.PostFormValue("password"), r.PostFormValue("message"))
		return err
	})
}

func (w *Server) unshareDropBox(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "drop box unshared", func(ctx context.Context, me *member, id string) error {
		return w.Hub.UnshareBack(ctx, me.sess, me.user, id)
	})
}

// --- the wall ---

func (w *Server) wallBox(rw http.ResponseWriter, r *http.Request) (store.DropBox, bool) {
	if w.Hub == nil {
		http.NotFound(rw, r)
		return store.DropBox{}, false
	}
	b, err := w.Hub.WallBox(r.Context(), r.PathValue("token"))
	if err != nil {
		http.NotFound(rw, r)
		return b, false
	}
	return b, true
}

// wallPage is the slideshow: full screen, no sign-in, no controls but the ones that fade away.
func (w *Server) wallPage(rw http.ResponseWriter, r *http.Request) {
	b, ok := w.wallBox(rw, r)
	if !ok {
		return
	}
	r = w.forGuest(r, b, r.PathValue("token"))
	mode := r.URL.Query().Get("mode")
	if mode != "grid" && mode != "collage" {
		mode = "single"
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	WallPage(b.Name, r.PathValue("token"), mode).Render(r.Context(), rw)
}

// wallFeed tells the wall which photos came since the last one it has, and which messages to show.
func (w *Server) wallFeed(rw http.ResponseWriter, r *http.Request) {
	b, ok := w.wallBox(rw, r)
	if !ok {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	items, msgs, err := w.Hub.WallFeed(r.Context(), b, after)
	if err != nil {
		http.Error(rw, "try again", http.StatusServiceUnavailable)
		return
	}
	type item struct {
		ID      int64  `json:"id"`
		Caption string `json:"caption,omitempty"`
	}
	type message struct {
		Name string `json:"name,omitempty"`
		Text string `json:"text"`
	}
	out := struct {
		Items    []item    `json:"items"`
		Messages []message `json:"messages"`
	}{Items: []item{}, Messages: []message{}}
	for _, i := range items {
		out.Items = append(out.Items, item{i.ID, i.Caption})
	}
	for _, m := range msgs {
		out.Messages = append(out.Messages, message{m.Name, m.Text})
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(rw).Encode(out)
}

// wallPhoto gives one picture to the wall: a photo of this box, by its place in the box's list.
func (w *Server) wallPhoto(rw http.ResponseWriter, r *http.Request) {
	b, ok := w.wallBox(rw, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	data, kind, err := w.Hub.WallImage(r.Context(), b, id)
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
	rw.Write(data)
}

// --- the owner's side of the wall, the best-of and handing on ---

func (w *Server) startWall(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "wall started", func(ctx context.Context, me *member, id string) error {
		_, err := w.Hub.StartWall(ctx, me.sess, me.user, id, r.PostFormValue("renew") == "1")
		return err
	})
}

func (w *Server) stopWall(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "wall stopped", func(ctx context.Context, me *member, id string) error {
		return w.Hub.StopWall(ctx, me.sess, me.user, id)
	})
}

func (w *Server) messageOnWall(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "message on the wall", func(ctx context.Context, me *member, id string) error {
		return w.Hub.SetMessageOnWall(ctx, me.user, id, r.PostFormValue("on") == "1")
	})
}

func (w *Server) startHighlights(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "best-of started", func(ctx context.Context, me *member, id string) error {
		return w.Hub.StartHighlights(ctx, me.sess, me.user, id, r.PostFormValue("strictness"))
	})
}

func (w *Server) setOnward(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "hand-on chosen", func(ctx context.Context, me *member, id string) error {
		return w.Hub.SetDropOnward(ctx, me.user, id, r.PostFormValue("target"), r.PostFormValue("auto") == "1")
	})
}

func (w *Server) handOn(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "handed on", func(ctx context.Context, me *member, id string) error {
		_, err := w.Hub.HandOnward(ctx, me.user, id, r.PostFormValue("scope"))
		return err
	})
}

// verdictText is the reason a photo was left out of a best-of, in the person's language.
func verdictText(ctx context.Context, v store.Verdict) string {
	switch v.Verdict {
	case hub.VerdictDark:
		return t(ctx, "too dark")
	case hub.VerdictBright:
		return t(ctx, "too bright")
	case hub.VerdictBlurry:
		return t(ctx, "blurry")
	case hub.VerdictDuplicate:
		return t(ctx, "the same moment as a better photo")
	}
	return v.Verdict
}

func strictnessLabel(ctx context.Context, name string) string {
	switch name {
	case "relaxed":
		return t(ctx, "relaxed: leaves out little")
	case "strict":
		return t(ctx, "strict: leaves out more")
	}
	return t(ctx, "normal")
}

func onwardScopeLabel(ctx context.Context, scope string) string {
	if scope == "best" {
		return t(ctx, "the best-of")
	}
	return t(ctx, "everything")
}

// highlightView is a box's best-of as the owner reads it: how far it is, what it kept, and why the rest was left out.
func (w *Server) highlightView(ctx context.Context, b store.DropBox) *HighlightView {
	h, err := w.Hub.Store.DropHighlightOf(ctx, b.ID)
	if err != nil {
		return nil
	}
	v := &HighlightView{Status: h.Status, Strictness: h.Strictness, Total: h.Total, Done: h.Done, Kept: h.Kept, Album: h.Album, Note: h.Note}
	if h.Status == store.HighlightDone {
		v.When = loc(ctx).Stamp(h.Finished.Local())
		if ex, err := w.Hub.Store.DropVerdicts(ctx, b.ID, true, 100); err == nil {
			v.OutCount = h.Total - h.Kept
			for _, x := range ex {
				v.Excluded = append(v.Excluded, VerdictView{Name: x.Name, Reason: verdictText(ctx, x), Detail: x.Detail})
			}
		}
	}
	return v
}
