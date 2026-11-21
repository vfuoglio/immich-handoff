package vault

import (
	"bytes"
	"testing"
)

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

func TestAMACBelongsToOnePurposeAndOneKey(t *testing.T) {
	a, _ := New("0123456789abcdef-one")
	b, _ := New("0123456789abcdef-two")
	m := a.MAC("unsubscribe", []byte("x"))
	if len(m) != 32 || !bytes.Equal(m, a.MAC("unsubscribe", []byte("x"))) {
		t.Fatal("not stable")
	}
	if bytes.Equal(m, a.MAC("other use", []byte("x"))) || bytes.Equal(m, b.MAC("unsubscribe", []byte("x"))) || bytes.Equal(m, a.MAC("unsubscribe", []byte("y"))) {
		t.Error("a signature must differ by purpose, key and message")
	}
}

func TestSaltedFormatIsReadAlongsideTheFirst(t *testing.T) {
	old, err := New("0123456789abcdef-store-key")
	if err != nil {
		t.Fatal(err)
	}
	first, _ := old.Seal([]byte("api-key-1"), "ctx")
	salt, _ := NewSalt()
	v, err := old.WithSalt(salt)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Salted() || old.Salted() {
		t.Fatal("only the vault given a salt is salted")
	}
	second, err := v.Seal([]byte("api-key-2"), "ctx")
	if err != nil || second[0] != version2 {
		t.Fatalf("sealed = %v %v", second, err)
	}
	if got, err := v.Open(first, "ctx"); err != nil || string(got) != "api-key-1" {
		t.Errorf("a value of the first form: %q %v", got, err)
	}
	if got, err := v.Open(second, "ctx"); err != nil || string(got) != "api-key-2" {
		t.Errorf("a value of the second form: %q %v", got, err)
	}
	if _, err := v.Open(second, "other"); err == nil {
		t.Error("the context is not authenticated in the second form")
	}
	if _, err := old.Open(second, "ctx"); err == nil {
		t.Error("a vault without the salt opened the second form")
	}
	if v.Current(first) || !v.Current(second) || !old.Current(first) {
		t.Error("Current does not tell the forms apart")
	}
	// another salt, another key
	salt2, _ := NewSalt()
	v2, _ := old.WithSalt(salt2)
	if _, err := v2.Open(second, "ctx"); err == nil {
		t.Error("a different salt opened the value")
	}
	// a first-form value whose first byte happens to be 0x02 still opens
	for i := 0; i < 2000; i++ {
		b, _ := old.Seal([]byte("x"), "c")
		if b[0] == version2 {
			if _, err := v.Open(b, "c"); err != nil {
				t.Fatalf("first-form value starting with 0x02: %v", err)
			}
			return
		}
	}
}
