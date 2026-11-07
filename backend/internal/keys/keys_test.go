package keys

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

var keyFormat = regexp.MustCompile(`^lsk_(ing|qry)_[a-z2-7]{8}_[A-Za-z0-9_-]{43}$`)

func TestGenerateFormat(t *testing.T) {
	for _, kind := range []Kind{KindIngest, KindQuery} {
		g, err := Generate(kind)
		if err != nil {
			t.Fatal(err)
		}
		if !keyFormat.MatchString(g.Plaintext) {
			t.Fatalf("key %q does not match the format", Redact(g.Plaintext))
		}
		if len(g.Plaintext) != 60 {
			t.Errorf("length = %d, want 60", len(g.Plaintext))
		}
		if !strings.HasPrefix(g.Plaintext, "lsk_"+kind.code()+"_"+g.Prefix+"_") {
			t.Errorf("prefix %q not embedded in key", g.Prefix)
		}
		if g.Hash != Hash(g.Plaintext) {
			t.Error("Hash does not match the plaintext")
		}
		gotKind, gotPrefix, err := Parse(g.Plaintext)
		if err != nil || gotKind != kind || gotPrefix != g.Prefix {
			t.Errorf("Parse = %v %q %v", gotKind, gotPrefix, err)
		}
	}
	if _, err := Generate("admin"); err == nil {
		t.Error("Generate accepted an unknown kind")
	}
}

func TestGenerateUniqueness(t *testing.T) {
	seenKey := map[string]bool{}
	seenPrefix := map[string]bool{}
	for i := 0; i < 20000; i++ {
		g, err := Generate(KindIngest)
		if err != nil {
			t.Fatal(err)
		}
		if seenKey[g.Plaintext] {
			t.Fatal("duplicate key")
		}
		seenKey[g.Plaintext] = true
		seenPrefix[g.Prefix] = true
	}
	// 40-bit prefixes: a collision among 20k is possible but very unlikely
	// (birthday bound ~1.8e-4); the store also enforces uniqueness.
	if len(seenPrefix) < 19999 {
		t.Errorf("only %d distinct prefixes in 20000 keys", len(seenPrefix))
	}
}

// TestGenerateUsesEntropySource: the key is exactly the bytes read from the
// reader, and a failing reader is an error, never a weaker fallback.
func TestGenerateUsesEntropySource(t *testing.T) {
	src := bytes.Repeat([]byte{0x00}, 5)
	src = append(src, bytes.Repeat([]byte{0xff}, 32)...)
	g, err := GenerateFrom(bytes.NewReader(src), KindQuery)
	if err != nil {
		t.Fatal(err)
	}
	// 0x00 x5 -> "aaaaaaaa"; 0xff x32 -> 42 x "_" then "8" (last 4 bits padded with 00).
	want := "lsk_qry_aaaaaaaa_" + strings.Repeat("_", 42) + "8"
	if g.Plaintext != want {
		t.Errorf("key = %q, want %q", g.Plaintext, want)
	}
	if _, err := GenerateFrom(bytes.NewReader(make([]byte, 10)), KindIngest); err == nil {
		t.Error("short entropy accepted")
	}
	if _, err := GenerateFrom(errReader{}, KindIngest); err == nil {
		t.Error("failing entropy source accepted")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

func TestParseRejectsMalformed(t *testing.T) {
	good, _ := Generate(KindIngest)
	parts := strings.SplitN(good.Plaintext, "_", 4)
	tests := map[string]string{
		"empty":                "",
		"no separators":        "lskingabcdefgh",
		"wrong product prefix": "abc_ing_" + parts[2] + "_" + parts[3],
		"unknown kind":         "lsk_adm_" + parts[2] + "_" + parts[3],
		"uppercase prefix":     "lsk_ing_" + strings.ToUpper(parts[2]) + "_" + parts[3],
		"short prefix":         "lsk_ing_abc_" + parts[3],
		"prefix with 1":        "lsk_ing_abcdefg1_" + parts[3],
		"short secret":         "lsk_ing_" + parts[2] + "_" + parts[3][:40],
		"long secret":          good.Plaintext + "AAAA",
		"secret not base64url": "lsk_ing_" + parts[2] + "_" + strings.Repeat("!", 43),
		"padded secret":        "lsk_ing_" + parts[2] + "_" + parts[3][:42] + "=",
		"whitespace":           " " + good.Plaintext,
		"huge":                 strings.Repeat("a", 10_000),
	}
	for name, k := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Parse(k); err == nil {
				t.Fatalf("Parse accepted %q", k)
			}
		})
	}
}

func TestRedact(t *testing.T) {
	g, _ := Generate(KindQuery)
	secret := strings.SplitN(g.Plaintext, "_", 4)[3]
	r := Redact(g.Plaintext)
	if strings.Contains(r, secret) || !strings.HasPrefix(r, "lsk_qry_"+g.Prefix+"_") {
		t.Errorf("Redact = %q", r)
	}
	if Redact("garbage") != "(invalid key)" {
		t.Error("Redact of garbage should not echo it")
	}
}

// fakeStore is an in-memory Store.
type fakeStore struct {
	mu      sync.Mutex
	recs    map[string]Record
	err     error
	touches []string
}

func (f *fakeStore) LookupKey(_ context.Context, prefix string) (Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return Record{}, f.err
	}
	r, ok := f.recs[prefix]
	if !ok {
		return Record{}, ErrNotFound
	}
	return r, nil
}

func (f *fakeStore) TouchKey(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touches = append(f.touches, id)
	return nil
}

func record(g Generated, mutate func(*Record)) Record {
	r := Record{ID: "key-" + g.Prefix, ProjectID: "proj-1", ProjectSlug: "demo", Kind: g.Kind, Hash: g.Hash[:]}
	if mutate != nil {
		mutate(&r)
	}
	return r
}

func TestVerify(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)

	ing, _ := Generate(KindIngest)
	qry, _ := Generate(KindQuery)
	other, _ := Generate(KindIngest) // same prefix as ing below, different secret
	forged := "lsk_ing_" + ing.Prefix + "_" + strings.SplitN(other.Plaintext, "_", 4)[3]

	tests := []struct {
		name    string
		key     string
		want    Kind
		rec     *Record // stored under the key's prefix; nil means unknown prefix
		wantErr error
		wantID  Identity
	}{
		{name: "valid ingest key", key: ing.Plaintext, want: KindIngest, rec: ptr(record(ing, nil)),
			wantID: Identity{KeyID: "key-" + ing.Prefix, ProjectID: "proj-1", ProjectSlug: "demo", Kind: KindIngest}},
		{name: "valid, any kind accepted", key: qry.Plaintext, want: "", rec: ptr(record(qry, nil)),
			wantID: Identity{KeyID: "key-" + qry.Prefix, ProjectID: "proj-1", ProjectSlug: "demo", Kind: KindQuery}},
		{name: "not yet expired", key: ing.Plaintext, want: KindIngest, rec: ptr(record(ing, func(r *Record) { r.ExpiresAt = &future })),
			wantID: Identity{KeyID: "key-" + ing.Prefix, ProjectID: "proj-1", ProjectSlug: "demo", Kind: KindIngest}},
		{name: "query key on ingest", key: qry.Plaintext, want: KindIngest, rec: ptr(record(qry, nil)), wantErr: ErrInvalidKey},
		{name: "ingest key on query", key: ing.Plaintext, want: KindQuery, rec: ptr(record(ing, nil)), wantErr: ErrInvalidKey},
		{name: "kind in key differs from stored kind", key: ing.Plaintext, want: "", rec: ptr(record(ing, func(r *Record) { r.Kind = KindQuery })), wantErr: ErrInvalidKey},
		{name: "revoked", key: ing.Plaintext, want: KindIngest, rec: ptr(record(ing, func(r *Record) { r.RevokedAt = &past })), wantErr: ErrInvalidKey},
		{name: "expired", key: ing.Plaintext, want: KindIngest, rec: ptr(record(ing, func(r *Record) { r.ExpiresAt = &past })), wantErr: ErrInvalidKey},
		{name: "expires exactly now", key: ing.Plaintext, want: KindIngest, rec: ptr(record(ing, func(r *Record) { r.ExpiresAt = &now })), wantErr: ErrInvalidKey},
		{name: "project disabled", key: ing.Plaintext, want: KindIngest, rec: ptr(record(ing, func(r *Record) { r.ProjectDisabledAt = &past })), wantErr: ErrInvalidKey},
		{name: "right prefix, wrong secret", key: forged, want: KindIngest, rec: ptr(record(ing, nil)), wantErr: ErrInvalidKey},
		{name: "unknown prefix", key: other.Plaintext, want: KindIngest, rec: nil, wantErr: ErrInvalidKey},
		{name: "malformed", key: "lsk_ing_nope", want: KindIngest, wantErr: ErrInvalidKey},
		{name: "empty", key: "", want: KindIngest, wantErr: ErrInvalidKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &fakeStore{recs: map[string]Record{}}
			if tt.rec != nil {
				_, prefix, _ := Parse(tt.key)
				st.recs[prefix] = *tt.rec
			}
			var logs bytes.Buffer
			v := NewVerifier(st, slog.New(slog.NewTextHandler(&logs, nil)))
			v.now = func() time.Time { return now }

			id, err := v.Verify(context.Background(), tt.key, tt.want)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) || err.Error() != "invalid API key" {
					t.Fatalf("err = %v, want exactly ErrInvalidKey", err)
				}
				if id != (Identity{}) {
					t.Errorf("identity returned on rejection: %+v", id)
				}
			} else if err != nil || id != tt.wantID {
				t.Fatalf("Verify = %+v, %v; want %+v", id, err, tt.wantID)
			}
			assertNoSecret(t, tt.key, logs.String())
			if err != nil {
				assertNoSecret(t, tt.key, err.Error())
			}
		})
	}
}

func TestVerifyStoreErrorIsNotInvalidKey(t *testing.T) {
	g, _ := Generate(KindIngest)
	st := &fakeStore{err: errors.New("connection refused to 10.0.0.5:5432")}
	_, err := NewVerifier(st, nil).Verify(context.Background(), g.Plaintext, KindIngest)
	if err == nil || errors.Is(err, ErrInvalidKey) {
		t.Fatalf("err = %v, want a storage error distinct from ErrInvalidKey", err)
	}
	assertNoSecret(t, g.Plaintext, err.Error())
}

func TestTouchThrottle(t *testing.T) {
	g, _ := Generate(KindIngest)
	st := &fakeStore{recs: map[string]Record{g.Prefix: record(g, nil)}}
	v := NewVerifier(st, nil)
	clock := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	v.now = func() time.Time { return clock }

	verify := func() {
		t.Helper()
		if _, err := v.Verify(context.Background(), g.Plaintext, KindIngest); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 50; i++ { // 50 verifications within the same minute
		verify()
		clock = clock.Add(time.Second)
	}
	if len(st.touches) != 1 {
		t.Fatalf("touches after 50 verifications in 50 s = %d, want 1", len(st.touches))
	}
	clock = clock.Add(11 * time.Second) // 61 s after the first touch
	verify()
	if len(st.touches) != 2 {
		t.Fatalf("touches after a minute = %d, want 2", len(st.touches))
	}
}

func assertNoSecret(t *testing.T, key, text string) {
	t.Helper()
	if key == "" || text == "" {
		return
	}
	if strings.Contains(text, key) {
		t.Errorf("text contains the key: %q", text)
	}
	if parts := strings.SplitN(key, "_", 4); len(parts) == 4 && len(parts[3]) > 8 && strings.Contains(text, parts[3]) {
		t.Errorf("text contains the secret part: %q", text)
	}
}

func ptr[T any](v T) *T { return &v }

var _ io.Reader = errReader{}
