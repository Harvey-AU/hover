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
