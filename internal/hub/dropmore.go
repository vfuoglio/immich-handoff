package hub

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// --- the shot list ---

const maxPrompts = 20

// cleanPrompts checks a shot list: at most twenty prompts, each 1 to 60 characters with no slash or comma (a prompt
// becomes part of a tag), no two alike (ignoring case). Empty lines are dropped.
func cleanPrompts(texts []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, t := range texts {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if n := utf8.RuneCountInString(t); n > 60 || strings.ContainsAny(t, "/,\r\n\x00\t") {
			return nil, ErrBadPrompts
		}
		if k := strings.ToLower(t); seen[k] {
			return nil, ErrBadPrompts
		} else {
			seen[k] = true
		}
		out = append(out, t)
	}
	if len(out) > maxPrompts {
		return nil, ErrBadPrompts
	}
	return out, nil
}

// newPrompts makes the prompts of a new box, with ids p1, p2, ...
func newPrompts(texts []string) ([]store.Prompt, error) {
	clean, err := cleanPrompts(texts)
	if err != nil {
		return nil, err
	}
	out := make([]store.Prompt, 0, len(clean))
	for i, t := range clean {
		out = append(out, store.Prompt{ID: fmt.Sprintf("p%d", i+1), Text: t})
	}
	return out, nil
}

// editPrompts makes the new list of an existing box: a prompt that is still there keeps its id (and so its count), a
// new one gets an id nobody has used.
func editPrompts(old []store.Prompt, texts []string) ([]store.Prompt, error) {
	clean, err := cleanPrompts(texts)
	if err != nil {
		return nil, err
	}
	byText := map[string]string{}
	next := 0
	for _, p := range old {
		byText[strings.ToLower(p.Text)] = p.ID
		var n int
		if _, err := fmt.Sscanf(p.ID, "p%d", &n); err == nil && n > next {
			next = n
		}
	}
	out := make([]store.Prompt, 0, len(clean))
	for _, t := range clean {
		id, ok := byText[strings.ToLower(t)]
		if !ok {
			next++
			id = fmt.Sprintf("p%d", next)
		}
		out = append(out, store.Prompt{ID: id, Text: t})
	}
	return out, nil
}

// SetDropExtras changes a box's shot list, whether a prompt is required, and whether it has a guest book. The owner and
// the co-hosts who manage may. Photos already tagged keep their tags.
func (h *Hub) SetDropExtras(ctx context.Context, user immich.User, id string, prompts []string, required, guestBook bool) error {
	b, _, err := h.dropBoxFor(ctx, user.ID, id, "manage")
	if err != nil {
		return err
	}
	list, err := editPrompts(b.Prompts, prompts)
	if err != nil {
		return err
	}
	if err := h.Store.SetDropPrompts(ctx, b.ID, list, required); err != nil {
		return err
	}
	return h.Store.SetDropGuestBook(ctx, b.ID, guestBook)
}

// PromptStatus is one prompt of a box's shot list and how many photos came for it.
type PromptStatus struct {
	ID, Text string
	Files    int
}

// PromptStatuses is the shot list with a count per prompt. Guests see it as a checklist of what is still missing.
func (h *Hub) PromptStatuses(ctx context.Context, b store.DropBox) []PromptStatus {
	if len(b.Prompts) == 0 {
		return nil
	}
	counts, _ := h.Store.PromptCounts(ctx, b.ID)
	out := make([]PromptStatus, 0, len(b.Prompts))
	for _, p := range b.Prompts {
		out = append(out, PromptStatus{ID: p.ID, Text: p.Text, Files: counts[p.ID]})
	}
	return out
}

// --- the guest book ---

const (
	maxMessageText = 1000
	maxVoiceBytes  = 2 << 20
	maxMessages    = 500
)

// sniffVoice says whether bytes are a recording a browser makes (WebM, Ogg, MP4/M4A) or a common audio file (WAV, MP3),
// and what kind, from the first bytes and never from a name or a claimed type.
func sniffVoice(b []byte) string {
	switch {
	case len(b) >= 4 && b[0] == 0x1A && b[1] == 0x45 && b[2] == 0xDF && b[3] == 0xA3:
		return "audio/webm"
	case len(b) >= 4 && string(b[:4]) == "OggS":
		return "audio/ogg"
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		return "audio/mp4"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WAVE":
		return "audio/wav"
	case len(b) >= 3 && string(b[:3]) == "ID3", len(b) >= 2 && b[0] == 0xFF && (b[1]&0xE0) == 0xE0:
		return "audio/mpeg"
	}
	return ""
}

func voiceExt(kind string) string {
	switch kind {
	case "audio/webm":
		return ".webm"
	case "audio/ogg":
		return ".ogg"
	case "audio/mp4":
		return ".m4a"
	case "audio/wav":
		return ".wav"
	}
	return ".mp3"
}

// LeaveMessage keeps a guest's message and voice note in the box's guest book. They are kept in the app's own data
// file, not in the owner's Immich (which holds photos and videos), and only the owner and co-hosts can read them.
// The box must have a guest book; the guest may give a name; the message or the voice note may be missing but not both.
func (h *Hub) LeaveMessage(ctx context.Context, b store.DropBox, who, name, text string, voice []byte) error {
	if !b.GuestBook {
		return ErrGuestBookOff
	}
	name, text = strings.TrimSpace(name), strings.TrimSpace(text)
	if utf8.RuneCountInString(name) > 60 || utf8.RuneCountInString(text) > maxMessageText || strings.ContainsRune(name+text, 0) || (text == "" && len(voice) == 0) {
		return ErrBadMessage
	}
	kind := ""
	if len(voice) > 0 {
		if len(voice) > maxVoiceBytes {
			return ErrBadVoice
		}
		if kind = sniffVoice(voice); kind == "" {
			return ErrBadVoice
		}
	}
	if h.Store.DropMessageCount(ctx, b.ID) >= maxMessages {
		return ErrGuestBookFull
	}
	if err := h.Store.AddDropMessage(ctx, store.DropMessage{BoxID: b.ID, Who: who, Name: name, Text: text, AudioKind: kind}, voice); err != nil {
		return err
	}
	h.dropEvent(ctx, b.ID, store.DropMessageEvent, i18n.Pack("A guest left a message"), who)
	return nil
}

// GuestBook lists the messages of a box for the owner and the co-hosts who manage it, newest first.
func (h *Hub) GuestBook(ctx context.Context, user uuid.UUID, id string) ([]store.DropMessage, error) {
	if _, _, err := h.dropBoxFor(ctx, user, id, "manage"); err != nil {
		return nil, err
	}
	return h.Store.DropMessages(ctx, id, 200)
}

// GuestBookAudio gives a voice note to someone who may read the box's guest book.
func (h *Hub) GuestBookAudio(ctx context.Context, user uuid.UUID, msgID string) (kind string, data []byte, err error) {
	box, err := h.Store.DropMessageBox(ctx, msgID)
	if err != nil {
		return "", nil, ErrNoDropBox
	}
	if _, _, err := h.dropBoxFor(ctx, user, box, "manage"); err != nil {
		return "", nil, err
	}
	_, kind, data, err = h.Store.DropMessageAudio(ctx, msgID)
	return kind, data, err
}

// DeleteGuestMessage removes a message from the guest book.
func (h *Hub) DeleteGuestMessage(ctx context.Context, user immich.User, msgID string) error {
	box, err := h.Store.DropMessageBox(ctx, msgID)
	if err != nil {
		return ErrNoDropBox
	}
	if _, _, err := h.dropBoxFor(ctx, user.ID, box, "manage"); err != nil {
		return err
	}
	return h.Store.RemoveDropMessage(ctx, msgID)
}

// ExportGuestBook writes the guest book as a zip: messages.txt with every message, and each voice note as a file named
// in it. It is for the owner to keep.
func (h *Hub) ExportGuestBook(ctx context.Context, user uuid.UUID, id string, w io.Writer) error {
	if _, _, err := h.dropBoxFor(ctx, user, id, "manage"); err != nil {
		return err
	}
	msgs, err := h.Store.DropMessages(ctx, id, maxMessages)
	if err != nil {
		return err
	}
	audio, err := h.Store.DropMessageAudios(ctx, id)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(w)
	var txt strings.Builder
	voices := 0
	for i := len(msgs) - 1; i >= 0; i-- { // oldest first, as a guest book reads
		m := msgs[i]
		who := m.Name
		if who == "" {
			who = "(no name)"
		}
		fmt.Fprintf(&txt, "%s, %s\n", who, m.At.Local().Format("2006-01-02 15:04"))
		if m.Text != "" {
			fmt.Fprintf(&txt, "%s\n", m.Text)
		}
		if data, ok := audio[m.ID]; ok {
			voices++
			file := fmt.Sprintf("voice-%03d%s", voices, voiceExt(m.AudioKind))
			fmt.Fprintf(&txt, "[voice note: %s]\n", file)
			f, err := zw.Create(file)
			if err != nil {
				return err
			}
			if _, err := f.Write(data); err != nil {
				return err
			}
		}
		txt.WriteString("\n")
	}
	f, err := zw.Create("messages.txt")
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, txt.String()); err != nil {
		return err
	}
	return zw.Close()
}

// --- sharing the result back ---

// ShareBack gives guests the photos back: it makes (or fills) an album in the owner's library with what the box
// received and makes a view-only Immich shared link to it, which guests open without an account, and can download from.
// It acts with the owner's own session, so the box's key needs nothing more; only the owner may. The box's album is used if
// it has one, otherwise an album named after the box. Doing it again adds what has arrived since and makes a new link.
func (h *Hub) ShareBack(ctx context.Context, sess *immich.API, owner immich.User, id string, days int, password, message string) (store.DropShare, error) {
	b, _, err := h.dropBoxFor(ctx, owner.ID, id, "owner")
	if err != nil {
		return store.DropShare{}, err
	}
	message = strings.TrimSpace(message)
	if days < 1 || days > 365 || utf8.RuneCountInString(password) > 64 || utf8.RuneCountInString(message) > 300 {
		return store.DropShare{}, ErrBadShare
	}
	tag, err := sess.FindTag(ctx, b.InboxTag, false)
	if err != nil {
		return store.DropShare{}, err
	}
	var ids []uuid.UUID
	if tag != nil {
		assets, err := sess.Search(ctx, immich.MetadataSearchDto{TagIds: &[]uuid.UUID{*tag}})
		if err != nil {
			return store.DropShare{}, err
		}
		for _, a := range assets {
			ids = append(ids, a.Id)
		}
	}
	if len(ids) == 0 {
		return store.DropShare{}, ErrNothingToShare
	}
	name := b.Album
	if name == "" {
		name = b.Name
	}
	album, _, err := sess.FindAlbum(ctx, name)
	if err == nil && album == nil {
		var made uuid.UUID
		if made, err = sess.CreateAlbum(ctx, name); err == nil {
			album = &made
		}
	}
	if err != nil {
		return store.DropShare{}, err
	}
	for len(ids) > 0 {
		n := min(len(ids), 200)
		if err := sess.AddToAlbum(ctx, *album, ids[:n]...); err != nil {
			return store.DropShare{}, err
		}
		ids = ids[n:]
	}
	if old, err := h.Store.DropShareOf(ctx, b.ID); err == nil { // a new link replaces the old one
		if lid, err := uuid.Parse(old.LinkID); err == nil {
			_ = sess.RemoveLink(ctx, lid)
		}
	}
	expires := time.Now().Add(time.Duration(days) * 24 * time.Hour)
	linkID, key, err := sess.CreateAlbumLink(ctx, *album, expires, password, message)
	if err != nil {
		return store.DropShare{}, fmt.Errorf("could not make the shared link in your Immich account: %w", err)
	}
	sh := store.DropShare{BoxID: b.ID, LinkID: linkID.String(), Key: key, Message: message, ExpiresAt: expires}
	if err := h.Store.SetDropShare(ctx, sh); err != nil {
		_ = sess.RemoveLink(context.WithoutCancel(ctx), linkID)
		return store.DropShare{}, err
	}
	h.dropEvent(ctx, b.ID, store.DropCreated, i18n.Pack("{name} shared the photos with the guests", "name", owner.Name), "")
	return sh, nil
}

// UnshareBack ends the shared link of a box. The album and the photos stay in the owner's library.
func (h *Hub) UnshareBack(ctx context.Context, sess *immich.API, owner immich.User, id string) error {
	b, _, err := h.dropBoxFor(ctx, owner.ID, id, "owner")
	if err != nil {
		return err
	}
	sh, err := h.Store.DropShareOf(ctx, b.ID)
	if err != nil {
		return ErrNoShare
	}
	if lid, err := uuid.Parse(sh.LinkID); err == nil {
		if err := sess.RemoveLink(ctx, lid); err != nil {
			return err
		}
	}
	if err := h.Store.RemoveDropShare(ctx, b.ID); err != nil {
		return err
	}
	h.dropEvent(ctx, b.ID, store.DropRevokedK, i18n.Pack("{name} stopped sharing the photos with the guests", "name", owner.Name), "")
	return nil
}

// DropShareFor is the box's shared link, when it has one that is still open (the owner's and the guests' pages show it).
func (h *Hub) DropShareFor(ctx context.Context, boxID string) (store.DropShare, bool) {
	sh, err := h.Store.DropShareOf(ctx, boxID)
	if err != nil || (!sh.ExpiresAt.IsZero() && time.Now().After(sh.ExpiresAt)) {
		return store.DropShare{}, false
	}
	return sh, true
}
