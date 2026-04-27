package broker

import (
	"context"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

// Returns the subset of jobIDs that are terminal in Postgres. Kept as
// an interface so this package stays free of SQL.
type TerminalFilter func(ctx context.Context, jobIDs []string) ([]string, error)

type ReclaimReport struct {
	CandidatesScanned int
	TerminalJobs      int
	Cleaned           int
	Failed            int
	// First failure only, to avoid retaining one error per failed job.
	FirstError error
}

// One-off backfill sweeper. Idempotent.
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

// Consumer-group keys live inside streams, so deleting the stream
// removes them — no separate scan needed.
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
