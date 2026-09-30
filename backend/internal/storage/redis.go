// Package storage wraps the infrastructure clients TripVault uses for
// ephemeral state: Redis for rate limiting, caching and short-lived job
// state. Nothing here is a system of record.
package storage

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/tripvault/backend/internal/config"
)

// Redis is the Redis client plus the key prefix, so every caller shares one
// namespace and one connection pool.
type Redis struct {
	client *redis.Client
	prefix string
}

// NewRedis dials Redis and verifies the connection before returning.
func NewRedis(ctx context.Context, cfg config.Redis, log *slog.Logger) (*Redis, error) {
	opts, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	// REDIS_PASSWORD wins over anything embedded in the URL so a secret in
	// .env does not have to be duplicated into the connection string.
	if cfg.Password != "" {
		opts.Password = cfg.Password
	}
	if cfg.DB != 0 {
		opts.DB = cfg.DB
	}

	client := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	if log != nil {
		log.Info("redis connected", "addr", opts.Addr, "db", opts.DB)
	}
	return &Redis{client: client, prefix: cfg.Prefix}, nil
}

// Client exposes the underlying client for callers that need raw commands.
func (r *Redis) Client() *redis.Client { return r.client }

// Prefix is the namespace all keys this client writes fall under.
func (r *Redis) Prefix() string { return r.prefix }

// Key namespaces a logical key under the configured prefix.
func (r *Redis) Key(parts ...string) string {
	key := r.prefix
	for _, p := range parts {
		key += ":" + p
	}
	return key
}

// Ping verifies Redis is reachable, for the readiness probe.
func (r *Redis) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := r.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}
	return nil
}

// Close releases the connection pool.
func (r *Redis) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Close()
}
