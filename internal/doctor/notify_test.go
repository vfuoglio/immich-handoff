package doctor

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func notifyReport(t *testing.T, env map[string]string) (string, *Report) {
	t.Helper()
	var buf bytes.Buffer
	r := &Report{W: &buf}
	notifications(r, Input{Env: func(k string) string { return env[k] }})
	return buf.String(), r
}

func TestTheNotificationSectionListsDestinationsWithoutTheirAddresses(t *testing.T) {
	file := filepath.Join(t.TempDir(), "notify.yaml")
	os.WriteFile(file, []byte("destinations:\n  - {name: home, format: json, url: 'https://ha.example/api/webhook/SECRET-ID', kinds: [problem, backup]}\n"), 0o600)
	out, r := notifyReport(t, map[string]string{"NOTIFY_URL": "https://ntfy.example/SECRET-TOPIC", "NOTIFY_FILE": file})
	if r.Fails != 0 || r.Warns != 0 {
		t.Errorf("%d fails, %d warns:\n%s", r.Fails, r.Warns, out)
	}
	for _, want := range []string{"NOTIFY_URL: ntfy, takes every kind", "home: json, takes problem, backup"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SECRET") {
		t.Errorf("an address was printed:\n%s", out)
	}
}

func TestNoDestinationIsFineAndABadFileIsAFailure(t *testing.T) {
	if out, r := notifyReport(t, nil); r.Fails != 0 || !strings.Contains(out, "no destination is set") {
		t.Errorf("%s", out)
	}
	bad := filepath.Join(t.TempDir(), "notify.yaml")
	os.WriteFile(bad, []byte("destinations:\n  - {name: x, format: smoke, url: 'https://x.example'}\n"), 0o600)
	if out, r := notifyReport(t, map[string]string{"NOTIFY_FILE": bad}); r.Fails != 1 || !strings.Contains(out, "will not start") || !strings.Contains(out, "smoke") {
		t.Errorf("%s", out)
	}
	if _, r := notifyReport(t, map[string]string{"NOTIFY_URL": "not a url"}); r.Fails != 1 {
		t.Error("a bad NOTIFY_URL passed")
	}
}

func TestAServerForPeopleNeedsTheAddressAndTheStoreJustAsTheAppDoes(t *testing.T) {
	file := filepath.Join(t.TempDir(), "notify.yaml")
	os.WriteFile(file, []byte("destinations:\n  - {name: people, format: email, host: smtp.example.com, from: h@example.com, people: true}\n"), 0o600)
	out, r := notifyReport(t, map[string]string{"NOTIFY_FILE": file})
	if r.Fails != 2 || !strings.Contains(out, "PUBLIC_APP_URL") || !strings.Contains(out, "HANDOFF_STORE_KEY") {
		t.Errorf("%d fails:\n%s", r.Fails, out)
	}
	if out, r := notifyReport(t, map[string]string{"NOTIFY_FILE": file, "PUBLIC_APP_URL": "https://h.example", "HANDOFF_STORE_KEY": "a-long-enough-store-key"}); r.Fails != 0 {
		t.Errorf("%s", out)
	}
}

func TestPushIsReportedOnOffOrMisconfigured(t *testing.T) {
	key := "a-long-enough-store-key"
	for _, tc := range []struct {
		env  map[string]string
		want string
		fail bool
	}{
		{map[string]string{"HANDOFF_STORE_KEY": key, "PUBLIC_APP_URL": "https://h.example"}, "notifications on devices are on", false},
		{map[string]string{"HANDOFF_STORE_KEY": key, "PUSH_CONTACT": "mailto:a@example.com"}, "notifications on devices are on", false},
		{map[string]string{"HANDOFF_STORE_KEY": key}, "need a way to reach", false},
		{map[string]string{"HANDOFF_STORE_KEY": key, "PUBLIC_APP_URL": "http://h.local"}, "need a way to reach", false},
		{map[string]string{"HANDOFF_STORE_KEY": key, "PUBLIC_APP_URL": "https://h.example", "PUSH": "off"}, "PUSH=off", false},
		{nil, "cannot join from the page", false},
		{map[string]string{"HANDOFF_STORE_KEY": key, "PUSH_CONTACT": "admin@example.com"}, "PUSH_CONTACT must be", true},
	} {
		out, r := notifyReport(t, tc.env)
		if !strings.Contains(out, tc.want) || (r.Fails > 0) != tc.fail {
			t.Errorf("%v: want %q (fail %v):\n%s", tc.env, tc.want, tc.fail, out)
		}
	}
}
