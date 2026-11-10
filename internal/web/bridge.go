package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/vfuoglio/immich-handoff/internal/bridge"
	"github.com/vfuoglio/immich-handoff/internal/hub"
)

// newBridgeInvite makes the link an owner sends to someone on another Immich server.
func (w *Server) newBridgeInvite(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "bridge invitation", func(ctx context.Context, me *member, service string) error {
		_, err := w.Hub.NewBridgeInvite(ctx, me.user, service)
		return err
	})
}

func (w *Server) bridgeClosed(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(http.StatusNotFound)
	Notice(t(r.Context(), "This invitation is not open"), t(r.Context(), "It may have been used, replaced or have expired. Ask the person who sent it for a new one."), true).Render(r.Context(), rw)
}

func (w *Server) bridgeView(r *http.Request, info hub.BridgeInviteInfo) BridgePageView {
	return BridgePageView{Token: r.PathValue("token"), Service: info.Service, Owner: info.Owner, Permissions: bridge.RequiredPermissions}
}

// bridgePage is where an invitation to another server leads. It needs no login; it says only the service and who offers
// it, and the same page answers a link that is unknown, used or old.
func (w *Server) bridgePage(rw http.ResponseWriter, r *http.Request) {
	if w.Hub == nil {
		w.bridgeClosed(rw, r)
		return
	}
	info, err := w.Hub.BridgeInvite(r.Context(), r.PathValue("token"))
	if err != nil {
		w.bridgeClosed(rw, r)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	BridgePage(w.bridgeView(r, info)).Render(r.Context(), rw)
}

// bridgeConnect checks what the receiver entered and connects the service. Each attempt makes the app contact an
// address a stranger typed, so attempts are rationed per address and per invitation.
func (w *Server) bridgeConnect(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if w.Hub == nil {
		w.bridgeClosed(rw, r)
		return
	}
	token := r.PathValue("token")
	info, err := w.Hub.BridgeInvite(ctx, token)
	if err != nil {
		w.bridgeClosed(rw, r)
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	view := w.bridgeView(r, info)
	view.Address, view.Name = r.PostFormValue("address"), r.PostFormValue("name")
	if !w.Hub.AllowBridgeAttempt(w.clientAddr(r), token) {
		rw.WriteHeader(http.StatusTooManyRequests)
		view.Error = bridgeReason(ctx, hub.ErrBridgeAttempts)
		BridgePage(view).Render(ctx, rw)
		return
	}
	if err := w.Hub.AcceptBridge(ctx, token, view.Address, r.PostFormValue("key"), view.Name); err != nil {
		if !hub.IsUserError(err) {
			w.Log.Warn("connecting a receiver on another server failed", "service", info.Service, "err", err)
		}
		rw.WriteHeader(http.StatusConflict)
		view.Error = bridgeReason(ctx, err)
		BridgePage(view).Render(ctx, rw)
		return
	}
	view.Done = true
	BridgePage(view).Render(ctx, rw)
}

// bridgeReason words a refusal for the person who typed the address. The sentence is translated; what follows it is
// only names and numbers (the permissions the key has too many of, a version, the address a server redirects to), and
// it never says anything about the app's own network beyond the sentence.
func bridgeReason(ctx context.Context, err error) string {
	for _, kind := range []error{bridge.ErrBadAddress, bridge.ErrRedirect, bridge.ErrNotImmich, bridge.ErrVersion, bridge.ErrKeyTooBroad,
		bridge.ErrKeyTooSmall, bridge.ErrRevoked, bridge.ErrUnreachable, hub.ErrBridgeInvite, hub.ErrBadBridgeName, hub.ErrBridgeAttempts, hub.ErrBridgesOff} {
		if !errors.Is(err, kind) {
			continue
		}
		out := t(ctx, kind.Error())
		var f *bridge.Failure
		if errors.As(err, &f) && f.Detail != "" {
			out += " (" + f.Detail + ")"
		}
		return out
	}
	return t(ctx, "Something went wrong on our side. Try again later.")
}

// removeBridge ends the connection to a service's receiver on another server.
func (w *Server) removeBridge(rw http.ResponseWriter, r *http.Request) {
	w.membership(rw, r, "bridge removal", func(ctx context.Context, me *member, service string) error {
		return w.Hub.RemoveBridge(ctx, me.user, service)
	})
}
