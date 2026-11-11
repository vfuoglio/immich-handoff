package notify

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

type fileDest struct {
	Name   string   `yaml:"name"`
	Format string   `yaml:"format"`
	URL    string   `yaml:"url"`
	Auth   string   `yaml:"auth"`
	Kinds  []string `yaml:"kinds"`
}

type file struct {
	Destinations []fileDest `yaml:"destinations"`
}

// FromEnv reads the destinations: NOTIFY_URL (one ntfy destination taking every kind, as it always did) and the
// file named by NOTIFY_FILE (more, each with a format and the kinds it takes). Secrets in the file are written as
// ${VARIABLE} and read from the environment, as in the routes file.
func FromEnv(getenv func(string) string) ([]Destination, error) {
	var out []Destination
	if u := strings.TrimSpace(getenv("NOTIFY_URL")); u != "" {
		if err := checkURL(u); err != nil {
			return nil, fmt.Errorf("NOTIFY_URL: %w", err)
		}
		out = append(out, Destination{Name: "NOTIFY_URL", Format: FormatNtfy, URL: u})
	}
	if path := getenv("NOTIFY_FILE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("NOTIFY_FILE: %w", err)
		}
		more, err := parse(raw, getenv, out)
		if err != nil {
			return nil, fmt.Errorf("NOTIFY_FILE: %w", err)
		}
		out = append(out, more...)
	}
	return out, nil
}

func parse(raw []byte, getenv func(string) string, have []Destination) ([]Destination, error) {
	var problems []string
	expanded := varRef.ReplaceAllStringFunc(string(raw), func(m string) string {
		name := m[2 : len(m)-1]
		v := getenv(name)
		if v == "" {
			problems = append(problems, "environment variable "+name+" is not set")
		}
		return v
	})
	var f file
	dec := yaml.NewDecoder(strings.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, d := range have {
		seen[d.Name] = true
	}
	var out []Destination
	for i, fd := range f.Destinations {
		name := fd.Name
		if name == "" {
			problems = append(problems, fmt.Sprintf("destination %d has no name", i+1))
			continue
		}
		if seen[name] {
			problems = append(problems, "destination "+name+" is named twice")
		}
		seen[name] = true
		format := fd.Format
		if format == "" {
			format = FormatNtfy
		}
		if format != FormatNtfy && format != FormatJSON {
			problems = append(problems, fmt.Sprintf("destination %s: format %q is not %s or %s", name, format, FormatNtfy, FormatJSON))
		}
		if err := checkURL(fd.URL); err != nil {
			problems = append(problems, "destination "+name+": "+err.Error())
		}
		kinds := map[Kind]bool{}
		for _, k := range fd.Kinds {
			if !known(Kind(k)) {
				problems = append(problems, fmt.Sprintf("destination %s: kind %q is not one of %s", name, k, kindList()))
				continue
			}
			kinds[Kind(k)] = true
		}
		out = append(out, Destination{Name: name, Format: format, URL: fd.URL, Auth: fd.Auth, Kinds: kinds})
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "; "))
	}
	return out, nil
}

func checkURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("the address must be an http or https URL")
	}
	return nil
}

func known(k Kind) bool {
	for _, x := range Kinds {
		if x == k {
			return true
		}
	}
	return false
}

func kindList() string {
	var s []string
	for _, k := range Kinds {
		s = append(s, string(k))
	}
	return strings.Join(s, ", ")
}
