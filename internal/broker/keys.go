// Package broker provides Redis-backed task scheduling, dispatch, and
// coordination primitives for the hover crawl execution pipeline.
package broker

import "fmt"

const keyPrefix = "hover:"

// ZSET; score = earliest runnable unix-ms.
func ScheduleKey(jobID string) string { return keyPrefix + "sched:" + jobID }

func StreamKey(jobID string) string { return keyPrefix + "stream:" + jobID }

// Distinct stream so crawl workers and analysis can scale independently.
func LighthouseStreamKey(jobID string) string { return keyPrefix + "stream:" + jobID + ":lh" }

func ConsumerGroup(jobID string) string           { return keyPrefix + "cg:" + jobID }
func LighthouseConsumerGroup(jobID string) string { return keyPrefix + "cg:" + jobID + ":lh" }

// HASH: jobID → in-flight task count.
const RunningCountersKey = keyPrefix + "running"

// String time-gate. SET NX PX {delay_ms} caps per-domain dispatch rate.
func DomainGateKey(domain string) string { return keyPrefix + "dom:gate:" + domain }

// HASH: base_delay_ms, adaptive_delay_ms, floor_ms, success_streak, error_streak.
func DomainConfigKey(domain string) string { return keyPrefix + "dom:cfg:" + domain }

// HASH: jobID → inflight task count for this domain+job pair.
func DomainInflightKey(domain string) string { return keyPrefix + "dom:flight:" + domain }

// Pipe-delimited (avoids JSON overhead on the scheduling hot path).
//
// Current:  taskID|jobID|pageID|host|path|priority|retryCount|sourceType|sourceURL|taskType|lighthouseRunID
// Legacy:   taskID|jobID|pageID|host|path|priority|retryCount|sourceType|sourceURL
//
// ParseScheduleEntry accepts both so a deploy rolls forward without a
// ZSET flush; legacy entries default to taskType='crawl'.
func FormatScheduleEntry(taskID, jobID string, pageID int, host, path string, priority float64, retryCount int, sourceType, sourceURL, taskType string, lighthouseRunID int64) string {
	return fmt.Sprintf("%s|%s|%d|%s|%s|%.4f|%d|%s|%s|%s|%d",
		taskID, jobID, pageID, host, path, priority, retryCount, sourceType, sourceURL, taskType, lighthouseRunID)
}
