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

// RemoveJobKeys deletes every Redis key owned by the broker for a single
// terminal (completed/cancelled/failed) job. Called from the completion
// tick and CancelJob to stop the per-job key set leaking into resident
// data — without it the schedule ZSET, both streams, both consumer
// groups, and the running-counter HASH field persist forever once the
// dispatcher stops scanning the job.
//
// The two XGroupDestroy calls are best-effort: NOGROUP/no-such-stream
// errors are tolerated so a partially-cleaned job (or a job that never
// produced lighthouse work) doesn't abort the rest of the cleanup.
func (c *Client) RemoveJobKeys(ctx context.Context, jobID string) error {
	if jobID == "" {
		return fmt.Errorf("broker: RemoveJobKeys requires a jobID")
	}

	streamKey := StreamKey(jobID)
	lhStreamKey := LighthouseStreamKey(jobID)

	// Destroy consumer groups before deleting the streams. Failures here
	// are non-fatal — the group may already be gone, or the stream may
	// never have been created (e.g. a job cancelled before any task ran).
	if err := c.rdb.XGroupDestroy(ctx, streamKey, ConsumerGroup(jobID)).Err(); err != nil && !isMissingGroup(err) {
		brokerLog.Warn("XGroupDestroy crawl group failed", "error", err, "job_id", jobID)
	}
	if err := c.rdb.XGroupDestroy(ctx, lhStreamKey, LighthouseConsumerGroup(jobID)).Err(); err != nil && !isMissingGroup(err) {
		brokerLog.Warn("XGroupDestroy lighthouse group failed", "error", err, "job_id", jobID)
	}

	pipe := c.rdb.Pipeline()
	pipe.Del(ctx, ScheduleKey(jobID), streamKey, lhStreamKey)
	pipe.HDel(ctx, RunningCountersKey, jobID)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("broker: remove job keys for %s: %w", jobID, err)
	}
	return nil
}

// isMissingGroup reports whether err is the Redis NOGROUP / no-such-key
// response from XGroupDestroy on a stream or group that does not exist.
// Tolerated by RemoveJobKeys so cleanup is idempotent.
func isMissingGroup(err error) bool {
	if err == nil || err == redis.Nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "NOGROUP") ||
		strings.Contains(msg, "no such key") ||
		strings.Contains(msg, "requires the key to exist")
}

// ClearAll deletes every Redis key the broker writes to. Used by admin
// reset endpoints. Does not call FLUSHDB — only touches hover:* prefixes
// owned by this package, so it stays safe on a shared Redis. Returns the
// number of keys deleted.
func (c *Client) ClearAll(ctx context.Context) (int, error) {
	// Patterns covering every prefix defined in keys.go. Deleting the
	// per-job stream keys also removes their attached consumer groups,
	// so there is no separate hover:cg:* key to scan.
	patterns := []string{
		keyPrefix + "sched:*",
		keyPrefix + "stream:*",
		keyPrefix + "dom:gate:*",
		keyPrefix + "dom:cfg:*",
		keyPrefix + "dom:flight:*",
	}

	total := 0
	for _, pattern := range patterns {
		n, err := c.scanAndDelete(ctx, pattern)
		if err != nil {
			return total, fmt.Errorf("broker: clear %s: %w", pattern, err)
		}
		total += n
	}

	// RunningCountersKey is a single fixed key — no scan needed.
	deleted, err := c.rdb.Del(ctx, RunningCountersKey).Result()
	if err != nil {
		return total, fmt.Errorf("broker: clear %s: %w", RunningCountersKey, err)
	}
	total += int(deleted)

	return total, nil
}

// scanAndDelete walks every key matching pattern with SCAN and deletes
// them in batches of up to 500 per DEL call.
func (c *Client) scanAndDelete(ctx context.Context, pattern string) (int, error) {
	const batch = 500
	var (
		cursor  uint64
		keys    []string
		deleted int
	)

	for {
		var (
			page []string
			err  error
		)
		page, cursor, err = c.rdb.Scan(ctx, cursor, pattern, batch).Result()
		if err != nil {
			return deleted, err
		}
		keys = append(keys, page...)

		// Flush whenever we have enough to make a worthwhile DEL call.
		for len(keys) >= batch {
			n, err := c.rdb.Del(ctx, keys[:batch]...).Result()
			if err != nil {
				return deleted, err
			}
			deleted += int(n)
			keys = keys[batch:]
		}

		if cursor == 0 {
			break
		}
	}

	if len(keys) > 0 {
		n, err := c.rdb.Del(ctx, keys...).Result()
		if err != nil {
			return deleted, err
		}
		deleted += int(n)
	}

	return deleted, nil
}

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
