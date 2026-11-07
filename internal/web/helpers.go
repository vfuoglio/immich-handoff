package web

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/a-h/templ"
	"strings"
)

func initial(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "?"
	}
	return strings.ToUpper(string([]rune(name)[0]))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func roleLabel(role string) string {
	if role == "sender" {
		return "You send"
	}
	return "You receive"
}

func roleClass(role string) string {
	if role == "sender" {
		return ""
	}
	return "ok"
}

func countLabel(role string) string {
	if role == "sender" {
		return "waiting to be handed over"
	}
	if role == "album" {
		return "in the album"
	}
	return "in your inbox"
}

func warnIf(b bool) string {
	if b {
		return "warn"
	}
	return ""
}

func badIf(b bool) string {
	if b {
		return "bad"
	}
	return ""
}

func badClass(b bool) string {
	if b {
		return "bad"
	}
	return ""
}

// joinConfirm tells the person exactly what joining does before they agree.
func joinConfirm(name, tag string, copyMode bool) string {
	what := "to read and download the photos you tag \"" + tag + "\" and to delete the originals after they have been handed over."
	if copyMode {
		what = "to read and download the photos you tag \"" + tag + "\". Your originals are kept: the key has no permission to delete anything."
	}
	return "This creates an API key named \"handoff: " + name + "\" in your Immich account. Immich Handoff uses it " + what +
		" Nobody else can see the key. You can stop at any time with Leave, or by deleting that key in Immich."
}

func originalsNote(copyMode bool) string {
	if copyMode {
		return "Your originals are kept."
	}
	return "Your originals go to your trash once handed over."
}

func joinLabel(state string, copyMode bool) string {
	verb := "Join"
	if state == "approved" {
		verb = "Activate"
	}
	if copyMode {
		return verb + " and keep my originals"
	}
	return verb + " and move my originals to my trash"
}

// vals builds the JSON for hx-vals.
func vals(k, v string) string {
	b, _ := json.Marshal(map[string]string{k: v})
	return string(b)
}

func decideVals(service, request, decision string) string {
	b, _ := json.Marshal(map[string]string{"service": service, "request": request, "decision": decision})
	return string(b)
}

func offerLabel(state string) string {
	switch state {
	case "join":
		return "Open to join"
	case "request":
		return "Needs approval"
	case "pending":
		return "Waiting for approval"
	case "approved":
		return "Approved"
	case "declined":
		return "Declined"
	}
	return state
}

func offerChip(state string) string {
	switch state {
	case "approved":
		return "ok"
	case "declined":
		return "bad"
	case "pending":
		return "warn"
	}
	return ""
}

func policyLabel(p string) string {
	if p == "open" {
		return "Anyone may send"
	}
	return "You approve senders"
}

func adminPolicy(p string) string {
	if p == "open" {
		return "Anyone may send"
	}
	return "Owner approves senders"
}

func memberChip(state string) string {
	if state == "broken" {
		return "bad"
	}
	return "ok"
}

func memberSuffix(state string, copyMode bool) string {
	if state == "broken" {
		return " (disconnected)"
	}
	if copyMode {
		return " (keeps originals)"
	}
	return " (moves originals)"
}

func valsJoin(service, invite, mode string) string {
	m := map[string]string{"service": service}
	if invite != "" {
		m["invite"] = invite
	}
	if mode != "" {
		m["mode"] = mode
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func visVals(service, visibility string) string {
	b, _ := json.Marshal(map[string]string{"service": service, "visibility": visibility})
	return string(b)
}

// reasonLabel turns the internal reason a pass ran into words for people.
func reasonLabel(reason string) string {
	switch {
	case reason == "webhook", strings.HasPrefix(reason, "retry"), reason == "coalesced event":
		return "a photo was tagged"
	case reason == "startup":
		return "the app started"
	case reason == "daily sweep":
		return "daily check"
	case strings.HasPrefix(reason, "manual"):
		return "Sync now"
	case strings.HasPrefix(reason, "joined"), strings.HasPrefix(reason, "reconnected"), strings.HasPrefix(reason, "workflows ready"):
		return "setup finished"
	}
	return reason
}

type baseKey struct{}

type adminKey struct{}

// isAdmin is whether the signed-in person is an Immich administrator, so the header can link to the admin page.
func isAdmin(ctx context.Context) bool {
	b, _ := ctx.Value(adminKey{}).(bool)
	return b
}

// baseHref is the <base> the page needs so its relative links reach the app root ("" = none needed).
func baseHref(ctx context.Context) string {
	b, _ := ctx.Value(baseKey{}).(string)
	return b
}

// gate switches the page off while Tags are off in the person's Immich account: dimmed, and "inert" so that
// nothing in it can be clicked or reached with the keyboard, until the notice's button turns Tags on.
func gate(off bool) templ.Attributes {
	if !off {
		return nil
	}
	return templ.Attributes{"class": "gated", "inert": true, "aria-disabled": "true"}
}

// AlbumOption is one album in the list a sender chooses from.
type AlbumOption struct{ ID, Label string }

func albumLabel(name string, count int) string {
	if count == 1 {
		return name + " (1 photo)"
	}
	return fmt.Sprintf("%s (%d photos)", name, count)
}

// albumConfirm is the question asked before an album is linked: in move mode this sends many originals to
// the trash, so it says so.
func albumConfirm(service string, copies bool) string {
	if copies {
		return "Hand over everything in this album to " + service + ", now and whatever you add later? Your originals stay where they are."
	}
	return "Hand over everything in this album to " + service + ", now and whatever you add later? Your originals are moved to your trash once the other side has a verified copy."
}
