package vault

import "testing"

func TestRoundTripAndTamper(t *testing.T) {
	v, err := New("a-long-enough-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := v.Seal([]byte("api-key-123"), "link-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(sealed) == "api-key-123" || len(sealed) <= len("api-key-123") {
		t.Fatal("not encrypted")
	}
	got, err := v.Open(sealed, "link-1")
	if err != nil || string(got) != "api-key-123" {
		t.Fatalf("open: %q %v", got, err)
	}
	if _, err := v.Open(sealed, "link-2"); err == nil {
		t.Error("a sealed key must not open under another context")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := v.Open(sealed, "link-1"); err == nil {
		t.Error("tampering must be detected")
	}
	other, _ := New("a-different-long-enough-secret")
	fresh, _ := v.Seal([]byte("x"), "c")
	if _, err := other.Open(fresh, "c"); err == nil {
		t.Error("wrong key must not open the value")
	}
	if _, err := New("short"); err == nil {
		t.Error("short store keys must be rejected")
	}
	a, _ := v.Seal([]byte("same"), "c")
	b, _ := v.Seal([]byte("same"), "c")
	if string(a) == string(b) {
		t.Error("nonce must differ per seal")
	}
}
