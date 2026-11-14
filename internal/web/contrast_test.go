package web

import (
	"fmt"
	"testing"
)

func TestWhateverColourAHostPicksTextOnItAndInItReads(t *testing.T) {
	// a grid over the colour cube: every one of them is somebody's brand colour
	n := 0
	for r := 0; r <= 255; r += 15 {
		for g := 0; g <= 255; g += 15 {
			for b := 0; b <= 255; b += 15 {
				hex := fmt.Sprintf("#%02x%02x%02x", r, g, b)
				if got := contrastRatio(hex, onAccent(hex)); got < 4.5 {
					t.Fatalf("text on %s is %s, contrast %.2f; at least 4.5 is needed", hex, onAccent(hex), got)
				}
				light, dark := accentInk(hex)
				if got := contrastRatio(light, lightPageEdge); got < 4.5 {
					t.Fatalf("%s as text on a light page became %s, contrast %.2f", hex, light, got)
				}
				if got := contrastRatio(dark, darkPageEdge); got < 4.5 {
					t.Fatalf("%s as text on a dark page became %s, contrast %.2f", hex, dark, got)
				}
				n++
			}
		}
	}
	if n < 4000 {
		t.Fatalf("only %d colours tried", n)
	}
}

func TestAColourThatReadsIsLeftAlone(t *testing.T) {
	// the app's own indigo reads on a light page, and its lighter form on a dark one
	if light, _ := accentInk("#4250af"); light != "#4250af" {
		t.Errorf("an indigo that reads must stay: %s", light)
	}
	if _, dark := accentInk("#98a6ff"); dark != "#98a6ff" {
		t.Errorf("a light indigo that reads on dark must stay: %s", dark)
	}
	// a pale yellow does not read on a light page: it is made darker, and keeps its hue
	light, dark := accentInk("#ffe066")
	if light == "#ffe066" || contrastRatio(light, lightPageEdge) < 4.5 {
		t.Errorf("a pale yellow as text on a light page: %s", light)
	}
	if dark != "#ffe066" {
		t.Errorf("but it reads on a dark page as it is: %s", dark)
	}
	if onAccent("#ffe066") != "#111111" || onAccent("#112233") != "#ffffff" {
		t.Error("text on a pale colour is dark, on a deep one light")
	}
	// white as the accent: the text is made as dark as it must be
	if l, _ := accentInk("#ffffff"); contrastRatio(l, lightPageEdge) < 4.5 {
		t.Errorf("white as text: %s", l)
	}
}
