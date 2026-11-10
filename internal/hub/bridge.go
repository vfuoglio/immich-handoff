package hub

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/bridge"
	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/live"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// bridgeBackoff is how long an unreachable server is left alone after its n-th failure in a row: a minute, doubling,
// up to an hour. A variable so that tests can shorten it.
var bridgeBackoff = func(failures int) time.Duration {
	d := time.Minute << min(max(failures-1, 0), 6)
	return min(d, time.Hour)
}

// remoteEntry is a receiver on another server, kept between reloads so its connections are reused. It is replaced
// when the address or the key changes.
type remoteEntry struct {
	address, key string
	remote       *bridge.Remote
}

// remoteFor returns the receiver for a stored bridge, or nil and the reason the service cannot run. Called with h.mu
// held, from Reload.
func (h *Hub) remoteFor(b store.Bridge) (syncer.Remote, string) {
	if !h.Bridge.Enabled {
		return nil, "bridges to other servers are switched off (BRIDGE_ENABLE); its receiver on another server is kept, not forgotten"
	}
	if h.remotes == nil {
		h.remotes = map[string]*remoteEntry{}
	}
	if e, ok := h.remotes[b.Service]; ok && e.address == b.Address && e.key == b.Key {
		return e.remote, ""
	}
	r, err := bridge.NewRemote(b.DisplayName, b.Address, b.Key, h.Bridge.Policy)
	if err != nil {
		h.Log.Error("cannot use the stored bridge", "service", b.Service, "err", err)
		return nil, "its receiver on another server cannot be used"
	}
	service := b.Service
	r.Gate = func() error { return h.bridgeGate(service) }
	r.Report = func(err error, version string) { h.bridgeReport(service, err, version) }
	h.remotes[b.Service] = &remoteEntry{address: b.Address, key: b.Key, remote: r}
	return r, ""
}

// bridgeGate holds a pass back, without contacting the other server, while its state says it would be pointless: a
// revoked key is not tried again (only a new key ends that), and an unreachable server waits for its retry time.
func (h *Hub) bridgeGate(service string) error {
	b, err := h.Store.Bridge(context.Background(), service)
	if err != nil {
		return nil // the next reload drops the receiver; nothing to hold
	}
	switch {
	case b.State == store.BridgeRevoked:
		return fmt.Errorf("%w; the receiver must make a new key (no retries until then)", bridge.ErrRevoked)
	case b.State == store.BridgeUnreachable && time.Now().Before(b.RetryAt):
		return fmt.Errorf("%w; trying again at %s", bridge.ErrUnreachable, b.RetryAt.Local().Format("15:04"))
	}
	return nil
}

// bridgeReport records how a check or a hand-over to the other server ended and, when the state changed, tells the
// people concerned once: the activity log, the open pages and the administrator's notification.
func (h *Hub) bridgeReport(service string, err error, version string) {
	ctx := context.Background()
	b, e := h.Store.Bridge(ctx, service)
	if e != nil {
		return
	}
	var owner store.Service
	if all, e := h.Store.Services(ctx); e == nil {
		for _, v := range all {
			if v.Name == service {
				owner = v
			}
		}
	}
	name := b.DisplayName
	switch {
	case err == nil:
		if b.State == store.BridgeOK {
			if version != "" && version != b.Version {
				h.Store.SetBridgeState(ctx, service, store.BridgeOK, "", version, 0, time.Time{})
			}
			return
		}
		if changed, _ := h.Store.SetBridgeState(ctx, service, store.BridgeOK, "", version, 0, time.Time{}); changed {
			h.event(ctx, service, owner.OwnerID, "restored", i18n.Pack("{service} can reach {name} on another server again", "service", service, "name", name))
			h.show(ctx, live.Restored, service, "", "service", true)
			h.tell(fmt.Sprintf("%q reaches %s on another server again; waiting photos are going.", service, name))
			h.Log.Info("the receiver on another server is back", "service", service)
			if h.Wake != nil {
				go h.Wake("bridge restored: " + service)
			}
		}
	case errors.Is(err, bridge.ErrRevoked):
		changed, _ := h.Store.SetBridgeState(ctx, service, store.BridgeRevoked, "The other server does not accept the key any more.", version, 0, time.Time{})
		if changed {
			h.event(ctx, service, owner.OwnerID, "broken", i18n.Pack("{name} on another server no longer accepts the key of {service}", "name", name, "service", service))
			h.show(ctx, live.Lost, service, "", "service", true)
			h.tell(fmt.Sprintf("The key for %s on another server (%q) was revoked; photos wait, still tagged, until they make a new one.", name, service))
			h.Log.Warn("the other server rejected the bridge key; photos wait", "service", service)
		}
	case errors.Is(err, bridge.ErrBadAddress):
		h.bridgeUnreachable(ctx, b, owner, "The app may not connect to that address: "+err.Error(), version)
	case isConnectionProblem(err):
		h.bridgeUnreachable(ctx, b, owner, err.Error(), version)
	}
	// anything else (a refusal of one photo) is not about the connection: that photo fails, the state stays
}

// isConnectionProblem: the reasons for which the other server cannot be used for now, and may be later.
func isConnectionProblem(err error) bool {
	for _, target := range []error{bridge.ErrUnreachable, bridge.ErrRedirect, bridge.ErrNotImmich, bridge.ErrVersion, bridge.ErrKeyTooBroad, bridge.ErrKeyTooSmall} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// bridgeUnreachable records a failure and schedules the next try. Failures within one pass (every photo of a burst
// fails the same way) count once: only a failure after the retry time grows the backoff.
func (h *Hub) bridgeUnreachable(ctx context.Context, b store.Bridge, owner store.Service, why, version string) {
	failures, retry := b.Failures, b.RetryAt
	if b.State != store.BridgeUnreachable || !time.Now().Before(b.RetryAt) {
		failures++
		retry = time.Now().Add(bridgeBackoff(failures))
	}
	changed, err := h.Store.SetBridgeState(ctx, b.Service, store.BridgeUnreachable, why, version, failures, retry)
	if err != nil {
		return
	}
	if changed {
		h.event(ctx, b.Service, owner.OwnerID, "broken", i18n.Pack("{service} cannot reach {name} on another server: photos wait", "service", b.Service, "name", b.DisplayName))
		h.show(ctx, live.Lost, b.Service, "", "service", true)
		h.tell(fmt.Sprintf("%q cannot reach %s on another server (%s); photos wait, still tagged, and it tries again.", b.Service, b.DisplayName, why))
		h.Log.Warn("the receiver on another server cannot be used; photos wait", "service", b.Service, "why", why)
	}
	h.bridgeRetryAt(b.Service, retry)
}

// bridgeRetryAt wakes a pass at t, once per service, so waiting photos go when the server is back without anyone
// tagging anything.
func (h *Hub) bridgeRetryAt(service string, t time.Time) {
	if h.Wake == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.retries == nil {
		h.retries = map[string]*time.Timer{}
	}
	if old := h.retries[service]; old != nil {
		old.Stop()
	}
	h.retries[service] = time.AfterFunc(time.Until(t)+time.Second, func() { h.Wake("bridge retry: " + service) })
}
