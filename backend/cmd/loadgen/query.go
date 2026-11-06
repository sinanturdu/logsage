package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type queryOptions struct {
	BaseURL        string // e.g. http://127.0.0.1:8082
	Key            string
	Reps           int
	Window         time.Duration // time range for scenarios a-e
	End            time.Time
	PageLimit      int           // limit per page in scenario f
	PaginateWindow time.Duration // window for scenario f
	Service        string
	Level          string
	Text           string
}

type scenarioResult struct {
	Name         string         `json:"name"`
	Query        string         `json:"query"`
	Reps         int            `json:"reps"`
	Latency      LatencySummary `json:"latency"`
	ServerTookMs LatencySummary `json:"server_took"`
	Rows         int            `json:"rows_returned"`
	Pages        int            `json:"pages,omitempty"`
	Errors       int            `json:"errors"`
	FirstError   string         `json:"first_error,omitempty"`
}

type logsResponse struct {
	Events []struct {
		TraceID string `json:"trace_id"`
	} `json:"events"`
	NextCursor *string `json:"next_cursor"`
	TookMs     int64   `json:"took_ms"`
	Error      string  `json:"error"`
}

func runQuery(ctx context.Context, o queryOptions) ([]scenarioResult, error) {
	if o.Reps < 1 {
		return nil, fmt.Errorf("--reps must be at least 1")
	}
	client := newHTTPClient(1)
	from := o.End.Add(-o.Window).UTC().Format(time.RFC3339)
	to := o.End.UTC().Format(time.RFC3339)
	base := url.Values{"from": {from}, "to": {to}}

	with := func(k, v string) url.Values {
		q := url.Values{}
		for kk, vv := range base {
			q[kk] = vv
		}
		q.Set(k, v)
		return q
	}

	// Find a trace id that exists in the window for scenario (e).
	traceID := ""
	if r, _, err := getLogs(ctx, client, o, with("limit", "200")); err == nil {
		for _, e := range r.Events {
			if e.TraceID != "" {
				traceID = e.TraceID
				break
			}
		}
	}

	scenarios := []struct {
		name string
		q    url.Values
	}{
		{"a: time range only", base},
		{"b: service filter", with("service", o.Service)},
		{"c: level filter", with("level", o.Level)},
		{"d: text search q", with("q", o.Text)},
	}
	if traceID != "" {
		scenarios = append(scenarios, struct {
			name string
			q    url.Values
		}{"e: trace_id", with("trace_id", traceID)})
	}

	var out []scenarioResult
	for _, sc := range scenarios {
		res := scenarioResult{Name: sc.name, Query: sc.q.Encode(), Reps: o.Reps}
		_, _, _ = getLogs(ctx, client, o, sc.q) // warm-up, not measured
		var lat, took []time.Duration
		for i := 0; i < o.Reps; i++ {
			r, d, err := getLogs(ctx, client, o, sc.q)
			if err != nil {
				res.Errors++
				if res.FirstError == "" {
					res.FirstError = err.Error()
				}
				continue
			}
			lat = append(lat, d)
			took = append(took, time.Duration(r.TookMs)*time.Millisecond)
			res.Rows = len(r.Events)
		}
		res.Latency, res.ServerTookMs = summarize(lat), summarize(took)
		out = append(out, res)
	}
	if traceID == "" {
		out = append(out, scenarioResult{Name: "e: trace_id", FirstError: "no event with a trace_id in the window; skipped"})
	}

	// (f) full pagination of a smaller window.
	pq := url.Values{
		"from":  {o.End.Add(-o.PaginateWindow).UTC().Format(time.RFC3339)},
		"to":    {to},
		"limit": {fmt.Sprint(o.PageLimit)},
	}
	pres := scenarioResult{Name: fmt.Sprintf("f: paginate %s window, limit %d", o.PaginateWindow, o.PageLimit), Query: pq.Encode(), Reps: o.Reps}
	var plat, ptook []time.Duration
	for i := 0; i < o.Reps; i++ {
		q := url.Values{}
		for k, v := range pq {
			q[k] = v
		}
		rows, pages := 0, 0
		start := time.Now()
		var serverTotal time.Duration
		var ferr error
		for pages < 100000 {
			r, _, err := getLogs(ctx, client, o, q)
			if err != nil {
				ferr = err
				break
			}
			pages++
			rows += len(r.Events)
			serverTotal += time.Duration(r.TookMs) * time.Millisecond
			if r.NextCursor == nil {
				break
			}
			q.Set("cursor", *r.NextCursor)
		}
		if ferr != nil {
			pres.Errors++
			if pres.FirstError == "" {
				pres.FirstError = ferr.Error()
			}
			continue
		}
		plat = append(plat, time.Since(start))
		ptook = append(ptook, serverTotal)
		pres.Rows, pres.Pages = rows, pages
	}
	pres.Latency, pres.ServerTookMs = summarize(plat), summarize(ptook)
	out = append(out, pres)
	return out, nil
}

func getLogs(ctx context.Context, c *http.Client, o queryOptions, q url.Values) (logsResponse, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.BaseURL+"/v1/logs?"+q.Encode(), nil)
	if err != nil {
		return logsResponse{}, 0, err
	}
	req.Header.Set("X-API-Key", o.Key)
	start := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		return logsResponse{}, 0, err
	}
	defer resp.Body.Close()
	var r logsResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return r, 0, fmt.Errorf("decode response (HTTP %d): %w", resp.StatusCode, err)
	}
	d := time.Since(start)
	if resp.StatusCode != http.StatusOK {
		return r, d, fmt.Errorf("HTTP %d: %s", resp.StatusCode, r.Error)
	}
	return r, d, nil
}

func printScenarios(w io.Writer, o queryOptions, rs []scenarioResult) {
	fmt.Fprintf(w, "query -> %s/v1/logs, window %s ending %s, %d reps per scenario (after 1 warm-up)\n", o.BaseURL, o.Window, o.End.UTC().Format(time.RFC3339), o.Reps)
	for _, r := range rs {
		fmt.Fprintf(w, "  %-36s rows %-5d", r.Name, r.Rows)
		if r.Pages > 0 {
			fmt.Fprintf(w, " pages %-5d", r.Pages)
		}
		fmt.Fprintf(w, " client %s | server took p50 %.0f ms p99 %.0f ms", r.Latency, r.ServerTookMs.P50, r.ServerTookMs.P99)
		if r.Errors > 0 || r.FirstError != "" {
			fmt.Fprintf(w, " | errors %d: %s", r.Errors, r.FirstError)
		}
		fmt.Fprintln(w)
	}
}
