package broker

import (
	"context"
	"database/sql"
	"time"

	"github.com/Harvey-AU/hover/internal/observability"
)

// ProbeOpts configures a broker Probe.
type ProbeOpts struct {
	// Interval between probe ticks. Default 5s.
	Interval time.Duration
	// TickTimeout bounds a single tick so a slow Redis or DB call
	// cannot stall the whole probe loop. Default 3s.
	TickTimeout time.Duration
}

// DefaultProbeOpts returns production defaults.
func DefaultProbeOpts() ProbeOpts {
	return ProbeOpts{
		Interval:    5 * time.Second,
		TickTimeout: 3 * time.Second,
	}
}

// Probe periodically scrapes Tier 1 gauges that have no natural
// emission site (stream length, ZSET depth, pending count, outbox
// backlog + age, Redis PING, pool stats) and feeds them to the
// observability package. Intended to run once per process.
type Probe struct {
	client    *Client
	db        *sql.DB
	jobLister JobLister
	opts      ProbeOpts
}

// NewProbe constructs a Probe. db may be nil on the API side if the
// outbox is only scraped by the worker. Zero-valued opts fields are
// back-filled from DefaultProbeOpts so the defaults have a single
// source of truth.
func NewProbe(client *Client, db *sql.DB, lister JobLister, opts ProbeOpts) *Probe {
	def := DefaultProbeOpts()
	if opts.Interval <= 0 {
		opts.Interval = def.Interval
	}
	if opts.TickTimeout <= 0 {
		opts.TickTimeout = def.TickTimeout
	}
	return &Probe{client: client, db: db, jobLister: lister, opts: opts}
}

// Run drives the probe loop until ctx is cancelled. Errors are logged
// and the loop continues; telemetry gaps are preferable to crashes.
func (p *Probe) Run(ctx context.Context) {
	brokerLog.Info("broker probe started", "interval", p.opts.Interval)

	t := time.NewTicker(p.opts.Interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			brokerLog.Info("broker probe stopped", "reason", ctx.Err())
			return
		case <-t.C:
			p.tick(ctx)
		}
	}
}

func (p *Probe) tick(ctx context.Context) {
	// Bound the whole tick so a single slow backend (Redis hang, DB
	// stall) can't pin the probe goroutine indefinitely. Skipped ticks
	// simply produce a gap in the series, which is the honest signal.
	tickCtx, cancel := context.WithTimeout(ctx, p.opts.TickTimeout)
	defer cancel()

	p.probePing(tickCtx)
	p.probePool(tickCtx)
	p.probeOutbox(tickCtx)
	p.probeJobs(tickCtx)
}

func (p *Probe) probePing(ctx context.Context) {
	start := time.Now()
	err := p.client.Ping(ctx)
	observability.RecordBrokerRedisPing(ctx, time.Since(start), err == nil)
	if err != nil {
		brokerLog.Warn("broker probe ping failed", "error", err)
	}
}

func (p *Probe) probePool(ctx context.Context) {
	stats := p.client.rdb.PoolStats()
	if stats == nil {
		return
	}
	observability.RecordBrokerRedisPool(ctx, observability.RedisPoolSnapshot{
		InUse: int64(stats.TotalConns - stats.IdleConns),
		Idle:  int64(stats.IdleConns),
		Waits: int64(stats.WaitCount),
	})
}

func (p *Probe) probeOutbox(ctx context.Context) {
	if p.db == nil {
		return
	}

	var (
		backlog       int64
		oldestSeconds sql.NullFloat64
	)
	// Only count rows that are actually due. Future-scheduled rows
	// (retry backoff, throttled reschedule) aren't a backlog — counting
	// them inflates the gauge and, worse, MIN(run_at) would be in the
	// future and produce a negative age.
	row := p.db.QueryRowContext(ctx, `
		SELECT COUNT(*)::bigint,
		       EXTRACT(EPOCH FROM NOW() - MIN(run_at))
		  FROM task_outbox
		 WHERE run_at <= NOW()
	`)
	if err := row.Scan(&backlog, &oldestSeconds); err != nil {
		brokerLog.Warn("broker probe outbox scan failed", "error", err)
		return
	}
	age := 0.0
	if oldestSeconds.Valid && oldestSeconds.Float64 > 0 {
		age = oldestSeconds.Float64
	}
	observability.RecordBrokerOutbox(ctx, backlog, age)
}

func (p *Probe) probeJobs(ctx context.Context) {
	if p.jobLister == nil {
		return
	}
	jobIDs, err := p.jobLister.ActiveJobIDs(ctx)
	if err != nil {
		brokerLog.Warn("broker probe active jobs failed", "error", err)
		return
	}

	for _, jobID := range jobIDs {
		if ctx.Err() != nil {
			return
		}
		p.probeJob(ctx, jobID)
	}
}

// probeJob issues XLEN, ZCARD, and XLEN-of-pending via XPENDING for a
// single job. The three calls are issued in one pipeline so the probe
// adds one round-trip per job rather than three.
func (p *Probe) probeJob(ctx context.Context, jobID string) {
	pipe := p.client.rdb.Pipeline()
	streamLenCmd := pipe.XLen(ctx, StreamKey(jobID))
	zsetCmd := pipe.ZCard(ctx, ScheduleKey(jobID))
	// XPENDING summary returns (count, min_id, max_id, consumers).
	pendingCmd := pipe.XPending(ctx, StreamKey(jobID), ConsumerGroup(jobID))

	if _, err := pipe.Exec(ctx); err != nil {
		// NOGROUP / "no such key" is expected before the first dispatch —
		// all three commands return zero, which is the correct snapshot.
		// For any other error (Redis outage, timeout, etc.) skip emission
		// so we produce a gap in the series rather than false zeroes that
		// masquerade as a healthy empty queue.
		if !isNoGroupErr(err) {
			brokerLog.Debug("broker probe pipeline error", "error", err, "job_id", jobID)
			return
		}
	}

	streamLen, _ := streamLenCmd.Result()
	zDepth, _ := zsetCmd.Result()

	var pendingCount int64
	if pending, err := pendingCmd.Result(); err == nil && pending != nil {
		pendingCount = pending.Count
	}

	observability.RecordBrokerStreamStats(ctx, observability.BrokerStreamStats{
		JobID:          jobID,
		StreamLength:   streamLen,
		ScheduledDepth: zDepth,
		Pending:        pendingCount,
	})
}
