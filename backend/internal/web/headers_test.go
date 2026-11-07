package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

var common = map[string]string{
	"X-Content-Type-Options":       "nosniff",
	"Referrer-Policy":              "no-referrer",
	"Permissions-Policy":           "camera=(), microphone=(), geolocation=(), payment=(), usb=()",
	"Cross-Origin-Opener-Policy":   "same-origin",
	"Cross-Origin-Resource-Policy": "same-origin",
	"X-Frame-Options":              "DENY",
}

func TestHeaders(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	cfg, _ := ConfigHandler(PublicConfig{IngestURL: "http://localhost:8080"})
	mux.Handle("/config.json", cfg)
	mux.Handle("/", New(built()))

	for _, hsts := range []bool{false, true} {
		h := Headers(mux, HeaderOptions{HSTS: hsts})
		for path, csp := range map[string]string{
			"/":                       HTMLPolicy,
			"/projects/demo/logs":     HTMLPolicy,
			"/assets/index-abc123.js": DefaultPolicy,
			"/config.json":            DefaultPolicy,
			"/api/auth/me":            DefaultPolicy,
			"/api/nope":               DefaultPolicy,
			"/healthz":                DefaultPolicy,
			"/missing.png":            DefaultPolicy,
		} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			got := w.Header()
			if got.Get("Content-Security-Policy") != csp {
				t.Errorf("hsts=%v %s: CSP %q", hsts, path, got.Get("Content-Security-Policy"))
			}
			for k, v := range common {
				if got.Get(k) != v {
					t.Errorf("hsts=%v %s: %s = %q, want %q", hsts, path, k, got.Get(k), v)
				}
			}
			if hasHSTS := got.Get("Strict-Transport-Security") == "max-age=31536000"; hasHSTS != hsts {
				t.Errorf("hsts=%v %s: Strict-Transport-Security %q", hsts, path, got.Get("Strict-Transport-Security"))
			}
		}
	}
}

func TestHTMLPolicyIsStrict(t *testing.T) {
	for _, bad := range []string{"unsafe-inline", "unsafe-eval", "*", "http:", "https:"} {
		if contains(HTMLPolicy, bad) {
			t.Errorf("HTMLPolicy contains %q", bad)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
