package broker

import (
	"context"
	"database/sql"
	"time"

	"github.com/Harvey-AU/hover/internal/observability"
)

type ProbeOpts struct {
	Interval time.Duration
	// Bounds a single tick so a slow Redis/DB call can't stall the loop.
	TickTimeout time.Duration
}

func DefaultProbeOpts() ProbeOpts {
	return ProbeOpts{
		Interval:    5 * time.Second,
		TickTimeout: 3 * time.Second,
	}
}

type Probe struct {
	client    *Client
	db        *sql.DB
	jobLister JobLister
	opts      ProbeOpts
}

// db may be nil on the API side. Zero opts fields fall back to defaults.
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
	// Age vs created_at not run_at: run_at can be inherited from a
	// long-waiting parent at insert time, inflating dwell-time.
	row := p.db.QueryRowContext(ctx, `
		SELECT COUNT(*)::bigint,
		       EXTRACT(EPOCH FROM NOW() - MIN(created_at))
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

	// Per-job labels removed to bound Mimir series cardinality.
	var totals observability.BrokerStreamStats
	for _, jobID := range jobIDs {
		if ctx.Err() != nil {
			return
		}
		streamLen, zDepth, pendingCount, ok := p.probeJob(ctx, jobID)
		if !ok {
			continue
		}
		totals.StreamLength += streamLen
		totals.ScheduledDepth += zDepth
		totals.Pending += pendingCount
	}
	observability.RecordBrokerStreamStats(ctx, totals)
}

// One pipelined RTT per job. ok=false on non-NOGROUP errors so a Redis
// outage produces a series gap, not false zeroes.
func (p *Probe) probeJob(ctx context.Context, jobID string) (streamLen, zDepth, pendingCount int64, ok bool) {
	pipe := p.client.rdb.Pipeline()
	streamLenCmd := pipe.XLen(ctx, StreamKey(jobID))
	zsetCmd := pipe.ZCard(ctx, ScheduleKey(jobID))
	pendingCmd := pipe.XPending(ctx, StreamKey(jobID), ConsumerGroup(jobID))

	if _, err := pipe.Exec(ctx); err != nil {
		// NOGROUP is expected before the first dispatch.
		if !isNoGroupErr(err) {
			brokerLog.Debug("broker probe pipeline error", "error", err, "job_id", jobID)
			return 0, 0, 0, false
		}
	}

	streamLen, _ = streamLenCmd.Result()
	zDepth, _ = zsetCmd.Result()

	if pending, err := pendingCmd.Result(); err == nil && pending != nil {
		pendingCount = pending.Count
	}
	return streamLen, zDepth, pendingCount, true
}
