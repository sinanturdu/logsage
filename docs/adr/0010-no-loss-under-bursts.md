# ADR 0010: No silent log loss under bursts

Status: accepted (supersedes the trimming part of ADR 0007)

## Context
ADR 0007 capped the Redis Stream with `XADD ... MAXLEN ~ REDIS_STREAM_MAXLEN`. `MAXLEN` trims the oldest entries whether or not a consumer group has read or acknowledged them. In the v0.1 benchmark, a 10-second burst at about 40k events/s outran the processor: 93,195 of 406,900 accepted events (22.9%) were trimmed before they were read. `group_lag` and `group_pending` stayed at 0, because trimmed entries simply disappear from the group's view, so `/stats` looked healthy. A `202 Accepted` that is later dropped breaks the ingest contract and N3.

## Decision
1. **No trimming at publish time.** `XADD` no longer uses `MAXLEN`. `REDIS_STREAM_MAXLEN` is removed, and setting it is a startup error, so an old configuration cannot silently keep the unsafe behavior.
2. **Safe trimming in the processor.** Every `PROCESSOR_TRIM_INTERVAL` (5s) the processor reads `XINFO GROUPS` and, for each group, `XPENDING`. Each group's safe point is:
   - with pending entries: its **lowest pending ID**. Entries below it were delivered in order and are acknowledged; the pending entry itself is kept;
   - with nothing pending: the ID **after its last-delivered ID**. Everything up to that ID is acknowledged, and everything after it has not been delivered yet.

   The trim boundary is the **minimum over all groups**, passed to exact `XTRIM MINID`. Pending and undelivered entries are therefore never removed.
3. **Multi-group rule.** Every consumer group on the stream holds trimming back for everyone, so a future alerting group cannot lose data to a faster processor. A group that has read nothing yet blocks trimming entirely. Nothing is trimmed when there are no groups (nobody has declared what they need), or when any reply cannot be read or understood; a warning is logged instead. A new group must be created before the data it needs is trimmed (at `$` for "from now on"); a group created later at `0` sees only what is still in the stream.
4. **Soft backpressure with 429.** `INGEST_MAX_STREAM_LEN` (default 500,000). The Redis publisher refreshes a cached `XLEN` in the background every 500 ms; there is never an extra Redis call per request. When the cached length is at or above the limit, `Publish` returns `ErrOverloaded` without writing, and ingest answers `429 Too Many Requests` with `Retry-After: 1` and a JSON error. Real publish failures stay `503`.
5. **Memory safety net.** Redis runs with `--maxmemory 1gb --maxmemory-policy noeviction` (AOF kept). A full Redis refuses `XADD` with `OOM`, which ingest maps to `503`, never `202`. Reads, `XACK`, `XAUTOCLAIM` and `XTRIM` are not refused, so the processor can still drain. `XGROUP CREATE` is refused, so on startup the processor accepts an `OOM` reply when the read-only `XINFO GROUPS` shows its group already exists.
6. **Observability.** Ingest `/stats`: `accepted_total` (events), `rejected_overload_total` and `publish_errors_total` (requests), `stream_length_cached` and `max_stream_len`. Processor `/stats` adds `trimmed_total` and `oldest_unprocessed_age_s`; the periodic stats log warns when the age exceeds `PROCESSOR_LAG_WARN_AGE` (30s).

## Trade-offs
- **The limit is soft.** The cache can be up to one refresh interval old, and concurrent requests all pass the same check. Worst-case overshoot is roughly *ingest rate × (refresh interval + XLEN latency) + in-flight batches*, multiplied by the number of ingest instances. Measured on the benchmark laptop: 5,500 events over a 20,000 limit at 20k events/s, 38,300 over 500,000 at about 60k events/s, and 30,000 over 20,000 when 50,000 events arrived in well under one refresh interval. The 1 GB `noeviction` cap is the hard backstop. Making it exact would need an atomic check-and-add (Lua) on every request, which costs a round trip on the hot path.
- **Clients must handle 429.** A client that ignores it drops its own events (they were never accepted). Clients should retry after `Retry-After` with backoff; loadgen's `--retry-on-429` shows the pattern.
- **Memory is bounded by the processor, not by a length cap.** Entries stay until every group has acknowledged them. A stuck group therefore makes the stream grow until backpressure, then until the memory cap, then turns ingest into 429s and 503s. That is the intended, visible failure, instead of silent loss. `oldest_unprocessed_age_s` and its warning make it observable.
- **Trimming has a delay** of up to one interval plus the drain time, and it uses exact `XTRIM MINID` (O(removed entries)) rather than the cheaper approximate form, so the tests can assert exactly what survives.
- **Redis is now a hard dependency for accepting writes**, as before, with the memory cap adding one more way to get `503`.

## Consequences
In the live check, three 10-second bursts at maximum rate stored every accepted event (648,900, 557,800 and 503,500; lost 0). A 30-second burst at concurrency 16 triggered 511 `429` responses, and all 1,260,100 accepted events were stored. With the processor stopped and a 20,000 limit, `202` turned into `429` and accepted equalled stored after the restart (`docs/benchmarks.md`).
