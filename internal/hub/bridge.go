package hub

import (
	"github.com/vfuoglio/immich-handoff/internal/bridge"
	"github.com/vfuoglio/immich-handoff/internal/store"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

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
	h.remotes[b.Service] = &remoteEntry{address: b.Address, key: b.Key, remote: r}
	return r, ""
}
