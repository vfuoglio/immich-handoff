package web

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/a-h/templ"
	"net/url"
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

func pausedSuffix(paused bool) string {
	if paused {
		return " (paused)"
	}
	return ""
}

func pauseVals(service string, paused bool) string {
	v := "0"
	if paused {
		v = "1"
	}
	b, _ := json.Marshal(map[string]string{"service": service, "paused": v})
	return string(b)
}

// limitText says a service's daily cap in words ("20 photos and 500 MB a day"); empty when there is none.
func limitText(photos, mb int) string {
	switch {
	case photos > 0 && mb > 0:
		return fmt.Sprintf("%d %s and %d MB a day", photos, plural(photos, "photo", "photos"), mb)
	case photos > 0:
		return fmt.Sprintf("%d %s a day", photos, plural(photos, "photo", "photos"))
	case mb > 0:
		return fmt.Sprintf("%d MB a day", mb)
	}
	return ""
}

func primaryIf(primary bool) string {
	if primary {
		return "primary"
	}
	return ""
}

func doneIf(done bool) string {
	if done {
		return "done"
	}
	return ""
}

func tickIf(done bool) string {
	if done {
		return "\u2713"
	}
	return ""
}

// needsLine is the sentence under the greeting.
func needsLine(n int) string {
	switch n {
	case 0:
		return "Nothing needs you right now."
	case 1:
		return "One thing needs you."
	}
	return fmt.Sprintf("%d things need you.", n)
}

// The week's chart is drawn to scale in a 300 by 56 box, one point at the centre of each of seven equal columns,
// so the day labels under it (a seven-column grid) sit exactly under their points.
const (
	sparkW, sparkH = 300.0, 56.0
	sparkTop       = 8.0
	sparkBottom    = 50.0
)

func sparkMax(days [7]DayBar) int {
	m := 1
	for _, d := range days {
		m = max(m, d.N)
	}
	return m
}

func sparkX(i int) float64 { return (float64(i) + .5) * sparkW / 7 }

func sparkY(n, m int) float64 { return sparkBottom - float64(n)/float64(m)*(sparkBottom-sparkTop) }

func sparkLine(days [7]DayBar) string {
	m := sparkMax(days)
	var b strings.Builder
	for i, d := range days {
		op := "L"
		if i == 0 {
			op = "M"
		}
		fmt.Fprintf(&b, "%s%.1f %.1f ", op, sparkX(i), sparkY(d.N, m))
	}
	return strings.TrimSpace(b.String())
}

func sparkArea(days [7]DayBar) string {
	return fmt.Sprintf("%s L%.1f %.0f L%.1f %.0f Z", sparkLine(days), sparkX(6), sparkH, sparkX(0), sparkH)
}

func sparkEndX() string { return fmt.Sprintf("%.1f", sparkX(6)) }

func sparkEndY(days [7]DayBar) string { return fmt.Sprintf("%.1f", sparkY(days[6].N, sparkMax(days))) }

func sparkLabel(days [7]DayBar) string {
	parts := make([]string, 0, 7)
	for _, d := range days {
		parts = append(parts, fmt.Sprintf("%s %d", d.Label, d.N))
	}
	return "Photos handed over per day, oldest first: " + strings.Join(parts, ", ")
}

// hueClass picks one of twelve colours for a service's initial from its name, so a service looks the same every time.
func hueClass(name string) string {
	var sum uint32 = 2166136261
	for _, b := range []byte(name) {
		sum = (sum ^ uint32(b)) * 16777619
	}
	return fmt.Sprintf("h%d", sum%12)
}

func originalsShort(copyMode bool) string {
	if copyMode {
		return "keeps originals"
	}
	return "moves originals"
}

func receiveShort(s ServiceView) string {
	if s.Album != "" {
		return "arrives in an album"
	}
	return "arrives tagged " + s.Tag
}

// shortLabel is the small word under the number on a row's line.
func shortLabel(role string) string {
	switch role {
	case "sender":
		return "waiting"
	case "album":
		return "in album"
	}
	return "in inbox"
}

// countURL is where a row asks for its number. sum names the element on the row's line that gets the number too.
func countURL(service, role, sum string) string {
	return "fragments/count?service=" + url.QueryEscape(service) + "&role=" + role + "&sum=" + sum
}

func sendChips(s ServiceView) []Chip {
	var c []Chip
	if s.Paused != "" {
		c = append(c, Chip{"bad", "Problem"})
	}
	if s.Hold != "" || s.MinePaused {
		c = append(c, Chip{"warn", "Paused"})
	}
	if s.Waiting != "" {
		c = append(c, Chip{"warn", "Limit"})
	}
	return c
}

func recvChips(s ServiceView) []Chip {
	var c []Chip
	if s.Paused != "" {
		c = append(c, Chip{"bad", "Problem"})
	}
	if s.Stopped {
		c = append(c, Chip{"warn", "Paused"})
	}
	return c
}

func ownedChips(o OwnedView) []Chip {
	var c []Chip
	if o.Broken != "" {
		c = append(c, Chip{"bad", "Disconnected"})
	}
	if o.Paused {
		c = append(c, Chip{"warn", "Paused"})
	}
	if n := len(o.Requests); n > 0 {
		c = append(c, Chip{"warn", fmt.Sprintf("%d waiting", n)})
	}
	return c
}

// Sending is the services the person sends to.
func (d DashboardData) Sending() []ServiceView { return d.byRole("sender", false) }

// Receiving is the services the person receives from but does not offer themselves (those are in Owned).
func (d DashboardData) Receiving() []ServiceView { return d.byRole("receiver", true) }

func (d DashboardData) byRole(role string, notOwned bool) []ServiceView {
	var out []ServiceView
	for _, s := range d.Services {
		if s.Role == role && !(notOwned && s.Owned) {
			out = append(out, s)
		}
	}
	return out
}

// RowCount is how many service rows the page shows; with exactly one it starts open.
func (d DashboardData) RowCount() int { return len(d.Sending()) + len(d.Receiving()) + len(d.Owned) }
