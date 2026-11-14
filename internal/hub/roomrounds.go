package hub

import (
	"context"
	"errors"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Rounds in a client room. A room made for several rounds starts in the first; when clients have sent their choices the
// photographer starts the next one with a limit of its own, and in it each client sees only the photos they chose before. The
// picks, notes and sent choices of every round are kept under its number, and what is delivered is the last round's.

var (
	ErrSingleRound   = errors.New("this room has only one round; make a room for several rounds to go on")
	ErrRoundNotReady = errors.New("no client has sent a choice in this round yet")
	ErrRoundReleased = errors.New("the originals are released; take the release back before starting another round")
	ErrTooManyRounds = errors.New("a room has at most 5 rounds")
)

const maxRounds = 5

// roomVisible is the set of photos a viewer sees in the room's current round: everything in the first round, in a later one only
// what their own selection chose in the round before. The bool is false when nothing is held back.
func (h *Hub) roomVisible(ctx context.Context, r store.Room, v RoomViewer) (map[string]bool, bool, error) {
	if r.CurrentRound() <= 1 {
		return nil, false, nil
	}
	set, err := h.Store.PreviousPicks(ctx, r.ID, r.CurrentRound()-1, scope(r, v))
	return set, true, err
}

// RoomVisibleTo lists the photos of the room a viewer sees now, in the order of the shoot.
func (h *Hub) RoomVisibleTo(ctx context.Context, r store.Room, v RoomViewer) ([]RoomPhoto, error) {
	photos, err := h.RoomPhotos(ctx, r)
	if err != nil {
		return nil, err
	}
	set, restricted, err := h.roomVisible(ctx, r, v)
	if err != nil || !restricted {
		return photos, err
	}
	var out []RoomPhoto
	for _, p := range photos {
		if set[p.ID] {
			out = append(out, p)
		}
	}
	return out, nil
}

// StartRoomRound moves a room on to its next round, with the most photos a client may choose in it (0: no limit).
func (h *Hub) StartRoomRound(ctx context.Context, owner immich.User, roomID string, limit int) error {
	r, err := h.Store.RoomByID(ctx, owner.ID, roomID)
	if err != nil {
		return ErrNoRoom
	}
	switch {
	case r.State == store.RoomEnded:
		return ErrRoomNotActive
	case !r.MultiRound:
		return ErrSingleRound
	case r.CurrentRound() >= maxRounds:
		return ErrTooManyRounds
	case h.RoomReleased(r) || !r.ReleasedAt.IsZero():
		return ErrRoundReleased
	}
	if limit < 0 || limit > maxPickLimit {
		return ErrBadPickLim
	}
	sels, err := h.Store.SelectionsOf(ctx, r.ID, r.CurrentRound())
	if err != nil {
		return err
	}
	sent := false
	for _, s := range sels {
		sent = sent || s.Done
	}
	if !sent {
		return ErrRoundNotReady
	}
	ok, err := h.Store.StartRound(ctx, r.ID, r.CurrentRound(), limit)
	if err != nil {
		return err
	}
	if !ok {
		return ErrRoundNotReady // somebody else started it a moment ago
	}
	h.event(ctx, "", owner.ID, "choice", i18n.Pack("{name} started round {n} of {room}", "name", owner.Name, "n", r.CurrentRound()+1, "room", r.Name))
	return nil
}

// RoundSummary is a past round of a room: its selections as they were.
type RoundSummary struct {
	Round      int
	Selections []store.SelectionSummary
}

// RoomRounds lists the rounds before the current one, oldest first.
func (h *Hub) RoomRounds(ctx context.Context, r store.Room) ([]RoundSummary, error) {
	var out []RoundSummary
	for n := 1; n < r.CurrentRound(); n++ {
		sels, err := h.Store.SelectionsOf(ctx, r.ID, n)
		if err != nil {
			return nil, err
		}
		out = append(out, RoundSummary{Round: n, Selections: sels})
	}
	return out, nil
}
