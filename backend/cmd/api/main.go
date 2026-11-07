// Command api serves the management API: login with server-side sessions,
// projects, members and API keys (ADR 0013).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/api"
	"github.com/sinanturdu/logsage/backend/internal/config"
	"github.com/sinanturdu/logsage/backend/internal/health"
	"github.com/sinanturdu/logsage/backend/internal/logging"
	"github.com/sinanturdu/logsage/backend/internal/password"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
)

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

// run serves until ctx is canceled, then shuts down within
// httpShutdownTimeout. ready, if not nil, receives the listening address.
func run(ctx context.Context, getenv func(string) string, ready chan<- string) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.ValidateAPI(); err != nil {
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

	limiter := api.NewLimiter(a.LoginMaxFailures, a.LoginDelayBase, a.LoginDelayMax, a.LoginLimiterSize)
	srv, err := api.New(store, hasher, limiter, api.Options{
		IdleTTL:             a.SessionIdleTTL,
		AbsoluteTTL:         a.SessionAbsoluteTTL,
		CookieSecure:        a.CookieSecure,
		AllowedOrigins:      a.Origins(),
		RegistrationEnabled: a.RegistrationEnabled,
	}, log)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	health.Register(mux)
	health.RegisterReady(mux, log, store)
	srv.Register(mux)

	hs := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}
	ln, err := net.Listen("tcp", a.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	if !a.CookieSecure {
		log.Warn("COOKIE_SECURE=false: session cookies are sent over plain http; use only for local development")
	}
	if a.RegistrationEnabled {
		log.Warn("REGISTRATION_ENABLED=true: anyone who can reach the API can create an account")
	}
	log.Info("management api starting", "addr", ln.Addr().String(), "cookie_secure", a.CookieSecure,
		"allowed_origins", len(a.Origins()), "idle_ttl", a.SessionIdleTTL.String(), "absolute_ttl", a.SessionAbsoluteTTL.String(),
		"argon2_m_kib", a.Argon2MemoryKiB, "argon2_t", a.Argon2Iterations, "argon2_p", a.Argon2Parallelism)
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
