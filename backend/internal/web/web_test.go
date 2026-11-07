package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const indexHTML = `<!doctype html><title>LogSage</title><script type="module" src="/assets/index-abc123.js"></script>`

func built() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                    {Data: []byte(indexHTML)},
		"assets/index-abc123.js":        {Data: []byte("console.log(1)")},
		"assets/index-def456.css":       {Data: []byte("body{}")},
		"assets/red-hat-text-x9.woff2":  {Data: []byte("wOF2")},
		"favicon.svg":                   {Data: []byte("<svg/>")},
		"assets/nested/deep-1a2b3c.png": {Data: []byte("png")},
	}
}

func do(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestServing(t *testing.T) {
	h := New(built())
	cases := []struct {
		name, method, target string
		status               int
		body                 string // substring
		cache, ctype         string
	}{
		{"root", "GET", "/", 200, "<title>LogSage</title>", "no-cache", "text/html; charset=utf-8"},
		{"index by name", "GET", "/index.html", 200, "<title>LogSage</title>", "no-cache", "text/html; charset=utf-8"},
		{"spa route", "GET", "/projects/demo/logs", 200, "<title>LogSage</title>", "no-cache", "text/html; charset=utf-8"},
		{"spa route with query dots", "GET", "/projects/demo/logs?q=a..b", 200, "<title>LogSage</title>", "no-cache", "text/html; charset=utf-8"},
		{"spa route trailing slash", "GET", "/projects/demo/keys/", 200, "<title>LogSage</title>", "no-cache", "text/html; charset=utf-8"},
		{"hashed js", "GET", "/assets/index-abc123.js", 200, "console.log", "public, max-age=31536000, immutable", "text/javascript; charset=utf-8"},
		{"hashed css", "GET", "/assets/index-def456.css", 200, "body{}", "public, max-age=31536000, immutable", "text/css; charset=utf-8"},
		{"font", "GET", "/assets/red-hat-text-x9.woff2", 200, "wOF2", "public, max-age=31536000, immutable", "font/woff2"},
		{"nested asset", "GET", "/assets/nested/deep-1a2b3c.png", 200, "png", "public, max-age=31536000, immutable", "image/png"},
		{"root file", "GET", "/favicon.svg", 200, "<svg/>", "no-cache", "image/svg+xml"},
		{"missing file with extension", "GET", "/missing.png", 404, "not found", "no-store", "text/plain; charset=utf-8"},
		{"missing asset", "GET", "/assets/index-zzz.js", 404, "not found", "no-store", "text/plain; charset=utf-8"},
		{"assets dir", "GET", "/assets", 200, "<title>LogSage</title>", "no-cache", "text/html; charset=utf-8"},
		{"unknown api", "GET", "/api/nope", 404, `{"error":"not found"}`, "no-store", "application/json"},
		{"bare api", "GET", "/api", 404, `{"error":"not found"}`, "no-store", "application/json"},
		{"post", "POST", "/", 405, "method not allowed", "no-store", "text/plain; charset=utf-8"},
		{"put asset", "PUT", "/assets/index-abc123.js", 405, "method not allowed", "no-store", "text/plain; charset=utf-8"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := do(t, h, c.method, c.target)
			if w.Code != c.status {
				t.Fatalf("status %d, want %d (body %q)", w.Code, c.status, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), c.body) {
				t.Fatalf("body %q lacks %q", w.Body.String(), c.body)
			}
			if got := w.Header().Get("Cache-Control"); got != c.cache {
				t.Fatalf("Cache-Control %q, want %q", got, c.cache)
			}
			if got := w.Header().Get("Content-Type"); got != c.ctype {
				t.Fatalf("Content-Type %q, want %q", got, c.ctype)
			}
			if c.status == 405 && w.Header().Get("Allow") != "GET, HEAD" {
				t.Fatalf("Allow %q", w.Header().Get("Allow"))
			}
		})
	}
}

func TestHead(t *testing.T) {
	h := New(built())
	for _, target := range []string{"/", "/projects/demo/logs", "/assets/index-abc123.js"} {
		w := do(t, h, "HEAD", target)
		if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") == "" {
			t.Fatalf("HEAD %s: %d, body %d bytes, length %q", target, w.Code, w.Body.Len(), w.Header().Get("Content-Length"))
		}
	}
}

func TestTraversalNeverEscapes(t *testing.T) {
	files := built()
	files["secret.txt"] = &fstest.MapFile{Data: []byte("top secret")} // inside the FS; outside paths cannot even be named
	h := New(files)
	for _, target := range []string{
		"/../secret.txt",
		"/assets/../secret.txt",
		"/assets/../../etc/passwd",
		"/%2e%2e/secret.txt",
		"/assets/%2e%2e/secret.txt",
		"/assets/..%2fsecret.txt",
		"/assets%2f..%2fsecret.txt",
		"/%252e%252e/secret.txt",
		"/assets/%252e%252e%252fsecret.txt",
		`/assets\..\secret.txt`,
		"/assets%5c..%5csecret.txt",
		"/assets/%00/secret.txt",
		"//secret.txt",
		"/assets//index-abc123.js",
	} {
		t.Run(target, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RequestURI = target
			u, err := r.URL.Parse(target)
			if err == nil {
				r.URL = u
			} else {
				r.URL.Path = target
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "top secret") || strings.Contains(w.Body.String(), "<title>") {
				t.Fatalf("%s: %d %q", target, w.Code, w.Body.String())
			}
		})
	}
}

func TestNotBuilt(t *testing.T) {
	h := New(fstest.MapFS{".gitkeep": {Data: nil}})
	if h.Built() {
		t.Fatal("Built() with no index.html")
	}
	for _, target := range []string{"/", "/projects/demo/logs", "/assets/index-abc123.js"} {
		w := do(t, h, "GET", target)
		if w.Code != 404 || w.Body.String() != NotBuilt || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
			t.Fatalf("%s: %d %q", target, w.Code, w.Body.String())
		}
	}
	if w := do(t, h, "GET", "/api/x"); w.Code != 404 || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("api path without a dashboard: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
}

func TestEmbeddedDistHasPlaceholder(t *testing.T) {
	// A plain checkout embeds only .gitkeep; a Docker build embeds the dashboard.
	if _, err := Dist().Open(".gitkeep"); err != nil {
		t.Fatalf("placeholder: %v", err)
	}
}

func TestConfigHandler(t *testing.T) {
	if _, err := ConfigHandler(PublicConfig{}); err == nil {
		t.Fatal("want an error without an ingest URL")
	}
	h, err := ConfigHandler(PublicConfig{IngestURL: "https://logs.example.com/ingest"})
	if err != nil {
		t.Fatal(err)
	}
	w := do(t, h, "GET", "/config.json")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	var got map[string]any
	body, _ := io.ReadAll(w.Body)
	if err := json.Unmarshal(body, &got); err != nil || len(got) != 1 || got["ingestUrl"] != "https://logs.example.com/ingest" {
		t.Fatalf("body %s (%v)", body, err)
	}
	if w := do(t, h, "HEAD", "/config.json"); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("HEAD: %d", w.Code)
	}
	if w := do(t, h, "POST", "/config.json"); w.Code != 405 {
		t.Fatalf("POST: %d", w.Code)
	}
}
