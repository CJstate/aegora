package ratelimit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// slidingWindowLuaScript is the Lua script executed atomically on Redis.
// It queries Redis TIME to prevent gateway replica clock skew, prunes expired
// timestamps from the sorted set, checks capacity, and conditionally adds the new request.
const slidingWindowLuaScript = `
local key = KEYS[1]
local window_ms = tonumber(ARGV[1])
local max_requests = tonumber(ARGV[2])
local unique_suffix = ARGV[3]

local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local clear_before = now_ms - window_ms

redis.call('ZREMRANGEBYSCORE', key, '-inf', clear_before)

local current_requests = redis.call('ZCARD', key)
if current_requests < max_requests then
	local member = tostring(now_ms) .. ':' .. unique_suffix
	redis.call('ZADD', key, now_ms, member)
	redis.call('PEXPIRE', key, window_ms)
	return 1
else
	return 0
end
`

// RedisLimiter implements a distributed sliding-window rate limiter backed by Redis.
// It executes an atomic Lua script using server time from Redis (TIME) to prevent clock
// skew across distributed gateway replicas.
type RedisLimiter struct {
	client      *redis.Client
	keyPrefix   string
	window      time.Duration
	maxRequests int
	failOpen    bool
	script      *redis.Script
}

// NewRedisLimiter constructs a Redis-backed distributed rate limiter.
// By default, keyPrefix is "aegora:rl" and failOpen is true.
// It panics if window or maxRequests is non-positive.
func NewRedisLimiter(client *redis.Client, window time.Duration, maxRequests int) *RedisLimiter {
	if window <= 0 {
		panic("ratelimit: window must be positive")
	}
	if maxRequests <= 0 {
		panic("ratelimit: maxRequests must be positive")
	}

	return &RedisLimiter{
		client:      client,
		keyPrefix:   "aegora:rl",
		window:      window,
		maxRequests: maxRequests,
		failOpen:    true,
		script:      redis.NewScript(slidingWindowLuaScript),
	}
}

// AllowFor reports whether a request is permitted for the specified key within the current window.
// It executes a single atomic Lua script against the namespaced Redis key.
func (l *RedisLimiter) AllowFor(ctx context.Context, key string) (bool, error) {
	redisKey := l.keyPrefix + ":" + key
	windowMS := l.window.Milliseconds()
	if windowMS <= 0 {
		windowMS = 1
	}

	suffix := generateRandomSuffix()

	res, err := l.script.Run(ctx, l.client, []string{redisKey}, windowMS, l.maxRequests, suffix).Int()
	if err != nil {
		return false, err
	}

	return res == 1, nil
}

// ForKey returns a keyless Limiter bound to the specified key.
// On Redis error it fails open (allows the request) per the failOpen flag.
//
// Fail-Open Rationale:
// Fail-open is the default strategy for distributed rate limiting in Aegora.
// A dead, unreachable, or degraded Redis cluster must never take down the gateway
// or block incoming user traffic. The accepted operational tradeoff is that rate
// limits are unenforced during an outage in order to preserve service availability.
func (l *RedisLimiter) ForKey(key string) Limiter {
	return &redisBoundLimiter{
		limiter: l,
		key:     key,
	}
}

type redisBoundLimiter struct {
	limiter *RedisLimiter
	key     string
}

var _ Limiter = (*redisBoundLimiter)(nil)

// Allow reports whether a request is permitted, failing open if configured and Redis errors.
func (r *redisBoundLimiter) Allow(ctx context.Context) bool {
	allowed, err := r.limiter.AllowFor(ctx, r.key)
	if err != nil {
		return r.limiter.failOpen
	}
	return allowed
}

// Limit returns the configured maximum request capacity.
func (r *redisBoundLimiter) Limit() int {
	return r.limiter.maxRequests
}

func generateRandomSuffix() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}
