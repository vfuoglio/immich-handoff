package hub

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"sync"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/store"
)

// The PIN of a client room: given once on the room's page, remembered in the client's browser as a token made from the PIN's
// stored hash (which no client can see), so it cannot be made up and stops working when the PIN changes.

// RoomUnlockToken is what a client's browser holds after giving the right PIN.
func RoomUnlockToken(r store.Room) string {
	sum := sha256.Sum256([]byte("room-unlock|" + r.ID + "|" + r.Password))
	return hex.EncodeToString(sum[:])
}

// RoomLocked reports whether a client who holds this token (empty: none) is kept out of the room: it has a PIN and the token
// is not the right one.
func RoomLocked(r store.Room, token string) bool {
	if r.Password == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(RoomUnlockToken(r))) != 1
}

const (
	roomWrongPINs   = 5
	roomPINWindow   = 10 * time.Minute
	roomPINCooldown = 10 * time.Minute
)

type pinGuard struct {
	mu    sync.Mutex
	wrong map[string][]time.Time
	until map[string]time.Time
}

// TryRoomPIN checks the PIN a client gave. Five wrong tries from one connection in ten minutes silence it for ten more
// (ErrDropBusy), as for a drop box.
func (h *Hub) TryRoomPIN(r store.Room, who, given string) (bool, error) {
	if r.Password == "" {
		return true, nil
	}
	key, now := r.ID+"|"+who, h.now()
	g := &h.pins
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.wrong == nil {
		g.wrong, g.until = map[string][]time.Time{}, map[string]time.Time{}
	}
	if now.Before(g.until[key]) {
		return false, ErrDropBusy
	}
	if verifyDropPassword(r.Password, given) {
		delete(g.wrong, key)
		return true, nil
	}
	recent := g.wrong[key][:0]
	for _, t := range g.wrong[key] {
		if now.Sub(t) < roomPINWindow {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	g.wrong[key] = recent
	if len(recent) >= roomWrongPINs {
		g.until[key] = now.Add(roomPINCooldown)
		delete(g.wrong, key)
	}
	return false, nil
}
