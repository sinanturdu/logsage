# ADR 0018: Row detail, Same trace, filter chips and zoom

Status: accepted (v0.2 package I2c-1)

## Context
The log explorer (ADR 0017) lists logs on single lines. During an incident the next step is to open one log, read all of it, and follow its trace to the other services, without losing the list. Design B2 and its polish round (`docs/design/dashboard.md`) set the layout. This ADR records the rules that the design leaves open: which attribute goes where, which logs count as "the same trace", how zoom fits the URL model, and when an error share is too noisy to draw.

## Decision

### Row detail
- **Semantics:** the list is a `treegrid`, no longer a `listbox`. Rows carry `aria-selected` and `aria-expanded`, and an expanded row is followed by a detail row whose content is interactive. `aria-expanded` is not allowed on `option`, and buttons cannot live inside a listbox.
- **Opening and closing:**
  - A click, Enter or Space on a row toggles its detail, and focus stays on the row.
  - Escape closes the detail first and returns focus to its row. A second Escape clears the selection.
  - One detail is open at a time, and a new result set closes it.
- **Layout:**
  - The full message comes first, wrapped and monospaced, through `<LogText>`.
  - One line holds `trace_id` and `span_id` with copy buttons. At its right end are "Filter by this trace" and "Copy as JSON".
  - Below are three zones: Request, Context and Same trace (`1fr 1fr 1.6fr`). Below 1100px they use two columns and Same trace spans both. Below 700px they use one column.
  - Without a `trace_id`: the line says `trace_id none`, there is no Same trace zone and no filter button, and Request and Context share the width.
- **Attribute grouping** (`groupAttributes` in `explorer/detail.ts`), deterministic because attributes are arbitrary:
  - `span_id` moves to the id line.
  - Keys that start with `http.`, `url.`, `request.`, `response.` or `gateway.`, or that equal `method`, `status`, `timeout_ms`, `attempt`, `retry` or `duration_ms`, go to Request.
  - Everything else goes to Context.
  - Each group is sorted by key in code-unit order. An empty group is hidden, and "No attributes" appears when both are empty. Values are always text.
- **Copy:**
  - "Copy as JSON" copies the whole log object, pretty-printed with two spaces. The copy buttons use the Clipboard API.
  - Without the Clipboard API, or when it refuses, the copy falls back to a hidden textarea and `execCommand('copy')`.
  - If that fails too, the polite status says how to copy by hand.
- **Filter by this trace** and the `t` key set the `trace_id` URL parameter. The id is validated like the trace field (32 hex characters); otherwise a polite notice explains why nothing happened. When the selected log has no `trace_id`, `t` says so.

### Same trace
- **Fetching:**
  - Only for a log with a `trace_id`, and only while its detail is open.
  - It uses `GET /api/projects/{slug}/logs` with `trace_id`, `limit=50` and a window of 10 minutes before and after the log. The end is exclusive plus 1 ms, so the log itself is inside.
  - The window is clamped to the server's maximum (7 days), which only matters if the half-window constant grows.
- **Cache and cancellation:**
  - The React Query key is `['trace', slug, traceId, from, to]`, so the cache is per trace and window.
  - The request takes the query's `AbortSignal`, so collapsing the row cancels it. A late answer for another trace lands under its own key and never shows in the open detail.
- **List:**
  - Newest first. At most 5 items show, then "Show all N".
  - Each item is one line: time, level shape and word, service and message, cut with an ellipsis, with the full message in `title`.
  - The current log has a left bar and `aria-current`, with no "(this log)" text.
- **States:**
  - loading skeleton;
  - "No other logs share this trace in this window.";
  - "The other logs of this trace could not be loaded." with Retry.
- **Detail switching:** clicking an item shows that log in the open detail, while the list selection and the expanded row stay where they are. Investigating along a trace therefore does not move the list.

### Filter chips and the Filters disclosure
- **Chips:**
  - A row above the list holds one button per active filter: a custom range, the level set when not all levels, each service, the search text and the trace id.
  - Each chip is named "Remove filter <label>", shows plain text (a long trace id is shortened visually, never in the name), and stays visible when the panel is collapsed.
  - Removing the range chip goes back to the last 15 minutes and drops zoom.
  - "Clear filters" clears levels, services, search and trace, and keeps the range, which has its own chip. This matches the panel's Clear filters.
- **Filters (N):** below 900px the filter panel collapses behind a "Filters (N)" button with `aria-expanded`. N counts the level set, services, search and trace, but not the range. The open state is component state, not URL state. The search box stays outside the disclosure.

### Zoom and its URL model
- **The parameter:** `zoom=1` is a new URL parameter and is only valid with an absolute `from`/`to`; on a relative range it is dropped.
  - Without zoom, the timeline shows the selection inside its context window (ADR 0017).
  - With zoom, the context window is the selection itself, so the histogram is fetched again for that range with finer buckets.
- **The buttons:**
  - "Zoom to selection" appears when an absolute selection exists, and pushes `zoom=1`.
  - "Zoom out" pushes the same URL without it, which shows the surrounding context again with the selection still in place.
  - Both are history entries, so Back undoes them.
- **Brushing:** inside a zoomed view, brushing zooms further (the new range keeps `zoom=1`), and Back steps out one level at a time. A preset, the custom range form or removing the range chip leaves zoom.
- **Scope wording:** a line above the strips says "4,226 logs in view, 2,113 in the selection". "In view" is the histogram total for the visible window, and "in the selection" is the facets total. When zoomed or on a relative range, only "N logs in view" is shown. The list keeps "Showing N of TOTAL logs, newest first."

### Small samples in the error share
- A bucket with fewer than 5 logs (`MIN_SHARE_SAMPLE`) has no error share: the error strip's line breaks there, and a lone point is drawn as a short flat stroke.
- When any bucket is that small, the key says "Buckets with fewer than 5 logs are left out of the error share."
- The summary line and the strip's peak label ignore those buckets. When errors appear only in small buckets, the summary says so instead of reporting a 100% peak.

## Consequences
- A shared link reproduces the zoomed view, and Back undoes zoom steps like any other filter change.
- Same trace adds one small request per opened detail. It is cached for a minute and limited to 50 rows, so a trace with more logs than that in the window shows the newest 50. "Filter by this trace" shows all of them.
- The baseline demo data uses `tr-…` trace ids that do not pass validation. Their Same trace still works, but Filter by this trace explains why it cannot filter. Real OpenTelemetry ids pass.
- The treegrid changes the list's accessible role, and tests query `treegrid` and `row`.
- No new dependencies. No backend change: the detail uses the existing logs endpoint and its `trace_id` filter.
