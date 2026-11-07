# ADR 0017: Log explorer state, timeline and loading

Status: accepted (v0.2 package I2b)

## Context
The log explorer is the dashboard's main screen (design B2): a range heading, a timeline, a filter column and log rows. It reads the session endpoints of ADR 0014. On-call engineers share links during incidents and use the back button while narrowing a search, so the state must survive both.

## Decision

### State lives in the URL
- Search parameters:
  - `range=15m|1h|6h|24h|7d` (relative), or `from` and `to` (absolute, RFC3339 UTC).
  - `level` and `service` (comma lists), `q`, `trace_id`.
- **Parsing is defensive** (`explorer/model.ts`):
  - Invalid values are dropped, never thrown: unknown levels, more than 20 services, text over 256 characters, a malformed trace id, and reversed or over-7-day ranges.
  - An absolute range wins over a relative one when both are present.
- **Serialization is minimal:** defaults are left out, levels are ordered, and all five levels means no level filter. A parse and serialize round trip is stable.
- **History:** every discrete action (a checkbox, a preset, a brush, Enter in search) pushes a history entry, so Back undoes it. The debounced search while typing replaces the current entry instead.
- **Time zone:** all times are UTC and labeled UTC.

### Relative and absolute ranges
- **Relative ranges** resolve against a "now" that is fixed until the user picks a preset or presses Refresh. Query keys therefore stay stable, and data does not shift under the reader.
- **Absolute ranges** come from brushing or the custom range form.
- **Timeline context:** an absolute selection is shown inside the smallest preset that ends now and contains it ("selected within the last 15 min"). If no preset contains it, the timeline shows just the selection.

### Timeline and brushing
- Hand-written SVG with two thin strips over one shared axis: logs per bucket, and the error plus fatal share. Each strip has its unit and peak value, and the server's bucket size (ADR 0014) labels them.
- **Brushing:** a pointer drag on either strip selects a range.
  - It snaps outward to bucket boundaries (aligned to the Unix epoch like the server's buckets) and spans at least 2 buckets.
  - It is clamped to the context window. A drag under half a bucket is a click, and Escape cancels.
  - The pure math in `explorer/time.ts` is unit-tested.
- **Accessible alternatives:** range presets, a custom range with two UTC date-time inputs (validated: both present, end after start, at most 7 days), and a summary line under the chart. The summary is computed deterministically from the data: total, peak volume and when, and peak error share and when, with ties going to the earliest bucket.

### Data loading
- React Query runs three queries:
  - histogram over the context window,
  - facets over the selected range,
  - logs over the selected range, with cursor pagination.
- Each query's key contains the project, the window and the filters, and each request takes the query's `AbortSignal`. When the filters change, the old query is no longer observed and its request is cancelled. A late response is cached under its own old key, so it can never replace newer data; a test covers this.
- **Pagination:** "Load more" fetches the next page (100 rows) with `next_cursor`. Pages are flattened in order and de-duplicated by `event_id`.
- **Row cap:** at most 1,000 rows are loaded. Past that, the view asks the reader to narrow the range or add filters, because a longer list is not a useful way to read logs.
- "Showing N of TOTAL logs" uses the facets total, which applies every filter.

### Facets
- **Levels:** all checked means no filter. Unchecking one keeps the other four. The last checked level cannot be unchecked, because an empty selection would show nothing.
- **Services: selection semantics.** None checked means every service, and checking one selects it. This deviates from the B2 mockup, which shows every service checked: "unchecking" from an all-checked state would need a list of up to 49 names, more than the API's 20-service limit.
- **Other row:** the "other" entry (services beyond the top 50) is shown with its count but cannot be selected.
- **Trace id:** it must be 32 hex characters. Invalid input shows what is wrong and how to fix it, and is not applied.

### Rendering and keyboard
- **Rows:** single-line B2 rows: UTC time with milliseconds (with the date when the range spans more than a day), level shape and word, service, and the message with an ellipsis and its full text in `title`.
- **Narrow screens:** below 700px rows use two lines. Below 900px the filter column moves above the list.
- **Untrusted text:** messages and service names go through `<LogText>`, as text only (ADR 0016).
- **Keyboard:**
  - `/` focuses search.
  - `j` and `k` move the selection; the selected row has `aria-selected` and focus.
  - Escape clears the selection.
- **Status line:** result counts are announced in a polite live region.
- **Error states, in plain language:**

  | case | what the reader sees |
  |---|---|
  | empty result | the empty-state copy and Clear filters |
  | 400 | the server's message |
  | 429 | the `Retry-After` seconds |
  | 503 or network | "LogSage cannot reach its log store right now. Try again in a moment." and a retry button |
  | 404 | the project-not-found copy |
  | 401 | the redirect-and-return flow of I2a |

  Fixed alongside: a second 401 in flight could replace the "session ended" notice. Only the first 401 redirects now.

### Local development
- `scripts/dev.ps1` (Windows PowerShell 5.1 and pwsh) runs the stack, creates the dev user, project and key, seeds demo data and starts the dashboard. It is documented in the README.
- **Demo data:** `loadgen seed` writes about 70,000 events into the past: an hour of history, plus an incident in the last 15 minutes in which payment gateway timeouts cascade into checkout and api errors.
- **Byte order mark:** passwords and keys reach the admin CLI through stdin as raw bytes. Under a UTF-8 console, Windows PowerShell 5.1 and .NET Framework would otherwise prepend a byte order mark to piped text, and the CLI would read it as part of the value.

## Deferred to I2c
- Row detail: expansion, Same trace, Copy as JSON.
- Live tail in the explorer.
- The API keys screen.
- The command palette (the `Ctrl K` hint stays inert).
- A dark theme and row virtualization; the 1,000-row cap keeps the DOM small until then.

## Consequences
- Links reproduce the exact view for absolute ranges, and the same relative window for presets. A relative link opened later shows newer data, by design.
- The chart and the facets cost two extra requests per filter change. They run in parallel with the logs request, and an older request is cancelled when a newer one starts.
