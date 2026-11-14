// Package mark puts a photographer's mark on a picture so that a client can look at it and choose, and a copy of it is worth
// little: text repeated diagonally across the whole picture, a logo in a corner, or both. It reads JPEG, PNG and WebP and always
// writes JPEG. It is deliberately strict: a picture it cannot read or draw on is an error, never the picture as it was.
package mark

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png" // the formats a picture or a logo may be in
	"math"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp"
)

// What to draw.
const (
	Text = "text" // the text, repeated diagonally over the picture
	Logo = "logo" // the logo in the bottom right corner
	Both = "both"
)

// Options say what the mark is.
type Options struct {
	Mode string // Text, Logo or Both
	Text string // for Text and Both
	Logo []byte // for Logo and Both: a JPEG, PNG or WebP
}

// ErrNothingToDraw: the options would leave the picture unmarked.
var ErrNothingToDraw = errors.New("mark: there is nothing to draw, so the picture would go out unmarked")

// ErrTooBig: the picture has more pixels than a preview should.
var ErrTooBig = errors.New("mark: the picture is larger than a preview")

const maxPixels = 40_000_000

// Preview marks the picture in src and returns it as a JPEG.
func Preview(src []byte, o Options) ([]byte, error) {
	wantText := (o.Mode == Text || o.Mode == Both) && o.Text != ""
	wantLogo := (o.Mode == Logo || o.Mode == Both) && len(o.Logo) > 0
	if !wantText && !wantLogo {
		return nil, ErrNothingToDraw
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	if cfg.Width*cfg.Height > maxPixels || cfg.Width < 1 || cfg.Height < 1 {
		return nil, ErrTooBig
	}
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	canvas := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(canvas, canvas.Bounds(), img, b.Min, draw.Src)

	if wantText {
		if err := tileText(canvas, o.Text); err != nil {
			return nil, err
		}
	}
	if wantLogo {
		if err := cornerLogo(canvas, o.Logo); err != nil {
			return nil, err
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, canvas, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

var regular *opentype.Font

func init() {
	f, err := opentype.Parse(goregular.TTF)
	if err != nil {
		panic(err) // the font is part of the program
	}
	regular = f
}

// stamp draws the text once, in white with a dark edge so that it reads on a bright picture and on a dark one, on a transparent
// tile, and returns the tile rotated about 30 degrees up to the right.
func stamp(text string, size float64) (*image.RGBA, error) {
	face, err := opentype.NewFace(regular, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, err
	}
	defer face.Close()
	adv := font.MeasureString(face, text)
	w := adv.Ceil() + int(size)
	h := int(size * 2)
	flat := image.NewRGBA(image.Rect(0, 0, w, h))
	d := &font.Drawer{Face: face}
	base := fixed.P(int(size)/2, int(size*1.3))
	// a dark shadow a little down and to the right, then the white text over it: it reads on a bright picture and on a dark one
	d.Dst, d.Src = flat, image.NewUniform(color.NRGBA{0, 0, 0, 120})
	d.Dot = fixed.Point26_6{X: base.X + fixed.I(2), Y: base.Y + fixed.I(2)}
	d.DrawString(text)
	d.Dst, d.Src, d.Dot = flat, image.NewUniform(color.NRGBA{255, 255, 255, 150}), base
	d.DrawString(text)

	const deg = 30.0
	sin, cos := math.Sin(deg*math.Pi/180), math.Cos(deg*math.Pi/180)
	rw := int(float64(w)*cos + float64(h)*sin)
	rh := int(float64(w)*sin + float64(h)*cos)
	rot := image.NewRGBA(image.Rect(0, 0, rw, rh))
	// rotate about the centre: destination -> source
	cx, cy, sx, sy := float64(rw)/2, float64(rh)/2, float64(w)/2, float64(h)/2
	for y := 0; y < rh; y++ {
		for x := 0; x < rw; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			fx := cos*dx - sin*dy + sx // rotate the other way: the text climbs to the right
			fy := sin*dx + cos*dy + sy
			if fx < 0 || fy < 0 || fx >= float64(w-1) || fy >= float64(h-1) {
				continue
			}
			rot.SetRGBA(x, y, bilinear(flat, fx, fy))
		}
	}
	return rot, nil
}

func bilinear(m *image.RGBA, x, y float64) color.RGBA {
	x0, y0 := int(x), int(y)
	fx, fy := x-float64(x0), y-float64(y0)
	var out [4]float64
	for _, c := range []struct {
		dx, dy int
		w      float64
	}{{0, 0, (1 - fx) * (1 - fy)}, {1, 0, fx * (1 - fy)}, {0, 1, (1 - fx) * fy}, {1, 1, fx * fy}} {
		p := m.RGBAAt(x0+c.dx, y0+c.dy)
		out[0] += float64(p.R) * c.w
		out[1] += float64(p.G) * c.w
		out[2] += float64(p.B) * c.w
		out[3] += float64(p.A) * c.w
	}
	return color.RGBA{uint8(out[0] + .5), uint8(out[1] + .5), uint8(out[2] + .5), uint8(out[3] + .5)}
}

// tileText covers the whole picture with the stamp, in rows that are shifted against each other so there is no clear path
// through the pattern, with a size that follows the picture's size.
func tileText(canvas *image.RGBA, text string) error {
	b := canvas.Bounds()
	short := min(b.Dx(), b.Dy())
	size := math.Max(14, float64(short)/22)
	tile, err := stamp(text, size)
	if err != nil {
		return err
	}
	tw, th := tile.Bounds().Dx(), tile.Bounds().Dy()
	stepX, stepY := tw+int(size*2), th/2+int(size*2)
	row := 0
	for y := -th; y < b.Dy()+th; y += stepY {
		shift := 0
		if row%2 == 1 {
			shift = stepX / 2
		}
		for x := -tw + shift; x < b.Dx()+tw; x += stepX {
			draw.Draw(canvas, image.Rect(x, y, x+tw, y+th).Intersect(b), tile, image.Pt(max(0, -x), max(0, -y)), draw.Over)
		}
		row++
	}
	return nil
}

// cornerLogo puts the logo, about a fifth as wide as the picture, in the bottom right corner, a little see-through.
func cornerLogo(canvas *image.RGBA, logo []byte) error {
	img, _, err := image.Decode(bytes.NewReader(logo))
	if err != nil {
		return err
	}
	b, lb := canvas.Bounds(), img.Bounds()
	if lb.Dx() < 1 || lb.Dy() < 1 {
		return errors.New("mark: the logo is empty")
	}
	short := min(b.Dx(), b.Dy())
	w := max(48, min(b.Dx()/4, int(float64(short)*0.3)))
	h := w * lb.Dy() / lb.Dx()
	if h > b.Dy()/3 {
		h = b.Dy() / 3
		w = h * lb.Dx() / lb.Dy()
	}
	scaled := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), img, lb, xdraw.Over, nil)
	margin := max(8, short/40)
	at := image.Rect(b.Dx()-margin-w, b.Dy()-margin-h, b.Dx()-margin, b.Dy()-margin)
	draw.DrawMask(canvas, at, scaled, image.Point{}, image.NewUniform(color.Alpha{A: 215}), image.Point{}, draw.Over)
	return nil
}
