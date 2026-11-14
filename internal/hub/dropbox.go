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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/notify"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Errors about drop boxes. The first group is shown to the owner as it is; the second is what a guest's upload can
// run into (the page words them for the guest).
var (
	ErrBadDropName       = errors.New("a drop box needs a name of 2 to 60 characters, without slashes or commas")
	ErrBadDropLimit      = errors.New("the limits of a drop box are whole numbers, and the days at most 365")
	ErrBadDropText       = errors.New("the message to guests is at most 300 characters")
	ErrBadDropSecret     = errors.New("a password is at most 64 characters")
	ErrDropNeedsPassword = errors.New("choose a password for the box, or choose No")
	ErrMessageNeedsName  = errors.New("please give your name")
	ErrNoDropBox         = errors.New("there is no such drop box")
	ErrDropFinished      = errors.New("this drop box has been revoked")
	ErrDropRole          = errors.New("your part in this drop box does not allow that")
	ErrBadDropAlbum      = errors.New("the album and the tag are at most 100 characters, with no line breaks, slashes or commas")
	ErrBadDropWhen       = errors.New("a drop box closes after it opens, and within a year from now")
	ErrNoStaging         = errors.New("approving before files arrive needs a place to keep them while they wait, and this install has none (it has no data folder)")
	ErrNoSuchPerson      = errors.New("nobody on this Immich server has that email address")
	ErrHostSelf          = errors.New("you already own this drop box")
	ErrBadHostRole       = errors.New("a co-host manages the box or only views it")
	ErrTooManyHosts      = errors.New("a drop box has at most ten co-hosts")
	ErrNoEnd             = errors.New("this drop box has no end to extend")
	ErrBadDropLook       = errors.New("the colour is six hex digits such as #4250af, the language one of those the pages come in, and the size between 800 and 8000 pixels")
	ErrBadCover          = errors.New("the cover photo is a JPEG, PNG or WebP of at most 900 KB")
	ErrBadTemplate       = errors.New("a template needs a name of 2 to 60 characters, and a person has at most 20")
	ErrNoTemplate        = errors.New("there is no such template")
	ErrDropDeleteOff     = errors.New("guests cannot delete what they sent from this drop box")
	ErrDropKept          = errors.New("this photo is already filed in the owner's library: ask them to remove it")
	ErrBadPrompts        = errors.New("a shot list has at most 20 prompts, each 1 to 60 characters without slashes or commas, and no two alike")
	ErrDropPrompt        = errors.New("choose what the photo is for")
	ErrBadMessage        = errors.New("a message is at most 1000 characters, a name at most 60, and it needs words or a voice note")
	ErrBadVoice          = errors.New("the voice note is not a recording this page makes, or it is too large")
	ErrGuestBookOff      = errors.New("this drop box takes no messages")
	ErrGuestBookFull     = errors.New("this drop box cannot take more messages")
	ErrNoShare           = errors.New("this drop box has nothing shared")
	ErrBadShare          = errors.New("a shared link stays open for 1 to 365 days, its password is at most 64 characters and its message at most 300")
	ErrNothingToShare    = errors.New("there are no photos in this drop box's inbox to share yet")
	ErrNothingChosen     = errors.New("choose the files first")
	ErrSomeNotStored     = errors.New("some files could not be stored right now and stay waiting; try again")

	ErrDropType     = errors.New("this kind of file is not accepted")
	ErrDropTooBig   = errors.New("the file is larger than this drop box takes")
	ErrDropEmpty    = errors.New("the file is empty")
	ErrDropFull     = store.ErrDropFull
	ErrDropGone     = store.ErrDropBoxGone
	ErrDropBusy     = errors.New("too many requests, try again later")
	ErrBadLogo      = errors.New("the logo must be a JPEG, PNG or WebP picture of at most 300 KB")
	ErrBadMark      = errors.New("that is not something a photo can be marked as")
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
	dropPendingFiles     = 1000                // files that may wait for approval in one box
	dropPendingBytes     = 10 << 30            // and the bytes they may hold
	dropPendingAge       = 14 * 24 * time.Hour // a file nobody decided on is deleted after this long
	dropReminder         = 24 * time.Hour      // how long before its end a box reminds
	dropMaxHosts         = 10
	notifiedWaiting      = 8  // Notified bit: the host was told that files wait (cleared when none wait any more)
	notifiedForgot       = 16 // Notified bit: what guests left with the box (names, hearts) was deleted at the end of its retention
)

// DropParams describe a new drop box. Zero values mean no limit, no expiry and no password.
type DropParams struct {
	Name, Message, Password string
	PasswordScope           string    // what the password guards: "upload" (sending) or "page" (everything a guest can reach); "" means "upload" when there is a password
	Days                    int       // the link works for this many days from now (or from Opens)
	Opens, Closes           time.Time // a start, and an end that replaces Days; zero: now, and Days
	MaxFiles, MaxMB         int       // the most the box takes in all
	MaxFileMB               int       // the largest single file
	PhotosOnly              bool
	Album, ExtraTag         string   // where files are filed besides the inbox tag
	Approve                 bool     // files wait for a host's approval
	Accent, Lang            string   // the guest page's colour (#rrggbb) and language
	MaxDim                  int      // photos larger than this many pixels are shrunk in the guest's browser
	GuestDelete             bool     // guests may delete what they sent while it is in the inbox
	Prompts                 []string // the shot list: what guests are asked to photograph
	PromptRequired          bool     // every photo must say which prompt it is for
	GuestBook               bool     // guests may leave a message (with their name) and a voice note
	KeepDays                int      // days after the box ends that what guests left with it (names, hearts, the gallery) is kept; 0: until it is revoked
	Gallery                 bool     // guests may look at the photos the box kept, and heart them
	GalleryDownload         bool     // and download them (needs Gallery)
	GalleryNames            bool     // and see the name the sender gave (needs Gallery)
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
	if p.PasswordScope != "" && p.Password == "" {
		return ErrDropNeedsPassword
	}
	if p.PasswordScope != "" && p.PasswordScope != store.DropScopeUpload && p.PasswordScope != store.DropScopePage {
		return ErrBadDropSecret
	}
	for _, v := range []int{p.MaxFiles, p.MaxMB, p.MaxFileMB} {
		if v < 0 || v > 1_000_000 {
			return ErrBadDropLimit
		}
	}
	if p.Days < 0 || p.Days > 365 || p.KeepDays < 0 || p.KeepDays > 3650 {
		return ErrBadDropLimit
	}
	if err := checkDropLook(p.Accent, p.Lang, p.MaxDim); err != nil {
		return err
	}
	if _, err := newPrompts(p.Prompts); err != nil {
		return err
	}
	for _, v := range []string{p.Album, p.ExtraTag} {
		if utf8.RuneCountInString(v) > 100 || strings.ContainsAny(v, "/,\r\n\x00") {
			return ErrBadDropAlbum
		}
	}
	now := time.Now()
	if !p.Closes.IsZero() {
		start := now
		if !p.Opens.IsZero() {
			start = p.Opens
		}
		if !p.Closes.After(start) || p.Closes.After(now.AddDate(1, 0, 0)) || p.Closes.Before(now) {
			return ErrBadDropWhen
		}
	}
	if !p.Opens.IsZero() && p.Opens.After(now.AddDate(1, 0, 0)) {
		return ErrBadDropWhen
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
	p.Album, p.ExtraTag = strings.TrimSpace(p.Album), strings.TrimSpace(p.ExtraTag)
	if err := checkDropParams(p); err != nil {
		return store.DropBox{}, err
	}
	if p.Approve && h.StageDir == "" {
		return store.DropBox{}, ErrNoStaging
	}
	p.GalleryDownload, p.GalleryNames = p.GalleryDownload && p.Gallery, p.GalleryNames && p.Gallery
	perms := immich.DropBoxPermissionsFor(p.Album != "", p.GuestDelete, p.Gallery, p.GalleryDownload)
	keyID, secret, err := sess.CreateAPIKey(ctx, keyName(p.Name)+" (drop box)", perms)
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
		Album: p.Album, ExtraTag: p.ExtraTag, Approve: p.Approve, StartsAt: p.Opens,
		Accent: strings.ToLower(p.Accent), Lang: p.Lang, MaxDim: p.MaxDim, GuestDelete: p.GuestDelete,
		GuestBook: p.GuestBook, KeepDays: p.KeepDays, Gallery: p.Gallery, GalleryDownload: p.GalleryDownload, GalleryNames: p.GalleryNames,
	}
	switch {
	case !p.Closes.IsZero():
		b.ExpiresAt = p.Closes
	case p.Days > 0:
		from := time.Now()
		if p.Opens.After(from) {
			from = p.Opens
		}
		b.ExpiresAt = from.Add(time.Duration(p.Days) * 24 * time.Hour)
	}
	if p.Password != "" {
		if b.Password, err = HashDropPassword(p.Password); err != nil {
			undo()
			return store.DropBox{}, err
		}
		b.PasswordScope = store.DropScopeUpload
		if p.PasswordScope == store.DropScopePage {
			b.PasswordScope = store.DropScopePage
		}
	}
	if b.Prompts, err = newPrompts(p.Prompts); err != nil {
		undo()
		return store.DropBox{}, err
	}
	b.PromptRequired = p.PromptRequired && len(b.Prompts) > 0
	if b, err = h.Store.AddDropBox(ctx, b); err != nil {
		undo()
		return store.DropBox{}, err
	}
	h.dropEvent(ctx, b.ID, store.DropCreated, i18n.Pack("{name} made this drop box", "name", owner.Name), "")
	h.Log.Info("drop box created", "name", b.Name, "owner", owner.Email)
	return b, nil
}

// DropAccess is a drop box and what the person asking may do with it: "owner", "manage" or "view".
type DropAccess struct {
	Box   store.DropBox
	Role  string
	Hosts []store.DropHost // only the owner is told who the co-hosts are
}

// the roles, from the least to the most
var dropRank = map[string]int{"view": 1, "manage": 2, "owner": 3}

// DropBoxes returns the drop boxes of a person, newest first: their own and the ones they help with. It also
// forgets old revoked ones.
func (h *Hub) DropBoxes(ctx context.Context, user uuid.UUID) ([]DropAccess, error) {
	if h.Store == nil {
		return nil, nil
	}
	_ = h.Store.PruneDropBoxes(ctx, time.Now().Add(-dropKeepRevoked))
	all, err := h.Store.DropBoxes(ctx)
	if err != nil {
		return nil, err
	}
	hosted, err := h.Store.HostedBoxes(ctx, user)
	if err != nil {
		return nil, err
	}
	var mine []DropAccess
	for i := len(all) - 1; i >= 0; i-- {
		b := all[i]
		role := hosted[b.ID]
		if b.OwnerID == user {
			role = "owner"
		}
		if role == "" {
			continue
		}
		b.Key = "" // the page never needs it
		a := DropAccess{Box: b, Role: role}
		if role == "owner" {
			a.Hosts, _ = h.Store.DropHosts(ctx, b.ID)
		}
		mine = append(mine, a)
	}
	return mine, nil
}

// dropBoxFor finds a box the person may use as at least this role. A box they have nothing to do with is "no such
// drop box", the same as one that does not exist.
func (h *Hub) dropBoxFor(ctx context.Context, user uuid.UUID, id, least string) (store.DropBox, string, error) {
	b, err := h.Store.DropBox(ctx, id)
	if err != nil {
		return b, "", ErrNoDropBox
	}
	role := ""
	if b.OwnerID == user {
		role = "owner"
	} else if hosted, err := h.Store.HostedBoxes(ctx, user); err == nil {
		role = hosted[id]
	}
	if role == "" {
		return b, "", ErrNoDropBox
	}
	if b.State == store.DropRevoked {
		return b, role, ErrDropFinished
	}
	if dropRank[role] < dropRank[least] {
		return b, role, ErrDropRole
	}
	return b, role, nil
}

// DropEvents is the log of a drop box the person owns or helps with, newest first.
func (h *Hub) DropEvents(ctx context.Context, user uuid.UUID, id string, limit int) ([]store.DropEvent, error) {
	if _, _, err := h.dropBoxFor(ctx, user, id, "view"); err != nil && !errors.Is(err, ErrDropFinished) {
		return nil, err
	}
	return h.Store.DropEvents(ctx, id, limit)
}

// SetDropBoxPaused stops a drop box taking photos, or lets it take them again. The owner and co-hosts who manage
// may do it.
func (h *Hub) SetDropBoxPaused(ctx context.Context, user immich.User, id string, paused bool) error {
	b, _, err := h.dropBoxFor(ctx, user.ID, id, "manage")
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
		h.dropEvent(ctx, b.ID, kind, i18n.Pack(msg, "name", user.Name), "")
	}
	return nil
}

// RevokeDropBox ends a drop box for good: its key is deleted from the owner's account and its link stops working.
// The photos already received stay in the library; files still waiting for approval are deleted. Only the owner may.
func (h *Hub) RevokeDropBox(ctx context.Context, sess *immich.API, owner immich.User, id string) error {
	b, _, err := h.dropBoxFor(ctx, owner.ID, id, "owner")
	if err != nil {
		return err
	}
	if err := sess.DeleteAPIKey(ctx, b.KeyID); err != nil && !immich.IsNotFound(err) {
		h.Log.Warn("revoke drop box: could not delete the key (it may already be gone)", "err", err)
	}
	if _, err := h.Store.SetDropBoxState(ctx, b.ID, store.DropRevoked, ""); err != nil {
		return err
	}
	h.purgePending(ctx, b.ID, "")
	_ = h.Store.ForgetGuestData(ctx, b.ID)
	_ = h.Store.SetDropShort(ctx, b.ID, "")
	_ = h.Store.SetDropCover(ctx, b.ID, "", nil)
	h.dropEvent(ctx, b.ID, store.DropRevokedK, i18n.Pack("{name} revoked the drop box", "name", owner.Name), "")
	h.Log.Info("drop box revoked", "name", b.Name, "owner", owner.Email)
	return nil
}

// ExtendDropBox moves the end of a box later by some days, counted from its end or from now if it already ended, and
// opens it for guests again if it had ended. The reminder and the summary can be sent again for the new end.
func (h *Hub) ExtendDropBox(ctx context.Context, user immich.User, id string, days int) error {
	b, _, err := h.dropBoxFor(ctx, user.ID, id, "manage")
	if err != nil {
		return err
	}
	if days < 1 || days > 90 {
		return ErrBadDropLimit
	}
	if b.ExpiresAt.IsZero() {
		return ErrNoEnd
	}
	from := b.ExpiresAt
	if now := time.Now(); from.Before(now) {
		from = now
	}
	end := from.Add(time.Duration(days) * 24 * time.Hour)
	if limit := time.Now().AddDate(1, 0, 0); end.After(limit) {
		end = limit
	}
	if err := h.Store.SetDropSchedule(ctx, b.ID, b.StartsAt, end); err != nil {
		return err
	}
	h.dropEvent(ctx, b.ID, store.DropResumed, i18n.Pack("{name} extended the drop box by {days} days", "name", user.Name, "days", days), "")
	return nil
}

// --- co-hosts ---

// AddDropHost lets another person on this Immich server help with a box: they find it on their own page. They are
// found by their exact email, through the owner's own session. Only the owner chooses co-hosts. Adding someone again
// changes their role.
func (h *Hub) AddDropHost(ctx context.Context, sess *immich.API, owner immich.User, id, email, role string) error {
	b, _, err := h.dropBoxFor(ctx, owner.ID, id, "owner")
	if err != nil {
		return err
	}
	if role != store.HostManage && role != store.HostView {
		return ErrBadHostRole
	}
	u, found, err := sess.FindUserByEmail(ctx, email)
	if err != nil {
		return err
	}
	if !found {
		return ErrNoSuchPerson
	}
	if u.ID == owner.ID {
		return ErrHostSelf
	}
	hosts, _ := h.Store.DropHosts(ctx, b.ID)
	known := false
	for _, x := range hosts {
		known = known || x.UserID == u.ID
	}
	if !known && len(hosts) >= dropMaxHosts {
		return ErrTooManyHosts
	}
	if err := h.Store.SetDropHost(ctx, b.ID, store.DropHost{UserID: u.ID, Name: u.Name, Email: u.Email, Role: role}); err != nil {
		return err
	}
	h.dropEvent(ctx, b.ID, store.DropCreated, i18n.Pack("{name} made {host} a co-host ({role})", "name", owner.Name, "host", u.Name, "role", role), "")
	return nil
}

// RemoveDropHost takes a co-host off a box. The owner may remove anybody; a co-host may remove themselves.
func (h *Hub) RemoveDropHost(ctx context.Context, user immich.User, id string, target uuid.UUID) error {
	b, role, err := h.dropBoxFor(ctx, user.ID, id, "view")
	if err != nil {
		return err
	}
	if role != "owner" && target != user.ID {
		return ErrDropRole
	}
	if err := h.Store.RemoveDropHost(ctx, b.ID, target); err != nil {
		return err
	}
	h.dropEvent(ctx, b.ID, store.DropRevokedK, i18n.Pack("{name} ended a co-host's part in the drop box", "name", user.Name), "")
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
	if h.Store == nil || len(token) < 8 || len(token) > 128 {
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

// DropUnlockToken is what a guest's browser holds after giving the right password to a box whose password guards the
// whole page. It is made from the stored hash of the password, which no guest can see, so it cannot be made up, and it
// stops working when the password changes.
func DropUnlockToken(b store.DropBox) string {
	sum := sha256.Sum256([]byte("drop-unlock|" + b.ID + "|" + b.Password))
	return hex.EncodeToString(sum[:])
}

// DropPageLocked reports whether a guest who holds this token (empty: none) is kept out of the box's page: the
// password guards the whole page and the token is not the right one.
func DropPageLocked(b store.DropBox, token string) bool {
	if b.Password == "" || b.PasswordScope != store.DropScopePage {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(DropUnlockToken(b))) != 1
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
		h.tell(notify.DropBox, fmt.Sprintf("The drop box %q of %s was paused by itself because of an unusual number of uploads or refusals. Its owner can look at its log and resume it.", b.Name, b.OwnerName))
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
	DropHeld                         // it waits for a host's approval
)

// dropRetagAfter is how long after filing a photo its tags are put on a second time.
var dropRetagAfter = 6 * time.Second

// tagDrop puts the tags on a photo; a tag that cannot be made or put on is logged.
func (h *Hub) tagDrop(ctx context.Context, api *immich.API, b store.DropBox, tags []string, id uuid.UUID) {
	for _, name := range tags {
		if tag, err := api.FindTag(ctx, name, true); err != nil {
			h.Log.Warn("drop box: could not find or make a tag", "box", b.Name, "tag", name, "err", err)
		} else if err := api.TagAssets(ctx, *tag, id); err != nil {
			h.Log.Warn("drop box: could not tag a photo", "box", b.Name, "tag", name, "err", err)
		}
	}
}

// fileDrop files a stored photo as the box says: its inbox tag, the box's own tag, and its album. Failing to file
// does not lose the photo, which is already in the library: it is logged and the photo stays where it is.
func (h *Hub) fileDrop(ctx context.Context, api *immich.API, b store.DropBox, id uuid.UUID, prompt string) {
	tags := []string{b.InboxTag}
	if b.ExtraTag != "" {
		tags = append(tags, b.ExtraTag)
	}
	for _, pr := range b.Prompts {
		if pr.ID == prompt { // a photo of a prompt is also tagged inbox/<box>/<prompt>, so Immich lists them together
			tags = append(tags, b.InboxTag+"/"+pr.Text)
		}
	}
	h.tagDrop(ctx, api, b, tags, id)
	// Immich now and then drops a tag put on in the first moments after an upload (see docs/immich-api-notes.md), and
	// the box's key cannot read a photo's tags to check. Putting them on again a little later costs nothing when they
	// are there and mends the photo when one was dropped.
	retag := context.WithoutCancel(ctx)
	time.AfterFunc(dropRetagAfter, func() { h.tagDrop(retag, api, b, tags, id) })
	if b.Album == "" {
		return
	}
	album, _, err := api.FindAlbum(ctx, b.Album)
	if err == nil && album == nil {
		var made uuid.UUID
		if made, err = api.CreateAlbum(ctx, b.Album); err == nil {
			album = &made
		}
	}
	if err == nil {
		err = api.AddToAlbum(ctx, *album, id)
	}
	if err != nil {
		h.Log.Warn("drop box: could not put a photo in the album", "box", b.Name, "album", b.Album, "err", err)
	}
}

// kept does the bookkeeping for a file that is now in the library: the summary's counts, the log, the guest's hourly
// allowance and the check for a flood. Bytes are counted here unless they already were (a file that waited).
func (h *Hub) kept(ctx context.Context, b store.DropBox, who, name string, n int64, countBytes bool, prompt, guestName string, asset uuid.UUID) {
	c := context.WithoutCancel(ctx)
	_ = h.Store.AddDropAsset(c, b.ID, asset.String(), prompt, guestName)
	if prompt != "" {
		_ = h.Store.AddPromptCount(c, b.ID, prompt, 1)
	}
	if countBytes {
		_ = h.Store.AddDropBytes(c, b.ID, n)
	}
	_ = h.Store.RecordDropArrival(c, b.ID, who, time.Now())
	h.dropEvent(ctx, b.ID, store.DropUploaded, i18n.Pack("{file} arrived ({size} MB)", "file", name, "size", fmt.Sprintf("%.1f", float64(n)/(1<<20))), who)
	h.checkBurst(ctx, b)
	g := &h.drop
	g.mu.Lock()
	if s := g.seen[b.ID+"|"+who]; s != nil {
		s.files = append(s.files, time.Now())
	}
	g.mu.Unlock()
}

// ReceiveDropFile takes one guest's file. It is streamed straight through to the owner's library and filed, keeping no
// copy, or, in a box that needs approval, kept on disk until a host decides. What is refused, and why, is written in the
// box's log; the caller shows the guest the error. The first bytes decide what the file is, not its name.
func (h *Hub) ReceiveDropFile(ctx context.Context, boxID, filename string, file io.Reader, who string) (DropOutcome, error) {
	return h.ReceiveDropFileFor(ctx, boxID, filename, file, who, "")
}

// ReceiveDropFileFor is ReceiveDropFile for a guest who holds a secret (see NewGuestSecret): in a box that lets guests
// take back what they sent, the file is recorded under it, so only that guest can see and delete it.
func (h *Hub) ReceiveDropFileFor(ctx context.Context, boxID, filename string, file io.Reader, who, secret string) (DropOutcome, error) {
	return h.ReceiveDropFileWith(ctx, boxID, filename, file, who, DropMeta{Secret: secret})
}

// DropMeta is what a guest says about a file besides the file: the secret that finds their uploads again, and which
// prompt of the shot list it is for (the prompt's id).
type DropMeta struct {
	Secret, Prompt string
	Name           string // the name the guest gives, to be shown with the photo (kept only by a box that shows names)
}

// ReceiveDropFileWith is ReceiveDropFileFor for a guest who also names the prompt a file is for.
func (h *Hub) ReceiveDropFileWith(ctx context.Context, boxID, filename string, file io.Reader, who string, meta DropMeta) (DropOutcome, error) {
	secret := meta.Secret
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

	prompt := ""
	for _, pr := range b.Prompts {
		if pr.ID == meta.Prompt {
			prompt = pr.ID
		}
	}
	if prompt == "" && b.PromptRequired {
		h.RejectDrop(ctx, b, who, filename, ErrDropPrompt)
		return 0, ErrDropPrompt
	}
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
	name := dropFileName(filename, ext)

	guestName := ""
	if b.GalleryNames {
		guestName = CleanGuestName(meta.Name)
	}
	guest := ""
	if b.GuestDelete && secret != "" {
		guest = GuestKey(secret)
	}
	if b.Approve {
		p, err := h.stage(ctx, b, name, kind, who, prompt, guestName, cr)
		if err != nil {
			if cr.hit || errors.Is(err, ErrDropTooBig) {
				h.RejectDrop(ctx, b, who, filename, ErrDropTooBig)
				return 0, ErrDropTooBig
			}
			if errors.Is(err, ErrDropFull) {
				h.RejectDrop(ctx, b, who, filename, ErrDropFull)
			}
			return 0, err
		}
		kept = true
		if guest != "" {
			_ = h.Store.AddDropUpload(context.WithoutCancel(ctx), store.DropUpload{BoxID: b.ID, Guest: guest, PendingID: p.ID, Name: name, Size: p.Size, Prompt: prompt})
		}
		return DropHeld, nil
	}

	api, err := h.Connect(b.Key)
	if err != nil {
		return 0, err
	}
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
	h.fileDrop(ctx, api, b, id, prompt)
	h.kept(ctx, b, who, name, cr.n, true, prompt, guestName, id)
	if guest != "" {
		_ = h.Store.AddDropUpload(context.WithoutCancel(ctx), store.DropUpload{BoxID: b.ID, Guest: guest, AssetID: id.String(), Name: name, Size: cr.n, Prompt: prompt})
	}
	return DropStored, nil
}

// stage keeps a file that waits for approval on disk, in a folder of its own for the box, and records it.
func (h *Hub) stage(ctx context.Context, b store.DropBox, name, kind, who, prompt, guestName string, r io.Reader) (store.DropPending, error) {
	var none store.DropPending
	if h.StageDir == "" {
		return none, ErrNoStaging
	}
	if n, size, err := h.Store.DropPendingTotals(ctx, b.ID); err != nil {
		return none, err
	} else if n >= dropPendingFiles || size >= dropPendingBytes {
		return none, ErrDropFull
	}
	dir := filepath.Join(h.StageDir, b.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return none, err
	}
	f, err := os.CreateTemp(dir, "p-*")
	if err != nil {
		return none, err
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return none, err
	}
	c := context.WithoutCancel(ctx)
	p, err := h.Store.AddDropPending(c, store.DropPending{BoxID: b.ID, Name: name, Size: n, Kind: kind, Who: who, Path: f.Name(), Prompt: prompt, GuestName: guestName})
	if err != nil {
		os.Remove(f.Name())
		return none, err
	}
	_ = h.Store.AddDropBytes(c, b.ID, n)
	h.dropEvent(ctx, b.ID, store.DropUploaded, i18n.Pack("{file} waits for approval ({size} MB)", "file", name, "size", fmt.Sprintf("%.1f", float64(n)/(1<<20))), who)
	if first, _ := h.Store.ClaimDropNotified(c, b.ID, notifiedWaiting); first { // once, until the queue has been emptied: a wedding sends hundreds
		h.tell(notify.DropBox, fmt.Sprintf("Files wait for approval in the drop box %q of %s. Its owner, a co-host or a moderator can look at them.", b.Name, b.OwnerName))
	}
	return p, nil
}

// DropPending lists the files of a box that wait for a decision, for the owner and the co-hosts who manage it.
func (h *Hub) DropPending(ctx context.Context, user uuid.UUID, id string, limit int) ([]store.DropPending, int, error) {
	if _, _, err := h.dropBoxFor(ctx, user, id, "manage"); err != nil {
		return nil, 0, err
	}
	n, _, err := h.Store.DropPendingTotals(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	list, err := h.Store.DropPendingOf(ctx, id, limit)
	return list, n, err
}

// DropPendingCount is how many files wait in a box, for anyone who may see the box.
func (h *Hub) DropPendingCount(ctx context.Context, id string) int {
	n, _, _ := h.Store.DropPendingTotals(ctx, id)
	return n
}

// DropPendingFile gives the path of one waiting file, so a host can see it before deciding.
func (h *Hub) DropPendingFile(ctx context.Context, user uuid.UUID, pendingID string) (store.DropPending, error) {
	p, err := h.Store.DropPendingByID(ctx, pendingID)
	if err != nil {
		return p, ErrNoDropBox
	}
	if _, _, err := h.dropBoxFor(ctx, user, p.BoxID, "manage"); err != nil {
		return store.DropPending{}, err
	}
	return p, nil
}

// purgePending deletes the waiting files of a box from the disk and the record; with an id, only that one.
func (h *Hub) purgePending(ctx context.Context, boxID, only string) {
	list, err := h.Store.DropPendingOf(ctx, boxID, 100000)
	if err != nil {
		return
	}
	for _, p := range list {
		if only != "" && p.ID != only {
			continue
		}
		if ok, _ := h.Store.RemoveDropPending(context.WithoutCancel(ctx), p.ID); ok {
			os.Remove(p.Path)
			_ = h.Store.DropUploadGone(context.WithoutCancel(ctx), p.ID)
		}
	}
	if only == "" && h.StageDir != "" {
		os.Remove(filepath.Join(h.StageDir, boxID))
	}
}

// DecideDrop approves or rejects waiting files of a box, the ones named or, with none named, all of them. An approved
// file is stored in the owner's library and filed; a rejected one is deleted and its room given back. Two people
// deciding on the same file cannot both succeed. It returns how many files were done and how many could not be stored
// (those stay waiting).
func (h *Hub) DecideDrop(ctx context.Context, user immich.User, id string, only []string, approve bool) (done, failed int, err error) {
	b, _, err := h.dropBoxFor(ctx, user.ID, id, "manage")
	if err != nil {
		return 0, 0, err
	}
	return h.decide(ctx, b, user.Name, only, approve)
}

// decide does what DecideDrop and a moderator's decision have in common. who is the name the log gives: a person's, or
// "" for a moderator, who has no name.
func (h *Hub) decide(ctx context.Context, b store.DropBox, who string, only []string, approve bool) (done, failed int, err error) {
	list, err := h.Store.DropPendingOf(ctx, b.ID, 5000)
	if err != nil {
		return 0, 0, err
	}
	want := map[string]bool{}
	for _, x := range only {
		want[x] = true
	}
	var api *immich.API
	if approve {
		if api, err = h.Connect(b.Key); err != nil {
			return 0, 0, err
		}
	}
	c := context.WithoutCancel(ctx)
	for _, p := range list {
		if len(want) > 0 && !want[p.ID] {
			continue
		}
		if ok, err := h.Store.RemoveDropPending(c, p.ID); err != nil || !ok {
			continue // another host decided first
		}
		if !approve {
			os.Remove(p.Path)
			_ = h.Store.ReleaseDropSlot(c, b.ID)
			_ = h.Store.AddDropBytes(c, b.ID, -p.Size)
			_ = h.Store.DropUploadGone(c, p.ID)
			done++
			continue
		}
		f, err := os.Open(p.Path)
		var imageID uuid.UUID
		dup := false
		if err == nil {
			imageID, dup, err = api.UploadFile(ctx, p.Name, f, p.At)
			f.Close()
		}
		if err != nil {
			h.Log.Warn("drop box: could not store an approved file; it stays waiting", "box", b.Name, "err", err)
			_, _ = h.Store.AddDropPending(c, p) // put it back, with the same id and path
			failed++
			continue
		}
		os.Remove(p.Path)
		if dup {
			_ = h.Store.ReleaseDropSlot(c, b.ID)
			_ = h.Store.AddDropBytes(c, b.ID, -p.Size)
			_ = h.Store.DropUploadGone(c, p.ID)
			h.dropEvent(ctx, b.ID, store.DropUploaded, i18n.Pack("{file} was already in the library", "file", p.Name), p.Who)
		} else {
			_ = h.Store.DropUploadApproved(c, p.ID, imageID.String())
			h.fileDrop(ctx, api, b, imageID, p.Prompt)
			h.kept(ctx, b, p.Who, p.Name, p.Size, false, p.Prompt, p.GuestName, imageID)
		}
		done++
	}
	if done > 0 {
		one, msg := "{name} approved {n} file", "{name} approved {n} files"
		if !approve {
			one, msg = "{name} rejected {n} file", "{name} rejected {n} files"
		}
		if who == "" {
			one, msg = "A moderator approved {n} file", "A moderator approved {n} files"
			if !approve {
				one, msg = "A moderator rejected {n} file", "A moderator rejected {n} files"
			}
		}
		h.dropEvent(ctx, b.ID, store.DropResumed, i18n.PackN(one, msg, done, "name", who), "")
	}
	if left, _, _ := h.Store.DropPendingTotals(c, b.ID); left == 0 { // the next file that waits tells the host again
		_ = h.Store.ClearDropNotified(c, b.ID, notifiedWaiting)
	}
	return done, failed, nil
}

// --- time ---

// DropMaintenance does what a drop box needs done on a clock: it reminds shortly before a box closes, tells what a
// box took when it has closed, deletes files nobody decided on for two weeks, and forgets old logs. It is safe to run
// as often as wanted.
func (h *Hub) DropMaintenance(ctx context.Context) {
	if h.Store == nil {
		return
	}
	now := time.Now()
	boxes, err := h.Store.DropBoxes(ctx)
	if err != nil {
		return
	}
	for _, b := range boxes {
		if b.State == store.DropRevoked {
			continue
		}
		// what guests left with the box goes when its retention is over, once
		if until := b.KeepUntil(); !until.IsZero() && !now.Before(until) {
			if first, _ := h.Store.ClaimDropNotified(ctx, b.ID, notifiedForgot); first {
				if err := h.Store.ForgetGuestData(ctx, b.ID); err != nil {
					h.Log.Warn("drop box: could not delete what guests left", "box", b.Name, "err", err)
				} else {
					h.dropEvent(ctx, b.ID, store.DropExpired, i18n.Pack("What guests left with the box (names and hearts) was deleted; the photos are in the library"), "")
				}
			}
		}
		if b.ExpiresAt.IsZero() {
			continue
		}
		switch {
		case b.Expired(now) && b.Notified&2 == 0:
			_ = h.Store.MarkDropNotified(ctx, b.ID, 2)
			// a box that was told to hand its photos on does it now, once, as its owner, and the summary says how it went
			onward := ""
			if b.OnwardAuto && b.OnwardService != "" && b.Notified&4 == 0 {
				_ = h.Store.MarkDropNotified(ctx, b.ID, 4)
				if n, err := h.handOnward(ctx, b.OwnerID, b.OwnerName, b, b.OnwardService, "all"); err != nil {
					onward = fmt.Sprintf(" Could not hand the photos on to %s: %v.", b.OnwardService, err)
				} else {
					onward = fmt.Sprintf(" Handed %d photos on to %s.", n, b.OnwardService)
				}
			}
			text := h.dropClosingText(ctx, b) + onward
			h.dropEvent(ctx, b.ID, store.DropExpired, i18n.Pack("The drop box closed. {summary}", "summary", text), "")
			h.tell(notify.DropBox, fmt.Sprintf("The drop box %q of %s closed. %s", b.Name, b.OwnerName, text))
		case !b.Expired(now) && b.ExpiresAt.Sub(now) < dropReminder && b.Notified&1 == 0:
			_ = h.Store.MarkDropNotified(ctx, b.ID, 1)
			h.tell(notify.DropBox, fmt.Sprintf("The drop box %q of %s closes at %s. Its owner or a co-host can extend it from the page.", b.Name, b.OwnerName, b.ExpiresAt.Local().Format("2006-01-02 15:04")))
		}
	}
	if old, err := h.Store.DropPendingBefore(ctx, now.Add(-dropPendingAge)); err == nil {
		for _, p := range old {
			if ok, _ := h.Store.RemoveDropPending(ctx, p.ID); ok {
				os.Remove(p.Path)
				_ = h.Store.DropUploadGone(ctx, p.ID)
				_ = h.Store.ReleaseDropSlot(ctx, p.BoxID)
				_ = h.Store.AddDropBytes(ctx, p.BoxID, -p.Size)
				h.dropEvent(ctx, p.BoxID, store.DropRejected, i18n.Pack("{file} was deleted: nobody decided on it in two weeks", "file", p.Name), p.Who)
			}
		}
	}
	_ = h.Store.PruneDropBoxes(ctx, now.Add(-dropKeepRevoked))
}

// dropClosingText is what a box took, in a sentence for the log and for the administrator.
func (h *Hub) dropClosingText(ctx context.Context, b store.DropBox) string {
	sum, _ := h.Store.DropSummaryOf(ctx, b.ID)
	waiting := h.DropPendingCount(ctx, b.ID)
	text := fmt.Sprintf("%d files (%.1f MB) from %d guests", b.Files, float64(b.Bytes)/(1<<20), sum.Guests)
	if waiting > 0 {
		text += fmt.Sprintf(", %d still waiting for approval", waiting)
	}
	if b.Removed > 0 {
		text += fmt.Sprintf(", %d taken back by guests", b.Removed)
	}
	if hearts := h.Store.HeartTotal(ctx, b.ID); hearts > 0 {
		text += fmt.Sprintf(", %d hearts given", hearts)
	}
	return text + "."
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
