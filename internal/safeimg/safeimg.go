// Package safeimg decodes pictures that come from outside without letting a small file claim a huge canvas.
//
// A few kilobytes of PNG or JPEG can announce a picture of billions of pixels, and decoding it takes gigabytes
// of memory. Decode reads the size from the header first and refuses a picture above the limit it was given.
package safeimg

import (
	"bytes"
	"errors"
	"image"
	_ "image/gif"  // register the formats image.Decode understands
	_ "image/jpeg" // ...
	_ "image/png"  // ...

	_ "golang.org/x/image/webp" // ...
)

// ErrTooBig: the picture has more pixels than the caller allows (or none at all).
var ErrTooBig = errors.New("safeimg: the picture is larger than allowed")

// Common limits, in pixels.
const (
	// Preview is for the small pictures Immich makes (about 1440 px wide).
	Preview = 16_000_000
	// Original is for a photo straight from a camera, about 64 megapixels.
	Original = 64_000_000
)

// Decode decodes b, or returns ErrTooBig without decoding when the header says it has more than maxPixels.
func Decode(b []byte, maxPixels int64) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return nil, ErrTooBig
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	return img, err
}
