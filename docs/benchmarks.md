# Benchmarks (v0.1)

> **Local Docker on a Windows laptop, not a production benchmark.** All services, Redis, ClickHouse and the load generator share one machine and one Docker Desktop VM. The host disk was 96–100% full during the runs (see "Environment"), which caused I/O stalls. Use these numbers to find bottlenecks and spot regressions, not to plan capacity.

## Environment

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

The stream cap matters: with the default `REDIS_STREAM_MAXLEN=100000`, a burst faster than the processor loses events (see finding 1). The throughput runs used a larger cap set in `.env` (`REDIS_STREAM_MAXLEN=1000000` for series A, `400000` for series B and later), followed by `up -d --force-recreate ingest processor`.

```bash
# 1. Ingest throughput at several concurrency levels (batch 100), with loss check
for c in 1 4 16 64; do ./loadgen ingest --concurrency $c --batch-size 100 --duration 15s --wait-drain $STATS --json; done

# N1: latency at a steady 10,000 events/s
./loadgen ingest --concurrency 16 --rate 10000 --duration 30s --wait-drain $STATS --json

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

## Results

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

## Against the requirements

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
