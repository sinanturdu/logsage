# Benchmarks (v0.1)

> **Local Docker on a Windows laptop, not a production benchmark.** All services, Redis, ClickHouse and the load generator share one machine and one Docker Desktop VM. Use these numbers to find bottlenecks and spot regressions, not to plan capacity.
>
> Two measurement sessions are below. The **2026-11-07 section** is after the no-loss fix (ADR 0010). The **older results** were measured on 2026-11-06/07 with the host disk 96–100% full and the publish-time stream cap that lost events; they are kept for comparison.

## Environment (older session; the 2026-11-07 section notes its differences)

| | |
|---|---|
| Date | 2026-11-06/07 |
| CPU | Intel Core i7-10750H @ 2.60 GHz, 6 cores / 12 threads |
| RAM | 15.8 GB |
| OS | Windows 11 Home Single Language 10.0.26300 |
| Docker | Docker Desktop 4.98.0, engine 29.9.2, WSL 3.0.3.0, kernel 6.18.54.1 |
| Docker VM limits | 12 CPUs, 7.67 GiB memory (WSL defaults; no `.wslconfig`) |
| Disk | C: 476 GB, 16–21 GB free at the start, 4.3 GB free for most runs |
| Images | `clickhouse/clickhouse-server:24.8` (24.8.14.39), `redis:7` (7.4.12), `logsage-backend:dev` built with `golang:1.23` |
| Code | services built from commit `8775411` (service code is unchanged after it on this branch except the behavior-neutral `ParseBatch` refactor); loadgen from `dab9fc2`; Redis named volume from `343b582` |
| Client | `cmd/loadgen` on the Windows host, through Docker Desktop port forwarding, unless noted |

## How to reproduce

```bash
cp .env.example .env            # set real keys; see README
docker compose --env-file .env -f infra/docker-compose.yml -f infra/docker-compose.app.yml --profile app up -d --build
cd backend && go build -o ../loadgen ./cmd/loadgen && cd ..
set -a; . ./.env; set +a
export LOADGEN_INGEST_KEY="$INGEST_API_KEY" LOADGEN_QUERY_KEY="$QUERY_API_KEY"
STATS=http://127.0.0.1:8081/stats
```

Since v0.2.0 the static `INGEST_API_KEY` / `QUERY_API_KEY` work only with `ALLOW_LEGACY_STATIC_KEYS=true` in `.env`. Without it, use per-project keys: after `scripts\dev.ps1 up`, `export LOADGEN_INGEST_KEY="$(cat .dev/ingest.key)"`, or create keys in the dashboard's API keys screen.

Since ADR 0010 there is no stream cap to tune: publishing never trims, and ingest answers `429` at `INGEST_MAX_STREAM_LEN` (500,000). (The older runs below set the removed `REDIS_STREAM_MAXLEN` to 1,000,000 or 400,000 to avoid the loss described in finding 1.) To check that nothing is lost, add `--verify` to any `ingest` run that uses `--wait-drain`, with `LOADGEN_CLICKHOUSE_USER` and `LOADGEN_CLICKHOUSE_PASSWORD` set.

```bash
# 1. Ingest throughput at several concurrency levels (batch 100), with loss check
for c in 1 4 16 64; do ./loadgen ingest --concurrency $c --batch-size 100 --duration 15s --wait-drain $STATS --json; done

# Burst with the no-loss check (2026-11-07 section)
./loadgen ingest --concurrency 4 --batch-size 100 --duration 10s --wait-drain $STATS --verify --json

# N1: latency at a steady 10,000 events/s (15 s in the 2026-11-07 section, 30 s before)
./loadgen ingest --concurrency 16 --rate 10000 --duration 15s --wait-drain $STATS --json

# 2. Processor-only drain: queue a backlog with the processor stopped, then time the drain
docker compose --env-file .env -f infra/docker-compose.yml -f infra/docker-compose.app.yml --profile app stop processor
./loadgen ingest --concurrency 8 --max-events 300000 --duration 0
docker compose --env-file .env -f infra/docker-compose.yml -f infra/docker-compose.app.yml --profile app start processor
curl -s $STATS   # poll until group_lag and group_pending are 0
cd backend && go test ./internal/processor -run '^$' -bench ParseAndEncodeBatch -benchtime 200x && cd ..

# 3. Query latency: 100k, then 1M rows spread over 24h, loaded below processor throughput
docker compose -f infra/docker-compose.yml exec clickhouse clickhouse-client --user "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" --query "TRUNCATE TABLE logsage.logs"
./loadgen ingest --concurrency 8 --rate 15000 --max-events 100000 --duration 0 --time-spread 24h --seed 100 --wait-drain $STATS
./loadgen query --reps 20 --window 24h --paginate-window 30m --page-limit 1000
./loadgen ingest --concurrency 8 --rate 15000 --max-events 900000 --duration 0 --time-spread 24h --seed 200 --wait-drain $STATS
./loadgen query --reps 20 --window 24h --paginate-window 30m --page-limit 1000
```

The cost of `FINAL` was measured in ClickHouse directly: the API's query with and without `FINAL`, 11 runs each with the first discarded, durations from `system.query_log` (`log_comment` tags), before and after `OPTIMIZE TABLE logsage.logs FINAL`.

## v0.2 regression check (2026-11-07)

Before the v0.2.0 release: the same N1 scenario as the re-run below, against the full v0.2 stack (`scripts\dev.ps1 up`, the dashboard served by `cmd/api`, 69k seeded events already stored). Ingest authenticated with a per-project key from PostgreSQL (`ALLOW_LEGACY_STATIC_KEYS` off, key cache 30 s).
- Same laptop, Docker Desktop 29.9.2, services built from commit `21e3b3c`; loadgen from the same commit, on the Windows host.
- C: had about 27 GB free.

```bash
loadgen ingest --duration 15s --concurrency 16 --batch-size 100 --rate 10000 --wait-drain http://127.0.0.1:8081/stats --verify
```

| Run | Accepted (events/s) | p50 | p95 | p99 | max | Statuses | Drain after stop | Stored / lost |
|---|---|---|---|---|---|---|---|---|
| 1 | 150,100 (10,004) | 3.80 ms | 5.76 ms | 6.66 ms | 13.72 ms | 1,501 × 202 | 1.13 s | 150,100 / 0 |
| 2 | 150,100 (10,005) | 3.23 ms | 4.99 ms | 6.34 ms | 14.64 ms | 1,501 × 202 | 1.13 s | 150,100 / 0 |
| 3 | 150,000 (10,000) | 3.78 ms | 5.85 ms | 7.38 ms | 12.26 ms | 1,500 × 202 | 0.10 s | 150,000 / 0 |

**No regression: p99 6.3–7.4 ms (the v0.1 re-run had 5.9–7.9 ms), no errors, nothing lost.**
- Each p99 rests on about 15 samples (1,500 requests), so treat it as an indication.
- End to end, the processor kept up: 9,302, 9,303 and 9,930 events/s including the drain.
- Only three runs, on a laptop, with the loadgen sharing the CPU.

## 2026-11-07: after the no-loss fix

Same laptop, Docker Desktop 29.9.2, services and loadgen built from commit `07fa556` (fix/no-loss-under-bursts). Fresh Docker state (images re-pulled, empty volumes). Redis runs with `--maxmemory 1gb --maxmemory-policy noeviction`; ingest with `INGEST_MAX_STREAM_LEN=500000` unless noted. **C: had 18.3 GB free at the start, 5.3–5.6 GB during the N1 and demo runs** (Docker's virtual disk grew, and other space went for reasons outside this work). So the disk was not much emptier than in the older session; see the N1 note below.

### Bursts at maximum rate (the scenario that lost 22.9% before)

Concurrency 4, batch 100, 10 s, no rate limit, `--wait-drain --verify` (stored = rows of the run in ClickHouse with `FINAL`).

| Run | Requests | 202 | 429 | Accepted (events/s) | p99 | Drained after send | Stored | Lost |
|---|---|---|---|---|---|---|---|---|
| 1 | 6,489 | 6,489 | 0 | 648,900 (64,859/s) | 12.7 ms | 8.2 s | 648,900 | **0** |
| 2 | 5,578 | 5,578 | 0 | 557,800 (55,748/s) | 19.1 ms | 8.1 s | 557,800 | **0** |
| 3 | 5,035 | 5,035 | 0 | 503,500 (50,330/s) | 17.7 ms | 7.8 s | 503,500 | **0** |

The backlog stayed below 500,000, so backpressure did not engage. Redis `used_memory_human` after each burst (after drain and trimming): 31.2 MB, 17.9 MB, 4.1 MB; peak 148 MB. The trimmer removed 1,700,700 acknowledged entries during these runs.

**Burst with backpressure engaged:** concurrency 16, 30 s, `--retry-on-429`. 13,112 requests: 12,601 × 202 and 511 × 429 (all retried). 1,260,100 accepted (41,988 events/s, p50 13.4 ms, p99 60.2 ms including the retried requests), **1,260,100 stored, lost 0**. The cached stream length peaked at 538,300 (7.7% over the soft limit). Redis peaked at 160 MB. One batch of 100 events was still being retried when the 30 s ended; it was never accepted, so it is not a loss.

### 429 demo (processor stopped, `INGEST_MAX_STREAM_LEN=20000`)

| | Sent | 202 | 429 (nothing written) | Stream length after | After processor restart |
|---|---|---|---|---|---|
| A: 50,000 events as fast as possible (2 senders, batch 500) | 50,000 | 100 requests (50,000 events) | 0 | 50,000 | accepted 50,000, stored 50,000, lost 0 |
| B: 50,000 events paced at 20,000 events/s | 50,000 | 51 requests (25,500 events) | 49 requests (24,500 events) | 25,500 (stayed there) | accepted 25,500, stored 25,500, lost 0 |

In A all 50,000 events arrived within one 500 ms refresh of the cached length, so no request saw the limit: the soft limit overshot by 30,000. The next request got:

```
HTTP/1.1 429 Too Many Requests
Content-Type: application/json
Retry-After: 1

{"error":"ingest queue is full, retry after the Retry-After delay"}
```

In B the cache caught up after about 15,500 accepted events and requests switched from `202` to `429` (overshoot 5,500, 27.5%). This is the trade-off documented in ADR 0010: the limit is soft by about ingest rate × refresh interval, and the 1 GB Redis memory cap is the hard backstop.

### N1 re-run: steady 10,000 events/s (concurrency 16, 15 s per run, batch 100)

| Run | Events/s | Requests | p50 | p95 | p99 | max | Lost |
|---|---|---|---|---|---|---|---|
| 1 | 10,004 | 1,501 | 2.97 ms | 4.91 ms | 6.29 ms | 13.25 ms | 0 |
| 2 | 10,003 | 1,501 | 3.21 ms | 5.29 ms | 6.16 ms | 9.51 ms | 0 |
| 3 | 10,000 | 1,500 | 3.20 ms | 5.63 ms | 7.94 ms | 12.56 ms | 0 |
| 4 | 10,004 | 1,501 | 3.08 ms | 5.04 ms | 6.22 ms | 8.08 ms | 0 |
| 5 | 10,000 | 1,500 | 3.36 ms | 5.62 ms | 6.98 ms | 10.80 ms | 0 |
| 6 | 10,000 | 1,500 | 3.24 ms | 5.83 ms | 7.45 ms | 17.69 ms | 0 |
| 7 | 10,003 | 1,501 | 3.16 ms | 5.16 ms | 6.37 ms | 7.54 ms | 0 |
| 8 | 10,004 | 1,501 | 3.13 ms | 4.90 ms | 5.91 ms | 8.30 ms | 0 |

**Median p99 6.33 ms; 8 of 8 runs under 50 ms.** N1 is met in this session.

What changed compared with the older N1 runs (7 of 14 above 50 ms):
- Runs were 15 s instead of 30 s (150,000 events per run instead of 300,000), to keep disk use down. That gives about 1,500 requests per run, so each p99 rests on about 15 samples.
- The disk was **not** much emptier (5.3–5.4 GB free against 4.3 GB before), so free disk alone does not explain the difference.
- The likely main cause is trimming. The stream now stays near empty: the trimmer removes acknowledged entries every 5 s, and `logsage.logs` was emptied between runs. Redis's append-only file therefore stays small, so AOF rewrites are cheap. Before, the stream held 400k–1M entries and the stalls coincided with large AOF rewrites (see "Bottlenecks" below).

This is consistent with the earlier diagnosis but not a controlled experiment. Re-measuring with 30 s runs and a large standing backlog would separate the factors.

## Results before the no-loss fix (2026-11-06/07, nearly full disk)

### 1. Ingest throughput (unlimited rate, batch 100)

Series A: Redis data on a bind mount (`./data/redis`, the compose file at the start of this work), cap 1M, 15 s per run.

| Concurrency | Events/s accepted | p50 | p95 | p99 | max | Drain after send | End-to-end | Lost |
|---|---|---|---|---|---|---|---|---|
| 1 | 14,860 | 5.9 ms | 9.4 ms | 14.1 ms | 223 ms | 0.1 s | 14,759 ev/s | 0 |
| 4 | 36,110 | 9.7 ms | 17.5 ms | 22.2 ms | 42 ms | 8.4 s | 23,183 ev/s | 0 |
| 16 | 56,161 | 25.9 ms | 40.1 ms | 49.9 ms | 83 ms | 21.0 s | 23,417 ev/s | 0 |
| 64 | 70,576 | 86.8 ms | 112.8 ms | 136.0 ms | 280 ms | 29.9 s | 23,646 ev/s | 0 |

Series B: Redis on a named volume (current compose), cap 400k, 10 s per run.

| Concurrency | Events/s accepted | p50 | p95 | p99 | max | Notes |
|---|---|---|---|---|---|---|
| 1 | 25,724 | 3.2 ms | 5.0 ms | 8.1 ms | 252 ms | e2e 23,331 ev/s, 0 lost |
| 4 | 50,462 | 5.4 ms | 10.0 ms | 18.5 ms | 1,798 ms | e2e 27,356 ev/s, 0 lost |
| 16 | 49,406 | 16.2 ms | 31.8 ms | 205.4 ms | 2,008 ms | 16 × 503 (Redis publish timeout); 200 events stored twice |
| 64 | – | – | – | – | – | aborted: drain never completed; the backlog most likely passed the 400k cap and was trimmed |

**Finding 1, data loss at the default cap.** With `REDIS_STREAM_MAXLEN=100000`, concurrency 4 for 10 s: 406,900 events accepted (40,688 ev/s, p99 19.5 ms, all 202), only **313,705 stored, 93,195 (22.9%) lost**. Ingest outran the processor, and `XADD MAXLEN ~` trimmed entries the consumer group had not read yet. Nothing reported it: `group_lag` and `group_pending` were 0 and `/stats` looked healthy. This is the most important follow-up from v0.1.

### N1 runs: latency at a steady 10,000 events/s (concurrency 16, 30 s)

| Setup | p50 | p99 per run |
|---|---|---|
| Bind mount, client on host | 4.8–5.5 ms | 80.4, 10.0, 17.9, 119.0, 77.0 ms |
| Bind mount, processor stopped | 4.8 ms | 91.0, 177.9 ms |
| Bind mount, client inside the Docker network | 3.6 ms | 43.1, 41.4 ms |
| Named volume, client on host | 2.7–3.2 ms | 34.3, 75.6, 325.7, 8.3, 15.3 ms |
| Named volume, AOF switched off (diagnostic only) | 2.5–2.6 ms | 11.9, 5.8 ms |

No events were lost in any steady 10k run (processed = accepted).

### 2. Drain time and processor throughput

| Measurement | Result |
|---|---|
| End-to-end under maximum ingest load (series A/B) | 20,000–27,000 events/s |
| 300k-event backlog, processor started on it | 10.9 s including container start = 27,500 events/s |
| Same drain, processing window only (`system.query_log`) | 300 inserts × 1000 rows in 7.35 s = **40,800 events/s**, 24.5 ms per batch |
| ClickHouse insert per 1000-row batch | p50 6 ms, p99 17 ms |
| Parse + JSONEachRow encode per 1000-entry batch (Go benchmark) | 3.7–4.1 ms, 1.7 MB and 12k allocations |
| Remainder per batch (Redis `XREADGROUP` of 1000 entries plus `XACK`, go-redis decoding) | about 14 ms |

### 3. Query latency (24h window, 20 measured reps after 1 warm-up, client-side)

| Scenario | 100k rows: p50 / p99 | 1M rows: p50 / p99 | Rows returned (1M) |
|---|---|---|---|
| a. time range only | 28 / 47 ms | 276 / 342 ms | 100 |
| b. + `service=checkout` | 15 / 18 ms | 63 / 76 ms | 100 |
| c. + `level=error` | 26 / 29 ms | 211 / 315 ms | 100 |
| d. + `q=timeout` | 30 / 41 ms | 223 / 313 ms | 100 |
| e. + `trace_id` | 21 / 24 ms | 199 / 273 ms | 1 |
| f. paginate 30 min, limit 1000 (all pages) | 74 / 112 ms (3 pages, 2,076 rows) | 1,348 / 1,515 ms (21 pages, 20,250 rows) | 20,250 |

The 100k dataset was merged (`OPTIMIZE ... FINAL`) before measuring; the 1M dataset was measured as ingested (9 active parts). The table on disk was 43.8 MiB for 1M rows.

Cost of `FINAL` at 1M rows (server-side p50):

| Query | 9 parts: `FINAL` / plain | After merge (2 parts): `FINAL` / plain |
|---|---|---|
| 24h, newest 101 rows | 184 / 102 ms (rows read 1.34M / 1.0M) | 100 / 103 ms |
| 24h + `service` | 48 / 11 ms (285k / 52k rows) | 30 / 5 ms |
| `count()` over 24h | 87 / 6 ms | 8 / 4 ms |

## Against the requirements (before the no-loss fix; see the 2026-11-07 section for N1 and losses now)

| Target | Result | Verdict |
|---|---|---|
| **N1** ingest p99 < 50 ms | At 10k events/s (14 runs, diagnostics excluded): p50 2.7–5.5 ms; p99 under 50 ms in 7 runs (8.3–43.1 ms), 75.6–325.7 ms in the other 7 | **Not reliably met on this machine** |
| **N2** 10,000 events/s | 10k events/s sustained end-to-end with zero loss in every run; ingest accepts up to ~50–70k events/s, the processor handles 23–41k events/s | **Met**, but see finding 1: bursts above processor throughput lose data at the default cap |
| **N4** 24h search < 2 s | 1M rows over 24h: every filter p99 ≤ 342 ms | **Met for this dataset**, with caveats below |

### Bottlenecks and explanations

- **N1 tail latency is I/O stalls, not request handling.** Median latency stays at a few milliseconds. The first cause found was Redis on a bind mount from the Windows filesystem: AOF rewrites and the default RDB snapshot rules wrote about 1 GB files through Docker Desktop's file sharing, and one snapshot took 79 s. Moving Redis to a named volume (commit `343b582`) helped but did not remove the tail. The remaining spikes coincide with Redis reporting `aof_delayed_fsync` (fsync over 2 s, 8 times) and with large AOF rewrites while the stream held 400k–1M entries, on a host disk that was 100% full. Stopping the processor did not help, which rules out ClickHouse contention. Turning AOF off made two runs clean, which points at persistence I/O. Follow-ups: rerun on a machine with free disk and a smaller stream, then look at `appendfsync` and AOF rewrite settings. Persistence settings were not changed to pass N1.
- **Processor throughput is limited by its serial loop.** Each 1000-entry batch takes about 24.5 ms: about 6 ms ClickHouse insert, about 4 ms CPU for parsing and encoding, and about 14 ms of Redis round trips and decoding, with one batch in flight. ClickHouse could take several times more. Options: overlap reading the next batch with the insert, run more consumers in the group, use larger batches, cut allocations in parse/encode.
- **Queries without a `service` filter read the whole window.** The sorting key is `(project_id, service, timestamp, event_id)`, so `ORDER BY timestamp DESC LIMIT 101` cannot stop early: the plain query read all 1M rows. A service filter cut it about 4× (76 ms against 342 ms p99). **Extrapolation:** 1M rows over 24h is about 11.6 events/s. At the N2 rate a 24h window holds about 864M rows, and the time-range-only query would take minutes, not under 2 s. N4 at N2 volume needs a skip index or projection on `timestamp`, or a different sorting key (follow-up).
- **`FINAL` costs most on fresh, unmerged data** (1.8× to 14× in the table above) and almost nothing after merges. That matches ADR 0009. `do_not_merge_across_partitions_select_final` is the first thing to try.

## Raw output

Raw loadgen JSON was kept outside the repository; only these summaries are committed.
