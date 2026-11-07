package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// tailOptions configure "loadgen tail": send events through ingest at a low
// steady rate and measure when each one arrives on the live tail stream.
type tailOptions struct {
	API, Project, IngestURL, CookieName string
	Events                              int
	Rate                                float64
	Wait                                time.Duration
	Session, Key                        string
}

func cmdTail(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("tail", flag.ContinueOnError)
	var o tailOptions
	fs.StringVar(&o.API, "api", "http://127.0.0.1:8083", "management API base URL")
	fs.StringVar(&o.Project, "project", "", "project slug (required)")
	fs.StringVar(&o.IngestURL, "ingest-url", "http://127.0.0.1:8080/v1/ingest", "ingest endpoint")
	fs.IntVar(&o.Events, "events", 100, "events to send")
	fs.Float64Var(&o.Rate, "rate", 5, "events per second")
	fs.DurationVar(&o.Wait, "wait", 30*time.Second, "how long to wait for the last events after sending")
	fs.StringVar(&o.CookieName, "cookie-name", "logsage_session", "session cookie name (__Host-logsage_session with COOKIE_SECURE=true)")
	cookieFile := fs.String("cookie-file", "", "curl cookie jar or a file holding the session token (else LOADGEN_SESSION)")
	allowRemote := fs.Bool("allow-remote", false, "allow non-localhost targets")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if o.Project == "" || o.Events <= 0 || o.Rate <= 0 {
		return errors.New("tail: --project is required; --events and --rate must be positive")
	}
	for _, u := range []string{o.API, o.IngestURL} {
		if err := checkTarget(u, *allowRemote); err != nil {
			return err
		}
	}
	var err error
	if o.Session, err = sessionToken(*cookieFile, o.CookieName, getenv); err != nil {
		return err
	}
	if o.Key, err = requireKey(getenv, "LOADGEN_INGEST_KEY"); err != nil {
		return err
	}
	res, err := runTail(ctx, o, http.DefaultClient)
	if err != nil {
		return err
	}
	printTail(out, o, res)
	return nil
}

// sessionToken reads the session token from a curl cookie jar, a file that
// holds only the token, or LOADGEN_SESSION. It is never a flag value.
func sessionToken(file, name string, getenv func(string) string) (string, error) {
	if file == "" {
		if v := strings.TrimSpace(getenv("LOADGEN_SESSION")); v != "" {
			return v, nil
		}
		return "", errors.New("tail: pass --cookie-file or set LOADGEN_SESSION")
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("tail: read cookie file: %w", err)
	}
	return parseCookieFile(string(b), name)
}

// parseCookieFile finds cookie name in a Netscape/curl cookie jar, or takes
// the whole file as the token when it is a single line without tabs.
func parseCookieFile(s, name string) (string, error) {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(strings.TrimPrefix(line, "#HttpOnly_"), "\r")
		f := strings.Split(line, "\t")
		if len(f) == 7 && f[5] == name && f[6] != "" {
			return f[6], nil
		}
	}
	if t := strings.TrimSpace(s); t != "" && !strings.ContainsAny(t, "\t\n# ") {
		return t, nil
	}
	return "", fmt.Errorf("tail: no %s cookie in the cookie file", name)
}

// tracker matches sent events with received ones by sequence number.
type tracker struct {
	mu   sync.Mutex
	run  string
	sent map[int]time.Time
	recv map[int]time.Time
	dups int
}

func newTracker(run string) *tracker {
	return &tracker{run: run, sent: map[int]time.Time{}, recv: map[int]time.Time{}}
}

func (t *tracker) message(seq int) string { return fmt.Sprintf("loadgen-tail %s %06d", t.run, seq) }

func (t *tracker) accepted(seq int, at time.Time) { t.mu.Lock(); t.sent[seq] = at; t.mu.Unlock() }

// received records a streamed message of this run; others are ignored.
func (t *tracker) received(msg string, at time.Time) {
	prefix := "loadgen-tail " + t.run + " "
	if !strings.HasPrefix(msg, prefix) {
		return
	}
	seq, err := strconv.Atoi(strings.TrimPrefix(msg, prefix))
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, dup := t.recv[seq]; dup {
		t.dups++
		return
	}
	t.recv[seq] = at
}

func (t *tracker) complete(n int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.recv) >= n
}

type tailResult struct {
	Sent, Received, Duplicates int
	Missing                    []int
	Latencies                  []time.Duration // sorted, accepted -> received
	Unsent                     int
}

func (t *tracker) result(total int) tailResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := tailResult{Sent: len(t.sent), Received: len(t.recv), Duplicates: t.dups, Unsent: total - len(t.sent)}
	for seq, at := range t.sent {
		got, ok := t.recv[seq]
		if !ok {
			r.Missing = append(r.Missing, seq)
			continue
		}
		r.Latencies = append(r.Latencies, got.Sub(at))
	}
	sort.Ints(r.Missing)
	sort.Slice(r.Latencies, func(i, j int) bool { return r.Latencies[i] < r.Latencies[j] })
	return r
}

func runTail(ctx context.Context, o tailOptions, client *http.Client) (tailResult, error) {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	tr := newTracker(hex.EncodeToString(b))

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	u := strings.TrimRight(o.API, "/") + "/api/projects/" + url.PathEscape(o.Project) + "/tail?q=" + url.QueryEscape("loadgen-tail "+tr.run)
	req, _ := http.NewRequestWithContext(sctx, http.MethodGet, u, nil)
	req.AddCookie(&http.Cookie{Name: o.CookieName, Value: o.Session})
	resp, err := client.Do(req)
	if err != nil {
		return tailResult{}, fmt.Errorf("tail: open stream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return tailResult{}, fmt.Errorf("tail: stream answered %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	live := make(chan struct{})
	go readStream(resp.Body, tr, live)
	select {
	case <-live:
	case <-time.After(15 * time.Second):
		return tailResult{}, errors.New("tail: stream did not report live within 15s")
	}

	interval := time.Duration(float64(time.Second) / o.Rate)
	next := time.Now()
	for seq := 0; seq < o.Events; seq++ {
		if d := time.Until(next); d > 0 {
			select {
			case <-ctx.Done():
				return tr.result(o.Events), ctx.Err()
			case <-time.After(d):
			}
		}
		next = next.Add(interval)
		if err := sendOne(ctx, client, o, tr.message(seq)); err != nil {
			fmt.Fprintf(os.Stderr, "event %d not accepted: %v\n", seq, err)
			continue
		}
		tr.accepted(seq, time.Now())
	}
	deadline := time.Now().Add(o.Wait)
	for !tr.complete(o.Events) && time.Now().Before(deadline) && ctx.Err() == nil {
		time.Sleep(50 * time.Millisecond)
	}
	return tr.result(o.Events), nil
}

// readStream feeds log events to the tracker; live is closed on the first
// status live.
func readStream(r io.Reader, tr *tracker, live chan struct{}) {
	var once sync.Once
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var event, data string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			switch event {
			case "status":
				if strings.Contains(data, `"state":"live"`) {
					once.Do(func() { close(live) })
				}
			case "log":
				var e struct {
					Message string `json:"message"`
				}
				if json.Unmarshal([]byte(data), &e) == nil {
					tr.received(e.Message, time.Now())
				}
			}
			event, data = "", ""
		case strings.HasPrefix(line, "event: "):
			event = line[7:]
		case strings.HasPrefix(line, "data: "):
			data = line[6:]
		}
	}
}

func sendOne(ctx context.Context, client *http.Client, o tailOptions, msg string) error {
	body, _ := json.Marshal(map[string]any{"events": []map[string]any{{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "level": "info", "service": "loadgen", "message": msg,
	}}})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, o.IngestURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", o.Key)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func printTail(w io.Writer, o tailOptions, r tailResult) {
	fmt.Fprintf(w, "tail: %d events at %.1f/s to %q: %d accepted, %d received, %d missing, %d duplicates\n",
		o.Events, o.Rate, o.Project, r.Sent, r.Received, len(r.Missing), r.Duplicates)
	if len(r.Latencies) > 0 {
		fmt.Fprintf(w, "end-to-end latency (ingest accepted -> received on the stream): p50 %s  p95 %s  p99 %s  max %s\n",
			percentile(r.Latencies, 50).Round(time.Millisecond), percentile(r.Latencies, 95).Round(time.Millisecond),
			percentile(r.Latencies, 99).Round(time.Millisecond), r.Latencies[len(r.Latencies)-1].Round(time.Millisecond))
	}
	if len(r.Missing) > 0 {
		show := r.Missing
		if len(show) > 10 {
			show = show[:10]
		}
		fmt.Fprintf(w, "missing sequence numbers (first %d): %v\n", len(show), show)
	}
}
