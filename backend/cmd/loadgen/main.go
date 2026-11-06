// Command loadgen generates realistic load for LogSage: ingest throughput,
// query latency benchmarks, and a continuous demo stream.
//
//	loadgen ingest [flags]   write load against POST /v1/ingest (key: LOADGEN_INGEST_KEY)
//	loadgen query  [flags]   query benchmark scenarios against GET /v1/logs (key: LOADGEN_QUERY_KEY)
//	loadgen demo   [flags]   steady realistic stream, optional simulated incident
//
// API keys are read from the environment only, never from flags. Non-local
// targets are refused unless --allow-remote is passed.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(2)
	}
}

const usage = `usage: loadgen <ingest|query|demo> [flags]  (loadgen <cmd> -h for flags)`

func run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "ingest":
		return cmdIngest(ctx, args[1:], getenv, out)
	case "query":
		return cmdQuery(ctx, args[1:], getenv, out)
	case "demo":
		return cmdDemo(ctx, args[1:], getenv, out)
	default:
		return fmt.Errorf("unknown command %q; %s", args[0], usage)
	}
}

func requireKey(getenv func(string) string, name string) (string, error) {
	k := strings.TrimSpace(getenv(name))
	if k == "" {
		return "", fmt.Errorf("%s is not set (keys are read from the environment, never from flags)", name)
	}
	return k, nil
}

func cmdIngest(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	var o ingestOptions
	fs.StringVar(&o.URL, "url", "http://127.0.0.1:8080/v1/ingest", "ingest endpoint")
	fs.DurationVar(&o.Duration, "duration", 30*time.Second, "how long to send (0 with --max-events: until that many events)")
	fs.IntVar(&o.Concurrency, "concurrency", 8, "parallel senders")
	fs.IntVar(&o.BatchSize, "batch-size", 100, "events per request")
	fs.Float64Var(&o.Rate, "rate", 0, "cap on events per second across all senders (0 = unlimited)")
	fs.Uint64Var(&o.Seed, "seed", 1, "random seed (sender i uses seed+i)")
	fs.Int64Var(&o.MaxEvents, "max-events", 0, "stop after this many events (0 = no cap)")
	fs.DurationVar(&o.Spread, "time-spread", 0, "spread event timestamps uniformly over the last DURATION (0 = now)")
	fs.StringVar(&o.WaitDrain, "wait-drain", "", "processor /stats URL; after sending, wait until processed_total covers what was accepted")
	fs.DurationVar(&o.DrainTimeout, "drain-timeout", 10*time.Minute, "give up waiting for the processor after this long")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	allowRemote := fs.Bool("allow-remote", false, "allow non-localhost targets")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for _, u := range []string{o.URL, o.WaitDrain} {
		if u == "" {
			continue
		}
		if err := checkTarget(u, *allowRemote); err != nil {
			return err
		}
	}
	key, err := requireKey(getenv, "LOADGEN_INGEST_KEY")
	if err != nil {
		return err
	}
	o.Key = key
	res, err := runIngest(ctx, o)
	if err != nil {
		return err
	}
	return emit(out, *asJSON, res, func(w io.Writer) { res.print(w) })
}

func cmdQuery(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	var o queryOptions
	end := fs.String("end", "", "end of the time window, RFC3339 (default: now)")
	fs.StringVar(&o.BaseURL, "url", "http://127.0.0.1:8082", "query API base URL")
	fs.IntVar(&o.Reps, "reps", 20, "measured repetitions per scenario")
	fs.DurationVar(&o.Window, "window", 24*time.Hour, "time range for scenarios a-e")
	fs.DurationVar(&o.PaginateWindow, "paginate-window", 5*time.Minute, "time range paged through in scenario f")
	fs.IntVar(&o.PageLimit, "page-limit", 1000, "limit per page in scenario f")
	fs.StringVar(&o.Service, "service", "checkout", "service for scenario b")
	fs.StringVar(&o.Level, "level", "error", "level for scenario c")
	fs.StringVar(&o.Text, "q", "timeout", "text for scenario d")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	allowRemote := fs.Bool("allow-remote", false, "allow non-localhost targets")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := checkTarget(o.BaseURL, *allowRemote); err != nil {
		return err
	}
	o.End = time.Now().UTC()
	if *end != "" {
		t, err := time.Parse(time.RFC3339, *end)
		if err != nil {
			return fmt.Errorf("--end: %w", err)
		}
		o.End = t
	}
	key, err := requireKey(getenv, "LOADGEN_QUERY_KEY")
	if err != nil {
		return err
	}
	o.Key = key
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	rs, err := runQuery(ctx, o)
	if err != nil {
		return err
	}
	return emit(out, *asJSON, rs, func(w io.Writer) { printScenarios(w, o, rs) })
}

func cmdDemo(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var o demoOptions
	fs.StringVar(&o.URL, "url", "http://127.0.0.1:8080/v1/ingest", "ingest endpoint")
	fs.IntVar(&o.Rate, "rate", 20, "normal events per second")
	fs.Uint64Var(&o.Seed, "seed", 1, "random seed")
	fs.DurationVar(&o.Duration, "duration", 0, "stop after this long (0 = until Ctrl-C)")
	fs.BoolVar(&o.Incident, "incident", false, "simulate an incident: a burst of errors in one service")
	fs.StringVar(&o.IncidentService, "incident-service", "payments", "service that fails during the incident")
	fs.DurationVar(&o.IncidentAfter, "incident-after", time.Minute, "incident starts this long after the demo starts")
	fs.DurationVar(&o.IncidentDuration, "incident-duration", 3*time.Minute, "how long the incident lasts")
	fs.IntVar(&o.IncidentRate, "incident-rate", 50, "extra error events per second during the incident")
	allowRemote := fs.Bool("allow-remote", false, "allow non-localhost targets")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := checkTarget(o.URL, *allowRemote); err != nil {
		return err
	}
	key, err := requireKey(getenv, "LOADGEN_INGEST_KEY")
	if err != nil {
		return err
	}
	o.Key = key
	return runDemo(ctx, o, out)
}

func emit(w io.Writer, asJSON bool, v any, human func(io.Writer)) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	human(w)
	return nil
}
