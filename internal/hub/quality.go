package hub

import (
	"image"
	"math"
	"math/bits"

	// the pictures Immich makes as previews: a JPEG by default; a WebP preview cannot be looked at and is kept as it is
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// frame is a picture as grey levels from 0 (black) to 255, small enough to look at quickly.
type frame struct {
	w, h int
	px   []float64
}

// grayscale shrinks a picture to at most maxSide pixels on its longer side and makes it grey, averaging a few samples of
// each block so nothing is read pixel by pixel.
func grayscale(img image.Image, maxSide int) frame {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return frame{}
	}
	scale := float64(max(w, h)) / float64(maxSide)
	if scale < 1 {
		scale = 1
	}
	tw, th := max(1, int(math.Ceil(float64(w)/scale))), max(1, int(math.Ceil(float64(h)/scale)))
	f := frame{w: tw, h: th, px: make([]float64, tw*th)}
	for y := 0; y < th; y++ {
		y0, y1 := b.Min.Y+y*h/th, b.Min.Y+(y+1)*h/th
		for x := 0; x < tw; x++ {
			x0, x1 := b.Min.X+x*w/tw, b.Min.X+(x+1)*w/tw
			var sum float64
			var n int
			sy, sx := max(1, (y1-y0)/3), max(1, (x1-x0)/3)
			for yy := y0; yy < max(y1, y0+1); yy += sy {
				for xx := x0; xx < max(x1, x0+1); xx += sx {
					r, g, bl, _ := img.At(xx, yy).RGBA()
					sum += (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)) / 257
					n++
				}
			}
			f.px[y*tw+x] = sum / float64(n)
		}
	}
	return f
}

// resize averages a frame down to w x h.
func (f frame) resize(w, h int) frame {
	out := frame{w: w, h: h, px: make([]float64, w*h)}
	for y := 0; y < h; y++ {
		y0, y1 := y*f.h/h, max((y+1)*f.h/h, y*f.h/h+1)
		for x := 0; x < w; x++ {
			x0, x1 := x*f.w/w, max((x+1)*f.w/w, x*f.w/w+1)
			var sum float64
			var n int
			for yy := y0; yy < y1 && yy < f.h; yy++ {
				for xx := x0; xx < x1 && xx < f.w; xx++ {
					sum += f.px[yy*f.w+xx]
					n++
				}
			}
			if n > 0 {
				out.px[y*w+x] = sum / float64(n)
			}
		}
	}
	return out
}

func (f frame) mean() float64 {
	if len(f.px) == 0 {
		return 0
	}
	var s float64
	for _, v := range f.px {
		s += v
	}
	return s / float64(len(f.px))
}

// sharpness is the variance of the Laplacian: how much fine detail there is. A blurred picture has almost none.
func (f frame) sharpness() float64 {
	if f.w < 3 || f.h < 3 {
		return 0
	}
	var sum, sumSq float64
	n := 0
	for y := 1; y < f.h-1; y++ {
		for x := 1; x < f.w-1; x++ {
			l := f.px[(y-1)*f.w+x] + f.px[(y+1)*f.w+x] + f.px[y*f.w+x-1] + f.px[y*f.w+x+1] - 4*f.px[y*f.w+x]
			sum += l
			sumSq += l * l
			n++
		}
	}
	mean := sum / float64(n)
	return sumSq/float64(n) - mean*mean
}

// dhash is a 64-bit mark of how a picture's brightness runs across it; two shots of the same scene have marks that differ
// in a few bits, and different scenes in about half of them.
func (f frame) dhash() uint64 {
	s := f.resize(9, 8)
	var h uint64
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			h <<= 1
			if s.px[y*9+x] < s.px[y*9+x+1] {
				h |= 1
			}
		}
	}
	return h
}

func hamming(a, b uint64) int { return bits.OnesCount64(a ^ b) }

// quality is what a best-of looks at in one picture.
type quality struct {
	Mean  float64 // 0 black to 255 white
	Sharp float64
	Hash  uint64
}

func analyze(img image.Image) quality {
	f := grayscale(img, 256)
	return quality{Mean: f.mean(), Sharp: f.sharpness(), Hash: f.dhash()}
}

// Strictness is how much a best-of leaves out: what counts as too dark, too bright and blurred, and how alike two
// pictures must be to be the same moment.
type Strictness struct {
	Name        string
	DarkBelow   float64
	BrightAbove float64
	BlurBelow   float64
	SameWithin  int // the most bits two marks may differ in to be the same scene
}

var strictnesses = []Strictness{
	{"relaxed", 30, 242, 8, 4},
	{"normal", 45, 232, 20, 7},
	{"strict", 60, 222, 40, 10},
}

// StrictnessNamed finds a strictness by name ("normal" when unknown).
func StrictnessNamed(name string) Strictness {
	for _, s := range strictnesses {
		if s.Name == name {
			return s
		}
	}
	return strictnesses[1]
}
