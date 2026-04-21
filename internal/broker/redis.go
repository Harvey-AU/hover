package broker

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Harvey-AU/hover/internal/logging"
	"github.com/redis/go-redis/v9"
)

// brokerLog is the package-scoped structured logger for all broker components.
var brokerLog = logging.Component("broker")

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
// TLS is inferred from the URL scheme (rediss:// = TLS) unless
// REDIS_TLS_ENABLED is explicitly set.
func ConfigFromEnv() Config {
	poolSize := envInt("REDIS_POOL_SIZE", 200)
	url := os.Getenv("REDIS_URL")

	// Default TLS from scheme: rediss:// → true, redis:// → false.
	tlsDefault := strings.HasPrefix(url, "rediss://")
	tlsEnabled := envBool("REDIS_TLS_ENABLED", tlsDefault)

	return Config{
		URL:          url,
		PoolSize:     poolSize,
		TLSEnabled:   tlsEnabled,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		MaxRetries:   3,
	}
}

// Client wraps a go-redis client with convenience helpers.
type Client struct {
	rdb *redis.Client
}

// NewClient parses cfg.URL and returns a connected Client.
func NewClient(cfg Config) (*Client, error) {
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

	return &Client{rdb: rdb}, nil
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
		brokerLog.Warn("invalid integer for env var, using default",
			"key", key, "value", v, "default", def)
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
