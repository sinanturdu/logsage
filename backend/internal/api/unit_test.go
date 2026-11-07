package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
)

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewLimiter(3, time.Second, 8*time.Second, 100)
	for i := 0; i < 3; i++ {
		if _, ok := l.Check(now, "ip", "em"); !ok {
			t.Fatalf("blocked after %d failures, want 3 free", i)
		}
		l.Fail(now, "ip", "em")
	}
	if _, ok := l.Check(now, "ip"); !ok {
		t.Fatal("blocked after exactly 3 failures")
	}
	// Delays double per further failure, capped at max.
	for i, want := range []time.Duration{1, 2, 4, 8, 8} {
		l.Fail(now, "ip")
		wait, ok := l.Check(now, "ip", "other")
		if ok || wait != want*time.Second {
			t.Fatalf("failure %d: wait=%v ok=%v, want %v", 4+i, wait, ok, want*time.Second)
		}
	}
	if _, ok := l.Check(now.Add(9*time.Second), "ip"); !ok {
		t.Error("still blocked after the delay")
	}
	// Reset forgets one key only.
	l.Fail(now, "em")
	l.Reset("em")
	if _, ok := l.Check(now, "em"); !ok {
		t.Error("email still blocked after Reset")
	}
	// Failures decay after a quiet period longer than max.
	l2 := NewLimiter(1, time.Second, 4*time.Second, 100)
	l2.Fail(now, "k")
	l2.Fail(now.Add(10*time.Second), "k")
	if _, ok := l2.Check(now.Add(10*time.Second), "k"); !ok {
		t.Error("old failure was not forgotten")
	}
}

func TestLimiterIsBounded(t *testing.T) {
	now := time.Now()
	l := NewLimiter(1, time.Second, time.Minute, 50)
	for i := 0; i < 1000; i++ {
		l.Fail(now, fmt.Sprintf("k%d", i), fmt.Sprintf("k%d", i))
	}
	if l.Len() != 50 {
		t.Fatalf("tracked keys = %d, want 50", l.Len())
	}
	if _, ok := l.Check(now, "k999"); ok {
		t.Error("most recent key evicted")
	}
}

func TestCookieAttributes(t *testing.T) {
	for _, secure := range []bool{true, false} {
		c := sessionCookie(secure, "tok", time.Hour)
		wantName := "logsage_session"
		if secure {
			wantName = "__Host-logsage_session"
		}
		if c.Name != wantName || !c.HttpOnly || c.Secure != secure || c.SameSite != http.SameSiteLaxMode ||
			c.Path != "/" || c.Domain != "" || c.MaxAge != 3600 {
			t.Errorf("secure=%v cookie = %+v", secure, c)
		}
		s := c.String()
		if !strings.Contains(s, "HttpOnly") || !strings.Contains(s, "SameSite=Lax") || strings.Contains(s, "Secure") != secure {
			t.Errorf("Set-Cookie = %q", s)
		}
		cl := sessionCookie(secure, "tok", -1)
		if cl.Value != "" || cl.MaxAge != -1 {
			t.Errorf("clearing cookie = %+v", cl)
		}
	}
}

func TestTokens(t *testing.T) {
	a, ha, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := newToken()
	if a == b || len(a) != 43 {
		t.Fatalf("tokens %q %q", a, b)
	}
	h, ok := hashToken(a)
	if !ok || string(h) != string(ha) {
		t.Error("hashToken does not match newToken")
	}
	for _, bad := range []string{"", "short", strings.Repeat("!", 43), a + "x"} {
		if _, ok := hashToken(bad); ok {
			t.Errorf("hashToken(%q) accepted", bad)
		}
	}
}

func TestCSRFOriginCheck(t *testing.T) {
	c := newOriginChecker([]string{"https://app.example.com"})
	tests := []struct {
		name            string
		host, origin    string
		referer         string
		wantTrusted     bool
		wantListedOrign bool
	}{
		{name: "listed origin", host: "api.example.com", origin: "https://app.example.com", wantTrusted: true, wantListedOrign: true},
		{name: "listed origin, other case", host: "api.example.com", origin: "HTTPS://APP.example.com", wantTrusted: true, wantListedOrign: true},
		{name: "same host", host: "localhost:8083", origin: "http://localhost:8083", wantTrusted: true},
		{name: "foreign origin", host: "localhost:8083", origin: "https://evil.example"},
		{name: "same host, other port", host: "localhost:8083", origin: "http://localhost:9999"},
		{name: "null origin", host: "localhost:8083", origin: "null"},
		{name: "no origin, same-host referer", host: "localhost:8083", referer: "http://localhost:8083/login?x=1", wantTrusted: true},
		{name: "no origin, foreign referer", host: "localhost:8083", referer: "https://evil.example/localhost:8083"},
		{name: "neither header", host: "localhost:8083"},
		{name: "origin wins over referer", host: "localhost:8083", origin: "https://evil.example", referer: "http://localhost:8083/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://"+tt.host+"/api/auth/login", nil)
			r.Host = tt.host
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			if tt.referer != "" {
				r.Header.Set("Referer", tt.referer)
			}
			if got := c.trusted(r); got != tt.wantTrusted {
				t.Errorf("trusted = %v, want %v", got, tt.wantTrusted)
			}
			if got := c.listed(originOf(tt.origin)); got != tt.wantListedOrign {
				t.Errorf("listed = %v, want %v", got, tt.wantListedOrign)
			}
		})
	}
}

func TestSecureMiddleware(t *testing.T) {
	s := &Server{origins: newOriginChecker([]string{"https://app.example.com"})}
	h := s.secure(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	do := func(method, ctype, origin string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost:8083/api/x", strings.NewReader("{}"))
		if ctype != "" {
			r.Header.Set("Content-Type", ctype)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	tests := []struct {
		name               string
		method, ctype, org string
		hdr                map[string]string
		want               int
		wantACAO           string
	}{
		{name: "GET passes without origin", method: "GET", want: http.StatusTeapot},
		{name: "POST same host JSON", method: "POST", ctype: "application/json; charset=utf-8", org: "http://localhost:8083", want: http.StatusTeapot},
		{name: "POST form content type", method: "POST", ctype: "application/x-www-form-urlencoded", org: "http://localhost:8083", want: http.StatusForbidden},
		{name: "POST text/plain", method: "POST", ctype: "text/plain", org: "http://localhost:8083", want: http.StatusForbidden},
		{name: "POST no content type", method: "POST", org: "http://localhost:8083", want: http.StatusForbidden},
		{name: "POST foreign origin", method: "POST", ctype: "application/json", org: "https://evil.example", want: http.StatusForbidden},
		{name: "POST no origin or referer", method: "POST", ctype: "application/json", want: http.StatusForbidden},
		{name: "POST listed origin gets CORS", method: "POST", ctype: "application/json", org: "https://app.example.com", want: http.StatusTeapot, wantACAO: "https://app.example.com"},
		{name: "preflight listed", method: "OPTIONS", org: "https://app.example.com", hdr: map[string]string{"Access-Control-Request-Method": "POST"}, want: http.StatusNoContent, wantACAO: "https://app.example.com"},
		{name: "preflight foreign", method: "OPTIONS", org: "https://evil.example", hdr: map[string]string{"Access-Control-Request-Method": "POST"}, want: http.StatusForbidden},
		{name: "preflight bad method", method: "OPTIONS", org: "https://app.example.com", hdr: map[string]string{"Access-Control-Request-Method": "DELETE"}, want: http.StatusForbidden, wantACAO: "https://app.example.com"},
		{name: "DELETE is state-changing", method: "DELETE", org: "https://evil.example", want: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := do(tt.method, tt.ctype, tt.org, tt.hdr)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d", w.Code, tt.want)
			}
			if got := w.Header().Get("Access-Control-Allow-Origin"); got != tt.wantACAO {
				t.Errorf("ACAO = %q, want %q", got, tt.wantACAO)
			}
			if got := w.Header().Get("Access-Control-Allow-Origin"); got == "*" {
				t.Error("wildcard CORS")
			}
			if tt.wantACAO != "" && w.Header().Get("Access-Control-Allow-Credentials") != "true" {
				t.Error("missing Allow-Credentials for listed origin")
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("headers = %v", w.Header())
			}
		})
	}
}

func TestRoleMatrix(t *testing.T) {
	want := map[postgres.Role][3]bool{ // view, manage keys, disable
		postgres.RoleOwner:  {true, true, true},
		postgres.RoleAdmin:  {true, true, false},
		postgres.RoleViewer: {true, false, false},
		postgres.Role(""):   {false, false, false},
		postgres.Role("x"):  {false, false, false},
	}
	for role, w := range want {
		for i, a := range []action{actView, actManageKeys, actDisable} {
			if got := can(role, a); got != w[i] {
				t.Errorf("can(%q, %d) = %v, want %v", role, a, got, w[i])
			}
		}
	}
}

func TestEmailRefHidesInput(t *testing.T) {
	ref := emailRef("Secret-Typed-Into-Email")
	if strings.Contains(strings.ToLower(ref), "secret") || len(ref) != len("email_sha256:")+16 {
		t.Errorf("emailRef = %q", ref)
	}
	if emailRef(" A@B.c ") != emailRef("a@b.c") {
		t.Error("emailRef not normalized")
	}
}
