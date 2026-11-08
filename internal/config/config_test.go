package config

import (
	"strings"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/immich"
)

func env(m map[string]string) Env { return func(k string) string { return m[k] } }

func connect(key string) (*immich.API, error) {
	return immich.New("http://immich.invalid", immich.Auth{APIKey: key}, nil)
}

const good = `
accounts:
  alice:   { key: ${ALICE} }
  bob:     { key: ${BOB} }
  family:  { key: ${FAMILY} }
  grandma: { key: ${GRANDMA} }
services:
  - name: family
    tag: share
    senders: [alice, bob]
    receivers: [family]
  - name: grandma
    tag: for-grandma
    senders: [alice]
    receivers: [grandma, family]
    inbox_tag: from-alice
    inbox_expiry_days: 7
    mode: copy
    sent_tag: given
`

var goodEnv = map[string]string{"ALICE": "a", "BOB": "b", "FAMILY": "f", "GRANDMA": "g"}

func TestRoutesFile(t *testing.T) {
	svcs, err := parseRoutes([]byte(good), env(goodEnv), DefaultsFrom(env(nil)), connect, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 2 || svcs[0].Name != "family" || len(svcs[0].Senders) != 2 || len(svcs[1].Receivers) != 2 {
		t.Fatalf("unexpected services: %+v", svcs)
	}
	if svcs[0].InboxTag != "inbox" || svcs[0].ExpiryDays != 30 || svcs[0].RefusedTag != "share-duplicate" {
		t.Errorf("defaults not applied: %+v", svcs[0])
	}
	if svcs[1].InboxTag != "from-alice" || svcs[1].ExpiryDays != 7 || !svcs[1].Copies() || svcs[1].SentTag != "given" {
		t.Errorf("overrides not applied: %+v", svcs[1])
	}
	if !svcs[0].Copies() || svcs[0].Mode != "copy" || svcs[0].SentTag != "handed-over" {
		t.Errorf("copy is the default: %+v", svcs[0])
	}
	moved, err := parseRoutes([]byte(strings.Replace(good, "    tag: share\n", "    tag: share\n    mode: move\n", 1)), env(goodEnv), DefaultsFrom(env(nil)), connect, false)
	if err != nil || moved[0].Copies() {
		t.Errorf("a service can ask for move: %v %+v", err, moved)
	}
	if d := DefaultsFrom(env(map[string]string{"HANDOFF_MODE": "move"})); d.Mode != "move" {
		t.Errorf("HANDOFF_MODE=move must be honoured, got %q", d.Mode)
	}
	if svcs[0].WorkflowName() != "immich-handoff: family" {
		t.Errorf("workflow name = %q", svcs[0].WorkflowName())
	}
}

func TestRoutesProblems(t *testing.T) {
	cases := map[string]struct {
		yaml, want string
		env        map[string]string
	}{
		"unset variable":   {good, "GRANDMA is not set", map[string]string{"ALICE": "a", "BOB": "b", "FAMILY": "f"}},
		"unknown account":  {strings.Replace(good, "receivers: [grandma, family]", "receivers: [nobody]", 1), "unknown receiver account nobody", goodEnv},
		"same sender tag":  {strings.Replace(good, "tag: for-grandma", "tag: share", 1), `sender alice already uses tag "share"`, goodEnv},
		"sender=receiver":  {strings.Replace(good, "senders: [alice]", "senders: [grandma]", 1), "cannot be both sender and receiver", goodEnv},
		"no services":      {"accounts: {a: {key: x}}\n", "no services defined", goodEnv},
		"bad mode":         {strings.Replace(good, "mode: copy", "mode: keep", 1), "mode must be move or copy", goodEnv},
		"unknown yaml key": {good + "bogus: 1\n", "field bogus not found", goodEnv},
	}
	for name, c := range cases {
		_, err := parseRoutes([]byte(c.yaml), env(c.env), DefaultsFrom(env(nil)), connect, false)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v does not contain %q", name, err, c.want)
		}
	}
}

func TestOpenServiceMayStartWithoutSenders(t *testing.T) {
	y := "accounts: {f: {key: k}}\nservices:\n  - {name: open, tag: join, receivers: [f], allow_join: true}\n"
	svcs, err := parseRoutes([]byte(y), env(nil), DefaultsFrom(env(nil)), connect, false)
	if err != nil || !svcs[0].AllowJoin || len(svcs[0].Senders) != 0 {
		t.Fatalf("open service: %v %+v", err, svcs)
	}
	closed := strings.Replace(y, ", allow_join: true", "", 1)
	if _, err := parseRoutes([]byte(closed), env(nil), DefaultsFrom(env(nil)), connect, false); err == nil || !strings.Contains(err.Error(), "allow_join") {
		t.Errorf("a closed service without senders must be rejected, got %v", err)
	}
}

func TestNoConfiguredServicesIsFineWithSelfService(t *testing.T) {
	svcs, err := Load(env(map[string]string{"HANDOFF_STORE_KEY": "a-long-enough-key-123"}), connect)
	if err != nil || len(svcs) != 0 {
		t.Errorf("self-service install with nothing configured: %v %v", svcs, err)
	}
	if _, err := Load(env(nil), connect); err == nil {
		t.Error("without self-service, missing keys stay an error")
	}
}

func TestPlainEnvMakesOneDefaultService(t *testing.T) {
	svcs, err := Load(env(map[string]string{"IMMICH_SOURCE_API_KEYS": "a, b", "IMMICH_DEST_API_KEY": "f", "SHARE_TAG": "send"}), connect)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 || svcs[0].Name != "" || svcs[0].Tag != "send" || len(svcs[0].Senders) != 2 || len(svcs[0].Receivers) != 1 {
		t.Fatalf("unexpected: %+v", svcs[0])
	}
	if svcs[0].WorkflowName() != "immich-handoff" {
		t.Errorf("default service must keep the original workflow name, got %q", svcs[0].WorkflowName())
	}
	if _, err := Load(env(nil), connect); err == nil {
		t.Error("missing keys must be an error")
	}
}

func TestAlbumInTheRoutesFileAndTheEnvironment(t *testing.T) {
	routes := strings.Replace(good, "    receivers: [family]\n", "    receivers: [family]\n    album: Arrivals\n", 1)
	svcs, err := parseRoutes([]byte(routes), env(goodEnv), DefaultsFrom(env(nil)), connect, false)
	if err != nil {
		t.Fatal(err)
	}
	if svcs[0].Album != "Arrivals" || svcs[1].Album != "" {
		t.Errorf("album belongs to the service that names it: %q, %q", svcs[0].Album, svcs[1].Album)
	}
	long := strings.Replace(good, "    receivers: [family]\n", "    receivers: [family]\n    album: "+strings.Repeat("x", 101)+"\n", 1)
	if _, err := parseRoutes([]byte(long), env(goodEnv), DefaultsFrom(env(nil)), connect, false); err == nil || !strings.Contains(err.Error(), "album") {
		t.Errorf("a 101-character album name must be refused: %v", err)
	}
	if d := DefaultsFrom(env(map[string]string{"ALBUM": " Holiday "})); d.Album != "Holiday" {
		t.Errorf("ALBUM = %q", d.Album)
	}
}

func TestQuietHoursAndAcceptRulesInTheRoutesFile(t *testing.T) {
	routes := `
accounts:
  alice:  { key: ${ALICE} }
  family: { key: ${FAMILY} }
services:
  - name: frame
    tag: frame
    senders: [alice]
    receivers: [family]
    quiet_hours: "01:00-06:00"
    quiet_large_mb: 200
    accept:
      min_rating: 4
      photos_only: true
      orientation: Landscape
      max_mb: 20
  - name: plain
    tag: plain
    senders: [alice]
    receivers: [family]
`
	svcs, err := parseRoutes([]byte(routes), env(goodEnv), DefaultsFrom(env(nil)), connect, false)
	if err != nil {
		t.Fatal(err)
	}
	f := svcs[0]
	if f.Quiet.From != 60 || f.Quiet.To != 360 || f.Quiet.LargeMB != 200 {
		t.Errorf("quiet hours: %+v", f.Quiet)
	}
	if f.Accept.MinRating != 4 || !f.Accept.PhotosOnly || f.Accept.Orientation != "landscape" || f.Accept.MaxMB != 20 {
		t.Errorf("accept rules: %+v", f.Accept)
	}
	if svcs[1].Quiet.Set() || svcs[1].Accept.Active() {
		t.Errorf("a service with no settings has no rules: %+v %+v", svcs[1].Quiet, svcs[1].Accept)
	}
}

func TestBadQuietHoursAndAcceptRulesAreReportedWithTheirService(t *testing.T) {
	routes := `
accounts:
  alice:  { key: ${ALICE} }
  family: { key: ${FAMILY} }
services:
  - name: one
    tag: one
    senders: [alice]
    receivers: [family]
    quiet_hours: "night"
  - name: two
    tag: two
    senders: [alice]
    receivers: [family]
    accept: { min_rating: 9 }
  - name: three
    tag: three
    senders: [alice]
    receivers: [family]
    accept: { orientation: square }
`
	_, err := parseRoutes([]byte(routes), env(goodEnv), DefaultsFrom(env(nil)), connect, false)
	if err == nil || !strings.Contains(err.Error(), "quiet_hours") || !strings.Contains(err.Error(), "minimum rating") || !strings.Contains(err.Error(), "orientation") {
		t.Errorf("each problem must be reported: %v", err)
	}
}

func TestTheAppWideQuietHoursAndThePlainInstallsRulesComeFromTheEnvironment(t *testing.T) {
	q, err := QuietFrom(env(map[string]string{"QUIET_HOURS": "22:00-06:00", "QUIET_LARGE_MB": "100"}))
	if err != nil || q.From != 22*60 || q.To != 360 || q.LargeMB != 100 {
		t.Errorf("%+v %v", q, err)
	}
	if q, err := QuietFrom(env(nil)); err != nil || q.Set() {
		t.Errorf("nothing set is no window: %+v %v", q, err)
	}
	for _, bad := range []map[string]string{{"QUIET_HOURS": "late"}, {"QUIET_HOURS": "01:00-06:00", "QUIET_LARGE_MB": "big"}} {
		if _, err := QuietFrom(env(bad)); err == nil {
			t.Errorf("%v must be refused", bad)
		}
	}
	svcs, err := Load(env(map[string]string{"IMMICH_DEST_API_KEY": "d", "IMMICH_SOURCE_API_KEYS": "s", "ACCEPT_MIN_RATING": "3", "ACCEPT_PHOTOS_ONLY": "true", "ACCEPT_ORIENTATION": "portrait", "ACCEPT_MAX_MB": "50"}), connect)
	if err != nil {
		t.Fatal(err)
	}
	if a := svcs[0].Accept; a.MinRating != 3 || !a.PhotosOnly || a.Orientation != "portrait" || a.MaxMB != 50 {
		t.Errorf("the plain install's rules: %+v", a)
	}
	if _, err := Load(env(map[string]string{"IMMICH_DEST_API_KEY": "d", "IMMICH_SOURCE_API_KEYS": "s", "ACCEPT_MIN_RATING": "8"}), connect); err == nil {
		t.Error("a rating of 8 must be refused")
	}
}
