package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// A client room is a link where a photographer's client looks at an album through marked pictures. This file is the
// photographer's side (making, pausing and ending a room) and the client's page.

// roomViews are the person's client rooms for the Services tab.
func (w *Server) roomViews(ctx context.Context, me *member) []RoomView {
	rooms, err := w.Hub.Rooms(ctx, me.user.ID)
	if err != nil {
		w.Log.Warn("could not list client rooms", "err", err)
		return nil
	}
	l := loc(ctx)
	var out []RoomView
	for _, r := range rooms {
		v := RoomView{ID: r.ID, Name: r.Name, Title: r.Title, Album: r.AlbumName, Token: r.Token, PIN: r.Password != "",
			Mark: markSummary(r), Ended: r.State == store.RoomEnded || (!r.EndsAt.IsZero() && !r.EndsAt.After(time.Now())), Paused: r.State == store.RoomPaused}
		if !r.EndsAt.IsZero() {
			v.Ends = l.Stamp(r.EndsAt.Local())
		}
		v.Mode, v.Limit, v.Notes = r.PickMode, r.PickLimit, w.Hub.RoomNoteCount(ctx, r)
		v.Round, v.Several = r.CurrentRound(), r.MultiRound
		if sels, err := w.Hub.RoomSelections(ctx, r); err == nil {
			for _, s := range sels {
				sv := SelectionView{Who: s.Who, Name: s.Name, Count: s.Count, Done: s.Done}
				if s.Done {
					sv.When = l.Stamp(s.DoneAt.Local())
				}
				v.Selections = append(v.Selections, sv)
			}
		}
		out = append(out, v)
	}
	return out
}

// roomAlbums lists the person's own albums for the form that makes a room; it is asked for when the form is opened.
func (w *Server) roomAlbums(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
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
	RoomAlbumSelect(opts).Render(r.Context(), rw)
}

func (w *Server) createRoom(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "room made", func(ctx context.Context, me *member, _ string) error {
		album, err := uuid.Parse(r.PostFormValue("album"))
		if err != nil {
			return hub.ErrNoAlbum
		}
		days, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("ends")))
		limit, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("picklimit")))
		relDays, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("releasedays")))
		if err != nil {
			relDays = 30
		}
		var logo []byte
		if f, _, err := r.FormFile("logo"); err == nil {
			defer f.Close()
			if logo, err = io.ReadAll(io.LimitReader(f, 300<<10+1)); err != nil {
				return hub.ErrRoomLogo
			}
		}
		_, err = w.Hub.CreateRoom(ctx, me.sess, me.user, hub.RoomParams{Name: r.PostFormValue("name"), Title: r.PostFormValue("title"),
			AlbumID: album, PIN: strings.TrimSpace(r.PostFormValue("pin")), EndsInDays: days,
			Mark: r.PostFormValue("mark"), MarkText: r.PostFormValue("marktext"), Logo: logo, PickMode: r.PostFormValue("pickmode"), PickLimit: limit,
			AutoRelease: r.PostFormValue("autorelease") == "1", ReleaseDays: relDays, NotesMode: r.PostFormValue("notes"), MultiRound: r.PostFormValue("rounds") == "several"})
		return err
	})
}

func (w *Server) pauseRoom(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "room paused", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.PauseRoom(ctx, me.user, r.PostFormValue("room"), r.PostFormValue("paused") == "1")
	})
}

func (w *Server) endRoom(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "room ended", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.EndRoom(ctx, me.sess, me.user, r.PostFormValue("room"))
	})
}

// roomUnlockCookie is the cookie in which a client's browser keeps that it gave the right PIN to a room.
func roomUnlockCookie(r store.Room) string { return "handoff-room-" + r.ID[:8] }

// roomLocked is whether this client is kept out of the room: it has a PIN and the client has not given it.
func roomLocked(r *http.Request, room store.Room) bool {
	v := ""
	if c, err := r.Cookie(roomUnlockCookie(room)); err == nil {
		v = c.Value
	}
	return hub.RoomLocked(room, v)
}

// roomOpen finds the room a link opens and checks that the client may see its pictures: unknown, ended, paused, and locked
// rooms all answer "not found" to everything but the room's own page.
func (w *Server) roomOpen(rw http.ResponseWriter, r *http.Request) (store.Room, bool) {
	if w.Hub == nil {
		http.NotFound(rw, r)
		return store.Room{}, false
	}
	room, err := w.Hub.RoomFor(r.Context(), r.PathValue("token"))
	if err != nil || room.State != store.RoomActive || roomLocked(r, room) {
		http.NotFound(rw, r)
		return room, false
	}
	return room, true
}

// roomPage is the client's page. It is public (the long link, and the PIN if there is one, is the only protection), is never
// indexed or cached, and sends no referrer.
func (w *Server) roomPage(rw http.ResponseWriter, r *http.Request) {
	if w.Hub == nil {
		http.NotFound(rw, r)
		return
	}
	room, err := w.Hub.RoomFor(r.Context(), r.PathValue("token"))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	rw.Header().Set("X-Robots-Tag", "noindex")
	ctx := r.Context()
	switch {
	case room.State == store.RoomPaused:
		RoomPaused().Render(ctx, rw)
		return
	case roomLocked(r, room):
		rw.WriteHeader(http.StatusUnauthorized)
		RoomLocked(room.Token, false, false).Render(ctx, rw)
		return
	}
	v := w.roomViewer(rw, r, room)
	all, err := w.Hub.RoomPhotos(ctx, room)
	if err != nil {
		http.Error(rw, "try again", http.StatusServiceUnavailable)
		return
	}
	place := map[string]int{}
	for i, p := range all {
		place[p.ID] = i + 1
	}
	photos, err := w.Hub.RoomVisibleTo(ctx, room, v)
	if err != nil {
		http.Error(rw, "try again", http.StatusServiceUnavailable)
		return
	}
	sel, err := w.Hub.RoomSelectionOf(ctx, room, v)
	if err != nil {
		http.Error(rw, "try again", http.StatusServiceUnavailable)
		return
	}
	d := RoomPageData{Title: room.ClientTitle(), Token: room.Token, Name: v.Name, Count: sel.Count, Limit: sel.Limit, Done: sel.Done, CanPick: sel.CanPick,
		NeedsName: sel.NeedsName, Chooser: sel.Chooser, Mode: sel.Mode, CounterText: pickCounter(ctx, sel.Count, sel.Limit), Message: roomMessage(ctx, r.URL.Query().Get("e"))}
	notes, _ := w.Hub.RoomNotesOf(ctx, room, v)
	if room.NotesMode != store.RoomNotesNone {
		d.Notes = room.NotesMode
	}
	mine := map[string]bool{}
	if dl, err := w.Hub.RoomDownloads(ctx, room, v); err == nil {
		d.Released = true
		for _, p := range dl.Photos {
			mine[p.ID] = true
		}
		if !dl.Until.IsZero() {
			d.Until = loc(ctx).Stamp(dl.Until.Local())
		}
	}
	d.Total, d.Round = len(all), room.CurrentRound()
	for _, p := range photos {
		d.Tiles = append(d.Tiles, RoomTile{N: place[p.ID], ID: p.ID, Video: p.Video, Picked: sel.Picked[p.ID], Mine: mine[p.ID], Note: notes[p.ID].Body, Reply: notes[p.ID].Reply})
	}
	RoomPage(d).Render(ctx, rw)
}

// roomMessage is the sentence for the code of an error that sent a client back to the page.
func roomMessage(ctx context.Context, code string) string {
	switch code {
	case "limit":
		return t(ctx, hub.ErrRoomLimit.Error())
	case "locked":
		return t(ctx, hub.ErrRoomLocked.Error())
	case "none":
		return t(ctx, hub.ErrRoomNoPicks.Error())
	case "claimed":
		return t(ctx, hub.ErrRoomClaimed.Error())
	case "name":
		return t(ctx, hub.ErrRoomName.Error())
	}
	return ""
}

func roomErrorCode(err error) string {
	switch {
	case errors.Is(err, hub.ErrRoomLimit):
		return "limit"
	case errors.Is(err, hub.ErrRoomLocked):
		return "locked"
	case errors.Is(err, hub.ErrRoomNoPicks):
		return "none"
	case errors.Is(err, hub.ErrRoomClaimed):
		return "claimed"
	case errors.Is(err, hub.ErrRoomName):
		return "name"
	}
	return ""
}

// Who a client is: a secret their browser keeps (and the app never shows anyone), hashed with the room's link, so it means
// nothing in another room and the photographer sees only the name the client gave.
func roomSecretCookie(r store.Room) string { return "handoff-roomid-" + r.ID[:8] }
func roomNameCookie(r store.Room) string   { return "handoff-roomname-" + r.ID[:8] }

func (w *Server) roomViewer(rw http.ResponseWriter, r *http.Request, room store.Room) hub.RoomViewer {
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	secret := ""
	if c, err := r.Cookie(roomSecretCookie(room)); err == nil && len(c.Value) == 32 {
		secret = c.Value
	}
	if secret == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err == nil {
			secret = hex.EncodeToString(b)
			http.SetCookie(rw, &http.Cookie{Name: roomSecretCookie(room), Value: secret, Path: "/", MaxAge: 365 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure})
		}
	}
	sum := sha256.Sum256([]byte("room-viewer|" + room.Token + "|" + secret))
	v := hub.RoomViewer{Who: hex.EncodeToString(sum[:])[:16]}
	if c, err := r.Cookie(roomNameCookie(room)); err == nil {
		v.Name = hub.CleanViewerName(c.Value)
	}
	return v
}

// roomAnswer answers a press on the page: JSON for the script, a return to the page for a form.
func (w *Server) roomAnswer(rw http.ResponseWriter, r *http.Request, room store.Room, back string, sel hub.RoomSelection, picked bool, err error) {
	ctx := r.Context()
	rw.Header().Set("Cache-Control", "no-store")
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		rw.Header().Set("Content-Type", "application/json")
		out := map[string]any{"count": sel.Count, "done": sel.Done, "text": pickCounter(ctx, sel.Count, sel.Limit), "picked": picked}
		if err != nil {
			rw.WriteHeader(http.StatusConflict)
			out["error"] = t(ctx, err.Error())
		}
		json.NewEncoder(rw).Encode(out)
		return
	}
	loc := "../../" + room.Token
	if back == "name" || back == "done" {
		loc = "../" + room.Token
	}
	if err != nil {
		loc += "?e=" + roomErrorCode(err)
	}
	rw.Header().Set("Location", loc)
	rw.WriteHeader(http.StatusSeeOther)
}

// roomPick adds a photo to the client's choice, or takes it away.
func (w *Server) roomPick(rw http.ResponseWriter, r *http.Request) {
	room, ok := w.roomOpen(rw, r)
	if !ok {
		return
	}
	v := w.roomViewer(rw, r, room)
	id := r.PathValue("id")
	before, _ := w.Hub.RoomSelectionOf(r.Context(), room, v)
	sel, err := w.Hub.RoomPick(r.Context(), room, v, id)
	if errors.Is(err, hub.ErrDropGone) {
		http.NotFound(rw, r)
		return
	}
	if err != nil && roomErrorCode(err) == "" {
		w.Log.Warn("a pick failed", "err", err)
		http.Error(rw, "try again", http.StatusBadGateway)
		return
	}
	if err != nil {
		sel = before
	}
	w.roomAnswer(rw, r, room, "pick", sel, sel.Picked[id], err)
}

// roomDone sends the client's choice to the photographer.
func (w *Server) roomDone(rw http.ResponseWriter, r *http.Request) {
	room, ok := w.roomOpen(rw, r)
	if !ok {
		return
	}
	v := w.roomViewer(rw, r, room)
	sel, err := w.Hub.RoomDone(r.Context(), room, v)
	if err != nil && roomErrorCode(err) == "" {
		w.Log.Warn("sending a choice failed", "err", err)
		http.Error(rw, "try again", http.StatusBadGateway)
		return
	}
	w.roomAnswer(rw, r, room, "done", sel, false, err)
}

// roomName keeps the name a client gives, and in a named room makes them the one who chooses.
func (w *Server) roomName(rw http.ResponseWriter, r *http.Request) {
	room, ok := w.roomOpen(rw, r)
	if !ok {
		return
	}
	r.ParseForm()
	v := w.roomViewer(rw, r, room)
	v.Name = hub.CleanViewerName(r.PostFormValue("name"))
	if v.Name != "" {
		http.SetCookie(rw, &http.Cookie{Name: roomNameCookie(room), Value: v.Name, Path: "/", MaxAge: 365 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteLaxMode,
			Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"})
	}
	err := w.Hub.RoomClaim(r.Context(), room, v)
	if err != nil && roomErrorCode(err) == "" {
		http.Error(rw, "try again", http.StatusBadGateway)
		return
	}
	w.roomAnswer(rw, r, room, "name", hub.RoomSelection{}, false, err)
}

// roomUnlock takes the PIN of a room and, if it is right, remembers that in the client's browser.
func (w *Server) roomUnlock(rw http.ResponseWriter, r *http.Request) {
	if w.Hub == nil {
		http.NotFound(rw, r)
		return
	}
	room, err := w.Hub.RoomFor(r.Context(), r.PathValue("token"))
	if err != nil || room.State != store.RoomActive || room.Password == "" {
		http.NotFound(rw, r)
		return
	}
	r.ParseForm()
	ok, err := w.Hub.TryRoomPIN(room, hub.DropWho(room.Token, w.clientAddr(r)), r.PostFormValue("pin"))
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	if err != nil {
		rw.Header().Set("Retry-After", "600")
		rw.WriteHeader(http.StatusTooManyRequests)
		RoomLocked(room.Token, false, true).Render(r.Context(), rw)
		return
	}
	if !ok {
		rw.WriteHeader(http.StatusUnauthorized)
		RoomLocked(room.Token, true, false).Render(r.Context(), rw)
		return
	}
	http.SetCookie(rw, &http.Cookie{Name: roomUnlockCookie(room), Value: hub.RoomUnlockToken(room), Path: "/", MaxAge: 7 * 24 * 3600, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"})
	rw.Header().Set("Location", "../"+room.Token) // relative, so it works under a path prefix
	rw.WriteHeader(http.StatusSeeOther)
}

// roomPhoto gives a client the picture of one photo: marked, never the plain preview.
func (w *Server) roomPhoto(rw http.ResponseWriter, r *http.Request) {
	room, ok := w.roomOpen(rw, r)
	if !ok {
		return
	}
	data, kind, err := w.Hub.RoomImage(r.Context(), room, r.PathValue("id"))
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
	rw.Header().Set("X-Robots-Tag", "noindex")
	rw.Write(data)
}

// markSummary says how a room's pictures are marked, for the photographer.
func markSummary(r store.Room) string {
	switch r.Mark {
	case "logo":
		return "logo"
	case "both":
		return "both:" + r.MarkText
	}
	return "text:" + r.MarkText
}

func (w *Server) reopenRoomChoice(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "choice reopened", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.ReopenRoomSelection(ctx, me.user, r.PostFormValue("room"), r.PostFormValue("who"))
	})
}

// roomReview is the photographer's page for the choices clients sent.
func (w *Server) roomReview(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	room, err := w.Hub.RoomOf(r.Context(), me.user.ID, r.PathValue("id"))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	review, err := w.Hub.RoomReview(r.Context(), room)
	if err != nil {
		http.Error(rw, "try again", http.StatusBadGateway)
		return
	}
	ctx := r.Context()
	l := loc(ctx)
	photos, _ := w.Hub.RoomPhotos(ctx, room)
	place := map[string]int{}
	for i, p := range photos {
		place[p.ID] = i + 1
	}
	d := ReviewData{User: me.user.Name, ID: room.ID, Name: room.Name, Released: w.Hub.RoomReleased(room), Downloads: room.Downloads, Auto: room.AutoRelease, Limit: room.PickLimit}
	if !room.ReleasedAt.IsZero() {
		d.ReleasedAt = l.Stamp(room.ReleasedAt.Local())
	}
	if !room.ReleaseUntil.IsZero() {
		d.Until = l.Stamp(room.ReleaseUntil.Local())
	}
	d.Replies = room.NotesMode == store.RoomNotesReplies
	d.Round, d.Several, d.Limit = room.CurrentRound(), room.MultiRound, room.PickLimit
	d.CanStart = room.MultiRound && room.ReleasedAt.IsZero() && room.CurrentRound() < 5
	if rounds, err := w.Hub.RoomRounds(ctx, room); err == nil {
		for _, rs := range rounds {
			hr := HistoryRound{Round: rs.Round}
			for _, s := range rs.Selections {
				hr.Lines = append(hr.Lines, t(ctx, "{name}: {n} photos", "name", selectionName(ctx, s.Name), "n", s.Count))
			}
			d.History = append(d.History, hr)
		}
	}
	for _, sel := range review {
		rs := ReviewSel{Name: sel.Name, Done: sel.Done}
		if sel.Done {
			rs.When = l.Stamp(sel.DoneAt.Local())
		}
		var names []string
		for _, p := range sel.Photos {
			rs.Photos = append(rs.Photos, ReviewPhoto{N: place[p.ID], ID: p.ID, File: p.Name, Who: sel.Who, Note: sel.Notes[p.ID].Body, Reply: sel.Notes[p.ID].Reply, Chosen: sel.Picked[p.ID]})
			if sel.Picked[p.ID] {
				names = append(names, p.Name)
			}
		}
		rs.Chosen = len(names)
		rs.Names = strings.Join(names, "\n")
		d.Selections = append(d.Selections, rs)
	}
	anyDone := false
	for _, sel := range d.Selections {
		anyDone = anyDone || sel.Done
	}
	d.CanStart = d.CanStart && anyDone
	rw.Header().Set("Cache-Control", "no-store")
	ctx = context.WithValue(ctx, adminKey{}, me.user.IsAdmin)
	RoomReviewPage(d).Render(ctx, rw)
}

// roomReviewPhoto gives the photographer the marked picture of a photo of their room, as a client sees it.
func (w *Server) roomReviewPhoto(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	room, err := w.Hub.RoomOf(r.Context(), me.user.ID, r.PathValue("id"))
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	data, kind, err := w.Hub.RoomImage(r.Context(), room, r.PathValue("pid"))
	if err != nil {
		if errors.Is(err, hub.ErrDropGone) {
			http.NotFound(rw, r)
		} else {
			http.Error(rw, "try again", http.StatusBadGateway)
		}
		return
	}
	rw.Header().Set("Content-Type", kind)
	rw.Header().Set("Cache-Control", "private, max-age=600")
	rw.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	rw.Write(data)
}

func (w *Server) releaseRoom(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "room released", func(ctx context.Context, me *member, _ string) error {
		days, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("days")))
		if err != nil {
			days = 30
		}
		return w.Hub.ReleaseRoom(ctx, me.sess, me.user, r.PostFormValue("room"), days)
	})
}

func (w *Server) withdrawRoom(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "release withdrawn", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.WithdrawRoom(ctx, me.sess, me.user, r.PostFormValue("room"))
	})
}

// roomOriginal gives a client the original of one photo of their own sent choice, once the photographer has released it.
func (w *Server) roomOriginal(rw http.ResponseWriter, r *http.Request) {
	room, ok := w.roomOpen(rw, r)
	if !ok {
		return
	}
	v := w.roomViewer(rw, r, room)
	resp, err := w.Hub.RoomOriginal(r.Context(), room, v, r.PathValue("id"))
	if err != nil {
		switch {
		case errors.Is(err, hub.ErrDropGone), errors.Is(err, hub.ErrNotReleased):
			http.NotFound(rw, r)
		case errors.Is(err, hub.ErrDropBusy):
			rw.Header().Set("Retry-After", "30")
			http.Error(rw, "busy, try again in a moment", http.StatusServiceUnavailable)
		default:
			http.Error(rw, "try again", http.StatusBadGateway)
		}
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Disposition"} {
		if v := resp.Header.Get(h); v != "" {
			rw.Header().Set(h, v)
		}
	}
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	rw.Header().Set("Cache-Control", "private, no-store")
	io.Copy(rw, resp.Body)
}

// roomZip streams the originals of the client's own sent choice as a ZIP.
func (w *Server) roomZip(rw http.ResponseWriter, r *http.Request) {
	room, ok := w.roomOpen(rw, r)
	if !ok {
		return
	}
	v := w.roomViewer(rw, r, room)
	if _, err := w.Hub.RoomDownloads(r.Context(), room, v); err != nil {
		http.NotFound(rw, r)
		return
	}
	cw := &countWriter{w: rw}
	n, err := w.Hub.RoomZip(r.Context(), room, v, headerOnce{cw, rw})
	if err != nil {
		switch {
		case cw.n == 0 && errors.Is(err, hub.ErrZipBusy):
			rw.Header().Set("Retry-After", "60")
			http.Error(rw, t(r.Context(), "Your photos are being got ready already, or too many people are asking at once. Try again in a minute."), http.StatusTooManyRequests)
		case cw.n == 0:
			w.Log.Warn("a room's ZIP failed before it began", "err", err)
			http.Error(rw, "try again", http.StatusBadGateway)
		default:
			w.Log.Warn("a room's ZIP failed half way", "err", err)
		}
		return
	}
	_ = n
}

// countWriter counts what was written, to tell a ZIP that never began from one that broke off.
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// headerOnce sets the headers of a ZIP when its first byte is written, so an answer that is refused before it begins is not a
// 200 with nothing in it.
type headerOnce struct {
	*countWriter
	rw http.ResponseWriter
}

func (h headerOnce) Write(p []byte) (int, error) {
	if h.n == 0 {
		h.rw.Header().Set("Content-Type", "application/zip")
		h.rw.Header().Set("Content-Disposition", `attachment; filename="photos.zip"`)
		h.rw.Header().Set("Cache-Control", "private, no-store")
		h.rw.Header().Set("X-Content-Type-Options", "nosniff")
	}
	return h.countWriter.Write(p)
}

// roomNote keeps the client's note on a photo (an empty text takes it away).
func (w *Server) roomNote(rw http.ResponseWriter, r *http.Request) {
	room, ok := w.roomOpen(rw, r)
	if !ok {
		return
	}
	v := w.roomViewer(rw, r, room)
	r.ParseForm()
	n, err := w.Hub.RoomNote(r.Context(), room, v, r.PathValue("id"), r.PostFormValue("text"))
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Content-Type", "application/json")
	if errors.Is(err, hub.ErrDropGone) {
		http.NotFound(rw, r)
		return
	}
	if err != nil {
		if hub.IsUserError(err) {
			rw.WriteHeader(http.StatusConflict)
			json.NewEncoder(rw).Encode(map[string]any{"error": t(r.Context(), err.Error())})
			return
		}
		w.Log.Warn("a note failed", "err", err)
		http.Error(rw, "try again", http.StatusBadGateway)
		return
	}
	json.NewEncoder(rw).Encode(map[string]any{"count": n})
}

func (w *Server) replyToNote(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "note answered", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.RoomReply(ctx, me.user, r.PostFormValue("room"), r.PostFormValue("who"), r.PostFormValue("photo"), r.PostFormValue("text"))
	})
}

func (w *Server) startRound(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "round started", func(ctx context.Context, me *member, _ string) error {
		limit, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("limit")))
		return w.Hub.StartRoomRound(ctx, me.user, r.PostFormValue("room"), limit)
	})
}
