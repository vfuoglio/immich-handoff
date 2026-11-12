package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"time"
)

// Keys are the application server's own key pair. The push service learns the public half and uses it to know that
// pushes for a browser always come from the same sender.
type Keys struct{ priv *ecdsa.PrivateKey }

// NewKeys makes a new pair.
func NewKeys() (*Keys, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Keys{k}, nil
}

// KeysFromPrivate reads a pair from its private half (32 bytes), as Private writes it.
func KeysFromPrivate(raw []byte) (*Keys, error) {
	k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		return nil, errors.New("push: the stored key is not a P-256 private key")
	}
	return &Keys{k}, nil
}

// Private is the private half, to be stored with care.
func (k *Keys) Private() ([]byte, error) { return k.priv.Bytes() }

// Public is the public half, 65 bytes.
func (k *Keys) Public() ([]byte, error) { return k.priv.PublicKey.Bytes() }

// PublicB64 is the public half as the browser wants it (base64url, no padding).
func (k *Keys) PublicB64() string {
	b, _ := k.Public()
	return base64.RawURLEncoding.EncodeToString(b)
}

// Authorization is the header value that tells a push service who is sending: a signed token for the service's own
// address (aud), valid for twelve hours, with a way to reach whoever runs the app (subject: a mailto: or https: URL).
func (k *Keys) Authorization(audience, subject string, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"typ": "JWT", "alg": "ES256"})
	claims, _ := json.Marshal(map[string]any{"aud": audience, "exp": now.Add(12 * time.Hour).Unix(), "sub": subject})
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	der, err := ecdsa.SignASN1(rand.Reader, k.priv, sum[:])
	if err != nil {
		return "", err
	}
	var sig struct{ R, S *big.Int }
	if _, err := asn1.Unmarshal(der, &sig); err != nil {
		return "", err
	}
	raw := make([]byte, 64) // the token wants r and s as two 32-byte numbers, not DER
	sig.R.FillBytes(raw[:32])
	sig.S.FillBytes(raw[32:])
	return "vapid t=" + signing + "." + enc.EncodeToString(raw) + ", k=" + k.PublicB64(), nil
}
