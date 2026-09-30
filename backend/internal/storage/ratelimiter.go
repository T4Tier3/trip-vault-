package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RateLimiter is a fixed-window rate limiter backed by Redis.
//
// Fixed windows are chosen over a sliding log because the limiter runs on the
// hot path for every request: a single atomic INCR plus one conditional
// EXPIRE is one round trip, where a sliding log needs a sorted set per request.
// The trade-off is that a client can send up to 2x the limit across a window
// boundary, which is acceptable for abuse control here.
type RateLimiter struct {
	client *redis.Client
	prefix string
}

// NewRateLimiter builds a limiter over the given Redis client.
func NewRateLimiter(client *redis.Client, prefix string) *RateLimiter {
	return &RateLimiter{client: client, prefix: prefix}
}

// LimitResult describes the outcome of one rate limit check.
type LimitResult struct {
	// Allowed reports whether the request may proceed.
	Allowed bool
	// Limit is the configured maximum for the window.
	Limit int
	// Remaining is how many requests are left in the current window.
	Remaining int
	// RetryAfter is how long the client should wait before retrying. It is zero
	// when the request was allowed.
	RetryAfter time.Duration
	// ResetAt is when the current window ends.
	ResetAt time.Time
}

// windowKey is the Redis key for a bucket. Buckets are aligned to wall-clock
// windows so every replica of the API shares the same boundaries.
func windowKey(bucket string) string {
	return bucket + ":window"
}

// Allow consumes one unit from the caller's bucket and reports whether the
// request is within the limit.
//
// The INCR and EXPIRE are issued together. The EXPIRE uses NX so the TTL is set
// only on the increment that created the bucket, which keeps a busy client's
// window from sliding forward forever.
func (r *RateLimiter) Allow(ctx context.Context, bucket string, limit int, window time.Duration) (LimitResult, error) {
	if limit <= 0 {
		return LimitResult{Allowed: true, Limit: limit}, nil
	}

	key := windowKey(r.prefix + ":" + bucket)
	// window is already a time.Duration, so it is passed to Redis as-is.
	// Multiplying by time.Second here would scale 60s by 1e9 and produce a
	// multi-century TTL.
	windowSeconds := int64(window / time.Second)
	if windowSeconds < 1 {
		windowSeconds = 1
	}

	// Pipeline both commands in one round trip. A pipeline is fine here: even
	// if the INCR does not run, the EXPIRE simply expires a nonexistent key.
	pipe := r.client.Pipeline()
	incr := pipe.Incr(ctx, key)
	pipe.ExpireNX(ctx, key, window)

	if _, err := pipe.Exec(ctx); err != nil {
		return LimitResult{}, fmt.Errorf("rate limit %s: %w", bucket, err)
	}

	count := incr.Val()
	ttl, err := r.client.TTL(ctx, key).Result()
	if err != nil && err != redis.Nil {
		return LimitResult{}, fmt.Errorf("rate limit ttl %s: %w", bucket, err)
	}
	if ttl < 0 {
		ttl = window
	}

	result := LimitResult{
		Limit:      limit,
		ResetAt:    time.Now().Add(ttl),
		RetryAfter: ttl,
	}

	if int(count) > limit {
		result.Allowed = false
		result.Remaining = 0
		return result, nil
	}

	result.Allowed = true
	result.Remaining = limit - int(count)
	return result, nil
}

// Peek reports the current state of a bucket without consuming a unit.
func (r *RateLimiter) Peek(ctx context.Context, bucket string) (int64, time.Duration, error) {
	key := windowKey(r.prefix + ":" + bucket)
	count, err := r.client.Get(ctx, key).Int64()
	if err != nil {
		if err == redis.Nil {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("rate limit peek %s: %w", bucket, err)
	}
	ttl, err := r.client.TTL(ctx, key).Result()
	if err != nil && err != redis.Nil {
		return 0, 0, fmt.Errorf("rate limit peek ttl %s: %w", bucket, err)
	}
	if ttl < 0 {
		ttl = 0
	}
	return count, ttl, nil
}

// Reset clears a bucket, used by tests and by administrative unlock flows.
func (r *RateLimiter) Reset(ctx context.Context, bucket string) error {
	if err := r.client.Del(ctx, windowKey(r.prefix+":"+bucket)).Err(); err != nil {
		return fmt.Errorf("rate limit reset %s: %w", bucket, err)
	}
	return nil
}

// ParseRetryAfterSeconds formats a duration for the Retry-After header, which
// requires whole seconds and at least 1.
func ParseRetryAfterSeconds(d time.Duration) int {
	seconds := int(d.Round(time.Second) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}

// FormatResetAt renders a window reset time as a Unix timestamp for headers.
func FormatResetAt(t time.Time) int64 {
	return t.Unix()
}
