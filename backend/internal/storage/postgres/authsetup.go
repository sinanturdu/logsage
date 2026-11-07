package postgres

import (
	"context"
	"log/slog"

	"github.com/sinanturdu/logsage/backend/internal/auth"
	"github.com/sinanturdu/logsage/backend/internal/config"
	"github.com/sinanturdu/logsage/backend/internal/keys"
)

// Ready pings PostgreSQL (for /readyz).
func (s *Store) Ready(ctx context.Context) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	return s.pool.Ping(ctx)
}

// NewAuthenticator builds the shared API key authenticator from config: a
// database-backed verifier when DATABASE_URL is set (store returned for
// readiness checks, close releases the pool), plus the legacy static keys
// when ALLOW_LEGACY_STATIC_KEYS allows them. Used by cmd/ingest and cmd/query.
func NewAuthenticator(ctx context.Context, cfg config.Config, log *slog.Logger) (*auth.Service, *Store, func(), error) {
	opts := auth.Options{
		AllowLegacy:     cfg.Auth.AllowLegacy,
		LegacyIngestKey: cfg.IngestAPIKey,
		LegacyQueryKey:  cfg.Query.APIKey,
		CacheTTL:        cfg.Auth.CacheTTL,
		NegativeTTL:     cfg.Auth.NegativeTTL,
		CacheSize:       cfg.Auth.CacheSize,
		VerifyTimeout:   cfg.Postgres.QueryTimeout,
	}
	var store *Store
	closeFn := func() {}
	if cfg.Postgres.URL != "" {
		pool, err := Open(ctx, cfg.Postgres)
		if err != nil {
			return nil, nil, nil, err
		}
		store = NewStore(pool, cfg.Postgres.QueryTimeout)
		opts.Verifier = keys.NewVerifier(store, log)
		closeFn = pool.Close
	}
	svc, err := auth.New(opts)
	if err != nil {
		closeFn()
		return nil, nil, nil, err
	}
	svc.LogDeprecation(log)
	return svc, store, closeFn, nil
}
