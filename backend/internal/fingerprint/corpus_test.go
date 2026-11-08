package fingerprint

import "strings"

// corpus is the golden corpus of ADR 0022: every loadgen template plus
// realistic first lines from Go, Java, Python, HTTP servers, databases and
// the OS. Its templates and fingerprints are pinned in testdata/golden.json.
var corpus = []string{
	// loadgen baseline
	"GET /api/v1/orders/482913 completed in 47ms",
	"user 31337 logged in from 10.12.200.7",
	"order 3fa9c0d1 created with 3 items, total 128.07 EUR",
	"order beefcafe created with 3 items, total 128.07 EUR",
	"search query returned 1520 results in 41ms",
	"email 9b2e44c1a7f0 queued for user 1290",
	"cache miss for key product:55123",
	"loaded config from /etc/app/flags.yaml in 312us",
	"slow query on table orders took 2921ms (threshold 500ms)",
	"retrying request to payment gateway, attempt 2 of 3",
	"rate limit at 93% for client 8a1f0c",
	"rate limit at 93% for client facade",
	"payment 7c1e0b9a42 declined: gateway timeout after 7086ms",
	"failed to write /var/data/exports/5078.json: disk quota exceeded",
	"connection to db-3.internal:5432 refused",
	"panic: runtime error: nil pointer dereference in handler CreateOrder",
	"panic: runtime error: nil pointer dereference in handler Refund",
	// loadgen incident and seed
	"upstream bank-api returned 503 for transaction 0f3c9a77b2",
	"circuit breaker open for bank-api, 37 requests rejected",
	"payment gateway timeout after 5000 ms (psp-eu-2)",
	"retrying charge for ord_7Q2K9M, attempt 3 of 3",
	"charge failed: upstream payments returned 504",
	"POST /v1/checkout 502 in 5151 ms",
	// Go
	"panic: runtime error: index out of range [5] with length 3",
	"panic: runtime error: invalid memory address or nil pointer dereference\n[signal SIGSEGV: segmentation violation code=0x1 addr=0x18 pc=0x4a2b1c]",
	"http: panic serving 10.0.3.14:53412: runtime error: slice bounds out of range [:12] with capacity 8",
	"context deadline exceeded (Client.Timeout exceeded while awaiting headers)",
	"dial tcp 10.0.4.21:6379: connect: connection refused",
	"read tcp 172.17.0.3:41234->172.17.0.5:5432: i/o timeout",
	"pq: duplicate key value violates unique constraint \"users_email_key\"",
	"json: cannot unmarshal string into Go struct field Order.items.qty of type int",
	"open /tmp/upload-3829121.part: no such file or directory",
	// Java
	"java.lang.NullPointerException: Cannot invoke \"String.length()\" because \"<local1>\" is null",
	"java.lang.IllegalStateException: Connection pool exhausted (max=50, active=50, idle=0)",
	"org.postgresql.util.PSQLException: ERROR: deadlock detected",
	"java.net.SocketTimeoutException: Read timed out after 30000 ms",
	"Exception in thread \"main\" java.lang.OutOfMemoryError: Java heap space",
	"Caused by: java.sql.SQLTransientConnectionException: HikariPool-1 - Connection is not available, request timed out after 30005ms.",
	// Python
	"Traceback (most recent call last):\n  File \"app.py\", line 12, in <module>",
	"KeyError: 'user_id'",
	"ValueError: invalid literal for int() with base 10: 'abc'",
	"ValueError: invalid literal for int() with base 10: 'xyz'",
	"ConnectionResetError: [Errno 104] Connection reset by peer",
	"redis.exceptions.TimeoutError: Timeout reading from socket",
	"psycopg2.OperationalError: could not connect to server: Connection refused",
	// HTTP
	"GET /api/v2/users/8f14e45f-ceea-467a-9575-1f5b3c1e2a9b 404 in 3 ms",
	"POST /api/v1/payments 500 in 120ms",
	"POST /api/v1/payments 503 in 98ms",
	"10.1.2.3 - - [07/Nov/2026:10:15:30 +0000] \"GET /index.html HTTP/1.1\" 200 512",
	"upstream timed out (110: Connection timed out) while reading response header from upstream, client: 192.168.1.10, server: api.example.com, request: \"GET /v1/orders?page=2 HTTP/1.1\"",
	"request to https://api.stripe.com/v1/charges?amount=5000&currency=eur failed with status 429",
	"request to https://api.stripe.com/v1/refunds/re_3NqX2b failed with status 429",
	// SQL and connections
	"ERROR: relation \"orders_2026_11\" does not exist at character 15",
	"too many connections for role \"app\"",
	"could not obtain lock on row in relation \"accounts\"",
	"Lost connection to MySQL server at 'reading initial communication packet', system error: 0",
	// timeouts, OOM, resources
	"timeout after 30s waiting for lock /var/lock/job-17.lock",
	"Out of memory: Killed process 18213 (java) total-vm:8123456kB, anon-rss:4012348kB",
	"OOMKilled: container api-7d9f8b6c4-x2kqp exceeded memory limit 512Mi",
	"disk usage at 97% on /dev/sda1",
	"write /data/wal/000000010000000A00000042: no space left on device",
	// auth and validation
	"invalid token for user alice@example.com: signature mismatch",
	"login failed for user bob@example.com from 203.0.113.9: wrong password",
	"permission denied: role viewer cannot revoke key lsk_ing_hyqay6m3",
	"validation failed: field \"email\" must be a valid address",
	"validation failed: field \"amount\" must be positive, got -12.50",
	"JWT expired at 2026-11-07T10:15:30Z, now 2026-11-07T10:20:01Z",
	// rate limits
	"rate limited: 1200 requests in 60s exceeds quota of 1000 for api key 4f9c2e1a8b7d6c5e",
	"429 Too Many Requests: retry after 17 seconds",
	// ipv6 and misc
	"connection from [2001:db8::8a2e:370:7334]:443 reset",
	"cache key session:7f3c2a91 evicted after 3600s",
	"job 7f3c2a91-2b4d-4c1e-9a77-0e5f6d3b2a19 failed after 3 retries",
	"worker-12 crashed with exit code 137",
	"\x00\xff\xfe binary \x01 junk",
	"",
	// added for the implementation: a query string on a bare path, and a status after HTTP/1.1
	`upstream timed out, request: "GET /v1/orders?page=2 HTTP/1.1", upstream: "http://10.0.0.7:8080/v1/orders?page=2"`,
	"HTTP/1.1 503 Service Unavailable from upstream catalog",
	// J1.1: Python tracebacks use their last non-empty line (the exception)
	"Traceback (most recent call last):\n  File \"/app/orders.py\", line 88, in charge\n    resp = client.post(url)\nrequests.exceptions.ConnectTimeout: HTTPSConnectionPool(host='psp.example.com', port=443): Max retries exceeded",
	"Traceback (most recent call last):\n  File \"/app/orders.py\", line 91, in charge\n    qty = int(raw)\nValueError: invalid literal for int() with base 10: 'abc'",
	"Traceback (most recent call last):\n  File \"/app/users.py\", line 12, in load\n    return cache[user_id]\nKeyError: 'user_id' for user 31337",
	"Traceback (most recent call last):\n  File \"/app/users.py\", line 140, in load\n    return cache[user_id]\nKeyError: 'user_id' for user 42",
	"Traceback (most recent call last):\n  File \"/app/db.py\", line 7, in connect\n    conn = pool.get()\nTimeoutError: pool exhausted after 30s\n\n   \n",
	"Traceback (most recent call last):\n" + strings.Repeat("  File \"/app/worker.py\", line 42, in run\n    self.step()\n", 120) + "RecursionError: maximum recursion depth exceeded while calling a Python object",
	"Traceback (most recent call last):\n   \n\t\n",
	"Traceback (most recent call last):\n  File \"/app/a.py\", line 3, in f\n    g()\nKeyError: 'id'\n\nDuring handling of the above exception, another exception occurred:\n\nTraceback (most recent call last):\n  File \"/app/a.py\", line 5, in f\n    raise LookupError(\"missing 7\")\nLookupError: missing 7",
}
