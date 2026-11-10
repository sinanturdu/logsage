// Command api serves the management API: login with server-side sessions,
// projects, members and API keys (ADR 0013), and the session-authenticated
// log read endpoints for the dashboard (ADR 0014).
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/alert/outbound"
	"github.com/sinanturdu/logsage/backend/internal/alert/secret"
	"github.com/sinanturdu/logsage/backend/internal/api"
	"github.com/sinanturdu/logsage/backend/internal/config"
	"github.com/sinanturdu/logsage/backend/internal/health"
	"github.com/sinanturdu/logsage/backend/internal/logging"
	"github.com/sinanturdu/logsage/backend/internal/password"
	"github.com/sinanturdu/logsage/backend/internal/query"
	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
	"github.com/sinanturdu/logsage/backend/internal/web"
)

// defaultPageSize is the log page size when the request has no limit,
// the same as cmd/query.
const defaultPageSize = 100

const httpShutdownTimeout = 5 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv, nil); err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

// hasherParams maps the ARGON2_* settings onto password.Params.
func hasherParams(a config.APIConfig) password.Params {
	p := password.DefaultParams()
	p.MemoryKiB = uint32(a.Argon2MemoryKiB)
	p.Iterations = uint32(a.Argon2Iterations)
	p.Parallelism = uint8(a.Argon2Parallelism)
	return p
}

// tailOptions maps the TAIL_* settings onto api.TailOptions.
func tailOptions(t config.TailConfig) api.TailOptions {
	return api.TailOptions{
		PollInterval: t.PollInterval, Batch: t.Batch, Heartbeat: t.Heartbeat, MaxDuration: t.MaxDuration,
		AuthRecheck: t.AuthRecheck, MaxReplay: t.MaxReplay, MaxLateness: t.MaxLateness, Overlap: t.Overlap,
		WriteTimeout: t.WriteTimeout, RetryMax: t.RetryMax, MaxConnUser: t.MaxConnUser,
		MaxConnProject: t.MaxConnProject, MaxConnTotal: t.MaxConnTotal, MaxSeen: t.MaxSeen,
	}
}

// alertOptions turns the alert endpoints on when ALERTS_ENABLED=true. A
// missing or malformed key does not stop the API: the alert endpoints then
// answer 503 with the reason (ADR 0024).
func alertOptions(getenv func(string) string, store *postgres.Store, log *slog.Logger) (*api.AlertsOptions, error) {
	ac, err := config.LoadAlerts(getenv)
	if err != nil {
		return nil, err
	}
	if !ac.Enabled {
		return nil, nil
	}
	if ac.KeyErr != nil {
		log.Error("alerts are enabled but cannot work", "err", ac.KeyErr)
		return &api.AlertsOptions{KeyErr: ac.KeyErr}, nil
	}
	sealer, err := secret.NewSealer(ac.SecretKey)
	if err != nil {
		return nil, err
	}
	return &api.AlertsOptions{
		Store: store, Sealer: sealer, DashboardURL: ac.DashboardURL,
		URLs: outbound.New(outbound.Options{AllowInsecureLocal: ac.AllowInsecureLocal, AllowedPorts: ac.AllowedPorts}),
	}, nil
}

// run serves until ctx is canceled, then shuts down within
// httpShutdownTimeout. ready, if not nil, receives the listening address.
// registerWeb adds /config.json and, when enabled, the dashboard as the
// catch-all route; /api/, /healthz and /readyz keep their own handlers.
// It reports whether a built dashboard is served.
func registerWeb(mux *http.ServeMux, a config.APIConfig, files fs.FS) (bool, error) {
	cfgHandler, err := web.ConfigHandler(web.PublicConfig{IngestURL: a.PublicIngestURL})
	if err != nil {
		return false, err
	}
	mux.Handle("/config.json", cfgHandler)
	if !a.WebEnabled {
		return false, nil
	}
	h := web.New(files)
	mux.Handle("/", h)
	return h.Built(), nil
}

func run(ctx context.Context, getenv func(string) string, ready chan<- string) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.ValidateAPI(getenv); err != nil {
		return err
	}
	log := logging.New(os.Stdout, cfg.LogLevel, cfg.LogFormat)
	a := cfg.API

	hasher, err := password.NewHasher(hasherParams(a), a.Argon2MaxConcurrent)
	if err != nil {
		return fmt.Errorf("password hasher: %w", err)
	}
	pool, err := postgres.Open(ctx, cfg.Postgres)
	if err != nil {
		return err
	}
	defer pool.Close()
	store := postgres.NewStore(pool, cfg.Postgres.QueryTimeout)

	// Log reads use the shared query service with the QUERY_* limits. The
	// HTTP client waits a little longer than ClickHouse's own limit so the
	// server reports a timeout itself.
	chTimeout := cfg.ClickHouse.Timeout
	if minTimeout := cfg.Query.Timeout + 5*time.Second; chTimeout < minTimeout {
		chTimeout = minTimeout
	}
	ch, err := clickhouse.New(clickhouse.Options{
		URL: cfg.ClickHouse.URL, Database: cfg.ClickHouse.Database, User: cfg.ClickHouse.User,
		Password: cfg.ClickHouse.Password, Timeout: chTimeout,
	})
	if err != nil {
		return err
	}
	logs, err := query.NewService(ch, query.Options{
		MaxWindow:    cfg.Query.MaxWindow,
		DefaultLimit: min(defaultPageSize, cfg.Query.MaxLimit),
		MaxLimit:     cfg.Query.MaxLimit,
		MaxTextLen:   cfg.Query.MaxTextLen,
		Timeout:      cfg.Query.Timeout,
	})
	if err != nil {
		return err
	}

	alerts, err := alertOptions(getenv, store, log)
	if err != nil {
		return err
	}

	limiter := api.NewLimiter(a.LoginMaxFailures, a.LoginDelayBase, a.LoginDelayMax, a.LoginLimiterSize)
	srv, err := api.New(store, hasher, limiter, api.Options{
		IdleTTL:              a.SessionIdleTTL,
		AbsoluteTTL:          a.SessionAbsoluteTTL,
		CookieSecure:         a.CookieSecure,
		AllowedOrigins:       a.Origins(),
		RegistrationEnabled:  a.RegistrationEnabled,
		Logs:                 logs,
		MaxConcurrentQueries: a.MaxConcurrentQueries,
		Tail:                 logs,
		TailOptions:          tailOptions(cfg.Tail),
		Alerts:               alerts,
		Incidents:            store,
	}, log)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	health.Register(mux)
	health.RegisterReady(mux, log, store, ch)
	srv.Register(mux)
	dashboard, err := registerWeb(mux, a, web.Dist())
	if err != nil {
		return err
	}

	hs := &http.Server{
		Handler:           web.Headers(mux, web.HeaderOptions{HSTS: a.CookieSecure}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}
	// Live tails set their own write deadline per event (WriteTimeout would
	// otherwise end them after 30s) and end on shutdown with status closed.
	hs.RegisterOnShutdown(srv.CloseStreams)
	ln, err := net.Listen("tcp", a.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	switch {
	case !a.WebEnabled:
		log.Info("WEB_ENABLED=false: the dashboard is not served; /config.json still is")
	case !dashboard:
		log.Warn("the dashboard is not built into this binary; use the Vite dev server or the Docker image")
	}
	if !a.CookieSecure {
		log.Warn("COOKIE_SECURE=false: session cookies are sent over plain http; use only for local development")
	}
	if a.RegistrationEnabled {
		log.Warn("REGISTRATION_ENABLED=true: anyone who can reach the API can create an account")
	}
	log.Info("management api starting", "addr", ln.Addr().String(), "cookie_secure", a.CookieSecure,
		"allowed_origins", len(a.Origins()), "idle_ttl", a.SessionIdleTTL.String(), "absolute_ttl", a.SessionAbsoluteTTL.String(),
		"argon2_m_kib", a.Argon2MemoryKiB, "argon2_t", a.Argon2Iterations, "argon2_p", a.Argon2Parallelism,
		"clickhouse_db", cfg.ClickHouse.Database, "max_concurrent_queries", a.MaxConcurrentQueries)
	if ready != nil {
		ready <- ln.Addr().String()
	}

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
		return nil
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer cancel()
	if err := hs.Shutdown(sctx); err != nil {
		_ = hs.Close()
	}
	log.Info("stopped")
	return nil
}
