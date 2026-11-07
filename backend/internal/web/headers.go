package web

import "net/http"

// HTMLPolicy is the Content-Security-Policy of the dashboard's HTML (ADR 0021):
// only same-origin scripts, styles, fonts and requests; no inline script or
// style, no eval, no framing, no plugins.
const HTMLPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; " +
	"connect-src 'self'; manifest-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'"

// DefaultPolicy is the policy of every other response (JSON, assets, health).
const DefaultPolicy = "default-src 'none'; frame-ancestors 'none'"

// HeaderOptions configures Headers.
type HeaderOptions struct {
	// HSTS adds Strict-Transport-Security. Set it only when TLS is terminated
	// in front of the API (COOKIE_SECURE=true says so); never on plain http.
	HSTS bool
}

// Headers sets the security headers on every response. Handlers that serve
// HTML replace the Content-Security-Policy with HTMLPolicy.
func Headers(next http.Handler, opts HeaderOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", DefaultPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		if opts.HSTS {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}
