package fingerprint

import (
	"encoding/json"
	"flag"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var update = flag.Bool("update", false, "rewrite testdata/golden.json from the current rules (only together with a Version bump or a reviewed rule change)")

type golden struct {
	Message     string `json:"message"`
	Template    string `json:"template"`
	Fingerprint string `json:"fingerprint"`
}

// TestGolden pins the template and fingerprint of every corpus message, so
// an accidental rule change fails here. After an intended change, bump
// Version and run: go test ./internal/fingerprint -run TestGolden -update
func TestGolden(t *testing.T) {
	path := filepath.Join("testdata", "golden.json")
	got := make([]golden, 0, len(corpus))
	for _, m := range corpus {
		tpl := Normalize(m)
		got = append(got, golden{Message: m, Template: tpl, Fingerprint: Hex(Of("api", tpl, ""))})
	}
	if *update {
		b, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	var want []golden
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatalf("golden has %d cases, corpus %d: run with -update after reviewing the change", len(want), len(got))
	}
	for i := range got {
		// The message is compared by position: JSON cannot hold invalid UTF-8 exactly.
		if got[i].Template != want[i].Template || got[i].Fingerprint != want[i].Fingerprint {
			t.Errorf("case %d %q:\n got  %q %s\n want %q %s", i, got[i].Message, got[i].Template, got[i].Fingerprint, want[i].Template, want[i].Fingerprint)
		}
	}
}

func TestTemplates(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"GET /api/v1/orders/482913 completed in 47ms", "GET /api/v1/orders/<n> completed in <n>ms"},
		{"search query returned 503 results in 41ms", "search query returned <n> results in <n>ms"},
		{"upstream bank-api returned 503 for transaction 0f3c9a77b2", "upstream bank-api returned 503 for transaction <id>"},
		{"charge failed: upstream payments returned 504", "charge failed: upstream payments returned 504"},
		{"POST /v1/checkout 502 in 5151 ms", "POST /v1/checkout 502 in <n> ms"},
		{"429 Too Many Requests: retry after 17 seconds", "429 Too Many Requests: retry after <n> seconds"},
		{"retrying charge for ord_7Q2K9M, attempt 3 of 3", "retrying charge for ord_<id>, attempt <n> of <n>"},
		{"connection to db-3.internal:5432 refused", "connection to db-<n>.internal:<n> refused"},
		{"failed to write /var/data/exports/5078.json: disk quota exceeded", "failed to write /var/data/exports/<n>.json: disk quota exceeded"},
		{"job 7f3c2a91-2b4d-4c1e-9a77-0e5f6d3b2a19 failed", "job <uuid> failed"},
		{"login failed for bob@example.com from 203.0.113.9", "login failed for <email> from <ip>"},
		{"request to https://api.stripe.com/v1/charges?amount=5000 failed", "request to https://api.stripe.com/v1/charges failed"},
		{`request: "GET /v1/orders?page=2"`, `request: "GET /v1/orders"`},
		{`invalid value "two words" for field "email"`, `invalid value "<str>" for field "email"`},
		{"psycopg2.OperationalError: connection refused", "psycopg2.OperationalError: connection refused"},
		{"first line\nsecond line 123", "first line"},
		{"  spaced   out\ttabs  ", "spaced out tabs"},
		{"", ""},
	} {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestKeyMergesIDShapes(t *testing.T) {
	// The same field with an all-digit hex id and a mixed one is one group.
	a, b := Normalize("rate limit at 93% for client 123456"), Normalize("rate limit at 93% for client 8a1f0c")
	if a == b {
		t.Fatalf("templates should differ in their readable form: %q", a)
	}
	if Of("api", a, "") != Of("api", b, "") {
		t.Fatal("key form should merge <n> and <id>")
	}
}

func TestFingerprintInputs(t *testing.T) {
	tpl := Normalize("payment gateway timeout after 5000 ms")
	base := Of("payments", tpl, "")
	if base == 0 {
		t.Fatal("0 is reserved")
	}
	if Of("payments", tpl, "") != base {
		t.Fatal("not deterministic")
	}
	if Of("checkout", tpl, "") == base {
		t.Fatal("service must be part of the key")
	}
	if Of("payments", tpl, "TimeoutError") == base {
		t.Fatal("error type must be part of the key")
	}
	// Level is not part of the key: warn and error lines of one message share it.
	fw, _ := Compute("warn", "payments", "payment gateway timeout after 5000 ms", nil)
	fe, _ := Compute("error", "payments", "payment gateway timeout after 6000 ms", nil)
	if fw != fe || fw != base {
		t.Fatalf("warn %x and error %x should be one group (%x)", fw, fe, base)
	}
	for _, level := range []string{"debug", "info", "", "nonsense"} {
		if fp, tpl := Compute(level, "payments", "x", nil); fp != 0 || tpl != "" {
			t.Errorf("level %q should not be grouped", level)
		}
	}
}

func TestErrorType(t *testing.T) {
	cases := []struct {
		attrs map[string]string
		want  string
	}{
		{nil, ""},
		{map[string]string{"error.type": " TimeoutError "}, "TimeoutError"},
		{map[string]string{"exception.type": "java.io.IOException"}, "java.io.IOException"},
		{map[string]string{"error.type": "A", "exception.type": "B"}, "A"},
		{map[string]string{"error.type": "  ", "exception.type": "B"}, "B"},
		{map[string]string{"error.type": strings.Repeat("x", 500)}, strings.Repeat("x", MaxErrorType)},
	}
	for _, c := range cases {
		if got := ErrorType(c.attrs); got != c.want {
			t.Errorf("ErrorType(%v) = %q, want %q", c.attrs, got, c.want)
		}
	}
}

func TestHex(t *testing.T) {
	if Hex(0) != "" || Hex(1) != "0000000000000001" || Hex(0xfedcba9876543210) != "fedcba9876543210" {
		t.Fatal("Hex")
	}
	for _, s := range []string{"fedcba9876543210", "FEDCBA9876543210", "0000000000000001"} {
		if _, err := ParseHex(s); err != nil {
			t.Errorf("ParseHex(%q): %v", s, err)
		}
	}
	for _, s := range []string{"", "0000000000000000", "fedcba987654321", "fedcba98765432100", "+edcba9876543210", "g000000000000000", "' OR 1=1 --xxxxx", "0x00000000000001"} {
		if _, err := ParseHex(s); err == nil {
			t.Errorf("ParseHex(%q) should fail", s)
		}
	}
}

// The invariants hold on every corpus line and on random junk.
func TestInvariants(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	inputs := append([]string{}, corpus...)
	alphabet := []string{"a", "Z", "0", "9", "/", ".", ":", "-", "_", "\"", "'", "%", "@", " ", "\n", "\x00", "\xff", "é", "日", "<", ">", "ms", "0x", "::", "503", "returned "}
	for i := 0; i < 3000; i++ {
		var b strings.Builder
		for n := r.IntN(80); n > 0; n-- {
			b.WriteString(alphabet[r.IntN(len(alphabet))])
		}
		inputs = append(inputs, b.String())
	}
	inputs = append(inputs,
		strings.Repeat("x", 1<<20),                              // 1 MiB
		strings.Repeat("é", MaxInput),                           // multibyte at the cut
		string([]byte{0xff, 0xfe, 0xfd, 0x80, 0x00, 'a', 0xc3}), // invalid UTF-8
		strings.Repeat("1", 10_000),
		strings.Repeat("a1b2 /x/12/y 10.0.0.1 \"q w\" ", 5000),
	)
	for _, in := range inputs {
		out := Normalize(in)
		if again := Normalize(out); again != out {
			t.Fatalf("not idempotent:\n in   %q\n once %q\n twice %q", in, out, again)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("invalid UTF-8 out of %q", in)
		}
		if len(out) > MaxInput*2 {
			t.Fatalf("output too long (%d) for %q", len(out), in[:min(len(in), 40)])
		}
		if strings.ContainsRune(out, '\x01') || strings.ContainsRune(out, '\n') {
			t.Fatalf("sentinel or newline left in %q", out)
		}
		if Normalize(in) != out {
			t.Fatal("not deterministic")
		}
	}
}

func TestBoundedTime(t *testing.T) {
	huge := strings.Repeat("a1b2c3 /x/12/y 10.0.0.1 \"q w\" 0x1f 5000ms ", 50_000) // about 2 MB
	start := time.Now()
	for i := 0; i < 100; i++ {
		Normalize(huge)
	}
	if per := time.Since(start) / 100; per > 20*time.Millisecond {
		t.Fatalf("worst case %v per message; the cut to %d bytes should bound it", per, MaxInput)
	}
}

func BenchmarkNormalize(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Normalize(corpus[i%len(corpus)])
	}
}

// FuzzNormalize checks the invariants on generated input:
// go test ./internal/fingerprint -run xxx -fuzz FuzzNormalize -fuzztime 30s
func FuzzNormalize(f *testing.F) {
	for _, m := range corpus {
		f.Add(m)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := Normalize(in)
		if Normalize(out) != out {
			t.Fatalf("not idempotent: %q -> %q -> %q", in, out, Normalize(out))
		}
		if !utf8.ValidString(out) || strings.ContainsAny(out, "\x01\n\r") {
			t.Fatalf("bad output %q", out)
		}
		if fp := Of("svc", out, ""); fp == 0 {
			t.Fatal("0 fingerprint")
		}
	})
}

func TestTraceback(t *testing.T) {
	tb := func(lines ...string) string {
		return "Traceback (most recent call last):\n  File \"/app/x.py\", line 9, in f\n    g()\n" + strings.Join(lines, "\n")
	}
	for _, c := range []struct{ name, in, want string }{
		{"exception line", tb("KeyError: 'user_id'"), "KeyError: 'user_id'"},
		{"trailing blank lines", tb("TimeoutError: pool exhausted after 30s", "", "  ", "\t"), "TimeoutError: pool exhausted after <n>s"},
		{"crlf", strings.ReplaceAll(tb("OSError: [Errno 28] No space left on device"), "\n", "\r\n"), "OSError: [Errno <n>] No space left on device"},
		{"leading space before the header", "  " + tb("ZeroDivisionError: division by zero"), "ZeroDivisionError: division by zero"},
		{"only whitespace after the header", "Traceback (most recent call last):\n \n\t\n", "Traceback (most recent call last):"},
		{"chained", tb("KeyError: 'id'", "", "During handling of the above exception, another exception occurred:", "", "Traceback (most recent call last):", "LookupError: missing 7"), "LookupError: missing <n>"},
		{"not a traceback keeps the first line", "error: boom\nValueError: other", "error: boom"},
		{"the header later in the message", "worker crashed\nTraceback (most recent call last):\nKeyError: 'x'", "worker crashed"},
		{"exception line longer than the scanned tail", tb("ValueError: " + strings.Repeat("a", maxTail+10)), "Traceback (most recent call last):"},
	} {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	// Same exception, other line numbers and ids: one template. Other exceptions: different ones.
	a := Normalize(strings.Replace(tb("KeyError: 'user_id' for user 31337"), "line 9", "line 140", 1))
	b := Normalize(tb("KeyError: 'user_id' for user 42"))
	c := Normalize(tb("ValueError: invalid literal for int() with base 10: 'abc'"))
	if a != b || a == c {
		t.Errorf("grouping: %q %q %q", a, b, c)
	}
	// A 1 MiB traceback only scans its last 4 KiB.
	huge := tb(strings.Repeat("  File \"/app/w.py\", line 1, in run\n    step()\n", 1<<20/40) + "RecursionError: maximum recursion depth exceeded")
	start := time.Now()
	if got := Normalize(huge); got != "RecursionError: maximum recursion depth exceeded" {
		t.Errorf("huge: %q", got)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("huge traceback took %v", d)
	}
}

func TestMeaningfulCodes(t *testing.T) {
	for _, pair := range [][2]string{
		{"worker-12 crashed with exit code 137", "worker-3 crashed with exit code 1"},
		{"job 1 failed: exit status 1", "job 2 failed: exit status 2"},
		{"terminated by signal 9", "terminated by signal 15"},
		{"insert failed (SQLSTATE 23505)", "insert failed (SQLSTATE 40001)"},
		{"txn aborted: sqlstate=40001", "txn aborted: sqlstate=40P01"},
		{"call failed status=503", "call failed status=502"},
		{"call failed status: 503", "call failed status: 504"},
		{"handler failed code=500", "handler failed code=502"},
		{"handler failed code: 500", "handler failed code: 503"},
	} {
		a, b := Normalize(pair[0]), Normalize(pair[1])
		if KeyForm(a) == KeyForm(b) {
			t.Errorf("%q and %q share the key %q", pair[0], pair[1], KeyForm(a))
		}
		if Normalize(a) != a || Normalize(b) != b {
			t.Errorf("not idempotent: %q %q", a, b)
		}
	}
	// The same code with other numbers around it is one group.
	if a, b := Normalize("pid 18213 killed by signal 9 after 30s"), Normalize("pid 7 killed by signal 9 after 2s"); a != b {
		t.Errorf("%q != %q", a, b)
	}
	if got := Normalize("child exited: signal: killed"); got != "child exited: signal: killed" {
		t.Errorf("signal: killed -> %q", got)
	}
}

// TestTemplateCap: invalid bytes become U+FFFD (3 bytes) and placeholders
// are longer than what they replace, yet a template stays within MaxInput,
// so normalizing it again changes nothing (found by FuzzNormalize).
func TestTemplateCap(t *testing.T) {
	for _, in := range []string{strings.Repeat("\x97 ", 300), strings.Repeat("1 ", 300), "xx: " + strings.Repeat("a\xe8", 250) + " 7"} {
		out := Normalize(in)
		if len(out) > MaxInput || Normalize(out) != out || !utf8.ValidString(out) {
			t.Errorf("len %d, idempotent %v for %q", len(out), Normalize(out) == out, in[:20])
		}
	}
}
