package immich

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
)

// Sidecar is an XMP file carrying the details a person added in Immich, which Immich keeps in its database and not in
// the photo: the description, the rating and the location. Uploaded beside the photo (`sidecarData`), it makes the
// receiver's Immich read them as if they had been written in the file. It returns nil when there is nothing to carry.
func Sidecar(a AssetResponseDto) []byte {
	e := a.ExifInfo
	if e == nil {
		return nil
	}
	var attrs, body bytes.Buffer
	if e.Rating != nil && *e.Rating > 0 { // -1 is "rejected" and 0 is "none": neither is worth sending
		fmt.Fprintf(&attrs, ` xmp:Rating="%d"`, *e.Rating)
	}
	if e.Latitude != nil && e.Longitude != nil && !math.IsNaN(*e.Latitude) && !math.IsNaN(*e.Longitude) {
		fmt.Fprintf(&attrs, ` exif:GPSLatitude="%s" exif:GPSLongitude="%s"`, gps(*e.Latitude, 'N', 'S'), gps(*e.Longitude, 'E', 'W'))
	}
	if e.Description != nil && *e.Description != "" {
		var esc bytes.Buffer
		xml.EscapeText(&esc, []byte(*e.Description))
		fmt.Fprintf(&body, `   <dc:description><rdf:Alt><rdf:li xml:lang="x-default">%s</rdf:li></rdf:Alt></dc:description>`+"\n", esc.String())
	}
	if attrs.Len() == 0 && body.Len() == 0 {
		return nil
	}
	return []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
 <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
  <rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmlns:exif="http://ns.adobe.com/exif/1.0/"` +
		attrs.String() + ">\n" + body.String() + `  </rdf:Description>
 </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>
`)
}

// gps writes a coordinate the way XMP does: whole degrees, decimal minutes and a hemisphere letter.
func gps(v float64, pos, neg byte) string {
	ref := pos
	if v < 0 {
		v, ref = -v, neg
	}
	deg := math.Floor(v)
	return strconv.FormatFloat(deg, 'f', 0, 64) + "," + strconv.FormatFloat((v-deg)*60, 'f', 6, 64) + string(ref)
}
