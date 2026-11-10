package hub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/vfuoglio/immich-handoff/internal/bridge"
	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
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
		return nil, "its receiver on another server cannot be used (see the log)"
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

// Errors about bridges, shown as they are (the page words them for the receiver).
var (
	ErrBridgesOff     = errors.New("connecting to other servers is not switched on")
	ErrBridgeInvite   = errors.New("this invitation is not valid any more")
	ErrBadBridgeName  = errors.New("a name needs 1 to 40 characters")
	ErrBridgeAttempts = errors.New("too many attempts; try again later")
)

// bridgeInviteLife is how long an invitation to a server stays open.
const bridgeInviteLife = 7 * 24 * time.Hour

// BridgeInviteInfo is what the invitation page may say: the service and who offers it, nothing else.
type BridgeInviteInfo struct{ Service, Owner string }

// NewBridgeInvite makes the link the owner of a service sends to someone on another Immich server. An older
// one stops working.
func (h *Hub) NewBridgeInvite(ctx context.Context, owner immich.User, service string) (string, error) {
	if !h.Bridge.Enabled {
		return "", ErrBridgesOff
	}
	if _, err := h.ownedService(ctx, owner, service); err != nil {
		return "", err
	}
	token := newToken()
	if err := h.Store.SetBridgeInvite(ctx, service, token); err != nil {
		return "", err
	}
	h.event(ctx, service, owner.ID, "invite", i18n.Pack("{name} made a link for someone on another Immich server to receive {service}", "name", owner.Name, "service", service))
	return token, nil
}

// BridgeInviteOf is the open invitation of a service the person owns, or "".
func (h *Hub) BridgeInviteOf(ctx context.Context, service string) string {
	if h.Store == nil || !h.Bridge.Enabled {
		return ""
	}
	if i, err := h.Store.BridgeInviteOf(ctx, service); err == nil && time.Since(i.CreatedAt) < bridgeInviteLife {
		return i.Token
	}
	return ""
}

// BridgeInvite says what an invitation is for, or ErrBridgeInvite for one that is unknown, replaced, used or old, and
// for a service that is gone (the same answer for all, so the page tells nothing to a guesser).
func (h *Hub) BridgeInvite(ctx context.Context, token string) (BridgeInviteInfo, error) {
	if h.Store == nil || !h.Bridge.Enabled || token == "" {
		return BridgeInviteInfo{}, ErrBridgeInvite
	}
	i, err := h.Store.BridgeInviteByToken(ctx, token)
	if err != nil || time.Since(i.CreatedAt) > bridgeInviteLife {
		return BridgeInviteInfo{}, ErrBridgeInvite
	}
	all, err := h.Store.Services(ctx)
	if err != nil {
		return BridgeInviteInfo{}, err
	}
	for _, v := range all {
		if v.Name == i.Service {
			return BridgeInviteInfo{Service: v.Name, Owner: v.OwnerName}, nil
		}
	}
	return BridgeInviteInfo{}, ErrBridgeInvite
}

// bridgeGuard counts the attempts of people who have no account, in memory (it is about the last hour).
type bridgeGuard struct {
	mu   sync.Mutex
	seen map[string][]time.Time
}

// AllowBridgeAttempt limits how often an address, and an invitation, may try to connect: each attempt makes the app
// contact an address a stranger typed, so it is rationed. Ten an hour per address and twenty per invitation.
func (h *Hub) AllowBridgeAttempt(who, token string) bool {
	g := &h.bridgeTries
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.seen == nil {
		g.seen = map[string][]time.Time{}
	}
	now := time.Now()
	if len(g.seen) > 20000 {
		for k, v := range g.seen {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > time.Hour {
				delete(g.seen, k)
			}
		}
	}
	ok := true
	for _, c := range []struct {
		key   string
		limit int
	}{{"ip|" + who, 10}, {"token|" + token, 20}} {
		recent := within(g.seen[c.key], time.Hour, now)
		if len(recent) >= c.limit {
			ok = false
		}
		g.seen[c.key] = recent
	}
	if ok {
		for _, k := range []string{"ip|" + who, "token|" + token} {
			g.seen[k] = append(g.seen[k], now)
		}
	}
	return ok
}

// AcceptBridge connects the service to the server the receiver named. Nothing is stored until the address passes the
// rules and the server and key pass the checks; the invitation is used up only then, so a mistake can be corrected
// on the same page.
func (h *Hub) AcceptBridge(ctx context.Context, token, address, key, name string) error {
	info, err := h.BridgeInvite(ctx, token)
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if n := len([]rune(name)); n < 1 || n > 40 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return ErrBadBridgeName
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return &bridge.Failure{Kind: bridge.ErrRevoked}
	}
	addr, err := h.Bridge.Policy.Address(address)
	if err != nil {
		return err
	}
	hc := h.Bridge.Policy.HTTPClient()
	defer hc.CloseIdleConnections()
	got, err := bridge.Check(ctx, hc, addr, key)
	if err != nil {
		return err
	}
	if err := h.Store.SetBridge(ctx, store.Bridge{Service: info.Service, Address: addr, Key: key, DisplayName: name, Version: got.Version, CheckedAt: time.Now()}); err != nil {
		return err
	}
	h.Store.DeleteBridgeInvite(ctx, token)
	var owner store.Service
	if all, err := h.Store.Services(ctx); err == nil {
		for _, v := range all {
			if v.Name == info.Service {
				owner = v
			}
		}
	}
	h.event(ctx, info.Service, owner.OwnerID, "joined", i18n.Pack("{name} on another Immich server will now receive {service}", "name", name, "service", info.Service))
	h.show(ctx, live.Restored, info.Service, "", "service", true)
	h.tell(fmt.Sprintf("%s on another Immich server now receives %q.", name, info.Service))
	h.Log.Info("a receiver on another server was connected", "service", info.Service, "version", got.Version)
	if err := h.Reload(ctx); err != nil {
		return err
	}
	if h.Wake != nil {
		h.Wake("bridge connected: " + info.Service)
	}
	return nil
}

// BridgeStatus is what the owner's page shows of a service's receiver on another server.
type BridgeStatus struct {
	Name, State, Note, Version string
	Checked, RetryAt           time.Time
}

// BridgeStatus returns the state of the service's receiver on another server.
func (h *Hub) BridgeStatus(ctx context.Context, service string) (BridgeStatus, bool) {
	if h.Store == nil {
		return BridgeStatus{}, false
	}
	b, err := h.Store.Bridge(ctx, service)
	if err != nil {
		return BridgeStatus{}, false
	}
	return BridgeStatus{Name: b.DisplayName, State: b.State, Note: b.Note, Version: b.Version, Checked: b.CheckedAt, RetryAt: b.RetryAt}, true
}

// ErrNoBridge: the service has no receiver on another server.
var ErrNoBridge = errors.New("this service has no receiver on another server")

// RemoveBridge ends the connection to the service's receiver on another server. Photos that were waiting for it then
// go to the service's own receiver alone; what already arrived on the other server stays there, and the receiver can
// delete their key when they like.
func (h *Hub) RemoveBridge(ctx context.Context, owner immich.User, service string) error {
	if _, err := h.ownedService(ctx, owner, service); err != nil {
		return err
	}
	b, err := h.Store.Bridge(ctx, service)
	if err != nil {
		return ErrNoBridge
	}
	if err := h.Store.RemoveBridge(ctx, service); err != nil {
		return err
	}
	h.mu.Lock()
	if t := h.retries[service]; t != nil {
		t.Stop()
		delete(h.retries, service)
	}
	h.mu.Unlock()
	h.event(ctx, service, owner.ID, "left", i18n.Pack("{name} stopped sending {service} to {receiver} on another server", "name", owner.Name, "service", service, "receiver", b.DisplayName))
	h.show(ctx, live.Restored, service, "", "service", true)
	if err := h.Reload(ctx); err != nil {
		return err
	}
	if h.Wake != nil {
		h.Wake("bridge removed: " + service)
	}
	return nil
}
