package web

import (
	"context"
	"strings"

	"github.com/a-h/templ"
)

// The look of the pages: two colour schemes (palettes), each with a light and a dark variant. The administrator picks
// which one people get to begin with (THEME) and, if they like, which variant (THEME_MODE); everyone can then choose
// for themselves from the bar, and the choice is remembered in their browser (see ts/theme.ts).
const (
	ThemeTwilight = "twilight" // the colours of the app's icon: indigo, magenta and the orange of the horizon (the default)
	ThemeImmich   = "immich"   // calm blues on near-neutral greys, close to Immich's own look

	ModeAuto  = "auto" // follow the browser's own light/dark setting (the default)
	ModeLight = "light"
	ModeDark  = "dark"
)

// Themes lists the colour schemes in the order the menu shows them.
func Themes() []string { return []string{ThemeTwilight, ThemeImmich} }

// NormalizeTheme turns the setting into one of the known colour schemes. An empty setting is the default and is fine;
// anything else that is not known gives the default and false, so the caller can say so in the log.
func NormalizeTheme(s string) (string, bool) {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case "":
		return ThemeTwilight, true
	case ThemeTwilight, ThemeImmich:
		return v, true
	}
	return ThemeTwilight, false
}

// NormalizeThemeMode does the same for the light/dark setting.
func NormalizeThemeMode(s string) (string, bool) {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case "":
		return ModeAuto, true
	case ModeAuto, ModeLight, ModeDark:
		return v, true
	}
	return ModeAuto, false
}

type themeKey struct{}

type themeInfo struct{ palette, mode string }

// withTheme carries the administrator's choice to the page.
func withTheme(ctx context.Context, palette, mode string) context.Context {
	p, _ := NormalizeTheme(palette)
	m, _ := NormalizeThemeMode(mode)
	return context.WithValue(ctx, themeKey{}, themeInfo{p, m})
}

func themeOf(ctx context.Context) themeInfo {
	if v, ok := ctx.Value(themeKey{}).(themeInfo); ok {
		return v
	}
	return themeInfo{ThemeTwilight, ModeAuto}
}

// paletteOf is the colour scheme the page starts with.
func paletteOf(ctx context.Context) string { return themeOf(ctx).palette }

// htmlTheme is what goes on the <html> element: the colour scheme always, and the variant only when the administrator
// fixed one (otherwise the browser's own setting decides, through CSS alone). A person's own choice is applied over
// both by theme.js before anything is painted.
func htmlTheme(ctx context.Context) templ.Attributes {
	th := themeOf(ctx)
	a := templ.Attributes{"data-palette": th.palette}
	if th.mode == ModeLight || th.mode == ModeDark {
		a["data-theme"] = th.mode
	}
	return a
}

// themeColor is the colour of the phone's own address bar for a scheme and variant; it matches the page's background.
func themeColor(palette string, dark bool) string {
	switch {
	case palette == ThemeImmich && dark:
		return "#07080d"
	case palette == ThemeImmich:
		return "#f5f7fd"
	case dark:
		return "#0d0b20"
	}
	return "#f5f2fd"
}

// themeLabel is the name of a colour scheme, as the menu shows it.
func themeLabel(ctx context.Context, palette string) string {
	if palette == ThemeImmich {
		return t(ctx, "Immich")
	}
	return t(ctx, "Twilight")
}
