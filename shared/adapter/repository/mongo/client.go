// Package mongo implements the MongoDB Atlas adapters for the write-side ports.
//
// Mongo and BSON types are confined to this package: callers only ever see
// shared/domain DTOs and shared/domain/errs errors. The data pipeline builds
// these stores, the chat service may read through the read-side ports.
package mongo

import (
	"context"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"

	sharedcfg "github.com/zed/platepilot/shared/config"
	"github.com/zed/platepilot/shared/domain/errs"
)

// Defaults applied when a Config field is left at its zero value.
const (
	DefaultDatabase               = "platepilot"
	DefaultTimeout                = 10 * time.Second
	DefaultConnectTimeout         = 10 * time.Second
	DefaultServerSelectionTimeout = 10 * time.Second
	// DefaultWriteTimeout bounds bulk writes and aggregations.
	DefaultWriteTimeout = 60 * time.Second
)

// Config is the resolved Atlas connection configuration.
//
// Retryable writes are left to the driver/connection-string default (Atlas
// enables them for SRV URIs), so there is no separate knob to misconfigure.
type Config struct {
	URI                    string
	Database               string
	Timeout                time.Duration
	WriteTimeout           time.Duration
	ConnectTimeout         time.Duration
	ServerSelectionTimeout time.Duration
	SocketTimeout          time.Duration
	MaxPoolSize            uint64
	MinPoolSize            uint64
}

// withDefaults fills in the zero values.
func (c Config) withDefaults() Config {
	if c.Database == "" {
		c.Database = DefaultDatabase
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = DefaultWriteTimeout
	}
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = DefaultConnectTimeout
	}
	if c.ServerSelectionTimeout <= 0 {
		c.ServerSelectionTimeout = DefaultServerSelectionTimeout
	}
	return c
}

// Validate reports configuration problems as a slice of human-readable strings.
func (c Config) Validate() []string {
	var problems []string
	if strings.TrimSpace(c.URI) == "" {
		problems = append(problems, "MONGO_URI: required to connect to Atlas")
	}
	if strings.TrimSpace(c.Database) == "" {
		problems = append(problems, "MONGO_DATABASE: must not be empty")
	}
	return problems
}

// Client wraps a connected *mongo.Client plus its resolved database.
type Client struct {
	client   *mongo.Client
	database *mongo.Database
	cfg      Config
}

// ConfigFromMongo derives a Config from the shared Mongo configuration.
func ConfigFromMongo(mc sharedcfg.MongoConfig) Config {
	return Config{
		URI:            mc.URI,
		Database:       mc.Database,
		Timeout:        mc.Timeout,
		WriteTimeout:   mc.WriteTimeout,
		ConnectTimeout: mc.ConnectTimeout,
		MaxPoolSize:    mc.MaxPoolSize,
		MinPoolSize:    mc.MinPoolSize,
	}.withDefaults()
}

// Connect dials Atlas, then pings to prove the credentials and network work.
func Connect(ctx context.Context, cfg Config) (*Client, error) {
	cfg = cfg.withDefaults()
	if problems := cfg.Validate(); len(problems) > 0 {
		return nil, errs.Newf(errs.CodeInvalidArgument, "invalid mongo config: %s", strings.Join(problems, "; "))
	}

	opts := options.Client().ApplyURI(cfg.URI)
	opts.SetConnectTimeout(cfg.ConnectTimeout)
	opts.SetServerSelectionTimeout(cfg.ServerSelectionTimeout)
	if cfg.SocketTimeout > 0 {
		opts.SetSocketTimeout(cfg.SocketTimeout)
	}
	if cfg.MaxPoolSize > 0 {
		opts.SetMaxPoolSize(cfg.MaxPoolSize)
	}
	if cfg.MinPoolSize > 0 {
		opts.SetMinPoolSize(cfg.MinPoolSize)
	}

	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, errs.Wrap(errs.CodeProviderUnavailable, "mongo: connect", err)
	}

	c := &Client{client: client, database: client.Database(cfg.Database), cfg: cfg}
	pingCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	if err := c.Ping(pingCtx); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}
	return c, nil
}

// Ping verifies connectivity through the primary.
func (c *Client) Ping(ctx context.Context) error {
	if err := c.client.Ping(ctx, readpref.Primary()); err != nil {
		return operationError("mongo: ping", err)
	}
	return nil
}

// Close releases the connection pool.
func (c *Client) Close(ctx context.Context) error {
	return operationError("mongo: close", c.client.Disconnect(ctx))
}

// DatabaseName returns the resolved database name (safe to log).
func (c *Client) DatabaseName() string { return c.cfg.Database }

// Timeout returns the configured per-operation timeout.
func (c *Client) Timeout() time.Duration { return c.cfg.Timeout }

func (c *Client) db() *mongo.Database { return c.database }

// collection is a convenience accessor for the stores in this package.
func (c *Client) collection(name string) *mongo.Collection {
	return c.database.Collection(name)
}

// withTimeout derives a per-operation context bounded by the configured
// timeout, so one slow query cannot hold a pooled connection indefinitely.
func (c *Client) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.cfg.Timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.cfg.Timeout)
}

// withWriteTimeout derives the (larger) budget used by bulk writes and
// aggregations, which cannot fit in a point-operation timeout on a remote
// cluster.
func (c *Client) withWriteTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.cfg.WriteTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.cfg.WriteTimeout)
}
