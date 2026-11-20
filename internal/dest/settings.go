package dest

import (
	"fmt"
	"os"
	"strings"
)

// Settings are the administrator's: the destinations named in DESTINATIONS_FILE, whether people may add network destinations of their own
// (DESTINATIONS_USER_ADD) and whether addresses on a private network are allowed (DESTINATIONS_ALLOW_PRIVATE).
type Settings struct {
	Specs        map[string]Spec
	UserAdd      bool
	AllowPrivate bool
}

func on(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// SettingsFrom reads the settings from the environment, and the file it names. A file that cannot be read or has problems is an error: the
// app does not start with a destination it would silently not have.
func SettingsFrom(env func(string) string) (Settings, error) {
	s := Settings{UserAdd: on(env("DESTINATIONS_USER_ADD")), AllowPrivate: on(env("DESTINATIONS_ALLOW_PRIVATE"))}
	path := strings.TrimSpace(env("DESTINATIONS_FILE"))
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return s, fmt.Errorf("DESTINATIONS_FILE: %w", err)
	}
	specs, err := ParseFile(raw, env, nil)
	if err != nil {
		return s, fmt.Errorf("DESTINATIONS_FILE: %w", err)
	}
	s.Specs = specs
	return s, nil
}
