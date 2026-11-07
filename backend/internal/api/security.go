package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Cookie names: the __Host- prefix makes browsers require Secure, Path=/
// and no Domain, so a sibling subdomain cannot set or shadow the cookie.
const (
	cookieSecure   = "__Host-logsage_session"
	cookieInsecure = "logsage_session"
	tokenBytes     = 32
)

// cookieName returns the session cookie name for the Secure setting.
func cookieName(secure bool) string {
	if secure {
		return cookieSecure
	}
	return cookieInsecure
}

// sessionCookie builds the cookie that carries token; maxAge < 0 clears it.
func sessionCookie(secure bool, token string, maxAge time.Duration) *http.Cookie {
	c := &http.Cookie{
		Name:     cookieName(secure),
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	if maxAge < 0 {
		c.Value = ""
		c.MaxAge = -1
	} else {
		c.MaxAge = int(maxAge / time.Second)
	}
	return c
}

// newToken returns an opaque random session token and its SHA-256. Only
// the hash is stored.
func newToken() (string, []byte, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	h := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(b), h[:], nil
}

// hashToken validates a presented token's shape and returns its hash.
func hashToken(token string) ([]byte, bool) {
	if len(token) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return nil, false
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(b) != tokenBytes {
		return nil, false
	}
	h := sha256.Sum256(b)
	return h[:], true
}

// originOf returns the lower-case scheme://host of a URL, or "".
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

// originChecker decides whether a request's Origin (or Referer) is trusted:
// listed in ALLOWED_ORIGINS, or naming the host the request was sent to.
type originChecker struct {
	allowed map[string]bool
}

func newOriginChecker(origins []string) originChecker {
	m := make(map[string]bool, len(origins))
	for _, o := range origins {
		m[strings.ToLower(o)] = true
	}
	return originChecker{allowed: m}
}

// listed reports whether origin is in ALLOWED_ORIGINS (used for CORS).
func (c originChecker) listed(origin string) bool {
	return origin != "" && c.allowed[strings.ToLower(origin)]
}

// trusted applies the CSRF rule to r: Origin if present, else Referer;
// neither present (or "null") is untrusted.
func (c originChecker) trusted(r *http.Request) bool {
	src := r.Header.Get("Origin")
	if src == "" {
		src = r.Header.Get("Referer")
	}
	o := originOf(src)
	if o == "" {
		return false
	}
	if c.allowed[o] {
		return true
	}
	u, _ := url.Parse(o)
	return r.Host != "" && strings.EqualFold(u.Host, r.Host)
}

// isJSON reports whether the Content-Type is application/json (parameters
// such as charset allowed). Browsers cannot send it cross-origin without a
// CORS preflight, which only listed origins pass.
func isJSON(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// secure wraps next with the response headers, CORS, CSRF and body limit.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Add("Vary", "Origin")

		origin := r.Header.Get("Origin")
		cors := s.origins.listed(originOf(origin))
		if cors {
			h.Set("Access-Control-Allow-Origin", originOf(origin))
			h.Set("Access-Control-Allow-Credentials", "true")
		}
		if r.Method == http.MethodOptions {
			m := r.Header.Get("Access-Control-Request-Method")
			if !cors || (m != http.MethodGet && m != http.MethodPost) {
				writeError(w, http.StatusForbidden)
				return
			}
			h.Set("Access-Control-Allow-Methods", "GET, POST")
			h.Set("Access-Control-Allow-Headers", "Content-Type")
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !safeMethod(r.Method) && (!isJSON(r) || !s.origins.trusted(r)) {
			writeError(w, http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}
