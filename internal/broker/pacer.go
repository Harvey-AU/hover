package broker

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/Harvey-AU/hover/internal/observability"
	"github.com/redis/go-redis/v9"
)

// PacerConfig holds the tuning knobs for domain pacing, mirroring
// the constants from the current in-memory DomainLimiter.
type PacerConfig struct {
	// SuccessThreshold is the number of consecutive successes before
	// the adaptive delay decreases. Default 5. Override via
	// GNH_RATE_LIMIT_SUCCESS_THRESHOLD. Lower values recover faster
	// after a transient rate-limit event.
	SuccessThreshold int

	// DelayStepMS is the amount (milliseconds) the adaptive delay
	// GROWS on each rate-limited release. Default 500.
	DelayStepMS int

	// DelayStepDownMS is the amount (milliseconds) the adaptive delay
	// SHRINKS on each success once SuccessThreshold is reached. Default
	// falls back to DelayStepMS (symmetric growth/recovery). Setting
	// this higher than DelayStepMS makes recovery faster than the
	// growth on rate-limit, which is usually the right default — a
	// single spike of 429s shouldn't throttle a domain for 20 minutes.
	DelayStepDownMS int

	// MaxDelayMS caps the adaptive delay. Default 60_000 (60s).
	MaxDelayMS int

	// MinPushbackMS is the floor applied to PaceResult.RetryAfter when
	// the gate is already held. Prevents the dispatcher from tight-looping
	// through rate-limited tasks whose gate TTL is near-zero — without
	// this floor a domain with a 1ms residual delay would be re-fetched
	// every Dispatcher tick (100ms). Named after the pre-merge constant
	// domainDelayPause (internal/jobs/worker.go:147, 100ms default).
	MinPushbackMS int
}

// DefaultPacerConfig returns production defaults.
func DefaultPacerConfig() PacerConfig {
	stepUp := envInt("GNH_RATE_LIMIT_DELAY_STEP_MS", 500)
	stepDown := envInt("GNH_RATE_LIMIT_DELAY_STEP_DOWN_MS", stepUp)
	return PacerConfig{
		SuccessThreshold: envInt("GNH_RATE_LIMIT_SUCCESS_THRESHOLD", 5),
		DelayStepMS:      stepUp,
		DelayStepDownMS:  stepDown,
		MaxDelayMS:       envInt("GNH_RATE_LIMIT_MAX_DELAY_MS", 60000),
		MinPushbackMS:    envInt("GNH_DOMAIN_DELAY_PAUSE_MS", 100),
	}
}

// PaceResult is returned by TryAcquire.
type PaceResult struct {
	// Acquired is true if the domain time-gate was successfully set.
	Acquired bool

	// RetryAfter is how long the caller should wait before retrying.
	// Only meaningful when Acquired is false.
	RetryAfter time.Duration
}

// DomainPacer coordinates per-domain request rate across all worker
// instances using Redis-backed time gates and adaptive delay state.
type DomainPacer struct {
	client *Client
	cfg    PacerConfig
}

// NewDomainPacer creates a DomainPacer.
func NewDomainPacer(client *Client, cfg PacerConfig) *DomainPacer {
	return &DomainPacer{
		client: client,
		cfg:    cfg,
	}
}

// Seed initialises the domain config hash with base delay values
// (typically from robots.txt and Postgres domain record). Safe to
// call multiple times — uses HSETNX so existing values are preserved.
func (p *DomainPacer) Seed(ctx context.Context, domain string, baseDelayMS, adaptiveDelayMS, floorMS int) error {
	key := DomainConfigKey(domain)
	pipe := p.client.rdb.Pipeline()

	pipe.HSetNX(ctx, key, "base_delay_ms", strconv.Itoa(baseDelayMS))
	pipe.HSetNX(ctx, key, "adaptive_delay_ms", strconv.Itoa(adaptiveDelayMS))
	pipe.HSetNX(ctx, key, "floor_ms", strconv.Itoa(floorMS))
	pipe.HSetNX(ctx, key, "success_streak", "0")
	pipe.HSetNX(ctx, key, "error_streak", "0")
	pipe.Expire(ctx, key, 24*time.Hour)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("broker: seed domain %s: %w", domain, err)
	}
	return nil
}

// TryAcquire attempts to set the domain time-gate. If the gate is
// already held (domain was recently accessed), it returns the
// remaining wait time. This is non-blocking.
//
// Implemented as a single Lua EVALSHA so the effective-delay read and
// SET NX PX / PTTL fallback all execute server-side. The earlier
// three-call version (HMGET → SET NX PX → PTTL) was the dominant
// dispatcher round-trip cost under multi-job workloads.
func (p *DomainPacer) TryAcquire(ctx context.Context, domain string) (PaceResult, error) {
	cfgKey := DomainConfigKey(domain)
	gateKey := DomainGateKey(domain)

	raw, err := tryAcquireScript.Run(ctx, p.client.rdb, []string{cfgKey, gateKey}).Result()
	if err != nil {
		return PaceResult{}, fmt.Errorf("broker: try acquire %s: %w", domain, err)
	}

	acquired, delayMS, ttlMS, err := parseTryAcquireResult(raw)
	if err != nil {
		return PaceResult{}, fmt.Errorf("broker: try acquire %s: %w", domain, err)
	}

	observability.RecordBrokerPacerDelay(ctx, domain, float64(delayMS))

	if acquired {
		return PaceResult{Acquired: true}, nil
	}

	ttl := time.Duration(ttlMS) * time.Millisecond
	if ttl <= 0 {
		// Gate TTL expired between the SET NX and the PTTL inside the
		// script (rare, but possible under clock drift) — fall back to
		// the full effective delay rather than zero so we still pause.
		ttl = time.Duration(delayMS) * time.Millisecond
	}
	return PaceResult{Acquired: false, RetryAfter: p.pushbackFloor(ttl)}, nil
}

// parseTryAcquireResult extracts the {acquired, delay_ms, ttl_ms} tuple from a
// tryAcquireScript return value. The go-redis driver decodes Lua arrays as
// []interface{} with int64 elements.
func parseTryAcquireResult(raw interface{}) (acquired bool, delayMS, ttlMS int64, err error) {
	arr, ok := raw.([]interface{})
	if !ok || len(arr) != 3 {
		return false, 0, 0, fmt.Errorf("unexpected script result shape: %T %v", raw, raw)
	}
	ok0, _ := arr[0].(int64)
	d, _ := arr[1].(int64)
	t, _ := arr[2].(int64)
	return ok0 == 1, d, t, nil
}

// pushbackFloor applies MinPushbackMS as a lower bound on pacer RetryAfter
// durations. Pre-merge this was the domainDelayPause constant — a brief
// back-off after a domain rate-limit push-back so the dispatcher does not
// re-claim the same task on the next tick and spin on rate-limited work.
func (p *DomainPacer) pushbackFloor(d time.Duration) time.Duration {
	floor := time.Duration(p.cfg.MinPushbackMS) * time.Millisecond
	if d < floor {
		return floor
	}
	return d
}

// Release is called after a crawl completes. It updates the adaptive
// delay state based on the outcome.
func (p *DomainPacer) Release(ctx context.Context, domain, jobID string, success, rateLimited bool) error {
	cfgKey := DomainConfigKey(domain)

	if success {
		stepDown := p.cfg.DelayStepDownMS
		if stepDown <= 0 {
			stepDown = p.cfg.DelayStepMS
		}
		_, err := adaptiveDelayOnSuccessScript.Run(ctx, p.client.rdb,
			[]string{cfgKey},
			p.cfg.SuccessThreshold,
			stepDown,
		).Result()
		if err != nil {
			return fmt.Errorf("broker: release success %s: %w", domain, err)
		}
	} else if rateLimited {
		_, err := adaptiveDelayOnErrorScript.Run(ctx, p.client.rdb,
			[]string{cfgKey},
			p.cfg.DelayStepMS,
			p.cfg.MaxDelayMS,
		).Result()
		if err != nil {
			return fmt.Errorf("broker: release rate-limited %s: %w", domain, err)
		}
		observability.RecordBrokerPacerPushback(ctx, domain, "rate_limited")
	}

	// Decrement inflight.
	return p.DecrementInflight(ctx, domain, jobID)
}

// FlushAdaptiveDelays removes all accumulated per-domain adaptive
// delay state (hover:dom:cfg:* keys). Pre-merge the DomainLimiter was
// in-memory and reset on every worker restart — so a bad afternoon of
// 429s from a flaky target could never throttle crawls for longer than
// the worker's lifetime. Post-merge the state lives in Redis with a
// 24h TTL, which means a single spike can keep a domain at the 60s
// adaptive floor for a full day.
//
// Call this on worker startup to restore the pre-merge behaviour: the
// pacer still grows delay on 429 during this run, but the slate is
// clean on each deploy. Returns the number of keys deleted.
func (p *DomainPacer) FlushAdaptiveDelays(ctx context.Context) (int, error) {
	pattern := keyPrefix + "dom:cfg:*"
	iter := p.client.rdb.Scan(ctx, 0, pattern, 500).Iterator()
	var keys []string
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return 0, fmt.Errorf("broker: flush scan %s: %w", pattern, err)
	}
	if len(keys) == 0 {
		return 0, nil
	}

	// Delete in chunks of 500 to avoid oversized commands.
	deleted := 0
	const chunk = 500
	for i := 0; i < len(keys); i += chunk {
		end := i + chunk
		if end > len(keys) {
			end = len(keys)
		}
		n, err := p.client.rdb.Del(ctx, keys[i:end]...).Result()
		if err != nil {
			return deleted, fmt.Errorf("broker: flush delete: %w", err)
		}
		deleted += int(n)
	}
	return deleted, nil
}

// IncrementInflight bumps the per-domain per-job inflight counter.
func (p *DomainPacer) IncrementInflight(ctx context.Context, domain, jobID string) error {
	key := DomainInflightKey(domain)
	return p.client.rdb.HIncrBy(ctx, key, jobID, 1).Err()
}

// DecrementInflight reduces the per-domain per-job inflight counter.
func (p *DomainPacer) DecrementInflight(ctx context.Context, domain, jobID string) error {
	key := DomainInflightKey(domain)
	val, err := p.client.rdb.HIncrBy(ctx, key, jobID, -1).Result()
	if err != nil {
		return err
	}
	// Clean up zero/negative entries.
	if val <= 0 {
		if err := p.client.rdb.HDel(ctx, key, jobID).Err(); err != nil {
			brokerLog.Warn("failed to clean zero inflight entry", "error", err, "domain", domain, "job_id", jobID)
		}
	}
	return nil
}

// GetInflight returns the current inflight count for a domain+job.
func (p *DomainPacer) GetInflight(ctx context.Context, domain, jobID string) (int64, error) {
	val, err := p.client.rdb.HGet(ctx, DomainInflightKey(domain), jobID).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return val, err
}
