// Package middleware holds the cross-cutting HTTP concerns every route shares.
package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/tripvault/backend/internal/httpx"
)

// RequestIDHeader is the header carrying the correlation id.
const RequestIDHeader = "X-Request-ID"

// RequestID assigns every request a correlation id.
//
// An inbound id is honoured when it looks like a UUID so a trace started by a
// gateway continues through this service; anything else is replaced rather than
// echoed back, which stops a caller from injecting arbitrary text into logs.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
		if parsed, err := uuid.Parse(id); err == nil {
			id = parsed.String()
		} else {
			id = uuid.NewString()
		}

		httpx.WithRequestID(c, id)
		c.Header(RequestIDHeader, id)
		c.Next()
	}
}

// Logger writes one structured line per request.
//
// The logger is attached to the context so handlers can log with the same
// correlation id without threading it through every signature.
func Logger(base *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery

		log := base.With(
			"request_id", httpx.RequestID(c),
			"method", c.Request.Method,
			"path", path,
		)
		httpx.WithLogger(c, log)

		c.Next()

		status := c.Writer.Status()
		attrs := []any{
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
			"bytes", c.Writer.Size(),
			"ip", c.ClientIP(),
		}
		if query != "" {
			attrs = append(attrs, "query", query)
		}

		switch {
		case status >= http.StatusInternalServerError:
			log.Error("request", attrs...)
		case status >= http.StatusBadRequest:
			log.Warn("request", attrs...)
		default:
			log.Info("request", attrs...)
		}
	}
}

// Recovery converts a panic into a 500 response.
//
// A panic in one request must not take down the process, and it must not reach
// the client as a stack trace either: the client gets a generic message and the
// correlation id, while the panic and its stack go to the log where they can be
// diagnosed.
func Recovery(base *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				log := base.With(
					"request_id", httpx.RequestID(c),
					"method", c.Request.Method,
					"path", c.Request.URL.Path,
				)

				// A broken pipe means the client vanished mid-response. That is
				// routine on mobile networks and is not worth an error-level
				// entry, but it must still not crash the server.
				if err, ok := r.(error); ok && isBrokenPipe(err) {
					log.Warn("client disconnected mid-response", "error", err)
					c.Abort()
					return
				}

				log.Error("panic recovered", "panic", r)
				if !c.Writer.Written() {
					c.AbortWithStatusJSON(http.StatusInternalServerError, httpx.ErrorEnvelope{
						Error: httpx.ErrorBody{
							Code:      httpx.CodeInternal,
							Message:   "Something went wrong. Please try again.",
							RequestID: httpx.RequestID(c),
						},
					})
				} else {
					// Headers are already on the wire; the best that can be done
					// is stop processing.
					c.Abort()
				}
			}
		}()
		c.Next()
	}
}
