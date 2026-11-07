package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTrackerMatching(t *testing.T) {
	tr := newTracker("ab12")
	t0 := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	for seq := 0; seq < 4; seq++ {
		tr.accepted(seq, t0.Add(time.Duration(seq)*time.Second))
	}
	tr.received(tr.message(0), t0.Add(300*time.Millisecond))
	tr.received(tr.message(2), t0.Add(2*time.Second+100*time.Millisecond))
	tr.received(tr.message(2), t0.Add(3*time.Second)) // duplicate
	tr.received(tr.message(1), t0.Add(time.Second+700*time.Millisecond))
	tr.received("loadgen-tail other 000003", t0)    // another run
	tr.received("loadgen-tail ab12 notanumber", t0) // malformed
	tr.received("unrelated message", t0)
	r := tr.result(5)
	if r.Sent != 4 || r.Received != 3 || r.Duplicates != 1 || r.Unsent != 1 || fmt.Sprint(r.Missing) != "[3]" {
		t.Fatalf("result = %+v", r)
	}
	want := []time.Duration{100 * time.Millisecond, 300 * time.Millisecond, 700 * time.Millisecond}
	if fmt.Sprint(r.Latencies) != fmt.Sprint(want) {
		t.Fatalf("latencies = %v, want %v (sorted)", r.Latencies, want)
	}
	if percentile(r.Latencies, 50) != 300*time.Millisecond || percentile(r.Latencies, 99) != 700*time.Millisecond {
		t.Errorf("percentiles p50 %v p99 %v", percentile(r.Latencies, 50), percentile(r.Latencies, 99))
	}
}

func TestParseCookieFile(t *testing.T) {
	jar := "# Netscape HTTP Cookie File\n#HttpOnly_127.0.0.1\tFALSE\t/\tFALSE\t0\tlogsage_session\tTOKEN123\r\n"
	if got, err := parseCookieFile(jar, "logsage_session"); err != nil || got != "TOKEN123" {
		t.Errorf("jar: %q %v", got, err)
	}
	if got, err := parseCookieFile("  RAWTOKEN \n", "logsage_session"); err != nil || got != "RAWTOKEN" {
		t.Errorf("raw token: %q %v", got, err)
	}
	if _, err := parseCookieFile(jar, "__Host-logsage_session"); err == nil {
		t.Error("missing cookie accepted")
	}
}

// TestRunTail runs against a fake API and ingest: the stream repeats every
// accepted message except one, and one twice.
func TestRunTail(t *testing.T) {
	var mu sync.Mutex
	var queue []string
	ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Events []struct{ Message string } `json:"events"`
		}
		if r.Header.Get("X-API-Key") != "k" || json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(401)
			return
		}
		mu.Lock()
		queue = append(queue, body.Events[0].Message)
		mu.Unlock()
		w.WriteHeader(202)
	}))
	defer ingest.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("logsage_session"); err != nil || c.Value != "tok" || !strings.HasPrefix(r.URL.Query().Get("q"), "loadgen-tail ") {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, "retry: 5000\n\nevent: status\ndata: {\"state\":\"live\"}\n\n")
		fl.Flush()
		for n := 0; ; n++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
			mu.Lock()
			q := queue
			queue = nil
			mu.Unlock()
			for _, m := range q {
				if strings.HasSuffix(m, " 000003") {
					continue // lost
				}
				b, _ := json.Marshal(map[string]string{"message": m})
				fmt.Fprintf(w, "id: x\nevent: log\ndata: %s\n\n", b)
				if strings.HasSuffix(m, " 000001") {
					fmt.Fprintf(w, "event: log\ndata: %s\n\n", b)
				}
			}
			fl.Flush()
		}
	}))
	defer api.Close()

	o := tailOptions{API: api.URL, Project: "shop", IngestURL: ingest.URL, CookieName: "logsage_session",
		Events: 6, Rate: 200, Wait: 500 * time.Millisecond, Session: "tok", Key: "k"}
	r, err := runTail(context.Background(), o, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if r.Sent != 6 || r.Received != 5 || r.Duplicates != 1 || fmt.Sprint(r.Missing) != "[3]" || len(r.Latencies) != 5 {
		t.Fatalf("result = %+v", r)
	}
	var out strings.Builder
	printTail(&out, o, r)
	if !strings.Contains(out.String(), "1 missing, 1 duplicates") || !strings.Contains(out.String(), "p95") {
		t.Errorf("report:\n%s", out.String())
	}
}
