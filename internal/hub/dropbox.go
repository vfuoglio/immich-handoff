package hub

import (
	"bufio"
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Errors about drop boxes. The first group is shown to the owner as it is; the second is what a guest's upload can
// run into (the page words them for the guest).
var (
	ErrBadDropName   = errors.New("a drop box needs a name of 2 to 60 characters, without slashes or commas")
	ErrBadDropLimit  = errors.New("the limits of a drop box are whole numbers, and the days at most 365")
	ErrBadDropText   = errors.New("the message to guests is at most 300 characters")
	ErrBadDropSecret = errors.New("a password is at most 64 characters")
	ErrNoDropBox     = errors.New("there is no such drop box")
	ErrDropFinished  = errors.New("this drop box has been revoked")

	ErrDropType     = errors.New("this kind of file is not accepted")
	ErrDropTooBig   = errors.New("the file is larger than this drop box takes")
	ErrDropEmpty    = errors.New("the file is empty")
	ErrDropFull     = store.ErrDropFull
	ErrDropGone     = store.ErrDropBoxGone
	ErrDropBusy     = errors.New("too many requests, try again later")
	ErrDropPassword = errors.New("the password is not right")
)

// What keeps a drop box from being abused. A guest is told apart only by a short one-way mark of their address
// (see DropWho), and every number here is per box.
const (
	dropRequestsPer10Min = 30                  // upload requests from one guest in ten minutes
	dropFilesPerHour     = 300                 // files from one guest in an hour
	dropWrongPasswords   = 5                   // wrong passwords from one guest in ten minutes, then ten minutes of silence
	dropConcurrentBox    = 4                   // uploads going on at once in one box
	dropConcurrentAll    = 16                  // and in all of them
	dropBurstFiles       = 1000                // files in ten minutes that pause a box by themselves
	dropBurstTurnedAway  = 200                 // refused files and blocked guests in ten minutes that pause it
	dropBurstWindow      = 10 * time.Minute    //
	dropKeepRevoked      = 30 * 24 * time.Hour // how long the log of a revoked box stays readable
)

// DropParams describe a new drop box. Zero values mean no limit, no expiry and no password.
type DropParams struct {
	Name, Message, Password string
	Days                    int // the link works for this many days
	MaxFiles, MaxMB         int // the most the box takes in all
	MaxFileMB               int // the largest single file
	PhotosOnly              bool
}

func checkDropParams(p DropParams) error {
	n := utf8.RuneCountInString(strings.TrimSpace(p.Name))
	if n < 2 || n > 60 || strings.ContainsAny(p.Name, "/,\r\n\x00\t") {
		return ErrBadDropName
	}
	if utf8.RuneCountInString(p.Message) > 300 || strings.ContainsRune(p.Message, 0) {
		return ErrBadDropText
	}
	if utf8.RuneCountInString(p.Password) > 64 {
		return ErrBadDropSecret
	}
	for _, v := range []int{p.MaxFiles, p.MaxMB, p.MaxFileMB} {
		if v < 0 || v > 1_000_000 {
			return ErrBadDropLimit
		}
	}
	if p.Days < 0 || p.Days > 365 {
		return ErrBadDropLimit
	}
	return nil
}

// CreateDropBox makes a drop box in the signed-in person's library. A key that can only store and tag photos is made
// in their account through their own session and kept encrypted; nobody ever sees it. What guests send arrives in the
// library under a tag of its own, inbox/<name>, to be looked through before it is filed.
func (h *Hub) CreateDropBox(ctx context.Context, sess *immich.API, owner immich.User, p DropParams) (store.DropBox, error) {
	if !h.SelfService() {
		return store.DropBox{}, ErrNotJoinable
	}
	p.Name, p.Message = strings.TrimSpace(p.Name), strings.TrimSpace(p.Message)
	if err := checkDropParams(p); err != nil {
		return store.DropBox{}, err
	}
	keyID, secret, err := sess.CreateAPIKey(ctx, keyName(p.Name)+" (drop box)", immich.DropBoxPermissions)
	if err != nil {
		return store.DropBox{}, fmt.Errorf("could not create an API key in your Immich account: %w", err)
	}
	undo := func() {
		if err := sess.DeleteAPIKey(context.WithoutCancel(ctx), keyID); err != nil {
			h.Log.Error("could not remove the key created for a failed drop box", "err", err)
		}
	}
	if api, err := h.Connect(secret); err != nil {
		undo()
		return store.DropBox{}, err
	} else if me, err := api.Me(ctx); err != nil || me.ID != owner.ID {
		undo()
		return store.DropBox{}, fmt.Errorf("the new key does not work for your account: %v", err)
	}
	b := store.DropBox{
		Token: newToken() + newToken(), Name: p.Name, Message: p.Message, OwnerID: owner.ID, OwnerName: owner.Name,
		KeyID: keyID, Key: secret, InboxTag: h.Options.InboxTag + "/" + p.Name,
		MaxFiles: p.MaxFiles, MaxMB: p.MaxMB, MaxFileMB: p.MaxFileMB, PhotosOnly: p.PhotosOnly,
	}
	if p.Days > 0 {
		b.ExpiresAt = time.Now().Add(time.Duration(p.Days) * 24 * time.Hour)
	}
	if p.Password != "" {
		if b.Password, err = HashDropPassword(p.Password); err != nil {
			undo()
			return store.DropBox{}, err
		}
	}
	if b, err = h.Store.AddDropBox(ctx, b); err != nil {
		undo()
		return store.DropBox{}, err
	}
	h.dropEvent(ctx, b.ID, store.DropCreated, i18n.Pack("{name} made this drop box", "name", owner.Name), "")
	h.Log.Info("drop box created", "name", b.Name, "owner", owner.Email)
	return b, nil
}

// DropBoxes returns the drop boxes of a person, newest first, and forgets old revoked ones.
func (h *Hub) DropBoxes(ctx context.Context, owner uuid.UUID) ([]store.DropBox, error) {
	if h.Store == nil {
		return nil, nil
	}
	_ = h.Store.PruneDropBoxes(ctx, time.Now().Add(-dropKeepRevoked))
	all, err := h.Store.DropBoxes(ctx)
	if err != nil {
		return nil, err
	}
	var mine []store.DropBox
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].OwnerID == owner {
			all[i].Key = "" // the page never needs it
			mine = append(mine, all[i])
		}
	}
	return mine, nil
}

func (h *Hub) ownedDropBox(ctx context.Context, owner uuid.UUID, id string) (store.DropBox, error) {
	b, err := h.Store.DropBox(ctx, id)
	if err != nil || b.OwnerID != owner {
		return b, ErrNoDropBox
	}
	if b.State == store.DropRevoked {
		return b, ErrDropFinished
	}
	return b, nil
}

// DropEvents is the log of one of the person's drop boxes, newest first.
func (h *Hub) DropEvents(ctx context.Context, owner uuid.UUID, id string, limit int) ([]store.DropEvent, error) {
	b, err := h.Store.DropBox(ctx, id)
	if err != nil || b.OwnerID != owner {
		return nil, ErrNoDropBox
	}
	return h.Store.DropEvents(ctx, id, limit)
}

// SetDropBoxPaused stops a drop box taking photos, or lets it take them again.
func (h *Hub) SetDropBoxPaused(ctx context.Context, owner immich.User, id string, paused bool) error {
	b, err := h.ownedDropBox(ctx, owner.ID, id)
	if err != nil {
		return err
	}
	state, kind, msg := store.DropActive, store.DropResumed, "{name} opened the drop box again"
	if paused {
		state, kind, msg = store.DropPaused, store.DropPausedOn, "{name} paused the drop box"
	}
	if changed, err := h.Store.SetDropBoxState(ctx, b.ID, state, ""); err != nil {
		return err
	} else if changed {
		h.dropEvent(ctx, b.ID, kind, i18n.Pack(msg, "name", owner.Name), "")
	}
	return nil
}

// RevokeDropBox ends a drop box for good: its key is deleted from the owner's account and its link stops working.
// The photos already received stay in the library.
func (h *Hub) RevokeDropBox(ctx context.Context, sess *immich.API, owner immich.User, id string) error {
	b, err := h.ownedDropBox(ctx, owner.ID, id)
	if err != nil {
		return err
	}
	if err := sess.DeleteAPIKey(ctx, b.KeyID); err != nil && !immich.IsNotFound(err) {
		h.Log.Warn("revoke drop box: could not delete the key (it may already be gone)", "err", err)
	}
	if _, err := h.Store.SetDropBoxState(ctx, b.ID, store.DropRevoked, ""); err != nil {
		return err
	}
	h.dropEvent(ctx, b.ID, store.DropRevokedK, i18n.Pack("{name} revoked the drop box", "name", owner.Name), "")
	h.Log.Info("drop box revoked", "name", b.Name, "owner", owner.Email)
	return nil
}

func (h *Hub) dropEvent(ctx context.Context, id, kind, text, who string) {
	if err := h.Store.AddDropEvent(context.WithoutCancel(ctx), id, kind, text, who); err != nil {
		h.Log.Warn("could not write the drop box log", "err", err)
	}
}

// --- guests ---

// DropWho is the short mark that stands for a guest's address in a box's log. It is made from the box's token and
// the address together, so it tells guests apart inside one box and is no use outside it; the address itself is
// never stored.
func DropWho(token, addr string) string {
	sum := sha256.Sum256([]byte(token + "|" + addr))
	return hex.EncodeToString(sum[:3])
}

// OpenDropBox finds the box a link opens. A link that is unknown, revoked, paused or past its end gives the same
// answer, so nobody can tell which it was.
func (h *Hub) OpenDropBox(ctx context.Context, token string) (store.DropBox, error) {
	if h.Store == nil || len(token) < 16 || len(token) > 128 {
		return store.DropBox{}, ErrDropGone
	}
	b, err := h.Store.DropBoxByToken(ctx, token)
	if err != nil {
		return b, ErrDropGone
	}
	if !b.Open(time.Now()) {
		return b, ErrDropGone
	}
	return b, nil
}

// dropGuard keeps what the limits need to know about guests, in memory: it is about the last hour, so losing it at a
// restart costs nothing.
type dropGuard struct {
	mu    sync.Mutex
	seen  map[string]*guestState // by box and mark
	boxes map[string]int         // uploads going on, by box id
	total int
}

type guestState struct {
	requests, files, wrong []time.Time
	silentUntil            time.Time
	told                   time.Time // when the log last said this guest was turned away
}

func within(ts []time.Time, d time.Duration, now time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < d {
			out = append(out, t)
		}
	}
	return out
}

// AllowDrop decides whether a guest may start another upload, and writes the refusal in the log (once a minute per
// guest, so a flood does not fill it). Every call that returns nil must be matched by DoneDrop.
func (h *Hub) AllowDrop(ctx context.Context, b store.DropBox, who string) error {
	now := time.Now()
	g := &h.drop
	g.mu.Lock()
	if g.seen == nil {
		g.seen, g.boxes = map[string]*guestState{}, map[string]int{}
	}
	k := b.ID + "|" + who
	s := g.seen[k]
	if s == nil {
		if len(g.seen) > 20000 { // a flood of different addresses must not grow this without end
			for key, v := range g.seen {
				if now.Sub(v.told) > time.Hour && len(within(v.requests, time.Hour, now)) == 0 {
					delete(g.seen, key)
				}
			}
		}
		s = &guestState{}
		g.seen[k] = s
	}
	s.requests = within(s.requests, 10*time.Minute, now)
	s.files = within(s.files, time.Hour, now)
	s.wrong = within(s.wrong, 10*time.Minute, now)
	var err error
	switch {
	case now.Before(s.silentUntil) || len(s.wrong) >= dropWrongPasswords:
		err = ErrDropBusy
	case len(s.requests) >= dropRequestsPer10Min || len(s.files) >= dropFilesPerHour:
		err = ErrDropBusy
	case g.boxes[b.ID] >= dropConcurrentBox || g.total >= dropConcurrentAll:
		err = ErrDropBusy
	}
	tell := err != nil && now.Sub(s.told) > time.Minute
	if tell {
		s.told = now
	}
	if err == nil {
		s.requests = append(s.requests, now)
		g.boxes[b.ID]++
		g.total++
	}
	g.mu.Unlock()
	if tell {
		h.dropEvent(ctx, b.ID, store.DropBlocked, i18n.Pack("A guest sent too many requests and was turned away"), who)
		h.checkBurst(ctx, b)
	}
	return err
}

// DoneDrop ends an upload that AllowDrop let in.
func (h *Hub) DoneDrop(b store.DropBox) {
	g := &h.drop
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.boxes[b.ID] > 0 {
		g.boxes[b.ID]--
	}
	if g.total > 0 {
		g.total--
	}
}

// CheckDropPassword reports whether the guest gave the box's password (a box without one takes everybody). Five
// wrong tries in ten minutes silence that guest for the next ten.
func (h *Hub) CheckDropPassword(ctx context.Context, b store.DropBox, who, given string) bool {
	if b.Password == "" {
		return true
	}
	if verifyDropPassword(b.Password, given) {
		return true
	}
	now := time.Now()
	g := &h.drop
	g.mu.Lock()
	if s := g.seen[b.ID+"|"+who]; s != nil {
		s.wrong = append(within(s.wrong, 10*time.Minute, now), now)
		if len(s.wrong) >= dropWrongPasswords {
			s.silentUntil = now.Add(10 * time.Minute)
		}
	}
	g.mu.Unlock()
	h.dropEvent(ctx, b.ID, store.DropBlocked, i18n.Pack("A guest gave a wrong password"), who)
	h.checkBurst(ctx, b)
	return false
}

// RejectDrop writes down a file that was turned away.
func (h *Hub) RejectDrop(ctx context.Context, b store.DropBox, who, name string, reason error) {
	h.dropEvent(ctx, b.ID, store.DropRejected, i18n.Pack("{file} was not taken: {reason}", "file", name, "reason", reason.Error()), who)
	h.checkBurst(ctx, b)
}

// checkBurst pauses a box by itself, and tells the administrator, when it is taking an unusual flood of files or of
// guests being turned away. The owner resumes it with one click once they have looked at the log.
func (h *Hub) checkBurst(ctx context.Context, b store.DropBox) {
	since := time.Now().Add(-dropBurstWindow)
	ok, _ := h.Store.DropEventCount(ctx, b.ID, store.DropUploaded, since)
	rejected, _ := h.Store.DropEventCount(ctx, b.ID, store.DropRejected, since)
	blocked, _ := h.Store.DropEventCount(ctx, b.ID, store.DropBlocked, since)
	var why string
	switch {
	case ok >= dropBurstFiles:
		why = "{n} files in ten minutes"
	case rejected+blocked >= dropBurstTurnedAway:
		why = "{n} refused files or guests turned away in ten minutes"
		ok = rejected + blocked
	default:
		return
	}
	note := i18n.Pack("Paused by itself: "+why, "n", ok)
	if changed, err := h.Store.SetDropBoxState(ctx, b.ID, store.DropPaused, note); err == nil && changed {
		h.dropEvent(ctx, b.ID, store.DropPausedOn, note, "")
		h.Log.Warn("drop box paused by itself", "name", b.Name, "count", ok)
		if h.Notify != nil {
			h.Notify(fmt.Sprintf("The drop box %q of %s was paused by itself because of an unusual number of uploads or refusals. Its owner can look at its log and resume it.", b.Name, b.OwnerName))
		}
	}
}

// capReader stops a stream that goes past a limit, so a file never has to be read to the end to be refused.
type capReader struct {
	r   io.Reader
	max int64 // 0: no limit
	n   int64
	hit bool
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.max > 0 {
		if left := c.max - c.n; left <= 0 {
			// one more byte tells whether the file ends exactly here
			var one [1]byte
			if n, _ := c.r.Read(one[:]); n > 0 {
				c.hit = true
				return 0, ErrDropTooBig
			}
			return 0, io.EOF
		} else if int64(len(p)) > left {
			p = p[:left]
		}
	}
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// DropOutcome says what became of a file.
type DropOutcome int

const (
	DropStored    DropOutcome = iota // it is in the owner's library, tagged
	DropDuplicate                    // Immich already had this very file
)

// ReceiveDropFile stores one guest's file in the owner's library and tags it with the box's inbox tag, streaming it
// straight through without keeping a copy. What is refused, and why, is written in the box's log; the caller shows
// the guest the error. The first bytes decide what the file is, not its name.
func (h *Hub) ReceiveDropFile(ctx context.Context, boxID, filename string, file io.Reader, who string) (DropOutcome, error) {
	b, err := h.Store.TakeDropSlot(ctx, boxID)
	if err != nil {
		if errors.Is(err, store.ErrDropFull) {
			h.RejectDrop(ctx, b, who, filename, ErrDropFull)
		}
		return 0, err
	}
	kept := false
	defer func() {
		if !kept {
			_ = h.Store.ReleaseDropSlot(context.WithoutCancel(ctx), b.ID)
		}
	}()

	br := bufio.NewReaderSize(file, 4096)
	head, _ := br.Peek(512)
	if len(head) == 0 {
		h.RejectDrop(ctx, b, who, filename, ErrDropEmpty)
		return 0, ErrDropEmpty
	}
	kind, ext := sniffDrop(head)
	if kind == "" || (b.PhotosOnly && kind == "video") {
		h.RejectDrop(ctx, b, who, filename, ErrDropType)
		return 0, ErrDropType
	}
	limit := int64(b.MaxFileMB) << 20
	if b.MaxMB > 0 {
		if left := int64(b.MaxMB)<<20 - b.Bytes; limit == 0 || left < limit {
			limit = left
		}
	}
	cr := &capReader{r: br, max: limit}
	api, err := h.Connect(b.Key)
	if err != nil {
		return 0, err
	}
	name := dropFileName(filename, ext)
	id, dup, err := api.UploadFile(ctx, name, cr, time.Now())
	if err != nil {
		if cr.hit || errors.Is(err, ErrDropTooBig) {
			h.RejectDrop(ctx, b, who, filename, ErrDropTooBig)
			return 0, ErrDropTooBig
		}
		h.Log.Warn("drop box: the upload to Immich failed", "box", b.Name, "err", err)
		return 0, err
	}
	if dup {
		h.dropEvent(ctx, b.ID, store.DropUploaded, i18n.Pack("{file} was already in the library", "file", name), who)
		return DropDuplicate, nil
	}
	kept = true
	_ = h.Store.AddDropBytes(context.WithoutCancel(ctx), b.ID, cr.n)
	if tag, err := api.FindTag(ctx, b.InboxTag, true); err != nil {
		h.Log.Warn("drop box: could not find or make the inbox tag", "box", b.Name, "err", err)
	} else if err := api.TagAssets(ctx, *tag, id); err != nil {
		h.Log.Warn("drop box: could not tag a photo", "box", b.Name, "err", err)
	}
	h.dropEvent(ctx, b.ID, store.DropUploaded, i18n.Pack("{file} arrived ({size} MB)", "file", name, "size", fmt.Sprintf("%.1f", float64(cr.n)/(1<<20))), who)
	h.checkBurst(ctx, b)
	// a guest's files count against their hourly allowance
	g := &h.drop
	g.mu.Lock()
	if s := g.seen[b.ID+"|"+who]; s != nil {
		s.files = append(s.files, time.Now())
	}
	g.mu.Unlock()
	return DropStored, nil
}

// --- passwords ---

// HashDropPassword is the stored, salted form of a drop box password.
func HashDropPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, pw, salt, 100_000, 32)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(salt) + "$" + hex.EncodeToString(key), nil
}

func verifyDropPassword(stored, given string) bool {
	saltHex, keyHex, ok := strings.Cut(stored, "$")
	if !ok {
		return false
	}
	salt, err1 := hex.DecodeString(saltHex)
	want, err2 := hex.DecodeString(keyHex)
	if err1 != nil || err2 != nil {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, given, salt, 100_000, 32)
	return err == nil && subtle.ConstantTimeCompare(key, want) == 1
}
