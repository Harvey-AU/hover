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
	// the adaptive delay decreases. Default 5.
	SuccessThreshold int

	// DelayStepMS is the amount (milliseconds) the adaptive delay
	// changes per adjustment. Default 500.
	DelayStepMS int

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
	return PacerConfig{
		SuccessThreshold: 5,
		DelayStepMS:      envInt("GNH_RATE_LIMIT_DELAY_STEP_MS", 500),
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
func (p *DomainPacer) TryAcquire(ctx context.Context, domain string) (PaceResult, error) {
	delayMS, err := p.effectiveDelayMS(ctx, domain)
	if err != nil {
		return PaceResult{}, err
	}

	observability.RecordBrokerPacerDelay(ctx, domain, float64(delayMS))

	// A zero delay means no pacing — always acquire.
	if delayMS <= 0 {
		return PaceResult{Acquired: true}, nil
	}

	gateKey := DomainGateKey(domain)

	// SET NX PX: only succeeds if key doesn't exist.
	err = p.client.rdb.SetArgs(ctx, gateKey, "1", redis.SetArgs{
		Mode: "NX",
		TTL:  time.Duration(delayMS) * time.Millisecond,
	}).Err()
	if err == nil {
		// Key was set — domain is available.
		return PaceResult{Acquired: true}, nil
	}
	if err != redis.Nil {
		return PaceResult{}, fmt.Errorf("broker: gate SET NX %s: %w", domain, err)
	}
	// redis.Nil means key already existed — domain in delay window.

	// Gate exists — get remaining TTL.
	ttl, err := p.client.rdb.PTTL(ctx, gateKey).Result()
	if err != nil {
		return PaceResult{}, fmt.Errorf("broker: gate PTTL %s: %w", domain, err)
	}
	if ttl <= 0 {
		// Key expired between SET NX and PTTL — short retry.
		return PaceResult{Acquired: false, RetryAfter: p.pushbackFloor(time.Duration(delayMS) * time.Millisecond)}, nil
	}

	return PaceResult{Acquired: false, RetryAfter: p.pushbackFloor(ttl)}, nil
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
		_, err := adaptiveDelayOnSuccessScript.Run(ctx, p.client.rdb,
			[]string{cfgKey},
			p.cfg.SuccessThreshold,
			p.cfg.DelayStepMS,
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

// effectiveDelayMS returns the current effective delay for a domain,
// combining base delay and adaptive delay.
func (p *DomainPacer) effectiveDelayMS(ctx context.Context, domain string) (int, error) {
	cfgKey := DomainConfigKey(domain)

	vals, err := p.client.rdb.HMGet(ctx, cfgKey, "base_delay_ms", "adaptive_delay_ms").Result()
	if err != nil {
		return 0, fmt.Errorf("broker: effective delay %s: %w", domain, err)
	}

	baseMS := intFromHMGet(vals, 0, "base_delay_ms", domain)
	adaptiveMS := intFromHMGet(vals, 1, "adaptive_delay_ms", domain)

	// Take the larger of base and adaptive.
	if adaptiveMS > baseMS {
		return adaptiveMS, nil
	}
	return baseMS, nil
}

// intFromHMGet parses an HMGet slot as an integer, defaulting to 0 when the
// value is missing, nil, or malformed. Malformed values are logged (but not
// returned as errors) so bad config is visible without blocking the pacer.
func intFromHMGet(vals []interface{}, idx int, field, domain string) int {
	if idx >= len(vals) || vals[idx] == nil {
		return 0
	}
	s, ok := vals[idx].(string)
	if !ok {
		brokerLog.Warn("pacer: non-string HMGet value, defaulting to 0",
			"field", field, "domain", domain, "type", fmt.Sprintf("%T", vals[idx]))
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		brokerLog.Warn("pacer: malformed integer in HMGet value, defaulting to 0",
			"field", field, "domain", domain, "value", s, "error", err)
		return 0
	}
	return n
}
