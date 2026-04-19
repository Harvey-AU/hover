# Redis Broker Operations Runbook

Status: draft (Stage 3 cutover). Last reviewed: 2026-04-19.

This runbook covers the day-two operations of the Redis-backed task broker
(`internal/broker/`) introduced in PR #330. It assumes familiarity with the
architecture described in `docs/architecture/ARCHITECTURE.md`.

## Redis key map

| Key                        | Type   | Purpose                                  | Owner         |
| -------------------------- | ------ | ---------------------------------------- | ------------- |
| `sched:{jobID}`            | ZSET   | Scheduled tasks (score = run-at unix ms) | Scheduler     |
| `stream:{jobID}`           | STREAM | Dispatched tasks ready for worker pickup | Dispatcher    |
| `grp:{jobID}`              | GROUP  | Consumer group for `stream:{jobID}`      | Consumer      |
| `running:{jobID}`          | STRING | Atomic running-task counter              | RunningCounts |
| `domain:cfg:{domain}`      | HASH   | Base/adaptive delay config per domain    | DomainPacer   |
| `domain:gate:{domain}`     | STRING | TryAcquire time-gate (SET NX PX)         | DomainPacer   |
| `domain:inflight:{domain}` | HASH   | Per-job inflight counters for the domain | DomainPacer   |

Consumer names are `worker-{machineID}-{goroutineID}`.

## Common operations

### Inspect a job

```
# How many tasks are scheduled but not yet dispatched?
ZCARD sched:{jobID}

# How many tasks are in the stream, waiting for a worker?
XLEN stream:{jobID}

# How many tasks have been delivered but not yet ACKed (in-flight + stuck)?
XPENDING stream:{jobID} grp:{jobID}

# Current running counter.
GET running:{jobID}
```

### Cancel a job (drain all in-flight work)

```
# 1. Mark job as cancelled in Postgres (normal job cancel flow).
# 2. Purge scheduled tasks.
DEL sched:{jobID}
# 3. Trim the stream. Workers mid-execute will still finish; their ACKs are no-ops.
XTRIM stream:{jobID} MAXLEN 0
# 4. Optional: drop the consumer group (orphans any unacked messages).
XGROUP DESTROY stream:{jobID} grp:{jobID}
# 5. Reset the running counter.
DEL running:{jobID}
```

Note: the application `CancelJob` path (`JobManager.CancelJob`) handles steps
1-3 already; only reach for manual commands if the job is stuck in a weird
state.

### Inspect stuck messages (PEL)

```
# Show up to 10 pending entries with consumer + idle-ms.
XPENDING stream:{jobID} grp:{jobID} IDLE 0 - + 10
```

If a message has been pending longer than `REDIS_CONSUMER_MIN_IDLE_MS` (default
180_000 = 3 min), XAUTOCLAIM should reclaim it on the next sweep. If it keeps
getting reclaimed without acking, check delivery count:

```
XPENDING stream:{jobID} grp:{jobID} - + 10
# delivery_count column >= 3 → dead-letter candidate
```

### Dead-letter handling

Currently: messages with delivery count ≥ `MaxDeliveries` (default 3) are
returned from `Consumer.ReclaimStale` in the `deadLetter` slice. The caller
(`StreamWorkerPool`) is responsible for marking the task as failed in Postgres
and ACKing the message. If dead-letter messages are piling up it means the
worker is crashing before ACK.

Signal to watch: `XPENDING stream:{jobID} grp:{jobID}` count over time. Steady
growth = workers not acking (either crashing or a poison message).

### Reset a drifted running counter

If `running:{jobID}` diverges from reality (e.g. worker OOM mid-ack):

```
# Count actual in-flight from the stream PEL.
XPENDING stream:{jobID} grp:{jobID}
# Set counter manually.
SET running:{jobID} <actual_count>
```

A periodic reconciler job would remove the need for this. See observability gap
#8.

### Inspect domain pacing state

```
HGETALL domain:cfg:{domain}
# Current time-gate (ttl is the remaining wait).
PTTL domain:gate:{domain}
# Per-job inflight.
HGETALL domain:inflight:{domain}
```

### Drain workers for deploy

Workers exit cleanly on SIGTERM:

1. Stop reading new messages from the stream.
2. Finish any in-flight task and ACK.
3. Decrement counters and pacer inflight.

Fly deploys send SIGTERM with a grace period. Set `WORKER_SHUTDOWN_GRACE_S=120`
(or similar) to give slow tasks time to finish. Messages still in the PEL after
grace will be reclaimed by another worker via XAUTOCLAIM.

## Failure modes

### Redis unavailable

- **API server:** `REDIS_URL` optional. Without Redis, task dispatch is disabled
  and jobs stay in Postgres. Safe degradation.
- **Worker:** hard-fails on startup if `REDIS_URL` is unreachable. Fly health
  check will mark the instance unhealthy.

Action: restart Upstash instance, verify `flyctl redis status`, redeploy worker.
No data loss (tasks persist in Postgres).

### Stream grows without bound

If `XLEN stream:{jobID}` keeps growing and workers aren't consuming:

1. Check worker logs for XREADGROUP errors.
2. Check `ACTIVE_JOBS` limit — is the job in the active set?
3. Check consumer count (`XINFO CONSUMERS stream:{jobID} grp:{jobID}`).

### Dispatcher not dispatching

Signs: ZSET has items with past `RunAt` but stream stays empty.

1. Is the dispatcher process running? Check worker service logs for
   `dispatcher started`.
2. Is the job in `ActiveJobIDs`? The dispatcher only scans active jobs.
3. Is concurrency maxed out? `GET running:{jobID}` vs job concurrency.
4. Is domain pacing blocking everything? `PTTL domain:gate:{domain}`.

### Poison message loop

A malformed message that can't be parsed is ACKed by the consumer to break the
loop (see `Read` / `ReadNonBlocking`). Malformed ZSET entries are ZREM'd by
`DueItems`. If you still see a loop, check logs for a specific `error` pattern
and verify the cleanup path reached the bad entry.

## Environment variables

| Variable                       | Default | Description                                 |
| ------------------------------ | ------- | ------------------------------------------- |
| `REDIS_URL`                    | -       | Redis connection string (rediss:// for TLS) |
| `REDIS_TLS_ENABLED`            | false   | Toggle TLS; must match URL scheme           |
| `REDIS_DISPATCH_INTERVAL_MS`   | 100     | Dispatcher scan interval                    |
| `REDIS_DISPATCH_BATCH_SIZE`    | 50      | Max ZSET entries per dispatch tick per job  |
| `REDIS_CONSUMER_BLOCK_MS`      | 2000    | XREADGROUP block duration                   |
| `REDIS_AUTOCLAIM_INTERVAL_S`   | 30      | XAUTOCLAIM sweep interval                   |
| `STREAM_ACTIVE_JOBS_LIMIT`     | 200     | Max active jobs tracked by the worker pool  |
| `GNH_RATE_LIMIT_DELAY_STEP_MS` | 500     | Adaptive delay step per adjustment          |
| `GNH_RATE_LIMIT_MAX_DELAY_MS`  | 60000   | Adaptive delay ceiling                      |

See `docs/operations/ENV_VARS.md` for the authoritative list.
