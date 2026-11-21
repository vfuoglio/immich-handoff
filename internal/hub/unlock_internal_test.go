package hub

import (
	"testing"
	"time"
)

func TestUnlockTokenExpiresAndCannotBeStretched(t *testing.T) {
	now := time.Now()
	tok := unlockToken("secret", now.Add(time.Hour))
	if !unlockTokenGood("secret", tok, now) {
		t.Fatal("a fresh token was refused")
	}
	if unlockTokenGood("secret", tok, now.Add(2*time.Hour)) {
		t.Error("an expired token was accepted")
	}
	if unlockTokenGood("other", tok, now) {
		t.Error("a token from another password was accepted")
	}
	// the guest lengthens the time in the cookie: the signature no longer fits
	_, sig, _ := cutDot(tok)
	if unlockTokenGood("secret", "99999999999."+sig, now) {
		t.Error("a token with a longer life was accepted")
	}
	for _, bad := range []string{"", "abc", ".", "1.", "x.y"} {
		if unlockTokenGood("secret", bad, now) {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func cutDot(s string) (string, string, bool) {
	for i := range s {
		if s[i] == '.' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
