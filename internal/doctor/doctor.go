// Package doctor checks a setup and says how to fix what is wrong. It is what `immich-handoff doctor` runs.
// It only reads: it never changes Immich, the store or a photo.
package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/vfuoglio/immich-handoff/internal/backup"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vfuoglio/immich-handoff/internal/bridge"
	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/notify"
	"github.com/vfuoglio/immich-handoff/internal/push"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// Level is how serious a finding is.
type Level int

const (
	OK   Level = iota // working
	Warn              // works, but something is likely to bite
	Fail              // broken: something will not work
)

func (l Level) String() string { return [...]string{"ok  ", "warn", "FAIL"}[l] }

// Input is everything a check needs. The caller builds it from the environment and the store.
type Input struct {
	Env         func(string) string // os.Getenv, or a map in tests
	BaseURL     string              // IMMICH_BASE_URL, as the app reaches Immich
	Probe       *immich.API         // a client with no key, for the version
	Services    []*syncer.Service   // the configured and joined services; nil when LoadErr is set
	LoadErr     error               // why the configuration or the store could not be loaded
	StoreNote   string              // what the store check found (empty: self-service is off)
	Hook        syncer.Webhook
	Client      *http.Client // for calling the webhook address; nil: a client with a short timeout
	RunsToCheck int          // how many recent workflow runs to look at (default 10)
}

// Report collects findings and prints them as it goes.
type Report struct {
	W     io.Writer
	Fails int
	Warns int
}

func (r *Report) add(l Level, what, fix string) {
	switch l {
	case Fail:
		r.Fails++
	case Warn:
		r.Warns++
	}
	fmt.Fprintf(r.W, "%s  %s\n", l, what)
	if fix != "" && l != OK {
		for _, line := range strings.Split(fix, "\n") {
			fmt.Fprintf(r.W, "      -> %s\n", line)
		}
	}
}

func (r *Report) section(name string) { fmt.Fprintf(r.W, "\n%s\n", name) }

// Run does every check and writes the result. It returns how many things failed and how many were warnings.
func Run(ctx context.Context, in Input, w io.Writer) (fails, warns int) {
	r := &Report{W: w}
	if in.RunsToCheck <= 0 {
		in.RunsToCheck = 10
	}
	settings(r, in)
	notifications(r, in)
	if !immichReachable(ctx, r, in) {
		fmt.Fprintln(w, "\nThe other checks need Immich, so they were skipped.")
		return summary(r)
	}
	if in.LoadErr != nil {
		r.section("Configuration")
		r.add(Fail, "cannot load the configuration or the stored keys: "+in.LoadErr.Error(),
			"Fix what the message says (the routes file, a missing variable, or HANDOFF_STORE_KEY) and run the check again.")
		return summary(r)
	}
	if in.StoreNote != "" {
		r.add(OK, in.StoreNote, "")
	}
	webhookReachable(ctx, r, in)
	services(ctx, r, in)
	return summary(r)
}

func summary(r *Report) (int, int) {
	fmt.Fprintln(r.W)
	switch {
	case r.Fails > 0:
		fmt.Fprintf(r.W, "%d problem(s) to fix, %d warning(s). See docs/troubleshooting.md for more.\n", r.Fails, r.Warns)
	case r.Warns > 0:
		fmt.Fprintf(r.W, "Nothing is broken; %d warning(s) above are worth a look.\n", r.Warns)
	default:
		fmt.Fprintln(r.W, "Everything checked is fine.")
	}
	return r.Fails, r.Warns
}

func settings(r *Report, in Input) {
	r.section("Settings")
	if n := len(in.Hook.Secret); n < 16 {
		r.add(Fail, fmt.Sprintf("WEBHOOK_SECRET is %d characters; it must be at least 16", n),
			"Set a long random value in .env: openssl rand -hex 24")
	} else {
		r.add(OK, "WEBHOOK_SECRET is set and long enough", "")
	}
	if key := firstOf(in.Env, "HANDOFF_STORE_KEY"); key != "" && len(key) < 16 {
		r.add(Fail, fmt.Sprintf("HANDOFF_STORE_KEY is %d characters; it must be at least 16", len(key)),
			"Set a long random value (openssl rand -hex 24). Changing it later locks the stored keys, so pick it once.")
	}
	u := in.Hook.URL
	switch {
	case u == "":
		r.add(Warn, "PUBLIC_WEBHOOK_URL is not set, so no workflows are created and photos are handed over only at startup and once a day",
			"Set PUBLIC_WEBHOOK_URL to the address Immich's server can reach this app at, ending in /webhook/immich.")
	default:
		pu, err := url.Parse(u)
		switch {
		case err != nil || pu.Host == "" || (pu.Scheme != "http" && pu.Scheme != "https"):
			r.add(Fail, "PUBLIC_WEBHOOK_URL is not a web address: "+u,
				"Use a full address such as http://immich-handoff:8080/webhook/immich")
		case isLoopback(pu.Hostname()):
			r.add(Fail, "PUBLIC_WEBHOOK_URL points at "+pu.Hostname()+", which for Immich's server is Immich itself, not this app",
				"Use the name this container has on Immich's Docker network (for example http://immich-handoff:8080/webhook/immich), or an address that reaches it from Immich's container.")
		case !strings.HasSuffix(strings.TrimRight(pu.Path, "/"), "/webhook/immich"):
			r.add(Warn, "PUBLIC_WEBHOOK_URL does not end in /webhook/immich: "+u,
				"The app listens for Immich at /webhook/immich. Behind a proxy that changes the path, check the proxy.")
		default:
			r.add(OK, "PUBLIC_WEBHOOK_URL is "+u, "")
		}
	}
	if firstOf(in.Env, "PUBLIC_IMMICH_URL") == "" {
		r.add(Warn, "PUBLIC_IMMICH_URL is not set, so the page shows no thumbnails",
			"Set it to Immich's address as your browser sees it (optional).")
	}
	backups(r, in)
}

// notifications reports where messages go and whether the settings are ones the app starts with. It sends nothing;
// `doctor --send-test` and the administrator's page do that.
func notifications(r *Report, in Input) {
	r.section("Notifications")
	dests, err := notify.FromEnv(in.Env)
	if err != nil {
		r.add(Fail, "the notification settings are not valid, and the app will not start with them: "+err.Error(),
			"Fix NOTIFY_URL, or the file that NOTIFY_FILE names (see notify.example.yaml).")
		return
	}
	people := false
	if len(dests) == 0 {
		r.add(OK, "no destination is set (NOTIFY_URL, NOTIFY_FILE): problems appear in the log and on the page only", "")
	}
	for _, d := range dests {
		kinds := "every kind"
		if len(d.Kinds) > 0 {
			var names []string
			for _, k := range notify.Kinds {
				if d.Kinds[k] {
					names = append(names, string(k))
				}
			}
			kinds = strings.Join(names, ", ")
		}
		switch {
		case d.Mail != nil && d.Mail.People:
			people = true
			r.add(OK, fmt.Sprintf("%s: mail to the people taking part (%s)", d.Name, d.Format), "")
		default:
			r.add(OK, fmt.Sprintf("%s: %s, takes %s", d.Name, d.Format, kinds), "")
		}
	}
	appURL := firstOf(in.Env, "PUBLIC_APP_URL")
	hasStore := firstOf(in.Env, "HANDOFF_STORE_KEY", "HANDOFF_STORE_KEY_FILE") != ""
	if people && appURL == "" {
		r.add(Fail, "a destination with people: true needs PUBLIC_APP_URL, and the app will not start without it",
			"Set PUBLIC_APP_URL to the address of this page as people open it: every mail links to the page and to the way to stop the emails.")
	}
	if people && !hasStore {
		r.add(Fail, "a destination with people: true needs HANDOFF_STORE_KEY, and the app will not start without it", "People turn email on from the page, which needs the data file.")
	}
	switch contact, err := push.Contact(in.Env("PUSH_CONTACT"), appURL); {
	case err != nil:
		r.add(Fail, err.Error(), "Use mailto:you@example.com or an https address.")
	case firstOf(in.Env, "PUSH") == "off":
		r.add(OK, "notifications on devices are switched off (PUSH=off)", "")
	case !hasStore:
		r.add(OK, "notifications on devices are off: people cannot join from the page (no HANDOFF_STORE_KEY)", "")
	case contact == "":
		r.add(OK, "notifications on devices are off: the push services need a way to reach whoever runs the app (set PUBLIC_APP_URL to an https address, or PUSH_CONTACT, to turn them on)", "")
	default:
		r.add(OK, "notifications on devices are on (the page needs HTTPS in people's browsers)", "")
	}
}

// backups reports what is set about backups. Without a store key nothing is stored, so there is nothing to say.
func backups(r *Report, in Input) {
	hasStore := firstOf(in.Env, "HANDOFF_STORE_KEY", "HANDOFF_STORE_KEY_FILE") != ""
	download := firstOf(in.Env, "HANDOFF_ADMIN_BACKUP_DOWNLOAD")
	switch {
	case download != "" && download != "true":
		r.add(Warn, "HANDOFF_ADMIN_BACKUP_DOWNLOAD is "+download+": only the value true switches the download on, so it is off",
			"Set it to true to offer Download backup on the administrator's page, or remove the line.")
	case download == "true" && !hasStore:
		r.add(Warn, "HANDOFF_ADMIN_BACKUP_DOWNLOAD is true but HANDOFF_STORE_KEY is not set, so nothing is stored and there is nothing to download",
			"Set HANDOFF_STORE_KEY (see the installation guide), or remove HANDOFF_ADMIN_BACKUP_DOWNLOAD.")
	case download == "true":
		r.add(OK, "an Immich administrator can download a backup from the administrator's page (HANDOFF_ADMIN_BACKUP_DOWNLOAD=true)",
			"")
	}
	if !hasStore {
		return
	}
	every, keep, err := backup.ParseSchedule(in.Env)
	switch {
	case err != nil:
		r.add(Fail, err.Error(), "See the Settings page in docs/settings.md.")
	case every == 0:
		r.add(Warn, "automatic backups are off (BACKUP_EVERY=off), so a backup exists only when you run `immich-handoff backup`",
			"Remove BACKUP_EVERY to make one every day, or keep making them yourself.")
	default:
		r.add(OK, fmt.Sprintf("automatic backups: every %s, keeping the newest %d", every, keep), "")
	}
}

func firstOf(env func(string) string, names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(env(n)); v != "" {
			return v
		}
	}
	return ""
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "::1" || strings.HasPrefix(host, "127.") || host == "0.0.0.0"
}

func immichReachable(ctx context.Context, r *Report, in Input) bool {
	r.section("Immich")
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	major, minor, err := in.Probe.ServerVersion(cctx)
	if err != nil {
		r.add(Fail, "cannot reach Immich at "+in.BaseURL+": "+err.Error(),
			"Check IMMICH_BASE_URL, that this container is on Immich's Docker network (docker network ls; IMMICH_NETWORK in .env), and that Immich is running.")
		return false
	}
	if major < 3 || (major == 3 && minor < 2) {
		r.add(Fail, fmt.Sprintf("Immich is %d.%d; tagging a photo can only start a hand-over from 3.2.0", major, minor),
			"Update Immich to 3.2.0 or newer. Until then photos move only at startup and once a day.")
	} else {
		r.add(OK, fmt.Sprintf("reached Immich %d.%d at %s", major, minor, in.BaseURL), "")
	}
	return true
}

// webhookReachable calls the app's own webhook address the way Immich would. From inside this container it
// proves the address leads to this app and that the secret matches; it cannot prove Immich's own network can
// reach it, which is what the workflow runs below show.
func webhookReachable(ctx context.Context, r *Report, in Input) {
	if in.Hook.URL == "" || unusableURL(in.Hook.URL) {
		return
	}
	r.section("Webhook")
	hc := in.Client
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, in.Hook.URL, bytes.NewReader([]byte(`{"handoffTest":true}`)))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(in.Hook.Header, in.Hook.Secret)
	res, err := hc.Do(req)
	if err != nil {
		r.add(Warn, "this container could not call "+in.Hook.URL+": "+err.Error(),
			"If this check runs where the address is not meant to work (it is the name on Immich's network, seen from elsewhere), this is fine; the workflow runs below tell the truth.\nOtherwise check the address, the proxy, and that the app is running.")
		return
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	switch {
	case res.StatusCode == http.StatusOK || res.StatusCode == http.StatusAccepted:
		r.add(OK, "the webhook address answers and accepts the secret", "")
	case res.StatusCode == http.StatusUnauthorized:
		r.add(Fail, "the webhook address answered 401: the secret was not accepted",
			"Something answers there but it is not this app with this WEBHOOK_SECRET (or WEBHOOK_HEADER). Check the proxy does not drop or rename the header "+in.Hook.Header+".")
	default:
		r.add(Fail, fmt.Sprintf("the webhook address answered %s, not the app", res.Status),
			"The address does not lead to this app's /webhook/immich. Check PUBLIC_WEBHOOK_URL and any proxy in front of the app.")
	}
}

// unusableURL is an address the settings check already reported, so calling it would only repeat the news.
func unusableURL(u string) bool {
	pu, err := url.Parse(u)
	return err != nil || pu.Host == "" || (pu.Scheme != "http" && pu.Scheme != "https") || isLoopback(pu.Hostname())
}

func services(ctx context.Context, r *Report, in Input) {
	if len(in.Services) == 0 {
		r.section("Services")
		r.add(Warn, "no services yet", "Create one on the page, or define them in the routes file.")
		return
	}
	for _, svc := range in.Services {
		r.section("Service " + svc.Label())
		if svc.Broken != "" {
			fix := "Its owner presses Reconnect on the page."
			if svc.BrokenFix != "" {
				fix = svc.BrokenFix
			}
			r.add(Fail, "the service is paused: "+svc.Broken, fix)
			continue
		}
		for _, rc := range svc.Receivers {
			want := immich.ReceiverPermissions
			if svc.Album != "" {
				want = immich.ReceiverPermissionsAlbum
			}
			if account(ctx, r, rc, "receiver", want) {
				deletePermission(ctx, r, rc, in.Env == nil || in.Env("RECEIVER_DELETE") != "off")
			}
			tags(ctx, r, rc, "receiver")
		}
		if rm, ok := svc.Remote.(*bridge.Remote); ok {
			remote(ctx, r, rm)
		}
		if len(svc.Senders) == 0 {
			r.add(OK, "no senders have joined yet", "")
		}
		for _, snd := range svc.Senders {
			want := immich.SenderPermissions
			if svc.CopiesFor(snd) {
				want = immich.SenderPermissionsCopy
			}
			if !account(ctx, r, snd, "sender", want) {
				continue
			}
			tags(ctx, r, snd, "sender")
			workflow(ctx, r, in, svc, snd)
		}
	}
}

// deletePermission says whether a receiver key has what RECEIVER_DELETE asks of it. On (the default), it should be able to
// move copies to the owner's trash, which is what expiring copies and taking a hand-over back need; off, it should not.
// Neither is a failure: the app works either way, and a key made by the page is brought in line when its owner opens it.
func deletePermission(ctx context.Context, r *Report, a *syncer.Account, enabled bool) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	alive, have, err := a.API.KeyAlive(cctx)
	if err != nil || !alive {
		return // account() has said so
	}
	held := false
	for _, p := range have {
		if p == "asset.delete" || p == "all" {
			held = true
		}
	}
	who := "receiver " + a.Name
	switch {
	case enabled && !held:
		r.add(Warn, who+": the key may not move photos to the trash (asset.delete), so copies cannot expire and a hand-over cannot be taken back",
			"A person's key made by the page gets the permission when its owner next opens the page. For a key in .env or the routes file, add asset.delete (docs/fixed-accounts.md), or set RECEIVER_DELETE=off to stop asking for it.")
	case !enabled && held && !contains(have, "all"):
		r.add(Warn, who+": the key may move photos to the trash (asset.delete) although RECEIVER_DELETE=off",
			"The app does not use it. A key made by the page loses the permission when its owner next opens the page; for a key in .env or the routes file, remove asset.delete from it in Immich.")
	}
}

func contains(ps []immich.Permission, name string) bool {
	for _, p := range ps {
		if string(p) == name {
			return true
		}
	}
	return false
}

// optionalPermission is one the check can do without: it is only used to read a workflow's run log.
const optionalPermission = immich.Permission("workflow.logs")

// account checks a key works and holds every permission the app uses with it. False: it does not work.
func account(ctx context.Context, r *Report, a *syncer.Account, role string, want []immich.Permission) bool {
	who := role + " " + a.Name
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	alive, have, err := a.API.KeyAlive(cctx)
	if err != nil {
		r.add(Fail, who+": could not ask Immich about the key: "+err.Error(), "Check that Immich is reachable and healthy, then run the check again.")
		return false
	}
	if !alive {
		r.add(Fail, who+": Immich does not accept the API key (deleted or revoked)",
			"For a person who joined from the page: they press Clean up and join again (an owner presses Reconnect). For a key in .env or the routes file: create a new key and put it there.")
		return false
	}
	held := map[immich.Permission]bool{}
	for _, p := range have {
		held[p] = true
	}
	if held[immich.Permission("all")] {
		r.add(OK, who+": key works (all permissions)", "")
		return true
	}
	var missing, optional []string
	for _, p := range want {
		if held[p] || p == optionalPermission {
			continue
		}
		if note, ok := immich.Optional[p]; ok {
			optional = append(optional, fmt.Sprintf("%s (without it: %s)", p, note))
			continue
		}
		missing = append(missing, string(p))
	}
	if len(missing) > 0 {
		r.add(Fail, who+": the key lacks "+strings.Join(missing, ", "),
			"Create a new key with the full list in docs/fixed-accounts.md. A person who joined from the page can press Clean up and join again.")
		return false
	}
	if len(optional) > 0 {
		r.add(Warn, who+": the key works but lacks "+strings.Join(optional, "; "),
			"A person who joined from the page, or who owns the service, gets it by opening the page once. For a key made by hand, add it (docs/fixed-accounts.md).")
		return true
	}
	r.add(OK, who+": key works and has every permission it needs", "")
	return true
}

func tags(ctx context.Context, r *Report, a *syncer.Account, role string) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	on, err := a.API.TagsEnabled(cctx)
	if err != nil {
		return // a key may not be allowed to read preferences; the page checks this with the person's own session
	}
	if !on {
		r.add(Warn, role+" "+a.Name+": Tags is switched off in their Immich account, so they cannot add or see tags",
			"They open Immich -> Account Settings -> Features and turn on Tags, then refresh Immich. (Photos still move without it.)")
	}
}

// workflow checks the sender's workflow is as expected and looks at how its recent runs ended.
func workflow(ctx context.Context, r *Report, in Input, svc *syncer.Service, snd *syncer.Account) {
	if in.Hook.URL == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	who := "sender " + snd.Name
	tag, err := snd.API.FindTag(cctx, svc.Tag, false)
	if err != nil {
		r.add(Fail, who+": could not look for the tag "+svc.Tag+": "+err.Error(), "")
		return
	}
	if tag == nil {
		r.add(Fail, who+": the tag "+svc.Tag+" does not exist in their account, so the workflow cannot be right",
			"Restart the app: it creates the tag and the workflow at startup.")
		return
	}
	state, id, err := snd.API.CheckWorkflow(cctx, immich.WorkflowSpec{
		Name: svc.WorkflowName(), TagID: *tag, WebhookURL: in.Hook.URL, HeaderName: in.Hook.Header, HeaderSecret: in.Hook.Secret,
	})
	if err != nil {
		r.add(Fail, who+": could not read their workflows: "+err.Error(), "The key needs workflow.read, workflow.create and workflow.update.")
		return
	}
	switch state {
	case immich.WorkflowMissing:
		r.add(Fail, who+": the workflow "+svc.WorkflowName()+" does not exist, so tagging a photo does nothing until the next daily pass",
			"Restart the app: it creates it. If it is not created, the log says why (often a key without workflow.create).")
		return
	case immich.WorkflowDrifted:
		r.add(Fail, who+": the workflow "+svc.WorkflowName()+" is disabled or does not match the current settings (address, secret or tag)",
			"Restart the app: it repairs the workflow.")
		return
	}
	runs, err := snd.API.WorkflowRuns(cctx, *id, in.RunsToCheck)
	var he *immich.HTTPError
	if errors.As(err, &he) && he.Code == http.StatusForbidden {
		r.add(Warn, who+": the workflow is right, but this key may not read its runs, so whether Immich reaches the app cannot be seen here",
			"Give the key the permission workflow.logs in Immich (Account Settings -> API Keys). Keys made from now on have it. Optional: nothing else depends on it.")
		return
	}
	if err != nil {
		r.add(Warn, who+": the workflow is right, but its runs could not be read: "+err.Error(), "")
		return
	}
	if len(runs) == 0 {
		r.add(OK, who+": the workflow is in place; it has not run yet (tag a photo with "+svc.Tag+" and run this check again to see Immich reach the app)", "")
		return
	}
	bad := 0
	for _, run := range runs {
		if run.Result == "error" {
			bad++
		}
	}
	last := runs[0]
	switch {
	case last.Result == "error":
		step := ""
		if last.LastStep != "" {
			step = " at " + last.LastStep
		}
		r.add(Fail, fmt.Sprintf("%s: Immich's last run of the workflow failed%s (%s); %d of the last %d runs failed", who, step, ago(last.At), bad, len(runs)),
			"Immich could not deliver the notice to PUBLIC_WEBHOOK_URL. The address must be one Immich's SERVER container can reach (not localhost, not a name only your browser knows).\nIn Immich, open the workflow's run log for the exact error.")
	case bad > 0:
		r.add(Warn, fmt.Sprintf("%s: the last run worked (%s), but %d of the last %d runs failed", who, ago(last.At), bad, len(runs)),
			"Notices were lost for a while. If it keeps happening, check the address and that the app was reachable.")
	default:
		r.add(OK, fmt.Sprintf("%s: Immich's last %d workflow run(s) worked, latest %s", who, len(runs), ago(last.At)), "")
	}
}

func ago(t time.Time) string {
	d := time.Since(t).Round(time.Minute)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}

// remote checks a receiver on another Immich server: the address against the rules, the server's version, the exact
// permissions of the key, and that it can be reached. It says what is wrong in the same words the page uses.
func remote(ctx context.Context, r *Report, rm *bridge.Remote) {
	who := "receiver on another server (" + rm.Name + ")"
	info, err := rm.Diagnose(ctx)
	var f *bridge.Failure
	detail := ""
	if errors.As(err, &f) {
		detail = f.Detail
	}
	switch {
	case err == nil:
		takeBack := "; taking a hand-over back does not reach it (the key has no asset.delete)"
		if info.CanTakeBack {
			takeBack = "; taking a hand-over back reaches it (the key has asset.delete)"
		}
		r.add(OK, who+": the address is allowed, the server answers (Immich "+info.Version+") and the key has "+strings.Join(bridge.RequiredPermissions, ", ")+takeBack, "")
	case errors.Is(err, bridge.ErrBadAddress):
		r.add(Fail, who+": the address is not one the app may connect to ("+err.Error()+")",
			"Use the receiver's public https address. Only when both servers are yours, behind a VPN, set BRIDGE_ALLOW_PRIVATE=true.")
	case errors.Is(err, bridge.ErrRedirect):
		r.add(Fail, who+": the server answers with a redirect to "+detail+", which the app does not follow",
			"Ask the receiver for the address it redirects to, and connect again with an invitation.")
	case errors.Is(err, bridge.ErrNotImmich):
		r.add(Fail, who+": the address does not answer like an Immich server", "Check the address with the receiver.")
	case errors.Is(err, bridge.ErrVersion):
		r.add(Fail, who+": its Immich version ("+detail+") is not one this app was tested with",
			"Photos wait, still tagged. Ask the receiver to update or downgrade, or update this app.")
	case errors.Is(err, bridge.ErrKeyTooBroad):
		r.add(Fail, who+": the key can do more than it should ("+detail+"): it can read the receiver's library",
			bridge.PermissionsHelp()+"\nAsk the receiver for a new key, and connect again with an invitation.")
	case errors.Is(err, bridge.ErrKeyTooSmall):
		r.add(Fail, who+": the key cannot do everything a hand-over needs (it lacks "+detail+")",
			bridge.PermissionsHelp()+"\nAsk the receiver for a new key, and connect again with an invitation.")
	case errors.Is(err, bridge.ErrRevoked):
		r.add(Fail, who+": the other server does not accept the key (it was deleted or revoked)",
			"Photos wait, still tagged. Ask the receiver for a new key and connect again with an invitation, or remove the receiver on the service's card.")
	default:
		r.add(Fail, who+": cannot reach the server ("+err.Error()+")",
			"Photos wait, still tagged, and the app tries again with growing pauses. Check that the receiver's server is on and reachable from here.")
	}
}
