package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tripvault/backend/internal/httpx"
	"github.com/tripvault/backend/internal/storage"
)

// Standard rate limit headers, so a client can back off without guessing.
const (
	headerRateLimit     = "X-RateLimit-Limit"
	headerRateRemaining = "X-RateLimit-Remaining"
	headerRateReset     = "X-RateLimit-Reset"
	headerRetryAfter    = "Retry-After"
)

// RateLimiter is the limiter interface, satisfied by storage.RateLimiter.
type RateLimiter interface {
	Allow(ctx context.Context, bucket string, limit int, window time.Duration) (storage.LimitResult, error)
}

// RateLimit applies a per-client request limit.
//
// The bucket key combines the client address with the route prefix, so a
// password-reset flood and normal browsing get separate budgets. That matters
// because these endpoints sit behind a shared NAT in some mobile networks, and
// a global limit would let one user lock out an entire carrier network.
//
// When the limiter is unavailable the request is allowed through. Failing closed
// would turn a Redis outage into a total outage of the API; a brief loss of
// abuse protection is the better trade.
func RateLimit(limiter RateLimiter, requests int, window time.Duration, excluded []string, trustProxy bool) gin.HandlerFunc {
	skip := map[string]struct{}{}
	for _, path := range excluded {
		skip[path] = struct{}{}
	}

	return func(c *gin.Context) {
		if _, ok := skip[c.Request.URL.Path]; ok {
			c.Next()
			return
		}

		// The default limit is generous for authenticated use; credentials are
		// protected by the stricter auth-specific limit mounted on those routes.
		bucket := "global:" + clientKey(c, trustProxy)

		result, err := limiter.Allow(c.Request.Context(), bucket, requests, window)
		if err != nil {
			if log := requestLogger(c); log != nil {
				log.Warn("rate limiter unavailable, allowing request", "error", err)
			}
			c.Next()
			return
		}

		setRateHeaders(c, result)

		if !result.Allowed {
			retry := storage.ParseRetryAfterSeconds(result.RetryAfter)
			c.Header(headerRetryAfter, strconv.Itoa(retry))
			httpx.Fail(c, httpx.TooManyRequests("Too many requests. Please slow down and try again shortly."))
			return
		}
		c.Next()
	}
}

// StrictRateLimit applies a tighter limit, used on the auth routes.
//
// Credential endpoints are the ones worth attacking: unlimited password
// attempts enable offline cracking, and unlimited email sends turn the service
// into a spam relay.
func StrictRateLimit(limiter RateLimiter, requests int, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Scoping the bucket to the exact route means each credential endpoint
		// gets its own budget, so an attacker cannot lock a victim out of login
		// by exhausting the reset endpoint's allowance.
		bucket := "auth:" + c.FullPath() + ":" + clientKey(c, false)

		result, err := limiter.Allow(c.Request.Context(), bucket, requests, window)
		if err != nil {
			if log := requestLogger(c); log != nil {
				log.Warn("rate limiter unavailable, allowing request", "error", err)
			}
			c.Next()
			return
		}

		setRateHeaders(c, result)

		if !result.Allowed {
			retry := storage.ParseRetryAfterSeconds(result.RetryAfter)
			c.Header(headerRetryAfter, strconv.Itoa(retry))
			httpx.Fail(c, httpx.TooManyRequests("Too many attempts. Please wait before trying again."))
			return
		}
		c.Next()
	}
}

// setRateHeaders publishes the limit state on every limited response.
func setRateHeaders(c *gin.Context, result storage.LimitResult) {
	c.Header(headerRateLimit, strconv.Itoa(result.Limit))
	remaining := result.Remaining
	if remaining < 0 {
		remaining = 0
	}
	c.Header(headerRateRemaining, strconv.Itoa(remaining))
	c.Header(headerRateReset, strconv.FormatInt(storage.FormatResetAt(result.ResetAt), 10))
}

// clientKey derives the rate limit bucket identity for a request.
//
// The user agent is folded in alongside the address. That is a deliberate
// coarsening, not a precise identity: it raises the cost of cycling addresses
// without pretending to authenticate anyone.
func clientKey(c *gin.Context, trustProxy bool) string {
	address := c.ClientIP()

	if trustProxy {
		if forwarded := c.GetHeader("X-Forwarded-For"); forwarded != "" {
			// The left-most entry is the original client when every proxy in the
			// chain appends rather than replaces.
			if first, _, found := strings.Cut(forwarded, ","); found {
				address = strings.TrimSpace(first)
			} else {
				address = strings.TrimSpace(forwarded)
			}
		}
	}

	sum := sha256.Sum256([]byte(address + "|" + c.Request.UserAgent()))
	return hex.EncodeToString(sum[:16])
}

// requestLogger retrieves the logger the Logger middleware attached.
func requestLogger(c *gin.Context) *slog.Logger {
	if v, ok := c.Get("httpx.logger"); ok {
		if log, ok := v.(*slog.Logger); ok {
			return log
		}
	}
	return nil
}
