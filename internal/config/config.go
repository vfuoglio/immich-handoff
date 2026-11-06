// Package config turns the environment (and an optional routes file) into services.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// Defaults for options a service does not set itself.
type Defaults struct {
	Tag        string
	InboxTag   string
	RefusedTag string
	SentTag    string
	Mode       string
	ExpiryDays int
	Album      string // ALBUM: used by the single service of a plain .env install only
}

// Env reads a variable (os.Getenv in production, a map in tests).
type Env func(string) string

func DefaultsFrom(env Env) Defaults {
	d := Defaults{Tag: "share", InboxTag: "inbox", RefusedTag: "share-duplicate", SentTag: "handed-over", Mode: syncer.ModeCopy, ExpiryDays: 30}
	if v := env("SHARE_TAG"); v != "" {
		d.Tag = v
	}
	if v := env("INBOX_TAG"); v != "" {
		d.InboxTag = v
	}
	if v := env("REFUSED_TAG"); v != "" {
		d.RefusedTag = v
	}
	if v := env("SENT_TAG"); v != "" {
		d.SentTag = v
	}
	d.Album = strings.TrimSpace(env("ALBUM"))
	if v := env("HANDOFF_MODE"); v == syncer.ModeMove || v == syncer.ModeCopy {
		d.Mode = v
	}
	if v := env("INBOX_EXPIRY_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			d.ExpiryDays = n
		}
	}
	return d
}

// Connect builds an API client for a key.
type Connect func(key string) (*immich.API, error)

// Load builds the services. With ROUTES_FILE set, the file defines them; otherwise the plain
// IMMICH_SOURCE_API_KEYS / IMMICH_DEST_API_KEY variables define one default service.
func Load(env Env, connect Connect) ([]*syncer.Service, error) {
	d := DefaultsFrom(env)
	// With self-service on (a store key is set) the app may start with no configured services at all:
	// everything is then created from the web page.
	selfService := env("HANDOFF_STORE_KEY") != "" || env("HANDOFF_STORE_KEY_FILE") != ""
	if path := env("ROUTES_FILE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("routes file: %w", err)
		}
		return parseRoutes(raw, env, d, connect, selfService)
	}
	return fromEnv(env, d, connect, selfService)
}

func fromEnv(env Env, d Defaults, connect Connect, selfService bool) ([]*syncer.Service, error) {
	dest := env("IMMICH_DEST_API_KEY")
	var srcKeys []string
	for _, k := range strings.Split(env("IMMICH_SOURCE_API_KEYS"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			srcKeys = append(srcKeys, k)
		}
	}
	if dest == "" && len(srcKeys) == 0 && selfService {
		return nil, nil
	}
	if dest == "" || len(srcKeys) == 0 {
		return nil, errors.New("IMMICH_DEST_API_KEY and IMMICH_SOURCE_API_KEYS are required (or set ROUTES_FILE)")
	}
	svc := &syncer.Service{Tag: d.Tag, InboxTag: d.InboxTag, RefusedTag: d.RefusedTag, SentTag: d.SentTag, Mode: d.Mode, ExpiryDays: d.ExpiryDays, AllowJoin: env("ALLOW_JOIN") == "true", Album: d.Album}
	if err := checkAlbum(d.Album); err != nil {
		return nil, fmt.Errorf("ALBUM: %w", err)
	}
	for i, k := range srcKeys {
		api, err := connect(k)
		if err != nil {
			return nil, err
		}
		svc.Senders = append(svc.Senders, &syncer.Account{Name: fmt.Sprintf("sender-%d", i+1), API: api})
	}
	api, err := connect(dest)
	if err != nil {
		return nil, err
	}
	svc.Receivers = []*syncer.Account{{Name: "receiver", API: api}}
	return []*syncer.Service{svc}, nil
}

type routesFile struct {
	Accounts map[string]struct {
		Key string `yaml:"key"`
	} `yaml:"accounts"`
	Services []struct {
		Name       string   `yaml:"name"`
		Tag        string   `yaml:"tag"`
		Senders    []string `yaml:"senders"`
		Receivers  []string `yaml:"receivers"`
		InboxTag   string   `yaml:"inbox_tag"`
		RefusedTag string   `yaml:"refused_tag"`
		ExpiryDays int      `yaml:"inbox_expiry_days"`
		AllowJoin  bool     `yaml:"allow_join"`
		Mode       string   `yaml:"mode"`
		SentTag    string   `yaml:"sent_tag"`
		Album      string   `yaml:"album"`
	} `yaml:"services"`
}

var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func parseRoutes(raw []byte, env Env, d Defaults, connect Connect, selfService bool) ([]*syncer.Service, error) {
	var problems []string
	expanded := varRef.ReplaceAllStringFunc(string(raw), func(m string) string {
		name := m[2 : len(m)-1]
		v := env(name)
		if v == "" {
			problems = append(problems, "environment variable "+name+" is not set")
		}
		return v
	})
	var f routesFile
	dec := yaml.NewDecoder(strings.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("routes file: %w", err)
	}

	accounts := map[string]*syncer.Account{}
	for name, a := range f.Accounts {
		if a.Key == "" {
			problems = append(problems, "account "+name+" has no key")
			continue
		}
		api, err := connect(a.Key)
		if err != nil {
			return nil, err
		}
		accounts[name] = &syncer.Account{Name: name, API: api}
	}
	if len(f.Services) == 0 && !selfService {
		problems = append(problems, "no services defined")
	}

	var services []*syncer.Service
	names := map[string]bool{}
	senderTags := map[string]string{} // "sender\x00tag" -> service
	for i, s := range f.Services {
		where := fmt.Sprintf("service #%d", i+1)
		if s.Name != "" {
			where = "service " + s.Name
		}
		if s.Name == "" {
			problems = append(problems, where+": name is required")
		} else if names[s.Name] {
			problems = append(problems, where+": duplicate service name")
		}
		names[s.Name] = true
		svc := &syncer.Service{Name: s.Name, Tag: s.Tag, InboxTag: s.InboxTag, RefusedTag: s.RefusedTag, SentTag: s.SentTag, Mode: s.Mode, ExpiryDays: s.ExpiryDays, AllowJoin: s.AllowJoin, Album: strings.TrimSpace(s.Album)}
		if err := checkAlbum(svc.Album); err != nil {
			problems = append(problems, where+": album: "+err.Error())
		}
		if svc.Tag == "" {
			problems = append(problems, where+": tag is required")
		}
		if svc.InboxTag == "" {
			svc.InboxTag = d.InboxTag
		}
		if svc.RefusedTag == "" {
			svc.RefusedTag = d.RefusedTag
		}
		if svc.ExpiryDays <= 0 {
			svc.ExpiryDays = d.ExpiryDays
		}
		if svc.SentTag == "" {
			svc.SentTag = d.SentTag
		}
		switch svc.Mode {
		case "":
			svc.Mode = d.Mode
		case syncer.ModeMove, syncer.ModeCopy:
		default:
			problems = append(problems, where+": mode must be move or copy")
		}
		if len(s.Receivers) == 0 || (len(s.Senders) == 0 && !s.AllowJoin) {
			problems = append(problems, where+": needs at least one receiver, and at least one sender unless allow_join is true")
		}
		isSender := map[string]bool{}
		for _, n := range s.Senders {
			a, ok := accounts[n]
			if !ok {
				problems = append(problems, where+": unknown sender account "+n)
				continue
			}
			isSender[n] = true
			svc.Senders = append(svc.Senders, a)
			k := n + "\x00" + svc.Tag
			if other, dup := senderTags[k]; dup {
				problems = append(problems, fmt.Sprintf("%s: sender %s already uses tag %q for service %s", where, n, svc.Tag, other))
			}
			senderTags[k] = s.Name
		}
		for _, n := range s.Receivers {
			a, ok := accounts[n]
			if !ok {
				problems = append(problems, where+": unknown receiver account "+n)
				continue
			}
			if isSender[n] {
				problems = append(problems, where+": account "+n+" cannot be both sender and receiver")
				continue
			}
			svc.Receivers = append(svc.Receivers, a)
		}
		services = append(services, svc)
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("routes file has problems:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return services, nil
}

// checkAlbum accepts an empty name (no album) or a plain one of at most 100 characters.
func checkAlbum(name string) error {
	if utf8.RuneCountInString(name) > 100 || strings.ContainsAny(name, "\r\n\x00") {
		return errors.New("must be at most 100 characters and have no line breaks")
	}
	return nil
}
