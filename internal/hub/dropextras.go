package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

var accentRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func checkDropLook(accent, lang string, maxDim int) error {
	if accent != "" && !accentRe.MatchString(accent) {
		return ErrBadDropLook
	}
	if lang != "" {
		ok := false
		for _, l := range i18n.All {
			ok = ok || string(l) == lang
		}
		if !ok {
			return ErrBadDropLook
		}
	}
	if maxDim != 0 && (maxDim < 800 || maxDim > 8000) {
		return ErrBadDropLook
	}
	return nil
}

// --- the look of the guest page ---

const maxCover = 900 << 10

// SetDropLook changes the colour, the language and the shrinking size of a box's guest page, and its cover photo (nil
// keeps it; removeCover takes it away). The owner and the co-hosts who manage may.
func (h *Hub) SetDropLook(ctx context.Context, user immich.User, id, accent, lang string, maxDim int, cover []byte, removeCover bool) error {
	b, _, err := h.dropBoxFor(ctx, user.ID, id, "manage")
	if err != nil {
		return err
	}
	accent = strings.ToLower(strings.TrimSpace(accent))
	if err := checkDropLook(accent, lang, maxDim); err != nil {
		return err
	}
	var kind string
	if len(cover) > 0 {
		if len(cover) > maxCover {
			return ErrBadCover
		}
		switch http.DetectContentType(cover) {
		case "image/jpeg", "image/png", "image/webp":
			kind = http.DetectContentType(cover)
		default:
			return ErrBadCover
		}
	}
	if err := h.Store.SetDropLook(ctx, b.ID, accent, lang, maxDim); err != nil {
		return err
	}
	switch {
	case kind != "":
		return h.Store.SetDropCover(ctx, b.ID, kind, cover)
	case removeCover:
		return h.Store.SetDropCover(ctx, b.ID, "", nil)
	}
	return nil
}

// --- more ways in ---

const shortAlphabet = "abcdefghjkmnpqrstuvwxyz23456789" // no letters that look like digits or each other

func newShort() string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = shortAlphabet[int(b[i])%len(shortAlphabet)]
	}
	return string(b)
}

// SetDropShort makes a short link for a box, or removes it. Removing it does not touch the long link, and the other
// way round: each way in is ended on its own. The owner and the co-hosts who manage may.
func (h *Hub) SetDropShort(ctx context.Context, user immich.User, id string, on bool) error {
	b, _, err := h.dropBoxFor(ctx, user.ID, id, "manage")
	if err != nil {
		return err
	}
	if !on {
		if b.Short == "" {
			return nil
		}
		if err := h.Store.SetDropShort(ctx, b.ID, ""); err != nil {
			return err
		}
		h.dropEvent(ctx, b.ID, store.DropRevokedK, i18n.Pack("{name} removed the short link", "name", user.Name), "")
		return nil
	}
	for i := 0; i < 5; i++ {
		err = h.Store.SetDropShort(ctx, b.ID, newShort())
		if !errors.Is(err, store.ErrExists) {
			break
		}
	}
	if err != nil {
		return err
	}
	h.dropEvent(ctx, b.ID, store.DropCreated, i18n.Pack("{name} made a short link", "name", user.Name), "")
	return nil
}

// RenewDropToken replaces the long link of a box: the old one, including any printed with it, stops working. Only the
// owner may. The short link, if there is one, is not changed.
func (h *Hub) RenewDropToken(ctx context.Context, owner immich.User, id string) error {
	b, _, err := h.dropBoxFor(ctx, owner.ID, id, "owner")
	if err != nil {
		return err
	}
	if err := h.Store.SetDropToken(ctx, b.ID, newToken()+newToken()); err != nil {
		return err
	}
	h.dropEvent(ctx, b.ID, store.DropRevokedK, i18n.Pack("{name} made a new long link; the old one stopped working", "name", owner.Name), "")
	return nil
}

// --- templates ---

// DropSettings are the settings of a box that a template keeps: everything but the name, the link, the password, the
// cover photo, the guests and the co-hosts.
type DropSettings struct {
	Message         string
	Days            int // how long the link works, from when it opens
	MaxFiles, MaxMB int
	MaxFileMB       int
	PhotosOnly      bool
	Album, ExtraTag string
	Approve         bool
	Accent, Lang    string
	MaxDim          int
	GuestDelete     bool
	Prompts         []string
	PromptRequired  bool
	GuestBook       bool
}

// SettingsOf reads a box's settings.
func SettingsOf(b store.DropBox) DropSettings {
	s := DropSettings{Message: b.Message, MaxFiles: b.MaxFiles, MaxMB: b.MaxMB, MaxFileMB: b.MaxFileMB, PhotosOnly: b.PhotosOnly,
		Album: b.Album, ExtraTag: b.ExtraTag, Approve: b.Approve, Accent: b.Accent, Lang: b.Lang, MaxDim: b.MaxDim, GuestDelete: b.GuestDelete,
		PromptRequired: b.PromptRequired, GuestBook: b.GuestBook}
	for _, pr := range b.Prompts {
		s.Prompts = append(s.Prompts, pr.Text)
	}
	if !b.ExpiresAt.IsZero() {
		from := b.CreatedAt
		if b.StartsAt.After(from) {
			from = b.StartsAt
		}
		if d := int(b.ExpiresAt.Sub(from).Hours()/24 + 0.5); d > 0 {
			s.Days = min(d, 365)
		}
	}
	return s
}

// Params makes the parameters of a new box with these settings.
func (s DropSettings) Params(name string) DropParams {
	return DropParams{Name: name, Message: s.Message, Days: s.Days, MaxFiles: s.MaxFiles, MaxMB: s.MaxMB, MaxFileMB: s.MaxFileMB,
		PhotosOnly: s.PhotosOnly, Album: s.Album, ExtraTag: s.ExtraTag, Approve: s.Approve, Accent: s.Accent, Lang: s.Lang,
		MaxDim: s.MaxDim, GuestDelete: s.GuestDelete, Prompts: s.Prompts, PromptRequired: s.PromptRequired, GuestBook: s.GuestBook}
}

// TemplateInfo is a template as the page lists it.
type TemplateInfo struct {
	ID, Name string
	Builtin  bool
	Settings DropSettings
}

// builtinDropTemplates are the starting points everybody has.
var builtinDropTemplates = []TemplateInfo{
	{ID: "builtin:wedding", Name: "Wedding", Builtin: true, Settings: DropSettings{Message: "Thank you for being there! Send us your photos.", Days: 14, MaxFileMB: 500, MaxMB: 50000, MaxFiles: 5000, MaxDim: 0}},
	{ID: "builtin:birthday", Name: "Birthday", Builtin: true, Settings: DropSettings{Message: "Share your photos of the party!", Days: 3, MaxFileMB: 300, MaxMB: 10000, MaxFiles: 1000}},
	{ID: "builtin:school", Name: "School trip", Builtin: true, Settings: DropSettings{Message: "Photos of the trip, please.", Days: 7, MaxFileMB: 100, MaxMB: 10000, MaxFiles: 1000, PhotosOnly: true, Approve: true, MaxDim: 4000}},
}

// DropTemplates lists the templates a person can start from: the ones everybody has, then their own.
func (h *Hub) DropTemplates(ctx context.Context, user uuid.UUID) ([]TemplateInfo, error) {
	out := append([]TemplateInfo{}, builtinDropTemplates...)
	if h.Store == nil {
		return out, nil
	}
	mine, err := h.Store.DropTemplates(ctx, user)
	if err != nil {
		return out, err
	}
	for _, t := range mine {
		var s DropSettings
		if json.Unmarshal([]byte(t.Data), &s) == nil {
			out = append(out, TemplateInfo{ID: t.ID, Name: t.Name, Settings: s})
		}
	}
	return out, nil
}

// SaveDropTemplate keeps the settings of one of the person's boxes under a name, to start new boxes from. Only the
// owner of a box may, and a person has at most twenty.
func (h *Hub) SaveDropTemplate(ctx context.Context, owner immich.User, boxID, name string) error {
	b, _, err := h.dropBoxFor(ctx, owner.ID, boxID, "owner")
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 2 || n > 60 || strings.ContainsAny(name, "\r\n\x00") {
		return ErrBadTemplate
	}
	if mine, err := h.Store.DropTemplates(ctx, owner.ID); err != nil {
		return err
	} else if len(mine) >= 20 {
		return ErrBadTemplate
	}
	data, _ := json.Marshal(SettingsOf(b))
	_, err = h.Store.AddDropTemplate(ctx, store.DropTemplate{OwnerID: owner.ID, Name: name, Data: string(data)})
	return err
}

// DeleteDropTemplate removes one of the person's templates.
func (h *Hub) DeleteDropTemplate(ctx context.Context, owner immich.User, id string) error {
	return h.Store.RemoveDropTemplate(ctx, owner.ID, id)
}

// TemplateParams are the parameters of a new box started from a template of the person's or a built-in one. A
// template that needs approval is only offered where files can wait (StageDir).
func (h *Hub) TemplateParams(ctx context.Context, user uuid.UUID, id, name string) (DropParams, error) {
	all, err := h.DropTemplates(ctx, user)
	if err != nil {
		return DropParams{}, err
	}
	for _, t := range all {
		if t.ID == id {
			p := t.Settings.Params(name)
			if t.Builtin && h.StageDir == "" {
				p.Approve = false
			}
			return p, nil
		}
	}
	return DropParams{}, ErrNoTemplate
}

// DuplicateParams are the parameters of a new box with the settings of one of the person's boxes (owned or managed).
func (h *Hub) DuplicateParams(ctx context.Context, user uuid.UUID, boxID, name string) (DropParams, error) {
	b, _, err := h.dropBoxFor(ctx, user, boxID, "manage")
	if err != nil && !errors.Is(err, ErrDropFinished) {
		return DropParams{}, err
	}
	return SettingsOf(b).Params(name), nil
}

// --- what a guest sent, and taking it back ---

// GuestKey is what is stored in place of the secret a guest's browser holds: the secret itself is never kept.
func GuestKey(secret string) string {
	sum := sha256.Sum256([]byte("drop-guest|" + secret))
	return hex.EncodeToString(sum[:])
}

// NewGuestSecret makes the secret a guest's browser keeps to find its own uploads again.
func NewGuestSecret() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// GuestFile is a file a guest sent, as that guest sees it.
type GuestFile struct {
	ID, Name string
	Size     int64
	Waiting  bool // it waits for approval and is not in the library yet
	At       time.Time
}

// GuestUploads lists what a guest sent to a box, newest first. The box must let guests manage their uploads.
func (h *Hub) GuestUploads(ctx context.Context, boxID, secret string) ([]GuestFile, error) {
	b, err := h.Store.DropBox(ctx, boxID)
	if err != nil || !b.GuestDelete || b.State == store.DropRevoked || secret == "" {
		return nil, ErrDropGone
	}
	list, err := h.Store.DropUploadsOf(ctx, boxID, GuestKey(secret), 200)
	if err != nil {
		return nil, err
	}
	out := make([]GuestFile, 0, len(list))
	for _, u := range list {
		out = append(out, GuestFile{ID: u.ID, Name: u.Name, Size: u.Size, Waiting: u.PendingID != "", At: u.At})
	}
	return out, nil
}

// DeleteGuestUpload takes back a file the guest sent: from the queue if it still waits, or from the library (to the
// owner's trash, so it can be got back) if it still carries the box's inbox tag. Once the owner has taken the tag off,
// the photo is theirs and the guest is told to ask them.
func (h *Hub) DeleteGuestUpload(ctx context.Context, boxID, secret, uploadID string) error {
	b, err := h.Store.DropBox(ctx, boxID)
	if err != nil || b.State == store.DropRevoked || secret == "" {
		return ErrDropGone
	}
	if !b.GuestDelete {
		return ErrDropDeleteOff
	}
	c := context.WithoutCancel(ctx)
	u, err := h.Store.DropUploadByID(ctx, boxID, GuestKey(secret), uploadID)
	if err != nil {
		return ErrDropGone
	}
	if u.PendingID != "" {
		path := ""
		if p, err := h.Store.DropPendingByID(ctx, u.PendingID); err == nil {
			path = p.Path
		}
		if ok, _ := h.Store.RemoveDropPending(c, u.PendingID); ok {
			if path != "" {
				os.Remove(path)
			}
			_ = h.Store.ReleaseDropSlot(c, b.ID)
			_ = h.Store.AddDropBytes(c, b.ID, -u.Size)
			_ = h.Store.DropUploadGone(c, u.PendingID)
			h.removedByGuest(ctx, b, u, false)
			return nil
		}
		// a host decided first: look again, the file may be in the library now
		if u, err = h.Store.DropUploadByID(ctx, boxID, GuestKey(secret), uploadID); err != nil || u.PendingID != "" {
			return ErrDropGone
		}
	}
	id, err := uuid.Parse(u.AssetID)
	if err != nil {
		return ErrDropGone
	}
	api, err := h.Connect(b.Key)
	if err != nil {
		return err
	}
	a, err := api.Asset(ctx, id)
	switch {
	case immich.IsNotFound(err):
		// it is gone already (the owner deleted it): nothing is left to take back
	case err != nil:
		return err
	default:
		inbox := false
		if a.IsTrashed {
			inbox = true // already in the trash
		}
		if a.Tags != nil {
			for _, tg := range *a.Tags {
				inbox = inbox || tg.Value == b.InboxTag
			}
		}
		if !inbox {
			return ErrDropKept
		}
		if !a.IsTrashed {
			if err := api.DeleteAsset(ctx, id, false); err != nil {
				return err
			}
		}
	}
	_, _ = h.Store.RemoveDropUpload(c, u.ID)
	_ = h.Store.RemoveDropAsset(c, b.ID, u.AssetID)
	_ = h.Store.ReleaseDropSlot(c, b.ID)
	_ = h.Store.AddDropBytes(c, b.ID, -u.Size)
	h.removedByGuest(ctx, b, u, true)
	return nil
}

func (h *Hub) removedByGuest(ctx context.Context, b store.DropBox, u store.DropUpload, counted bool) {
	if counted && u.Prompt != "" {
		_ = h.Store.AddPromptCount(context.WithoutCancel(ctx), b.ID, u.Prompt, -1)
	}
	_ = h.Store.AddDropRemoved(context.WithoutCancel(ctx), b.ID)
	h.dropEvent(ctx, b.ID, store.DropRejected, i18n.Pack("A guest took back {file}", "file", u.Name), "")
}

var _ = fmt.Sprint

// DropBoxAccess is a box the person owns or manages, with its secrets, for pages that draw its link (a printable card).
func (h *Hub) DropBoxAccess(ctx context.Context, user uuid.UUID, id string) (store.DropBox, error) {
	b, _, err := h.dropBoxFor(ctx, user, id, "manage")
	b.Key = ""
	return b, err
}

// DropBoxForGuest finds a box by its link for a guest managing their own uploads: it need not be open (a guest may take
// something back after the box has closed), but it must not be revoked.
func (h *Hub) DropBoxForGuest(ctx context.Context, token string) (store.DropBox, error) {
	if h.Store == nil || len(token) < 8 || len(token) > 128 {
		return store.DropBox{}, ErrDropGone
	}
	b, err := h.Store.DropBoxByToken(ctx, token)
	if err != nil {
		return b, ErrDropGone
	}
	b.Key = ""
	return b, nil
}
