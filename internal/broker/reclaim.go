package broker

import (
	"context"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

// TerminalFilter receives a batch of jobIDs found in Redis and must
// return the subset that have reached a terminal state in the
// authoritative store (Postgres). Implementations can be batched (one
// IN-clause SELECT) so the broker package stays free of SQL.
type TerminalFilter func(ctx context.Context, jobIDs []string) ([]string, error)

// ReclaimReport summarises a one-off reclaim sweep.
type ReclaimReport struct {
	// CandidatesScanned is the number of unique jobIDs found in any
	// per-job Redis key (schedule ZSET, streams, running-counter HASH).
	CandidatesScanned int
	// TerminalJobs is the number of jobIDs the filter classified as
	// terminal — i.e. eligible for cleanup.
	TerminalJobs int
	// Cleaned is the number of jobs whose RemoveJobKeys call returned
	// without error.
	Cleaned int
	// Failed is the number of jobs whose RemoveJobKeys call returned an
	// error. The first such error is captured in FirstError so the caller
	// can surface it without holding a slice of every failure.
	Failed     int
	FirstError error
}

// ReclaimTerminalJobKeys is the one-off backfill sweeper described in
// the Redis usage optimisation plan, phase 2. It enumerates jobIDs that
// still own per-job Redis state, asks the supplied filter which of
// those are terminal in Postgres, and runs RemoveJobKeys for each.
//
// Designed to be invoked manually after the completion-tick cleanup in
// startHealthMonitoring is verified in production. Idempotent; safe to
// re-run.
func (c *Client) ReclaimTerminalJobKeys(ctx context.Context, filter TerminalFilter) (ReclaimReport, error) {
	if filter == nil {
		return ReclaimReport{}, fmt.Errorf("broker: ReclaimTerminalJobKeys requires a TerminalFilter")
	}

	candidates, err := c.listJobIDsInRedis(ctx)
	if err != nil {
		return ReclaimReport{}, err
	}

	report := ReclaimReport{CandidatesScanned: len(candidates)}
	if len(candidates) == 0 {
		return report, nil
	}

	terminal, err := filter(ctx, candidates)
	if err != nil {
		return report, fmt.Errorf("broker: terminal filter: %w", err)
	}
	report.TerminalJobs = len(terminal)

	for _, jobID := range terminal {
		if err := c.RemoveJobKeys(ctx, jobID); err != nil {
			report.Failed++
			if report.FirstError == nil {
				report.FirstError = err
			}
			brokerLog.Warn("reclaim: RemoveJobKeys failed", "error", err, "job_id", jobID)
			continue
		}
		report.Cleaned++
	}
	return report, nil
}

// listJobIDsInRedis returns every jobID that owns at least one per-job
// key in Redis. Sources scanned: schedule ZSETs, both stream variants,
// and the running-counter HASH fields. Consumer-group keys live inside
// streams so deleting the stream removes them implicitly — no separate
// scan needed.
func (c *Client) listJobIDsInRedis(ctx context.Context) ([]string, error) {
	const batch = 500
	seen := make(map[string]struct{})

	schedPrefix := keyPrefix + "sched:"
	streamPrefix := keyPrefix + "stream:"
	lhSuffix := ":lh"

	for _, pattern := range []string{schedPrefix + "*", streamPrefix + "*"} {
		var cursor uint64
		for {
			page, next, err := c.rdb.Scan(ctx, cursor, pattern, batch).Result()
			if err != nil {
				return nil, fmt.Errorf("broker: scan %s: %w", pattern, err)
			}
			for _, key := range page {
				var jobID string
				switch {
				case strings.HasPrefix(key, schedPrefix):
					jobID = strings.TrimPrefix(key, schedPrefix)
				case strings.HasPrefix(key, streamPrefix):
					jobID = strings.TrimPrefix(key, streamPrefix)
					jobID = strings.TrimSuffix(jobID, lhSuffix)
				}
				if jobID != "" {
					seen[jobID] = struct{}{}
				}
			}
			if next == 0 {
				break
			}
			cursor = next
		}
	}

	fields, err := c.rdb.HKeys(ctx, RunningCountersKey).Result()
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("broker: hkeys %s: %w", RunningCountersKey, err)
	}
	for _, f := range fields {
		if f != "" {
			seen[f] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out, nil
}
