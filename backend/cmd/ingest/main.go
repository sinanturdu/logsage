// Command ingest runs the LogSage ingestion API.
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
	"github.com/sinanturdu/logsage/backend/internal/ingest"
	"github.com/sinanturdu/logsage/backend/internal/logging"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
)

const (
	shutdownTimeout = 10 * time.Second
	// memoryPublisherCap bounds the in-memory publisher used until the Redis
	// Streams publisher exists.
	memoryPublisherCap = 10_000
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "ingest:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	log := logging.New(os.Stdout, cfg.LogLevel, cfg.LogFormat)

	if err := cfg.ValidateIngest(); err != nil {
		return err
	}
	authn, keyStore, closeAuth, err := postgres.NewAuthenticator(ctx, cfg, log)
	if err != nil {
		return fmt.Errorf("authenticator: %w", err)
	}
	defer closeAuth()

	pub, readiness, closePub, err := newPublisher(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer closePub()

	ingestHandler, err := ingest.NewHandler(
		pub,
		authn,
		ingest.Limits{
			MaxBodyBytes:      cfg.Ingest.MaxBodyBytes,
			MaxEvents:         cfg.Ingest.MaxEvents,
			MaxMessageBytes:   cfg.Ingest.MaxMessageBytes,
			MaxAttributes:     cfg.Ingest.MaxAttributes,
			MaxAttrKeyChars:   cfg.Ingest.MaxAttrKeyChars,
			MaxAttrValueChars: cfg.Ingest.MaxAttrValueChars,
			MaxTraceIDChars:   cfg.Ingest.MaxTraceIDChars,
		},
		log,
	)
	if err != nil {
		return fmt.Errorf("ingest handler: %w", err)
	}

	mux := http.NewServeMux()
	health.Register(mux)
	if keyStore != nil {
		readiness = append(readiness, keyStore)
	}
	health.RegisterReady(mux, log, readiness...)
	ingestHandler.Register(mux)
	ingestHandler.RegisterStats(mux)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http server starting", "addr", cfg.HTTPAddr, "publisher", cfg.Publisher)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		// Connections still open after the budget (for example an idle
		// keep-alive dial that never sent a request) are force-closed.
		log.Warn("graceful shutdown timed out; closing remaining connections", "err", err)
		_ = srv.Close()
	}
	log.Info("stopped")
	return nil
}

// newPublisher builds the publisher selected by cfg.Publisher, the readiness
// checks it needs, and a function that releases it.
func newPublisher(ctx context.Context, cfg config.Config, log *slog.Logger) (ingest.Publisher, []health.ReadinessChecker, func(), error) {
	switch cfg.Publisher {
	case config.PublisherMemory:
		return ingest.NewMemoryPublisher(memoryPublisherCap), nil, func() {}, nil
	case config.PublisherRedis:
		rp, err := ingest.NewRedisPublisher(ingest.RedisOptions{
			Addr:         cfg.Redis.Addr,
			Stream:       cfg.Redis.Stream,
			Timeout:      cfg.Redis.Timeout,
			MaxStreamLen: cfg.IngestMaxStreamLen,
		})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("redis publisher: %w", err)
		}
		// Not fatal: /readyz reports 503 until Redis is reachable.
		if err := rp.Ready(ctx); err != nil {
			log.Warn("redis not reachable at startup", "addr", cfg.Redis.Addr, "err", err)
		} else {
			log.Info("redis publisher ready", "addr", cfg.Redis.Addr, "stream", cfg.Redis.Stream,
				"max_stream_len", cfg.IngestMaxStreamLen)
		}
		// Backpressure reads the stream length from this background refresh,
		// never with an extra Redis call per request.
		refreshCtx, stopRefresh := context.WithCancel(context.Background())
		refreshDone := make(chan struct{})
		go func() {
			defer close(refreshDone)
			rp.RunLengthRefresher(refreshCtx)
		}()
		closeFn := func() {
			stopRefresh()
			<-refreshDone
			if err := rp.Close(); err != nil {
				log.Warn("close redis publisher", "err", err)
			}
		}
		return rp, []health.ReadinessChecker{rp}, closeFn, nil
	default:
		return nil, nil, nil, fmt.Errorf("unknown publisher %q", cfg.Publisher)
	}
}
