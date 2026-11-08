// Package i18n translates what people see on the pages. English is the source: a message is written in English where
// it is used (T("Sign in to Immich first")), and that English text is also its key in the catalogs. A language that has
// no translation for a message simply shows the English, so nothing is ever blank.
//
// Only the screen is translated. Logs, the setup check, notifications to whoever runs the app and the documentation stay
// in English, always.
package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"
)

// Lang is a language code. English is the source language; the others are translated from it.
type Lang string

const (
	English    Lang = "en"
	German     Lang = "de"
	French     Lang = "fr"
	Spanish    Lang = "es"
	Italian    Lang = "it"
	Portuguese Lang = "pt"
	Russian    Lang = "ru"
)

// All is every language the pages come in, English first.
var All = []Lang{English, German, French, Spanish, Italian, Portuguese, Russian}

// Names are the languages' own names, as they appear in the language menu.
var Names = map[Lang]string{English: "English", German: "Deutsch", French: "Français", Spanish: "Español", Italian: "Italiano", Portuguese: "Português", Russian: "Русский"}

//go:embed lang/*.json
var files embed.FS

type entry struct {
	text  string
	forms map[string]string // plural forms (one, few, many, other) when the message has them
}

var catalogs = map[Lang]map[string]entry{}

func init() {
	for _, l := range All {
		if l == English {
			continue
		}
		raw, err := files.ReadFile("lang/" + string(l) + ".json")
		if err != nil {
			panic("i18n: " + err.Error())
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			panic(fmt.Sprintf("i18n: lang/%s.json: %v", l, err))
		}
		cat := make(map[string]entry, len(m))
		for id, v := range m {
			var s string
			if json.Unmarshal(v, &s) == nil {
				cat[id] = entry{text: s}
				continue
			}
			var forms map[string]string
			if err := json.Unmarshal(v, &forms); err != nil {
				panic(fmt.Sprintf("i18n: lang/%s.json: %q is neither text nor plural forms", l, id))
			}
			cat[id] = entry{forms: forms}
		}
		catalogs[l] = cat
	}
	for _, l := range All {
		locales[l] = &Locale{Lang: l, cat: catalogs[l]}
	}
}

// Locale is the pages' words in one language.
type Locale struct {
	Lang Lang
	cat  map[string]entry
}

// locales is filled by init, after the catalogs are read.
var locales = map[Lang]*Locale{}

// For returns the locale of a language (English for anything unknown).
func For(l Lang) *Locale {
	if loc, ok := locales[l]; ok {
		return loc
	}
	return locales[English]
}

type ctxKey struct{}

// With puts a locale in a context, where the templates and handlers find it.
func With(ctx context.Context, l *Locale) context.Context { return context.WithValue(ctx, ctxKey{}, l) }

// From is the locale in a context, English when there is none.
func From(ctx context.Context) *Locale {
	if l, ok := ctx.Value(ctxKey{}).(*Locale); ok {
		return l
	}
	return locales[English]
}

// Supported reports whether a code is one of the languages.
func Supported(code string) (Lang, bool) {
	for _, l := range All {
		if string(l) == code {
			return l, true
		}
	}
	return English, false
}

// Negotiate picks the language for a request: the person's own choice (the cookie) if it is one we have, else the best
// match in the browser's Accept-Language, else English.
func Negotiate(cookie, acceptLanguage string) Lang {
	if l, ok := Supported(strings.TrimSpace(cookie)); ok {
		return l
	}
	type pref struct {
		code string
		q    float64
	}
	var prefs []pref
	for _, part := range strings.Split(acceptLanguage, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		code := strings.ToLower(strings.TrimSpace(fields[0]))
		if code == "" || code == "*" {
			continue
		}
		q := 1.0
		for _, f := range fields[1:] {
			if v, ok := strings.CutPrefix(strings.TrimSpace(f), "q="); ok {
				if _, err := fmt.Sscanf(v, "%f", &q); err != nil {
					q = 0
				}
			}
		}
		if i := strings.IndexAny(code, "-_"); i > 0 {
			code = code[:i] // "pt-BR" is Portuguese
		}
		prefs = append(prefs, pref{code, q})
	}
	sort.SliceStable(prefs, func(i, j int) bool { return prefs[i].q > prefs[j].q })
	for _, p := range prefs {
		if p.q <= 0 {
			continue
		}
		if l, ok := Supported(p.code); ok {
			return l
		}
	}
	return English
}

func fill(msg string, escape bool, kv []any) string {
	for i := 0; i+1 < len(kv); i += 2 {
		v := fmt.Sprint(kv[i+1])
		if escape {
			v = html.EscapeString(v)
		}
		msg = strings.ReplaceAll(msg, "{"+fmt.Sprint(kv[i])+"}", v)
	}
	return msg
}

// T is a message in this language, with {name} placeholders filled from the key and value pairs.
func (l *Locale) T(msg string, kv ...any) string {
	if e, ok := l.cat[msg]; ok && e.text != "" {
		msg = e.text
	}
	return fill(msg, false, kv)
}

// H is T for a message that holds a little markup (<code>, <strong>): the message is ours and is used as it is, the values
// are escaped, so a name with a < in it cannot become markup.
func (l *Locale) H(msg string, kv ...any) string {
	if e, ok := l.cat[msg]; ok && e.text != "" {
		msg = e.text
	}
	return fill(msg, true, kv)
}

// N is a message with a count: one is the English singular (and the key), other its plural. {n} is filled with the count.
// The translation has the forms its language needs (Russian has three, for example).
func (l *Locale) N(one, other string, n int, kv ...any) string {
	msg := other
	if n == 1 {
		msg = one
	}
	if e, ok := l.cat[one]; ok && e.forms != nil {
		if f, ok := e.forms[Plural(l.Lang, n)]; ok {
			msg = f
		} else if f, ok := e.forms["other"]; ok {
			msg = f
		}
	}
	return fill(msg, false, append([]any{"n", n}, kv...))
}

// Plural is the plural category (one, few, many or other) of a whole number in a language, by the CLDR rules of the
// languages the pages come in.
func Plural(l Lang, n int) string {
	if n < 0 {
		n = -n
	}
	switch l {
	case French, Portuguese:
		if n == 0 || n == 1 {
			return "one"
		}
	case Russian:
		switch {
		case n%10 == 1 && n%100 != 11:
			return "one"
		case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
			return "few"
		}
		return "many"
	default: // English, German, Spanish, Italian
		if n == 1 {
			return "one"
		}
	}
	return "other"
}

// Has reports whether this language has its own text for a message (English always does: it is the message).
func (l *Locale) Has(msg string) bool {
	if l.Lang == English {
		return true
	}
	e, ok := l.cat[msg]
	return ok && (e.text != "" || len(e.forms) > 0)
}

var months = map[Lang][12]string{
	English:    {"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"},
	German:     {"Jan.", "Feb.", "März", "Apr.", "Mai", "Juni", "Juli", "Aug.", "Sep.", "Okt.", "Nov.", "Dez."},
	French:     {"janv.", "févr.", "mars", "avr.", "mai", "juin", "juil.", "août", "sept.", "oct.", "nov.", "déc."},
	Spanish:    {"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sept", "oct", "nov", "dic"},
	Italian:    {"gen", "feb", "mar", "apr", "mag", "giu", "lug", "ago", "set", "ott", "nov", "dic"},
	Portuguese: {"jan.", "fev.", "mar.", "abr.", "mai.", "jun.", "jul.", "ago.", "set.", "out.", "nov.", "dez."},
	Russian:    {"янв.", "февр.", "мар.", "апр.", "мая", "июн.", "июл.", "авг.", "сент.", "окт.", "нояб.", "дек."},
}

var weekdays = map[Lang][7]string{ // Sunday first, as time.Weekday counts
	English:    {"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
	German:     {"So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"},
	French:     {"dim.", "lun.", "mar.", "mer.", "jeu.", "ven.", "sam."},
	Spanish:    {"dom", "lun", "mar", "mié", "jue", "vie", "sáb"},
	Italian:    {"dom", "lun", "mar", "mer", "gio", "ven", "sab"},
	Portuguese: {"dom", "seg", "ter", "qua", "qui", "sex", "sáb"},
	Russian:    {"вс", "пн", "вт", "ср", "чт", "пт", "сб"},
}

// Stamp is a moment as the pages show it: day, month and the time ("2 Nov 15:04", "2. Nov. 15:04").
func (l *Locale) Stamp(t time.Time) string {
	day := fmt.Sprint(t.Day())
	if l.Lang == German {
		day += "."
	}
	return fmt.Sprintf("%s %s %02d:%02d", day, months[l.Lang][t.Month()-1], t.Hour(), t.Minute())
}

// Weekday is the short name of the day of the week.
func (l *Locale) Weekday(t time.Time) string { return weekdays[l.Lang][t.Weekday()] }

// Messages lists the messages a language has translated (for tests and tools).
func Messages(l Lang) []string {
	out := make([]string, 0, len(catalogs[l]))
	for id := range catalogs[l] {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Forms returns the plural forms a translation has, or nil when the message is plain text.
func Forms(l Lang, id string) map[string]string { return catalogs[l][id].forms }

// Text returns a plain translation, or "" when there is none.
func Text(l Lang, id string) string { return catalogs[l][id].text }

// Entry is a message's translation: text, or the plural forms of a counted message. It is for handing the messages that
// scripts use to the page.
func (l *Locale) Entry(msg string) (any, bool) {
	e, ok := l.cat[msg]
	switch {
	case !ok:
		return nil, false
	case e.text != "":
		return e.text, true
	case len(e.forms) > 0:
		return e.forms, true
	}
	return nil, false
}

// Code is the language's two-letter code, for <html lang>.
func (l *Locale) Code() string { return string(l.Lang) }

// Day marks a value that Pack should show as a date in the reader's language (a time.Time is shown as a date and a time).
type Day time.Time

// packed is what Pack stores: a message and its values, a counted message (N with its plural form O), or several
// packed messages joined by a separator (J, P).
type packed struct {
	M string            `json:"m,omitempty"`
	O string            `json:"o,omitempty"`
	N *int              `json:"n,omitempty"`
	A map[string]string `json:"a,omitempty"`
	T map[string]string `json:"t,omitempty"` // values that are moments (RFC 3339), shown as a date and a time
	D map[string]string `json:"d,omitempty"` // values that are days, shown as a date
	J string            `json:"j,omitempty"`
	P []string          `json:"p,omitempty"`
}

func fillPacked(p *packed, kv []any) {
	for i := 0; i+1 < len(kv); i += 2 {
		key := fmt.Sprint(kv[i])
		switch v := kv[i+1].(type) {
		case time.Time:
			if p.T == nil {
				p.T = map[string]string{}
			}
			p.T[key] = v.UTC().Format(time.RFC3339)
		case Day:
			if p.D == nil {
				p.D = map[string]string{}
			}
			p.D[key] = time.Time(v).UTC().Format(time.RFC3339)
		default:
			if p.A == nil {
				p.A = map[string]string{}
			}
			p.A[key] = fmt.Sprint(v)
		}
	}
}

func marshal(p packed) string {
	b, _ := json.Marshal(p)
	return string(b)
}

// Pack makes a message that is shown later, in whatever language the reader has, from something stored (a line of
// the activity log, the reason a connection is paused). Stored text cannot be translated once and for all, because
// the person who reads it may not be the one it happened to. Pack keeps the English message and its values; Unpack
// turns it into words. Old stored text that is plain English (not packed) is shown as it is.
func Pack(msg string, kv ...any) string {
	p := packed{M: msg}
	fillPacked(&p, kv)
	return marshal(p)
}

// PackN is Pack for a message with a count (see N).
func PackN(one, other string, count int, kv ...any) string {
	p := packed{M: one, O: other, N: &count}
	fillPacked(&p, kv)
	return marshal(p)
}

// PackJoin joins several packed messages into one, with a separator, so a sentence made of optional parts can be
// translated part by part.
func PackJoin(sep string, parts ...string) string { return marshal(packed{J: sep, P: parts}) }

// Unpack is the words of something Pack made, in this language; text that was not packed comes back unchanged.
func (l *Locale) Unpack(s string) string {
	if !strings.HasPrefix(s, `{"`) {
		return s
	}
	var p packed
	if json.Unmarshal([]byte(s), &p) != nil || (p.M == "" && len(p.P) == 0) {
		return s
	}
	if len(p.P) > 0 {
		parts := make([]string, len(p.P))
		for i, part := range p.P {
			parts[i] = l.Unpack(part)
		}
		return strings.Join(parts, p.J)
	}
	kv := make([]any, 0, 2*(len(p.A)+len(p.T)+len(p.D)))
	for k, v := range p.A {
		kv = append(kv, k, v)
	}
	for k, v := range p.T {
		if at, err := time.Parse(time.RFC3339, v); err == nil {
			kv = append(kv, k, l.Stamp(at.Local()))
		}
	}
	for k, v := range p.D {
		if at, err := time.Parse(time.RFC3339, v); err == nil {
			kv = append(kv, k, l.DateOnly(at.Local()))
		}
	}
	if p.N != nil {
		return l.N(p.M, p.O, *p.N, kv...)
	}
	return l.T(p.M, kv...)
}

// DateOnly is a day as the pages show it ("2 Nov 2026", "2. Nov. 2026").
func (l *Locale) DateOnly(t time.Time) string {
	day := fmt.Sprint(t.Day())
	if l.Lang == German {
		day += "."
	}
	return fmt.Sprintf("%s %s %d", day, months[l.Lang][t.Month()-1], t.Year())
}
