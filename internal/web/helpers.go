package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/a-h/templ"

	"github.com/vfuoglio/immich-handoff/internal/i18n"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// The words on the page come from the person's language, which travels in the request's context (see i18n). English is
// the source: the English text of a message is its key, and a language without a translation shows the English.
func loc(ctx context.Context) *i18n.Locale { return i18n.From(ctx) }

func t(ctx context.Context, msg string, kv ...any) string { return i18n.From(ctx).T(msg, kv...) }

// h is a message with a little markup of its own (<code>, <strong>, <em>); its values are escaped.
func h(ctx context.Context, msg string, kv ...any) string { return i18n.From(ctx).H(msg, kv...) }

// n is a message with a count: the English singular (its key) and plural.
func n(ctx context.Context, one, other string, count int, kv ...any) string {
	return i18n.From(ctx).N(one, other, count, kv...)
}

// unpack is the words of something stored as a message (a reason, a line of the log), in the person's language.
func unpack(ctx context.Context, s string) string { return i18n.From(ctx).Unpack(s) }

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

func roleLabel(ctx context.Context, role string) string {
	if role == "sender" {
		return t(ctx, "You send")
	}
	return t(ctx, "You receive")
}

func roleClass(role string) string {
	if role == "sender" {
		return ""
	}
	return "ok"
}

func countLabel(ctx context.Context, role string) string {
	if role == "sender" {
		return t(ctx, "waiting to be handed over")
	}
	if role == "album" {
		return t(ctx, "in the album")
	}
	return t(ctx, "in your inbox")
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

func joinConfirm(ctx context.Context, name, tag string, copyMode bool) string {
	if copyMode {
		return t(ctx, "This creates an API key named \"handoff: {name}\" in your Immich account. Immich Handoff uses it to read and download the photos you tag \"{tag}\". Your originals are kept: the key has no permission to delete anything. Nobody else can see the key. You can stop at any time with Leave, or by deleting that key in Immich.", "name", name, "tag", tag)
	}
	return t(ctx, "This creates an API key named \"handoff: {name}\" in your Immich account. Immich Handoff uses it to read and download the photos you tag \"{tag}\" and to delete the originals after they have been handed over. Nobody else can see the key. You can stop at any time with Leave, or by deleting that key in Immich.", "name", name, "tag", tag)
}

func originalsNote(ctx context.Context, copyMode bool) string {
	if copyMode {
		return t(ctx, "Your originals are kept.")
	}
	return t(ctx, "Your originals go to your trash once handed over.")
}

func joinLabel(ctx context.Context, state string, copyMode bool) string {
	switch {
	case state == "approved" && copyMode:
		return t(ctx, "Activate and keep my originals")
	case state == "approved":
		return t(ctx, "Activate and move my originals to my trash")
	case copyMode:
		return t(ctx, "Join and keep my originals")
	}
	return t(ctx, "Join and move my originals to my trash")
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

func offerLabel(ctx context.Context, state string) string {
	switch state {
	case "join":
		return t(ctx, "Open to join")
	case "request":
		return t(ctx, "Needs approval")
	case "pending":
		return t(ctx, "Waiting for approval")
	case "approved":
		return t(ctx, "Approved")
	case "declined":
		return t(ctx, "Declined")
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

func policyLabel(ctx context.Context, p string) string {
	if p == "open" {
		return t(ctx, "Anyone may send")
	}
	return t(ctx, "You approve senders")
}

func adminPolicy(ctx context.Context, p string) string {
	if p == "open" {
		return t(ctx, "Anyone may send")
	}
	return t(ctx, "Owner approves senders")
}

func memberChip(state string) string {
	if state == "broken" {
		return "bad"
	}
	return "ok"
}

func memberSuffix(ctx context.Context, state string, copyMode bool) string {
	if state == "broken" {
		return " (" + t(ctx, "disconnected") + ")"
	}
	if copyMode {
		return " (" + t(ctx, "keeps originals") + ")"
	}
	return " (" + t(ctx, "moves originals") + ")"
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

func reasonLabel(ctx context.Context, reason string) string {
	switch {
	case reason == "webhook", strings.HasPrefix(reason, "retry"), reason == "coalesced event":
		return t(ctx, "a photo was tagged")
	case reason == "startup":
		return t(ctx, "the app started")
	case reason == "daily sweep":
		return t(ctx, "daily check")
	case strings.HasPrefix(reason, "manual"):
		return t(ctx, "Sync now")
	case strings.HasPrefix(reason, "joined"), strings.HasPrefix(reason, "reconnected"), strings.HasPrefix(reason, "workflows ready"):
		return t(ctx, "setup finished")
	}
	return reason
}

type baseKey struct{}

type adminKey struct{}

type tabsKey struct{}

// hasTabs is whether this page has views to switch between (the Overview, Services and Activity): the dashboard has,
// the invitation, admin and notice pages do not.
func hasTabs(ctx context.Context) bool {
	b, _ := ctx.Value(tabsKey{}).(bool)
	return b
}

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

func albumLabel(ctx context.Context, name string, count int) string {
	return t(ctx, "{name} ({photos})", "name", name, "photos", n(ctx, "{n} photo", "{n} photos", count))
}

func albumConfirm(ctx context.Context, service string, copies bool) string {
	if copies {
		return t(ctx, "Hand over everything in this album to {service}, now and whatever you add later? Your originals stay where they are.", "service", service)
	}
	return t(ctx, "Hand over everything in this album to {service}, now and whatever you add later? Your originals are moved to your trash once the other side has a verified copy.", "service", service)
}

func pausedSuffix(ctx context.Context, paused bool) string {
	if paused {
		return " (" + t(ctx, "paused") + ")"
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

func limitText(ctx context.Context, photos, mb int) string {
	switch {
	case photos > 0 && mb > 0:
		return t(ctx, "{photos} and {mb} MB a day", "photos", n(ctx, "{n} photo", "{n} photos", photos), "mb", mb)
	case photos > 0:
		return t(ctx, "{photos} a day", "photos", n(ctx, "{n} photo", "{n} photos", photos))
	case mb > 0:
		return t(ctx, "{mb} MB a day", "mb", mb)
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

func sparkLabel(ctx context.Context, days [7]DayBar) string {
	parts := make([]string, 0, 7)
	for _, d := range days {
		parts = append(parts, fmt.Sprintf("%s %d", d.Label, d.N))
	}
	return t(ctx, "Photos handed over per day, oldest first: {days}", "days", strings.Join(parts, ", "))
}

// hueClass picks one of twelve colours for a service's initial from its name, so a service looks the same every time.
func hueClass(name string) string {
	var sum uint32 = 2166136261
	for _, b := range []byte(name) {
		sum = (sum ^ uint32(b)) * 16777619
	}
	return fmt.Sprintf("h%d", sum%12)
}

func originalsShort(ctx context.Context, copyMode bool) string {
	if copyMode {
		return t(ctx, "keeps originals")
	}
	return t(ctx, "moves originals")
}

func receiveShort(ctx context.Context, s ServiceView) string {
	if s.Album != "" {
		return t(ctx, "arrives in an album")
	}
	return t(ctx, "arrives tagged {tag}", "tag", s.Tag)
}

func shortLabel(ctx context.Context, role string) string {
	switch role {
	case "sender":
		return t(ctx, "waiting")
	case "album":
		return t(ctx, "in album")
	}
	return t(ctx, "in inbox")
}

// countURL is where a row asks for its number. sum names the element on the row's line that gets the number too.
func countURL(service, role, sum string) string {
	return "fragments/count?service=" + url.QueryEscape(service) + "&role=" + role + "&sum=" + sum
}

func sendChips(ctx context.Context, s ServiceView) []Chip {
	var c []Chip
	if s.Paused != "" {
		c = append(c, Chip{"bad", t(ctx, "Problem")})
	}
	if s.Hold != "" || s.MinePaused {
		c = append(c, Chip{"warn", t(ctx, "Paused")})
	}
	if s.Waiting != "" {
		c = append(c, Chip{"warn", t(ctx, "Limit")})
	}
	return c
}

func recvChips(ctx context.Context, s ServiceView) []Chip {
	var c []Chip
	if s.Paused != "" {
		c = append(c, Chip{"bad", t(ctx, "Problem")})
	}
	if s.Stopped {
		c = append(c, Chip{"warn", t(ctx, "Paused")})
	}
	return c
}

func ownedChips(ctx context.Context, o OwnedView) []Chip {
	var c []Chip
	if o.Broken != "" {
		c = append(c, Chip{"bad", t(ctx, "Disconnected")})
	}
	if o.Paused {
		c = append(c, Chip{"warn", t(ctx, "Paused")})
	}
	if count := len(o.Requests); count > 0 {
		c = append(c, Chip{"warn", t(ctx, "{n} waiting", "n", count)})
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

func laneLabel(ctx context.Context, l SceneLane) string {
	switch l.State {
	case "broken":
		return t(ctx, "Connection lost")
	case "paused":
		if l.PausedBy == "service" {
			return t(ctx, "Paused by the owner")
		}
		return t(ctx, "Paused by the sender")
	case "capped":
		return t(ctx, "Daily limit reached: photos wait")
	}
	return t(ctx, "Flowing")
}

// sceneOf is the live scene for the person: a stage for every service they send to (their own lane), receive from
// (the senders from the configuration) or offer (the senders who joined), with the state each lane is in now.
func sceneOf(ctx context.Context, d *DashboardData, current []*syncer.Service) []SceneStage {
	var out []SceneStage
	for _, s := range d.Sending() {
		lane := SceneLane{Name: s.Account, State: "flow"}
		switch {
		case s.Paused != "":
			lane.State = "broken"
		case s.MinePaused:
			lane.State, lane.PausedBy = "paused", "sender"
		case s.Hold != "":
			lane.State, lane.PausedBy = "paused", "service"
		case s.Waiting != "":
			lane.State = "capped"
		}
		if lane.Name != "" {
			out = append(out, SceneStage{Service: s.Name, Where: n(ctx, "{n} receiver receiving", "{n} receivers receiving", s.To), Lanes: []SceneLane{lane}})
		}
	}
	for _, s := range d.Receiving() {
		st := SceneStage{Service: s.Name, Where: t(ctx, "into your library")}
		for _, svc := range current {
			if svc.Label() != s.Name {
				continue
			}
			for _, a := range svc.Senders {
				lane := SceneLane{Name: a.Name, State: "flow"}
				switch {
				case svc.Broken != "":
					lane.State = "broken"
				case svc.Paused:
					lane.State, lane.PausedBy = "paused", "service"
				}
				st.Lanes = append(st.Lanes, lane)
			}
		}
		if len(st.Lanes) > 0 {
			out = append(out, st)
		}
	}
	for _, o := range d.Owned {
		st := SceneStage{Service: o.Name, Where: t(ctx, "into your library")}
		for _, m := range o.Members {
			lane := SceneLane{Name: m.Name, State: "flow"}
			switch {
			case o.Broken != "" || m.State == "broken":
				lane.State = "broken"
			case o.Paused:
				lane.State, lane.PausedBy = "paused", "service"
			case m.Paused:
				lane.State, lane.PausedBy = "paused", "sender"
			case m.AtLimit:
				lane.State = "capped"
			}
			st.Lanes = append(st.Lanes, lane)
		}
		if len(st.Lanes) > 0 {
			out = append(out, st)
		}
	}
	return out
}

// needsGist is the highlighted half of the page's heading.
func needsGist(ctx context.Context, count int) string {
	switch count {
	case 0:
		return t(ctx, "Nothing needs you.")
	case 1:
		return t(ctx, "One thing needs you.")
	case 2:
		return t(ctx, "Two things need you.")
	case 3:
		return t(ctx, "Three things need you.")
	case 4:
		return t(ctx, "Four things need you.")
	case 5:
		return t(ctx, "Five things need you.")
	}
	return t(ctx, "{n} things need you.", "n", count)
}

// activityTone colours a timeline dot by what kind of event it is.
func activityTone(kind string) string {
	switch kind {
	case "broken", "removed", "deleted", "left":
		return "bad"
	case "limit", "paused", "album":
		return "warn"
	case "joined", "approved", "reconnected", "resumed", "created", "backup":
		return "ok"
	}
	return ""
}

// adminOwner is " · offered by ..." for the line under a service's name, or nothing for a service from the configuration.
func adminOwner(owner string) string {
	if owner == "" {
		return ""
	}
	return " \u00b7 " + owner
}

// languages are the languages the pages come in, in the order of the language menu.
func languages() []i18n.Lang { return i18n.All }

func langName(l i18n.Lang) string { return i18n.Names[l] }

func checked(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// clientMessages are the messages the page's scripts use (the scene's sentences, the palette's commands, the dialogs'
// defaults). The page carries them, translated, for the scripts to read (see ts/i18n.ts). A test keeps this list equal
// to what the scripts really ask for.
var clientMessages = []string{
	"A sender", "Activity", "Continue", "Copied", "Connection lost", "Daily limit reached: {waits}", "Daily limit reached: {waits} until {when}",
	"Flowing", "Go to {view}", "Nothing matches", "Offer a service of your own", "Open the administrator's page", "Open {name}", "Overview",
	"Paused by the owner", "Paused by the sender", "Photos wait", "Please confirm", "Sending {n}", "Services", "Some updates were missed; refreshing",
	"Switch between the light and the dark theme", "Sync now", "Turn the arrival chime on or off", "a service", "action", "admin", "needs you", "service", "sound", "theme", "view",
	"{n} photo", "{n} photo waits", "{service} lost its connection", "{service} was paused by its owner", "{service} was reconnected", "{service} was resumed",
	"{who} handed over {photos} to {service}", "{who} is handing over {photos} to {service}", "{who} paused sending to {service}", "{who} reached the daily limit for {service}",
	"{who} resumed sending to {service}", "{who}'s connection to {service} was lost; no photo was touched", "{who}: {photos} already there in {service}",
	"{who}: {photos} could not be handed over; the originals were kept and it will be retried", "{who}: {photos} refused, the receiver has them in the trash; the originals were kept",
	"{who}: {photos} wait for the daily limit", "{who}: {photos} wait for the daily limit, until {when}",
}

// clientCatalog is what the page hands its scripts: the language and the translated messages they use.
func clientCatalog(ctx context.Context) map[string]any {
	l := i18n.From(ctx)
	msgs := map[string]any{}
	for _, id := range clientMessages {
		if e, ok := l.Entry(id); ok {
			msgs[id] = e
		}
	}
	return map[string]any{"lang": l.Code(), "messages": msgs}
}

// numbersTrigger is when the numbers section asks for itself again. The first paint (skeletons) asks at once; the
// section that comes back must not, or it would ask again and again for ever, as it replaces itself.
func numbersTrigger(n NumbersView) string {
	if n.Loading {
		return "load, handoff:run from:body"
	}
	return "handoff:run from:body"
}
