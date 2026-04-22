package broker

import "github.com/redis/go-redis/v9"

// Lua scripts for atomic domain pacing operations. Using scripts
// avoids race conditions between read-modify-write cycles that would
// occur with separate GET/SET calls across multiple workers.

// adaptiveDelayOnSuccessScript atomically:
// 1. Increments success_streak
// 2. Resets error_streak to 0
// 3. If success_streak >= threshold, decreases adaptive_delay_ms by step (min = floor)
//
// KEYS[1] = hover:dom:cfg:{domain}
// ARGV[1] = success threshold (e.g. 5)
// ARGV[2] = step down ms (e.g. 500)
//
// Returns the new adaptive_delay_ms.
var adaptiveDelayOnSuccessScript = redis.NewScript(`
local key = KEYS[1]
local threshold = tonumber(ARGV[1])
local step = tonumber(ARGV[2])

redis.call('HINCRBY', key, 'success_streak', 1)
redis.call('HSET', key, 'error_streak', '0')

local streak = tonumber(redis.call('HGET', key, 'success_streak') or '0')
local delay = tonumber(redis.call('HGET', key, 'adaptive_delay_ms') or '0')
local floor = tonumber(redis.call('HGET', key, 'floor_ms') or '0')

if streak >= threshold and delay > floor then
    delay = math.max(floor, delay - step)
    redis.call('HSET', key, 'adaptive_delay_ms', tostring(delay))
    redis.call('HSET', key, 'success_streak', '0')
end

redis.call('EXPIRE', key, 86400)
return delay
`)

// tryAcquireScript atomically reads the effective per-domain delay from the
// config hash and attempts to set the domain gate in one round-trip. This
// replaces the pre-existing three-call sequence (HMGET + SET NX PX + PTTL)
// which serialised the dispatcher at ~2-3 RTTs per task — under a 100-job
// workload the cumulative RTT cost was the dominant throughput ceiling.
//
// KEYS[1] = hover:dom:cfg:{domain}
// KEYS[2] = hover:dom:gate:{domain}
//
// Returns {acquired, delay_ms, ttl_ms}:
//   - acquired = 1 if the gate was set (or delay was zero); 0 if already held
//   - delay_ms = effective delay applied (max(base, adaptive))
//   - ttl_ms   = remaining gate TTL when not acquired; 0 when acquired
var tryAcquireScript = redis.NewScript(`
local cfgKey = KEYS[1]
local gateKey = KEYS[2]

local base = tonumber(redis.call('HGET', cfgKey, 'base_delay_ms') or '0') or 0
local adaptive = tonumber(redis.call('HGET', cfgKey, 'adaptive_delay_ms') or '0') or 0
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

// adaptiveDelayOnErrorScript atomically:
// 1. Increments error_streak
// 2. Resets success_streak to 0
// 3. Increases adaptive_delay_ms by step (capped at max)
//
// KEYS[1] = hover:dom:cfg:{domain}
// ARGV[1] = step up ms (e.g. 500)
// ARGV[2] = max delay ms (e.g. 60000)
//
// Returns the new adaptive_delay_ms.
var adaptiveDelayOnErrorScript = redis.NewScript(`
local key = KEYS[1]
local step = tonumber(ARGV[1])
local maxDelay = tonumber(ARGV[2])

redis.call('HINCRBY', key, 'error_streak', 1)
redis.call('HSET', key, 'success_streak', '0')

local delay = tonumber(redis.call('HGET', key, 'adaptive_delay_ms') or '0')
delay = math.min(maxDelay, delay + step)
redis.call('HSET', key, 'adaptive_delay_ms', tostring(delay))

redis.call('EXPIRE', key, 86400)
return delay
`)
