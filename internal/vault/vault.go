// Package vault encrypts small secrets (API keys) for storage on disk with AES-256-GCM.
//
// A sealed value comes in two forms. The first (what every earlier version wrote) is nonce || ciphertext, with a key
// made from the store key by HKDF. The second, which a vault given a salt (WithSalt) writes, starts with the byte
// 0x02, and its key is made by Argon2id from the store key and a salt kept in the database, so a stolen data file
// cannot be tried against a guessed store key at the speed of a fast hash. Open reads both, so nothing has to be
// converted when the app is updated; `rotate-key` converts everything and can change the store key too.
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

	"golang.org/x/crypto/argon2"
)

// version2 is the first byte of a value sealed with the salted key.
const version2 = 0x02

// The Argon2id cost: the minimum the OWASP guide gives (19 MiB, two passes), about 40 ms once at start-up.
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
)

// Vault seals and opens secrets with a key derived from a passphrase-like secret supplied at startup.
type Vault struct {
	aead   cipher.AEAD // the first form; nil never
	key    []byte
	secret string
	salted cipher.AEAD // the second form, once WithSalt has been called
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
	return &Vault{aead: aead, key: key, secret: secret}, nil
}

// SaltSize is the length of the salt WithSalt wants.
const SaltSize = 16

// NewSalt makes a random salt for a database.
func NewSalt() ([]byte, error) {
	b := make([]byte, SaltSize)
	_, err := rand.Read(b)
	return b, err
}

// WithSalt returns a vault that seals with the key made from the store key and salt (Argon2id), and still opens what
// the first form sealed. The salt is not secret: it belongs with the data.
func (v *Vault) WithSalt(salt []byte) (*Vault, error) {
	if len(salt) < SaltSize {
		return nil, errors.New("vault: the salt is too short")
	}
	key := argon2.IDKey([]byte(v.secret), salt, argonTime, argonMemory, argonThreads, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	out := *v
	out.salted = aead
	return &out, nil
}

// Salted reports whether Seal writes the second form.
func (v *Vault) Salted() bool { return v.salted != nil }

// Current reports whether sealed is in the form Seal writes now.
func (v *Vault) Current(sealed []byte) bool {
	return (v.salted != nil) == (len(sealed) > 0 && sealed[0] == version2)
}

// Seal returns nonce || ciphertext. context is authenticated but not stored: opening with a
// different context fails, so a sealed key cannot be moved to another row undetected.
func (v *Vault) Seal(plain []byte, context string) ([]byte, error) {
	if v.salted != nil {
		nonce := make([]byte, 1+v.salted.NonceSize())
		nonce[0] = version2
		if _, err := rand.Read(nonce[1:]); err != nil {
			return nil, err
		}
		return v.salted.Seal(nonce, nonce[1:], plain, []byte(context)), nil
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return v.aead.Seal(nonce, nonce, plain, []byte(context)), nil
}

func (v *Vault) Open(sealed []byte, context string) ([]byte, error) {
	if v.salted != nil && len(sealed) > 0 && sealed[0] == version2 {
		n := v.salted.NonceSize()
		if len(sealed) >= 1+n+v.salted.Overhead() {
			if plain, err := v.salted.Open(nil, sealed[1:1+n], sealed[1+n:], []byte(context)); err == nil {
				return plain, nil
			}
		}
		// a value of the first form can start with the byte 0x02 too: the authentication tells them apart
	}
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

// DeriveKey makes a 256-bit key for one purpose from the store key and a salt, with Argon2id. A backup file keeps its
// own salt beside it, so it can be opened with the store key alone, without the database it came from.
func (v *Vault) DeriveKey(purpose string, salt []byte) []byte {
	return argon2.IDKey([]byte(v.secret), append([]byte(purpose+"|"), salt...), argonTime, argonMemory, argonThreads, 32)
}
