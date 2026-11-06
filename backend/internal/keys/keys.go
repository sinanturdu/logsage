// Package keys generates, parses and verifies LogSage API keys.
//
// Format: lsk_<kind>_<prefix>_<secret>
//
//	kind    "ing" (ingest) or "qry" (query)
//	prefix  8 characters, lowercase base32 (a-z, 2-7): 40 random bits, stored
//	        in clear and used to look the key up
//	secret  32 random bytes, base64url without padding (43 characters):
//	        256 bits, never stored
//
// Only the prefix and the SHA-256 of the whole key are stored. See ADR 0011.
package keys

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Kind says which API a key may be used for.
type Kind string

const (
	KindIngest Kind = "ingest"
	KindQuery  Kind = "query"
)

// ParseKind accepts "ingest" or "query".
func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case KindIngest, KindQuery:
		return Kind(s), nil
	}
	return "", fmt.Errorf("unknown key kind %q: want ingest or query", s)
}

func (k Kind) code() string {
	if k == KindQuery {
		return "qry"
	}
	return "ing"
}

func kindFromCode(c string) (Kind, bool) {
	switch c {
	case "ing":
		return KindIngest, true
	case "qry":
		return KindQuery, true
	}
	return "", false
}

const (
	keyPrefix    = "lsk"
	prefixBytes  = 5  // 40 bits -> 8 base32 characters
	secretBytes  = 32 // 256 bits -> 43 base64url characters
	prefixLen    = 8
	secretLen    = 43
	maxKeyLength = 64 // 3 + 1 + 3 + 1 + 8 + 1 + 43 = 60, with slack
)

var prefixEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// Generated is a new key. Plaintext must be shown to the operator once and
// then dropped: it is never stored or logged.
type Generated struct {
	Plaintext string
	Kind      Kind
	Prefix    string
	Hash      [sha256.Size]byte
}

// Generate creates a key of kind using crypto/rand.
func Generate(kind Kind) (Generated, error) {
	return GenerateFrom(rand.Reader, kind)
}

// GenerateFrom creates a key reading randomness from r. It fails rather than
// fall back to a weaker source when r fails.
func GenerateFrom(r io.Reader, kind Kind) (Generated, error) {
	if _, err := ParseKind(string(kind)); err != nil {
		return Generated{}, err
	}
	buf := make([]byte, prefixBytes+secretBytes)
	if _, err := io.ReadFull(r, buf); err != nil {
		return Generated{}, fmt.Errorf("keys: read randomness: %w", err)
	}
	prefix := prefixEncoding.EncodeToString(buf[:prefixBytes])
	secret := base64.RawURLEncoding.EncodeToString(buf[prefixBytes:])
	plain := keyPrefix + "_" + kind.code() + "_" + prefix + "_" + secret
	return Generated{Plaintext: plain, Kind: kind, Prefix: prefix, Hash: Hash(plain)}, nil
}

// Hash is the stored form of a key: SHA-256 of the whole plaintext. A fast
// hash is enough because keys carry 256 random bits (ADR 0011).
func Hash(plaintext string) [sha256.Size]byte {
	return sha256.Sum256([]byte(plaintext))
}

// errMalformed is internal; callers only ever see ErrInvalidKey.
var errMalformed = errors.New("malformed key")

// Parse splits a key into kind and prefix. The secret may itself contain
// "_" (base64url), so only the first three separators are used.
func Parse(plaintext string) (Kind, string, error) {
	if len(plaintext) == 0 || len(plaintext) > maxKeyLength {
		return "", "", errMalformed
	}
	parts := strings.SplitN(plaintext, "_", 4)
	if len(parts) != 4 || parts[0] != keyPrefix {
		return "", "", errMalformed
	}
	kind, ok := kindFromCode(parts[1])
	if !ok {
		return "", "", errMalformed
	}
	prefix, secret := parts[2], parts[3]
	if len(prefix) != prefixLen || !validPrefix(prefix) {
		return "", "", errMalformed
	}
	if len(secret) != secretLen {
		return "", "", errMalformed
	}
	if raw, err := base64.RawURLEncoding.Strict().DecodeString(secret); err != nil || len(raw) != secretBytes {
		return "", "", errMalformed
	}
	return kind, prefix, nil
}

func validPrefix(p string) bool {
	for _, c := range p {
		if !(c >= 'a' && c <= 'z' || c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}

// Redact returns a key reduced to its public part, for messages:
// "lsk_ing_abcdefgh_…". Anything that does not parse becomes "(invalid key)".
func Redact(plaintext string) string {
	kind, prefix, err := Parse(plaintext)
	if err != nil {
		return "(invalid key)"
	}
	return keyPrefix + "_" + kind.code() + "_" + prefix + "_…"
}
