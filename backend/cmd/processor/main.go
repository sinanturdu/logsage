// Command processor moves log events from the Redis Stream into ClickHouse.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/config"
	"github.com/sinanturdu/logsage/backend/internal/health"
	"github.com/sinanturdu/logsage/backend/internal/logging"
	"github.com/sinanturdu/logsage/backend/internal/processor"
	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
	"github.com/sinanturdu/logsage/backend/migrations"
)

const (
	logsTable           = "logs"
	httpShutdownTimeout = 5 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "processor:", err)
		os.Exit(1)
	}
}

// run starts the processor and its HTTP server and blocks until ctx is
// canceled (or the HTTP server fails). On cancel the processor flushes and
// acks its current batch before run returns.
func run(ctx context.Context, getenv func(string) string) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.ValidateProcessor(); err != nil {
		return err
	}
	log := logging.New(os.Stdout, cfg.LogLevel, cfg.LogFormat)

	consumer := cfg.Processor.Consumer
	if consumer == "" {
		host, err := os.Hostname()
		if err != nil || host == "" {
			host = "processor"
		}
		consumer = fmt.Sprintf("%s-%d", host, os.Getpid())
	}

	ch, err := clickhouse.New(clickhouse.Options{
		URL:      cfg.ClickHouse.URL,
		Database: cfg.ClickHouse.Database,
		User:     cfg.ClickHouse.User,
		Password: cfg.ClickHouse.Password,
		Timeout:  cfg.ClickHouse.Timeout,
	})
	if err != nil {
		return err
	}

	stream, err := processor.NewRedisStream(processor.RedisStreamOptions{
		Addr:       cfg.Redis.Addr,
		Stream:     cfg.Redis.Stream,
		DeadStream: cfg.Processor.DeadStream,
		DeadMaxLen: cfg.Redis.MaxLen,
		Consumer:   consumer,
		Timeout:    cfg.Redis.Timeout,
	})
	if err != nil {
		return err
	}
	defer stream.Close()

	proc, err := processor.New(stream, ch, processor.Options{
		Table:           logsTable,
		BatchSize:       cfg.Processor.BatchSize,
		ReadCount:       cfg.Processor.ReadCount,
		ReadBlock:       cfg.Processor.ReadBlock,
		FlushInterval:   cfg.Processor.FlushInterval,
		ClaimIdle:       cfg.Processor.ClaimIdle,
		ClaimInterval:   cfg.Processor.ClaimInterval,
		RetryInitial:    cfg.Processor.RetryInitial,
		RetryMax:        cfg.Processor.RetryMax,
		ShutdownTimeout: cfg.Processor.ShutdownTimeout,
	}, log)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The HTTP server starts first so /healthz and /readyz answer while
	// ClickHouse is still coming up.
	mux := http.NewServeMux()
	health.Register(mux)
	health.RegisterReady(mux, log, stream, ch)
	proc.RegisterStats(mux)
	srv := &http.Server{
		Addr:              cfg.Processor.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}
	srvErr := make(chan error, 1)
	go func() {
		log.Info("processor http server starting", "addr", cfg.Processor.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srvErr <- err
			cancel()
		}
		close(srvErr)
	}()
	defer func() {
		sctx, scancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
		defer scancel()
		_ = srv.Shutdown(sctx)
	}()

	if err := migrate(ctx, ch, cfg, log); err != nil {
		if ctx.Err() != nil {
			return serverError(srvErr)
		}
		return err
	}

	log.Info("processor starting",
		"stream", cfg.Redis.Stream, "group", processor.GroupName, "consumer", consumer,
		"dead_stream", cfg.Processor.DeadStream, "clickhouse_db", cfg.ClickHouse.Database,
		"options", fmt.Sprint(processorOptions(cfg)))

	go proc.LogStats(ctx, cfg.Processor.StatsInterval)
	if err := proc.Run(ctx); err != nil {
		return err
	}
	log.Info("processor stopped")
	return serverError(srvErr)
}

// migrate applies the embedded ClickHouse migrations, retrying while
// ClickHouse is unreachable so the processor can start before it.
func migrate(ctx context.Context, ch *clickhouse.Client, cfg config.Config, log *slog.Logger) error {
	data := clickhouse.MigrationData{Database: cfg.ClickHouse.Database, TTLDays: cfg.ClickHouse.TTLDays}
	wait := cfg.Processor.RetryInitial
	for attempt := 1; ; attempt++ {
		err := ch.Migrate(ctx, migrations.ClickHouse(), data)
		if err == nil {
			log.Info("clickhouse migrations applied", "database", cfg.ClickHouse.Database)
			return nil
		}
		var chErr *clickhouse.Error
		if errors.As(err, &chErr) && chErr.Status >= 400 && chErr.Status < 500 {
			return fmt.Errorf("apply migrations: %w", err) // rejected SQL or auth: retrying will not help
		}
		log.Warn("clickhouse migrations failed; retrying", "attempt", attempt, "retry_in", wait.String(), "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		if wait *= 2; wait > cfg.Processor.RetryMax {
			wait = cfg.Processor.RetryMax
		}
	}
}

func processorOptions(cfg config.Config) processor.Options {
	return processor.Options{
		BatchSize: cfg.Processor.BatchSize, ReadCount: cfg.Processor.ReadCount, ReadBlock: cfg.Processor.ReadBlock,
		FlushInterval: cfg.Processor.FlushInterval, ClaimIdle: cfg.Processor.ClaimIdle, ClaimInterval: cfg.Processor.ClaimInterval,
		RetryInitial: cfg.Processor.RetryInitial, RetryMax: cfg.Processor.RetryMax,
	}
}

// serverError returns the HTTP server's startup error, if any.
func serverError(ch <-chan error) error {
	select {
	case err, ok := <-ch:
		if ok && err != nil {
			return fmt.Errorf("http server: %w", err)
		}
	default:
	}
	return nil
}
