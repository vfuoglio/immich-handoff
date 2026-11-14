package doctor

import (
	"bytes"
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
	if out, r := sharedReport(map[string]string{"SHARED_ALBUMS_ENABLE": "true"}); r.Fails != 1 || !strings.Contains(out, "PUBLIC_APP_URL is not set") {
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
