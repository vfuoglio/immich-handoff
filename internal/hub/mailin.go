package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/mailin"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

var (
	// ErrMailInOff: the person who runs the app has not set up a mailbox to read.
	ErrMailInOff = errors.New("the person who runs the app has not set up a mailbox for photos by email (MAIL_IN_HOST)")
	// ErrDropMailLocked: a box with a password cannot take mail, since nobody can type the password into an email.
	ErrDropMailLocked = errors.New("a box with a password cannot take photos by email: nobody can type the password into a mail")
)

const (
	mailTriesMax = 3   // how many polls a message that could not be stored is tried for before it is let go
	mailPerPoll  = 200 // the most messages looked at in one poll
)

// MailIn reads the mailbox the administrator set up, and hands the photos in the messages to the drop box each message was
// addressed to, exactly as if a guest had uploaded them: the box's limits, its approval, its filing and its log all apply.
// The sender's address is used for the same short one-way mark as a guest's and is never stored; the body of a message is
// dropped; nobody gets a reply.
type MailIn struct {
	Hub      *Hub
	Settings mailin.Settings
	Log      *slog.Logger
	TLS      *tls.Config // tests only: what the server's certificate is checked against

	mu    sync.Mutex
	tries map[uint32]int
	last  MailInStatus
}

// MailInStatus is how the last look at the mailbox went.
type MailInStatus struct {
	At      time.Time
	Err     string
	Handled int
}

// Status is how the last poll went.
func (m *MailIn) Status() MailInStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
}

// Address is the mail address of a box with this code, or "" when no mailbox is set up.
func (h *Hub) MailAddress(code string) string {
	if h.MailIn == nil || code == "" {
		return ""
	}
	return h.MailIn.Settings.AddressFor(code)
}

// Run looks at the mailbox now and then until the context ends.
func (m *MailIn) Run(ctx context.Context) {
	t := time.NewTicker(m.Settings.Every)
	defer t.Stop()
	for {
		n, err := m.Poll(ctx)
		m.mu.Lock()
		m.last = MailInStatus{At: time.Now(), Handled: n}
		if err != nil {
			m.last.Err = err.Error()
		}
		m.mu.Unlock()
		if err != nil {
			m.Log.Warn("could not read the mailbox for photos by email", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Poll makes one pass over the mailbox and returns how many messages were handled.
func (m *MailIn) Poll(ctx context.Context) (int, error) {
	c, err := mailin.Dial(ctx, m.Settings, m.TLS)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	if err := c.Login(m.Settings.User, m.Settings.Password); err != nil {
		return 0, err
	}
	if err := c.Select(m.Settings.Folder); err != nil {
		return 0, err
	}
	ids, err := c.Unseen()
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		m.mu.Lock()
		m.tries = nil
		m.mu.Unlock()
		return 0, nil
	}
	if len(ids) > mailPerPoll {
		ids = ids[:mailPerPoll]
	}
	handled, deleted := 0, false
	for _, uid := range ids {
		if ctx.Err() != nil {
			break
		}
		if !m.one(ctx, c, uid) {
			continue
		}
		var err error
		if m.Settings.After == mailin.AfterDelete {
			err, deleted = c.MarkDeleted(uid), true
		} else {
			err = c.MarkSeen(uid)
		}
		if err != nil {
			return handled, err
		}
		handled++
	}
	if deleted {
		if err := c.Expunge(); err != nil {
			return handled, err
		}
	}
	return handled, nil
}

// one handles a message and says whether it is done with: stored, refused by the box's rules, or not for any box of ours.
// A message that failed for a reason that may pass (Immich is down) is not done with, and is tried again at the next
// look, up to a few times.
func (m *MailIn) one(ctx context.Context, c *mailin.Client, uid uint32) bool {
	size, err := c.Size(uid)
	if err != nil {
		return false
	}
	if size > m.Settings.MaxBytes {
		m.Log.Debug("a mail was too large and was left unread", "bytes", size)
		return true
	}
	raw, err := c.Fetch(uid)
	if err != nil {
		return false
	}
	p, err := mailin.Parse(raw, m.Settings.Address, m.Settings.MaxBytes)
	if err != nil || p.Code == "" || len(p.Files) == 0 {
		return true
	}
	h := m.Hub
	box, err := h.Store.DropBoxByMailCode(ctx, p.Code)
	if err != nil || !box.Open(time.Now()) || box.Password != "" {
		return true // not a box of ours, or one that is closed, paused or locked: dropped without a word
	}
	from := p.From
	if from == "" {
		from = "unknown"
	}
	who := DropWho(box.Token, from)
	if err := h.AllowDrop(ctx, box, who); err != nil {
		return true // a sender that sends too much is turned away; the box's log says so
	}
	defer h.DoneDrop(box)
	accepted, temporary := 0, false
	for _, f := range p.Files {
		out, err := h.ReceiveDropFileWith(ctx, box.ID, f.Name, bytes.NewReader(f.Data), who, DropMeta{})
		switch {
		case err == nil:
			_ = out
			accepted++
		case isRefusal(err):
			// the box's rules turned it away, and its log says so
		default:
			temporary = true
		}
	}
	if accepted > 0 {
		h.dropEvent(ctx, box.ID, store.DropUploaded, i18n.PackN("A mail brought {n} file", "A mail brought {n} files", accepted), who)
	}
	if temporary {
		m.mu.Lock()
		if m.tries == nil {
			m.tries = map[uint32]int{}
		}
		m.tries[uid]++
		giveUp := m.tries[uid] >= mailTriesMax
		m.mu.Unlock()
		return giveUp
	}
	return true
}

// isRefusal is an answer that comes from the box's rules and would be the same next time.
func isRefusal(err error) bool {
	for _, e := range []error{ErrDropType, ErrDropTooBig, ErrDropEmpty, ErrDropFull, ErrDropPrompt, ErrDropBusy} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// newMailCode makes the code of a box's mail address: ten lower-case letters and digits.
func newMailCode() string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = shortAlphabet[int(b[i])%len(shortAlphabet)]
	}
	return string(b)
}

// SetDropMail gives a box a mail address, or takes it away. The owner and the co-hosts who manage may. A box with a
// password cannot have one, and without a mailbox set up by the administrator nobody can.
func (h *Hub) SetDropMail(ctx context.Context, user immich.User, id string, on bool) error {
	b, _, err := h.dropBoxFor(ctx, user.ID, id, "manage")
	if err != nil {
		return err
	}
	if !on {
		if b.MailCode == "" {
			return nil
		}
		if err := h.Store.SetDropMailCode(ctx, b.ID, ""); err != nil {
			return err
		}
		h.dropEvent(ctx, b.ID, store.DropRevokedK, i18n.Pack("{name} removed the mail address", "name", user.Name), "")
		return nil
	}
	if h.MailIn == nil {
		return ErrMailInOff
	}
	if b.Password != "" {
		return ErrDropMailLocked
	}
	if b.MailCode != "" {
		return nil
	}
	for i := 0; i < 5; i++ {
		err = h.Store.SetDropMailCode(ctx, b.ID, newMailCode())
		if !errors.Is(err, store.ErrExists) {
			break
		}
	}
	if err != nil {
		return err
	}
	h.dropEvent(ctx, b.ID, store.DropCreated, i18n.Pack("{name} gave the box a mail address", "name", user.Name), "")
	return nil
}
