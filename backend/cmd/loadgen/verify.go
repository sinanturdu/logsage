package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
)

// verifier counts the stored rows of one loadgen run in ClickHouse. Every
// event of the run carries the attribute loadgen_run=<run id>.
type verifier struct {
	ch      *clickhouse.Client
	timeout time.Duration // how long to wait for stored rows to settle
}

type verifyResult struct {
	RunID     string `json:"run_id"`
	Accepted  int64  `json:"accepted"`
	Stored    int64  `json:"stored"`     // distinct rows (FINAL)
	StoredRaw int64  `json:"stored_raw"` // rows before deduplication
	Lost      int64  `json:"lost"`       // accepted - stored, if positive
	Extra     int64  `json:"extra"`      // stored - accepted, if positive (503 partial writes)
	Summary   string `json:"summary"`
}

func newRunID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// verify waits until the stored count stops changing (or reaches accepted)
// and reports the difference.
func (v *verifier) verify(ctx context.Context, runID string, accepted int64) (verifyResult, error) {
	count := func(final string) (int64, error) {
		out, err := v.ch.QueryWithParams(ctx,
			"SELECT count() FROM logs "+final+" WHERE attributes['loadgen_run'] = {run:String} FORMAT TabSeparated",
			map[string]string{"run": runID}, map[string]string{"readonly": "1"})
		if err != nil {
			return 0, err
		}
		return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	}
	deadline := time.Now().Add(v.timeout)
	var stored, prev int64 = 0, -1
	for {
		n, err := count("FINAL")
		if err != nil {
			return verifyResult{}, err
		}
		stored = n
		if stored >= accepted || (stored == prev && time.Now().After(deadline)) {
			break
		}
		prev = stored
		if time.Now().After(deadline.Add(v.timeout)) {
			break
		}
		select {
		case <-ctx.Done():
			return verifyResult{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	raw, err := count("")
	if err != nil {
		return verifyResult{}, err
	}
	r := verifyResult{RunID: runID, Accepted: accepted, Stored: stored, StoredRaw: raw}
	if accepted > stored {
		r.Lost = accepted - stored
	} else {
		r.Extra = stored - accepted
	}
	r.Summary = fmt.Sprintf("accepted %d, stored %d, lost %d", accepted, stored, r.Lost)
	if r.Extra > 0 {
		r.Summary += fmt.Sprintf(" (+%d stored from batches answered 503)", r.Extra)
	}
	if raw > stored {
		r.Summary += fmt.Sprintf("; %d duplicate rows before FINAL", raw-stored)
	}
	return r, nil
}
