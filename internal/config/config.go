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

	"gopkg.in/yaml.v3"

	"github.com/vfuoglio/immich-handoff/internal/immich"
	"github.com/vfuoglio/immich-handoff/internal/syncer"
)

// Defaults for options a service does not set itself.
type Defaults struct {
	Tag        string
	InboxTag   string
	RefusedTag string
	ExpiryDays int
}

// Env reads a variable (os.Getenv in production, a map in tests).
type Env func(string) string

func DefaultsFrom(env Env) Defaults {
	d := Defaults{Tag: "share", InboxTag: "inbox", RefusedTag: "share-duplicate", ExpiryDays: 30}
	if v := env("SHARE_TAG"); v != "" {
		d.Tag = v
	}
	if v := env("INBOX_TAG"); v != "" {
		d.InboxTag = v
	}
	if v := env("REFUSED_TAG"); v != "" {
		d.RefusedTag = v
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
	if path := env("ROUTES_FILE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("routes file: %w", err)
		}
		return parseRoutes(raw, env, d, connect)
	}
	return fromEnv(env, d, connect)
}

func fromEnv(env Env, d Defaults, connect Connect) ([]*syncer.Service, error) {
	dest := env("IMMICH_DEST_API_KEY")
	var srcKeys []string
	for _, k := range strings.Split(env("IMMICH_SOURCE_API_KEYS"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			srcKeys = append(srcKeys, k)
		}
	}
	if dest == "" || len(srcKeys) == 0 {
		return nil, errors.New("IMMICH_DEST_API_KEY and IMMICH_SOURCE_API_KEYS are required (or set ROUTES_FILE)")
	}
	svc := &syncer.Service{Tag: d.Tag, InboxTag: d.InboxTag, RefusedTag: d.RefusedTag, ExpiryDays: d.ExpiryDays}
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
	svc.Receivers = []*syncer.Account{{Name: "family", API: api}}
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
	} `yaml:"services"`
}

var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func parseRoutes(raw []byte, env Env, d Defaults, connect Connect) ([]*syncer.Service, error) {
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
	if len(f.Services) == 0 {
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
		svc := &syncer.Service{Name: s.Name, Tag: s.Tag, InboxTag: s.InboxTag, RefusedTag: s.RefusedTag, ExpiryDays: s.ExpiryDays}
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
		if len(s.Senders) == 0 || len(s.Receivers) == 0 {
			problems = append(problems, where+": needs at least one sender and one receiver")
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
