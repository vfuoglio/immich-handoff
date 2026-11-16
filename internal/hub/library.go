package hub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Where a copy made by "Add to my library" goes in the person's own library.
const (
	LibraryTag   = "tag"   // tagged with who shared it: from/<owner's name>
	LibraryPlain = "plain" // nothing added: the copy is in the library like any photo they uploaded
	LibraryAlbum = "album" // in an album of theirs, made when it does not exist
)

// UndoWindow is how long after a copy was made the person may take it back with Undo.
const UndoWindow = 15 * time.Minute

var (
	ErrNotVisible  = errors.New("some of these photos cannot be seen")
	ErrBadLibrary  = errors.New("choose where the copies go: a tag, your library as it is, or an album")
	ErrNoLibAlbum  = errors.New("choose an album, or give a new one a name")
	ErrBadLibAlbum = errors.New("the album's name is too long")
	ErrUndoLate    = errors.New("it is too late to take these back: they were added more than 15 minutes ago")
	ErrNoCopyKept  = errors.New("none of these copies can be taken back")
)

// How long to wait for Immich to read a new upload before tagging it, how often to look, and how long to wait before
// checking the tag stuck. Tests make them short.
var (
	LibraryWait   = 30 * time.Second
	LibraryPoll   = 150 * time.Millisecond
	LibrarySettle = 400 * time.Millisecond
)

// LibraryDest is where the copies go.
type LibraryDest struct {
	Kind  string // LibraryTag, LibraryPlain or LibraryAlbum; empty means LibraryTag
	Album string // for LibraryAlbum: the album's name (an existing one of the person's, or a new one)
}

// LibraryResult says what a press did.
type LibraryResult struct {
	Added   int         // copies made
	Already int         // photos whose copy was already in the person's library (a repeat press)
	Skipped int         // photos that are the person's own, or are in the trash or are not photos or videos
	Copies  []uuid.UUID // the new copies, for Undo
	Clips   []uuid.UUID // the new hidden motion clips of live photos, for Undo
	Note    string      // set when the copies were made but the tag or the album could not be put on them
}

// AddToLibrary copies photos the person can see but does not own (from an album shared with them, or from a partner who
// shares their library) into their own library, with their own session: the copy is a real photo of theirs, with the
// description, rating, location and a live photo's clip, and the owner is not asked or told. Photos that are the
// person's own are left alone. A photo already copied (the same file) is counted, not copied twice.
func (h *Hub) AddToLibrary(ctx context.Context, sess *immich.API, user immich.User, assets []string, dest LibraryDest) (LibraryResult, error) {
	var res LibraryResult
	ids, err := photoIDs(assets)
	if err != nil {
		return res, err
	}
	dest, err = checkLibraryDest(dest)
	if err != nil {
		return res, err
	}
	// Look at every photo before copying any, so one that cannot be seen stops the whole press.
	infos := make([]*immich.AssetResponseDto, 0, len(ids))
	for _, id := range ids {
		info, err := sess.Asset(ctx, id)
		if err != nil || info == nil {
			return res, ErrNotVisible
		}
		infos = append(infos, info)
	}
	var album *uuid.UUID
	for _, a := range infos {
		if a.OwnerId == user.ID || a.IsTrashed || (a.Type != immich.IMAGE && a.Type != immich.VIDEO) {
			res.Skipped++
			continue
		}
		copyID, clipID, created, clipNew, err := h.copyShared(ctx, sess, a)
		if err != nil {
			return res, fmt.Errorf("%s: %w", a.OriginalFileName, err)
		}
		if clipNew {
			res.Clips = append(res.Clips, clipID)
		}
		if !created {
			res.Already++
			continue
		}
		res.Added++
		res.Copies = append(res.Copies, copyID)
		if note := h.fileCopy(ctx, sess, a, copyID, dest, &album); note != "" && res.Note == "" {
			res.Note = note
		}
	}
	if res.Added > 0 {
		h.event(ctx, "", user.ID, "library", i18n.PackN("{n} photo added to your library from Immich", "{n} photos added to your library from Immich", res.Added))
	}
	return res, nil
}

// Ownership says which of the photos are the person's own and which are someone else's that they can see (a shared
// album, a partner's library). A photo that cannot be read is left out of both. It looks at up to HandOverMax photos,
// a few at a time.
func (h *Hub) Ownership(ctx context.Context, sess *immich.API, user immich.User, assets []string) (mine, others []string, err error) {
	ids, err := photoIDs(assets)
	if err != nil {
		return nil, nil, err
	}
	owned := make([]int, len(ids)) // 0: unreadable, 1: mine, 2: someone else's
	var wg sync.WaitGroup
	gate := make(chan struct{}, 8)
	for i, id := range ids {
		wg.Add(1)
		gate <- struct{}{}
		go func() {
			defer func() { <-gate; wg.Done() }()
			if a, err := sess.Asset(ctx, id); err == nil && a != nil {
				owned[i] = 2
				if a.OwnerId == user.ID {
					owned[i] = 1
				}
			}
		}()
	}
	wg.Wait()
	for i, id := range ids {
		switch owned[i] {
		case 1:
			mine = append(mine, id.String())
		case 2:
			others = append(others, id.String())
		}
	}
	return mine, others, nil
}

// photoIDs parses the ids, once each; there must be at least one and at most HandOverMax.
func photoIDs(assets []string) ([]uuid.UUID, error) {
	seen := map[uuid.UUID]bool{}
	var ids []uuid.UUID
	for _, a := range assets {
		id, err := uuid.Parse(a)
		if err != nil {
			return nil, ErrNotVisible
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	switch {
	case len(ids) == 0:
		return nil, ErrNoPhotosChosen
	case len(ids) > HandOverMax:
		return nil, ErrTooManyChosen
	}
	return ids, nil
}

func checkLibraryDest(d LibraryDest) (LibraryDest, error) {
	switch d.Kind {
	case "":
		d.Kind = LibraryTag
	case LibraryTag, LibraryPlain, LibraryAlbum:
	default:
		return d, ErrBadLibrary
	}
	d.Album = strings.TrimSpace(d.Album)
	if d.Kind == LibraryAlbum {
		if d.Album == "" {
			return d, ErrNoLibAlbum
		}
		if utf8.RuneCountInString(d.Album) > 100 {
			return d, ErrBadLibAlbum
		}
	}
	return d, nil
}

// copyShared puts one photo (and its motion clip, for a live photo) in the person's own library, streamed from where
// they can see it, and checks the copies by checksum. created is false when the photo's copy was already there.
func (h *Hub) copyShared(ctx context.Context, sess *immich.API, a *immich.AssetResponseDto) (copyID, clipID uuid.UUID, created, clipNew bool, err error) {
	var motion *uuid.UUID
	if a.LivePhotoVideoId != nil {
		if cid, perr := uuid.Parse(*a.LivePhotoVideoId); perr == nil {
			if clip, cerr := sess.Asset(ctx, cid); cerr == nil && clip != nil && !clip.IsTrashed {
				up, sum, terr := immich.TransferWith(ctx, sess, sess, *clip, immich.UploadOptions{Hidden: true})
				if terr != nil {
					return uuid.Nil, uuid.Nil, false, false, fmt.Errorf("motion clip: %w", terr)
				}
				cc, aerr := sess.Asset(ctx, up.Id)
				if aerr != nil {
					return uuid.Nil, uuid.Nil, false, false, fmt.Errorf("motion clip: %w", aerr)
				}
				if cc.IsTrashed { // taken out before (Undo, or the person's own delete) and pressed again: it is wanted back
					if rerr := sess.RestoreAssets(ctx, []uuid.UUID{up.Id}); rerr != nil {
						return uuid.Nil, uuid.Nil, false, false, fmt.Errorf("motion clip: %w", rerr)
					}
				}
				if !immich.ChecksumMatches(cc.Checksum, sum) {
					return uuid.Nil, uuid.Nil, false, false, fmt.Errorf("checksum mismatch on the copy %s of the motion clip", up.Id)
				}
				id := up.Id
				motion = &id
				clipID, clipNew = up.Id, up.Status == immich.AssetMediaStatusCreated
			}
		}
	}
	if e := a.ExifInfo; e == nil || (e.Description == nil && e.Rating == nil && e.Latitude == nil) {
		// a plain read may give only part of what Immich knows; the photo itself has the description, rating and location
		if full, ferr := sess.Asset(ctx, a.Id); ferr == nil && full != nil {
			a = full
		}
	}
	up, sum, err := immich.TransferWith(ctx, sess, sess, *a, immich.UploadOptions{MotionClip: motion, Details: true})
	if err != nil {
		return uuid.Nil, clipID, false, clipNew, err
	}
	cp, err := sess.Asset(ctx, up.Id)
	if err != nil {
		return uuid.Nil, clipID, false, clipNew, err
	}
	if !immich.ChecksumMatches(cp.Checksum, sum) {
		return uuid.Nil, clipID, false, clipNew, fmt.Errorf("checksum mismatch on the copy %s", up.Id)
	}
	if cp.IsTrashed {
		// A copy that was taken out before (Undo, or the person's own delete) and added again is wanted back: Immich
		// answers "duplicate" with the one in the trash, so restore it, and file it like a new copy.
		if err := sess.RestoreAssets(ctx, []uuid.UUID{up.Id}); err != nil {
			return uuid.Nil, clipID, false, clipNew, err
		}
		return up.Id, clipID, true, clipNew, nil
	}
	return up.Id, clipID, up.Status == immich.AssetMediaStatusCreated, clipNew, nil
}

// fileCopy puts the destination on a new copy: waits for Immich to read the upload, then tags it (checking the tag
// stuck) or adds it to the album. It returns a short note when that did not work; the copy is kept either way.
func (h *Hub) fileCopy(ctx context.Context, sess *immich.API, from *immich.AssetResponseDto, copyID uuid.UUID, dest LibraryDest, album **uuid.UUID) string {
	switch dest.Kind {
	case LibraryPlain:
		return ""
	case LibraryAlbum:
		if *album == nil {
			id, _, err := sess.FindAlbum(ctx, dest.Album)
			if err == nil && id == nil {
				var nid uuid.UUID
				if nid, err = sess.CreateAlbum(ctx, dest.Album); err == nil {
					id = &nid
				}
			}
			if err != nil || id == nil {
				return "the copies were made but the album could not be made"
			}
			*album = id
		}
		if err := sess.AddToAlbum(ctx, **album, copyID); err != nil {
			return "the copies were made but could not be put in the album"
		}
		return ""
	}
	name := "someone"
	if from.Owner != nil && strings.TrimSpace(from.Owner.Name) != "" {
		name = from.Owner.Name
	}
	h.waitCopyReady(ctx, sess, copyID)
	if err := h.tagVerified(ctx, sess, immich.FromTag(name), copyID); err != nil {
		h.Log.Warn("could not tag a copy made by Add to my library", "err", err)
		return "the copies were made but could not be tagged"
	}
	return ""
}

func (h *Hub) waitCopyReady(ctx context.Context, sess *immich.API, id uuid.UUID) {
	deadline := time.Now().Add(LibraryWait)
	for time.Now().Before(deadline) {
		if a, err := sess.Asset(ctx, id); err == nil && a.HasMetadata && a.ExifInfo != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(LibraryPoll):
		}
	}
}

// tagVerified adds a tag and checks it is there afterwards: Immich sometimes drops a tag added right after an upload.
func (h *Hub) tagVerified(ctx context.Context, sess *immich.API, name string, id uuid.UUID) error {
	tag, err := sess.FindTag(ctx, name, true)
	if err != nil || tag == nil {
		return fmt.Errorf("ensure tag %q: %v", name, err)
	}
	for attempt := 0; attempt < 5; attempt++ {
		if err := sess.TagAssets(ctx, *tag, id); err != nil {
			return err
		}
		time.Sleep(LibrarySettle)
		a, err := sess.Asset(ctx, id)
		if err != nil {
			return err
		}
		if a.Tags != nil {
			for _, t := range *a.Tags {
				if t.Name == name || t.Value == name {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("tag %q did not stick on %s", name, id)
}

// UndoAddToLibrary takes copies made by AddToLibrary back out of the person's library, into Immich's trash (from where
// they can be restored). Only copies that are the person's own, were added in the last UndoWindow, and are not filed in
// an album other than the destination one (album, when the copies went to one) are removed; the rest stay.
// It returns how many were removed.
func (h *Hub) UndoAddToLibrary(ctx context.Context, sess *immich.API, user immich.User, copies []string, album string) (int, error) {
	ids, err := photoIDs(copies)
	if err != nil {
		return 0, err
	}
	var inDest map[uuid.UUID]bool
	if album = strings.TrimSpace(album); album != "" {
		if aid, _, ferr := sess.FindAlbum(ctx, album); ferr == nil && aid != nil {
			if list, lerr := sess.AlbumAssets(ctx, *aid); lerr == nil {
				inDest = map[uuid.UUID]bool{}
				for _, a := range list {
					inDest[a.Id] = true
				}
			}
		}
	}
	var gone []uuid.UUID
	removed := 0
	late := false
	for _, id := range ids {
		a, err := sess.Asset(ctx, id)
		if err != nil || a == nil || a.OwnerId != user.ID {
			return 0, ErrNotYourPhotos
		}
		if time.Since(a.CreatedAt) > UndoWindow {
			late = true
			continue
		}
		if !inDest[id] {
			if filed, err := sess.InAnAlbum(ctx, id); err != nil || filed {
				continue // the person has put it in an album of their own: it is theirs now
			}
		}
		gone = append(gone, id)
		removed++
		if a.LivePhotoVideoId != nil { // a live photo's hidden clip goes with it
			if cid, err := uuid.Parse(*a.LivePhotoVideoId); err == nil {
				gone = append(gone, cid)
			}
		}
	}
	if len(gone) == 0 {
		if late {
			return 0, ErrUndoLate
		}
		return 0, ErrNoCopyKept
	}
	if err := sess.DeleteAssets(ctx, gone, false); err != nil {
		return 0, err
	}
	return removed, nil
}

// LibraryDefault is where the person's copies go unless the button is told otherwise: what they chose to remember, or a
// tag naming who shared the photo.
func (h *Hub) LibraryDefault(ctx context.Context, user uuid.UUID) LibraryDest {
	d, _ := h.LibraryChoice(ctx, user)
	return d
}

// LibraryChoice is LibraryDefault, and whether the person has made the choice (the button asks only until they have).
func (h *Hub) LibraryChoice(ctx context.Context, user uuid.UUID) (LibraryDest, bool) {
	if h != nil && h.Store != nil {
		if p, ok, err := h.Store.LibraryPref(ctx, user); err == nil && ok {
			return LibraryDest{Kind: p.Dest, Album: p.Album}, true
		}
	}
	return LibraryDest{Kind: LibraryTag}, false
}

// SetLibraryDefault remembers where the person's copies go.
func (h *Hub) SetLibraryDefault(ctx context.Context, user uuid.UUID, d LibraryDest) error {
	d, err := checkLibraryDest(d)
	if err != nil {
		return err
	}
	if h.Store == nil {
		return nil
	}
	if d.Kind != LibraryAlbum {
		d.Album = ""
	}
	return h.Store.SetLibraryPref(ctx, user, store.LibraryPref{Dest: d.Kind, Album: d.Album})
}
