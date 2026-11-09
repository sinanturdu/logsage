# ADR 0025: Alerts UI

Status: accepted (v0.3 package K2)

## Context
K1 (ADR 0024) added alert rules, channels, the evaluator and the alert API. K2 puts them in the dashboard in the B2 style ([dashboard brief](../design/dashboard.md)). Two things the UI needed were missing from the API, so K2 has one small backend change (below).

## Decision
**Backend change (the only Go change in K2):**
- Rule reads (`GET rules`, `GET rules/{id}`) carry the state summary that ADR 0024 section 8 promised: `state` (`firing` when any key fires, else `pending`, else the latest transition's target, `ok` without one), `firing`, `pending` and `last_change_at`.
- `GET notifications` takes `rule=` and returns `rule_id` and `rule_name`, so a rule page lists its own deliveries across pages.

**Routes and nav:**
- The header nav is Logs | Error groups | Alerts | API keys.
- `/projects/:slug/alerts` has Rules, Channels and Activity as WAI-ARIA tabs (arrow keys, Home, End). The tab lives in the URL (`tab=channels|activity`, rules is the default), and so does the Activity rule filter (`rule=<id>`).
- `/projects/:slug/alerts/rules/:id` is a rule: summary, the current state per key, the timeline and the delivery history, each with Load more.
- `create=rule|channel` opens a create dialog once (from the command palette), then leaves the URL with a replace, so Back and reload do not open it again.

**Nav count:** the Alerts link reads "Alerts, 2 firing" (rules with at least one firing key, from `GET states`).
- It is polled every 30 s through React Query. There is no polling while the tab is hidden (`refetchIntervalInBackground` off), and the count is refreshed when the tab is shown again.
- A 429 waits for its Retry-After, at least 30 s. When alerts are off, polling stops and the link just says "Alerts".
- The count is text plus weight; its red repeats the words, it is not the meaning.

**Off state:** a 503 whose message starts with "alerts are" (alerts disabled, or the key is missing or malformed) shows one panel instead of errors.
- The panel reads "Alerts are turned off on this server.", followed by the README steps: the PowerShell key command, `ALERTS_ENABLED` and `ALERTS_SECRET_KEY`, and the warning about losing the key. A "Check again" button retries.
- Other 5xx and network errors use the usual Try again; 429 says when to retry. Alert queries never retry the off answer.

**Rule form:** a dialog with focus handling (first field, trap, Escape, focus back). It shows only the chosen type's fields.
- **Validation:** mirrors `alert.Validate` (name 100, note 500, interval 30 s to 1 h, window 1 min to 24 h, `for` 0 to 24 h, `clear_for` 0 to 1 h, repeat off or 5 min to 7 days, 1 to 10 channels, the per-type parameter ranges). Errors sit next to their fields; the first invalid field gets focus.
- **Server errors:** a server 400 names its field ("window: ..."), so it is shown next to that field. A 409 (limits) is shown for the whole form.
- **Durations:** entered as a number and a unit and sent as whole seconds (`300s`).
- **The preview:** two plain sentences, updated on every keystroke, built from the same values the request sends.
  - The first says when it fires and whom it notifies, for example "Fires when error and fatal logs are at least 20% of all logs over 5 minutes, with at least 50 logs, for at least 2 minutes. Notifies: Slack ops."
  - The second says how it resolves, including the 10% hysteresis band and `clear_for`; a `clear_for` of 0 uses the interval, as the server does.
  - The preview is not a live region, because it would speak on every keystroke.
- Service choices come from `/facets` over the last 24 hours.

**One-time secret:** after a webhook channel is created or its secret rotated, the secret is shown once.
- **Display:** a read-only wrapping block with Copy (and the copy fallback message), and "I copied the secret" before Done is enabled. Escape does not close it. This is the API key dialog's flow (ADR 0019).
- **Storage:** the secret lives in component state only. It is never in React Query, the URL, storage or the console, and it is gone when the dialog closes; tests check all of these.
- **"How to verify the signature":** an expandable section with short Node and Python examples. They check `t=<unix>,v1=<hex HMAC-SHA256 over "t.body">` in constant time and reject timestamps more than 5 minutes away. The examples use placeholders; the tests run the exact code against the backend's known vectors (Python when it is installed).

**Channels:**
- The table shows the type, the name, the scheme and host only (as the API returns them), the last delivery (one small request per channel, at most 10) and the sample setting in words.
- Include sample is off by default, with a warning that logs can contain secrets.
- Refused URLs become plain sentences. SSRF refusals read "This address is not allowed. LogSage only sends to public https addresses."; credentials in the URL get their own sentence. A more specific server message (port, resolve) follows the plain one.
- Send test reports inline. A 429 explains "5 per channel every 10 minutes" and the wait, and the test payload is described as marked as a test.
- Deleting a channel names the rules that use it, and which of them would have no other channel.

**Rules table:**
- **State:** text plus shape (○ ok, ◐ pending, ◉ firing, ✓ resolved, ⊘ disabled), with weight and color repeating the word.
- **Enabled switch:** `role="switch"` for owners and admins. Viewers see On or Off as text and get no write controls anywhere.
- **Firing rules:** disabling or deleting a firing rule asks first and says that a final resolved notice goes out with the reason.

**Activity:** project events newest first, with a rule filter and Load more. Failed deliveries are listed above them in a box headed "Failed deliveries", and each says "failed" in words.

**Keyboard and palette:**
- `j`, `k` and `Enter` on the alerts tables, in the shared definition (`shortcuts.ts`) and the help dialog.
- Palette: "Go to Alerts" (also in the empty-query list when there is room), and "Create alert rule..." and "Create channel..." for owners and admins.

**Text only:** names, notes, templates, hosts, services and server messages are rendered with `LogText` or `TemplateText` (text nodes). XSS payload tests cover each of them.

## Consequences
- The rules table needs no request per rule; a rule page lists exactly its deliveries.
- The preview and the client-side validation must follow the backend when its limits or semantics change (`model.ts` names `alert.Validate`).
- Channels' last delivery costs one request per channel (at most 10). A field on the channel list would replace it if channels grow.

**Deferred:**
- Muting or snoozing a rule.
- Creating a rule from an error group's page.
- Email channels.
- A hysteresis field in the form (the API takes `hysteresis_percent`; the form keeps an existing value and otherwise uses the 10% default).
- Editing a rule's levels for `error_rate` (not used by that type).
