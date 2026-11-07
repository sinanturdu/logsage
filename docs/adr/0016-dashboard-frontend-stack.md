# ADR 0016: Dashboard frontend stack

Status: accepted (v0.2 package I2a)

## Context
The dashboard (design B2, `docs/design/dashboard.md`) is a single-page app on top of the management API: session cookie, same-origin JSON, and SSE for the live tail (ADR 0013 to 0015). It shows untrusted text all the time: every log message and attribute comes from whoever holds an ingest key.

## Decision

### Stack (`frontend/`)
- Vite, React and TypeScript in strict mode (`noUncheckedIndexedAccess`, `exactOptionalPropertyTypes`).
- Node 24 LTS (`.nvmrc`, `engines >= 22.12`).
- Exact versions and a committed `package-lock.json`; CI installs with `npm ci`.

Runtime dependencies (6):

| package | why |
|---|---|
| `react`, `react-dom` 19.3.1 | UI |
| `react-router` 8.5.1 | routes and URL search params (the state, see below) |
| `@tanstack/react-query` 5.107.1 | server cache, retries for network errors and 5xx, cancellation; later the cursor pagination of the explorer |
| `@fontsource/red-hat-text`, `@fontsource/red-hat-mono` 5.3.0 | the B2 fonts, self-hosted (OFL-1.1): no third-party font requests |

Development dependencies:

| package | why |
|---|---|
| `typescript` 6.0.3 | typecheck; 7.x is not used yet because `typescript-eslint` supports `<6.1.0` |
| `vite` 8.3.6, `@vitejs/plugin-react` 6.2.1 | dev server with an `/api` proxy, and the build |
| `vitest` 5.1.0, `jsdom` 31.0.0 | tests without browser binaries |
| `@testing-library/react` 16.3.3, `/dom` 10.4.2, `/user-event` 14.6.8 | render and query components the way a user does, including real Tab order |
| `eslint` 10.14.0, `@eslint/js` 10.0.1, `typescript-eslint` 8.72.1, `eslint-plugin-react-hooks` 7.1.1 | lint |
| `@types/react`, `@types/react-dom` 19.3.0 | types |

Not used, on purpose:
- No UI kit, CSS-in-JS, chart library or state library. Styling is plain CSS with custom properties (`src/styles/tokens.css`), and the timeline will be hand-written SVG.
- `@tanstack/react-virtual` waits for the explorer list (I2b).

### State lives in the URL
The project slug and, from I2b on, the time range, filters, cursor and selected row are path segments and search parameters. Back and forward work, and links can be shared. Server data lives only in the React Query cache, which is cleared on sign-out and when a session ends.

### Session in a cookie only
- The browser holds no token. The session is the HttpOnly cookie set by `cmd/api`, and `fetch` uses `credentials: 'same-origin'`.
- The app writes nothing to `localStorage` or `sessionStorage`; an ESLint rule forbids both, and a test checks they stay empty after sign-in.
- Passwords exist only in the login form's state and are cleared after each attempt.
- **401:** a request made with a session gets 401 means the session ended. The cache is dropped and the user goes to `/login?next=<same URL>&expired=1`. After signing in they return to that URL. Only same-app paths are accepted, so `next` is not an open redirect.
- **Error messages:** 429 shows the `Retry-After` seconds. Errors say what happened and how to fix it, and wrong credentials never say which field was wrong.

### Local development
- **Proxy:** `npm run dev` serves `http://localhost:5173` and proxies `/api` to `http://127.0.0.1:8083` with `changeOrigin: false`. The browser talks to one origin, so the cookie is first-party.
- **CSRF check:** the API sees `Host: localhost:5173` matching the `Origin`, so its request-host rule accepts POSTs without `ALLOWED_ORIGINS`.
- **Cookie:** `COOKIE_SECURE=false` is required on plain http, because the default `__Host-` cookie needs https.

### Untrusted text is rendered as text only
- Every log value goes through `<LogText>`, which renders a React text node and nothing else.
- ESLint forbids `dangerouslySetInnerHTML` and writing `innerHTML`/`outerHTML` anywhere.
- A test renders HTML and script payloads, such as `<img src=x onerror=alert(1)>`, through `<LogText>` and checks that no element, event handler, `href` or `src` is created.

## Open decision: how the app is served in production, and its CSP
The build is static files in `frontend/dist`. Options:
1. **`cmd/api` serves them** (embedded or from a directory) on the same origin as `/api`.
   - Simplest for cookies and CSRF.
   - The API owns the security headers.
2. **A reverse proxy (nginx, Caddy) serves them** and proxies `/api`. Still one origin, with the headers set in the proxy configuration.
3. **A separate origin (CDN).**
   - Needs `ALLOWED_ORIGINS`, CORS with credentials and `SameSite=None` cookies.
   - This is the most moving parts; not recommended.

Whatever is chosen should send this Content-Security-Policy:

```
default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self';
connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'
```

plus `X-Content-Type-Options: nosniff` and `Referrer-Policy: same-origin`. The build contains no inline scripts, and fonts and CSS are files from the same origin.

## Consequences
- The frontend adds a Node toolchain and about 160 MB of `node_modules` for development. Production ships only `dist` (about 100 kB of gzipped JavaScript plus the font files).
- **Audit findings:** `npm audit` reports a moderate advisory in `minimatch`, which `eslint` and `typescript-eslint` pull in. It is development-only and not shipped. CI reports audit findings without failing the build.
- New screens must keep untrusted data in `<LogText>` (or plain text nodes) and keep state in the URL.
