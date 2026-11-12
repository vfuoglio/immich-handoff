package push

import "testing"

func TestTheContactForPushServicesIsChosenAndChecked(t *testing.T) {
	for _, tc := range []struct{ configured, app, want string }{
		{"", "https://handoff.example.com/", "https://handoff.example.com"},
		{"", "http://handoff.local", ""},
		{"", "", ""},
		{"mailto:admin@example.com", "https://handoff.example.com", "mailto:admin@example.com"},
		{"https://contact.example.com/me", "", "https://contact.example.com/me"},
	} {
		got, err := Contact(tc.configured, tc.app)
		if err != nil || got != tc.want {
			t.Errorf("%+v: %q %v", tc, got, err)
		}
	}
	for _, bad := range []string{"admin@example.com", "http://example.com", "mailto:", "https://", "ftp://x", "nonsense"} {
		if _, err := Contact(bad, ""); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
