// Package pets is the client of the optional pet sidecar (pet-sidecar/): it finds the cats and dogs in a photo and fingerprints each
// one. The sidecar is stateless and is told nothing but the photo's bytes.
package pets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrDown says the sidecar did not answer: the rule falls back to Immich's own search and says so.
var ErrDown = errors.New("the pet helper is not answering")

// Animal is a cat or a dog the sidecar found.
type Animal struct {
	Kind  string     `json:"kind"`
	Score float64    `json:"score"`
	Box   [4]float64 `json:"box"`   // x, y, width, height, as fractions of the photo
	Print []float32  `json:"print"` // a fingerprint of the crop, length 1
}

// Client talks to the sidecar at URL.
type Client struct {
	URL  string
	HTTP *http.Client // nil: one with a minute's timeout
}

// New checks the address (http or https, nothing after the host) and makes the client.
func New(raw string) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("PET_SIDECAR_URL must be an address such as http://pets:8090, not %q", raw)
	}
	return &Client{URL: strings.TrimRight(u.String(), "/")}, nil
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: time.Minute}
}

// Ping says whether the sidecar answers.
func (c *Client) Ping(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", c.URL+"/ping", nil)
	res, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDown, err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("%w: it answered %d", ErrDown, res.StatusCode)
	}
	return nil
}

// Analyze sends a photo (a preview, never the original) and gets back the animals in it, best first. A photo the sidecar cannot read
// gives none and no error; a sidecar that does not answer gives ErrDown.
func (c *Client) Analyze(ctx context.Context, photo []byte) ([]Animal, error) {
	req, _ := http.NewRequestWithContext(ctx, "POST", c.URL+"/analyze", bytes.NewReader(photo))
	req.Header.Set("Content-Type", "image/jpeg")
	res, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDown, err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == 422 || res.StatusCode == 400 || res.StatusCode == 413:
		io.Copy(io.Discard, res.Body)
		return nil, nil
	case res.StatusCode != 200:
		io.Copy(io.Discard, res.Body)
		return nil, fmt.Errorf("%w: it answered %d", ErrDown, res.StatusCode)
	}
	var out struct {
		Animals []Animal `json:"animals"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDown, err)
	}
	return out.Animals, nil
}

// Cosine is how alike two fingerprints are (they are unit length): 1 is the same, 0 is nothing in common.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}
