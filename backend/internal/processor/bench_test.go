package processor

import (
	"fmt"
	"testing"
	"time"
)

// BenchmarkParseAndEncodeBatch measures the processor's CPU work for one
// 1000-entry batch (parse every entry, encode JSONEachRow), without Redis or
// ClickHouse round trips. See docs/benchmarks.md.
func BenchmarkParseAndEncodeBatch(b *testing.B) {
	entries := make([]Entry, 1000)
	for i := range entries {
		entries[i] = validEntry(fmt.Sprintf("1793999215713-%d", i))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		rows := make([]Row, 0, len(entries))
		for _, e := range entries {
			row, reason := parseEntry(e)
			if reason != "" {
				b.Fatal(reason)
			}
			rows = append(rows, row)
		}
		if _, err := encodeRows(rows, time.Now()); err != nil {
			b.Fatal(err)
		}
	}
}
