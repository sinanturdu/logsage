// Package password hashes and verifies user passwords with argon2id
// (ADR 0013). Hashes are stored as PHC strings:
//
//	$argon2id$v=19$m=<KiB>,t=<iterations>,p=<parallelism>$<salt b64>$<hash b64>
//
// Passwords and hashes are never logged or returned.
package password

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Params are argon2id cost parameters.
type Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLen     uint32
	KeyLen      uint32
}

// OWASP's minimum argon2id configuration is m=19 MiB, t=2, p=1.
const (
	MinMemoryKiB = 19 * 1024
	saltLen      = 16
	keyLen       = 32
)

// DefaultParams were benchmarked for 50-150 ms per hash on the reference
// laptop (docs/adr/0013): 64 MiB, 3 iterations, 2 lanes.
func DefaultParams() Params {
	return Params{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLen: saltLen, KeyLen: keyLen}
}

// Validate rejects parameters below the OWASP minimum.
func (p Params) Validate() error {
	switch {
	case p.MemoryKiB < MinMemoryKiB:
		return fmt.Errorf("argon2 memory %d KiB is below the minimum %d KiB", p.MemoryKiB, MinMemoryKiB)
	case p.Iterations < 1 || p.Iterations > 20:
		return errors.New("argon2 iterations must be 1..20")
	case p.Parallelism < 1 || p.Parallelism > 16:
		return errors.New("argon2 parallelism must be 1..16")
	case p.SaltLen < 16 || p.KeyLen < 32:
		return errors.New("argon2 salt must be >= 16 bytes and key >= 32 bytes")
	}
	return nil
}

// Policy limits.
const (
	MinLength = 12
	MaxLength = 128 // characters; also bounds hashing cost
)

// ErrPolicy is wrapped by CheckPolicy errors; the message is safe to show.
var ErrPolicy = errors.New("password does not meet the policy")

// CheckPolicy enforces length 12-128 characters and "not equal to the
// email". There are no composition rules (NIST SP 800-63B).
func CheckPolicy(pw, email string) error {
	n := utf8.RuneCountInString(pw)
	switch {
	case !utf8.ValidString(pw):
		return fmt.Errorf("%w: must be valid UTF-8", ErrPolicy)
	case n < MinLength:
		return fmt.Errorf("%w: at least %d characters", ErrPolicy, MinLength)
	case n > MaxLength:
		return fmt.Errorf("%w: at most %d characters", ErrPolicy, MaxLength)
	case email != "" && strings.EqualFold(strings.TrimSpace(pw), strings.TrimSpace(email)):
		return fmt.Errorf("%w: must not be the email address", ErrPolicy)
	}
	return nil
}

var b64 = base64.RawStdEncoding

// encode returns the PHC string for p, salt and key.
func encode(p Params, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Iterations, p.Parallelism, b64.EncodeToString(salt), b64.EncodeToString(key))
}

// ErrMalformed means a stored hash is not a PHC argon2id string we can read.
var ErrMalformed = errors.New("malformed password hash")

// Parse reads a PHC argon2id string.
func Parse(phc string) (Params, []byte, []byte, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Params{}, nil, nil, ErrMalformed
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return Params{}, nil, nil, ErrMalformed
	}
	var p Params
	var par uint32
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		n, err := strconv.ParseUint(v, 10, 32)
		if !ok || err != nil {
			return Params{}, nil, nil, ErrMalformed
		}
		switch k {
		case "m":
			p.MemoryKiB = uint32(n)
		case "t":
			p.Iterations = uint32(n)
		case "p":
			par = uint32(n)
		default:
			return Params{}, nil, nil, ErrMalformed
		}
	}
	if p.MemoryKiB == 0 || p.Iterations == 0 || par == 0 || par > 255 {
		return Params{}, nil, nil, ErrMalformed
	}
	p.Parallelism = uint8(par)
	salt, err1 := b64.DecodeString(parts[4])
	key, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(salt) < 8 || len(key) < 16 {
		return Params{}, nil, nil, ErrMalformed
	}
	p.SaltLen, p.KeyLen = uint32(len(salt)), uint32(len(key))
	return p, salt, key, nil
}

// Hasher hashes and verifies with a bound on concurrent computations, so a
// login flood cannot allocate more than MaxConcurrent x MemoryKiB.
type Hasher struct {
	params Params
	sem    chan struct{}
	dummy  string // hash of a random password, verified for unknown emails
}

// NewHasher validates params and precomputes the dummy hash.
func NewHasher(params Params, maxConcurrent int) (*Hasher, error) {
	if err := params.Validate(); err != nil {
		return nil, err
	}
	if maxConcurrent < 1 {
		return nil, errors.New("max concurrent hashes must be at least 1")
	}
	h := &Hasher{params: params, sem: make(chan struct{}, maxConcurrent)}
	rnd := make([]byte, 32)
	if _, err := rand.Read(rnd); err != nil {
		return nil, err
	}
	d, err := h.hash(rnd)
	if err != nil {
		return nil, err
	}
	h.dummy = d
	return h, nil
}

// Params returns the configured parameters.
func (h *Hasher) Params() Params { return h.params }

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.sem }

func (h *Hasher) hash(pw []byte) (string, error) {
	salt := make([]byte, h.params.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: read salt: %w", err)
	}
	key := argon2.IDKey(pw, salt, h.params.Iterations, h.params.MemoryKiB, h.params.Parallelism, h.params.KeyLen)
	return encode(h.params, salt, key), nil
}

// Hash returns a PHC string for pw with a fresh random salt.
func (h *Hasher) Hash(ctx context.Context, pw string) (string, error) {
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	return h.hash([]byte(pw))
}

// Verify reports whether pw matches phc. needsRehash is true when phc was
// made with parameters other than the configured ones (rehash on login).
// An unreadable phc is a mismatch, not a panic.
func (h *Hasher) Verify(ctx context.Context, pw, phc string) (ok, needsRehash bool, err error) {
	p, salt, want, perr := Parse(phc)
	if perr != nil {
		return false, false, perr
	}
	if err := h.acquire(ctx); err != nil {
		return false, false, err
	}
	defer h.release()
	got := argon2.IDKey([]byte(pw), salt, p.Iterations, p.MemoryKiB, p.Parallelism, uint32(len(want)))
	ok = subtle.ConstantTimeCompare(got, want) == 1
	needsRehash = p.MemoryKiB != h.params.MemoryKiB || p.Iterations != h.params.Iterations ||
		p.Parallelism != h.params.Parallelism || p.KeyLen != h.params.KeyLen || p.SaltLen != h.params.SaltLen
	return ok, needsRehash, nil
}

// VerifyDummy spends the same work as a real verification and always
// fails; use it when the account does not exist so timing does not reveal
// which emails are registered.
func (h *Hasher) VerifyDummy(ctx context.Context, pw string) {
	_, _, _ = h.Verify(ctx, pw, h.dummy)
}
