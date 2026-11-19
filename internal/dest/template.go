package dest

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// DefaultTemplate is where a photo goes when its service's owner chose nothing: by sender, then by date.
const DefaultTemplate = "{service}/{sender}/{yyyy}/{mm}"

var placeholders = map[string]bool{"service": true, "sender": true, "yyyy": true, "mm": true, "dd": true, "name": true}

// ValidTemplate says whether a template is one the app accepts: placeholders from {service} {sender} {yyyy} {mm} {dd} {name}, written
// with "/" between folders, relative, up to 200 characters. What a placeholder stands for is cleaned when it is used, so a template
// cannot make a path leave the destination's root, whatever a sender's name or a file's name is.
func ValidTemplate(t string) error {
	t = strings.TrimSpace(t)
	if t == "" {
		return nil // the default
	}
	if utf8.RuneCountInString(t) > 200 {
		return errors.New("a template is up to 200 characters")
	}
	if strings.HasPrefix(t, "/") || strings.Contains(t, "\\") || strings.Contains(t, "..") {
		return errors.New("a template is a relative path with / between folders, and no ..")
	}
	rest := t
	for {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			break
		}
		j := strings.IndexByte(rest[i:], '}')
		if j < 0 {
			return errors.New("a { in the template has no }")
		}
		if name := rest[i+1 : i+j]; !placeholders[name] {
			return fmt.Errorf("{%s} is not a placeholder: use {service} {sender} {yyyy} {mm} {dd} {name}", name)
		}
		rest = rest[i+j+1:]
	}
	if strings.ContainsAny(rest, "}") {
		return errors.New("a } in the template has no {")
	}
	for _, r := range t {
		if unicode.IsControl(r) {
			return errors.New("a template has no control characters")
		}
	}
	return nil
}

// Vars is what a template's placeholders stand for.
type Vars struct {
	Service, Sender, Name string
	Taken                 time.Time
}

// Path is the file's path under the destination's root: the template with every placeholder replaced by a cleaned value, and the
// original file name added at the end unless the template has {name}. No part of it is empty, "." or "..", contains a separator or a
// character that some file systems refuse, or is longer than 120 characters, and the whole is at most 480.
func Path(tmpl string, v Vars) string {
	tmpl = strings.TrimSpace(tmpl)
	if tmpl == "" {
		tmpl = DefaultTemplate
	}
	if !strings.Contains(tmpl, "{name}") {
		tmpl = strings.TrimRight(tmpl, "/") + "/{name}"
	}
	taken := v.Taken
	if taken.IsZero() {
		taken = time.Now()
	}
	vals := map[string]string{
		"service": v.Service, "sender": v.Sender, "name": v.Name,
		"yyyy": fmt.Sprintf("%04d", taken.Year()), "mm": fmt.Sprintf("%02d", int(taken.Month())), "dd": fmt.Sprintf("%02d", taken.Day()),
	}
	var parts []string
	for _, seg := range strings.Split(tmpl, "/") {
		for k, val := range vals {
			seg = strings.ReplaceAll(seg, "{"+k+"}", cleanPart(val))
		}
		seg = cleanPart(seg)
		if seg != "" {
			parts = append(parts, seg)
		}
	}
	if len(parts) == 0 {
		parts = []string{"file"}
	}
	out := path.Join(parts...)
	if len(out) > 480 {
		out = out[len(out)-480:]
		out = strings.TrimLeft(out, "/ ")
	}
	return out
}

// cleanPart makes one piece of a path safe: no separators or characters file systems refuse, no control characters, no leading dots
// or trailing dots and spaces, not empty, at most 120 characters.
func cleanPart(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			return '_'
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
	s = strings.TrimLeft(s, ". ")
	s = strings.TrimRight(s, ". ")
	if utf8.RuneCountInString(s) > 120 {
		r := []rune(s)
		s = string(r[:120])
	}
	return s
}
