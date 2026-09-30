package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tripvault/backend/internal/config"
	"github.com/tripvault/backend/internal/httpx"
)

// CORS applies cross-origin rules.
//
// TripVault is a mobile API, so no browser origin needs access. The default
// policy therefore allows nothing, and an operator who genuinely needs a web
// client has to name the origins explicitly in ALLOWED_ORIGINS. Denying by
// default is the only choice that cannot be forgotten in a way that matters.
func CORS(cfg config.App, allowed []string) gin.HandlerFunc {
	// An empty map means "no origin allowed"; a nil map would match everything.
	permitted := map[string]struct{}{}
	for _, origin := range allowed {
		if trimmed := strings.TrimSpace(origin); trimmed != "" {
			permitted[trimmed] = struct{}{}
		}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			// Not a cross-origin request.
			c.Next()
			return
		}

		_, allowedHere := permitted[origin]
		if _, wildcard := permitted["*"]; wildcard || allowedHere {
			if _, wildcard := permitted["*"]; wildcard {
				c.Header("Access-Control-Allow-Origin", "*")
			} else {
				// Echo the specific origin rather than "*" so credentialed
				// requests remain possible.
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Access-Control-Allow-Credentials", "true")
			}
			c.Header("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID, X-Device-ID")
			c.Header("Access-Control-Expose-Headers", "X-Request-ID, "+headerRateLimit+", "+headerRateRemaining+", "+headerRateReset+", "+headerRetryAfter)
			c.Header("Access-Control-Max-Age", strconv.Itoa(int((12 * time.Hour).Seconds())))
			c.Header("Vary", "Origin")
		}

		if c.Request.Method == http.MethodOptions {
			// Preflight is answered here; the route never runs.
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// SecurityHeaders sets conservative defaults for every response.
//
// A mobile API has little to expose to a browser, but the headers cost nothing
// and remove a class of problem if the API is ever embedded in a web view.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		// A mobile client has no reason to be framed or sniffed.
		c.Header("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		c.Next()
	}
}

// NoStore prevents caches from keeping authenticated responses.
func NoStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Pragma", "no-cache")
		c.Next()
	}
}

// BodyLimit rejects oversized request bodies before they are parsed.
//
// The cap is generous for the JSON this API accepts, which is measured in
// kilobytes, and small enough that a single client cannot exhaust server memory
// with a few concurrent uploads of garbage.
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > maxBytes {
			httpx.Fail(c, httpx.BadRequest(httpx.CodeRequestTooLarge,
				"Request body is too large.").WithCause(nil))
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}

// MaxRequestBodyBytes bounds any JSON request body the API accepts.
const MaxRequestBodyBytes = 64 << 10 // 64 KiB
