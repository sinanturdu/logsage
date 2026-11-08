// Package secret encrypts channel secrets and URLs at rest (AES-256-GCM) and
// signs webhook deliveries (HMAC-SHA256), ADR 0024.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// KeyID is written in front of every ciphertext so a second key can be
// added for rotation later; K1 only knows key 1.
const KeyID byte = 1

var (
	ErrKey       = errors.New("secret: ALERTS_SECRET_KEY must be 32 bytes, base64-encoded")
	ErrDecrypt   = errors.New("secret: cannot decrypt (wrong key, other row or tampered data)")
	ErrSignature = errors.New("secret: signature does not match")
	ErrTimestamp = errors.New("secret: signature timestamp outside the allowed skew")
	ErrHeader    = errors.New("secret: malformed signature header")
)

// ParseKey decodes the base64 key from the environment (standard or URL
// alphabet, padded or not). The key is never logged.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if k, err := enc.DecodeString(s); err == nil && len(k) == 32 {
			return k, nil
		}
	}
	return nil, ErrKey
}

// Sealer encrypts and decrypts with AES-256-GCM. The additional data binds a
// ciphertext to its row (project and channel ids), so it cannot be copied to
// another one.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer takes a 32-byte key.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != 32 {
		return nil, ErrKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// Seal returns keyID || nonce || ciphertext+tag, with a fresh random nonce.
func (s *Sealer) Seal(plain, ad []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secret: nonce: %w", err)
	}
	out := append([]byte{KeyID}, nonce...)
	return s.aead.Seal(out, nonce, plain, append([]byte{KeyID}, ad...)), nil
}

// Open reverses Seal; any change to the data, the key id or the additional
// data fails.
func (s *Sealer) Open(sealed, ad []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < 1+n+s.aead.Overhead() || sealed[0] != KeyID {
		return nil, ErrDecrypt
	}
	plain, err := s.aead.Open(nil, sealed[1:1+n], sealed[1+n:], append([]byte{KeyID}, ad...))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}

// AD is the additional data for a channel's sealed fields.
func AD(projectID, channelID, field string) []byte {
	return []byte(projectID + "\x00" + channelID + "\x00" + field)
}

// NewWebhookSecret returns a signing secret: "lsw_" and 32 random bytes,
// base64url. It is shown to the user once and stored sealed.
func NewWebhookSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("secret: %w", err)
	}
	return "lsw_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// Sign returns the X-LogSage-Signature value: "t=<unix>,v1=<hex>", where
// v1 is HMAC-SHA256 with the secret over "<unix>.<body>".
func Sign(secret string, at time.Time, body []byte) string {
	t := strconv.FormatInt(at.Unix(), 10)
	return "t=" + t + ",v1=" + hex.EncodeToString(mac(secret, t, body))
}

func mac(secret, t string, body []byte) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(t))
	m.Write([]byte("."))
	m.Write(body)
	return m.Sum(nil)
}

// Verify checks a signature header the way a receiver should: the timestamp
// within skew of now, and v1 equal in constant time. Several v1 values are
// accepted (one may come from a rotated secret).
func Verify(secret, header string, body []byte, now time.Time, skew time.Duration) error {
	var t string
	var sigs [][]byte
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return ErrHeader
		}
		switch k {
		case "t":
			t = v
		case "v1":
			b, err := hex.DecodeString(v)
			if err != nil || len(b) != sha256.Size {
				return ErrHeader
			}
			sigs = append(sigs, b)
		}
	}
	sec, err := strconv.ParseInt(t, 10, 64)
	if err != nil || len(sigs) == 0 {
		return ErrHeader
	}
	if d := now.Sub(time.Unix(sec, 0)); d > skew || d < -skew {
		return ErrTimestamp
	}
	want := mac(secret, t, body)
	for _, s := range sigs {
		if hmac.Equal(s, want) {
			return nil
		}
	}
	return ErrSignature
}
