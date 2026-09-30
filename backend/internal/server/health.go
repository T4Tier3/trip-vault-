package server

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tripvault/backend/internal/config"
	"github.com/tripvault/backend/internal/database"
	"github.com/tripvault/backend/internal/storage"
)

// HealthHandler serves the liveness and readiness probes.
//
// The distinction is deliberate and load bearing:
//
//   - Liveness answers "is this process running". It must never touch a
//     dependency, or a database blip would make an orchestrator kill an API
//     that is perfectly able to recover on its own.
//   - Readiness answers "can this process serve traffic". It does check
//     dependencies, so a node with a broken pool is pulled out of rotation
//     without taking the node down.
type HealthHandler struct {
	pool  *database.Pool
	redis *storage.Redis
	cfg   *config.Config
}

// NewHealthHandler builds a HealthHandler.
func NewHealthHandler(deps Deps) *HealthHandler {
	return &HealthHandler{pool: deps.DB, redis: deps.Redis, cfg: deps.Config}
}

// LivenessReport is the body of GET /health.
type LivenessReport struct {
	Status  string    `json:"status"`
	Service string    `json:"service"`
	Time    time.Time `json:"time"`
}

// DependencyStatus is one dependency's readiness.
type DependencyStatus struct {
	Status  string `json:"status"`
	Latency int64  `json:"latencyMs"`
	Error   string `json:"error,omitempty"`
}

// ReadinessReport is the body of GET /ready.
type ReadinessReport struct {
	Status       string                      `json:"status"`
	Dependencies map[string]DependencyStatus `json:"dependencies"`
}

// Liveness handles GET /health.
func (h *HealthHandler) Liveness(c *gin.Context) {
	c.JSON(http.StatusOK, LivenessReport{
		Status:  "ok",
		Service: h.cfg.App.Name,
		Time:    time.Now().UTC(),
	})
}

// Readiness handles GET /ready.
func (h *HealthHandler) Readiness(c *gin.Context) {
	ctx := c.Request.Context()
	deps := map[string]DependencyStatus{}

	if h.pool != nil {
		deps["postgres"] = check(ctx, h.pool.HealthCheck)
	}
	if h.redis != nil {
		deps["redis"] = check(ctx, h.redis.Ping)
	}

	ready := true
	for _, status := range deps {
		if status.Status != "ok" {
			ready = false
			break
		}
	}

	report := ReadinessReport{
		Status:       "ok",
		Dependencies: deps,
	}
	code := http.StatusOK
	if !ready {
		report.Status = "degraded"
		code = http.StatusServiceUnavailable
	}

	c.JSON(code, report)
}

// check times one dependency probe.
//
// The error text is reported because /ready is an operator-facing endpoint, not
// a client-facing one; the value in knowing that the pool was refused beats
// hiding it behind a generic message.
func check(ctx context.Context, probe func(context.Context) error) DependencyStatus {
	start := time.Now()
	err := probe(ctx)
	status := DependencyStatus{
		Status:  "ok",
		Latency: time.Since(start).Milliseconds(),
	}
	if err != nil {
		status.Status = "unavailable"
		status.Error = err.Error()
	}
	return status
}
