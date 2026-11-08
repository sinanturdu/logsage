# ADR 0023: Error groups UI

Status: accepted (v0.3 package J2)

## Context
J1 (ADR 0022) added the groups API: a list with counts, sparklines, `is_new` and `regressed`; a detail; a status endpoint with audit rows; and a `fingerprint` filter on the log reads. J2 puts this in the dashboard in the B2 style ([dashboard brief](../design/dashboard.md)), with no backend change.

## Decision
**Routes and nav:**
- The header nav is Logs | Error groups | API keys.
- `/projects/:slug/groups` is the list and `/projects/:slug/groups/:fingerprint` a group.
- An address that is not 16 hex characters (or all zeros, which means "not grouped") is a 404 page that links back to the list.

**URL state:** like the explorer's (ADR 0017), parsed defensively; invalid values are dropped and defaults are left out of the URL.
- **Range:** `range`, or `from`/`to`, with the explorer's rules.
- **`level`:** a subset of `warn,error,fatal`; the default is `error,fatal`. Unchecking the last level is refused.
- **Other filters:** `service`, and `q` (searches the template).
- **`status`:** `open` (default), `ignored`, `resolved` or `all`.
- **`sort`:** `count` (default), `last_seen` or `first_seen`.
- **`new=1`:** new groups only.
- The group page keeps the range in its URL, because the API needs a window.

**"New only" without an API filter:** the API has no `is_new` parameter, so new-only asks for `sort=first_seen`.
- New groups (first seen inside the range) then come first. The list shows that prefix and stops paging at the first group that is not new. The sort control is disabled meanwhile.
- **Trade-off:** a page can load groups that are not shown. No backend change was needed.

**List:** a real `table` with column headers.
- **Status:** text plus a shape that differs from the level shapes (□ Open, – Ignored, ✓ Resolved).
- **Flags:** "New" and "Regressed" as text badges.
- **The template:** split into text nodes, with the placeholders as muted spans; it is never parsed as HTML.
- **Sparkline:** a hand-written SVG path. Its accessible label states the peak ("Trend, peak 340 events at 20:57 UTC"); the bucket start is computed from the window's start.
- **Times:** relative text, with the full UTC time in the title.
- **Load more:** de-duplicates by fingerprint. A 400 about the cursor means "The list changed. Reload it.", with a button that starts from the first page.
- **The summary:** counts only what is loaded ("Showing N groups, K new in this range."), because the API gives no total.
- **Keys:** `j`, `k`, `Enter` and `/` are in the one shortcut definition (ADR 0020), so the help lists them.

**Flag wording:** the detail explains each flag in a sentence: "New: first seen in this time range." and "Regressed: resolved on <UTC time> and seen again after that."

**Status dialog and optimistic update:**
- **Who:** owners and admins get the two other statuses as buttons (Resolve, Ignore, Reopen).
- **Dialog:** the shared `Dialog` (focus trap, Escape, focus returns) with an optional note up to the server's 500 characters, counted in characters, with an inline error above that.
- **Optimistic:** confirming closes the dialog and changes the cached detail at once. The server's answer replaces it, and the group lists are invalidated. An error restores the previous detail and says why.
- **Announced:** success is announced in a polite live region ("Group resolved").
- **Viewers:** they see the status, note and author read-only, with "Owners and admins of the project can change its status."

**Explorer integration:**
- **The `fingerprint` parameter:** 16 hex characters; anything else is dropped. It filters the logs, histogram, facets and live tail.
- **The chip:** removable and named "group: <template>". The template is fetched lazily from the group endpoint over the last minute (the API takes the title from all-time data) and cached per fingerprint. The chip shows the first 8 hex characters until then, or on error.
- **Row detail:** a grouped log shows "Group: <template> - View group", using the same cache. A `warn`, `error` or `fatal` log without a fingerprint shows "Not grouped (ingested before v0.3)"; `info` and `debug` logs show nothing.
- The group page's volume strip is the explorer's `Timeline` in a volume-only mode, fed by `/histogram?fingerprint=`.

**Palette:**
- "Go to Error groups" is available on every project page.
- On the list: the other status filters, "Show new groups only" (or the way back), and the other sort orders.
- They go through the existing page bridge, which now carries either the explorer's or the list's context.

## Consequences
- **No Go change.** Every group screen uses the J1 contract as it is.
- **The detail is fetched twice:** the explorer chip and row line use a one-minute window, separate from the detail page's range, so they can call the group endpoint for the same group again. The result is cached per fingerprint for the session.
- **Tests:** `findBy` and `waitFor` now wait up to 5 s (was 1 s). The condition still decides the result; the chip's two chained requests needed the room under a loaded parallel run.

## Addendum (J2.1): combine same message
- **What it does:** with `combine=1`, groups whose template is identical are shown as one parent row. A template is often the same in several services (one bad disk, one database), and the list then repeats it once per service.
- **Where it is computed:** in the browser, over the groups loaded so far (`groups/combine.ts`, pure). The API pages groups by fingerprint, and service is part of the fingerprint (ADR 0022). A server-side "group by template" would need a new query and its own cursor, and that is a backend change this patch does not make.
- **Consequences:**
  - A parent's sums cover the loaded groups only, and the page says so: "Combined from the N groups loaded so far." Load more folds new groups into their parents, and the sort is applied to the combined values.
  - Parents have no status actions: a status belongs to one group, and changing several at once is a bulk action, which is still deferred.
- **Error-share rule:** the explorer also hides its error-share strip when the facets show only error and fatal logs in view, not only when those levels are selected. Under a group filter the share would be 100% everywhere.

**Deferred:**
- Bulk status changes, assignment and comments.
- Merging groups.
- An `is_new` filter in the API.
- A group's history (the audit rows are not shown).
- Showing the note on the list.
- Collapsing table columns on narrow screens: below 900 px the table scrolls inside its frame instead.
