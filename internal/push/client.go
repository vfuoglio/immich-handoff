package push

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Errors a send can end in.
var (
	// ErrGone: the push service says this subscription no longer exists (the person revoked it or cleared the browser).
	ErrGone = errors.New("push: the subscription is gone")
	// ErrTooLarge: the push service refused the size of the message.
	ErrTooLarge = errors.New("push: the message is too large for the push service")
	// ErrHost: the address is not one of the push services we send to.
	ErrHost = errors.New("push: that is not the address of a known push service")
	// ErrTemporary: the push service could not be reached or was busy, and trying again did not help.
	ErrTemporary = errors.New("push: the push service is not answering")
)

// RejectedError is any other refusal. 401 and 403 mean the push service did not accept our key or contact.
type RejectedError struct{ Code int }

func (e *RejectedError) Error() string {
	return fmt.Sprintf("push: the push service refused it (%d)", e.Code)
}

// AllowedEndpoint says whether an address is one we may send pushes to: https, no login in the address, and the host
// of one of the push services browsers use (Google, Mozilla, Apple, Microsoft). Anything else could be used to make
// this server call an address of a person's choosing.
func AllowedEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" || u.Fragment != "" {
		return ErrHost
	}
	if p := u.Port(); p != "" && p != "443" {
		return ErrHost
	}
	host := strings.ToLower(u.Hostname())
	if net.ParseIP(host) != nil || strings.HasSuffix(host, ".") {
		return ErrHost
	}
	switch host {
	case "fcm.googleapis.com", "web.push.apple.com":
		return nil
	}
	for _, suffix := range []string{".push.services.mozilla.com", ".push.apple.com", ".notify.windows.com"} {
		if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
			return nil
		}
	}
	return ErrHost
}

// Client sends pushes.
type Client struct {
	Keys    *Keys
	Subject string       // how the push service can reach whoever runs the app: a mailto: or https: URL
	HTTP    *http.Client // nil: one with a 15-second limit that follows no redirect
	// Allow decides which addresses are sent to; nil: AllowedEndpoint. Only tests replace it.
	Allow func(string) error
	// Wait is the pause before each new attempt after a failure that may pass (default 2 s, then 10 s).
	Wait []time.Duration
	Now  func() time.Time
}

// Options of one push.
type Options struct {
	TTL     time.Duration // how long the push service keeps it for a browser that is off (default a day)
	Urgency string        // very-low, low, normal or high
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Send encrypts payload for the browser and hands it to its push service.
func (c *Client) Send(ctx context.Context, sub Subscription, payload []byte, o Options) error {
	allow := c.Allow
	if allow == nil {
		allow = AllowedEndpoint
	}
	if err := allow(sub.Endpoint); err != nil { // checked again here, whatever was checked when it was stored
		return err
	}
	u, err := url.Parse(sub.Endpoint)
	if err != nil {
		return ErrHost
	}
	body, err := Encrypt(sub, payload)
	if err != nil {
		return err
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	auth, err := c.Keys.Authorization(u.Scheme+"://"+u.Host, c.Subject, now())
	if err != nil {
		return err
	}
	ttl := o.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	wait := c.Wait
	if wait == nil {
		wait = []time.Duration{2 * time.Second, 10 * time.Second}
	}
	for attempt := 0; ; attempt++ {
		retryAfter, err := c.once(ctx, sub.Endpoint, body, auth, ttl, o.Urgency)
		if err == nil || !errors.Is(err, ErrTemporary) || attempt >= len(wait) {
			return err
		}
		pause := wait[attempt]
		if retryAfter > pause && retryAfter <= time.Minute {
			pause = retryAfter
		}
		select {
		case <-time.After(pause):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Client) once(ctx context.Context, endpoint string, body []byte, auth string, ttl time.Duration, urgency string) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, ErrHost
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(ttl.Seconds())))
	if urgency != "" {
		req.Header.Set("Urgency", urgency)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return 0, ErrTemporary // the error text holds the address: not passed on
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return 0, nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return 0, ErrGone
	case resp.StatusCode == http.StatusRequestEntityTooLarge:
		return 0, ErrTooLarge
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		var after time.Duration
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			after = time.Duration(s) * time.Second
		}
		return after, ErrTemporary
	}
	return 0, &RejectedError{resp.StatusCode}
}
