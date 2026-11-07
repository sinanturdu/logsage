# ADR 0021: Serving the dashboard from cmd/api, CSP, runtime config and the legacy key default

Status: accepted (v0.2 package I2c-4)

## Context
Until now the dashboard only ran on the Vite dev server, which proxies `/api` to `cmd/api`. v0.2.0 needs one way to run it in production. It also needs strict browser security headers, an ingest address the dashboard can show without a rebuild, and an end to the static API keys, which have been deprecated since ADR 0012.

## Decision

### Serve the dashboard from the API binary
- **How it is built:** `internal/web` embeds `backend/internal/web/dist` with `go:embed`. The Docker image's Node stage builds the frontend into that directory before `go build`.
- **In git:** the directory holds only a `.gitkeep`, so `go build` and `go test` work in a plain checkout. Such a binary answers every page with a short plain-text 404 that says the dashboard is not built in.
- **Why not a separate static container or a reverse proxy:**
  - One origin means the session cookie stays first-party with `SameSite=Strict` and the `__Host-` prefix, CSRF stays a same-origin check, and no CORS is needed.
  - One process means one port and one health check, and the API and the dashboard of a version always ship together.
  - The binary grows by about 1 MB (the image is 85 MB).
  - A CDN or a reverse proxy can still sit in front later; nothing here prevents it.
- **Routing:**
  - `/api/*`, `/healthz` and `/readyz` keep their handlers, and `/config.json` is a route of its own. The dashboard is the catch-all.
  - An unknown `/api/` path (and `/api`) is a JSON 404, never `index.html`.
  - Only GET and HEAD are allowed; anything else gets 405 with `Allow`.
  - Extension-less paths (`/projects/demo/logs`) get `index.html` for the single-page app. A missing path with an extension is a plain 404.
  - Path traversal in any spelling (`..`, `%2e`, `%2f`, `%5c`, `%25`, backslashes, NUL, `//`) is a 404 before the file system is touched, and `fs.ValidPath` is checked as well.
- **Caching:**
  - `/assets/*` files carry a content hash in their names, so they get `public, max-age=31536000, immutable`.
  - `index.html`, the fallback and other root files get `no-cache`.
  - API responses stay `no-store`, and so does `/config.json`.
- **Content types** come from an explicit table, because the Alpine image has no `/etc/mime.types`.
- **Setting:** `WEB_ENABLED` (default `true`) turns the dashboard off; `/config.json` is served either way, for the Vite dev server.

### Security headers and the CSP
- **On every response:**
  - `X-Content-Type-Options: nosniff`
  - `Referrer-Policy: no-referrer` (the page's meta tag now says the same)
  - `Permissions-Policy: camera=(), microphone=(), geolocation=(), payment=(), usb=()`
  - `Cross-Origin-Opener-Policy: same-origin`
  - `Cross-Origin-Resource-Policy: same-origin`
  - `X-Frame-Options: DENY`
  - `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'`
- **The dashboard's HTML** replaces that policy with:
  `default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; manifest-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'`.
- **No exceptions are needed:**
  - The build emits one module script and one stylesheet, with no inline code.
  - React renders no `style` attributes; the timeline uses SVG presentation attributes and classes.
  - The clipboard fallback sets styles through the CSSOM, which the CSP does not restrict.
  - So there is no `unsafe-inline` or `unsafe-eval`, and no `style-src-attr` exception.
  - Checked in the browser: every screen, including the live tail over SSE (`connect-src 'self'`) and the self-hosted fonts, ran without a CSP violation, and an injected inline script was blocked.
- **HSTS:** `Strict-Transport-Security: max-age=31536000` is sent only when `COOKIE_SECURE=true`. That setting already means the API is reached over HTTPS: TLS is terminated in front of it by a reverse proxy or load balancer. LogSage does not terminate TLS itself; the README recommends that proxy. HSTS over plain http would be ignored at best, and at worst would lock a misconfigured local host into https.

### Runtime public config
- **Server side:** `PUBLIC_INGEST_URL` (default `http://localhost:8080`) must be an http(s) URL with a host and no credentials, query or fragment. An invalid value stops start-up. A trailing slash is removed. It is served as `/config.json` (`{"ingestUrl": "..."}`, `no-store`).
- **Dashboard side:**
  - Before rendering, it fetches `/config.json` with a 3 s timeout.
  - It accepts only a 2xx `application/json` response with exactly a valid `ingestUrl`, so an HTML page (a fallback route, a proxy error page) is never read as config.
  - Otherwise it uses `http://localhost:8080`, with a console warning in development only.
  - The new-key examples use the configured address.
  - The Vite dev server proxies `/config.json` like `/api`, so development reads the same config.

### Legacy static keys off by default
- `ALLOW_LEGACY_STATIC_KEYS` now defaults to `false` in the Go config, the compose files and `.env.example`.
- Tests that exercise the static keys set it explicitly.
- Upgrading deployments that still send `INGEST_API_KEY` / `QUERY_API_KEY` set it to `true` until their clients use per-project keys.
- The static keys are removed in v0.3.

## Consequences
- **Build:** `docker build -f backend/Dockerfile .` with the repository root as context. The allow-list `.dockerignore` keeps `.env`, `.dev`, keys, `node_modules`, `dist` and `.git` out; this was checked by planting such files, and CI checks the image.
- **Local work:** the plain `go run ./cmd/api` has no dashboard; frontend work keeps using the Vite dev server (`dev.ps1 web`).
- **Changing the CSP:** a new kind of resource, such as an external font, analytics or a CDN, needs a deliberate CSP change and must be rechecked in the browser.
- **Upgrades:** a deployment that relied on the static keys fails to start, or rejects those keys, until it sets the flag. This is the breaking change in v0.2.0.
