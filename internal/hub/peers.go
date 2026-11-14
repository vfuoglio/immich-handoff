package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/peer"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Peers: the other Handoff apps this one shares albums with (docs/shared-albums-design.md). A peer is made by an invitation.
// The person who invites makes a link on their app; the other person pastes it into their own app and accepts, which gives the
// two apps a secret (made by the joiner, sent over HTTPS with the one-time token), and the inviter's app proves the joiner's app
// can be reached by calling it with a signed request before it agrees. From then on every request between them is signed.

var (
	ErrSharingOff       = errors.New("sharing albums with other servers is not switched on here")
	ErrBadPeerLink      = errors.New("that is not an invitation link from another Handoff")
	ErrPeerInviteGone   = errors.New("that invitation has expired or was already used")
	ErrNoPeer           = errors.New("that is not one of your connections")
	ErrPeerUnreachable  = errors.New("the other app could not be reached")
	ErrPeerSelf         = errors.New("that invitation is from this very app")
	ErrTooManyPeers     = errors.New("you already have 20 connections; remove one first")
	ErrBadPeerName      = errors.New("your name is needed to invite someone")
	ErrPeerVersionTaken = errors.New("the other app speaks a version of the sharing protocol this one does not")
	ErrPeerRefused      = errors.New("the other app did not accept")
)

const (
	peerInviteLife = 7 * 24 * time.Hour
	maxPeers       = 20
)

// PeersOn reports whether this app takes part in sharing albums at all: the administrator switched it on, people can join from
// the page (there is a data file) and the app knows its own public address, which it gives to the other app.
func (h *Hub) PeersOn() bool { return h.Share && h.SelfService() && strings.TrimSpace(h.AppURL) != "" }

func (h *Hub) peerClient() *http.Client { return h.Bridge.Policy.HTTPClient() }

func (h *Hub) selfBase() string { return strings.TrimSuffix(strings.TrimSpace(h.AppURL), "/") }

// peerBase checks the address of another app and keeps its path (an app may live under a prefix): the scheme, host and port are
// judged by the same policy as a bridge's.
func (h *Hub) peerBase(raw string) (string, error) {
	base, err := h.Bridge.Policy.Address(raw)
	if err != nil {
		return "", err
	}
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	if u, err := url.Parse(raw); err == nil {
		if p := strings.TrimSuffix(u.EscapedPath(), "/"); p != "" && !strings.ContainsAny(p, "?#") {
			base += p
		}
	}
	return base, nil
}

var peerLink = regexp.MustCompile(`^(.*)/peer/invite/([0-9a-f]{64})/?$`)

// parsePeerLink reads an invitation link: the other app's address and the token.
func (h *Hub) parsePeerLink(link string) (base, token string, err error) {
	u, perr := url.Parse(strings.TrimSpace(link))
	if perr != nil || u.Host == "" {
		return "", "", ErrBadPeerLink
	}
	m := peerLink.FindStringSubmatch(u.Scheme + "://" + u.Host + u.EscapedPath())
	if m == nil {
		return "", "", ErrBadPeerLink
	}
	base, err = h.peerBase(m[1])
	if err != nil {
		return "", "", err
	}
	return base, m[2], nil
}

// NewPeerInvite makes an invitation: a link the person gives to the other person, good for a week and for one use.
func (h *Hub) NewPeerInvite(ctx context.Context, owner immich.User) (string, time.Time, error) {
	if !h.PeersOn() {
		return "", time.Time{}, ErrSharingOff
	}
	if strings.TrimSpace(owner.Name) == "" {
		return "", time.Time{}, ErrBadPeerName
	}
	if list, err := h.Store.PeersOf(ctx, owner.ID); err != nil {
		return "", time.Time{}, err
	} else if len(list) >= maxPeers {
		return "", time.Time{}, ErrTooManyPeers
	}
	token := newToken() + newToken()
	until := h.now().Add(peerInviteLife)
	if err := h.Store.AddPeerInvite(ctx, token, owner.ID, owner.Name, until); err != nil {
		return "", time.Time{}, err
	}
	return h.selfBase() + "/peer/invite/" + token, until, nil
}

// PeerInviteInfo is what the other app (and the invitation's page) may learn about an invitation: who made it.
type PeerInviteInfo struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// InviteInfo answers for an invitation's token: the name of whoever made it, if it can still be used.
func (h *Hub) InviteInfo(ctx context.Context, token string) (PeerInviteInfo, error) {
	if !h.PeersOn() {
		return PeerInviteInfo{}, ErrSharingOff
	}
	in, err := h.Store.PeerInviteByToken(ctx, token)
	if err != nil || in.UsedBy != "" || !h.now().Before(in.ExpiresAt) {
		return PeerInviteInfo{}, ErrPeerInviteGone
	}
	return PeerInviteInfo{Name: in.OwnerName, Version: peer.Version}, nil
}

// PreviewPeerInvite asks the app that made an invitation who it is from, so the person can decide before accepting.
func (h *Hub) PreviewPeerInvite(ctx context.Context, link string) (PeerInviteInfo, string, error) {
	if !h.PeersOn() {
		return PeerInviteInfo{}, "", ErrSharingOff
	}
	base, token, err := h.parsePeerLink(link)
	if err != nil {
		return PeerInviteInfo{}, "", err
	}
	if base == h.selfBase() {
		return PeerInviteInfo{}, "", ErrPeerSelf
	}
	var info PeerInviteInfo
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	code, _, err := peer.Plain(cctx, h.peerClient(), "GET", base+"/peer/v1/invite/"+token, nil, &info)
	switch {
	case err != nil:
		return info, base, fmt.Errorf("%w: %v", ErrPeerUnreachable, trimNet(err))
	case code == http.StatusNotFound || code == http.StatusGone:
		return info, base, ErrPeerInviteGone
	case code != 200:
		return info, base, fmt.Errorf("%w (%d)", ErrPeerUnreachable, code)
	}
	if info.Version != peer.Version {
		return info, base, ErrPeerVersionTaken
	}
	return info, base, nil
}

// JoinRequest is what the joiner's app sends to the inviter's.
type JoinRequest struct {
	Address string `json:"address"` // where the joiner's app can be reached
	Name    string `json:"name"`
	Secret  string `json:"secret"`  // made by the joiner; both apps sign with it
	PeerID  string `json:"peer_id"` // the id the joiner's app uses for the inviter
	Version int    `json:"version"`
}

// JoinReply is the inviter's answer.
type JoinReply struct {
	PeerID  string `json:"peer_id"` // the id the inviter's app uses for the joiner
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// AcceptPeerInvite makes this app the joiner of an invitation: it makes the secret, asks the inviter's app to join, and keeps
// the peer. The inviter's app calls this app back, signed, before it agrees, so nothing is kept if this app cannot be reached.
func (h *Hub) AcceptPeerInvite(ctx context.Context, joiner immich.User, link string) (store.Peer, error) {
	info, base, err := h.PreviewPeerInvite(ctx, link)
	if err != nil {
		return store.Peer{}, err
	}
	_, token, _ := h.parsePeerLink(link)
	if strings.TrimSpace(joiner.Name) == "" {
		return store.Peer{}, ErrBadPeerName
	}
	if list, err := h.Store.PeersOf(ctx, joiner.ID); err != nil {
		return store.Peer{}, err
	} else if len(list) >= maxPeers {
		return store.Peer{}, ErrTooManyPeers
	}
	secret, err := peer.NewSecret()
	if err != nil {
		return store.Peer{}, err
	}
	p, err := h.Store.AddPeer(ctx, store.Peer{OwnerID: joiner.ID, OwnerName: joiner.Name, Name: info.Name, Address: base, Secret: secret, Role: store.PeerJoiner, Version: info.Version})
	if err != nil {
		return store.Peer{}, err
	}
	undo := func() { h.Store.DeletePeer(context.WithoutCancel(ctx), p.ID) }
	var reply JoinReply
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	code, why, err := peer.Plain(cctx, h.peerClient(), "POST", base+"/peer/v1/join/"+token,
		JoinRequest{Address: h.selfBase(), Name: joiner.Name, Secret: secret, PeerID: p.ID, Version: peer.Version}, &reply)
	if err != nil {
		undo()
		return store.Peer{}, fmt.Errorf("%w: %v", ErrPeerUnreachable, trimNet(err))
	}
	if code != 200 {
		undo()
		msg := why
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", code)
		}
		return store.Peer{}, fmt.Errorf("%w: %s", ErrPeerRefused, msg)
	}
	if err := h.Store.SetPeerTheirID(ctx, p.ID, reply.PeerID, info.Name, info.Version); err != nil {
		undo()
		return store.Peer{}, err
	}
	h.Store.MarkPeerSeen(ctx, p.ID, h.now(), "")
	h.event(ctx, "", joiner.ID, "peer", i18n.Pack("{name} and you can now share albums between your servers", "name", info.Name))
	p, _ = h.Store.PeerByID(ctx, p.ID)
	return p, nil
}

// PeerJoin is the inviter's side: it takes the joiner's request, checks the token and the address, keeps the peer and calls the
// joiner's app back, signed, to prove that it can be reached and holds the secret.
func (h *Hub) PeerJoin(ctx context.Context, token string, req JoinRequest) (JoinReply, error) {
	if !h.PeersOn() {
		return JoinReply{}, ErrSharingOff
	}
	in, err := h.Store.PeerInviteByToken(ctx, token)
	if err != nil || in.UsedBy != "" || !h.now().Before(in.ExpiresAt) {
		return JoinReply{}, ErrPeerInviteGone
	}
	if req.Version != peer.Version {
		return JoinReply{}, ErrPeerVersionTaken
	}
	name := CleanGuestName(req.Name)
	if name == "" || len(req.Secret) < 40 || len(req.Secret) > 100 || req.PeerID == "" || utf8.RuneCountInString(req.PeerID) > 64 {
		return JoinReply{}, ErrBadPeerLink
	}
	base, err := h.peerBase(req.Address)
	if err != nil {
		return JoinReply{}, err
	}
	if base == h.selfBase() {
		return JoinReply{}, ErrPeerSelf
	}
	if list, err := h.Store.PeersOf(ctx, in.OwnerID); err == nil && len(list) >= maxPeers {
		return JoinReply{}, ErrTooManyPeers
	}
	p, err := h.Store.AddPeer(ctx, store.Peer{OwnerID: in.OwnerID, OwnerName: in.OwnerName, Name: name, Address: base, Secret: req.Secret, TheirID: req.PeerID, Role: store.PeerInviter, Version: req.Version})
	if err != nil {
		return JoinReply{}, err
	}
	if ok, err := h.Store.UsePeerInvite(ctx, token, p.ID); err != nil || !ok {
		h.Store.DeletePeer(context.WithoutCancel(ctx), p.ID)
		return JoinReply{}, ErrPeerInviteGone
	}
	if err := h.helloPeer(ctx, p); err != nil {
		h.Store.DeletePeer(context.WithoutCancel(ctx), p.ID)
		return JoinReply{}, fmt.Errorf("%w: %v", ErrPeerUnreachable, err)
	}
	h.event(ctx, "", in.OwnerID, "peer", i18n.Pack("{name} and you can now share albums between your servers", "name", name))
	return JoinReply{PeerID: p.ID, Name: in.OwnerName, Version: peer.Version}, nil
}

// HelloRequest and HelloReply are the greeting two apps exchange (also to see that the other is there).
type HelloRequest struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	Paused  bool   `json:"paused"`
}

type HelloReply struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	Paused  bool   `json:"paused"`
}

// helloPeer greets a peer with a signed request and records that it answered, or why it did not.
func (h *Hub) helloPeer(ctx context.Context, p store.Peer) error {
	var reply HelloReply
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	code, why, err := peer.Call(cctx, h.peerClient(), p.Address, p.TheirID, p.Secret, "POST", "/peer/v1/hello", HelloRequest{Name: p.OwnerName, Version: peer.Version, Paused: p.IPaused}, &reply)
	problem := ""
	switch {
	case err != nil:
		problem = trimNet(err)
	case code != 200:
		problem = why
		if problem == "" {
			problem = fmt.Sprintf("HTTP %d", code)
		}
	case reply.Version != peer.Version:
		problem = ErrPeerVersionTaken.Error()
	}
	if problem != "" {
		h.Store.MarkPeerSeen(context.WithoutCancel(ctx), p.ID, time.Time{}, problem)
		if p.Problem == "" && !p.SeenAt.IsZero() { // told once, when it stops working
			h.event(ctx, "", p.OwnerID, "sharing_problem", i18n.Pack("The connection to {name} is not working: {reason}", "name", p.Name, "reason", problem))
		}
		return errors.New(problem)
	}
	if p.Problem != "" {
		h.event(ctx, "", p.OwnerID, "peer", i18n.Pack("The connection to {name} works again", "name", p.Name))
	}
	h.Store.MarkPeerSeen(context.WithoutCancel(ctx), p.ID, h.now(), "")
	h.Store.SetPeerTheyPaused(context.WithoutCancel(ctx), p.ID, reply.Paused)
	return nil
}

// ErrPeerAuth is what an unsigned or wrongly signed request to a peer endpoint gets: always the same, so it says nothing.
var ErrPeerAuth = errors.New("not allowed")

// PeerAuth finds the peer a signed request is from and checks the signature, the time and that the request is new.
func (h *Hub) PeerAuth(ctx context.Context, peerID string, header http.Header, method, path string, body []byte) (store.Peer, error) {
	if !h.PeersOn() || peerID == "" || len(peerID) > 64 {
		return store.Peer{}, ErrPeerAuth
	}
	p, err := h.Store.PeerByID(ctx, peerID)
	if err != nil || p.State != store.PeerActive {
		return store.Peer{}, ErrPeerAuth
	}
	if err := h.peerVerify.Check(p.Secret, header, method, peer.SignedPath(path), body, h.now()); err != nil {
		return store.Peer{}, ErrPeerAuth
	}
	return p, nil
}

// PeerHello answers a greeting: it notes that the other side paused or resumed.
func (h *Hub) PeerHello(ctx context.Context, p store.Peer, req HelloRequest) HelloReply {
	h.Store.MarkPeerSeen(ctx, p.ID, h.now(), "")
	h.Store.SetPeerTheyPaused(ctx, p.ID, req.Paused)
	if n := CleanGuestName(req.Name); n != "" && n != p.Name {
		h.Store.SetPeerTheirID(ctx, p.ID, p.TheirID, n, p.Version)
	}
	return HelloReply{Name: p.OwnerName, Version: peer.Version, Paused: p.IPaused}
}

// PeerLeft is what a signed "leave" does: the other side ended the connection. The record stays, marked, until the person removes
// it, so they can see what happened.
func (h *Hub) PeerLeft(ctx context.Context, p store.Peer) {
	h.Store.SetPeerState(ctx, p.ID, store.PeerLeft)
	h.Store.SetSharedStateWith(ctx, p.ID, store.ShareLeft)
	h.event(ctx, "", p.OwnerID, "peer", i18n.Pack("{name} ended the connection between your servers", "name", p.Name))
}

// Peers lists a person's connections.
func (h *Hub) Peers(ctx context.Context, owner uuid.UUID) ([]store.Peer, error) {
	if h.Store == nil {
		return nil, nil
	}
	return h.Store.PeersOf(ctx, owner)
}

func (h *Hub) peerOf(ctx context.Context, owner uuid.UUID, id string) (store.Peer, error) {
	p, err := h.Store.PeerByID(ctx, id)
	if err != nil || p.OwnerID != owner {
		return store.Peer{}, ErrNoPeer
	}
	return p, nil
}

// SetPeerPaused pauses or resumes sharing with a peer, and tells the other app so it can show it.
func (h *Hub) SetPeerPaused(ctx context.Context, owner immich.User, id string, paused bool) error {
	p, err := h.peerOf(ctx, owner.ID, id)
	if err != nil {
		return err
	}
	if err := h.Store.SetPeerPaused(ctx, p.ID, paused); err != nil {
		return err
	}
	p.IPaused = paused
	if p.State == store.PeerActive {
		h.helloPeer(ctx, p) // the greeting carries the state; if the other app is away it learns at the next one
	}
	return nil
}

// RemovePeer ends a connection: the other app is told (if it can be reached) and the record, with its secret, is forgotten.
func (h *Hub) RemovePeer(ctx context.Context, sess *immich.API, owner immich.User, id string) error {
	p, err := h.peerOf(ctx, owner.ID, id)
	if err != nil {
		return err
	}
	if p.State == store.PeerActive {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		peer.Call(cctx, h.peerClient(), p.Address, p.TheirID, p.Secret, "POST", "/peer/v1/leave", struct{}{}, nil) //nolint:errcheck
		cancel()
	}
	if shared, err := h.Store.SharedAlbumsWith(ctx, p.ID); err == nil {
		for _, a := range shared {
			if a.KeyID != uuid.Nil && sess != nil {
				if err := sess.DeleteAPIKey(ctx, a.KeyID); err != nil {
					h.Log.Warn("ending a connection: could not delete a shared album's key (it may already be gone)", "err", err)
				}
			}
		}
		h.Store.DeleteSharedWith(ctx, p.ID)
	}
	if err := h.Store.DeletePeer(ctx, p.ID); err != nil {
		return err
	}
	h.event(ctx, "", owner.ID, "peer", i18n.Pack("{name} removed the connection to {other}", "name", owner.Name, "other", p.Name))
	return nil
}

// PeerMaintenance greets every active peer to see that it is there, and forgets old invitations.
func (h *Hub) PeerMaintenance(ctx context.Context) {
	if !h.PeersOn() {
		return
	}
	h.Store.PrunePeerInvites(ctx, h.now())
	list, err := h.Store.AllPeers(ctx)
	if err != nil {
		return
	}
	for _, p := range list {
		h.helloPeer(ctx, p)
	}
}

func trimNet(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && i < len(s)-2 {
		s = s[i+2:]
	}
	return s
}
