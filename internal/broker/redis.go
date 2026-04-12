package broker

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

// Config holds Redis connection parameters, typically loaded from
// environment variables.
type Config struct {
	URL        string
	PoolSize   int
	TLSEnabled bool

	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	MaxRetries   int
}

// ConfigFromEnv builds a Config from the process environment.
func ConfigFromEnv() Config {
	poolSize := envInt("REDIS_POOL_SIZE", 200)
	tlsEnabled := envBool("REDIS_TLS_ENABLED", true)

	return Config{
		URL:          os.Getenv("REDIS_URL"),
		PoolSize:     poolSize,
		TLSEnabled:   tlsEnabled,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		MaxRetries:   3,
	}
}

// Client wraps a go-redis client with convenience helpers.
type Client struct {
	rdb    *redis.Client
	logger zerolog.Logger
}

// NewClient parses cfg.URL and returns a connected Client.
func NewClient(cfg Config, logger zerolog.Logger) (*Client, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("broker: REDIS_URL is required")
	}

	opts, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("broker: parse REDIS_URL: %w", err)
	}

	opts.PoolSize = cfg.PoolSize
	opts.ReadTimeout = cfg.ReadTimeout
	opts.WriteTimeout = cfg.WriteTimeout
	opts.MaxRetries = cfg.MaxRetries

	if cfg.TLSEnabled && opts.TLSConfig == nil {
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if !cfg.TLSEnabled {
		opts.TLSConfig = nil
	}

	rdb := redis.NewClient(opts)

	c := &Client{
		rdb:    rdb,
		logger: logger.With().Str("component", "broker").Logger(),
	}
	return c, nil
}

// Ping verifies the connection is alive.
func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

// Close releases the underlying connection pool.
func (c *Client) Close() error {
	return c.rdb.Close()
}

// RDB exposes the raw go-redis client for packages that need direct
// access (e.g. Lua scripts, pipelines).
func (c *Client) RDB() *redis.Client { return c.rdb }

// --- env helpers ---

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
