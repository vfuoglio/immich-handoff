package safeimg

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"testing"
)

func pngOf(w, h int) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)))
	return b.Bytes()
}

func TestDecodeRefusesAPictureAboveTheLimitBeforeDecoding(t *testing.T) {
	if _, err := Decode(pngOf(100, 100), 10_000); err != nil {
		t.Fatalf("a picture at the limit: %v", err)
	}
	if _, err := Decode(pngOf(101, 100), 10_000); !errors.Is(err, ErrTooBig) {
		t.Fatalf("above the limit = %v, want ErrTooBig", err)
	}
	if _, err := Decode([]byte("not a picture"), 10_000); err == nil {
		t.Fatal("garbage decoded")
	}
}
