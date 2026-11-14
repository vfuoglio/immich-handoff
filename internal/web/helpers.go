package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
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
// backupDocsURL is where the page sends an administrator to read about switching the backup download on.
const backupDocsURL = "https://github.com/vfuoglio/immich-handoff/blob/main/docs/operations.md#download-a-backup-from-the-administrators-page"

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

func mailVals(box string, on bool) string {
	v := "0"
	if on {
		v = "1"
	}
	return vals2("service", box, "on", v)
}

func vals2(k1, v1, k2, v2 string) string {
	b, _ := json.Marshal(map[string]string{k1: v1, k2: v2})
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

func valsJoinPool(service, invite string) string {
	m := map[string]string{"service": service, "mode": "copy", "pool": "1"}
	if invite != "" {
		m["invite"] = invite
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// poolConfirm is the question a person answers before joining an event pool. It says plainly what joining means.
func poolConfirm(ctx context.Context, name, tag, ends string) string {
	text := t(ctx, "This creates an API key named \"handoff: {name}\" in your Immich account that can read and download the photos you tag \"{tag}\" and also receive photos. Every photo you tag \"{tag}\" will be handed over to everyone else in this pool, and the photos they tag will come to you, each tagged with who took it. Your originals stay where they are. People you do not know may be in the pool if its owner lets anyone join. You can stop at any time with Leave: you keep what you received, and nothing more is shared with you.", "name", name, "tag", tag)
	if ends != "" {
		text += " " + t(ctx, "It ends {when}.", "when", ends)
	}
	return text
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

type versionKey struct{}

// versionOf is the version of the app that is running: set when it was built (a release says 1.2.0, a build from a
// checkout says dev), and read from the running program, not written into the pages.
func versionOf(ctx context.Context) string { v, _ := ctx.Value(versionKey{}).(string); return v }

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

// keyAttr marks a button with the key that presses it, for the style sheet to show and keys.ts to listen for.
func keyAttr(key string) templ.Attributes {
	if key == "" {
		return nil
	}
	return templ.Attributes{"data-key": key}
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

// sceneDir is which way a stage's photos fly: "in" toward the person (they receive or offer), "out" away from them (they send).
func sceneDir(s SceneStage) string {
	if s.Out {
		return "out"
	}
	return "in"
}

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
			out = append(out, SceneStage{Service: s.Name, Out: true, Where: n(ctx, "{n} receiver receiving", "{n} receivers receiving", s.To), Lanes: []SceneLane{lane}})
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
	"Photo {n} of {total}", "Previous photo", "Next photo", "Close",
	"Photos from this device", "{n} photo arrived in your Immich", "{n} photo could not be sent", "{n} photo is waiting for you to sign in to Immich again",
	"Choose where it goes, then press Send", "{n} file shared with Handoff is waiting for you to choose where it goes", "Sharing to Handoff needs the app to be installed on this phone. Choose the photos here instead.",
	"A large video on mobile data", "Cancel", "Locations go with your photos", "Send now", "Wait for Wi-Fi", "Waiting for Wi-Fi: this device is on mobile data", "This browser cannot keep files waiting while the page is closed; keep this page open until they are sent.", "{n} file could not be kept for sending: this browser has no room left. Send what is waiting first, or free some space on the device.", "{n} large video is about to go over mobile data. Send it now, or wait until this device is on Wi-Fi?", "Photos are sent as they are, so the place where each was taken goes with it, and so do the details Immich shows, for you and for whoever you send them to. Nothing is removed.",
	"Waiting", "Sending, {pct}%", "Waiting for the network", "Sign in to Immich again to go on", "Already in your library", "Sent", "Could not be sent: {reason}", "Try again", "Send again", "Remove", "Send", "{n} file left to send", "{n} file sent", "Send {n} file", "{n} file was left out: it is not a photo or video, or it is too large", "Send photos from this device",
	"A sender", "Activity", "Continue", "Copied", "Connection lost", "Daily limit reached: {waits}", "Daily limit reached: {waits} until {when}",
	"Flowing", "Go to {view}", "Nothing matches", "Offer a service of your own", "Open the administrator's page", "Open {name}", "Overview",
	"Paused by the owner", "Paused by the sender", "Photos wait", "Please confirm", "Sending {n}", "Services", "Some updates were missed; refreshing",
	"Switch between the light and the dark theme", "Sync now", "The QR code could not be made.", "The backup could not be downloaded.", "The backup was downloaded.", "Turn the arrival chime on or off", "a service", "action", "admin", "needs you", "service", "sound", "theme", "view",
	"{n} photo", "{n} photo waits", "{service} lost its connection", "{service} was paused by its owner", "{service} was reconnected", "{service} was resumed",
	"{who} handed over {photos} to {service}", "{who} is handing over {photos} to {service}", "{who} paused sending to {service}", "{who} reached the daily limit for {service}",
	"{who} resumed sending to {service}", "{who}'s connection to {service} was lost; no photo was touched", "{who}: {photos} already there in {service}",
	"{who}: {photos} could not be handed over; the originals were kept and it will be retried", "{who}: {photos} refused, the receiver has them in the trash; the originals were kept",
	"{who}: {photos} wait for the daily limit", "{who}: {photos} wait for the daily limit, until {when}",
	"Could not be sent", "Received", "Sending", "Sent already", "Waiting for the connection", "Waiting to send",
	"Could not turn on notifications.", "Notifications are blocked for this site. Allow them in your browser's settings for this page, then try again.",
	"Notifications need this page to be opened over HTTPS.", "Notifications work on this device.", "On an iPhone or iPad, add this page to the Home Screen first, then open it from there.",
	"This browser cannot show notifications from this page.",
	"Choose what the photo is for", "Could not send the message", "Record a voice message", "Record again", "Stop recording",
	"The microphone is not available", "Thank you for your message!",
	"Choose photos first", "Everything here was sent already", "Remove {name}", "Sending {done} of {total}", "Try again", "{n} file", "{n} file could not be sent",
	"{n} file sent. Thank you!", "Only photos are taken here: {name} was left out",
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

// ratingLabel is a choice in the form's list of minimum ratings.
func ratingLabel(ctx context.Context, stars int) string {
	if stars == 0 {
		return t(ctx, "Any rating")
	}
	return n(ctx, "{n} star or more", "{n} stars or more", stars)
}

// rulesText says in words when a service hands photos over and what it takes in, for the people who send to it.
func (w *Server) rulesText(ctx context.Context, svc *syncer.Service) string {
	var out []string
	if q := w.S.QuietOf(svc); q.Set() {
		if q.LargeMB > 0 {
			out = append(out, t(ctx, "Files over {mb} MB are handed over only between {from} and {to}.", "mb", q.LargeMB, "from", syncer.Clock(q.From), "to", syncer.Clock(q.To)))
		} else {
			out = append(out, t(ctx, "Everything is handed over only between {from} and {to}.", "from", syncer.Clock(q.From), "to", syncer.Clock(q.To)))
		}
	}
	a := svc.Accept
	var parts []string
	if a.MinRating > 0 {
		parts = append(parts, t(ctx, "a rating of {n} or more", "n", a.MinRating))
	}
	if a.PhotosOnly {
		parts = append(parts, t(ctx, "photos only"))
	}
	switch a.Orientation {
	case "landscape":
		parts = append(parts, t(ctx, "landscape only"))
	case "portrait":
		parts = append(parts, t(ctx, "portrait only"))
	}
	if a.MaxMB > 0 {
		parts = append(parts, t(ctx, "files up to {mb} MB", "mb", a.MaxMB))
	}
	if len(parts) > 0 {
		out = append(out, t(ctx, "Accepts: {parts}.", "parts", strings.Join(parts, ", ")))
	}
	return strings.Join(out, " ")
}

// createConfirm is what the dialog that makes a service says about the key it makes.
func createConfirm(ctx context.Context, deleteOK bool) string {
	msg := t(ctx, "Creating a service makes an API key named for it in your Immich account. The app uses it to upload the photos senders hand to you. Nobody can see the key.")
	if deleteOK {
		msg += " " + t(ctx, "The key can also move photos the app handed over to your trash (copies that expire, a hand-over taken back), never delete them for good.")
	}
	return msg
}

func extendVals(id string, days int) string {
	b, _ := json.Marshal(map[string]string{"service": id, "days": strconv.Itoa(days)})
	return string(b)
}

func allVals(id string) string {
	b, _ := json.Marshal(map[string]string{"service": id, "all": "1"})
	return string(b)
}

// hostVals is the form of removing a co-host (an empty user: the person asking stops helping).
func hostVals(id, user string) string {
	m := map[string]string{"service": id}
	if user != "" {
		m["user"] = user
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func hostRoleLabel(ctx context.Context, manage bool) string {
	if manage {
		return t(ctx, "approves, pauses and extends")
	}
	return t(ctx, "only looks")
}

func shortVals(id string, on bool) string {
	v := "0"
	if on {
		v = "1"
	}
	b, _ := json.Marshal(map[string]string{"service": id, "on": v})
	return string(b)
}

func dimLabel(ctx context.Context, px int) string {
	if px == 0 {
		return t(ctx, "never (send them as they are)")
	}
	return t(ctx, "{px} pixels", "px", px)
}

func messageAuthor(ctx context.Context, name string) string {
	if name == "" {
		return t(ctx, "A guest")
	}
	return name
}

func promptState(ctx context.Context, files int) string {
	if files == 0 {
		return t(ctx, "no photo yet")
	}
	return n(ctx, "{n} photo", "{n} photos", files)
}

// shareHref is where "See the photos" leads: the full address when the app knows where Immich is, or the shared page's
// path (the page's script makes it a full address from the key).
func shareHref(s ShareView) string {
	if s.URL != "" {
		return s.URL
	}
	return "/share/" + s.Key
}

func wallVals(id string, renew bool) string {
	v := "0"
	if renew {
		v = "1"
	}
	b, _ := json.Marshal(map[string]string{"service": id, "renew": v})
	return string(b)
}

func wallMsgVals(id string, on bool) string {
	v := "0"
	if on {
		v = "1"
	}
	b, _ := json.Marshal(map[string]string{"service": id, "on": v})
	return string(b)
}

func scopeVals(id, scope string) string {
	b, _ := json.Marshal(map[string]string{"service": id, "scope": scope})
	return string(b)
}

func onwardOptionLabel(ctx context.Context, o OnwardOptionView) string {
	if o.Copy {
		return o.Service + " \u00b7 " + t(ctx, "you keep your originals")
	}
	return o.Service + " \u00b7 " + t(ctx, "your originals go to your trash")
}

// frameChips are the chips of a frame's row: whether it is showing and when it ends.
func frameChips(ctx context.Context, f FrameView) []Chip {
	if f.Ended {
		return []Chip{{Text: t(ctx, "Ended"), Tone: "bad"}}
	}
	return []Chip{{Text: t(ctx, "Showing"), Tone: "ok"}}
}

func roomChips(ctx context.Context, r RoomView) []Chip {
	switch {
	case r.Ended:
		return []Chip{{Text: t(ctx, "Ended"), Tone: "bad"}}
	case r.Paused:
		return []Chip{{Text: t(ctx, "Paused"), Tone: "warn"}}
	}
	return []Chip{{Text: t(ctx, "Open"), Tone: "ok"}}
}

// pausedValue is what the pause button sends: the state the room should be in after the press.
func pausedValue(nowPaused bool) string {
	if nowPaused {
		return "0"
	}
	return "1"
}

// markText says in words how a room's pictures are marked (see markSummary).
func markText(ctx context.Context, summary string) string {
	kind, text, _ := strings.Cut(summary, ":")
	switch kind {
	case "logo":
		return t(ctx, "The pictures carry your logo in a corner.")
	case "both":
		return t(ctx, "The pictures carry \"{text}\" across them and your logo in a corner.", "text", text)
	}
	return t(ctx, "The pictures carry \"{text}\" across them.", "text", text)
}
