// Package web serves the built dashboard and its runtime config from the
// management API binary (ADR 0021). The files are embedded at build time
// from dist/, which the Docker frontend stage fills; in a plain checkout it
// holds only a placeholder and every page answers with a short 404.
package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var embedded embed.FS

// Dist returns the embedded dashboard files.
func Dist() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err) // the directive above guarantees dist exists
	}
	return sub
}

// NotBuilt is the body of every page when the binary has no dashboard.
const NotBuilt = "The dashboard is not built into this binary. For development, run the Vite dev server " +
	"(scripts\\dev.ps1 web, or npm run dev in frontend/); for production, build the Docker image, which includes it.\n"

const (
	cacheImmutable  = "public, max-age=31536000, immutable"
	cacheRevalidate = "no-cache"
)

// Explicit types, so a minimal image without /etc/mime.types still sends the right ones.
var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".ico":         "image/x-icon",
	".webp":        "image/webp",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".txt":         "text/plain; charset=utf-8",
	".webmanifest": "application/manifest+json",
}

// Handler serves the dashboard: files from the embedded build, index.html for
// extension-less paths (the single-page app's routes), and plain 404s for the rest.
type Handler struct {
	files fs.FS
	index []byte // nil when the dashboard is not built in
}

// New returns a Handler over files (usually Dist()).
func New(files fs.FS) *Handler {
	h := &Handler{files: files}
	if b, err := fs.ReadFile(files, "index.html"); err == nil {
		h.index = b
	}
	return h
}

// Built reports whether the dashboard is built into the files.
func (h *Handler) Built() bool { return h.index != nil }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		plain(w, http.StatusMethodNotAllowed, "method not allowed\n")
		return
	}
	p := r.URL.Path
	// API paths never fall back to the dashboard: an unknown one is a JSON 404.
	if p == "/api" || strings.HasPrefix(p, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"not found"}`+"\n")
		return
	}
	name, ok := cleanName(r)
	if !ok {
		plain(w, http.StatusNotFound, "not found\n")
		return
	}
	if h.index == nil {
		plain(w, http.StatusNotFound, NotBuilt)
		return
	}
	if name == "" || name == "index.html" {
		h.serveIndex(w, r)
		return
	}
	if f, err := h.files.Open(name); err == nil {
		defer f.Close()
		if st, err := f.Stat(); err == nil && !st.IsDir() {
			if rs, ok := f.(io.ReadSeeker); ok {
				cache := cacheRevalidate
				if strings.HasPrefix(name, "assets/") {
					cache = cacheImmutable // Vite puts a content hash in every asset name
				}
				w.Header().Set("Cache-Control", cache)
				setType(w, name)
				http.ServeContent(w, r, name, time.Time{}, rs)
				return
			}
		}
	}
	if path.Ext(name) != "" {
		plain(w, http.StatusNotFound, "not found\n")
		return
	}
	h.serveIndex(w, r) // a route of the app, such as /projects/demo/logs
}

func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", HTMLPolicy)
	w.Header().Set("Cache-Control", cacheRevalidate)
	w.Header().Set("Content-Type", contentTypes[".html"])
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(h.index))
}

// cleanName turns the request path into a name inside the embedded files, or
// reports that it must not be served: traversal in any spelling (.., encoded
// dots or slashes, backslashes, NUL) never reaches the file system.
func cleanName(r *http.Request) (string, bool) {
	uri, _, _ := strings.Cut(r.RequestURI, "?") // the query is not a path; "..b" in a search is fine
	raw := strings.ToLower(r.URL.RawPath + r.URL.Path + uri)
	for _, bad := range []string{"..", "\\", "%2e", "%2f", "%5c", "%00", "%25", "\x00"} {
		if strings.Contains(raw, bad) {
			return "", false
		}
	}
	p := r.URL.Path
	if !strings.HasPrefix(p, "/") || strings.Contains(p, "//") {
		return "", false
	}
	name := strings.TrimPrefix(p, "/")
	if name == "" {
		return "", true
	}
	name = strings.TrimSuffix(name, "/")
	if !fs.ValidPath(name) {
		return "", false
	}
	return name, true
}

func setType(w http.ResponseWriter, name string) {
	if t, ok := contentTypes[strings.ToLower(path.Ext(name))]; ok {
		w.Header().Set("Content-Type", t)
	}
}

func plain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// PublicConfig is what the dashboard reads from /config.json before it renders.
type PublicConfig struct {
	IngestURL string `json:"ingestUrl"`
}

// ConfigHandler serves /config.json; it is never cached.
func ConfigHandler(c PublicConfig) (http.Handler, error) {
	if c.IngestURL == "" {
		return nil, errors.New("web: the public ingest URL is required")
	}
	body, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	body = append(body, '\n')
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			plain(w, http.StatusMethodNotAllowed, "method not allowed\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, "config.json", time.Time{}, bytes.NewReader(body))
	}), nil
}
