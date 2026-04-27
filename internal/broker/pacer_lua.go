package broker

import "github.com/redis/go-redis/v9"

// Lua scripts: atomic read-modify-write for per-domain pacing across
// multiple workers.

// KEYS[1] = hover:dom:cfg:{domain}
// ARGV[1] = success threshold; ARGV[2] = step down ms
// Returns the new adaptive_delay_ms.
var adaptiveDelayOnSuccessScript = redis.NewScript(`
local key = KEYS[1]
local threshold = tonumber(ARGV[1])
local step = tonumber(ARGV[2])

redis.call('HINCRBY', key, 'success_streak', 1)

-- One HMGET replaces three HGETs against the same hash; the post-
-- HINCRBY read is intentional so the streak value reflects this call.
local fields = redis.call('HMGET', key, 'success_streak', 'adaptive_delay_ms', 'floor_ms')
local streak = tonumber(fields[1] or '0') or 0
local delay = tonumber(fields[2] or '0') or 0
local floor = tonumber(fields[3] or '0') or 0

if streak >= threshold and delay > floor then
    delay = math.max(floor, delay - step)
    -- Single HMSET batches the three writes (error_streak reset, new
    -- adaptive delay, success_streak reset) that previously ran as
    -- separate HSETs.
    redis.call('HMSET', key,
        'error_streak', '0',
        'adaptive_delay_ms', tostring(delay),
        'success_streak', '0')
else
    redis.call('HSET', key, 'error_streak', '0')
end

redis.call('EXPIRE', key, 86400)
return delay
`)

// KEYS[1] = hover:dom:cfg:{domain}; KEYS[2] = hover:dom:gate:{domain}
// Returns {acquired, delay_ms, ttl_ms}. Replaces a 3-call sequence
// (HMGET + SET NX PX + PTTL) that was the dispatcher's RTT bottleneck
// under 100-job workloads.
var tryAcquireScript = redis.NewScript(`
local cfgKey = KEYS[1]
local gateKey = KEYS[2]

-- One HMGET replaces two HGETs against cfgKey. tryAcquire is on the
-- dispatch hot path; halving the per-call command count meaningfully
-- shrinks the Upstash command bill.
local cfg = redis.call('HMGET', cfgKey, 'base_delay_ms', 'adaptive_delay_ms')
local base = tonumber(cfg[1] or '0') or 0
local adaptive = tonumber(cfg[2] or '0') or 0
local delay = base
if adaptive > delay then
    delay = adaptive
end

if delay <= 0 then
    return {1, 0, 0}
end

local ok = redis.call('SET', gateKey, '1', 'NX', 'PX', delay)
if ok then
    return {1, delay, 0}
end

local ttl = redis.call('PTTL', gateKey)
if ttl < 0 then
    ttl = 0
end
return {0, delay, ttl}
`)

// KEYS[1] = hover:dom:cfg:{domain}
// ARGV[1] = step up ms; ARGV[2] = max delay ms
// Returns the new adaptive_delay_ms.
var adaptiveDelayOnErrorScript = redis.NewScript(`
local key = KEYS[1]
local step = tonumber(ARGV[1])
local maxDelay = tonumber(ARGV[2])

redis.call('HINCRBY', key, 'error_streak', 1)

local delay = tonumber(redis.call('HGET', key, 'adaptive_delay_ms') or '0')
delay = math.min(maxDelay, delay + step)
-- HMSET coalesces the success_streak reset and the new adaptive delay
-- into a single write; the previous code issued these as two HSETs.
redis.call('HMSET', key,
    'success_streak', '0',
    'adaptive_delay_ms', tostring(delay))

redis.call('EXPIRE', key, 86400)
return delay
`)
