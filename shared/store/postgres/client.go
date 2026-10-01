// Package postgres is the PostgreSQL adapter: the system of record for
// restaurants, reviews, knowledge documents, and ingestion audit.
//
// It replaces the MongoDB adapter. PostgreSQL was chosen because the product
// needs three things in one store that a document database makes awkward:
// structured hard filters, geography, and vector recall. See
// deploy/postgres/migrations for the schema and the index strategy.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	sharedcfg "github.com/zed/platepilot/shared/config"
	"github.com/zed/platepilot/shared/domain/errs"
)

// Client owns the connection pool.
type Client struct {
	pool     *pgxpool.Pool
	database string
	timeout  time.Duration
}

// Config is the connection configuration.
type Config struct {
	// DSN is a libpq-style connection string. Empty disables the store.
	DSN string
	// Database is the logical database name, used for reporting only.
	Database string
	// MaxConns bounds the pool. The import pipeline is write-heavy, so this is
	// kept modest; a larger pool mostly moves the bottleneck to the disk.
	MaxConns int32
	// MinConns keeps warm connections so a batch run does not re-handshake per
	// statement.
	MinConns int32
	// ConnectTimeout bounds the initial connection attempt.
	ConnectTimeout time.Duration
	// Timeout bounds each individual operation.
	Timeout time.Duration
}

// DefaultMaxConns is the pool size used when configuration does not set one.
const DefaultMaxConns = 8

// ConfigFromPostgres maps shared configuration onto the adapter config.
func ConfigFromPostgres(cfg sharedcfg.PostgresConfig) Config {
	return Config{
		DSN:            cfg.DSN,
		Database:       cfg.Database,
		MaxConns:       cfg.MaxPoolSize,
		MinConns:       cfg.MinPoolSize,
		ConnectTimeout: cfg.ConnectTimeout,
		Timeout:        cfg.Timeout,
	}
}

// Connect opens a pool and verifies it with a ping.
func Connect(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.DSN == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "postgres: DSN is required")
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInvalidArgument, "postgres: parse DSN", err)
	}
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	} else {
		poolCfg.MaxConns = DefaultMaxConns
	}
	if cfg.MinConns > 0 {
		poolCfg.MinConns = cfg.MinConns
	}
	if cfg.ConnectTimeout > 0 {
		poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, operationError("postgres: create pool", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, operationError("postgres: ping", err)
	}
	database := cfg.Database
	if database == "" {
		database = poolCfg.ConnConfig.Database
	}
	return &Client{pool: pool, database: database, timeout: cfg.Timeout}, nil
}

// Pool exposes the underlying pool for callers that need a transaction.
func (c *Client) Pool() *pgxpool.Pool { return c.pool }

// DatabaseName returns the connected database.
func (c *Client) DatabaseName() string { return c.database }

// Timeout returns the per-operation timeout.
func (c *Client) Timeout() time.Duration { return c.timeout }

// Close releases the pool.
func (c *Client) Close(_ context.Context) error {
	if c.pool == nil {
		return nil
	}
	c.pool.Close()
	return nil
}

// withTimeout bounds one operation, unless the caller already set a deadline.
func (c *Client) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.timeout <= 0 || ctx.Err() != nil {
		return context.WithCancel(ctx)
	}
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.timeout)
}

// Enabled reports whether a DSN was configured.
func Enabled(cfg sharedcfg.PostgresConfig) bool { return cfg.DSN != "" }

// operationError converts a pgx error into the shared error model. PostgreSQL
// and pgtype values never escape this package, so every returned error is a
// domain error carrying a stable, machine-readable code.
func operationError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return errs.Wrap(errs.CodeNotFound, op+": row not found", err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return errs.Wrap(errs.CodeConflict, op+": unique constraint violated", err)
		case "23503": // foreign_key_violation
			return errs.Wrap(errs.CodeInvalidArgument, op+": referenced row does not exist", err)
		case "23514", "22001", "22P02": // check_violation, string_data_right_truncation, invalid_text_representation
			return errs.Wrap(errs.CodeInvalidArgument, op+": value rejected by the schema: "+pgErr.Message, err)
		case "42P01": // undefined_table
			return errs.Wrap(errs.CodeInvalidArgument, op+": relation does not exist; run migrate", err)
		case "57014": // query_canceled
			return errs.Wrap(errs.CodeProviderTimeout, op+": query canceled", err)
		case "53300", "08006", "08003": // too_many_connections, connection_failure, cannot_connect_now
			return errs.Wrap(errs.CodeProviderUnavailable, op+": database unavailable", err)
		}
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return errs.Wrap(errs.CodeProviderTimeout, op+": operation timed out or was canceled", err)
	}
	return errs.Wrap(errs.CodeProviderUnavailable, fmt.Sprintf("%s: %v", op, err), err)
}
