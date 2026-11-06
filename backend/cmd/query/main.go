// Command query serves the read-only log search API (GET /v1/logs).
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
	"github.com/sinanturdu/logsage/backend/internal/query"
	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
)

const (
	defaultPageSize     = 100
	httpShutdownTimeout = 5 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "query:", err)
		os.Exit(1)
	}
}

// run serves until ctx is canceled, then shuts the HTTP server down within
// httpShutdownTimeout (force-closing connections after that).
func run(ctx context.Context, getenv func(string) string) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.ValidateQuery(); err != nil {
		return err
	}
	log := logging.New(os.Stdout, cfg.LogLevel, cfg.LogFormat)

	// The HTTP client may wait a little longer than ClickHouse's own
	// max_execution_time so the server reports the timeout itself.
	chTimeout := cfg.ClickHouse.Timeout
	if minTimeout := cfg.Query.Timeout + 5*time.Second; chTimeout < minTimeout {
		chTimeout = minTimeout
	}
	ch, err := clickhouse.New(clickhouse.Options{
		URL:      cfg.ClickHouse.URL,
		Database: cfg.ClickHouse.Database,
		User:     cfg.ClickHouse.User,
		Password: cfg.ClickHouse.Password,
		Timeout:  chTimeout,
	})
	if err != nil {
		return err
	}

	pageSize := defaultPageSize
	if cfg.Query.MaxLimit < pageSize {
		pageSize = cfg.Query.MaxLimit
	}
	h, err := query.NewHandler(ch, cfg.Query.APIKey, query.Options{
		ProjectID:    query.DefaultProjectID,
		MaxWindow:    cfg.Query.MaxWindow,
		DefaultLimit: pageSize,
		MaxLimit:     cfg.Query.MaxLimit,
		MaxTextLen:   cfg.Query.MaxTextLen,
		Timeout:      cfg.Query.Timeout,
	}, log)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	health.Register(mux)
	health.RegisterReady(mux, log, ch)
	h.Register(mux)

	srv := &http.Server{
		Addr:              cfg.Query.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      cfg.Query.Timeout + 10*time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("query api starting", "addr", cfg.Query.HTTPAddr, "clickhouse_db", cfg.ClickHouse.Database,
			"max_window", cfg.Query.MaxWindow.String(), "max_limit", cfg.Query.MaxLimit, "timeout", cfg.Query.Timeout.String())
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
	sctx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close() // force-close connections still open after the budget
	}
	log.Info("stopped")
	return nil
}
