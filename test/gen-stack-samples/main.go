// Command gen-stack-samples writes the small sample photos the stacking tests use (test/external-photos/stacks): tiny JPEGs that
// carry real EXIF (camera make and model, and the moment they were taken) and, where a RAW is wanted, a file with a RAW's name and
// extension whose bytes are a JPEG followed by padding (Immich keeps the path and reads the EXIF; the checksum must differ).
// They are made here, not taken from anyone, so they are free of any licence. Run: go run ./test/gen-stack-samples test/external-photos/stacks
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"time"
)

type sample struct {
	path        string
	make, model string
	taken       time.Time
	raw         bool
}

func main() {
	root := os.Args[1]
	base := time.Date(2025, 7, 4, 12, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return base.Add(time.Duration(s) * time.Second) }
	canon, nikon := [2]string{"Canon", "EOS R5"}, [2]string{"Nikon", "Z 6"}
	samples := []sample{
		// a RAW and a JPEG of one shot
		{"trip/IMG_1001.JPG", canon[0], canon[1], at(0), false}, {"trip/IMG_1001.DNG", canon[0], canon[1], at(0), true},
		// a photo and its edit
		{"trip/IMG_1002.jpg", canon[0], canon[1], at(60), false}, {"trip/IMG_1002-edited.jpg", canon[0], canon[1], at(60), false},
		// the same name in another folder, and later: not the same shot
		{"other/IMG_1001.JPG", canon[0], canon[1], at(7200), false},
		// a burst of three, a second apart
		{"burst/DSC_0101.jpg", nikon[0], nikon[1], at(300), false}, {"burst/DSC_0102.jpg", nikon[0], nikon[1], at(301), false}, {"burst/DSC_0103.jpg", nikon[0], nikon[1], at(302), false},
		// two shots a second apart: not a burst
		{"pairs/DSC_0200.jpg", nikon[0], nikon[1], at(600), false}, {"pairs/DSC_0201.jpg", nikon[0], nikon[1], at(601), false},
		// a photo on its own
		{"alone/IMG_2000.jpg", canon[0], canon[1], at(900), false},
	}
	for i, s := range samples {
		img := image.NewRGBA(image.Rect(0, 0, 64, 48))
		st := uint32(i+3) * 2654435761
		for y := 0; y < 48; y++ {
			for x := 0; x < 64; x++ {
				st = st*1664525 + 1013904223
				img.Set(x, y, color.RGBA{uint8(st >> 16), uint8(st >> 8), uint8(st), 255})
			}
		}
		var b bytes.Buffer
		if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 70}); err != nil {
			panic(err)
		}
		data := withExif(b.Bytes(), s.make, s.model, s.taken)
		if s.raw {
			data = append(data, bytes.Repeat([]byte("RAW-PADDING-"), 4)...)
		}
		p := filepath.Join(root, s.path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			panic(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			panic(err)
		}
		fmt.Println(p)
	}
}

// withExif puts an EXIF segment with the camera's make and model and the moment of the shot right after the JPEG's start.
func withExif(jpg []byte, camMake, camModel string, taken time.Time) []byte {
	asc := func(s string) []byte { return append([]byte(s), 0) }
	mk, md, dt := asc(camMake), asc(camModel), asc(taken.Format("2006:01:02 15:04:05"))
	be := binary.BigEndian
	const ifd0Len = 2 + 3*12 + 4
	off := uint32(8 + ifd0Len) // where the data after IFD0 starts
	mkOff := off
	mdOff := mkOff + uint32(len(mk))
	exifOff := mdOff + uint32(len(md))
	entry := func(tag, typ uint16, count, val uint32) []byte {
		e := make([]byte, 12)
		be.PutUint16(e[0:], tag)
		be.PutUint16(e[2:], typ)
		be.PutUint32(e[4:], count)
		be.PutUint32(e[8:], val)
		return e
	}
	var t bytes.Buffer
	t.WriteString("MM\x00\x2a")
	binary.Write(&t, be, uint32(8))
	binary.Write(&t, be, uint16(3))
	t.Write(entry(0x010f, 2, uint32(len(mk)), mkOff))
	t.Write(entry(0x0110, 2, uint32(len(md)), mdOff))
	t.Write(entry(0x8769, 4, 1, exifOff))
	binary.Write(&t, be, uint32(0))
	t.Write(mk)
	t.Write(md)
	// the Exif IFD: one entry, DateTimeOriginal, whose text follows it
	binary.Write(&t, be, uint16(1))
	t.Write(entry(0x9003, 2, uint32(len(dt)), exifOff+2+12+4))
	binary.Write(&t, be, uint32(0))
	t.Write(dt)
	seg := append([]byte("Exif\x00\x00"), t.Bytes()...)
	var out bytes.Buffer
	out.Write([]byte{0xff, 0xd8, 0xff, 0xe1})
	binary.Write(&out, be, uint16(len(seg)+2))
	out.Write(seg)
	out.Write(jpg[2:]) // the encoder's own start marker is dropped
	return out.Bytes()
}
