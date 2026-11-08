# ADR 0020: Command palette, shared shortcuts and the I2c-3 polish

Status: accepted (v0.2 package I2c-3)

## Context
The header has shown a `Ctrl K` hint since I2a, and the explorer has grown keyboard shortcuts with each package. A first round of use also found rough edges:
- the key dialog repeated the kind names and clipped the new key;
- the explorer showed its total twice and put a long note in the summary;
- the live status repeated "Live";
- live arrivals were not in time order;
- a section label was in capitals;
- a detail with one attribute group left most of its width empty.

## Decision

### Command palette
- **Opening:**
  - `Ctrl K` or `Cmd K` opens it from anywhere, including from inside a text field, and calls `preventDefault` so the browser's own shortcut does not run.
  - A visible "Commands" button with `aria-keyshortcuts` sits next to the hint.
- **Pattern:** WAI-ARIA combobox with a listbox, inside the shared modal `Dialog` (labelled "Command palette", focus trap, Escape, focus return).
  - The input keeps focus and has `role="combobox"`, `aria-expanded`, `aria-controls` and `aria-activedescendant`.
  - Options have `role="option"` and `aria-selected`.
  - Arrow keys wrap, Home and End jump, Enter runs, and a click runs.
  - Hovering does not move the keyboard selection; a mouse-down does not take focus from the input.
  - A polite region announces "N commands." and "No commands match."
  - The only motion is an 80 ms fade, and none under reduced motion.
- **Commands** are a pure function `commandsFor(AppContext)` in `palette/commands.ts`, each with a label, keywords, a group and an action as data:
  - Navigation: Go to Logs, Go to API keys, and "Switch project: <slug>", which keeps the section.
  - Time range: the five presets and Custom range...
  - Filters: errors and fatal only, all levels, Clear filters, "Filter by service: <name>" for the listed facets, Focus search, Focus trace id.
  - View: live tail on or off, Refresh, Zoom to selection or Zoom out when they apply, and Copy link to this view.
  - Account: Change password..., Sign out of all devices..., Sign out.
  - Help: Show keyboard shortcuts.
  - A command appears only when it applies. For example, the explorer commands appear only on the logs page, and Zoom out only when zoomed.
- **Running a command:** the shell runs navigation, account, help and copy actions itself. Explorer actions go through a bridge: the explorer registers its state and a `run(action)` in a ref, so the shell never re-renders because of it.
  - The account dialogs moved to the shell, so "Sign out of all devices..." opens the existing confirmation and never signs out directly.
  - Commands run after the palette has closed and returned focus, so "Focus search" ends in the search box.
- **Copy link:** copies the current URL with a polite "Copied" notice. When copying fails, the notice shows the link so it can be selected by hand.
- **Empty query:** shows recent commands (up to 5, in component state, never in storage) and then navigation, at most 8.

### Fuzzy matcher
- `palette/fuzzy.ts`, written in the repo with no dependency.
- Case-insensitive subsequence matching on code points; whitespace in the query is ignored.
- A dynamic program picks the best alignment by score:
  - +10 for the first character;
  - +8 for a word start (after a space or `-_:./()[],`);
  - +6 for each character next to the previous match;
  - a small penalty per skipped character.
- Labels are matched before keywords. A keyword-only match ranks below any label match and is not highlighted.
- Equal scores keep the command order, so the result is deterministic.
- The query is never a regular expression.
- Matched characters are rendered as plain `<span>`s; untrusted names stay text.

### One shortcut definition
- `src/shortcuts.ts` lists every shortcut once: id, keys as shown, description, where it applies, whether it works while typing, and a `matches` predicate.
- The explorer, the row keys and the shell match through `shortcutFor(event, ids)`. The `?` help dialog renders the same array, so the help cannot drift from the handlers; a test compares them.
- Single-key shortcuts are ignored while typing. `Ctrl K` is the only one that also works in a field.

### Polish decisions
- **New-key dialog:**
  - Each kind shows its name once with a short description, and the description is linked with `aria-describedby`.
  - The test-log example has two accessible tabs. macOS / Linux uses curl with a single-quoted body. Windows PowerShell writes the body to a temp file with `Set-Content -Encoding ascii -NoNewline` and sends it with `curl.exe --data-binary "@file"`: never the `curl` alias, which runs `Invoke-WebRequest`, and ASCII so there is no BOM. Both examples were run against the dev stack and got 202.
  - Both use `YOUR_KEY` and a timestamp taken when the dialog renders.
  - The key is shown in full in a wrapping, read-only, labelled block, and a click selects all of it. The one-time rules of ADR 0019 are unchanged.
- **Chart:** the scope line keeps the total, and the summary starts with the peaks. The note on small buckets, the meaning of the two strips and the brush move into a native "About this chart" `<details>`. One visible hint stays: "Drag across either strip to select a time range."
- **Page titles:** "Logs - <project> - LogSage", "API keys - <project> - LogSage", "Sign in - LogSage", "Projects - LogSage" and "Page not found - LogSage". A polite region announces the new title after the first page. Filter changes are not page changes.
- **Live:** the button says Live, and the status says what is happening ("Receiving new logs").
  - Rows reach the tail in the order they became visible, not in event time. While Live is on, each batch is sorted newest first by timestamp and goes on top, and the list reads "latest arrivals first".
  - Turning Live off restores strict newest-first order, and so does Refresh, which reloads the list.
  - The facets total is hidden while rows kept from the tail are shown, because it does not count them.
- **Labels:** no `text-transform: uppercase` anywhere; zone headings are "Request", "Context" and "Same trace". A test reads the stylesheet.
- **Detail layout:** `zoneLayout` returns one of three layouts:
  - three zones: Request, Context and Same trace;
  - a pair: one group or "No attributes" next to Same trace, or both groups without a trace;
  - a single group: its attributes flow in `repeat(auto-fill, minmax(18rem, 1fr))` columns.

## Consequences
- New commands are a line in `commandsFor` plus, for explorer actions, a case in the bridge. The command list can be tested without rendering anything.
- A new shortcut must be added to `shortcuts.ts`, or neither the handlers nor the help will know about it.
- Recent commands are forgotten on reload by design.
- No new dependencies, no backend change. The ingest address in the examples is still fixed (`http://localhost:8080`); I2c-4 makes it configurable.

## Lessons (fix after v0.2.0)
- **The bug:** the palette could not be closed once focus had left its panel. Escape was handled only by a key handler on the panel element, Ctrl K only opened, and the backdrop did nothing.
  - A click outside moved focus to `<body>`, and from there nothing but running a command closed it.
  - The owner hit it after opening with Ctrl K in Chrome.
- **Why tests missed it:** the jsdom tests passed because Testing Library's `user.keyboard` sends keys to the element that has focus, which was always the palette input. No test clicked outside, lost focus, or dispatched keys on `<body>` the way a browser does.
- **The fix:**
  - The shell listens for Escape and Ctrl K / Cmd K in the capture phase on the document while the palette is open, and Ctrl K toggles.
  - The backdrop closes on a click, and there is a Close button.
  - A route change closes the palette.
  - Focus returns to the element that had it before.
- **The rule from now on:**
  - Keyboard behaviour of overlays is also checked in a real browser, by opening every way it can be opened and closing every way it can be closed, including after clicking elsewhere.
  - Tests dispatch raw `keydown`/`keyup` sequences on `<body>` and on the focused element, not only through `user.keyboard`.
