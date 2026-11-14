package hub

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/notify"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Choosing in a client room. Whose selection a pick belongs to depends on the room: one for everyone who has the link (shared),
// one for each visitor (visitor), or one for the first client to give a name (named), whom the others can only watch.

var (
	ErrRoomLimit   = errors.New("you have already chosen the most there can be; take one away to choose another")
	ErrRoomLocked  = errors.New("your choice has been sent to the photographer, so it cannot change now")
	ErrRoomNoPicks = errors.New("choose at least one photo first")
	ErrRoomClaimed = errors.New("someone else is choosing for this room; you can look at the photos")
	ErrRoomName    = errors.New("tell us your name first (up to 40 letters)")
	ErrBadPickMode = errors.New("choose who picks: everyone together, each person, or one named client")
	ErrBadPickLim  = errors.New("the most photos to choose is a number from 1 to 5000, or none")
)

const maxPickLimit = 5000

// RoomViewer is who is looking at a room: a mark made from a secret their browser keeps (never their address), and the name
// they gave, if any.
type RoomViewer struct {
	Who  string
	Name string
}

// CleanViewerName cleans a name a client gave: no control characters, spaces collapsed, at most 40 letters.
func CleanViewerName(s string) string { return CleanGuestName(s) }

// RoomSelection is where a viewer stands in a room: what they have picked and what they may do.
type RoomSelection struct {
	Picked    map[string]bool
	Count     int
	Limit     int
	Done      bool
	CanPick   bool   // they may choose (a selection that is not sent, and in a named room, theirs)
	NeedsName bool   // a named room nobody has claimed: giving a name starts it
	Chooser   string // in a named room somebody else has claimed: their name
	Mode      string
}

// scope is the selection a viewer's picks go in: the shared one and the named client's are the empty scope.
func scope(r store.Room, v RoomViewer) string {
	if r.PickMode == store.RoomVisitor {
		return v.Who
	}
	return ""
}

func (h *Hub) roomSelection(ctx context.Context, r store.Room, v RoomViewer) (RoomSelection, error) {
	sc := scope(r, v)
	sel := RoomSelection{Picked: map[string]bool{}, Limit: r.PickLimit, Mode: r.PickMode}
	picks, err := h.Store.Picks(ctx, r.ID, 1, &sc)
	if err != nil {
		return sel, err
	}
	for _, p := range picks {
		sel.Picked[p.Photo] = true
	}
	sel.Count = len(sel.Picked)
	if sel.Done, err = h.Store.SelectionDone(ctx, r.ID, 1, sc); err != nil {
		return sel, err
	}
	sel.CanPick = !sel.Done
	if r.PickMode == store.RoomNamed {
		switch {
		case r.ClaimWho == "":
			sel.NeedsName, sel.CanPick = true, false
		case r.ClaimWho != v.Who:
			sel.Chooser, sel.CanPick = r.ClaimName, false
		}
	}
	return sel, nil
}

// RoomSelectionOf says where a viewer stands in a room.
func (h *Hub) RoomSelectionOf(ctx context.Context, r store.Room, v RoomViewer) (RoomSelection, error) {
	return h.roomSelection(ctx, r, v)
}

// RoomClaim lets the first client to give a name be the one who chooses in a named room.
func (h *Hub) RoomClaim(ctx context.Context, r store.Room, v RoomViewer) error {
	name := CleanViewerName(v.Name)
	if name == "" {
		return ErrRoomName
	}
	if r.PickMode != store.RoomNamed {
		return nil
	}
	ok, err := h.Store.ClaimRoom(ctx, r.ID, v.Who, name)
	if err != nil {
		return err
	}
	if !ok {
		fresh, err := h.Store.RoomByIDAny(ctx, r.ID)
		if err != nil || fresh.ClaimWho != v.Who {
			return ErrRoomClaimed
		}
	}
	return nil
}

// RoomPick adds a photo to the viewer's selection, or takes it out again.
func (h *Hub) RoomPick(ctx context.Context, r store.Room, v RoomViewer, photo string) (RoomSelection, error) {
	s, err := h.roomSnapshot(ctx, r)
	if err != nil {
		return RoomSelection{}, err
	}
	if _, ok := s.by[photo]; !ok {
		return RoomSelection{}, ErrDropGone
	}
	sel, err := h.roomSelection(ctx, r, v)
	if err != nil {
		return sel, err
	}
	switch {
	case sel.Done:
		return sel, ErrRoomLocked
	case sel.NeedsName:
		return sel, ErrRoomName
	case sel.Chooser != "":
		return sel, ErrRoomClaimed
	}
	name := CleanViewerName(v.Name)
	if r.PickMode == store.RoomNamed {
		name = r.ClaimName
	}
	_, _, err = h.Store.TogglePick(ctx, r.ID, 1, scope(r, v), photo, name, r.PickLimit)
	switch {
	case errors.Is(err, store.ErrPickLimit):
		err = ErrRoomLimit
	case errors.Is(err, store.ErrPickLocked):
		err = ErrRoomLocked
	}
	if err != nil {
		return sel, err
	}
	return h.roomSelection(ctx, r, v)
}

// RoomDone sends the viewer's selection to the photographer, and tells the photographer.
func (h *Hub) RoomDone(ctx context.Context, r store.Room, v RoomViewer) (RoomSelection, error) {
	sel, err := h.roomSelection(ctx, r, v)
	if err != nil {
		return sel, err
	}
	switch {
	case sel.Done:
		return sel, nil
	case sel.NeedsName:
		return sel, ErrRoomName
	case sel.Chooser != "":
		return sel, ErrRoomClaimed
	case sel.Count == 0:
		return sel, ErrRoomNoPicks
	}
	name := strings.TrimSpace(CleanViewerName(v.Name))
	if r.PickMode == store.RoomNamed {
		name = r.ClaimName
	}
	first, err := h.Store.FinishSelection(ctx, r.ID, 1, scope(r, v), name)
	if err != nil {
		return sel, err
	}
	if first {
		who := name
		if who == "" {
			who = "Your client"
		}
		sc := scope(r, v)
		notes, _ := h.Store.Notes(ctx, r.ID, 1, &sc)
		h.event(ctx, "", r.OwnerID, "choice", notesLine(whoText(who), r.Name, sel.Count, len(notes)))
		h.tell(notify.DropBox, fmt.Sprintf("%s finished choosing in the client room %q of %s: %d photos. Open the room on the Services tab to look at the choice.", who, r.Name, r.OwnerName, sel.Count))
		h.Log.Info("a client finished choosing", "room", r.Name, "photos", sel.Count)
	}
	h.releaseIfAuto(ctx, r)
	sel.Done, sel.CanPick = true, false
	return sel, nil
}

// checkRoomPicks checks how a photographer wants clients to choose.
func checkRoomPicks(p RoomParams) (mode string, limit int, err error) {
	mode = p.PickMode
	if mode == "" {
		mode = store.RoomShared
	}
	if mode != store.RoomShared && mode != store.RoomVisitor && mode != store.RoomNamed {
		return "", 0, ErrBadPickMode
	}
	if p.PickLimit < 0 || p.PickLimit > maxPickLimit {
		return "", 0, ErrBadPickLim
	}
	return mode, p.PickLimit, nil
}

// whoText is how a client is named in the photographer's log: the name they gave, or "A client".
func whoText(name string) string {
	if name == "" || name == "Your client" {
		return "A client"
	}
	return name
}

// RoomSelections lists the selections of a room for its owner: whose, how many photos, and whether sent.
func (h *Hub) RoomSelections(ctx context.Context, r store.Room) ([]store.SelectionSummary, error) {
	return h.Store.SelectionsOf(ctx, r.ID, 1)
}

// ReopenRoomSelection lets the owner give a sent selection back to the client to change. who is the selection's mark.
func (h *Hub) ReopenRoomSelection(ctx context.Context, owner immich.User, roomID, who string) error {
	r, err := h.Store.RoomByID(ctx, owner.ID, roomID)
	if err != nil {
		return ErrNoRoom
	}
	if err := h.Store.ReopenSelection(ctx, r.ID, 1, who); err != nil {
		return err
	}
	h.event(ctx, "", owner.ID, "choice", i18n.Pack("{name} gave a choice in {room} back to the client to change", "name", owner.Name, "room", r.Name))
	return nil
}
