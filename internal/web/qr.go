package web

import (
	"fmt"
	"net/http"
	"strings"

	"rsc.io/qr"
)

// qrCode answers with a QR code, as an SVG, for the address a page asks about. The pages compose invitation links in
// the browser (they depend on where the page is served), so the browser sends the text here. It is black on white with
// the quiet border scanners need, whatever the page's theme: a code on a dark background is often not read.
func (w *Server) qrCode(rw http.ResponseWriter, r *http.Request) {
	if w.guarded(rw, r) == nil {
		return
	}
	text := r.FormValue("text")
	if text == "" || len(text) > 500 || !(strings.HasPrefix(text, "https://") || strings.HasPrefix(text, "http://")) {
		http.Error(rw, "a web address of at most 500 characters is needed", http.StatusBadRequest)
		return
	}
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		http.Error(rw, "cannot encode that", http.StatusBadRequest)
		return
	}
	const border = 4
	size := code.Size + 2*border
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges"><rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, size, size, size, size)
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+border, y+border)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	rw.Header().Set("Content-Type", "image/svg+xml")
	rw.Header().Set("Cache-Control", "no-store")
	rw.Write([]byte(b.String()))
}
