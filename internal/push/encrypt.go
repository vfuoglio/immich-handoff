// Package push sends Web Push messages: the encryption of the message for one browser (RFC 8291, "aes128gcm"), the
// signature that tells the push service who sends it (VAPID, RFC 8292), and the request itself. It uses the standard
// library only and knows nothing else of the app.
package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// Subscription is what a browser gives a page when it agrees to receive pushes.
type Subscription struct {
	Endpoint string // where the push service takes messages for this browser
	P256dh   []byte // the browser's public key (65 bytes, uncompressed P-256)
	Auth     []byte // a secret of 16 bytes shared with the browser
}

// MaxPayload is the most a message may hold: push services take 4096 bytes in all, and the header and the end of the
// encrypted record use the rest.
const MaxPayload = 4096 - 86 - 17

// ErrTooLong means the message would not fit in a push.
var ErrTooLong = errors.New("push: the message is too long")

// Encrypt makes the body of a push to one browser.
func Encrypt(sub Subscription, plaintext []byte) ([]byte, error) {
	eph, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encrypt(sub, plaintext, eph, salt)
}

// encrypt is Encrypt with the random parts given, so the example in the RFC can be reproduced.
func encrypt(sub Subscription, plaintext []byte, eph *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(plaintext) > MaxPayload {
		return nil, ErrTooLong
	}
	if len(sub.Auth) != 16 {
		return nil, errors.New("push: the authentication secret must be 16 bytes")
	}
	ua, err := ecdh.P256().NewPublicKey(sub.P256dh)
	if err != nil {
		return nil, errors.New("push: the browser's key is not a P-256 point")
	}
	secret, err := eph.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPublic := eph.PublicKey().Bytes()

	prkKey, err := hkdf.Extract(sha256.New, secret, sub.Auth)
	if err != nil {
		return nil, err
	}
	keyInfo := "WebPush: info\x00" + string(sub.P256dh) + string(asPublic)
	ikm, err := hkdf.Expand(sha256.New, prkKey, keyInfo, 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	record := aead.Seal(nil, nonce, append(append([]byte{}, plaintext...), 0x02), nil) // 0x02: the last record

	out := make([]byte, 0, 86+len(record))
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, 4096)
	out = append(out, byte(len(asPublic)))
	out = append(out, asPublic...)
	return append(out, record...), nil
}
