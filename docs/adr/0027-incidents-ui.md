# ADR 0027: Incidents UI

Status: accepted (v0.3 package L2)

## Context
L1 (ADR 0026) added incidents: opened and grouped from alerts, a closed timeline, comments, a status workflow with versions, group links and live suggestions. L2 puts them in the dashboard in the B2 style ([dashboard brief](../design/dashboard.md)).

## Decision
**Backend change:** list rows carry `alert_count` and `group_count`, two subqueries in the list read, so the list can show the link counts. It is the only Go change in L2.

**Routes and nav:**
- **Nav:** the header is Logs | Error groups | Alerts | Incidents | API keys. The Incidents link reads "Incidents, 2 open" from the summary endpoint, in ink and bold; red stays the Alerts firing color.
- **Routes:** `/projects/:slug/incidents` and `/projects/:slug/incidents/:number`. A number that is not a positive integer is an "Incident not found" page; a missing one says "This incident does not exist in this project."

**Shared poll:** one timer (`useSharedPoll`) refreshes both header counts (the alert rules and the incident summary) every 30 s while the tab is visible.
- It refreshes at once when the tab is shown again, and waits for the longest Retry-After after a 429.
- It drops a query whose server said alerts are off.
- The queries themselves no longer poll, so the Rules table still shares its query with the nav count.

**List URL state:**
- **Filters:** `status` (`active`, the default, is open and acknowledged; also `open`, `acknowledged`, `resolved` and `all`), `severity`, `assignee` (`me`, `unassigned`), `service`, `q` and `range` (opened in the last 24 h, 7 d or 30 d).
- **Parsing:** values are parsed defensively and defaults are left out of the URL. The only order is last activity, which is what the API gives.
- **Summary line:** "Showing N incidents, K open" counts only what is loaded; there is never a total the API does not give.

**Marks** (text plus shape, never color alone):
- **Status:** ▶ open, ◧ acknowledged, ◇ resolved. They differ from the alert states (○ ◐ ◉ ✓ ⊘), the levels (○ ● ▲ ■ ◆) and the group statuses (□ – ✓); a test checks it.
- **Severity:** four bars plus the word: ▰▰▰▰ critical, ▰▰▰▱ high, ▰▰▱▱ medium, ▰▱▱▱ low.

**Detail:**
- **Status actions:** acknowledge, resolve and reopen each open a small dialog for an optional note, with a focus trap and Escape.
  - The new status shows at once and is rolled back if the request fails.
  - A polite live region announces the result.
- **409 flow:** "This incident changed while you were editing. Reload it." with a Reload button. The dialog stays open with everything the user typed; Reload refreshes the incident, and saving again uses the new version.
- **All alerts resolved:** a calm prompt, "All linked alerts are resolved. Resolve this incident?", with Resolve. Nothing resolves by itself.
- **Suggested groups:** always under the sentence "Suggestions are the biggest error groups on the affected services during this incident. They are not confirmed causes." Nothing on the page says "root cause" or "caused by"; a test checks it.
- **Linked groups:** a group linked by hand has no stored template. Its template and its count in the incident window come from the group detail, fetched lazily per fingerprint; React Query caches it and cancels the request when the row unmounts. Without it, the first 8 hex characters are shown.
- **Timeline:**
  - One sentence per closed event type, built only from that type's payload fields. Repeats read "fired 5 times, most recently at 14:05 UTC".
  - "Reopened because rule X fired again" takes the rule's name from the linked alerts. The recurrence link also appears in the header.
  - An unknown type is the neutral "Something changed on this incident."; the payload is never dumped.
  - Dropped events and the event cap each get one sentence.
- **Comments:** plain text in the timeline, with a 2000-character counter. Owners and admins delete any comment, after a confirmation. Viewers get no form, and one sentence explains why.
- **Open in Logs:** the explorer over the incident window (opened − 15 min to resolved or last activity, at most 7 days), filtered to the group when exactly one is linked.

**Rule form:**
- A switch "Open an incident when this rule fires" (off by default) and an incident severity shown only when it is on.
- One more preview sentence: "Opens a high severity incident when it fires."
- The Rules table has an Incident column and the rule page a line, both text.

**Palette:** "Go to Incidents" and "Create incident...". On an incident page: Acknowledge, Resolve or Reopen (by status), Assign to me (owners and admins), and "Copy link to this incident" (everyone).

## Consequences
- The two header counts cost one request each per 30 s for a visible tab and nothing for a hidden one.
- Linked groups without a template cost one group detail request each, at most 50 per incident and cached.
- The incidents test server applies the API's CSRF rule, validation messages, version checks, workflow and roles (the AGENTS.md rule about test fakes).

**Deferred:**
- Notifications on comments, status changes or assignment.
- Paging, escalation and on-call schedules.
- A status page and postmortems.
- Merging or splitting incidents, and moving an alert between incidents.
- Listing a rule's incidents on the rule page: the API has no filter by rule, so no request is invented.
