package broker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Harvey-AU/hover/internal/observability"
	"github.com/redis/go-redis/v9"
)

// StreamMessage is a parsed task envelope read from a Redis Stream.
type StreamMessage struct {
	// MessageID is the Redis stream entry ID (e.g. "1234567890-0").
	MessageID string

	TaskID     string
	JobID      string
	PageID     int
	Host       string
	Path       string
	Priority   float64
	RetryCount int
	SourceType string
	SourceURL  string
}

// ConsumerOpts controls stream reading behaviour.
type ConsumerOpts struct {
	// ConsumerName uniquely identifies this consumer within the group.
	// Typically "worker-{machineID}-{goroutineID}".
	ConsumerName string

	// BlockTimeout is the XREADGROUP BLOCK duration. Default 2s.
	BlockTimeout time.Duration

	// Count is the max messages per XREADGROUP call. Default 1.
	Count int64

	// ClaimInterval is how often the XAUTOCLAIM sweep runs. Default 30s.
	ClaimInterval time.Duration

	// MinIdleTime is the XAUTOCLAIM min-idle-time. Messages pending
	// longer than this are reclaimed. Default 3min (TaskStaleTimeout).
	MinIdleTime time.Duration

	// MaxDeliveries is the maximum number of times a message can be
	// delivered before it is treated as a permanent failure. Default 3.
	MaxDeliveries int64

	// AutoclaimCount is the per-call XAUTOCLAIM COUNT used by ReclaimStale.
	// Default 100. Override via REDIS_AUTOCLAIM_COUNT.
	AutoclaimCount int64

	// AutoclaimMaxPerSweep is the safety cap on messages reclaimed per
	// ReclaimStale invocation across the cursor loop, so one pathological
	// job cannot starve the other jobs the reclaim loop still has to scan.
	// Default 1000. Override via REDIS_AUTOCLAIM_MAX_PER_SWEEP.
	AutoclaimMaxPerSweep int
}

// DefaultConsumerOpts returns production defaults.
func DefaultConsumerOpts(consumerName string) ConsumerOpts {
	return ConsumerOpts{
		ConsumerName: consumerName,
		BlockTimeout: time.Duration(envInt("REDIS_CONSUMER_BLOCK_MS", 2000)) * time.Millisecond,
		// Count is the max messages returned per XREADGROUP call. With
		// Count=1, every ReadNonBlocking is a round-trip per message —
		// a worker rotating across 30 active jobs makes 30 Redis calls
		// per outer loop iteration. Bumping to 10 gives the worker a
		// batch to fan out through its semaphore before the next Redis
		// round-trip, reducing tail latency without changing semantics.
		// Override via REDIS_CONSUMER_READ_COUNT.
		Count:                int64(envInt("REDIS_CONSUMER_READ_COUNT", 10)),
		ClaimInterval:        time.Duration(envInt("REDIS_AUTOCLAIM_INTERVAL_S", 30)) * time.Second,
		MinIdleTime:          time.Duration(envInt("REDIS_AUTOCLAIM_MIN_IDLE_S", 180)) * time.Second,
		MaxDeliveries:        int64(envInt("REDIS_AUTOCLAIM_MAX_DELIVERIES", 3)),
		AutoclaimCount:       int64(envInt("REDIS_AUTOCLAIM_COUNT", 100)),
		AutoclaimMaxPerSweep: envInt("REDIS_AUTOCLAIM_MAX_PER_SWEEP", 1000),
	}
}

// Consumer reads from one or more job streams via XREADGROUP and
// reclaims stale messages via XAUTOCLAIM.
type Consumer struct {
	client *Client
	opts   ConsumerOpts
}

// NewConsumer creates a Consumer.
func NewConsumer(client *Client, opts ConsumerOpts) *Consumer {
	return &Consumer{
		client: client,
		opts:   opts,
	}
}

// Read fetches new messages from the given job's stream. It blocks
// for up to opts.BlockTimeout if no messages are available.
// Returns nil (not error) when no messages are ready.
func (c *Consumer) Read(ctx context.Context, jobID string) ([]StreamMessage, error) {
	streamKey := StreamKey(jobID)
	groupName := ConsumerGroup(jobID)

	streams, err := c.client.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    groupName,
		Consumer: c.opts.ConsumerName,
		Streams:  []string{streamKey, ">"},
		Count:    c.opts.Count,
		Block:    c.opts.BlockTimeout,
	}).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		// Stream or consumer group doesn't exist yet — not an error,
		// the dispatcher creates both lazily on first XADD.
		if isNoGroupErr(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("broker: XREADGROUP %s: %w", jobID, err)
	}

	var msgs []StreamMessage
	for _, stream := range streams {
		for _, xMsg := range stream.Messages {
			msg, err := parseStreamMessage(xMsg)
			if err != nil {
				brokerLog.Warn("skipping malformed stream message", "error", err, "message_id", xMsg.ID, "consumer", c.opts.ConsumerName)
				// ACK to prevent infinite redelivery of bad messages.
				_ = c.Ack(ctx, jobID, xMsg.ID)
				continue
			}
			recordMessageAge(ctx, jobID, xMsg.ID)
			msgs = append(msgs, msg)
		}
	}
	return msgs, nil
}

// ReadNonBlocking is like Read but returns immediately if no messages
// are available. Useful for round-robin scanning across multiple jobs.
func (c *Consumer) ReadNonBlocking(ctx context.Context, jobID string) ([]StreamMessage, error) {
	streamKey := StreamKey(jobID)
	groupName := ConsumerGroup(jobID)

	streams, err := c.client.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    groupName,
		Consumer: c.opts.ConsumerName,
		Streams:  []string{streamKey, ">"},
		Count:    c.opts.Count,
		// go-redis treats Block: 0 as BLOCK 0 ms — which Redis interprets as
		// "block indefinitely". A negative duration makes the client omit the
		// BLOCK clause entirely, giving a true non-blocking poll.
		Block: -1,
	}).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		if isNoGroupErr(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("broker: XREADGROUP (non-blocking) %s: %w", jobID, err)
	}

	var msgs []StreamMessage
	for _, stream := range streams {
		for _, xMsg := range stream.Messages {
			msg, err := parseStreamMessage(xMsg)
			if err != nil {
				brokerLog.Warn("skipping malformed stream message", "error", err, "message_id", xMsg.ID, "consumer", c.opts.ConsumerName)
				_ = c.Ack(ctx, jobID, xMsg.ID)
				continue
			}
			recordMessageAge(ctx, jobID, xMsg.ID)
			msgs = append(msgs, msg)
		}
	}
	return msgs, nil
}

// recordMessageAge emits bee.broker.consumer_message_age_ms. Redis stream
// IDs are "ms-seq"; any parse failure is silently skipped so telemetry
// never blocks the consume path.
func recordMessageAge(ctx context.Context, jobID, streamID string) {
	dash := strings.IndexByte(streamID, '-')
	if dash <= 0 {
		return
	}
	ms, err := strconv.ParseInt(streamID[:dash], 10, 64)
	if err != nil {
		return
	}
	age := time.Since(time.UnixMilli(ms))
	if age < 0 {
		age = 0
	}
	observability.RecordBrokerMessageAge(ctx, jobID, float64(age.Milliseconds()))
}

// Ack acknowledges one or more messages, removing them from the
// pending entries list (PEL).
func (c *Consumer) Ack(ctx context.Context, jobID string, messageIDs ...string) error {
	if len(messageIDs) == 0 {
		return nil
	}
	streamKey := StreamKey(jobID)
	groupName := ConsumerGroup(jobID)
	return c.client.rdb.XAck(ctx, streamKey, groupName, messageIDs...).Err()
}

// ReclaimStale uses XAUTOCLAIM to take ownership of messages that
// have been pending longer than MinIdleTime. Returns the reclaimed
// messages. Messages that have been delivered more than MaxDeliveries
// times are returned separately as dead-letter candidates.
//
// A single call sweeps the full PEL by following the XAUTOCLAIM cursor
// until it returns to "0-0", so a burst of stuck messages drains in one
// tick rather than one-batch-per-30s. Per-call safety caps keep any
// single sweep bounded when the PEL is pathologically large.
//
// Note: ReclaimStale does NOT ACK messages in the returned deadLetter
// slice. The caller owns final disposition and must ACK or NACK each
// dead-letter message explicitly — otherwise the same messages will be
// reclaimed again on the next XAUTOCLAIM sweep.
func (c *Consumer) ReclaimStale(ctx context.Context, jobID string) (reclaimed []StreamMessage, deadLetter []StreamMessage, err error) {
	streamKey := StreamKey(jobID)
	groupName := ConsumerGroup(jobID)

	// perCallCount bounds a single XAUTOCLAIM RTT. maxMessagesPerSweep
	// caps work per tick so one pathological job cannot starve the other
	// jobs the reclaim loop still has to scan. Both are operator dials
	// — see REDIS_AUTOCLAIM_COUNT and REDIS_AUTOCLAIM_MAX_PER_SWEEP.
	perCallCount := c.opts.AutoclaimCount
	if perCallCount <= 0 {
		perCallCount = 100
	}
	maxMessagesPerSweep := c.opts.AutoclaimMaxPerSweep
	if maxMessagesPerSweep <= 0 {
		maxMessagesPerSweep = 1000
	}

	cursor := "0-0"
	totalSeen := 0
	// iterationCap guards against an unlikely infinite loop where Redis
	// returns a non-terminal cursor with zero messages indefinitely. A
	// legitimate walk does at most ceil(maxMessagesPerSweep / perCallCount)
	// productive iterations; we allow 3× that to absorb a few empty-batch
	// gaps XAUTOCLAIM can produce when candidate messages fail the idle
	// filter mid-walk (see coderabbit review on PR #338).
	iterationCap := 0
	if perCallCount > 0 {
		iterationCap = int((int64(maxMessagesPerSweep)+perCallCount-1)/perCallCount) * 3
	}
	if iterationCap < 10 {
		iterationCap = 10
	}
	iterations := 0

	for {
		msgs, next, claimErr := c.client.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   streamKey,
			Group:    groupName,
			Consumer: c.opts.ConsumerName,
			MinIdle:  c.opts.MinIdleTime,
			Start:    cursor,
			Count:    perCallCount,
		}).Result()
		if claimErr != nil {
			if isNoGroupErr(claimErr) {
				return nil, nil, nil
			}
			return nil, nil, fmt.Errorf("broker: XAUTOCLAIM %s: %w", jobID, claimErr)
		}

		// Classify the batch in one XPENDING round-trip instead of one
		// round-trip per message. XAUTOCLAIM returns the messages in
		// stream-id order, so their first/last IDs form a contiguous range
		// XPENDING can answer in a single call.
		delivered, parsed := c.classifyBatch(ctx, jobID, streamKey, groupName, msgs)
		for _, entry := range parsed {
			if entry.unparseable {
				continue
			}
			if delivered[entry.msg.MessageID] >= c.opts.MaxDeliveries {
				deadLetter = append(deadLetter, entry.msg)
			} else {
				reclaimed = append(reclaimed, entry.msg)
			}
		}

		totalSeen += len(msgs)
		iterations++

		// Redis signals end-of-walk with "0-0" (or empty) as the next
		// cursor. Empty msgs batches mid-walk are legal — XAUTOCLAIM can
		// return zero claimed entries when candidates in the range have
		// dropped below the idle threshold between scan and claim — so we
		// must NOT break on len(msgs) == 0 alone (coderabbit PR #338).
		if next == "0-0" || next == "" {
			break
		}
		if totalSeen >= maxMessagesPerSweep {
			brokerLog.Warn("reclaim sweep hit per-tick cap; will resume next tick",
				"job_id", jobID, "cap", maxMessagesPerSweep)
			break
		}
		if iterations >= iterationCap {
			brokerLog.Warn("reclaim sweep hit iteration cap; will resume next tick",
				"job_id", jobID, "iterations", iterations, "seen", totalSeen)
			break
		}
		cursor = next
	}

	observability.RecordBrokerAutoclaim(ctx, jobID, "reclaimed", len(reclaimed))
	observability.RecordBrokerAutoclaim(ctx, jobID, "dead_letter", len(deadLetter))

	return reclaimed, deadLetter, nil
}

// classifiedMessage carries both the parsed message and a flag indicating
// it was already ACKed as unparseable.
type classifiedMessage struct {
	msg         StreamMessage
	unparseable bool
}

// classifyBatch parses the raw XAUTOCLAIM batch and fetches delivery
// counts for every parseable message in a single XPENDING range call.
// Returns a map of message ID → delivery count and the parsed messages
// in the same order as the input.
//
// Pre-batching, the reclaim loop issued one XPENDING per message, which
// at Count=100 meant 100 extra Redis RTTs per sweep. For large stuck
// PELs this dominated tick time and starved other jobs — coderabbit
// flagged this on PR #338.
func (c *Consumer) classifyBatch(
	ctx context.Context,
	jobID, streamKey, groupName string,
	msgs []redis.XMessage,
) (map[string]int64, []classifiedMessage) {
	parsed := make([]classifiedMessage, 0, len(msgs))
	for _, xMsg := range msgs {
		m, parseErr := parseStreamMessage(xMsg)
		if parseErr != nil {
			brokerLog.Warn("dead-lettering unparseable reclaimed message",
				"error", parseErr, "message_id", xMsg.ID, "consumer", c.opts.ConsumerName)
			_ = c.Ack(ctx, jobID, xMsg.ID)
			parsed = append(parsed, classifiedMessage{unparseable: true})
			continue
		}
		parsed = append(parsed, classifiedMessage{msg: m})
	}

	// Nothing to classify — avoid the XPENDING RTT on an empty batch.
	var firstID, lastID string
	any := false
	for _, p := range parsed {
		if p.unparseable {
			continue
		}
		if !any {
			firstID, lastID = p.msg.MessageID, p.msg.MessageID
			any = true
			continue
		}
		// XAUTOCLAIM preserves stream-ID order, so first and last form
		// the enclosing range for the XPENDING lookup.
		lastID = p.msg.MessageID
	}
	delivered := make(map[string]int64, len(parsed))
	if !any {
		return delivered, parsed
	}

	// Request 2× len(parsed) so interleaved entries belonging to other
	// consumers (fresh, not-yet-idle PEL entries that sit between our
	// claimed IDs) don't push any of our IDs out of the page. XAUTOCLAIM
	// only reassigns messages whose idle >= MinIdleTime, but XPENDING
	// lists ALL pending entries in the range regardless of consumer or
	// idle time, so the range can be sparser than the claimed slice.
	pending, err := c.client.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: streamKey,
		Group:  groupName,
		Start:  firstID,
		End:    lastID,
		Count:  int64(len(parsed)) * 2,
	}).Result()
	if err != nil {
		// Treat as reclaimable — the caller's fallback path preserves
		// the pre-batch safety net (over-count briefly rather than drop
		// work). A delivery count of 0 maps to reclaimed, not dead-letter.
		brokerLog.Warn("batched delivery-count lookup failed; treating batch as reclaimable",
			"error", err, "job_id", jobID, "batch_size", len(parsed))
		return delivered, parsed
	}
	for _, p := range pending {
		delivered[p.ID] = p.RetryCount
	}

	// Any claimed ID missing from the batched response (because the
	// range was too sparse even for the 2× budget, or because Redis
	// returned an unexpectedly small page) must NOT silently default
	// to delivery=0 — that would bypass the max-delivery gate and let
	// a stuck message loop forever. Fall back to a per-ID XPENDING for
	// stragglers so classification is never lossy (coderabbit PR #338).
	for _, p := range parsed {
		if p.unparseable {
			continue
		}
		if _, ok := delivered[p.msg.MessageID]; ok {
			continue
		}
		single, perErr := c.client.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
			Stream: streamKey,
			Group:  groupName,
			Start:  p.msg.MessageID,
			End:    p.msg.MessageID,
			Count:  1,
		}).Result()
		if perErr != nil {
			brokerLog.Warn("per-id delivery-count fallback failed; treating message as reclaimable",
				"error", perErr, "job_id", jobID, "message_id", p.msg.MessageID)
			continue
		}
		if len(single) == 0 {
			// Message has already been ACKed by another path — nothing
			// to classify. Leaving it absent from delivered is safe because
			// the caller only classifies parsed entries it still holds.
			continue
		}
		delivered[single[0].ID] = single[0].RetryCount
	}
	return delivered, parsed
}

// (getDeliveryCount was removed when the reclaim path was batched — the
// single-message path was its only caller. Use XPendingExt directly with
// Start=End=messageID if a single-message lookup is ever needed again.)

// PendingCount returns the number of messages in the pending entries
// list (PEL) for a job's consumer group — i.e. tasks that have been
// delivered to a worker but not yet ACKed. This is the authoritative
// source of "currently running" for a given job; the RunningCounters
// HASH in Redis is a fast-path mirror that can drift under partial
// failures. Returns 0 when the stream or group does not yet exist.
func (c *Consumer) PendingCount(ctx context.Context, jobID string) (int64, error) {
	streamKey := StreamKey(jobID)
	groupName := ConsumerGroup(jobID)

	summary, err := c.client.rdb.XPending(ctx, streamKey, groupName).Result()
	if err != nil {
		if isNoGroupErr(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("broker: XPENDING %s: %w", jobID, err)
	}
	if summary == nil {
		return 0, nil
	}
	return summary.Count, nil
}

// isNoGroupErr returns true when Redis reports that the stream or
// consumer group doesn't exist. This is expected before the
// dispatcher first XADDs to a job's stream.
func isNoGroupErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "NOGROUP") || strings.Contains(s, "no such key")
}

// --- parsing ---

func parseStreamMessage(xMsg redis.XMessage) (StreamMessage, error) {
	get := func(key string) string {
		v, _ := xMsg.Values[key].(string)
		return v
	}

	// Validate required string fields.
	for _, key := range []string{"task_id", "job_id", "host", "path"} {
		if get(key) == "" {
			return StreamMessage{}, fmt.Errorf("missing required field: %s", key)
		}
	}

	pageID, err := strconv.Atoi(get("page_id"))
	if err != nil {
		return StreamMessage{}, fmt.Errorf("bad page_id: %w", err)
	}
	priority, err := strconv.ParseFloat(get("priority"), 64)
	if err != nil {
		return StreamMessage{}, fmt.Errorf("bad priority: %w", err)
	}
	retryCount, err := strconv.Atoi(get("retry_count"))
	if err != nil {
		return StreamMessage{}, fmt.Errorf("bad retry_count: %w", err)
	}

	return StreamMessage{
		MessageID:  xMsg.ID,
		TaskID:     get("task_id"),
		JobID:      get("job_id"),
		PageID:     pageID,
		Host:       get("host"),
		Path:       get("path"),
		Priority:   priority,
		RetryCount: retryCount,
		SourceType: get("source_type"),
		SourceURL:  get("source_url"),
	}, nil
}
