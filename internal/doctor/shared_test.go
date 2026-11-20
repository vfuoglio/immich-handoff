package doctor

import (
	"bytes"
	"errors"
	"github.com/vfuoglio/immich-handoff/internal/dest"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sharedReport(env map[string]string) (string, *Report) {
	var buf bytes.Buffer
	r := &Report{W: &buf}
	sharedAlbums(r, Input{Env: func(k string) string { return env[k] }})
	return buf.String(), r
}

func TestSharingAlbumsIsCheckedOnlyWhenItIsOn(t *testing.T) {
	if out, r := sharedReport(nil); out != "" || r.Fails != 0 {
		t.Errorf("off: %q", out)
	}
	if out, r := sharedReport(map[string]string{"SHARED_ALBUMS_ENABLE": "true"}); r.Fails != 0 || r.Warns != 1 || !strings.Contains(out, "PUBLIC_APP_URL is not set") {
		t.Errorf("no address: %d\n%s", r.Fails, out)
	}
	if out, r := sharedReport(map[string]string{"SHARED_ALBUMS_ENABLE": "on", "PUBLIC_APP_URL": "https://handoff.example.com"}); r.Fails != 0 || r.Warns != 0 || !strings.Contains(out, "sharing albums is on") {
		t.Errorf("fine: %s", out)
	}
}

func TestTheAddressTheOtherAppIsToldMustBeOneItCanUse(t *testing.T) {
	if out, r := sharedReport(map[string]string{"SHARED_ALBUMS_ENABLE": "1", "PUBLIC_APP_URL": "http://handoff.example.com"}); r.Warns != 1 || !strings.Contains(out, "https://") {
		t.Errorf("plain http: %d\n%s", r.Warns, out)
	}
	if _, r := sharedReport(map[string]string{"SHARED_ALBUMS_ENABLE": "1", "PUBLIC_APP_URL": "http://10.0.0.5:8080", "BRIDGE_ALLOW_PRIVATE": "true"}); r.Warns != 0 || r.Fails != 0 {
		t.Error("private addresses are fine when both servers are the person's own")
	}
	if out, r := sharedReport(map[string]string{"SHARED_ALBUMS_ENABLE": "1", "PUBLIC_APP_URL": "https://localhost"}); r.Warns != 1 || !strings.Contains(out, "cannot reach") {
		t.Errorf("loopback: %s", out)
	}
	if _, r := sharedReport(map[string]string{"SHARED_ALBUMS_ENABLE": "1", "PUBLIC_APP_URL": "not an address"}); r.Fails != 1 {
		t.Error("not an address")
	}
	if _, r := sharedReport(map[string]string{"SHARED_ALBUMS_ENABLE": "1", "PUBLIC_APP_URL": "https://h.example", "SHARED_ALBUMS_EVERY_MINUTES": "0", "SHARED_ALBUMS_MAX_MB": "x"}); r.Fails != 2 {
		t.Errorf("bad numbers: %d", r.Fails)
	}
}

func TestTheRulesIntervalIsCheckedOnlyWhenSet(t *testing.T) {
	r := &Report{W: &bytes.Buffer{}}
	rulesSettings(r, Input{Env: func(k string) string { return map[string]string{}[k] }})
	if r.Fails != 0 {
		t.Error("unset is fine")
	}
	for v, fails := range map[string]int{"10": 0, "0.1": 0, "0": 1, "x": 1, "2000": 1} {
		r := &Report{W: &bytes.Buffer{}}
		rulesSettings(r, Input{Env: func(k string) string { return map[string]string{"RULES_CHECK_MINUTES": v}[k] }})
		if r.Fails != fails {
			t.Errorf("%s: %d fails", v, r.Fails)
		}
	}
}

func TestTheHandOverButtonSwitchIsCheckedOnlyWhenSet(t *testing.T) {
	for v, warns := range map[string]int{"": 0, "true": 0, "false": 0, "off": 0, "maybe": 1} {
		r := &Report{W: &bytes.Buffer{}}
		buttonSettings(r, Input{Env: func(k string) string { return map[string]string{"IMMICH_BUTTON_ACTIONS": v}[k] }})
		if r.Warns != warns || r.Fails != 0 {
			t.Errorf("%q: %d warnings, %d fails", v, r.Warns, r.Fails)
		}
	}
}

func TestTheOnThisDayIntervalIsCheckedOnlyWhenSet(t *testing.T) {
	check := func(env map[string]string) (string, *Report) {
		var buf bytes.Buffer
		r := &Report{W: &buf}
		memoriesSettings(r, Input{Env: func(k string) string { return env[k] }})
		return buf.String(), r
	}
	if out, r := check(nil); out != "" || r.Fails != 0 || r.Warns != 0 {
		t.Errorf("unset is fine and says nothing: %q", out)
	}
	if _, r := check(map[string]string{"MEMORIES_CHECK_MINUTES": "soon"}); r.Fails != 1 {
		t.Error("not a number")
	}
	if _, r := check(map[string]string{"MEMORIES_CHECK_MINUTES": "2000"}); r.Fails != 1 {
		t.Error("too long")
	}
	if out, r := check(map[string]string{"MEMORIES_CHECK_MINUTES": "5"}); r.Fails != 0 || r.Warns != 1 || !strings.Contains(out, "PUBLIC_IMMICH_URL") {
		t.Errorf("fine, but the link needs Immich's address: %s", out)
	}
	if _, r := check(map[string]string{"MEMORIES_CHECK_MINUTES": "5", "PUBLIC_IMMICH_URL": "https://photos.example"}); r.Fails != 0 || r.Warns != 0 {
		t.Error("all set")
	}
}

func TestThePetSidecarAddressIsCheckedOnlyWhenSetAndAnUnreachableOneIsAWarning(t *testing.T) {
	run := func(url string, client *http.Client) *Report {
		var buf bytes.Buffer
		r := &Report{W: &buf}
		petSidecar(r, Input{Env: func(k string) string { return map[string]string{"PET_SIDECAR_URL": url}[k] }, Client: client})
		return r
	}
	if r := run("", nil); r.Fails+r.Warns != 0 {
		t.Error("unset is fine")
	}
	if r := run("pets:8090", nil); r.Fails != 1 {
		t.Error("an address without http:// is refused, as the app would")
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer up.Close()
	if r := run(up.URL, up.Client()); r.Fails+r.Warns != 0 {
		t.Errorf("it answers: %d fails %d warnings", r.Fails, r.Warns)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := down.URL
	down.Close()
	if r := run(url, nil); r.Fails != 0 || r.Warns != 1 {
		t.Errorf("it does not answer: the app still works, so a warning: %d fails %d warnings", r.Fails, r.Warns)
	}
}

func TestTheDestinationsFileIsCheckedAndEachDestinationIsAskedWhetherItWorks(t *testing.T) {
	dest.Register("folder", func(s dest.Spec, n dest.Net) (dest.Destination, error) {
		if s.Settings["root"] == "/broken" {
			return &dest.Memory{Fail: errors.New("the share is not mounted")}, nil
		}
		return &dest.Memory{}, nil
	})
	run := func(file string, svcs ...*syncer.Service) (string, *Report) {
		var buf bytes.Buffer
		r := &Report{W: &buf}
		env := map[string]string{}
		if file != "" {
			p := filepath.Join(t.TempDir(), "d.yaml")
			os.WriteFile(p, []byte(file), 0o600)
			env["DESTINATIONS_FILE"] = p
		}
		destinations(r, Input{Env: func(k string) string { return env[k] }, Services: svcs})
		return buf.String(), r
	}
	if out, r := run(""); out != "" || r.Fails != 0 {
		t.Errorf("unset is fine: %q", out)
	}
	out, r := run("destinations:\n  nas:\n    kind: folder\n    root: /ok\n  broken:\n    kind: folder\n    root: /broken\n",
		&syncer.Service{Name: "family", DestRefs: []syncer.DestRef{{Name: "nas"}, {Name: "nope"}}})
	if r.Fails != 2 || r.Warns != 0 {
		t.Errorf("%d fails %d warnings\n%s", r.Fails, r.Warns, out)
	}
	for _, want := range []string{"destination nas: the memory destination answers", "destination broken: the share is not mounted", `names the destination nope`} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if _, r := run("destinations:\n  a:\n    kind: folder\n"); r.Fails != 1 {
		t.Error("a file with a problem is a failure")
	}
}
