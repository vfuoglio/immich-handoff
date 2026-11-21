package hub

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/store"
)

// The PIN of a client room: given once on the room's page, remembered in the client's browser as a token made from the PIN's
// stored hash (which no client can see), so it cannot be made up and stops working when the PIN changes.

// RoomUnlockToken is what a client's browser holds after giving the right PIN: when it stops being good, and a
// signature over that and the PIN's stored hash.
func RoomUnlockToken(r store.Room) string {
	return unlockToken("room-unlock|"+r.ID+"|"+r.Password, time.Now().Add(UnlockLife))
}

// RoomLocked reports whether a client who holds this token (empty: none) is kept out of the room: it has a PIN and the token
// is not the right one.
func RoomLocked(r store.Room, token string) bool {
	if r.Password == "" {
		return false
	}
	return !unlockTokenGood("room-unlock|"+r.ID+"|"+r.Password, token, time.Now())
}

// UnlockLife is how long a password given on a drop box or room page is remembered. The server checks it, so a
// cookie kept longer is no good.
const UnlockLife = 7 * 24 * time.Hour

// unlockToken signs "until" with a secret only the server has (the stored hash of the password), so a guest cannot
// make one up or lengthen it, and a changed password ends every one at once.
func unlockToken(secret string, until time.Time) string {
	exp := strconv.FormatInt(until.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret)) // a keyed signature of the end time; the secret is itself the stored hash of the password
	mac.Write([]byte("unlock|" + exp))
	return exp + "." + hex.EncodeToString(mac.Sum(nil))
}

func unlockTokenGood(secret, token string, now time.Time) bool {
	exp, _, ok := strings.Cut(token, ".")
	until, err := strconv.ParseInt(exp, 10, 64)
	if !ok || err != nil || now.Unix() > until {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(unlockToken(secret, time.Unix(until, 0)))) == 1
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
