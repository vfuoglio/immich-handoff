package immich

import (
	"bytes"
	"encoding/xml"
	"io"
	"mime/multipart"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func detailed() AssetResponseDto {
	desc, rating, lat, lon := `Sunset at the "pier" & <more>`, 4, 48.8584, -2.2945
	return AssetResponseDto{Id: uuid.New(), OriginalFileName: "pier.jpg", FileCreatedAt: time.Now(), FileModifiedAt: time.Now(),
		ExifInfo: &ExifResponseDto{Description: &desc, Rating: &rating, Latitude: &lat, Longitude: &lon}}
}

func TestTheSidecarCarriesDescriptionRatingAndLocationAsValidXML(t *testing.T) {
	x := Sidecar(detailed())
	if x == nil {
		t.Fatal("no sidecar")
	}
	if err := xml.NewDecoder(bytes.NewReader(x)).Decode(new(struct{})); err != nil && !strings.Contains(err.Error(), "expected element") {
		t.Fatalf("not well-formed XML: %v\n%s", err, x)
	}
	for _, want := range []string{`xmp:Rating="4"`, `exif:GPSLatitude="48,51.504000N"`, `exif:GPSLongitude="2,17.670000W"`,
		`Sunset at the &#34;pier&#34; &amp; &lt;more&gt;`} {
		if !strings.Contains(string(x), want) {
			t.Errorf("missing %q in\n%s", want, x)
		}
	}
}

func TestThereIsNoSidecarWhenThereIsNothingToCarry(t *testing.T) {
	none, zero, blank := 0, 0, ""
	for name, a := range map[string]AssetResponseDto{
		"no exif":         {},
		"empty":           {ExifInfo: &ExifResponseDto{}},
		"nothing":         {ExifInfo: &ExifResponseDto{Rating: &none, Description: &blank}},
		"rejected":        {ExifInfo: &ExifResponseDto{Rating: func() *int { r := -1; return &r }()}},
		"half a location": {ExifInfo: &ExifResponseDto{Latitude: func() *float64 { v := 1.0; return &v }()}},
	} {
		_ = zero
		if x := Sidecar(a); x != nil {
			t.Errorf("%s: unexpected sidecar\n%s", name, x)
		}
	}
}

func upload(t *testing.T, a AssetResponseDto, opts UploadOptions) map[string]string {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := writeUpload(mw, strings.NewReader("file"), a, opts); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	r := multipart.NewReader(&buf, mw.Boundary())
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			return got
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p)
		got[p.FormName()] = string(b)
	}
}

func TestTheUploadCarriesTheDetailsOnlyWhenAskedTo(t *testing.T) {
	a := detailed()
	a.Visibility = Archive
	with := upload(t, a, UploadOptions{Details: true})
	if with["visibility"] != "archive" || !strings.Contains(with["sidecarData"], "xmp:Rating") {
		t.Errorf("archived state and sidecar expected: %v", with)
	}
	without := upload(t, a, UploadOptions{})
	if _, ok := without["visibility"]; ok || without["sidecarData"] != "" {
		t.Errorf("nothing extra expected: %v", without)
	}
	hidden := upload(t, a, UploadOptions{Hidden: true, Details: true})
	if hidden["visibility"] != "hidden" {
		t.Errorf("a motion clip stays hidden: %v", hidden)
	}
}
