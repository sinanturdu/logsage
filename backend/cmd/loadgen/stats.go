package main

import (
	"fmt"
	"math"
	"net"
	"net/url"
	"sort"
	"time"
)

// percentile returns the p-th percentile (0 < p <= 100) of sorted using the
// nearest-rank method: the smallest value with at least p% of samples at or
// below it. sorted must be ascending; an empty slice yields 0.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// LatencySummary holds latency percentiles in milliseconds.
type LatencySummary struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50_ms"`
	P95   float64 `json:"p95_ms"`
	P99   float64 `json:"p99_ms"`
	Max   float64 `json:"max_ms"`
}

func summarize(samples []time.Duration) LatencySummary {
	s := append([]time.Duration(nil), samples...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	ms := func(d time.Duration) float64 { return math.Round(float64(d)/float64(time.Millisecond)*100) / 100 }
	out := LatencySummary{Count: len(s)}
	if len(s) == 0 {
		return out
	}
	out.P50, out.P95, out.P99, out.Max = ms(percentile(s, 50)), ms(percentile(s, 95)), ms(percentile(s, 99)), ms(s[len(s)-1])
	return out
}

func (l LatencySummary) String() string {
	return fmt.Sprintf("p50 %.2f ms  p95 %.2f ms  p99 %.2f ms  max %.2f ms  (n=%d)", l.P50, l.P95, l.P99, l.Max, l.Count)
}

// checkTarget refuses non-local URLs unless allowRemote is set, so a load
// test cannot be pointed at a shared system by accident.
func checkTarget(raw string, allowRemote bool) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid URL %q", raw)
	}
	if allowRemote {
		return nil
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("refusing non-local target %q; pass --allow-remote if you really mean it", host)
}
