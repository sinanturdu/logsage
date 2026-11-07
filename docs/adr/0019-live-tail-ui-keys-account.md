# ADR 0019: Live tail UI, API keys screen and account menu

Status: accepted (v0.2 package I2c-2)

## Context
Three parts of the dashboard are added on top of existing server endpoints, with no backend change:
- the live tail of ADR 0015 in the explorer;
- the project key endpoints of ADR 0013;
- the account endpoints (change password, sign out of all devices).

The live tail has to survive drops, restarts and slow storage without hiding problems from the reader or hammering the server. A new API key is a secret that the browser holds for a few seconds.

## Decision

### A fetch-based SSE client, not EventSource
- **Why not EventSource:**
  - It hides the HTTP status: 401, 404, 400 and 429 all look like the same error, so the UI could not tell "sign in again" from "back off".
  - It cannot send headers, read `Retry-After` or control its own reconnect timing.
  - It reconnects on its own, so its timing would fight the backoff below.
- **Instead,** `live/sse.ts` uses `fetch` with `credentials: 'same-origin'` and `Accept: text/event-stream`, and reads the body stream with a tested parser:
  - lines end in CRLF, LF or CR, also when split across chunks;
  - comments are ignored;
  - multi-line data is joined with `\n`;
  - the `event`, `id` and `retry` fields are parsed;
  - the last event id persists.
- **Requests:**
  - The URL carries the explorer's filters (`level`, `service`, `q`, `trace_id`) and never a time range.
  - The last event id is sent as `Last-Event-ID` on every reconnect with the same filters.
  - New filters start without a cursor.
- **Errors:** an error status becomes the app's `ApiError`. A 401 ends the session app-wide (the existing redirect, with the return path).
- **Closing:** an `AbortController` closes the stream at once when the reader turns Live off, changes a filter or leaves the page. The reader is also cancelled directly, whatever the platform does with the body.

### The state machine
- `live/machine.ts` is pure and unit-tested. Its states:
  - `off`
  - `connecting`
  - `live`
  - `degraded`
  - `reconnecting`, with the disconnect time, a reason and the retry time
  - `stopped`, with a reason
- **Backoff:** it starts at the server's `retry` (5 s), doubles up to 30 s, and resets once data flows again. A failure never reconnects sooner than its backoff.
- **Status codes:**
  - 401: stop, "your session ended"; the app goes to sign-in.
  - 403: stop.
  - 404: stop, "this project was not found, or you are not a member of it".
  - 400: stop with the server's fixed message and "Change the filters to try again".
  - 429: wait at least `Retry-After`.
  - 5xx and network errors: back off and retry.
- **Server close reasons:**
  - `max_duration`: reconnect at once, silently, with `Last-Event-ID`. If the stream lasted less than the backoff, it waits like a failure instead, so a misbehaving server cannot cause a fast loop.
  - `shutdown`: back off.
  - `session_ended` and `access_revoked`: stop.
  - A stream that ends without a close status is a drop: back off.
- **Reset:** a `reset` event clears the live rows and fetches the list again up to "now"; the stream continues.
- **Retry now:** `r`, or the Retry now button, while reconnecting.

### Live in the explorer
- **Turning it on:** the Live toggle in the list header, the `l` key (ignored while typing), or `live=1` in the URL.
  - Live keeps a relative range. `from`/`to` in the URL are ignored and dropped.
  - Brushing or a custom range turns Live off.
  - Turning Live off keeps the loaded rows.
- **States in words and shapes, not color alone:**
  - "● Live"
  - "◌ Connecting"
  - "▲ Live tail degraded: the log store is slow to respond. Showing what arrived, retrying."
  - "○ Live tail disconnected at HH:MM:SS: <reason>. Reconnecting in N s, or press r to retry now." with a Retry now button
  - "■ Live tail stopped: <reason>"
  - The countdown is not a live region; state changes are announced once.
- **Rows:**
  - New logs are batched per animation frame and go on top.
  - They are de-duplicated by `event_id` against the list, the buffer and redeliveries.
  - The list is capped at 1,000 rows, and the oldest drop off.
  - An arrival gets a 1.6 s background fade, disabled under `prefers-reduced-motion`.
- **Auto-pause:**
  - The list pauses while a row is expanded, the selection is below the first row, or the list is scrolled away from the top.
  - New logs then wait in a buffer, and the bar says "Paused while you read; new logs wait here." with "N new logs", or "Resume" when nothing is waiting.
  - Either button closes the detail, clears the selection, inserts the buffer and scrolls to the top.
  - While the buffer has logs, later arrivals join it, so the list never shows logs out of order.
- **Screen readers:** one polite summary ("12 new logs") at most every 5 seconds, never one per row.
- **Timeline:** while Live is on it is refetched every 10 seconds. After a 429 it waits out `Retry-After`. The range heading follows the timeline.
- **Status line:** "Showing N logs, newest first." The facets total is only for the window that was loaded, so it is left out.

### API keys screen
- **Route:** `/projects/:slug/keys`, behind a Logs / API keys nav in the header. Switching projects keeps the section.
- **Table:**
  - Prefix (monospace), kind, name (text only), created, expires, last used, and status in words with a shape: ● active, ○ expired, ■ revoked.
  - Times are in UTC, with the full instant in `title`.
  - Active keys come first, then newest first.
- **Roles:** they come from the `role` that `GET /api/projects` already returns.
  - Owners and admins can create and revoke keys.
  - Viewers see the list with "You can see this project's keys. Only owners and admins can create or revoke them."
- **Create dialog:**
  - A modal `Dialog`: labelled, with a focus trap, Escape and focus return.
  - Kind, with plain-language descriptions.
  - Name, required, at most 200 characters (the server's rule).
  - Expiry (never, 30 days, 90 days, 1 year) maps to `expires_in` 720h, 2160h or 8760h.
- **One-time reveal:**
  - The new key is shown once in a read-only monospace field with Copy, which uses the clipboard fallback of ADR 0018, and the "shown once" text.
  - An ingest key also gets a curl example that uses the placeholder `YOUR_KEY` and a timestamp, because the ingest API requires one.
  - Closing needs "I copied the key" and Done. Escape does not close it.
- **Where the key lives:**
  - The plaintext is returned by a plain `request` call, not a React Query query or mutation, so it never enters either cache.
  - It lives only in the dialog's component state and goes away when the dialog closes.
  - It is never in the URL, browser storage, logs or error messages.
  - Tests check the DOM, both caches, both storages, the URL and the console after Done.
- **Revoke:** a confirmation that says "Applications using this key will start getting 401 errors within about 30 seconds." Focus starts on Cancel.

### Account menu
- **Menu button pattern:**
  - `aria-haspopup="menu"`; focus moves to the first item.
  - Arrow keys, Home and End move between items; Escape closes the menu and returns focus; Tab or a click outside closes it.
  - Items: Change password, Sign out of all devices, Sign out.
  - The signed-in email sits above the items.
- **Change password:**
  - Current password, new password (12 to 128 characters, counted in characters like the server) and confirmation.
  - Problems are shown before sending: mismatch, length, or no current password.
  - A 403 says the current password is wrong; 400 and 429 are shown plainly.
  - Afterwards: "Your password was changed. Your other sessions were signed out; this one stays signed in."
  - Passwords live only in the fields' state.
- **Sign out of all devices:** a confirmation that says it ends every session, including this one, then sign-in.

## Consequences
- The live tail behaves predictably under failures. The countdown is visible, there are no hidden retries, and the server never sees retries faster than the backoff.
- Tests drive the client with a mocked `fetch` that returns a `ReadableStream`, and the backoff with fake timers. No browser binaries are needed.
- The curl example assumes the ingest API at `http://localhost:8080`; a deployment with another address edits that line. Production serving and a CSP come in I2c-3.
- No new dependencies and no backend change.
