package peer

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func signed(secret string, at time.Time, nonce, method, path string, body []byte) http.Header {
	h := http.Header{}
	h.Set(HeaderPeer, "p1")
	h.Set(HeaderTime, strconv.FormatInt(at.Unix(), 10))
	h.Set(HeaderNonce, nonce)
	h.Set(HeaderSignature, Sign(secret, method, path, at.Unix(), nonce, body))
	return h
}

func TestARequestIsAcceptedOnceWithTheRightSecretAndNeverChangedOrReplayed(t *testing.T) {
	secret, _ := NewSecret()
	now := time.Now()
	var v Verifier
	body := []byte(`{"a":1}`)
	h := signed(secret, now, "n1", "POST", "/peer/v1/hello", body)
	if err := v.Check(secret, h, "POST", "/peer/v1/hello", body, now); err != nil {
		t.Fatal(err)
	}
	if err := v.Check(secret, h, "POST", "/peer/v1/hello", body, now); err != ErrReplay {
		t.Errorf("the same request again: %v", err)
	}
	for name, c := range map[string]struct {
		secret, method, path string
		body                 []byte
	}{
		"another secret": {"other", "POST", "/peer/v1/hello", body},
		"another path":   {secret, "POST", "/peer/v1/leave", body},
		"another method": {secret, "GET", "/peer/v1/hello", body},
		"another body":   {secret, "POST", "/peer/v1/hello", []byte(`{"a":2}`)},
	} {
		h := signed(secret, now, "n-"+name, "POST", "/peer/v1/hello", body)
		if err := v.Check(c.secret, h, c.method, c.path, c.body, now); err != ErrBadSignature {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestARequestFromTheWrongTimeOrWithoutAValidHeaderIsRefused(t *testing.T) {
	secret, _ := NewSecret()
	now := time.Now()
	var v Verifier
	for name, at := range map[string]time.Time{"an hour old": now.Add(-time.Hour), "from the future": now.Add(time.Hour)} {
		h := signed(secret, at, "n-"+name, "POST", "/p", nil)
		if err := v.Check(secret, h, "POST", "/p", nil, now); err != ErrStale {
			t.Errorf("%s: %v", name, err)
		}
	}
	ok := signed(secret, now.Add(-4*time.Minute), "fine", "POST", "/p", nil)
	if err := v.Check(secret, ok, "POST", "/p", nil, now); err != nil {
		t.Errorf("within the skew: %v", err)
	}
	if err := v.Check(secret, http.Header{}, "POST", "/p", nil, now); err != ErrStale {
		t.Errorf("no headers: %v", err)
	}
	h := signed(secret, now, "", "POST", "/p", nil)
	if err := v.Check(secret, h, "POST", "/p", nil, now); err != ErrBadSignature {
		t.Errorf("no nonce: %v", err)
	}
}

func TestSecretsAndNoncesAreLongAndDifferent(t *testing.T) {
	a, _ := NewSecret()
	b, _ := NewSecret()
	if len(a) < 40 || a == b || NewNonce() == NewNonce() {
		t.Errorf("%q %q", a, b)
	}
}

func TestOldNoncesAreForgottenSoTheListStaysSmall(t *testing.T) {
	secret, _ := NewSecret()
	now := time.Now()
	var v Verifier
	for i := 0; i < 50; i++ {
		h := signed(secret, now, "n"+strconv.Itoa(i), "POST", "/p", nil)
		if err := v.Check(secret, h, "POST", "/p", nil, now); err != nil {
			t.Fatal(err)
		}
	}
	later := now.Add(3 * Skew)
	h := signed(secret, later, "late", "POST", "/p", nil)
	v.Check(secret, h, "POST", "/p", nil, later)
	if len(v.seen) != 1 {
		t.Errorf("%d nonces kept", len(v.seen))
	}
}

func TestOnlyThePartFromPeerOnIsSigned(t *testing.T) {
	for in, want := range map[string]string{
		"/peer/v1/hello":         "/peer/v1/hello",
		"/handoff/peer/v1/hello": "/peer/v1/hello",
		"/a/b/peer/v1/join/abc":  "/peer/v1/join/abc",
		"/nothing":               "/nothing",
	} {
		if got := SignedPath(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}
