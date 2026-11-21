package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/vfuoglio/immich-handoff/internal/bridge"
	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// The one-app way of sharing an album (docs/contributing/design/shared-albums-design.md): the other person has an Immich and no Handoff. This app
// works on both sides, with a key it made on its own person's Immich and a key the other person made on theirs. The key is
// judged by exactly what is needed (bridge.CheckShared): a small key only receives (the photos are put into an album on their
// server); a bigger one also reads that album, so photos they add come here. Taking a photo out of the album does not travel,
// because the small key cannot see what is in the album and the bigger one is kept as small as it can be.

var (
	ErrBadImmichKey  = errors.New("give the key the other person made on their Immich")
	ErrBadPersonName = errors.New("say who this is, in one to forty characters")
)

// ImmichShare is what a person fills in to share an album with someone who has no Handoff.
type ImmichShare struct {
	Person  string    // what the other person is called
	Address string    // their Immich
	Key     string    // the key they made
	Name    string    // the shared album's name (also the name of the album made on their server)
	AlbumID uuid.UUID // the person's own album; the zero value makes a new one
	History string
}

// ShareWithImmich shares an album with someone who has an Immich and no Handoff. The key is checked first; if it is no good
// nothing is made.
func (h *Hub) ShareWithImmich(ctx context.Context, sess *immich.API, owner immich.User, in ImmichShare) (store.SharedAlbum, error) {
	if !h.SharingOn() {
		return store.SharedAlbum{}, ErrSharingOff
	}
	person := strings.TrimSpace(in.Person)
	if n := utf8.RuneCountInString(person); n < 1 || n > 40 || strings.ContainsAny(person, "\r\n\x00") {
		return store.SharedAlbum{}, ErrBadPersonName
	}
	name, history, _, err := checkShare(ShareParams{Name: in.Name, History: in.History}, true)
	if err != nil {
		return store.SharedAlbum{}, err
	}
	key := strings.TrimSpace(in.Key)
	if key == "" || len(key) > 512 || strings.ContainsAny(key, " \r\n\x00") {
		return store.SharedAlbum{}, ErrBadImmichKey
	}
	addr, err := h.Bridge.Policy.Address(in.Address)
	if err != nil {
		return store.SharedAlbum{}, err
	}
	if list, err := h.Store.SharedAlbumsOf(ctx, owner.ID); err != nil {
		return store.SharedAlbum{}, err
	} else if len(list) >= maxShared {
		return store.SharedAlbum{}, ErrTooManyShared
	}
	if peers, err := h.Store.PeersOf(ctx, owner.ID); err == nil && len(peers) >= maxPeers {
		return store.SharedAlbum{}, ErrTooManyPeers
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, twoWay, err := bridge.CheckShared(cctx, h.peerClient(), addr, key)
	if err != nil {
		return store.SharedAlbum{}, err
	}
	theirs, err := immich.New(addr, immich.Auth{APIKey: key}, h.peerClient())
	if err != nil {
		return store.SharedAlbum{}, err
	}
	// their album: an existing one of that name if the key can look, otherwise a new one
	var theirAlbum openapi_types.UUID
	existing := false
	if twoWay {
		if owned, err := theirs.OwnedAlbums(cctx); err == nil {
			for _, al := range owned {
				if al.Name == name {
					theirAlbum, existing = al.ID, true
					break
				}
			}
		} else {
			return store.SharedAlbum{}, fmt.Errorf("could not read their albums: %s", trimNet(err))
		}
	}
	if !existing {
		if theirAlbum, err = theirs.CreateAlbum(cctx, name); err != nil {
			return store.SharedAlbum{}, fmt.Errorf("could not make the album on their Immich: %s", trimNet(err))
		}
	}
	if err := h.checkGroupRoom(ctx, owner.ID, in.AlbumID); err != nil {
		return store.SharedAlbum{}, err
	}
	id := uuid.NewString()
	album, albumName, keyID, secret, err := h.ownAlbum(ctx, sess, owner, in.AlbumID, name, history, id)
	if err != nil {
		return store.SharedAlbum{}, err
	}
	pr, err := h.Store.AddPeer(ctx, store.Peer{OwnerID: owner.ID, OwnerName: owner.Name, Name: person, Address: addr, Secret: key, Role: store.PeerImmich, SeenAt: h.now()})
	if err != nil {
		sess.DeleteAPIKey(context.WithoutCancel(ctx), keyID)
		return store.SharedAlbum{}, err
	}
	theirHistory := store.HistoryNew
	if existing {
		theirHistory = store.HistoryAll // what is in that album already will come here
	}
	a, err := h.Store.AddSharedAlbum(ctx, store.SharedAlbum{ID: id, OwnerID: owner.ID, PeerID: pr.ID, Name: name, AlbumID: album, AlbumName: albumName, KeyID: keyID, Key: secret,
		State: store.ShareActive, Proposer: true, History: history, TheirHistory: theirHistory, Deletion: store.DeleteNothing, TheirAlbum: theirAlbum.String(), TwoWay: twoWay})
	if err != nil {
		sess.DeleteAPIKey(context.WithoutCancel(ctx), keyID)
		h.Store.DeletePeer(context.WithoutCancel(ctx), pr.ID)
		return store.SharedAlbum{}, err
	}
	h.event(ctx, "", owner.ID, "share", i18n.Pack("{name} began sharing the album {album} with {other}, who has no Handoff", "name", owner.Name, "album", name, "other", person))
	return a, nil
}

// checkImmichPeer asks the other Immich whether the key still works, and records the answer for the page.
func (h *Hub) checkImmichPeer(ctx context.Context, p store.Peer) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, _, err := bridge.CheckShared(cctx, h.peerClient(), p.Address, p.Secret); err != nil {
		reason := err.Error()
		var f *bridge.Failure
		if errors.As(err, &f) {
			reason = f.Kind.Error()
			if f.Detail != "" {
				reason += " (" + f.Detail + ")"
			}
		}
		if p.Problem == "" {
			h.event(ctx, "", p.OwnerID, "sharing_problem", i18n.Pack("The connection to {name} is not working: {reason}", "name", p.Name, "reason", reason))
		}
		h.Store.MarkPeerSeen(context.WithoutCancel(ctx), p.ID, time.Time{}, reason)
		return
	}
	if p.Problem != "" {
		h.event(ctx, "", p.OwnerID, "peer", i18n.Pack("The connection to {name} works again", "name", p.Name))
	}
	h.Store.MarkPeerSeen(context.WithoutCancel(ctx), p.ID, h.now(), "")
}

// syncImmich is a pass of an album shared with someone who has no Handoff: what is new here goes to their album, and with a
// two-way key what is new in their album comes here. Their key cannot say whether they already have a photo (a small key cannot
// read), so every photo is put to them and Immich answers that it is a duplicate when it is.
func (h *Hub) syncImmich(ctx context.Context, a store.SharedAlbum, p store.Peer) (sent, received int, problem string) {
	mine, err := h.Connect(a.Key)
	if err != nil {
		return 0, 0, err.Error()
	}
	theirs, err := immich.New(p.Address, immich.Auth{APIKey: p.Secret}, h.peerClient())
	if err != nil {
		return 0, 0, err.Error()
	}
	theirAlbum, err := uuid.Parse(a.TheirAlbum)
	if err != nil {
		return 0, 0, "the album on their Immich is not known"
	}
	by, problem := h.noteOwn(ctx, a, mine)
	if problem != "" {
		return 0, 0, problem
	}
	_, order, err := h.Store.LedgerUnsent(ctx, a.ID)
	if err != nil {
		return 0, 0, err.Error()
	}
	for _, c := range order {
		x, ok := by[c]
		if !ok {
			continue
		}
		if ctx.Err() != nil {
			return sent, received, "stopped"
		}
		switch err := h.putToImmich(ctx, mine, theirs, theirAlbum, a.TwoWay, x); {
		case errors.Is(err, ErrSharedTooBig):
			h.Store.LedgerSkip(ctx, a.ID, c)
			problem = fmt.Sprintf("%s is larger than %d MB and was not sent", x.OriginalFileName, h.sharedMaxFile()>>20)
		case err != nil:
			return sent, received, themProblem(err)
		default:
			h.Store.LedgerMarkSent(ctx, a.ID, c)
			sent++
		}
	}
	if a.TwoWay {
		n, rproblem := h.takeFromImmich(ctx, a, mine, theirs, theirAlbum)
		received = n
		if rproblem != "" {
			problem = rproblem
		}
	}
	return sent, received, problem
}

func themProblem(err error) string {
	switch {
	case immich.IsAuthError(err):
		return "their Immich no longer accepts the key"
	}
	return "their Immich: " + trimNet(err)
}

// putToImmich streams one original from the person's Immich into the other person's, and puts it in their album (unless the copy
// they already had is in their trash and the key can tell).
func (h *Hub) putToImmich(ctx context.Context, mine, theirs *immich.API, album uuid.UUID, canRead bool, x immich.AssetResponseDto) error {
	resp, err := mine.Original(ctx, x.Id)
	if err != nil {
		if immich.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("could not read %s: %s", x.OriginalFileName, trimNet(err))
	}
	defer resp.Body.Close()
	if resp.ContentLength > h.sharedMaxFile() {
		return ErrSharedTooBig
	}
	id, _, err := theirs.UploadFile(ctx, cleanFileName(x.OriginalFileName), io.LimitReader(resp.Body, h.sharedMaxFile()+1), x.FileCreatedAt)
	if err != nil {
		return err
	}
	if canRead {
		if as, err := theirs.Asset(ctx, id); err == nil && as.IsTrashed {
			return nil // they deleted it from their library: their deleting is respected
		}
	}
	return theirs.AddToAlbum(ctx, album, id)
}

// takeFromImmich brings here what is new in their album: photos the ledger has not seen.
func (h *Hub) takeFromImmich(ctx context.Context, a store.SharedAlbum, mine, theirs *immich.API, album uuid.UUID) (n int, problem string) {
	list, err := theirs.AlbumAssets(ctx, album)
	if err != nil {
		return 0, themProblem(err)
	}
	for _, x := range list {
		if x.Checksum == "" || x.IsTrashed {
			continue
		}
		if ctx.Err() != nil {
			return n, "stopped"
		}
		if _, _, known, err := h.Store.LedgerHas(ctx, a.ID, x.Checksum); err != nil {
			return n, err.Error()
		} else if known {
			continue
		}
		resp, err := theirs.Original(ctx, x.Id)
		if err != nil {
			if immich.IsNotFound(err) {
				continue
			}
			return n, themProblem(err)
		}
		if resp.ContentLength > h.sharedMaxFile() {
			resp.Body.Close()
			h.Store.LedgerPut(ctx, a.ID, x.Checksum, "", store.OriginArrived, true)
			problem = fmt.Sprintf("%s is larger than %d MB and was not brought over", x.OriginalFileName, h.sharedMaxFile()>>20)
			continue
		}
		id, dup, err := mine.UploadFile(ctx, cleanFileName(x.OriginalFileName), io.LimitReader(resp.Body, h.sharedMaxFile()+1), x.FileCreatedAt)
		resp.Body.Close()
		if err != nil {
			return n, err.Error()
		}
		as, err := mine.Asset(ctx, id)
		if err != nil {
			return n, err.Error()
		}
		if as.Checksum != x.Checksum {
			h.Log.Warn("a photo came from another server with another checksum than listed", "album", a.ID)
			return n, ErrBadSharedPhoto.Error()
		}
		if !as.IsTrashed {
			if err := mine.AddToAlbum(ctx, a.AlbumID, id); err != nil {
				return n, err.Error()
			}
		}
		h.Store.LedgerPut(ctx, a.ID, x.Checksum, id.String(), store.OriginArrived, true)
		if !dup || !as.IsTrashed {
			n++
		}
	}
	return n, problem
}
