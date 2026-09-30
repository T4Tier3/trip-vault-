package config

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// setEnv replaces the process environment for the duration of the test.
// configEnvKeys is every environment variable Load reads.
//
// It exists so the tests can start from a known-empty environment instead of
// inheriting whatever the developer happens to have exported. That is not
// hypothetical: `make test` includes the repository .env and exports it, so
// without this the config tests pass under `go test ./...` and fail under
// `make test`, for reasons that have nothing to do with the code under test.
var configEnvKeys = []string{
	"ALLOWED_ORIGINS", "APP_BASE_URL", "APP_ENV", "APP_LINK_SCHEME", "APP_NAME",
	"ARGON2_MEMORY_KIB", "ARGON2_THREADS", "ARGON2_TIME",
	"AUTH_RATE_LIMIT_REQUESTS", "AUTH_RATE_LIMIT_WINDOW",
	"DATABASE_URL", "DB_CONNECT_TIMEOUT", "DB_MAX_CONN_IDLE_TIME",
	"DB_MAX_CONN_LIFETIME", "DB_MAX_CONNS", "DB_MIN_CONNS",
	"FCM_PROJECT_ID",
	"IMAGEKIT_AUTH_TOKEN_TTL", "IMAGEKIT_PRIVATE_KEY", "IMAGEKIT_PUBLIC_KEY",
	"IMAGEKIT_URL_ENDPOINT",
	"JWT_ACCESS_EXPIRATION", "JWT_ACCESS_ISSUER", "JWT_ACCESS_SECRET",
	"JWT_REFRESH_EXPIRATION", "JWT_REFRESH_GRACE_PERIOD", "JWT_REFRESH_ISSUER",
	"JWT_REFRESH_REUSE_DETECTION", "JWT_REFRESH_SECRET",
	"LOG_LEVEL", "MEDIA_DIR",
	"PUSH_ENABLED",
	"RATE_LIMIT_ENABLED", "RATE_LIMIT_EXCLUDED_PATHS", "RATE_LIMIT_REQUESTS",
	"RATE_LIMIT_TRUST_PROXY", "RATE_LIMIT_WINDOW",
	"REDIS_DB", "REDIS_PASSWORD", "REDIS_PREFIX", "REDIS_URL",
	"SENTRY_DSN",
	"SMTP_ENABLED", "SMTP_FROM", "SMTP_HOST", "SMTP_PASSWORD", "SMTP_PORT",
	"SMTP_SKIP_VERIFY", "SMTP_TLS", "SMTP_USERNAME",
	"TRUST_PROXY", "PORT",
}

// setEnv clears every variable Load reads and then applies the given values, so
// each test describes the whole environment it needs.
func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, key := range configEnvKeys {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

// validSecrets returns a key/value map that satisfies validation, so each test
// only has to describe the field it actually cares about.
func validSecrets() map[string]string {
	return map[string]string{
		"APP_ENV":            EnvTest,
		"DATABASE_URL":       "postgresql://postgres:postgres@localhost:5432/tripvault",
		"REDIS_URL":          "redis://localhost:6379/0",
		"JWT_ACCESS_SECRET":  strings.Repeat("a", 48),
		"JWT_REFRESH_SECRET": strings.Repeat("b", 48),
		"SMTP_ENABLED":       "false",
		"RATE_LIMIT_ENABLED": "true",
	}
}

func TestLoad_ValidConfigReturnsNoError(t *testing.T) {
	setEnv(t, validSecrets())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if cfg.App.Env != EnvTest {
		t.Errorf("App.Env = %q, want %q", cfg.App.Env, EnvTest)
	}
	if cfg.App.Port != 8080 {
		t.Errorf("App.Port = %d, want 8080", cfg.App.Port)
	}
	if cfg.JWT.AccessExpiration.Minutes() != 15 {
		t.Errorf("AccessExpiration = %s, want 15m", cfg.JWT.AccessExpiration)
	}
	if !cfg.App.IsTest() {
		t.Error("IsTest() = false, want true")
	}
	if cfg.App.IsProduction() {
		t.Error("IsProduction() = true, want false")
	}
}

func TestLoad_DefaultsAreApplied(t *testing.T) {
	setEnv(t, validSecrets())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if got, want := cfg.Database.MaxConns, int32(20); got != want {
		t.Errorf("MaxConns = %d, want %d", got, want)
	}
	if got, want := cfg.Argon2.Memory, uint32(65536); got != want {
		t.Errorf("Argon2.Memory = %d, want %d", got, want)
	}
	if got, want := cfg.RateLimit.Requests, 300; got != want {
		t.Errorf("RateLimit.Requests = %d, want %d", got, want)
	}
	if got := cfg.RateLimit.ExcludedPaths; len(got) != 2 || got[0] != "/health" {
		t.Errorf("ExcludedPaths = %v, want [/health /ready]", got)
	}
}

func TestLoad_MissingSecretsIsRejected(t *testing.T) {
	setEnv(t, map[string]string{
		"APP_ENV":            EnvTest,
		"DATABASE_URL":       "postgresql://localhost/tripvault",
		"REDIS_URL":          "redis://localhost:6379/0",
		"SMTP_ENABLED":       "false",
		"JWT_ACCESS_SECRET":  "",
		"JWT_REFRESH_SECRET": "",
	})

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want validation error for missing JWT secrets")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("errors.Is(err, ErrInvalidConfig) = false, want true (err = %v)", err)
	}
	if !strings.Contains(err.Error(), "JWT_ACCESS_SECRET") {
		t.Errorf("error %q does not mention JWT_ACCESS_SECRET", err)
	}
	if !strings.Contains(err.Error(), "JWT_REFRESH_SECRET") {
		t.Errorf("error %q does not mention JWT_REFRESH_SECRET", err)
	}
}

func TestLoad_ShortSecretIsRejected(t *testing.T) {
	env := validSecrets()
	env["JWT_ACCESS_SECRET"] = "too-short"
	setEnv(t, env)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want validation error for short secret")
	}
	if !strings.Contains(err.Error(), "at least 32 bytes") {
		t.Errorf("error %q does not explain the minimum length", err)
	}
}

func TestLoad_IdenticalSecretsAreRejected(t *testing.T) {
	env := validSecrets()
	shared := strings.Repeat("c", 48)
	env["JWT_ACCESS_SECRET"] = shared
	env["JWT_REFRESH_SECRET"] = shared
	setEnv(t, env)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want validation error for identical secrets")
	}
	if !strings.Contains(err.Error(), "must be different") {
		t.Errorf("error %q does not explain that the secrets must differ", err)
	}
}

func TestLoad_RefreshExpiryMustExceedAccessExpiry(t *testing.T) {
	env := validSecrets()
	env["JWT_ACCESS_EXPIRATION"] = "2h"
	env["JWT_REFRESH_EXPIRATION"] = "1h"
	setEnv(t, env)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want validation error")
	}
	if !strings.Contains(err.Error(), "must be longer than") {
		t.Errorf("error %q does not explain the ordering requirement", err)
	}
}

func TestLoad_LongLivedAccessTokenIsRejected(t *testing.T) {
	env := validSecrets()
	env["JWT_ACCESS_EXPIRATION"] = "24h"
	env["JWT_REFRESH_EXPIRATION"] = "720h"
	setEnv(t, env)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want validation error for long-lived access token")
	}
	if !strings.Contains(err.Error(), "short-lived") {
		t.Errorf("error %q does not explain the short-lived requirement", err)
	}
}

func TestLoad_InvalidEnvironmentIsRejected(t *testing.T) {
	env := validSecrets()
	env["APP_ENV"] = "productionn"
	setEnv(t, env)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want validation error for unknown APP_ENV")
	}
	if !strings.Contains(err.Error(), "APP_ENV must be one of") {
		t.Errorf("error %q does not list valid environments", err)
	}
}

func TestLoad_InvalidBaseURLIsRejected(t *testing.T) {
	env := validSecrets()
	env["APP_BASE_URL"] = "not-a-url"
	setEnv(t, env)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want validation error for relative APP_BASE_URL")
	}
	if !strings.Contains(err.Error(), "APP_BASE_URL") {
		t.Errorf("error %q does not mention APP_BASE_URL", err)
	}
}

func TestLoad_ConnPoolBoundsAreValidated(t *testing.T) {
	env := validSecrets()
	env["DB_MIN_CONNS"] = "50"
	env["DB_MAX_CONNS"] = "10"
	setEnv(t, env)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want validation error for inverted pool bounds")
	}
	if !strings.Contains(err.Error(), "cannot exceed DB_MAX_CONNS") {
		t.Errorf("error %q does not explain the pool bound relationship", err)
	}
}

func TestLoad_PartialImageKitConfigIsRejected(t *testing.T) {
	env := validSecrets()
	env["IMAGEKIT_PRIVATE_KEY"] = "private"
	setEnv(t, env)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want validation error for half-configured ImageKit")
	}
	if !strings.Contains(err.Error(), "IMAGEKIT_PUBLIC_KEY is required") {
		t.Errorf("error %q does not name the missing ImageKit field", err)
	}
}

func TestLoad_EmptyImageKitIsAllowedForAuth(t *testing.T) {
	setEnv(t, validSecrets())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.ImageKit.Configured() {
		t.Error("ImageKit.Configured() = true, want false when unset")
	}
}

func TestLoad_ReportsEveryProblemAtOnce(t *testing.T) {
	setEnv(t, map[string]string{
		"APP_ENV":      "nope",
		"PORT":         "70000",
		"SMTP_ENABLED": "false",
	})

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want validation error")
	}

	var verr *ValidateError
	if !errors.As(err, &verr) {
		t.Fatalf("error type = %T, want *ValidateError", err)
	}
	if len(verr.Problems) < 3 {
		t.Errorf("Problems = %v, want at least 3 problems reported together", verr.Problems)
	}
}

func TestLookupParsersFallBackOnBadInput(t *testing.T) {
	setEnv(t, map[string]string{
		"PORT":                      "not-a-number",
		"RATE_LIMIT_ENABLED":        "yes-please",
		"JWT_ACCESS_EXPIRATION":     "forever",
		"RATE_LIMIT_EXCLUDED_PATHS": "/health, , /ready ,",
	})

	cfg := &Config{}
	_ = cfg

	if got := lookupInt("PORT", 8080); got != 8080 {
		t.Errorf("lookupInt fallback = %d, want 8080", got)
	}
	if got := lookupBool("RATE_LIMIT_ENABLED", true); got != true {
		t.Errorf("lookupBool fallback = %v, want true", got)
	}
	if got := lookupDuration("JWT_ACCESS_EXPIRATION", 15*time.Minute); got != 15*time.Minute {
		t.Errorf("lookupDuration fallback = %s, want 15m", got)
	}
	got := lookupCSV("RATE_LIMIT_EXCLUDED_PATHS", "")
	if len(got) != 2 || got[0] != "/health" || got[1] != "/ready" {
		t.Errorf("lookupCSV = %v, want [/health /ready]", got)
	}
}
