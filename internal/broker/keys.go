// Package broker provides Redis-backed task scheduling, dispatch, and
// coordination primitives for the hover crawl execution pipeline.
package broker

import "fmt"

// Redis key prefixes and patterns. Every key used by the broker is
// defined here so naming stays consistent and grep-able.

const keyPrefix = "hover:"

// Schedule keys — sorted sets keyed by job, scored by earliest
// runnable unix-millisecond timestamp.
func ScheduleKey(jobID string) string { return keyPrefix + "sched:" + jobID }

// Stream keys — per-job streams that hold ready-to-run task envelopes.
func StreamKey(jobID string) string { return keyPrefix + "stream:" + jobID }

// LighthouseStreamKey returns the per-job Redis stream that the
// hover-analysis service consumes. Lives alongside the crawl stream so
// crawler workers cannot accidentally pop a lighthouse audit (and vice
// versa) and so the analysis app can be sized independently.
func LighthouseStreamKey(jobID string) string { return keyPrefix + "stream:" + jobID + ":lh" }

// ConsumerGroup returns the consumer group name for a job stream.
func ConsumerGroup(jobID string) string { return keyPrefix + "cg:" + jobID }

// LighthouseConsumerGroup is the consumer group name for the per-job
// lighthouse stream. Distinct from the crawl group so the analysis app
// gets its own delivery state and PEL.
func LighthouseConsumerGroup(jobID string) string { return keyPrefix + "cg:" + jobID + ":lh" }

// RunningCountersKey is a single hash whose fields are job IDs and
// values are the number of tasks currently in-flight.
const RunningCountersKey = keyPrefix + "running"

// Domain pacing keys.

// DomainGateKey is a short-lived string used as a time gate.
// SET NX PX {delay_ms} prevents dispatching to the same domain
// faster than its configured rate.
func DomainGateKey(domain string) string { return keyPrefix + "dom:gate:" + domain }

// DomainConfigKey is a hash storing adaptive delay state:
// base_delay_ms, adaptive_delay_ms, floor_ms, success_streak, error_streak.
func DomainConfigKey(domain string) string { return keyPrefix + "dom:cfg:" + domain }

// DomainInflightKey is a hash whose fields are job IDs and values
// are the number of inflight tasks for that domain+job pair.
func DomainInflightKey(domain string) string { return keyPrefix + "dom:flight:" + domain }

// ScheduleEntry is the member value stored inside the schedule ZSET.
// It is serialised as a compact pipe-delimited string to avoid JSON
// overhead on the hot scheduling path.
//
// Format (current):
//
//	taskID|jobID|pageID|host|path|priority|retryCount|sourceType|sourceURL|taskType|lighthouseRunID
//
// Format (legacy, pre-Phase-2):
//
//	taskID|jobID|pageID|host|path|priority|retryCount|sourceType|sourceURL
//
// ParseScheduleEntry accepts both shapes so a deploy can roll forward
// without flushing the in-flight ZSET. Legacy members are interpreted
// as taskType='crawl' with no lighthouse_run_id.
func FormatScheduleEntry(taskID, jobID string, pageID int, host, path string, priority float64, retryCount int, sourceType, sourceURL, taskType string, lighthouseRunID int64) string {
	return fmt.Sprintf("%s|%s|%d|%s|%s|%.4f|%d|%s|%s|%s|%d",
		taskID, jobID, pageID, host, path, priority, retryCount, sourceType, sourceURL, taskType, lighthouseRunID)
}
