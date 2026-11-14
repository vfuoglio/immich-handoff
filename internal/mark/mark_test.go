package mark_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"testing"

	"github.com/vfuoglio/immich-handoff/internal/mark"
)

// picture is a smooth gradient, so that anything drawn on it shows.
func picture(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(60 + x*100/w), uint8(90 + y*100/h), 140, 255})
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 95})
	return b.Bytes()
}

func logo() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 80, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 80; x++ {
			img.Set(x, y, color.RGBA{250, 40, 40, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func decode(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the result is a JPEG: %v", err)
	}
	return img
}

// changed counts how many sampled points of a region differ clearly from the plain picture.
func changed(a, b image.Image, r image.Rectangle) (n, of int) {
	for y := r.Min.Y; y < r.Max.Y; y += 3 {
		for x := r.Min.X; x < r.Max.X; x += 3 {
			ar, ag, ab, _ := a.At(x, y).RGBA()
			br, bg, bb, _ := b.At(x, y).RGBA()
			d := abs(int(ar>>8)-int(br>>8)) + abs(int(ag>>8)-int(bg>>8)) + abs(int(ab>>8)-int(bb>>8))
			of++
			if d > 24 {
				n++
			}
		}
	}
	return
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func TestTheTextIsRepeatedOverEveryPartOfThePicture(t *testing.T) {
	src := picture(1440, 960)
	out, err := mark.Preview(src, mark.Options{Mode: mark.Text, Text: "Chiara Bianchi Photography"})
	if err != nil {
		t.Fatal(err)
	}
	plain, marked := decode(t, src), decode(t, out)
	if marked.Bounds() != plain.Bounds() {
		t.Fatalf("the size is kept: %v", marked.Bounds())
	}
	w, h := 1440, 960
	for name, r := range map[string]image.Rectangle{
		"top left": image.Rect(0, 0, w/3, h/3), "top right": image.Rect(2*w/3, 0, w, h/3), "middle": image.Rect(w/3, h/3, 2*w/3, 2*h/3),
		"bottom left": image.Rect(0, 2*h/3, w/3, h), "bottom right": image.Rect(2*w/3, 2*h/3, w, h),
	} {
		if n, of := changed(plain, marked, r); n*100/of < 2 {
			t.Errorf("%s: only %d of %d points carry the mark, so it could be cropped out", name, n, of)
		}
	}
	if os.Getenv("MARK_SAMPLE") != "" { // to look at: MARK_SAMPLE=/tmp/x.jpg go test ./internal/mark
		os.WriteFile(os.Getenv("MARK_SAMPLE"), out, 0o644)
	}
}

func TestTheLogoGoesInTheBottomRightCornerAndNowhereElse(t *testing.T) {
	src := picture(1200, 800)
	out, err := mark.Preview(src, mark.Options{Mode: mark.Logo, Logo: logo()})
	if err != nil {
		t.Fatal(err)
	}
	plain, marked := decode(t, src), decode(t, out)
	if n, of := changed(plain, marked, image.Rect(1200-340, 800-200, 1200, 800)); n*100/of < 20 {
		t.Errorf("the corner carries the logo: %d of %d", n, of)
	}
	if n, _ := changed(plain, marked, image.Rect(0, 0, 800, 500)); n != 0 {
		t.Errorf("nothing else is drawn: %d points changed", n)
	}
}

func TestBothDrawsTheTextAndTheLogo(t *testing.T) {
	src := picture(1200, 800)
	out, err := mark.Preview(src, mark.Options{Mode: mark.Both, Text: "Studio", Logo: logo()})
	if err != nil {
		t.Fatal(err)
	}
	plain, marked := decode(t, src), decode(t, out)
	if n, of := changed(plain, marked, image.Rect(0, 0, 600, 400)); n*100/of < 2 {
		t.Errorf("the text: %d of %d", n, of)
	}
	if n, of := changed(plain, marked, image.Rect(1200-340, 800-200, 1200, 800)); n*100/of < 20 {
		t.Errorf("the logo: %d of %d", n, of)
	}
}

func TestAPictureThatCannotBeMarkedIsAnErrorNeverThePlainPicture(t *testing.T) {
	src := picture(400, 300)
	for name, o := range map[string]mark.Options{
		"no mode":        {},
		"text, no text":  {Mode: mark.Text},
		"logo, no logo":  {Mode: mark.Logo},
		"unknown mode":   {Mode: "none", Text: "x"},
		"both, nothing":  {Mode: mark.Both},
		"empty text too": {Mode: mark.Text, Text: ""},
	} {
		out, err := mark.Preview(src, o)
		if !errors.Is(err, mark.ErrNothingToDraw) || out != nil {
			t.Errorf("%s: %v (%d bytes)", name, err, len(out))
		}
	}
	for name, bad := range map[string][]byte{"garbage": []byte("not a picture"), "empty": nil, "a page": []byte("<html></html>"), "cut off": src[:200]} {
		if out, err := mark.Preview(bad, mark.Options{Mode: mark.Text, Text: "x"}); err == nil || out != nil {
			t.Errorf("%s: %v (%d bytes)", name, err, len(out))
		}
	}
	if out, err := mark.Preview(src, mark.Options{Mode: mark.Logo, Logo: []byte("not a logo")}); err == nil || out != nil {
		t.Errorf("a logo that cannot be read: %v", err)
	}
}

func TestAWebPPreviewIsReadAndMarked(t *testing.T) {
	// a 1x1 lossless WebP: Immich can make previews in this format, and the standard library cannot read it
	webp, _ := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	out, err := mark.Preview(webp, mark.Options{Mode: mark.Text, Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	decode(t, out)
}

func TestVeryLargePicturesAreRefused(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 9000, 9000))
	var b bytes.Buffer
	png.Encode(&b, img)
	if _, err := mark.Preview(b.Bytes(), mark.Options{Mode: mark.Text, Text: "x"}); !errors.Is(err, mark.ErrTooBig) {
		t.Errorf("%v", err)
	}
}
