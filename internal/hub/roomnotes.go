package hub

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/store"
)

// Notes on the photos of a client room: a short note from the client to the photographer, and, where the room allows it, the
// photographer's answer. A note belongs to a selection (in a shared room everybody who has the link shares it), is plain text and
// can be written until the choice is sent.

var (
	ErrRoomNotesOff = errors.New("this room does not take notes")
	ErrBadNotesMode = errors.New("choose what clients may write: nothing, notes, or notes the photographer can answer")
	ErrRoomNoteLong = errors.New("a note is at most 280 characters")
	ErrRoomNotesMax = errors.New("that is as many notes as one choice can have")
	ErrBadReply     = errors.New("a reply is at most 280 characters")
	ErrNoNote       = errors.New("there is no such note")
)

const (
	maxNoteChars = 280
	maxNotes     = 200
)

func checkNotesMode(p RoomParams) (string, error) {
	switch p.NotesMode {
	case "":
		return store.RoomNotesNone, nil
	case store.RoomNotesNone, store.RoomNotesNote, store.RoomNotesReplies:
		return p.NotesMode, nil
	}
	return "", ErrBadNotesMode
}

// cleanNote makes what a person wrote into plain text of one or more lines: no control characters, no leading or trailing space,
// no more than two blank lines in a row.
func cleanNote(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 32 || r == 127 || (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) || r == 0xfeff:
			return -1
		}
		return r
	}, s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	s = strings.TrimSpace(strings.Join(lines, "\n"))
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// RoomNote writes the viewer's note on a photo, or takes it away when the text is empty. It returns how many notes the selection
// has.
func (h *Hub) RoomNote(ctx context.Context, r store.Room, v RoomViewer, photo, text string) (int, error) {
	if r.NotesMode == store.RoomNotesNone || r.NotesMode == "" {
		return 0, ErrRoomNotesOff
	}
	text = cleanNote(text)
	if utf8.RuneCountInString(text) > maxNoteChars {
		return 0, ErrRoomNoteLong
	}
	s, err := h.roomSnapshot(ctx, r)
	if err != nil {
		return 0, err
	}
	if _, ok := s.by[photo]; !ok {
		return 0, ErrDropGone
	}
	sel, err := h.roomSelection(ctx, r, v)
	if err != nil {
		return 0, err
	}
	switch {
	case sel.Done:
		return 0, ErrRoomLocked
	case sel.NeedsName:
		return 0, ErrRoomName
	case sel.Chooser != "":
		return 0, ErrRoomClaimed
	}
	sc := scope(r, v)
	if text != "" {
		have, err := h.Store.Notes(ctx, r.ID, 1, &sc)
		if err != nil {
			return 0, err
		}
		if len(have) >= maxNotes {
			exists := false
			for _, n := range have {
				exists = exists || n.Photo == photo
			}
			if !exists {
				return 0, ErrRoomNotesMax
			}
		}
	}
	name := CleanViewerName(v.Name)
	if r.PickMode == store.RoomNamed {
		name = r.ClaimName
	}
	return h.Store.SetNote(ctx, r.ID, 1, sc, photo, name, text)
}

// RoomNotesOf lists the notes the viewer can see in a room, by photo: those of their own selection, with the answers.
func (h *Hub) RoomNotesOf(ctx context.Context, r store.Room, v RoomViewer) (map[string]store.Note, error) {
	out := map[string]store.Note{}
	if r.NotesMode == store.RoomNotesNone || r.NotesMode == "" {
		return out, nil
	}
	sc := scope(r, v)
	list, err := h.Store.Notes(ctx, r.ID, 1, &sc)
	if err != nil {
		return nil, err
	}
	for _, n := range list {
		if r.NotesMode != store.RoomNotesReplies {
			n.Reply = ""
		}
		out[n.Photo] = n
	}
	return out, nil
}

// RoomReply is the photographer's answer to a client's note (empty text takes the answer away).
func (h *Hub) RoomReply(ctx context.Context, owner immich.User, roomID, who, photo, text string) error {
	r, err := h.Store.RoomByID(ctx, owner.ID, roomID)
	if err != nil {
		return ErrNoRoom
	}
	if r.NotesMode != store.RoomNotesReplies {
		return ErrRoomNotesOff
	}
	text = cleanNote(text)
	if utf8.RuneCountInString(text) > maxNoteChars {
		return ErrBadReply
	}
	ok, err := h.Store.SetReply(ctx, r.ID, 1, who, photo, text)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNoNote
	}
	return nil
}

// RoomNoteCount is how many notes a room holds, for the photographer.
func (h *Hub) RoomNoteCount(ctx context.Context, r store.Room) int {
	list, err := h.Store.Notes(ctx, r.ID, 1, nil)
	if err != nil {
		return 0
	}
	return len(list)
}

// notesLine is the part of the log line for a finished choice that tells about its notes.
func notesLine(who, room string, photos, notes int) string {
	if notes == 0 {
		return i18n.PackN("{who} finished choosing in {room}: {n} photo", "{who} finished choosing in {room}: {n} photos", photos, "who", who, "room", room)
	}
	return i18n.PackN("{who} finished choosing in {room}: {n} photo, with notes", "{who} finished choosing in {room}: {n} photos, with notes", photos, "who", who, "room", room)
}
