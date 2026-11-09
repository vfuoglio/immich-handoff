package hub

import (
	"bytes"
	"net/http"
	"path/filepath"
	"strings"
	"unicode"
)

// sniffDrop says what a guest's file really is from its first bytes, never from its name or from what the browser
// claims: "image" or "video", with the extension the file should be stored with, or "" when it is neither (a page, a
// program, an archive, a document). Only what Immich can show is let in.
func sniffDrop(head []byte) (kind, ext string) {
	// ISO base media files (HEIC, AVIF, MP4, MOV, 3GP) all start with a box of type ftyp, whose brand tells them apart
	if len(head) >= 12 && string(head[4:8]) == "ftyp" {
		brand := string(head[8:12])
		switch brand {
		case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
			return "image", ".heic"
		case "avif", "avis":
			return "image", ".avif"
		case "qt  ":
			return "video", ".mov"
		case "3gp4", "3gp5", "3gp6", "3gp7", "3g2a":
			return "video", ".3gp"
		}
		if strings.HasPrefix(brand, "mp4") || strings.HasPrefix(brand, "iso") || strings.HasPrefix(brand, "avc") ||
			brand == "M4V " || brand == "M4VH" || brand == "dash" || brand == "MSNV" {
			return "video", ".mp4"
		}
		return "", ""
	}
	// a TIFF header also opens most camera raw files (DNG and several others)
	if bytes.HasPrefix(head, []byte("II*\x00")) || bytes.HasPrefix(head, []byte("MM\x00*")) {
		return "image", ".tiff"
	}
	switch http.DetectContentType(head) {
	case "image/jpeg":
		return "image", ".jpg"
	case "image/png":
		return "image", ".png"
	case "image/gif":
		return "image", ".gif"
	case "image/webp":
		return "image", ".webp"
	case "image/bmp":
		return "image", ".bmp"
	case "video/webm":
		return "video", ".webm"
	case "video/mp4":
		return "video", ".mp4"
	}
	return "", ""
}

// dropFileName is the name a guest's file is stored under: what they called it, cleaned of anything that is not a
// letter, a digit or plain punctuation, with the extension its content calls for.
func dropFileName(original, ext string) string {
	base := original
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, filepath.Ext(base))
	var b strings.Builder
	for _, r := range base {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
		if b.Len() >= 80 {
			break
		}
	}
	stem := strings.Trim(b.String(), " .")
	if stem == "" {
		stem = "photo"
	}
	return stem + ext
}
