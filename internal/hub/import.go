package hub

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"path"
	"strings"

	"github.com/google/uuid"
	xdraw "golang.org/x/image/draw"

	"github.com/vfuoglio/immich-handoff/internal/dest"
	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/safeimg"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Following a destination (#243, docs/contributing/design/destinations-design.md): the files that appear in a folder, a bucket or a server are offered
// to the person with pictures, and the ones they say yes to are added to their own Immich with their own key (the same key as for following
// an album, which also has the permission to upload). Duplicates are Immich's to refuse, by checksum. Nothing at the destination is
// ever changed or deleted: it is only listed and read.

var (
	ErrNoImportSource   = errors.New("that is not a destination you can import from")
	ErrImportNotAllowed = errors.New("the administrator has not allowed importing from that destination")
	ErrAlreadyImporting = errors.New("you already follow that destination")
	ErrNoThumbnail      = errors.New("no picture for that file")
	ErrImportTooBig     = errors.New("that destination holds too many files to follow as a whole: follow a folder inside it")
)

// mediaExt are the files an import offers: the photos and videos Immich takes. Everything else in a folder (documents, sidecars, the
// records this app writes) is left alone.
var mediaExt = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".heic": true, ".heif": true, ".avif": true, ".tif": true, ".tiff": true, ".bmp": true,
	".dng": true, ".cr2": true, ".cr3": true, ".nef": true, ".arw": true, ".orf": true, ".rw2": true, ".raf": true, ".srw": true, ".pef": true,
	".mp4": true, ".mov": true, ".m4v": true, ".avi": true, ".mkv": true, ".webm": true, ".3gp": true, ".mts": true, ".m2ts": true,
}

const importMostPerLook = 200

// ImportParams is what a person fills in to follow a destination.
type ImportParams struct {
	Source  string // "admin:<name>" or "own:<id>"
	Prefix  string // the folder under the destination's root (empty: all of it)
	Dest    LibraryDest
	History string // FollowNew (the default: only what appears from now on) or FollowAll
}

// ImportSources are the destinations the person may import from: their own network destinations (their own storage), and the
// administrator's that the administrator has opened to it (`import: true` in the destinations file: it then lets whoever may use that
// destination also read from it, so it is for a place that holds nobody else's files).
func (h *Hub) ImportSources(ctx context.Context, owner uuid.UUID) []DestChoice {
	var out []DestChoice
	for _, c := range h.DestinationChoices(ctx, owner) {
		if c.Own {
			out = append(out, c)
		} else if spec, ok := h.AdminDests[strings.TrimPrefix(c.Ref, "admin:")]; ok && on(spec.Settings["import"]) {
			out = append(out, c)
		}
	}
	return out
}

func on(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// importSource says whether the person may import from the destination and what it is called.
func (h *Hub) importSource(ctx context.Context, owner uuid.UUID, ref string) (string, error) {
	switch {
	case strings.HasPrefix(ref, "admin:"):
		spec, ok := h.AdminDests[strings.TrimPrefix(ref, "admin:")]
		if !ok {
			return "", ErrNoImportSource
		}
		if !on(spec.Settings["import"]) {
			return "", ErrImportNotAllowed
		}
		return spec.Name, nil
	case strings.HasPrefix(ref, "own:"):
		if d, err := h.Store.OwnDestination(ctx, strings.TrimPrefix(ref, "own:")); err == nil && d.OwnerID == owner {
			return d.Name, nil
		}
	}
	return "", ErrNoImportSource
}

// importDest is the Destination a rule follows, kept between looks (a server session is not made again each time) until its settings change.
func (h *Hub) importDest(ctx context.Context, r store.Rule) (dest.Destination, error) {
	if _, err := h.importSource(ctx, r.OwnerID, r.Cond.Source); err != nil {
		return nil, err
	}
	spec, err := h.specOf(ctx, r.Cond.Source)
	if err != nil {
		return nil, ErrNoImportSource
	}
	key := r.Cond.Source + fmt.Sprintf("%v", spec)
	h.destMu.Lock()
	defer h.destMu.Unlock()
	if d, ok := h.importDests[key]; ok {
		return d, nil
	}
	d, err := dest.Build(spec, h.DestNet)
	if err != nil {
		return nil, err
	}
	if h.importDests == nil {
		h.importDests = map[string]dest.Destination{}
	}
	h.importDests[key] = d
	return d, nil
}

func cleanPrefix(p string) string {
	p = strings.Trim(path.Clean("/"+strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")), "/")
	return p
}

func isAux(ref string) bool {
	return strings.HasSuffix(ref, ".xmp") || strings.HasSuffix(ref, ".handoff.json")
}

// importCandidates are the files of a listing that an import may offer: photos and videos that are not a delivery of this app (a file with
// a record beside it came from a hand-over, and is not offered back) and not a file of a write that is going on.
func importCandidates(entries []dest.Entry) []dest.Entry {
	have := map[string]bool{}
	for _, e := range entries {
		have[e.Ref] = true
	}
	var out []dest.Entry
	for _, e := range entries {
		base := path.Base(e.Ref)
		if strings.HasPrefix(base, ".") || isAux(e.Ref) || !mediaExt[strings.ToLower(path.Ext(base))] || have[e.Ref+".handoff.json"] {
			continue
		}
		out = append(out, e)
	}
	return out
}

// FollowDestination makes a rule that follows a destination: what appears in it is offered (or, once the person has let the rule go on by
// itself, added) to their own library. With FollowNew what is there now is only remembered; with FollowAll it is offered too.
func (h *Hub) FollowDestination(ctx context.Context, sess *immich.API, user immich.User, p ImportParams) (store.Rule, error) {
	if !h.SelfService() {
		return store.Rule{}, ErrNotJoined
	}
	name, err := h.importSource(ctx, user.ID, p.Source)
	if err != nil {
		return store.Rule{}, err
	}
	d, err := checkLibraryDest(p.Dest)
	if err != nil {
		return store.Rule{}, err
	}
	switch p.History {
	case "":
		p.History = FollowNew
	case FollowNew, FollowAll:
	default:
		return store.Rule{}, ErrBadFollowHistory
	}
	prefix := cleanPrefix(p.Prefix)
	rules, err := h.Store.RulesOf(ctx, user.ID)
	if err != nil {
		return store.Rule{}, err
	}
	if len(rules) >= maxRules {
		return store.Rule{}, ErrTooManyRules
	}
	for _, r := range rules {
		if r.Kind == store.RuleKindImport && r.Cond.Source == p.Source && r.Cond.Prefix == prefix {
			return store.Rule{}, ErrAlreadyImporting
		}
	}
	probe := store.Rule{OwnerID: user.ID, Cond: store.RuleCond{Source: p.Source, Prefix: prefix}}
	src, err := h.importDest(ctx, probe)
	if err != nil {
		return store.Rule{}, err
	}
	entries, err := src.List(ctx, prefix) // read now: this also shows that it can be reached and listed
	if errors.Is(err, dest.ErrTooManyFiles) {
		return store.Rule{}, ErrImportTooBig
	}
	if err != nil {
		return store.Rule{}, fmt.Errorf("the destination could not be listed: %w", err)
	}
	if _, err := h.ensureLibraryKey(ctx, sess, user, d.Kind == LibraryAlbum); err != nil {
		return store.Rule{}, err
	}
	if err := sess.AddKeyPermission(ctx, mustLibraryKeyID(ctx, h, user.ID), "asset.upload"); err != nil { // a key made before may lack it
		return store.Rule{}, fmt.Errorf("could not let the app add photos to your library: %w", err)
	}
	rule, err := h.Store.AddRule(ctx, store.Rule{OwnerID: user.ID, Kind: store.RuleKindImport, Dest: d.Kind, DestAlbum: d.Album,
		Cond: store.RuleCond{Source: p.Source, SourceName: name, Prefix: prefix}})
	if err != nil {
		return store.Rule{}, err
	}
	if p.History == FollowNew {
		for _, e := range importCandidates(entries) {
			if err := h.Store.PutRulePhoto(ctx, rule.ID, e.Ref, store.RuleSeen, ""); err != nil {
				return rule, err
			}
		}
	}
	h.event(ctx, "", user.ID, "rule", i18n.Pack("{name} follows the destination {destination}", "name", user.Name, "destination", name))
	return rule, nil
}

func mustLibraryKeyID(ctx context.Context, h *Hub, user uuid.UUID) uuid.UUID {
	k, ok, err := h.Store.LibraryKey(ctx, user)
	if err != nil || !ok {
		return uuid.Nil
	}
	return k.KeyID
}

// lookImport looks at one rule that follows a destination: the files that appeared since it last looked become suggestions, or, when the
// person has let the rule go on by itself, are added to their library. It returns how many files are new.
func (h *Hub) lookImport(ctx context.Context, r store.Rule) (int, error) {
	fail := func(problem string, err error) (int, error) {
		h.Store.MarkRuleLook(ctx, r.ID, h.now(), problem)
		return 0, err
	}
	src, err := h.importDest(ctx, r)
	switch {
	case errors.Is(err, ErrImportNotAllowed):
		return fail("the administrator has not allowed importing from this destination any more", err)
	case err != nil:
		return fail("the destination is not available: "+trimNet(err), err)
	}
	entries, err := src.List(ctx, r.Cond.Prefix)
	if errors.Is(err, dest.ErrTooManyFiles) {
		return fail("the destination holds too many files to look at in one go: follow a folder inside it instead", ErrImportTooBig)
	}
	if err != nil {
		return fail("the destination could not be listed: "+trimNet(err), err)
	}
	known, err := h.Store.RulePhotoStates(ctx, r.ID)
	if err != nil {
		return 0, err
	}
	var fresh []string
	why := map[string]string{}
	for _, e := range importCandidates(entries) {
		if known[e.Ref] != "" || (!e.Modified.IsZero() && h.now().Sub(e.Modified) < h.ruleSettle()) {
			continue // seen before, or still being written
		}
		if len(fresh) >= importMostPerLook {
			break
		}
		fresh = append(fresh, e.Ref)
		why[e.Ref] = fmt.Sprintf("%s (%s)", path.Base(e.Ref), sizeWords(e.Size))
	}
	waiting, _ := h.Store.RulePhotos(ctx, r.ID, store.RuleSuggested)
	for _, ref := range fresh {
		if err := h.Store.PutRulePhoto(ctx, r.ID, ref, store.RuleSuggested, why[ref]); err != nil {
			return 0, err
		}
	}
	if r.Mode == store.RuleAuto && len(fresh) > 0 {
		if err := h.importForRule(ctx, r, fresh); err != nil {
			h.Store.MarkRuleLook(ctx, r.ID, h.now(), trimNet(err))
			return len(fresh), err
		}
		h.Store.SetRulePhotos(ctx, r.ID, fresh, store.RuleSuggested, store.RuleSent)
		h.event(ctx, "", r.OwnerID, "import", i18n.PackN("{n} file added to your library from {destination} on its own", "{n} files added to your library from {destination} on their own", len(fresh), "destination", r.Cond.SourceName))
		h.Store.MarkRuleLook(ctx, r.ID, h.now(), "")
		return len(fresh), nil
	}
	h.Store.MarkRuleLook(ctx, r.ID, h.now(), "")
	if len(fresh) > 0 && len(waiting) == 0 {
		h.event(ctx, "", r.OwnerID, "suggestion", i18n.PackN("{n} new file in the destination {destination} you follow. Look at it on the Overview.", "{n} new files in the destination {destination} you follow. Look at them on the Overview.", len(fresh), "destination", r.Cond.SourceName))
	}
	return len(fresh), nil
}

func sizeWords(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}

// importForRule adds files of a followed destination to the owner's library, with the owner's key: each is streamed (never kept on disk),
// with its XMP sidecar when there is one beside it, and Immich refuses a duplicate by checksum. Then it files them where the rule says.
func (h *Hub) importForRule(ctx context.Context, r store.Rule, refs []string) error {
	api, err := h.libraryAPI(ctx, r.OwnerID)
	if err != nil {
		return err
	}
	src, err := h.importDest(ctx, r)
	if err != nil {
		return err
	}
	var album *uuid.UUID
	dst := LibraryDest{Kind: r.Dest, Album: r.DestAlbum}
	for _, ref := range refs {
		if !strings.HasPrefix(ref, r.Cond.Prefix) { // never a file outside what the rule follows
			return fmt.Errorf("%q is not under the folder this rule follows", ref)
		}
		info, err := src.Stat(ctx, ref)
		if err != nil {
			return fmt.Errorf("%s: %w", path.Base(ref), err)
		}
		var sidecar []byte
		if rc, err := src.Open(ctx, ref+".xmp"); err == nil {
			sidecar, _ = io.ReadAll(io.LimitReader(rc, 1<<20))
			rc.Close()
		}
		rc, err := src.Open(ctx, ref)
		if err != nil {
			return fmt.Errorf("%s: %w", path.Base(ref), err)
		}
		at := info.Modified
		if at.IsZero() {
			at = h.now()
		}
		id, _, err := api.UploadFileWith(ctx, path.Base(ref), rc, at, immich.UploadFileOptions{Modified: info.Modified, Sidecar: sidecar})
		rc.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", path.Base(ref), err)
		}
		h.importFile(ctx, api, r, dst, id, &album)
	}
	return nil
}

// importFile files an added photo where the rule says: nowhere (plain), in an album, or tagged with where it came from.
func (h *Hub) importFile(ctx context.Context, api *immich.API, r store.Rule, dst LibraryDest, id uuid.UUID, album **uuid.UUID) {
	switch dst.Kind {
	case LibraryPlain:
		return
	case LibraryAlbum:
		if *album == nil {
			aid, _, err := api.FindAlbum(ctx, dst.Album)
			if err == nil && aid == nil {
				var n uuid.UUID
				if n, err = api.CreateAlbum(ctx, dst.Album); err == nil {
					aid = &n
				}
			}
			if err != nil || aid == nil {
				h.Log.Warn("an imported file could not be put in its album", "err", err)
				return
			}
			*album = aid
		}
		if err := api.AddToAlbum(ctx, **album, id); err != nil {
			h.Log.Warn("an imported file could not be put in its album", "err", err)
		}
		return
	}
	h.waitCopyReady(ctx, api, id)
	if err := h.tagVerified(ctx, api, immich.FromTag(r.Cond.SourceName), id); err != nil {
		h.Log.Warn("an imported file could not be tagged", "err", err)
	}
}

// EncodeImportRef and decodeImportRef make a file's name fit one segment of an address.
func EncodeImportRef(ref string) string { return base64.RawURLEncoding.EncodeToString([]byte(ref)) }

// ImportThumbnail is a small picture of a file of a followed destination, for the cards: the file is read (up to 40 MB, never kept) and
// scaled. A file that is not a picture the app can read (a video, a RAW, a HEIC) has none, and the card shows its name.
func (h *Hub) ImportThumbnail(ctx context.Context, user uuid.UUID, ruleID, enc string) ([]byte, error) {
	r, err := h.ruleOf(ctx, user, ruleID)
	if err != nil || r.Kind != store.RuleKindImport {
		return nil, ErrNoRule
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return nil, ErrNoThumbnail
	}
	ref := string(raw)
	states, err := h.Store.RulePhotoStates(ctx, r.ID)
	if err != nil || states[ref] == "" { // only what the rule has offered, never a file somebody names
		return nil, ErrNoThumbnail
	}
	src, err := h.importDest(ctx, r)
	if err != nil {
		return nil, err
	}
	rc, err := src.Open(ctx, ref)
	if err != nil {
		return nil, ErrNoThumbnail
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, 40<<20+1))
	if err != nil || len(b) > 40<<20 {
		return nil, ErrNoThumbnail
	}
	switch strings.ToLower(path.Ext(ref)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
	default:
		return nil, ErrNoThumbnail
	}
	img, err := safeimg.Decode(b, safeimg.Original)
	if err != nil {
		return nil, ErrNoThumbnail
	}
	bounds := img.Bounds()
	w, hgt := bounds.Dx(), bounds.Dy()
	if w > 480 {
		hgt = hgt * 480 / w
		w = 480
	}
	if w < 1 || hgt < 1 {
		return nil, ErrNoThumbnail
	}
	small := image.NewRGBA(image.Rect(0, 0, w, hgt))
	xdraw.ApproxBiLinear.Scale(small, small.Bounds(), img, bounds, xdraw.Over, nil)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, small, &jpeg.Options{Quality: 75}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
