//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
)

// inFrontOfImmich is what docs/admin/find-handoff.md asks of a reverse proxy: for signed-in people, one script tag before
// </body>, and this app under /handoff/ on the same address. It returns that address, written with "localhost" because
// the browser's sign-in cookies are set for that name, not for the one the test server prints.
func (w world) inFrontOfImmich(t *testing.T) string { return w.front(t, false) }

// front is inFrontOfImmich, and with noButton the app answers nothing under /handoff/immich/ (as an app that has the
// button switched off, or an older one, does).
func (w world) front(t *testing.T, noButton bool) string {
	t.Helper()
	immich, _ := url.Parse(baseURL)
	toImmich := httputil.NewSingleHostReverseProxy(immich)
	director := toImmich.Director
	toImmich.Director = func(r *http.Request) { director(r); r.Header.Del("Accept-Encoding") }
	toImmich.ModifyResponse = func(res *http.Response) error {
		if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") || !strings.Contains(res.Request.Header.Get("Cookie"), "immich_is_authenticated=true") {
			return nil
		}
		body, err := io.ReadAll(res.Body)
		if err != nil {
			return err
		}
		body = bytes.Replace(body, []byte("</body>"), []byte(`<script src="/handoff/static/immich-button.js" defer></script></body>`), 1)
		res.Body = io.NopCloser(bytes.NewReader(body))
		res.ContentLength = int64(len(body))
		res.Header.Set("Content-Length", fmt.Sprint(len(body)))
		return nil
	}
	appURL, _ := url.Parse("http://localhost:" + w.port)
	toApp := httputil.NewSingleHostReverseProxy(appURL)
	mux := http.NewServeMux()
	if noButton {
		mux.HandleFunc("/handoff/immich/", http.NotFound)
	}
	mux.Handle("/handoff/", http.StripPrefix("/handoff", toApp))
	mux.Handle("/", toImmich)
	front := httptest.NewServer(mux)
	t.Cleanup(front.Close) // after the browsers (cleanups run last in, first out): their open connections would hold it
	// the browser's sign-in cookies are set for "localhost", not for the address the test server prints
	address := strings.Replace(front.URL, "127.0.0.1", "localhost", 1)
	return address
}
