package dest

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Kinds are the kinds of destination a file or a person may name. A kind that has no builder in this version is accepted (the
// file stays valid) and says so when it is used.
var Kinds = []string{"folder", "webdav", "s3", "sftp"}

// required are the settings each kind needs; one of a group (a|b) is enough.
var required = map[string][]string{
	"folder": {"root"},
	"webdav": {"url"},
	"s3":     {"bucket", "access_key", "secret_key"},
	"sftp":   {"host", "user", "password|private_key"},
}

// SecretKeys are the settings that are secrets: they are sealed when a person enters them and never shown again.
var SecretKeys = map[string]bool{"password": true, "secret_key": true, "private_key": true, "access_key": true, "passphrase": true}

// Spec is a destination as the administrator names it or a person enters it: a name, a kind and the kind's settings.
type Spec struct {
	Name     string
	Kind     string
	Settings map[string]string
}

// Net is what a builder needs to know about the network: private addresses (a NAS on the LAN) are refused unless the administrator allows them.
type Net struct{ AllowPrivate bool }

// Builder makes the Destination of a spec.
type Builder func(s Spec, n Net) (Destination, error)

var builders = map[string]Builder{}

// Register makes a kind available (each kind registers itself).
func Register(kind string, b Builder) { builders[kind] = b }

// ErrKindUnavailable: this version of the app cannot write to that kind yet.
var ErrKindUnavailable = errors.New("this version of the app cannot hand over to that kind of destination yet")

// Build makes the Destination of a spec, or says why it cannot.
func Build(s Spec, n Net) (Destination, error) {
	b, ok := builders[s.Kind]
	if !ok {
		return nil, fmt.Errorf("%w (%s)", ErrKindUnavailable, s.Kind)
	}
	return b(s, n)
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,39}$`)

// ValidName says whether a destination's name is one the app accepts: letters, digits, spaces, dots, dashes and underscores, up to 40.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// Validate checks that a spec has a known kind and what the kind needs; the problem names the setting.
func (s Spec) Validate() error {
	if !ValidName(s.Name) {
		return errors.New("a name is 1 to 40 letters, digits, spaces, dots, dashes or underscores")
	}
	need, ok := required[s.Kind]
	if !ok {
		return fmt.Errorf("the kind %q is not one of %s", s.Kind, strings.Join(Kinds, ", "))
	}
	for _, group := range need {
		any := false
		for _, k := range strings.Split(group, "|") {
			if strings.TrimSpace(s.Settings[k]) != "" {
				any = true
			}
		}
		if !any {
			return fmt.Errorf("a %s destination needs %s", s.Kind, strings.ReplaceAll(group, "|", " or "))
		}
	}
	if s.Kind == "folder" && !strings.HasPrefix(s.Settings["root"], "/") {
		return errors.New("a folder's root must be an absolute path such as /destinations/nas")
	}
	return nil
}

var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ParseFile reads the administrator's file of named destinations:
//
//	destinations:
//	  nas:
//	    kind: folder
//	    root: /destinations/nas
//	  cloud:
//	    kind: s3
//	    bucket: photos
//	    access_key: ${S3_KEY}
//	    secret_key_file: /run/secrets/s3_secret
//
// ${VAR} is replaced by the environment's value (an unset one is a problem), and a setting named x_file is replaced by x with the content of
// that file. Every problem is reported, not only the first.
func ParseFile(raw []byte, env func(string) string, readFile func(string) ([]byte, error)) (map[string]Spec, error) {
	if readFile == nil {
		readFile = os.ReadFile
	}
	var problems []string
	expanded := varRef.ReplaceAllStringFunc(string(raw), func(m string) string {
		v := env(m[2 : len(m)-1])
		if v == "" {
			problems = append(problems, "environment variable "+m[2:len(m)-1]+" is not set")
		}
		return v
	})
	var f struct {
		Destinations map[string]map[string]string `yaml:"destinations"`
	}
	dec := yaml.NewDecoder(strings.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("destinations file: %w", err)
	}
	out := map[string]Spec{}
	names := make([]string, 0, len(f.Destinations))
	for n := range f.Destinations {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		raw := f.Destinations[name]
		s := Spec{Name: name, Kind: strings.ToLower(strings.TrimSpace(raw["kind"])), Settings: map[string]string{}}
		for k, v := range raw {
			if k == "kind" {
				continue
			}
			if base, ok := strings.CutSuffix(k, "_file"); ok {
				b, err := readFile(v)
				if err != nil {
					problems = append(problems, fmt.Sprintf("destination %s: %s: %v", name, k, err))
					continue
				}
				s.Settings[base] = strings.TrimRight(string(b), "\r\n")
				continue
			}
			s.Settings[k] = strings.TrimSpace(v)
		}
		if err := s.Validate(); err != nil {
			problems = append(problems, "destination "+name+": "+err.Error())
			continue
		}
		out[name] = s
	}
	if len(problems) > 0 {
		return nil, errors.New("destinations file: " + strings.Join(problems, "; "))
	}
	return out, nil
}
