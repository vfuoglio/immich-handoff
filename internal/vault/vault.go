// Package vault encrypts small secrets (API keys) for storage on disk with AES-256-GCM.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// Vault seals and opens secrets with a key derived from a passphrase-like secret supplied at startup.
type Vault struct {
	aead cipher.AEAD
	key  []byte
}

var errShort = errors.New("vault: sealed value too short")

// New derives a 256-bit key from secret (any string of at least 16 characters).
func New(secret string) (*Vault, error) {
	if len(secret) < 16 {
		return nil, errors.New("vault: the store key must be at least 16 characters long")
	}
	key, err := hkdf.Key(sha256.New, []byte(secret), []byte("immich-handoff vault v1"), "api-key encryption", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{aead: aead, key: key}, nil
}

// Seal returns nonce || ciphertext. context is authenticated but not stored: opening with a
// different context fails, so a sealed key cannot be moved to another row undetected.
func (v *Vault) Seal(plain []byte, context string) ([]byte, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return v.aead.Seal(nonce, nonce, plain, []byte(context)), nil
}

func (v *Vault) Open(sealed []byte, context string) ([]byte, error) {
	n := v.aead.NonceSize()
	if len(sealed) < n+v.aead.Overhead() {
		return nil, errShort
	}
	plain, err := v.aead.Open(nil, sealed[:n], sealed[n:], []byte(context))
	if err != nil {
		return nil, fmt.Errorf("vault: cannot decrypt (wrong store key, or the data was altered)")
	}
	return plain, nil
}

// MAC signs msg with a key derived from the store key for one purpose only, so a signature made for one use (a link in
// a mail, say) is worth nothing for another.
func (v *Vault) MAC(purpose string, msg []byte) []byte {
	sub, err := hkdf.Key(sha256.New, v.key, []byte("immich-handoff mac v1"), purpose, 32)
	if err != nil {
		panic(err) // only for a length that is too big
	}
	m := hmac.New(sha256.New, sub)
	m.Write(msg)
	return m.Sum(nil)
}
