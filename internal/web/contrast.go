package web

import (
	"fmt"
	"math"
	"strings"
)

// A drop box can have a colour of its own. The colour is used as a background (buttons, bars) and as the colour of text
// (links, small headings), and a host may pick one that is lovely and unreadable: a pale yellow on white. So the page
// takes care of the contrast itself, with the WCAG measure: text on the colour is black or white, whichever reads better
// (always at least 4.5 to 1), and text in the colour is the colour made darker (light page) or lighter (dark page) just
// enough to reach 4.5 to 1.

// the lightest background of the dark pages and the darkest of the light pages, so a text colour that reads on these
// reads on the rest
const (
	lightPageEdge = "#e8e4f6"
	darkPageEdge  = "#26205a"
)

func rgbOf(hex string) (r, g, b float64) {
	var ri, gi, bi int
	fmt.Sscanf(strings.TrimPrefix(hex, "#"), "%02x%02x%02x", &ri, &gi, &bi)
	return float64(ri), float64(gi), float64(bi)
}

func channel(v float64) float64 {
	v /= 255
	if v <= 0.03928 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// luminance is the WCAG relative luminance of a colour, 0 (black) to 1 (white).
func luminance(hex string) float64 {
	r, g, b := rgbOf(hex)
	return 0.2126*channel(r) + 0.7152*channel(g) + 0.0722*channel(b)
}

// contrastRatio is the WCAG contrast ratio of two colours, 1 to 21.
func contrastRatio(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// onAccent is the colour of text on a background of the given colour: near-black or white, whichever reads better, and
// pure black where neither reaches 4.5 to 1 (for any colour, black or white does).
func onAccent(hex string) string {
	best := "#ffffff"
	if contrastRatio(hex, "#111111") > contrastRatio(hex, "#ffffff") {
		best = "#111111"
	}
	if contrastRatio(hex, best) < 4.5 && contrastRatio(hex, "#000000") > contrastRatio(hex, "#ffffff") {
		return "#000000"
	}
	return best
}

// inkFor is the colour as text on the given background: the colour itself if it reads (4.5 to 1), else mixed with black or
// white, a little at a time, until it does.
func inkFor(hex, background, toward string) string {
	if contrastRatio(hex, background) >= 4.5 {
		return hex
	}
	r, g, b := rgbOf(hex)
	tr, tg, tb := rgbOf(toward)
	for step := 1; step <= 25; step++ {
		f := float64(step) * 0.04
		mix := fmt.Sprintf("#%02x%02x%02x", int(math.Round(r+(tr-r)*f)), int(math.Round(g+(tg-g)*f)), int(math.Round(b+(tb-b)*f)))
		if contrastRatio(mix, background) >= 4.5 {
			return mix
		}
	}
	return toward
}

// accentInk is the colour for text in a box's colour, one for the light pages and one for the dark.
func accentInk(hex string) (light, dark string) {
	return inkFor(hex, lightPageEdge, "#000000"), inkFor(hex, darkPageEdge, "#ffffff")
}
