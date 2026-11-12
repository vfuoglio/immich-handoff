package mailin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestTheWayInIsOffWithoutAHostAndOtherwiseCheckedWhole(t *testing.T) {
	if _, on, err := SettingsFrom(env(nil)); on || err != nil {
		t.Errorf("off: %v %v", on, err)
	}
	if _, on, err := SettingsFrom(env(map[string]string{"MAIL_IN_USER": "x"})); on || err != nil {
		t.Errorf("settings without a host are not looked at: %v %v", on, err)
	}
	pw := filepath.Join(t.TempDir(), "pw")
	os.WriteFile(pw, []byte("from-a-file\n"), 0o600)
	s, on, err := SettingsFrom(env(map[string]string{"MAIL_IN_HOST": "imap.example.org", "MAIL_IN_USER": "drop", "MAIL_IN_PASSWORD_FILE": pw,
		"MAIL_IN_ADDRESS": "Drop <Drop@Example.org>", "MAIL_IN_AFTER": "SEEN", "MAIL_IN_EVERY": "30s", "MAIL_IN_MAX_MB": "10", "MAIL_IN_FOLDER": "Photos"}))
	if err != nil || !on {
		t.Fatal(err)
	}
	if s.Port != 993 || s.TLS != TLSImplicit || s.Password != "from-a-file" || s.Address != "drop@example.org" || s.After != AfterSeen || s.Every != 30*time.Second || s.MaxBytes != 10<<20 || s.Folder != "Photos" {
		t.Errorf("%+v", s)
	}
	if got := s.AddressFor("abc123xyz9"); got != "drop+abc123xyz9@example.org" {
		t.Errorf("%s", got)
	}
	d, _, _ := SettingsFrom(env(map[string]string{"MAIL_IN_HOST": "imap.example.org", "MAIL_IN_USER": "u", "MAIL_IN_PASSWORD": "p", "MAIL_IN_ADDRESS": "d@example.org", "MAIL_IN_TLS": "starttls"}))
	if d.Port != 143 || d.After != AfterDelete || d.Every != time.Minute || d.Folder != "INBOX" || d.MaxBytes != 25<<20 {
		t.Errorf("defaults: %+v", d)
	}
}

func TestEverySettingThatCouldBeWrongIsRefusedByName(t *testing.T) {
	base := map[string]string{"MAIL_IN_HOST": "imap.example.org", "MAIL_IN_USER": "u", "MAIL_IN_PASSWORD": "p", "MAIL_IN_ADDRESS": "d@example.org"}
	for want, change := range map[string]map[string]string{
		"not a host name":                        {"MAIL_IN_HOST": "imap example"},
		"only for a mail server on this machine": {"MAIL_IN_TLS": "none"},
		"is not tls":                             {"MAIL_IN_TLS": "ssl"},
		"not a port":                             {"MAIL_IN_PORT": "99999"},
		"both needed":                            {"MAIL_IN_PASSWORD": ""},
		"line breaks":                            {"MAIL_IN_USER": "u\nx"},
		"without a plus sign":                    {"MAIL_IN_ADDRESS": "d+x@example.org"},
		"MAIL_IN_ADDRESS":                        {"MAIL_IN_ADDRESS": "nonsense"},
		"not a folder name":                      {"MAIL_IN_FOLDER": "a\"b"},
		"MAIL_IN_AFTER":                          {"MAIL_IN_AFTER": "forget"},
		"MAIL_IN_EVERY":                          {"MAIL_IN_EVERY": "1s"},
		"MAIL_IN_MAX_MB":                         {"MAIL_IN_MAX_MB": "0"},
		"MAIL_IN_PASSWORD_FILE":                  {"MAIL_IN_PASSWORD": "", "MAIL_IN_PASSWORD_FILE": "/nonexistent/pw"},
	} {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range change {
			m[k] = v
		}
		if _, _, err := SettingsFrom(env(m)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: got %v", change, err)
		}
	}
	// without protection is allowed on this machine only
	m := map[string]string{"MAIL_IN_HOST": "127.0.0.1", "MAIL_IN_USER": "u", "MAIL_IN_PASSWORD": "p", "MAIL_IN_ADDRESS": "d@example.org", "MAIL_IN_TLS": "none"}
	if _, _, err := SettingsFrom(env(m)); err != nil {
		t.Errorf("loopback: %v", err)
	}
	m["MAIL_IN_HOST"] = "10.0.0.5"
	if _, _, err := SettingsFrom(env(m)); err == nil {
		t.Error("a mail server on the network without protection")
	}
}
