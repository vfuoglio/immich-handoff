// Package peer is how two Handoff apps talk to each other about an album they share (docs/shared-albums-design.md). Every
// request between them is signed with a secret only the two apps know: the signature covers the method, the path, the time, a
// nonce and a hash of the body, so a request cannot be changed, replayed or made up. Nothing here knows about Immich.
package peer

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Version is the protocol the app speaks. It is in every path (/peer/v1/...) and an app refuses what it does not know.
const Version = 1

// The headers of a signed request.
const (
	HeaderPeer      = "X-Handoff-Peer"      // the id of the peer, as the receiver knows it
	HeaderTime      = "X-Handoff-Time"      // seconds since 1970
	HeaderNonce     = "X-Handoff-Nonce"     // used once
	HeaderSignature = "X-Handoff-Signature" // hex HMAC-SHA256
)

// Skew is how far a request's time may be from the receiver's clock.
const Skew = 5 * time.Minute

var (
	ErrBadSignature = errors.New("peer: the signature is not right")
	ErrStale        = errors.New("peer: the request is too old or from the future (check the clocks)")
	ErrReplay       = errors.New("peer: this request was already seen")
)

// NewSecret makes a secret of 32 random bytes, in base64.
func NewSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewNonce makes a random value for one request.
func NewNonce() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system's random source failing is not recoverable
	}
	return hex.EncodeToString(b)
}

// SignedPath is the part of a path that is signed: from /peer/ on, so that an app that lives under a path prefix (and a proxy
// that takes the prefix off) signs and checks the same text.
func SignedPath(p string) string {
	if i := strings.Index(p, "/peer/"); i >= 0 {
		return p[i:]
	}
	return p
}

// Sign is the signature of a request.
func Sign(secret, method, path string, at int64, nonce string, body []byte) string {
	sum := sha256.Sum256(body)
	m := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(m, "%s\n%s\n%d\n%s\n%s", strings.ToUpper(method), path, at, nonce, hex.EncodeToString(sum[:]))
	return hex.EncodeToString(m.Sum(nil))
}

// Verifier checks the signed requests an app receives, and remembers the nonces it has seen for as long as a request could still
// be accepted.
type Verifier struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// Check verifies a request that arrived with these headers against the secret of the peer it claims to be from.
func (v *Verifier) Check(secret string, h http.Header, method, path string, body []byte, now time.Time) error {
	at, err := strconv.ParseInt(h.Get(HeaderTime), 10, 64)
	if err != nil {
		return ErrStale
	}
	if d := now.Sub(time.Unix(at, 0)); d > Skew || d < -Skew {
		return ErrStale
	}
	nonce := h.Get(HeaderNonce)
	if nonce == "" || len(nonce) > 64 {
		return ErrBadSignature
	}
	want := Sign(secret, method, path, at, nonce, body)
	if subtle.ConstantTimeCompare([]byte(want), []byte(h.Get(HeaderSignature))) != 1 {
		return ErrBadSignature
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.seen == nil {
		v.seen = map[string]time.Time{}
	}
	for k, t := range v.seen {
		if now.Sub(t) > 2*Skew {
			delete(v.seen, k)
		}
	}
	key := h.Get(HeaderPeer) + "|" + nonce
	if _, ok := v.seen[key]; ok {
		return ErrReplay
	}
	v.seen[key] = now
	return nil
}

// Call sends a signed JSON request to a peer and reads the JSON answer. base is the peer's address (with any path prefix),
// peerID is the id the peer knows this app by. It returns the status; a status of 400 or more is not an error here, the answer
// is the message the other app gave and the caller decides.
func Call(ctx context.Context, hc *http.Client, base, peerID, secret, method, path string, in, out any) (int, string, error) {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return 0, "", err
		}
	}
	u := strings.TrimSuffix(base, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	at, nonce := time.Now().Unix(), NewNonce()
	// the path that is signed is the one the receiver sees: the part of the address after the host, with any prefix
	signed := SignedPath(req.URL.EscapedPath())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderPeer, peerID)
	req.Header.Set(HeaderTime, strconv.FormatInt(at, 10))
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderSignature, Sign(secret, method, signed, at, nonce, body))
	return do(hc, req, out)
}

// Plain sends an unsigned JSON request (the first steps of an invitation, before there is a secret).
func Plain(ctx context.Context, hc *http.Client, method, u string, in, out any) (int, string, error) {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return 0, "", err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	return do(hc, req, out)
}

// Error is what a peer answers when it refuses.
type Error struct {
	Message string `json:"error"`
}

func do(hc *http.Client, req *http.Request, out any) (int, string, error) {
	res, err := hc.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return res.StatusCode, "", err
	}
	if res.StatusCode >= 400 {
		var e Error
		json.Unmarshal(b, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(b))
		}
		if len(e.Message) > 300 {
			e.Message = e.Message[:300]
		}
		return res.StatusCode, e.Message, nil
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return res.StatusCode, "", fmt.Errorf("peer: an answer that is not JSON: %w", err)
		}
	}
	return res.StatusCode, "", nil
}
