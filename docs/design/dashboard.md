# Dashboard design brief (v0.2 package I0)

Status: proposal for the owner's review. No code depends on it yet.

Who it is for: developers and on-call engineers in the middle of an outage, often at night, tired, on a laptop or a tablet. The main job is finding the cause fast. Managing projects and API keys comes second.

Screens: login, project switcher, log explorer, timeline, live tail, API keys, account settings. Later: error groups, alerts, incidents.

Mockups of the log explorer for each direction are in `docs/design/mockups/`. Open them directly in Chrome.

## Shared quality floor (all directions)

- **Keyboard first.**
  - `/` focuses search, `j`/`k` move between rows, `Enter` expands a row and `Esc` collapses it.
  - `t` pivots to the focused row's trace, and `Ctrl+K` / `⌘K` opens the command palette (switch project, jump to a screen, set the time range).
  - Focus is always visible as a 2px outline in the direction's focus color.
- **Levels are never shown by color alone.** Each level has a word (lower case, the same as the API values) and a shape: debug `○`, info `●`, warn `▲`, error `■`, fatal `◆`. The level colors come from color-blind-safe hues (blue, orange, vermilion, red-purple, grey) and are adjusted to each background.
- **Contrast.** Text and level colors were checked against WCAG AA (4.5:1 or better on their background; the lowest is 4.7:1).
- **Device sizes.** Full use at 1024px and up; at 768px (tablet) the filter column collapses into a drawer; on phones the screens are read-only, with one column and no live tail controls.
- **Reduced motion.** `prefers-reduced-motion: reduce` turns off every transition; nothing in the dashboard depends on motion.
- **Fonts.** The mockups load fonts from Google Fonts only for the preview. Production self-hosts the WOFF2 files. All the fonts used are under the SIL Open Font License.
- **Data.** Times are shown in UTC by default with a toggle for local time. Numbers are exact counts from the API; nothing is rounded for show.

## Direction A: Dark cockpit

**Concept.** In aviation, a "dark cockpit" is a panel where every light is off when the systems are normal, so the one lamp that comes on is impossible to miss. The explorer stays grey and quiet, and color is spent only on abnormal things: the level marks of warn, error and fatal rows, and services whose error rate is above their own baseline.

**Palette** (default dark)

| name | hex | role |
|------|-----|------|
| Panel | `#232B31` | page background (slate, not black) |
| Bezel | `#2E3840` | raised areas: header, expanded row, inputs |
| Legend | `#E4E9EB` | primary text (11.7:1) |
| Dim | `#9DA9B0` | secondary text, debug level (6.0:1) |
| Caution | `#F2B33D` | warn level; also the focus outline (7.7:1) |
| Warning | `#FF7A6B` | error level (5.6:1) |

Info is `#8CC4F0` (7.7:1) and fatal is `#EFA3F5` (7.7:1). Info is deliberately close to the text color in weight: normal is not news.

**Type.** B612 (Airbus and ENAC, OFL) for UI. It was designed for cockpit displays, so it is legible at small sizes, under fatigue and in low light. B612 Mono for log messages, timestamps, trace ids and attribute values. Monospace is used only where columns of machine text must align or a character must be copied exactly (`0`/`O`, `l`/`1` in trace ids). Scale: 12 / 13 / 14 / 16 / 20 px. Rows are 13px mono and the body text is 14px. Weights are 400 and 700 only.

**Layout.** A single column, full width, dense. An annunciator row sits at the top, then a 48px timeline strip, the filter bar and the table. Columns align on a 4px grid. Timestamps are right-aligned so the seconds line up. The level column is a fixed 9ch wide, and messages are left-aligned and never wrap unless the row is expanded.

```
+----------------------------------------------------------------------------+
| LogSage  [shop-prod v]    [/ Search messages and attributes      ] [Ctrl K]|
| checkout(*)  payments(*)  search  billing  api(*)  auth      last 15 min v |
| [timeline: ....__..___..|||||||||||||||__....]  brush = time range         |
| level [warn+ v] service [any v] trace [        ]        Live tail [ off ]  |
|   time       level     service   message                                   |
| 02:16:58.412 ■ error   payments  payment gateway timeout after 5000 ms     |
| 02:16:58.390 ■ error   checkout  charge failed: upstream payments 504      |
|   | trace 4bf92f3577b34da6a3ce929d0e0e4736   [Filter by this trace]  |     |
|   | http.route  /v1/charges     gateway.host  psp-eu-2.example        |     |
| 02:16:57.981 ▲ warn    payments  retrying charge, attempt 3 of 3           |
| ...                                                   [Load older logs]    |
+----------------------------------------------------------------------------+
```

**The one memorable element: the annunciator row.** Every service is a plain word on the panel. A service whose error rate over the selected range is above its own 24-hour baseline lights up: its word gets the Caution or Warning color, a lamp glyph (`(*)` in the wireframe) and the rate (`payments 38% errors`). Clicking a lit service filters to it. Apart from level marks, nothing else on the screen is colored or bold.

**Motion.** A live-tail row arrives with a 120ms background flash in Bezel, and nothing else moves. An expanding row opens instantly; the only transition is a 100ms height change. An annunciator lamp turning on is a color change only, with no blinking (blinking is reserved for nothing). With reduced motion there is no flash and no height change.

**Theme.** Dark by default. The audience works at night next to a sleeping household, and a dark-cockpit concept only works when "normal" recedes. A light theme ("day panel": `#EEF1F2` background, `#1D262B` text, darker level colors, still to be contrast-checked) exists for daylight and projectors. The choice is per user and remembered.

**Copy.**
- Empty: "No logs match these filters in the last 15 minutes. Widen the time range or clear the service filter."
- Error: "The log search timed out after 30 seconds. Narrow the time range or add a service filter, then search again."
- Key created: "Your ingest key is shown once. Copy it now and store it in your secret manager; LogSage keeps only a hash and cannot show it again."

## Direction B: Strip chart

**Concept.** Before dashboards, operators read incidents off paper strip-chart recorders: a pen trace on gridded paper where an anomaly is a visible jolt in the line. In this direction time is the main axis of the screen. The timeline is the place you start, and the list underneath shows how dense or quiet the time between rows was.

**Palette** (default light)

| name | hex | role |
|------|-----|------|
| Chart paper | `#F4F6F5` | page background (cool green-grey, not cream) |
| Grid | `#D6DEDA` | timeline grid lines, row separators |
| Ink | `#18211E` | primary text (15.2:1) |
| Pencil | `#5B6661` | secondary text, debug level (5.5:1) |
| Pen blue | `#1F5AA6` | the trace, selection, links, focus; info level (6.3:1) |
| Pen red | `#B42318` | error level, error-rate trace (6.1:1) |

Warn is `#8A5A00` (5.5:1) and fatal is `#7B2269` (8.5:1).

**Type.** Red Hat Text (OFL) for UI. Red Hat Mono (OFL) for messages, times and ids. The two share proportions and x-height, so mono messages sit on the same baseline as sans labels without a visible jump; mono is used only for machine text. Scale: 12 / 14 / 15 / 18 / 24 px. The 24px size is used once, for the time range readout above the chart.

**Layout.** The chart is the hero: 160px tall and full width, on a visible grid. Below it sits a two-column body: a 220px filter column on the left (level checkboxes with counts, services with counts, trace id) and the list on the right. Between rows more than 30 seconds apart, a thin gap line reads "42 s quiet", so bursts look compressed and quiet periods look open. Every chart x-position aligns with the list's time column header.

```
+----------------------------------------------------------------------------+
| LogSage   shop-prod v                    02:02 to 02:17 UTC   [Ctrl K]     |
|   |  |  |  |  |  |  |  |  |  |  |  |  |  |  |  |  |  |  |  |  |  |  |     |
| __|__|__|__|__|__|__|__|__|__|__|_/\/\__/\|  |  |  |  |  |  |  |  |  |     |
|   |  |  |  |  |  |  |  |  |  |  |/      \_\__|__|__|__|__|__|__|__|__|     |
|   [=========== brushed range ==========]   blue: volume  red: error rate   |
+-------------------+--------------------------------------------------------+
| / Search          | 02:16:58.412  ■ error  payments                        |
| Level             |   payment gateway timeout after 5000 ms                |
|  [x] ■ error  61  | 02:16:58.390  ■ error  checkout                        |
|  [x] ▲ warn   14  |   charge failed: upstream payments 504                 |
|  [ ] ● info  212  | ------------------ 42 s quiet ------------------------ |
| Service           | 02:16:16.002  ● info   search                          |
|  payments    48   |   reindex batch 1187 committed                         |
|  checkout    19   |                                                        |
| Trace id [      ] |                               [Load older logs]        |
+-------------------+--------------------------------------------------------+
```

**The one memorable element: the pen trace.** Log volume is drawn as one continuous blue pen line over the grid, not as bars, with the error rate as a second red line. Dragging across the chart lays a translucent blue band (the brush) that sets the time range. The current mouse position draws a vertical hairline that also highlights the nearest list row's time. The rest of the screen is flat paper.

**Motion.** While live tail is on, the trace extends to the right as new buckets arrive. This is a real change, drawn without easing. New rows insert at the top with no slide. A "3 new rows" marker sits above them until you scroll. Expanding a row is instant. With reduced motion the trace still updates (it is data) but without the 200ms line-drawing interpolation.

**Theme.** Light by default. This direction is about reading dense mixed text and a chart precisely, and dark-on-light gives the highest acuity for long reading. It is also how postmortems, which reuse these views, get shared and printed. A dark "night paper" theme (`#1A2120` background, Grid `#2F3B38`, lighter pens) follows the OS when the user picks "match system".

**Copy.**
- Empty: "Nothing was logged between 02:02 and 02:17 for these filters. Drag across the chart to pick another range."
- Error: "Could not load the chart: the server returned 503 because the log store is unreachable. The list below may be incomplete; try again in a minute."
- Key created: "Copy this query key now. It will not be shown again; if you lose it, revoke it and create a new one."

## Direction C: Trailhead

**Concept.** An investigation is a route: from an alert to a service, from a service to a request, from a request to its trace, and sometimes back again. This direction borrows from road signage. Every pivot you make is posted as a sign on a trail you can walk back along, so nobody on call loses the thread at 3 a.m.

**Palette** (default follows the OS; light shown)

| name | hex | role |
|------|-----|------|
| Road white | `#FBFBF9` | background |
| Asphalt | `#14202B` | primary text (15.9:1) |
| Shoulder | `#55616C` | secondary text, debug level (6.1:1) |
| Sign green | `#006B47` | the trail signs only (white on it 6.6:1); focus outline |
| Signal blue | `#0B61A4` | info level, links (6.2:1) |
| Signal red | `#B3261E` | error level (6.3:1) |

Warn is `#8F5B00` (5.5:1) and fatal is `#6B2C91` (8.5:1).

The dark theme uses `#152029` as background with lifted level colors (all 7:1 or higher).

**Type.** Overpass (OFL) for UI. It is derived from Highway Gothic, the US road-sign face, so it is wide, open and readable at a glance. Overpass Mono for messages, times, ids and attribute values only, where copying exactly and aligning matter. Scale: 12 / 13 / 15 / 17 / 22 px. Sign labels are 15px bold, in sentence case.

**Layout.** Three panes: a slim filter rail (200px), the list (flexible), and a detail pane (360px) that shows the focused row's attributes and its trace's other rows. No inline expansion: selecting a row fills the detail pane, so the list never jumps. The trail sits under the header. The timeline is a 40px strip above the list, aligned to the list's width. On tablets the detail pane becomes a bottom sheet.

```
+----------------------------------------------------------------------------+
| LogSage  shop-prod v  [/ Search                            ]      [Ctrl K] |
| [ Last 15 min ] [ service is payments ] [ trace 4bf92f35 ]   Clear trail   |
+-----------+--------------------------------------+-------------------------+
| Level     | [timeline .._||||||||_..   ]          | Selected log            |
|  ■ error  | 02:16:58.412 ■ error payments gate... | payment gateway timeout |
|  ▲ warn   | 02:16:58.390 ■ error checkout charg...| after 5000 ms           |
|  ● info   |>02:16:57.981 ▲ warn  payments retr... |                         |
|  ○ debug  | 02:16:57.204 ■ error payments gate... | trace   4bf92f3577b3... |
| Service   | 02:16:56.877 ● info  api      POST... | span    00f067aa0ba902b7|
|  payments |                                       | route   /v1/charges     |
|  checkout |                                       | [Filter by this trace]  |
|  ...      |                [Load older logs]      | Same trace (4 rows)     |
+-----------+--------------------------------------+-------------------------+
```

**The one memorable element: the trail.** Each filter or pivot is posted as a green road sign with white text, left to right in the order you made them: "Last 15 min", "service is payments", "trace 4bf92f35". Clicking a sign goes back to that point and drops the signs after it. Hovering a sign shows a cross to remove just that one. The browser's back button walks the same trail, because each sign is a URL state. Nothing else uses green.

**Motion.** A new sign appears on the trail instantly, with no slide. A live-tail row arrives with a 2px Signal blue left edge that fades over 1 s, showing "this is new". The detail pane swaps content without animation. With reduced motion the new-row edge stays for 1 s and disappears without fading.

**Theme.** Follows the OS by default (light or dark). People who are paged at night usually already run their OS in dark mode at night, and the signage palette was built to hold contrast in both. An explicit override is in account settings.

**Copy.**
- Empty: "No logs on this trail. Step back to "service is payments" or widen the time range."
- Error: "This trace id is not valid: it must be 32 hexadecimal characters. Check the value you pasted."
- Key created: "Here is your new ingest key. Copy it now; after you leave this page only its prefix lsk_ing_uj3ey33v is shown."

## Review pass (what changed after the first draft)

- **A:** the first draft used a near-black background with one amber accent, which is the classic log-tool look. It now uses slate `#232B31` and the aviation two-signal convention (amber caution, red warning), and color is reserved for abnormal states.
- **B:** the first draft had a warm off-white background and serif headings, which read as the cream-and-serif default. It is now cool green-grey chart paper with Red Hat Text.
- **B:** the first draft used bars for the timeline like A and C. It now uses a continuous pen line, so the three timelines read differently: lit lamps for A, a trace for B, a strip under a trail for C.
- **C:** the first draft was light with a top chart and a two-column list, too close to B. It now follows the OS theme, uses a three-pane layout with a detail pane instead of inline expansion, and spends its boldness on the trail.
- **All:** the first draft put levels in upper-case pills (`ERROR`). Levels are now lower-case words plus a shape, matching the API values, with no pills.
- **All:** the first draft had a "Retry →" button. The arrow was dropped; buttons say what they do.

## Mockup notes

- Each mockup was checked in a browser at 1366px and 768px wide. Tablet layouts work; on tablets C stacks the detail pane below the list instead of showing the bottom sheet.
- One decoration was removed from each:
  - A: the border around the project switcher.
  - B: the horizontal grid lines in the chart (the vertical time grid stays).
  - C: the rules between rows in the "Same trace" list.
- After the first look, A's warn and error messages were made plain text again: only the level column carries color, otherwise an incident turns the whole list orange and red.
- C's dark theme (OS dark mode) could not be checked visually, because the preview renders local files in light mode only; its colors pass the contrast check.

## Comparison

| | A Dark cockpit | B Strip chart | C Trailhead |
|---|---|---|---|
| Default theme | dark | light | follows OS |
| Starts the user at | which service is abnormal | when it changed | where they have been |
| Density | highest (single column) | medium | medium (3 panes) |
| Memorable element | annunciator row | pen-trace timeline | pivot trail |
| Tablet | good (one column) | chart scales, filters to drawer | detail pane to bottom sheet |
| Extra backend need | per-service baseline error rate | histogram with fine buckets | none beyond search |
| Risk | dark default dislikes daylight/projectors | light default at 3 a.m. | three panes are tight at 1024px |

## Recommendation

**A, Dark cockpit**, with one idea taken from C: the trail, shown quietly as plain text chips without the green.
- A answers the first on-call question ("what is broken?") before the user types anything.
- It matches the night-time context.
- It has the highest information density.
- Its "color means abnormal" rule also disciplines future screens (alerts, incidents).

The per-service baseline can start simply: the error rate over the selected range compared with the same service over the previous 24 h.

## Open questions

1. Which direction, or which mix (for example A plus C's trail)?
2. Is self-hosting the fonts acceptable (two families, a few hundred KB of WOFF2 at most)?
3. Should the dashboard be served from the same origin as the management API (needed for the `__Host-` cookie without CORS), or from its own host?
4. Is live tail over Server-Sent Events acceptable, or must it be polling only?
5. Should the theme preference be stored per user on the server, or per browser?
6. Should the annunciator baseline (if A) be computed in ClickHouse per request, or precomputed by the processor?

## Chosen direction: B2

**Decision (owner):** direction B, Strip chart, is the base. The refined version is B2 ([mockups/direction-b2.html](mockups/direction-b2.html)); `direction-a`, `-b` and `-c` stay as the record of the exploration.

**Changes from B:**
- **Expanded row in zones, using the full width:**
  - `trace_id` and `span_id` on one line at the top.
  - Below them, three columns grouped by meaning:
    - **Request:** method, route, gateway host, timeout, attempt.
    - **Context:** order id, pod, service version, region.
    - **Same trace:** every log that shares the `trace_id`, newest first, with time, level shape and color, service and message. The current log is marked, and clicking an entry selects and expands that log.
  - A log without request attributes says so instead of showing an empty column.
  - Actions: "Filter by this trace" and "Copy as JSON".
  - The columns wrap to two below 1100px and to one below 700px.
- **Timeline as two thin strips over one shared time axis:** logs per 30 s on top, the error and fatal share (% of logs per 30 s) below. Each strip is labeled with its unit and peak value.
  - Brushing across either strip sets the range. The shaded band is the selected range, and the large heading shows it ("02:11:30 to 02:17:00 UTC, selected within the last 15 min").
  - The strips keep the surrounding 15 minutes as context.
  - Facet counts and the result count are for the selected range, and they add up.
- **Keyboard:**
  - `/` search, `j`/`k` move the selection, `Enter`/`Space` expand, `Esc` collapse.
  - `t` filters by the selected log's trace, the same action as the button.
  - The selected row has one tab stop (roving `tabindex`). Every control shows a focus outline.
- **Tablet:** below 900px the filter column moves above the list in three columns instead of disappearing, so the trace id error stays visible. It is one column below 700px.

**Taken from C, and why:**
- **The Same trace list:** pivoting along a trace is the core move in an incident, and seeing the trace's other logs next to the attributes saves a round trip. It is placed inside the expanded row instead of C's side pane, so B's single list stays the only scroll area.
- **The invalid trace id error:** "it must be 32 hexadecimal characters with no dashes", plus how to fix it ("paste the full trace_id, or select a log and press t"). The fix points to the same pivot the Same trace list uses, so both paths end in the same filter.
- **Not taken:** the green trail signs. Undoing a pivot is covered by the URL (back button) and the trace field, and B spends its boldness on the strips.

**Dropped from A:**
- The annunciator row and its per-service 24 h baseline, which would need a new computation in the backend.
- B612.
- The dark default. B2 stays light by default, with the "night paper" dark theme from B.
- **Kept from A's spirit:** color only on level marks, with messages in plain ink.

**Self-critique of B2:**
- **Decoration removed:** the white background and frame behind the chart. The strips sit directly on the chart paper with a single base line.
- **Checked against the list of generic defaults:**
  - no gradients, glass, card grids or KPI tiles;
  - no upper-case eyebrows, middle-dot meta strings or trailing arrows;
  - monospace only for machine text.
- **Widths checked:** 1440, 1100 and 768 in a browser.
- **Fixed after the check:** the facets were hidden on tablets (now above the list), the band did not match the heading (now both show the selection), and the browser restored a stale trace value (`autocomplete="off"`).

## Frontend plan for I2

**Stack:** React + TypeScript + Vite in `frontend/`, built to static files that the `api` service (or a static server on the same origin) serves. Each dependency below needs the owner's approval, and nothing is installed yet.

**Runtime dependencies**

| package | why |
|---|---|
| `react`, `react-dom` | the UI library chosen in the architecture |
| `react-router` | URL-addressable state: project, filters, trace pivots and C's trail all live in the URL, so back/forward and shared links work |
| `@tanstack/react-query` | caching, retries, cancellation and `useInfiniteQuery` for cursor pagination, instead of hand-rolled fetch state |
| `@tanstack/react-virtual` | renders only visible rows, so a 10,000-row result stays fast on a tablet |
| `@fontsource/<chosen fonts>` | self-hosted font files through the bundler, no Google Fonts at runtime |

**Development dependencies:** `typescript`, `vite`, `@vitejs/plugin-react`; `vitest`, `@testing-library/react` and `@testing-library/user-event` for unit tests; `eslint` with `typescript-eslint`; later `@playwright/test` for end-to-end tests against the compose stack.

**Not added:**
- No chart library: the timeline is a hand-written SVG of about 200 lines. It needs bars or a line plus a brush, and the scales are a few lines of arithmetic.
- No command-palette library: it is a filtered list in a dialog.
- No CSS framework: plain CSS with custom properties for the tokens above.
- No state library beyond the URL and React Query.

**What the frontend needs from the backend** (all under `/api/projects/{slug}/`, cookie session, the same role rules as H2, and viewers can read)

| endpoint | need |
|---|---|
| `GET logs?from&to&level&service&q&trace_id&cursor&limit` | session-based log search with the same filters and cursor as the query API's `/v1/logs`, newest first |
| `GET logs/histogram?from&to&buckets=&level&service&q` | counts per time bucket and level; B2 draws volume and the error-plus-fatal share from it. The server picks a bucket size from the range (5 s to 1 h), and the strips request the wider context range (for example 15 min) separately from the selected range |
| `GET logs/facets?from&to&level&service&q&trace_id` | counts per level and per service for the selected range and filters (the filter column); no baseline is needed for B2 |
| `GET logs?trace_id=…` | the Same trace list: the existing search filtered by trace, newest first; log rows must include `span_id` and the attributes map so the expanded row can group them |
| `GET logs/tail?level&service&q` | live tail as Server-Sent Events (`text/event-stream`). It is one-way, works with the session cookie and needs only the standard library. It sends a heartbeat every 15 s, a cap on events per second, and a "dropped N rows" event when the client is too slow |
| `GET me`, `GET projects`, key endpoints | already exist from H2 |

The query API keeps using API keys for machines. The dashboard endpoints reuse its ClickHouse query code through an interface, so the filter semantics stay identical.
