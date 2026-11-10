// Command alerts evaluates alert rules and delivers their notifications
// (ADR 0024). One instance evaluates at a time (a PostgreSQL advisory
// lock); others wait as hot standbys. Deliveries are claimed with SKIP
// LOCKED, so several instances may deliver.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/alert"
	"github.com/sinanturdu/logsage/backend/internal/alert/outbound"
	"github.com/sinanturdu/logsage/backend/internal/alert/secret"
	"github.com/sinanturdu/logsage/backend/internal/config"
	"github.com/sinanturdu/logsage/backend/internal/incident"
	"github.com/sinanturdu/logsage/backend/internal/health"
	"github.com/sinanturdu/logsage/backend/internal/logging"
	"github.com/sinanturdu/logsage/backend/internal/query"
	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv, nil); err != nil {
		fmt.Fprintln(os.Stderr, "alerts:", err)
		os.Exit(1)
	}
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

// Deps lets tests replace the outbound client options and the tick sources.
type Deps struct {
	Outbound  *outbound.Options
	EvalTick  <-chan time.Time
	DelivTick <-chan time.Time
}

func run(ctx context.Context, getenv func(string) string, deps *Deps) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	ac, err := config.LoadAlerts(getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	// Without a usable key nothing can be delivered: refuse to start.
	if err := ac.RequireKey(); err != nil {
		return err
	}
	if err := cfg.ValidateDatabase(); err != nil {
		return err
	}
	log := logging.New(os.Stdout, cfg.LogLevel, cfg.LogFormat)

	pool, err := postgres.Open(ctx, cfg.Postgres)
	if err != nil {
		return err
	}
	defer pool.Close()
	store := postgres.NewStore(pool, cfg.Postgres.QueryTimeout)
	store.SetIncidentConfig(incident.Config{MergeWindow: ac.IncidentMergeWindow, ReopenWindow: ac.IncidentReopenWindow,
		MaxOpen: incident.MaxOpenIncidents, Retention: ac.IncidentRetention})
	ch, err := clickhouse.New(clickhouse.Options{
		URL: cfg.ClickHouse.URL, Database: cfg.ClickHouse.Database, User: cfg.ClickHouse.User,
		Password: cfg.ClickHouse.Password, Timeout: max(cfg.ClickHouse.Timeout, cfg.Query.Timeout+5*time.Second),
	})
	if err != nil {
		return err
	}
	logs, err := query.NewService(ch, query.Options{
		MaxWindow: cfg.Query.MaxWindow, DefaultLimit: 1, MaxLimit: cfg.Query.MaxLimit, MaxTextLen: cfg.Query.MaxTextLen, Timeout: cfg.Query.Timeout,
	})
	if err != nil {
		return err
	}
	sealer, err := secret.NewSealer(ac.SecretKey)
	if err != nil {
		return err
	}
	oopts := outbound.Options{AllowInsecureLocal: ac.AllowInsecureLocal, AllowedPorts: ac.AllowedPorts}
	if deps != nil && deps.Outbound != nil {
		oopts = *deps.Outbound
	}
	client := outbound.New(oopts)

	locker := func(ctx context.Context) (alert.Lock, error) {
		l, err := store.TryEvaluatorLock(ctx)
		if err != nil || l == nil {
			return nil, err // no typed nil: nil means another instance holds it
		}
		return l, nil
	}
	eval := alert.NewEvaluator(store, logs, realClock{}, locker, alert.EvalOptions{
		QueriesPerCycle: ac.QueriesPerCycle, Concurrency: ac.Concurrency, Lateness: ac.Lateness, Retention: ac.Retention, DashboardURL: ac.DashboardURL,
	}, log)
	host, _ := os.Hostname()
	worker := host + "-" + strconv.Itoa(os.Getpid())
	deliv := alert.NewDeliverer(store, client, sealer, realClock{}, worker, alert.DeliverOptions{
		Concurrency: ac.DeliveryConcurrency, MaxAttempts: ac.MaxAttempts, PerChannelPerMinute: ac.PerChannelPerMinute,
	}, rand.Float64, log)

	mux := http.NewServeMux()
	health.Register(mux)
	health.RegisterReady(mux, log, store, ch)
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(eval.Stats())
	})
	ln, err := net.Listen("tcp", ac.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	hs := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelError)}
	if ac.AllowInsecureLocal {
		log.Warn("ALERTS_ALLOW_INSECURE_LOCAL=true: plain http webhooks to localhost are allowed; use only for development")
	}
	log.Info("alerts starting", "addr", ln.Addr().String(), "worker", worker, "tick", ac.Tick.String(), "lateness", ac.Lateness.String(),
		"queries_per_cycle", ac.QueriesPerCycle, "concurrency", ac.Concurrency, "dashboard_links", ac.DashboardURL != "")

	evalTick, delivTick := tickers(ctx, ac.Tick, deps)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); eval.Run(ctx, evalTick) }()
	go func() { defer wg.Done(); deliv.Run(ctx, delivTick) }()
	errCh := make(chan error, 1)
	go func() {
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_ = hs.Shutdown(sctx)
	wg.Wait() // the evaluator releases its lock, the worker its claims
	log.Info("alerts stopped")
	return nil
}

func tickers(ctx context.Context, every time.Duration, deps *Deps) (<-chan time.Time, <-chan time.Time) {
	if deps != nil && deps.EvalTick != nil && deps.DelivTick != nil {
		return deps.EvalTick, deps.DelivTick
	}
	e, d := time.NewTicker(every), time.NewTicker(min(every, 2*time.Second))
	go func() {
		<-ctx.Done()
		e.Stop()
		d.Stop()
	}()
	return e.C, d.C
}
