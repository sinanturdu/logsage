package secret

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var key = bytes.Repeat([]byte{7}, 32)

func TestParseKey(t *testing.T) {
	for _, s := range []string{base64.StdEncoding.EncodeToString(key), base64.RawURLEncoding.EncodeToString(key), " " + base64.StdEncoding.EncodeToString(key) + "\n"} {
		k, err := ParseKey(s)
		if err != nil || !bytes.Equal(k, key) {
			t.Errorf("ParseKey(%q) = %v", s, err)
		}
	}
	for _, s := range []string{"", "short", base64.StdEncoding.EncodeToString(key[:16]), base64.StdEncoding.EncodeToString(append(key, 1)), "!!!"} {
		if _, err := ParseKey(s); !errors.Is(err, ErrKey) {
			t.Errorf("ParseKey(%q) = %v, want ErrKey", s, err)
		}
	}
	if _, err := NewSealer(key[:31]); !errors.Is(err, ErrKey) {
		t.Error("a 31-byte key must be refused")
	}
}

func TestSealOpen(t *testing.T) {
	s, _ := NewSealer(key)
	ad := AD("project-1", "channel-1", "secret")
	sealed, err := s.Seal([]byte("lsw_abc"), ad)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("lsw_abc")) || sealed[0] != KeyID {
		t.Fatalf("sealed %x", sealed)
	}
	got, err := s.Open(sealed, ad)
	if err != nil || string(got) != "lsw_abc" {
		t.Fatalf("Open = %q, %v", got, err)
	}

	other, _ := NewSealer(bytes.Repeat([]byte{8}, 32))
	cases := map[string]func() ([]byte, error){
		"wrong key":     func() ([]byte, error) { return other.Open(sealed, ad) },
		"other channel": func() ([]byte, error) { return s.Open(sealed, AD("project-1", "channel-2", "secret")) },
		"other project": func() ([]byte, error) { return s.Open(sealed, AD("project-2", "channel-1", "secret")) },
		"other field":   func() ([]byte, error) { return s.Open(sealed, AD("project-1", "channel-1", "url")) },
		"short":         func() ([]byte, error) { return s.Open(sealed[:10], ad) },
		"empty":         func() ([]byte, error) { return s.Open(nil, ad) },
	}
	for i := range sealed {
		i := i
		cases[fmt.Sprintf("flip byte %d", i)] = func() ([]byte, error) {
			b := bytes.Clone(sealed)
			b[i] ^= 0x01
			return s.Open(b, ad)
		}
	}
	for name, open := range cases {
		if _, err := open(); !errors.Is(err, ErrDecrypt) {
			t.Errorf("%s: err = %v, want ErrDecrypt", name, err)
		}
	}
}

func TestNoncesAreUnique(t *testing.T) {
	s, _ := NewSealer(key)
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		b, err := s.Seal([]byte("same"), nil)
		if err != nil {
			t.Fatal(err)
		}
		nonce := string(b[1:13])
		if seen[nonce] {
			t.Fatalf("nonce repeated after %d seals", i)
		}
		seen[nonce] = true
	}
}

func TestWebhookSecret(t *testing.T) {
	a, _ := NewWebhookSecret()
	b, _ := NewWebhookSecret()
	if !strings.HasPrefix(a, "lsw_") || len(a) != 4+43 || a == b {
		t.Fatalf("secrets %q %q", a, b)
	}
}

// Vectors computed independently (Python hmac/hashlib).
func TestSignKnownVectors(t *testing.T) {
	at := time.Unix(1762600000, 0)
	if got := Sign("lsw_test-secret", at, []byte(`{"version":1}`)); got != "t=1762600000,v1=e9aff3fd84d7701a427ffd4f71cf0ed4549ac234efdd790edb9efd08c7f72f59" {
		t.Fatalf("Sign = %s", got)
	}
	if got := Sign("key", time.Unix(1700000000, 0), nil); got != "t=1700000000,v1=0f1cc1f811f42fd12af9618acf321769899fa521fe07a642f70a61785e130770" {
		t.Fatalf("Sign empty body = %s", got)
	}
}

func TestVerify(t *testing.T) {
	at := time.Unix(1762600000, 0)
	body := []byte(`{"version":1}`)
	h := Sign("s1", at, body)
	ok := []struct {
		header string
		now    time.Time
	}{
		{h, at},
		{h, at.Add(4 * time.Minute)},
		{h, at.Add(-4 * time.Minute)},
		{h + ",v1=" + strings.Repeat("0", 64), at}, // an extra (old) signature is fine
		{"v1=" + strings.Repeat("0", 64) + "," + h, at},
	}
	for _, c := range ok {
		if err := Verify("s1", c.header, body, c.now, 5*time.Minute); err != nil {
			t.Errorf("Verify(%q) = %v", c.header, err)
		}
	}
	bad := []struct {
		secret, header string
		body           []byte
		now            time.Time
		want           error
	}{
		{"s2", h, body, at, ErrSignature},
		{"s1", h, []byte(`{"version":2}`), at, ErrSignature},
		{"s1", h, body, at.Add(6 * time.Minute), ErrTimestamp},
		{"s1", h, body, at.Add(-6 * time.Minute), ErrTimestamp},
		{"s1", strings.Replace(h, "t=1762600000", "t=1762600001", 1), body, at, ErrSignature},
		{"s1", "", body, at, ErrHeader},
		{"s1", "t=1762600000", body, at, ErrHeader},
		{"s1", "t=x,v1=" + strings.Repeat("0", 64), body, at, ErrHeader},
		{"s1", "t=1762600000,v1=zz", body, at, ErrHeader},
		{"s1", "t=1762600000,v1=abcd", body, at, ErrHeader},
		{"s1", "garbage", body, at, ErrHeader},
	}
	for _, c := range bad {
		if err := Verify(c.secret, c.header, c.body, c.now, 5*time.Minute); !errors.Is(err, c.want) {
			t.Errorf("Verify(%q, %q) = %v, want %v", c.secret, c.header, err, c.want)
		}
	}
}
