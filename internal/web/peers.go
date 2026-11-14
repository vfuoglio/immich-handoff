package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/hub"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// The web side of sharing albums with people on other servers (docs/shared-albums-design.md): the photographer's, or anyone's,
// page for inviting and joining, and the endpoints the other app calls. The other app's calls need no Immich sign-in: the
// invitation steps are guarded by a one-time token, everything after by the signature.

func peerJSON(rw http.ResponseWriter, status int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(status)
	json.NewEncoder(rw).Encode(v)
}

// peerFail answers a refusal in JSON, with the status that tells the other app what kind it was.
func (w *Server) peerFail(rw http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, hub.ErrPeerInviteGone), errors.Is(err, hub.ErrSharingOff), errors.Is(err, hub.ErrNoSharedAlbum):
		status = http.StatusNotFound
	case errors.Is(err, hub.ErrPeerAuth):
		status = http.StatusUnauthorized
	case !hub.IsUserError(err):
		w.Log.Warn("a peer request failed", "err", err)
		status = http.StatusInternalServerError
	}
	peerJSON(rw, status, map[string]string{"error": err.Error()})
}

// peerInviteInfo is what the other app asks first: who made this invitation.
func (w *Server) peerInviteInfo(rw http.ResponseWriter, r *http.Request) {
	if w.Hub == nil {
		http.NotFound(rw, r)
		return
	}
	info, err := w.Hub.InviteInfo(r.Context(), r.PathValue("token"))
	if err != nil {
		w.peerFail(rw, err)
		return
	}
	peerJSON(rw, http.StatusOK, info)
}

// peerJoin takes the other app's request to join.
func (w *Server) peerJoin(rw http.ResponseWriter, r *http.Request) {
	if w.Hub == nil {
		http.NotFound(rw, r)
		return
	}
	var req hub.JoinRequest
	if json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req) != nil {
		peerJSON(rw, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	reply, err := w.Hub.PeerJoin(r.Context(), r.PathValue("token"), req)
	if err != nil {
		w.peerFail(rw, err)
		return
	}
	peerJSON(rw, http.StatusOK, reply)
}

// signed runs a handler for a request from a peer, after the signature has been checked.
func (w *Server) signed(next func(rw http.ResponseWriter, r *http.Request, p store.Peer, body []byte)) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if w.Hub == nil {
			http.NotFound(rw, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			peerJSON(rw, http.StatusBadRequest, map[string]string{"error": "bad request"})
			return
		}
		p, err := w.Hub.PeerAuth(r.Context(), r.Header.Get("X-Handoff-Peer"), r.Header, r.Method, r.URL.EscapedPath(), body)
		if err != nil {
			w.peerFail(rw, hub.ErrPeerAuth)
			return
		}
		next(rw, r, p, body)
	}
}

func (w *Server) peerHello(rw http.ResponseWriter, r *http.Request, p store.Peer, body []byte) {
	var req hub.HelloRequest
	json.Unmarshal(body, &req)
	peerJSON(rw, http.StatusOK, w.Hub.PeerHello(r.Context(), p, req))
}

func (w *Server) peerLeave(rw http.ResponseWriter, r *http.Request, p store.Peer, _ []byte) {
	w.Hub.PeerLeft(r.Context(), p)
	peerJSON(rw, http.StatusOK, map[string]bool{"ok": true})
}

// peerInvitePage is the page the invitation link opens in a browser: it says what the link is for. It joins nothing; the other
// person gives the link to their own app.
func (w *Server) peerInvitePage(rw http.ResponseWriter, r *http.Request) {
	if w.Hub == nil {
		http.NotFound(rw, r)
		return
	}
	info, err := w.Hub.InviteInfo(r.Context(), r.PathValue("token"))
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	rw.Header().Set("X-Robots-Tag", "noindex")
	if err != nil {
		rw.WriteHeader(http.StatusNotFound)
		PeerInviteGone().Render(r.Context(), rw)
		return
	}
	PeerInvitePage(info.Name, r.URL.String()).Render(r.Context(), rw)
}

// --- the person's own side ---

// PeerView is one connection on the Services tab.
type PeerView struct {
	ID, Name, Host     string
	Role, State        string
	Shared             []SharedView
	IPaused, TheyPause bool
	Seen, Problem      string
}

func (w *Server) peerViews(ctx context.Context, me *member) []PeerView {
	list, err := w.Hub.Peers(ctx, me.user.ID)
	if err != nil {
		w.Log.Warn("could not list connections", "err", err)
		return nil
	}
	l := loc(ctx)
	var out []PeerView
	for _, p := range list {
		v := PeerView{ID: p.ID, Name: p.Name, Role: p.Role, State: p.State, IPaused: p.IPaused, TheyPause: p.TheyPaused, Problem: p.Problem}
		if u, err := url.Parse(p.Address); err == nil {
			v.Host = u.Host + u.Path
		}
		if !p.SeenAt.IsZero() {
			v.Seen = l.Stamp(p.SeenAt.Local())
		}
		if shared, err := w.Hub.Store.SharedAlbumsWith(ctx, p.ID); err == nil {
			for _, a := range shared {
				sv := SharedView{ID: a.ID, Name: a.Name, Album: a.AlbumName, State: a.State, Proposer: a.Proposer, History: a.History, TheirHistory: a.TheirHistory, Deletion: a.Deletion, Peer: p.Name, Problem: a.Problem, Sent: a.Sent, Received: a.Received, Pending: len(a.Pending)}
				if !a.LastSync.IsZero() {
					sv.Last = l.Stamp(a.LastSync.Local())
				}
				v.Shared = append(v.Shared, sv)
			}
		}
		out = append(out, v)
	}
	return out
}

// SharedView is one shared album on the Services tab.
type SharedView struct {
	ID, Name, Album, State, Peer string
	Proposer                     bool
	History, TheirHistory        string
	Deletion                     string
	Last, Problem                string
	Sent, Received               int
	Pending                      int // photos taken out that wait for the person's yes
}

func (w *Server) newPeerInvite(rw http.ResponseWriter, r *http.Request) {
	me := w.guarded(rw, r)
	if me == nil {
		return
	}
	if w.Hub == nil {
		http.NotFound(rw, r)
		return
	}
	link, until, err := w.Hub.NewPeerInvite(r.Context(), me.user)
	if err != nil {
		w.peerUserError(rw, r, err)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	PeerInviteLink(link, loc(r.Context()).Stamp(until.Local())).Render(r.Context(), rw)
}

func (w *Server) peerUserError(rw http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	if hub.IsUserError(err) {
		status = http.StatusConflict
	} else {
		w.Log.Warn("sharing albums failed", "err", err)
	}
	rw.Header().Set("HX-Reswap", "innerHTML")
	rw.WriteHeader(status)
	rw.Write([]byte(t(r.Context(), "Could not do that: {reason}", "reason", t(r.Context(), err.Error()))))
}

// previewPeerInvite asks the other app who an invitation is from, before the person decides.
func (w *Server) previewPeerInvite(rw http.ResponseWriter, r *http.Request) {
	me := w.guarded(rw, r)
	if me == nil {
		return
	}
	link := strings.TrimSpace(r.PostFormValue("link"))
	info, base, err := w.Hub.PreviewPeerInvite(r.Context(), link)
	if err != nil {
		w.peerUserError(rw, r, err)
		return
	}
	u, _ := url.Parse(base)
	rw.Header().Set("Cache-Control", "no-store")
	PeerPreview(info.Name, u.Host+u.Path, link).Render(r.Context(), rw)
}

func (w *Server) acceptPeerInvite(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "connection made", func(ctx context.Context, me *member, _ string) error {
		_, err := w.Hub.AcceptPeerInvite(ctx, me.user, strings.TrimSpace(r.PostFormValue("link")))
		return err
	})
}

func (w *Server) pausePeer(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "connection paused", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.SetPeerPaused(ctx, me.user, r.PostFormValue("peer"), r.PostFormValue("paused") == "1")
	})
}

func (w *Server) removePeer(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "connection removed", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.RemovePeer(ctx, me.sess, me.user, r.PostFormValue("peer"))
	})
}

// --- signed messages about a shared album ---

func (w *Server) peerAlbumPropose(rw http.ResponseWriter, r *http.Request, p store.Peer, body []byte) {
	var m hub.ProposeMsg
	if json.Unmarshal(body, &m) != nil {
		peerJSON(rw, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if err := w.Hub.PeerPropose(r.Context(), p, m); err != nil {
		w.peerFail(rw, err)
		return
	}
	peerJSON(rw, http.StatusOK, map[string]bool{"ok": true})
}

func (w *Server) peerAlbumAccept(rw http.ResponseWriter, r *http.Request, p store.Peer, body []byte) {
	var m hub.AcceptMsg
	if json.Unmarshal(body, &m) != nil {
		peerJSON(rw, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if err := w.Hub.PeerAccepted(r.Context(), p, m); err != nil {
		w.peerFail(rw, err)
		return
	}
	peerJSON(rw, http.StatusOK, map[string]bool{"ok": true})
}

func (w *Server) peerAlbumAnswer(f func(*hub.Hub, context.Context, store.Peer, hub.AnswerMsg) error) func(http.ResponseWriter, *http.Request, store.Peer, []byte) {
	return func(rw http.ResponseWriter, r *http.Request, p store.Peer, body []byte) {
		var m hub.AnswerMsg
		if json.Unmarshal(body, &m) != nil {
			peerJSON(rw, http.StatusBadRequest, map[string]string{"error": "bad request"})
			return
		}
		if err := f(w.Hub, r.Context(), p, m); err != nil {
			w.peerFail(rw, err)
			return
		}
		peerJSON(rw, http.StatusOK, map[string]bool{"ok": true})
	}
}

// --- the person's side ---

// shareAlbums lists the person's own albums for the forms that propose or accept a shared album, with "a new album" first.
func (w *Server) shareAlbums(rw http.ResponseWriter, r *http.Request) {
	me, code, msg := w.authenticate(r)
	if me == nil {
		http.Error(rw, msg, code)
		return
	}
	albums, err := me.sess.OwnedAlbums(r.Context())
	if err != nil {
		http.Error(rw, "could not list your albums: "+err.Error(), http.StatusBadGateway)
		return
	}
	opts := make([]AlbumOption, 0, len(albums))
	for _, a := range albums {
		opts = append(opts, AlbumOption{ID: a.ID.String(), Label: albumLabel(r.Context(), a.Name, a.Count)})
	}
	rw.Header().Set("Cache-Control", "no-store")
	ShareAlbumSelect(opts, r.URL.Query().Get("for")).Render(r.Context(), rw)
}

func shareParams(r *http.Request) hub.ShareParams {
	p := hub.ShareParams{PeerID: r.PostFormValue("peer"), Name: r.PostFormValue("name"), History: r.PostFormValue("history"), Deletion: r.PostFormValue("deletion")}
	if id, err := uuid.Parse(r.PostFormValue("album")); err == nil {
		p.AlbumID = id
	}
	return p
}

func (w *Server) proposeAlbum(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "album proposed", func(ctx context.Context, me *member, _ string) error {
		_, err := w.Hub.ProposeAlbum(ctx, me.sess, me.user, shareParams(r))
		return err
	})
}

func (w *Server) acceptAlbum(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "album accepted", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.AcceptAlbum(ctx, me.sess, me.user, r.PostFormValue("shared"), shareParams(r))
	})
}

func (w *Server) declineAlbum(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "album declined", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.DeclineAlbum(ctx, me.user, r.PostFormValue("shared"))
	})
}

func (w *Server) leaveAlbum(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "album sharing ended", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.LeaveSharedAlbum(ctx, me.sess, me.user, r.PostFormValue("shared"))
	})
}

func (w *Server) peerAlbumHave(rw http.ResponseWriter, r *http.Request, p store.Peer, body []byte) {
	var m hub.HaveMsg
	if json.Unmarshal(body, &m) != nil {
		peerJSON(rw, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	reply, err := w.Hub.PeerHave(r.Context(), p, m)
	if err != nil {
		w.peerFail(rw, err)
		return
	}
	peerJSON(rw, http.StatusOK, reply)
}

// peerAlbumPhoto takes a photo the other app streams: the request is signed (the path and the query, which carry the checksum),
// the body is the photo itself and goes straight on to Immich.
func (w *Server) peerAlbumPhoto(rw http.ResponseWriter, r *http.Request) {
	if w.Hub == nil {
		http.NotFound(rw, r)
		return
	}
	p, err := w.Hub.PeerAuth(r.Context(), r.Header.Get("X-Handoff-Peer"), r.Header, r.Method, r.URL.RequestURI(), nil)
	if err != nil {
		w.peerFail(rw, hub.ErrPeerAuth)
		return
	}
	q := r.URL.Query()
	taken, _ := time.Parse(time.RFC3339Nano, q.Get("taken"))
	if taken.IsZero() || taken.After(time.Now().Add(24*time.Hour)) {
		taken = time.Now()
	}
	size, _ := strconv.ParseInt(q.Get("size"), 10, 64)
	err = w.Hub.PeerPhoto(r.Context(), p, hub.PhotoIn{To: q.Get("to"), Checksum: q.Get("checksum"), Name: q.Get("name"), Taken: taken, Size: size, Body: r.Body})
	if err != nil {
		if errors.Is(err, hub.ErrSharedTooBig) {
			peerJSON(rw, http.StatusRequestEntityTooLarge, map[string]string{"error": err.Error()})
			return
		}
		w.peerFail(rw, err)
		return
	}
	peerJSON(rw, http.StatusOK, map[string]bool{"ok": true})
}

func (w *Server) syncAlbumNow(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "album looked at", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.SyncSharedNow(ctx, me.user.ID, r.PostFormValue("shared"))
	})
}

func (w *Server) peerAlbumDone(rw http.ResponseWriter, r *http.Request, p store.Peer, body []byte) {
	var m hub.DoneMsg
	if json.Unmarshal(body, &m) != nil {
		peerJSON(rw, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if err := w.Hub.PeerArrived(r.Context(), p, m); err != nil {
		w.peerFail(rw, err)
		return
	}
	peerJSON(rw, http.StatusOK, map[string]bool{"ok": true})
}

func (w *Server) peerAlbumRemoved(rw http.ResponseWriter, r *http.Request, p store.Peer, body []byte) {
	var m hub.RemovedMsg
	if json.Unmarshal(body, &m) != nil {
		peerJSON(rw, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if err := w.Hub.PeerRemoved(r.Context(), p, m); err != nil {
		w.peerFail(rw, err)
		return
	}
	peerJSON(rw, http.StatusOK, map[string]bool{"ok": true})
}

func (w *Server) answerRemoval(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "removal answered", func(ctx context.Context, me *member, _ string) error {
		return w.Hub.AnswerRemoval(ctx, me.user, r.PostFormValue("shared"), r.PostFormValue("do") == "follow")
	})
}
