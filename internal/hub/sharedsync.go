package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/peer"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Moving the photos of a shared album (docs/shared-albums-design.md). Each app only sends what is new in its own copy of the
// album and the other app only stores what it is sent, so both ways is the same code run on both sides. What is new is told by
// checksum against a ledger: a photo that arrived is remembered as arrived and never sent back; one that was there when sharing
// began (and its person chose "only what is new") is remembered as the baseline and never sent.

// ErrSharedBusy is returned when a pass of the album is already running.
var ErrSharedBusy = errors.New("this album is being looked at already; try again in a moment")

// SharedEvery is how often each app looks at its shared albums (SHARED_ALBUMS_EVERY_MINUTES, default 3).
func (h *Hub) sharedEvery() time.Duration {
	if h.SharedEvery > 0 {
		return h.SharedEvery
	}
	return 3 * time.Minute
}

func (h *Hub) sharedMaxFile() int64 {
	if h.SharedMaxFile > 0 {
		return h.SharedMaxFile
	}
	return 2 << 30
}

// HaveMsg and HaveReply are how a sender asks which of its photos the other side lacks.
type HaveMsg struct {
	To        string   `json:"to"`
	Checksums []string `json:"checksums"`
}

type HaveReply struct {
	Missing []string `json:"missing"`
}

// SharedActive finds the shared album a signed request is about, if it is active and the connection is not paused.
func (h *Hub) sharedFor(ctx context.Context, p store.Peer, id string) (store.SharedAlbum, error) {
	a, err := h.Store.SharedAlbumByID(ctx, id)
	if err != nil || a.PeerID != p.ID || a.State != store.ShareActive {
		return store.SharedAlbum{}, ErrNoSharedAlbum
	}
	if p.IPaused {
		return store.SharedAlbum{}, ErrPeerNotActive
	}
	return a, nil
}

// PeerHave answers which of the checksums this side lacks: those its ledger does not know.
func (h *Hub) PeerHave(ctx context.Context, p store.Peer, m HaveMsg) (HaveReply, error) {
	a, err := h.sharedFor(ctx, p, m.To)
	if err != nil {
		return HaveReply{}, err
	}
	if len(m.Checksums) > 2000 {
		return HaveReply{}, ErrNoSharedAlbum
	}
	out := HaveReply{Missing: []string{}}
	for _, c := range m.Checksums {
		if origin, _, known, err := h.Store.LedgerHas(ctx, a.ID, c); err != nil {
			return HaveReply{}, err
		} else if !known || origin == store.OriginRemoved {
			out.Missing = append(out.Missing, c)
		}
	}
	return out, nil
}

// PhotoIn is a photo that arrives through a shared album.
type PhotoIn struct {
	To, Checksum, Name string
	Taken              time.Time
	Size               int64
	Body               io.Reader
}

// PeerPhoto stores a photo the other side sent: in this person's own Immich with the app's key, into the album, and remembers
// it as arrived so it is never sent back. A photo Immich already has is not stored again; one the person put in their trash is
// left there and is not put in the album, so their deleting is respected.
func (h *Hub) PeerPhoto(ctx context.Context, p store.Peer, in PhotoIn) error {
	a, err := h.sharedFor(ctx, p, in.To)
	if err != nil {
		return err
	}
	if in.Checksum == "" || len(in.Checksum) > 64 {
		return ErrBadSharedPhoto
	}
	if in.Size > h.sharedMaxFile() {
		return ErrSharedTooBig
	}
	api, err := h.Connect(a.Key)
	if err != nil {
		return err
	}
	name := cleanFileName(in.Name)
	id, dup, err := api.UploadFile(ctx, name, io.LimitReader(in.Body, h.sharedMaxFile()+1), in.Taken)
	if err != nil {
		return err
	}
	asset, err := api.Asset(ctx, id)
	if err != nil {
		return err
	}
	if asset.Checksum != in.Checksum {
		// the bytes that reached Immich are not the ones that were announced: do not put the photo in the album
		h.Log.Warn("a shared photo arrived with another checksum than announced", "album", a.ID)
		return ErrBadSharedPhoto
	}
	if !asset.IsTrashed {
		if err := api.AddToAlbum(ctx, uuidOfAlbum(a), id); err != nil {
			return err
		}
	}
	if origin, _, known, _ := h.Store.LedgerHas(ctx, a.ID, in.Checksum); known && origin == store.OriginRemoved {
		h.Store.LedgerRestore(ctx, a.ID, in.Checksum, id.String(), store.OriginArrived, true) // it was taken out and has come back
	} else {
		h.Store.LedgerPut(ctx, a.ID, in.Checksum, id.String(), store.OriginArrived, true)
	}
	if !dup || !asset.IsTrashed {
		h.Store.AddSharedReceived(ctx, a.ID, 1)
	}
	return nil
}

var (
	ErrBadSharedPhoto = errors.New("that photo did not arrive as announced")
	ErrSharedTooBig   = errors.New("that file is larger than this server accepts for shared albums")
)

func uuidOfAlbum(a store.SharedAlbum) uuid.UUID { return a.AlbumID }

// cleanFileName makes a file name from what the other app sent: no path, no control characters.
func cleanFileName(s string) string {
	s = strings.ReplaceAll(s, "\\", "/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if s == "" || s == "." || s == ".." {
		return "photo"
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// SyncShared looks at one shared album and sends what is new to the other app. It does nothing for an album that is not active,
// or whose connection is paused on either side. Only one pass of an album runs at a time.
func (h *Hub) SyncShared(ctx context.Context, id string) error {
	a, err := h.Store.SharedAlbumByID(ctx, id)
	if err != nil || a.State != store.ShareActive {
		return ErrNoSharedAlbum
	}
	p, err := h.Store.PeerByID(ctx, a.PeerID)
	if err != nil || p.State != store.PeerActive || p.IPaused || p.TheyPaused {
		return nil
	}
	if _, busy := h.syncing.LoadOrStore(a.ID, true); busy {
		return ErrSharedBusy
	}
	defer h.syncing.Delete(a.ID)
	sent, problem := h.sendNew(ctx, a, p)
	h.Store.MarkSharedPass(context.WithoutCancel(ctx), a.ID, h.now(), problem, sent, 0)
	switch {
	case problem != "" && a.Problem == "": // told once, not on every look
		h.event(ctx, "", a.OwnerID, "sharing_problem", i18n.Pack("Sharing the album {album} with {other} ran into a problem: {reason}", "album", a.Name, "other", p.Name, "reason", problem))
	case problem == "" && a.Problem != "":
		h.event(ctx, "", a.OwnerID, "share", i18n.Pack("Sharing the album {album} with {other} works again", "album", a.Name, "other", p.Name))
	}
	if sent > 0 { // the other person is told that photos arrived, once for the whole pass
		var reply struct{}
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		peer.Call(cctx, h.peerClient(), p.Address, p.TheirID, p.Secret, "POST", "/peer/v1/album/done", DoneMsg{To: a.TheirID, N: sent}, &reply)
		cancel()
	}
	if problem != "" {
		return errors.New(problem)
	}
	return nil
}

func (h *Hub) sendNew(ctx context.Context, a store.SharedAlbum, p store.Peer) (sent int, problem string) {
	api, err := h.Connect(a.Key)
	if err != nil {
		return 0, err.Error()
	}
	list, err := api.AlbumAssets(ctx, a.AlbumID)
	if err != nil {
		return 0, "could not read the album: " + trimNet(err)
	}
	by := map[string]immich.AssetResponseDto{}
	for _, x := range list {
		if x.Checksum == "" || x.IsTrashed {
			continue
		}
		by[x.Checksum] = x
		// a photo the ledger has not seen is this person's own new photo; one that was taken out and is back is new again
		origin, _, known, err := h.Store.LedgerHas(ctx, a.ID, x.Checksum)
		switch {
		case err != nil:
			return 0, err.Error()
		case !known:
			h.Store.LedgerPut(ctx, a.ID, x.Checksum, x.Id.String(), store.OriginMine, false)
		case origin == store.OriginRemoved:
			h.Store.LedgerRestore(ctx, a.ID, x.Checksum, x.Id.String(), store.OriginMine, false)
		}
	}
	h.followRemovals(ctx, a, p, api, by)
	_, order, err := h.Store.LedgerUnsent(ctx, a.ID)
	if err != nil {
		return 0, err.Error()
	}
	var pending []string
	for _, c := range order {
		if _, ok := by[c]; ok {
			pending = append(pending, c)
		}
	}
	if len(pending) == 0 {
		return 0, ""
	}
	var reply HaveReply
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	code, why, err := peer.Call(cctx, h.peerClient(), p.Address, p.TheirID, p.Secret, "POST", "/peer/v1/album/have", HaveMsg{To: a.TheirID, Checksums: pending}, &reply)
	cancel()
	switch {
	case err != nil:
		return 0, "the other app could not be reached: " + trimNet(err)
	case code != 200:
		return 0, "the other app refused: " + why
	}
	missing := map[string]bool{}
	for _, c := range reply.Missing {
		missing[c] = true
	}
	for _, c := range pending {
		if ctx.Err() != nil {
			return sent, "stopped"
		}
		if !missing[c] { // the other side has it (an echo, or the same photo on both sides)
			h.Store.LedgerMarkSent(ctx, a.ID, c)
			continue
		}
		x := by[c]
		if err := h.sendPhoto(ctx, api, a, p, x); err != nil {
			if errors.Is(err, ErrSharedTooBig) {
				h.Store.LedgerSkip(ctx, a.ID, c)
				problem = fmt.Sprintf("%s is larger than %d MB and was not sent", x.OriginalFileName, h.sharedMaxFile()>>20)
				continue
			}
			return sent, err.Error()
		}
		h.Store.LedgerMarkSent(ctx, a.ID, c)
		sent++
	}
	return sent, problem
}

// sendPhoto streams one original to the other app.
func (h *Hub) sendPhoto(ctx context.Context, api *immich.API, a store.SharedAlbum, p store.Peer, x immich.AssetResponseDto) error {
	resp, err := api.Original(ctx, x.Id)
	if err != nil {
		if immich.IsNotFound(err) {
			return nil // gone since the listing
		}
		return fmt.Errorf("could not read %s: %s", x.OriginalFileName, trimNet(err))
	}
	defer resp.Body.Close()
	if resp.ContentLength > h.sharedMaxFile() {
		return ErrSharedTooBig
	}
	q := url.Values{"to": {a.TheirID}, "checksum": {x.Checksum}, "name": {x.OriginalFileName}, "taken": {x.FileCreatedAt.UTC().Format(time.RFC3339Nano)}, "size": {strconv.FormatInt(resp.ContentLength, 10)}}
	var reply struct{}
	code, why, err := peer.Stream(ctx, h.peerClient(), p.Address, p.TheirID, p.Secret, "POST", "/peer/v1/album/photo?"+q.Encode(), resp.Body, resp.ContentLength, &reply)
	switch {
	case err != nil:
		return fmt.Errorf("sending %s: %s", x.OriginalFileName, trimNet(err))
	case code == http.StatusRequestEntityTooLarge:
		return ErrSharedTooBig
	case code != 200:
		return fmt.Errorf("the other app did not take %s: %s", x.OriginalFileName, why)
	}
	return nil
}

// SyncSharedNow looks at an album at once, for its owner (the "Sync now" button), in the background.
func (h *Hub) SyncSharedNow(ctx context.Context, owner uuid.UUID, id string) error {
	a, err := h.sharedOf(ctx, owner, id)
	if err != nil {
		return err
	}
	if a.State != store.ShareActive {
		return ErrNoSharedAlbum
	}
	if _, busy := h.syncing.Load(a.ID); busy {
		return ErrSharedBusy
	}
	go func() {
		if err := h.SyncShared(context.WithoutCancel(ctx), a.ID); err != nil && !errors.Is(err, ErrSharedBusy) {
			h.Log.Debug("a shared album pass ended with a problem", "err", err)
		}
	}()
	return nil
}

// SharedMaintenance looks at every active shared album, one after the other.
func (h *Hub) SharedMaintenance(ctx context.Context) {
	if !h.PeersOn() {
		return
	}
	list, err := h.Store.ActiveSharedAlbums(ctx)
	if err != nil {
		return
	}
	for _, a := range list {
		if ctx.Err() != nil {
			return
		}
		if err := h.SyncShared(ctx, a.ID); err != nil && !errors.Is(err, ErrSharedBusy) {
			h.Log.Debug("a shared album pass ended with a problem", "album", a.ID, "err", err)
		}
	}
}

// SharedLoop runs SharedMaintenance every few minutes until the context ends.
func (h *Hub) SharedLoop(ctx context.Context) {
	t := time.NewTicker(h.sharedEvery())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.SharedMaintenance(ctx)
		}
	}
}

// DoneMsg tells the receiving app that a pass sent it N photos, so that its person is told once.
type DoneMsg struct {
	To string `json:"to"`
	N  int    `json:"n"`
}

// PeerArrived is what a signed "done" does: a line for the person, that photos arrived.
func (h *Hub) PeerArrived(ctx context.Context, p store.Peer, m DoneMsg) error {
	a, err := h.sharedFor(ctx, p, m.To)
	if err != nil {
		return err
	}
	if m.N < 1 || m.N > 100000 {
		return nil
	}
	h.event(ctx, "", a.OwnerID, "share", i18n.PackN("{n} photo arrived from {other} in {album}", "{n} photos arrived from {other} in {album}", m.N, "other", p.Name, "album", a.Name))
	return nil
}
