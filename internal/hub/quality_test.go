package hub

import (
	"image"
	"image/color"
	"testing"
	"time"
)

// noise is a picture full of detail: a repeatable pattern of dots between lo and hi grey.
func noise(seed uint32, lo, hi uint8) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	state := seed*2654435761 + 1
	for y := 0; y < 300; y += 3 {
		for x := 0; x < 400; x += 3 {
			state = state*1664525 + 1013904223
			v := lo + uint8((state>>16)%uint32(hi-lo+1))
			for dy := 0; dy < 3; dy++ {
				for dx := 0; dx < 3; dx++ {
					img.Set(x+dx, y+dy, color.RGBA{v, v, v, 255})
				}
			}
		}
	}
	return img
}

// smooth is a picture with no detail at all: a soft gradient, as a blurred shot is.
func smooth() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			v := uint8(80 + x*80/400 + y*20/300)
			img.Set(x, y, color.RGBA{v, v, v, 255})
		}
	}
	return img
}

func TestThePictureAnalysisTellsDetailBrightnessAndLikeness(t *testing.T) {
	sharp, blurred := analyze(noise(1, 40, 220)), analyze(smooth())
	if sharp.Sharp < 200 || blurred.Sharp > 5 {
		t.Errorf("detail: sharp %.1f, blurred %.1f", sharp.Sharp, blurred.Sharp)
	}
	if dark := analyze(noise(2, 0, 30)); dark.Mean > 20 {
		t.Errorf("dark: %.1f", dark.Mean)
	}
	if bright := analyze(noise(3, 235, 255)); bright.Mean < 235 {
		t.Errorf("bright: %.1f", bright.Mean)
	}
	// the same scene a little lighter is a few bits away; another scene is far
	same := analyze(noise(1, 46, 226))
	other := analyze(noise(9, 40, 220))
	if d := hamming(sharp.Hash, same.Hash); d > 8 {
		t.Errorf("the same scene differs in %d bits", d)
	}
	if d := hamming(sharp.Hash, other.Hash); d < 12 {
		t.Errorf("another scene differs in only %d bits", d)
	}
	// nothing to look at is not a crash
	if q := analyze(image.NewRGBA(image.Rect(0, 0, 1, 1))); q.Sharp != 0 {
		t.Errorf("a one-pixel picture: %+v", q)
	}
}

func TestTheJudgementLeavesOutTheDarkTheBlurredAndTheSameMoment(t *testing.T) {
	base := time.Date(2026, 6, 1, 14, 0, 0, 0, time.UTC)
	mk := func(name string, at time.Duration, q *quality) lookedAt {
		l := lookedAt{q: q}
		l.a.OriginalFileName = name
		l.a.FileCreatedAt = base.Add(at)
		return l
	}
	hashA, hashB := uint64(0x00ff00ff00ff00ff), uint64(0xf0f0f0f0f0f0f0f0)
	rs := []lookedAt{
		mk("good.jpg", 0, &quality{Mean: 120, Sharp: 300, Hash: hashA}),
		mk("better.jpg", 2*time.Minute, &quality{Mean: 120, Sharp: 500, Hash: hashA ^ 0b101}), // the same shot, sharper
		mk("later.jpg", 3*time.Hour, &quality{Mean: 120, Sharp: 100, Hash: hashA}),            // the same picture, hours on: another moment
		mk("other.jpg", 4*time.Minute, &quality{Mean: 120, Sharp: 150, Hash: hashB}),
		mk("night.jpg", time.Minute, &quality{Mean: 10, Sharp: 900, Hash: 1}),
		mk("glare.jpg", time.Minute, &quality{Mean: 250, Sharp: 900, Hash: 2}),
		mk("soft.jpg", time.Minute, &quality{Mean: 120, Sharp: 3, Hash: 3}),
		mk("odd.heic", time.Minute, nil),
	}
	got := judge(rs, StrictnessNamed("normal"))
	want := map[string]string{"good.jpg": VerdictDuplicate, "better.jpg": VerdictKept, "later.jpg": VerdictKept, "other.jpg": VerdictKept,
		"night.jpg": VerdictDark, "glare.jpg": VerdictBright, "soft.jpg": VerdictBlurry, "odd.heic": VerdictKept}
	for i, r := range rs {
		if got[i].verdict != want[r.a.OriginalFileName] {
			t.Errorf("%s: %s (%s), want %s", r.a.OriginalFileName, got[i].verdict, got[i].detail, want[r.a.OriginalFileName])
		}
	}
	if got[0].detail != "same moment as better.jpg" {
		t.Errorf("the reason must say which photo won: %q", got[0].detail)
	}
	// strictness moves the lines
	rs2 := []lookedAt{mk("a.jpg", 0, &quality{Mean: 55, Sharp: 30, Hash: 5})}
	for name, want := range map[string]string{"relaxed": VerdictKept, "normal": VerdictKept, "strict": VerdictDark} {
		if v := judge(rs2, StrictnessNamed(name))[0].verdict; v != want {
			t.Errorf("%s: %s, want %s", name, v, want)
		}
	}
	if StrictnessNamed("nonsense").Name != "normal" {
		t.Error("an unknown strictness is normal")
	}
}
