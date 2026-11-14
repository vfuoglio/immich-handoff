package hub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/peer"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Shared albums: an album of this person's that is kept the same as an album of the person on the other server. This file is
// agreeing on one: a proposal from either side, an answer, the key each app gets for its own Immich, and what each side chose
// to share. The photos themselves move in a later part (docs/shared-albums-design.md).

var (
	ErrBadShareName   = errors.New("give the shared album a name of one to sixty characters")
	ErrBadHistory     = errors.New("choose what to share at the start: what is in the album now, or only what you add from now on")
	ErrBadDeletion    = errors.New("choose what removing a photo does: nothing, or take it out of the other album too")
	ErrPeerNotActive  = errors.New("that connection is paused or ended; sharing needs it to work")
	ErrNoSharedAlbum  = errors.New("that is not one of your shared albums")
	ErrNotYetAnswered = errors.New("that proposal has already been answered")
	ErrTooManyShared  = errors.New("you already share 50 albums; end one first")
)

const maxShared = 50

// ShareParams is what a person chooses when proposing or accepting a shared album.
type ShareParams struct {
	PeerID   string
	Name     string    // the shared album's name between the two people (a proposal)
	AlbumID  uuid.UUID // one of the person's own albums; the zero value makes a new one
	History  string    // store.HistoryAll or store.HistoryNew
	Deletion string    // store.DeleteNothing or store.DeleteFollow (a proposal; the other side agrees to it)
}

func checkShare(p ShareParams, proposing bool) (name, history, deletion string, err error) {
	name = strings.TrimSpace(p.Name)
	if proposing {
		if n := utf8.RuneCountInString(name); n < 1 || n > 60 || strings.ContainsAny(name, "\r\n\x00") {
			return "", "", "", ErrBadShareName
		}
	}
	switch p.History {
	case store.HistoryAll, store.HistoryNew:
		history = p.History
	default:
		return "", "", "", ErrBadHistory
	}
	deletion = p.Deletion
	if deletion == "" {
		deletion = store.DeleteNothing
	}
	if deletion != store.DeleteNothing && deletion != store.DeleteFollow {
		return "", "", "", ErrBadDeletion
	}
	return name, history, deletion, nil
}

// ownAlbum finds the album a person chose (or makes one) and a key for this app to work on it with: the permissions of
// immich.SharedAlbumPermissions, made with the person's own session. It also remembers, for "only what is new", every photo that
// is in the album now, so none of them is ever sent.
func (h *Hub) ownAlbum(ctx context.Context, sess *immich.API, owner immich.User, id uuid.UUID, newName, history string, record string) (album uuid.UUID, albumName string, keyID uuid.UUID, secret string, err error) {
	albums, err := sess.OwnedAlbums(ctx)
	if err != nil {
		return
	}
	if id == uuid.Nil {
		var made openapi_types.UUID
		if made, err = sess.CreateAlbum(ctx, newName); err != nil {
			return
		}
		album, albumName = uuid.UUID(made), newName
	} else {
		found := false
		for _, a := range albums {
			if uuid.UUID(a.ID) == id {
				album, albumName, found = id, a.Name, true
			}
		}
		if !found {
			err = ErrNoAlbum
			return
		}
	}
	keyID, secret, err = sess.CreateAPIKey(ctx, keyName("shared album "+albumName), immich.SharedAlbumPermissions)
	if err != nil {
		return
	}
	undo := func() { sess.DeleteAPIKey(context.WithoutCancel(ctx), keyID) }
	api, cerr := h.Connect(secret)
	if cerr != nil {
		undo()
		err = cerr
		return
	}
	if me, merr := api.Me(ctx); merr != nil || me.ID != owner.ID {
		undo()
		err = fmt.Errorf("the new key does not work for your account: %v", merr)
		return
	}
	if history == store.HistoryNew {
		list, lerr := api.AlbumAssets(ctx, album)
		if lerr != nil {
			undo()
			err = lerr
			return
		}
		for _, a := range list {
			if a.Checksum != "" {
				h.Store.LedgerPut(ctx, record, a.Checksum, a.Id.String(), store.OriginBaseline, false)
			}
		}
	}
	return
}

// ProposeAlbum proposes a shared album to a connection: this person's choices are made (and the app's key for their Immich) and
// the other app is asked. Nothing is sent to the other server until they accept.
func (h *Hub) ProposeAlbum(ctx context.Context, sess *immich.API, owner immich.User, p ShareParams) (store.SharedAlbum, error) {
	if !h.PeersOn() {
		return store.SharedAlbum{}, ErrSharingOff
	}
	name, history, deletion, err := checkShare(p, true)
	if err != nil {
		return store.SharedAlbum{}, err
	}
	pr, err := h.peerOf(ctx, owner.ID, p.PeerID)
	if err != nil {
		return store.SharedAlbum{}, err
	}
	if pr.State != store.PeerActive || pr.IPaused {
		return store.SharedAlbum{}, ErrPeerNotActive
	}
	if list, err := h.Store.SharedAlbumsOf(ctx, owner.ID); err != nil {
		return store.SharedAlbum{}, err
	} else if len(list) >= maxShared {
		return store.SharedAlbum{}, ErrTooManyShared
	}
	id := uuid.NewString()
	album, albumName, keyID, secret, err := h.ownAlbum(ctx, sess, owner, p.AlbumID, name, history, id)
	if err != nil {
		return store.SharedAlbum{}, err
	}
	undo := func() {
		sess.DeleteAPIKey(context.WithoutCancel(ctx), keyID)
		h.Store.DeleteSharedAlbum(context.WithoutCancel(ctx), id)
	}
	a, err := h.Store.AddSharedAlbum(ctx, store.SharedAlbum{ID: id, OwnerID: owner.ID, PeerID: pr.ID, Name: name, AlbumID: album, AlbumName: albumName, KeyID: keyID, Key: secret,
		State: store.ShareProposed, Proposer: true, History: history, Deletion: deletion})
	if err != nil {
		sess.DeleteAPIKey(context.WithoutCancel(ctx), keyID)
		return store.SharedAlbum{}, err
	}
	var reply struct{}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	code, why, err := peer.Call(cctx, h.peerClient(), pr.Address, pr.TheirID, pr.Secret, "POST", "/peer/v1/album/propose",
		ProposeMsg{ID: a.ID, Name: name, Deletion: deletion, History: history}, &reply)
	if err != nil || code != 200 {
		undo()
		if err != nil {
			return store.SharedAlbum{}, fmt.Errorf("%w: %s", ErrPeerUnreachable, trimNet(err))
		}
		return store.SharedAlbum{}, fmt.Errorf("%w: %s", ErrPeerRefused, why)
	}
	h.event(ctx, "", owner.ID, "share", i18n.Pack("{name} proposed to share the album {album} with {other}", "name", owner.Name, "album", name, "other", pr.Name))
	return a, nil
}

// ProposeMsg is the proposal one app sends to the other.
type ProposeMsg struct {
	ID       string `json:"id"` // the proposer's id for the shared album
	Name     string `json:"name"`
	Deletion string `json:"deletion"`
	History  string `json:"history"` // what the proposer shares
}

// AcceptMsg, DeclineMsg and LeaveMsg are the answers. To is the receiver's id for the shared album.
type AcceptMsg struct {
	To      string `json:"to"`
	ID      string `json:"id"` // the acceptor's id for it
	History string `json:"history"`
}

type AnswerMsg struct {
	To string `json:"to"`
}

// PeerPropose is what a signed proposal does: it makes a pending shared album on the person's page, for them to answer.
func (h *Hub) PeerPropose(ctx context.Context, p store.Peer, m ProposeMsg) error {
	name := strings.TrimSpace(m.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 60 || strings.ContainsAny(name, "\r\n\x00") || m.ID == "" || len(m.ID) > 64 {
		return ErrBadShareName
	}
	if m.History != store.HistoryAll && m.History != store.HistoryNew {
		return ErrBadHistory
	}
	if m.Deletion != store.DeleteNothing && m.Deletion != store.DeleteFollow {
		return ErrBadDeletion
	}
	if p.IPaused {
		return ErrPeerNotActive
	}
	if list, err := h.Store.SharedAlbumsWith(ctx, p.ID); err == nil {
		for _, a := range list {
			if a.TheirID == m.ID {
				return nil // the same proposal again
			}
		}
	}
	if _, err := h.Store.AddSharedAlbum(ctx, store.SharedAlbum{OwnerID: p.OwnerID, PeerID: p.ID, TheirID: m.ID, Name: name, State: store.ShareInvited,
		TheirHistory: m.History, Deletion: m.Deletion, History: store.HistoryNew}); err != nil {
		return err
	}
	h.event(ctx, "", p.OwnerID, "share", i18n.Pack("{other} proposes to share the album {album} with you", "other", p.Name, "album", name))
	return nil
}

// AcceptAlbum answers a proposal with yes: the person chooses their album (or a new one) and what they share, the app gets its
// key, and the other app is told.
func (h *Hub) AcceptAlbum(ctx context.Context, sess *immich.API, owner immich.User, id string, p ShareParams) error {
	if !h.PeersOn() {
		return ErrSharingOff
	}
	a, err := h.sharedOf(ctx, owner.ID, id)
	if err != nil {
		return err
	}
	if a.State != store.ShareInvited {
		return ErrNotYetAnswered
	}
	_, history, _, err := checkShare(ShareParams{History: p.History, Deletion: a.Deletion}, false)
	if err != nil {
		return err
	}
	pr, err := h.peerOf(ctx, owner.ID, a.PeerID)
	if err != nil {
		return err
	}
	if pr.State != store.PeerActive || pr.IPaused {
		return ErrPeerNotActive
	}
	album, albumName, keyID, secret, err := h.ownAlbum(ctx, sess, owner, p.AlbumID, a.Name, history, a.ID)
	if err != nil {
		return err
	}
	undo := func() {
		sess.DeleteAPIKey(context.WithoutCancel(ctx), keyID)
		h.Store.ClearLedger(context.WithoutCancel(ctx), a.ID)
	}
	var reply struct{}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	code, why, err := peer.Call(cctx, h.peerClient(), pr.Address, pr.TheirID, pr.Secret, "POST", "/peer/v1/album/accept", AcceptMsg{To: a.TheirID, ID: a.ID, History: history}, &reply)
	if err != nil || code != 200 {
		undo()
		if err != nil {
			return fmt.Errorf("%w: %s", ErrPeerUnreachable, trimNet(err))
		}
		return fmt.Errorf("%w: %s", ErrPeerRefused, why)
	}
	if err := h.Store.ActivateSharedAlbum(ctx, a.ID, a.TheirID, album, albumName, keyID, secret, history, a.TheirHistory); err != nil {
		undo()
		return err
	}
	h.event(ctx, "", owner.ID, "share", i18n.Pack("{name} accepted sharing the album {album} with {other}", "name", owner.Name, "album", a.Name, "other", pr.Name))
	return nil
}

// DeclineAlbum answers a proposal with no.
func (h *Hub) DeclineAlbum(ctx context.Context, owner immich.User, id string) error {
	a, err := h.sharedOf(ctx, owner.ID, id)
	if err != nil {
		return err
	}
	if a.State != store.ShareInvited {
		return ErrNotYetAnswered
	}
	if pr, err := h.peerOf(ctx, owner.ID, a.PeerID); err == nil && pr.State == store.PeerActive {
		var reply struct{}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		peer.Call(cctx, h.peerClient(), pr.Address, pr.TheirID, pr.Secret, "POST", "/peer/v1/album/decline", AnswerMsg{To: a.TheirID}, &reply)
		cancel()
	}
	return h.Store.DeleteSharedAlbum(ctx, a.ID)
}

// LeaveSharedAlbum ends the sharing of an album from this side: the app's key is deleted from the person's Immich, the other app
// is told, and what this app knew of it is forgotten. The photos in both libraries stay.
func (h *Hub) LeaveSharedAlbum(ctx context.Context, sess *immich.API, owner immich.User, id string) error {
	a, err := h.sharedOf(ctx, owner.ID, id)
	if err != nil {
		return err
	}
	if a.KeyID != uuid.Nil {
		if err := sess.DeleteAPIKey(ctx, a.KeyID); err != nil {
			h.Log.Warn("ending a shared album: could not delete its key (it may already be gone)", "err", err)
		}
	}
	if pr, err := h.peerOf(ctx, owner.ID, a.PeerID); err == nil && pr.State == store.PeerActive && a.TheirID != "" && a.State != store.ShareLeft {
		var reply struct{}
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		peer.Call(cctx, h.peerClient(), pr.Address, pr.TheirID, pr.Secret, "POST", "/peer/v1/album/leave", AnswerMsg{To: a.TheirID}, &reply)
		cancel()
	}
	if err := h.Store.DeleteSharedAlbum(ctx, a.ID); err != nil {
		return err
	}
	h.event(ctx, "", owner.ID, "share", i18n.Pack("{name} ended sharing the album {album}", "name", owner.Name, "album", a.Name))
	return nil
}

func (h *Hub) sharedOf(ctx context.Context, owner uuid.UUID, id string) (store.SharedAlbum, error) {
	a, err := h.Store.SharedAlbumByID(ctx, id)
	if err != nil || a.OwnerID != owner {
		return store.SharedAlbum{}, ErrNoSharedAlbum
	}
	return a, nil
}

// SharedAlbums lists a person's shared albums.
func (h *Hub) SharedAlbums(ctx context.Context, owner uuid.UUID) ([]store.SharedAlbum, error) {
	if h.Store == nil {
		return nil, nil
	}
	return h.Store.SharedAlbumsOf(ctx, owner)
}

// PeerAccepted is what a signed "accept" does on the side that proposed.
func (h *Hub) PeerAccepted(ctx context.Context, p store.Peer, m AcceptMsg) error {
	a, err := h.Store.SharedAlbumByID(ctx, m.To)
	if err != nil || a.PeerID != p.ID || a.State != store.ShareProposed {
		return ErrNoSharedAlbum
	}
	if m.History != store.HistoryAll && m.History != store.HistoryNew {
		return ErrBadHistory
	}
	if err := h.Store.ActivateProposed(ctx, a.ID, m.ID, m.History); err != nil {
		return err
	}
	h.event(ctx, "", p.OwnerID, "share", i18n.Pack("{other} accepted sharing the album {album}", "other", p.Name, "album", a.Name))
	return nil
}

// PeerDeclined is what a signed "decline" does: the proposal is over. The key made for it is deleted when the person removes it.
func (h *Hub) PeerDeclined(ctx context.Context, p store.Peer, m AnswerMsg) error {
	a, err := h.Store.SharedAlbumByID(ctx, m.To)
	if err != nil || a.PeerID != p.ID || a.State != store.ShareProposed {
		return ErrNoSharedAlbum
	}
	if err := h.Store.SetSharedState(ctx, a.ID, store.ShareDeclined); err != nil {
		return err
	}
	h.event(ctx, "", p.OwnerID, "share", i18n.Pack("{other} declined sharing the album {album}", "other", p.Name, "album", a.Name))
	return nil
}

// PeerLeftAlbum is what a signed "leave" does: the other side ended the sharing of the album. It stays on the page, marked, until
// the person removes it (which deletes the key).
func (h *Hub) PeerLeftAlbum(ctx context.Context, p store.Peer, m AnswerMsg) error {
	a, err := h.Store.SharedAlbumByID(ctx, m.To)
	if err != nil || a.PeerID != p.ID {
		return ErrNoSharedAlbum
	}
	if err := h.Store.SetSharedState(ctx, a.ID, store.ShareLeft); err != nil {
		return err
	}
	h.event(ctx, "", p.OwnerID, "share", i18n.Pack("{other} ended sharing the album {album}", "other", p.Name, "album", a.Name))
	return nil
}
