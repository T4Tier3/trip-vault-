// Package config loads and validates runtime configuration from the process
// environment. Configuration is read once at startup and never mutated, so the
// rest of the application can depend on a value it knows to be valid.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment names for the deployment environments TripVault supports.
const (
	EnvDevelopment = "development"
	EnvTest        = "test"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// Config is the fully validated application configuration.
type Config struct {
	App       App
	Database  Database
	Redis     Redis
	JWT       JWT
	Argon2    Argon2
	SMTP      SMTP
	ImageKit  ImageKit
	Push      Push
	LogLevel  string
	SentryDSN string
	RateLimit RateLimit
}

// App holds process-wide application settings.
type App struct {
	Env        string
	Port       int
	BaseURL    string
	Name       string
	MediaDir   string
	TrustProxy bool
	// LinkScheme is the custom URL scheme the mobile app registers, for example
	// "tripvault". Transactional email links to this scheme so tapping a link on
	// a device hands it to the app rather than to a web page the API does not
	// serve.
	LinkScheme string
	// AllowedOrigins is the CORS allowlist. It is empty by default: a mobile
	// API needs no browser origin, so a web client must be opted in explicitly.
	AllowedOrigins []string
}

// IsDevelopment reports whether the process runs in the development environment.
func (a App) IsDevelopment() bool { return a.Env == EnvDevelopment }

// IsTest reports whether the process runs under the test environment.
func (a App) IsTest() bool { return a.Env == EnvTest }

// IsProduction reports whether the process runs in production.
func (a App) IsProduction() bool { return a.Env == EnvProduction }

// Address is the listen address for the HTTP server.
func (a App) Address() string { return fmt.Sprintf(":%d", a.Port) }

// Database holds PostgreSQL connection settings.
type Database struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
}

// Redis holds Redis connection settings, used for caching, rate limiting and
// short-lived job state.
type Redis struct {
	URL      string
	Password string
	DB       int
	Prefix   string
}

// JWT holds signing settings for the access and refresh token pairs.
type JWT struct {
	AccessSecret     []byte
	RefreshSecret    []byte
	AccessIssuer     string
	RefreshIssuer    string
	AccessExpiration time.Duration
	// RefreshExpiration is the lifetime of a single refresh token. Rotation
	// issues a new token on every refresh, so a leaked token is only usable
	// until the legitimate client next refreshes.
	RefreshExpiration time.Duration
	// RefreshReuseDetection revokes the entire token family when a refresh
	// token that was already rotated is presented again.
	RefreshReuseDetection bool
	// RefreshGracePeriod keeps a short overlap window after rotation so two
	// concurrent refreshes from the same client do not log each other out.
	RefreshGracePeriod time.Duration
}

// Argon2 holds Argon2id password hashing parameters.
type Argon2 struct {
	Time    uint32
	Memory  uint32
	Threads uint8
	SaltLen uint32
	KeyLen  uint32
}

// SMTP holds outbound email settings.
type SMTP struct {
	Host       string
	Port       int
	Username   string
	Password   string
	From       string
	FromName   string
	TLS        bool
	SkipVerify bool
	// Enabled controls whether the mailer actually dials SMTP. In the test
	// environment it stays false so tests never require a live mail server.
	Enabled bool
}

// Address is the host:port dial target for the SMTP server.
func (s SMTP) Address() string { return fmt.Sprintf("%s:%d", s.Host, s.Port) }

// ImageKit holds media infrastructure settings.
//
// PrivateKey is intentionally backend-only. It must never be exposed through
// an API response or bundled into the mobile application. See
// docs/architecture.md.
type ImageKit struct {
	PublicKey    string
	PrivateKey   string
	URLEndpoint  string
	AuthTokenTTL time.Duration
}

// Configured reports whether enough ImageKit settings exist to sign uploads.
func (i ImageKit) Configured() bool {
	return i.PublicKey != "" && i.PrivateKey != "" && i.URLEndpoint != ""
}

// Push holds push notification settings.
type Push struct {
	FCMProjectID string
	Enabled      bool
}

// RateLimit holds per-client and per-route request limits.
type RateLimit struct {
	Enabled       bool
	Requests      int
	Window        time.Duration
	AuthRequests  int
	AuthWindow    time.Duration
	TrustForward  bool
	ExcludedPaths []string
}

// minSecretLength is the shortest signing secret accepted. A shorter HMAC key
// weakens the token signature, so startup fails rather than silently accept it.
const minSecretLength = 32

// Load reads configuration from the environment and validates it.
//
// Every problem found is reported at once so a misconfigured deployment does
// not have to be restarted repeatedly to discover each issue.
func Load() (*Config, error) {
	env := lookupEnv("APP_ENV", EnvDevelopment)

	cfg := &Config{
		App: App{
			Env:            env,
			Port:           lookupInt("PORT", 8080),
			BaseURL:        strings.TrimRight(lookupEnv("APP_BASE_URL", "http://localhost:8080"), "/"),
			Name:           lookupEnv("APP_NAME", "TripVault"),
			MediaDir:       lookupEnv("MEDIA_DIR", ""),
			TrustProxy:     lookupBool("TRUST_PROXY", false),
			LinkScheme:     strings.ToLower(lookupEnv("APP_LINK_SCHEME", "tripvault")),
			AllowedOrigins: lookupCSV("ALLOWED_ORIGINS", ""),
		},
		Database: Database{
			URL:             lookupEnv("DATABASE_URL", ""),
			MaxConns:        int32(lookupInt("DB_MAX_CONNS", 20)),
			MinConns:        int32(lookupInt("DB_MIN_CONNS", 2)),
			MaxConnLifetime: lookupDuration("DB_MAX_CONN_LIFETIME", time.Hour),
			MaxConnIdleTime: lookupDuration("DB_MAX_CONN_IDLE_TIME", 30*time.Minute),
			ConnectTimeout:  lookupDuration("DB_CONNECT_TIMEOUT", 10*time.Second),
		},
		Redis: Redis{
			URL:      lookupEnv("REDIS_URL", "redis://localhost:6379/0"),
			Password: lookupEnv("REDIS_PASSWORD", ""),
			DB:       lookupInt("REDIS_DB", 0),
			Prefix:   lookupEnv("REDIS_PREFIX", "tripvault"),
		},
		JWT: JWT{
			AccessSecret:          []byte(lookupEnv("JWT_ACCESS_SECRET", "")),
			RefreshSecret:         []byte(lookupEnv("JWT_REFRESH_SECRET", "")),
			AccessIssuer:          lookupEnv("JWT_ACCESS_ISSUER", "tripvault-api"),
			RefreshIssuer:         lookupEnv("JWT_REFRESH_ISSUER", "tripvault-api"),
			AccessExpiration:      lookupDuration("JWT_ACCESS_EXPIRATION", 15*time.Minute),
			RefreshExpiration:     lookupDuration("JWT_REFRESH_EXPIRATION", 30*24*time.Hour),
			RefreshReuseDetection: lookupBool("JWT_REFRESH_REUSE_DETECTION", true),
			RefreshGracePeriod:    lookupDuration("JWT_REFRESH_GRACE_PERIOD", 10*time.Second),
		},
		Argon2: Argon2{
			Time:    uint32(lookupInt("ARGON2_TIME", 3)),
			Memory:  uint32(lookupInt("ARGON2_MEMORY_KIB", 64*1024)),
			Threads: uint8(lookupInt("ARGON2_THREADS", 2)),
			SaltLen: 16,
			KeyLen:  32,
		},
		SMTP: SMTP{
			Host:       lookupEnv("SMTP_HOST", "localhost"),
			Port:       lookupInt("SMTP_PORT", 1025),
			Username:   lookupEnv("SMTP_USERNAME", ""),
			Password:   lookupEnv("SMTP_PASSWORD", ""),
			From:       lookupEnv("SMTP_FROM", ""),
			TLS:        lookupBool("SMTP_TLS", false),
			SkipVerify: lookupBool("SMTP_SKIP_VERIFY", false),
			Enabled:    lookupBool("SMTP_ENABLED", true),
		},
		ImageKit: ImageKit{
			PublicKey:    lookupEnv("IMAGEKIT_PUBLIC_KEY", ""),
			PrivateKey:   lookupEnv("IMAGEKIT_PRIVATE_KEY", ""),
			URLEndpoint:  strings.TrimRight(lookupEnv("IMAGEKIT_URL_ENDPOINT", ""), "/"),
			AuthTokenTTL: lookupDuration("IMAGEKIT_AUTH_TOKEN_TTL", 30*time.Minute),
		},
		Push: Push{
			FCMProjectID: lookupEnv("FCM_PROJECT_ID", ""),
			Enabled:      lookupBool("PUSH_ENABLED", false),
		},
		LogLevel:  lookupEnv("LOG_LEVEL", "info"),
		SentryDSN: lookupEnv("SENTRY_DSN", ""),
		RateLimit: RateLimit{
			Enabled:       lookupBool("RATE_LIMIT_ENABLED", true),
			Requests:      lookupInt("RATE_LIMIT_REQUESTS", 300),
			Window:        lookupDuration("RATE_LIMIT_WINDOW", time.Minute),
			AuthRequests:  lookupInt("AUTH_RATE_LIMIT_REQUESTS", 10),
			AuthWindow:    lookupDuration("AUTH_RATE_LIMIT_WINDOW", time.Minute),
			TrustForward:  lookupBool("RATE_LIMIT_TRUST_PROXY", false),
			ExcludedPaths: lookupCSV("RATE_LIMIT_EXCLUDED_PATHS", "/health,/ready"),
		},
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ValidateError aggregates every configuration problem found at startup.
type ValidateError struct {
	Problems []string
}

func (e *ValidateError) Error() string {
	return fmt.Sprintf("invalid configuration:\n  - %s", strings.Join(e.Problems, "\n  - "))
}

// Unwrap lets errors.Is match a sentinel for "configuration was invalid".
func (e *ValidateError) Unwrap() error { return ErrInvalidConfig }

// ErrInvalidConfig is the sentinel returned when validation fails.
var ErrInvalidConfig = errors.New("invalid configuration")

// validate checks the loaded configuration and collects every problem.
func (c *Config) validate() error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	switch c.App.Env {
	case EnvDevelopment, EnvTest, EnvStaging, EnvProduction:
	default:
		add("APP_ENV must be one of development, test, staging, production (got %q)", c.App.Env)
	}

	if c.App.Port < 1 || c.App.Port > 65535 {
		add("PORT must be between 1 and 65535 (got %d)", c.App.Port)
	}

	if u, err := url.Parse(c.App.BaseURL); err != nil || u.Scheme == "" || u.Host == "" {
		add("APP_BASE_URL must be an absolute URL (got %q)", c.App.BaseURL)
	}

	// A malformed scheme produces a link no device can act on, so it is caught
	// at startup rather than silently emailed to a user.
	if !validLinkScheme(c.App.LinkScheme) {
		add("APP_LINK_SCHEME must be a URL scheme such as tripvault (got %q)", c.App.LinkScheme)
	}

	if c.Database.URL == "" {
		add("DATABASE_URL is required")
	}
	if c.Database.MaxConns < 1 {
		add("DB_MAX_CONNS must be at least 1 (got %d)", c.Database.MaxConns)
	}
	if c.Database.MinConns < 0 {
		add("DB_MIN_CONNS cannot be negative (got %d)", c.Database.MinConns)
	}
	if c.Database.MinConns > c.Database.MaxConns {
		add("DB_MIN_CONNS (%d) cannot exceed DB_MAX_CONNS (%d)", c.Database.MinConns, c.Database.MaxConns)
	}

	if c.Redis.URL == "" {
		add("REDIS_URL is required")
	}

	if len(c.JWT.AccessSecret) < minSecretLength {
		add("JWT_ACCESS_SECRET is required and must be at least %d bytes (got %d)", minSecretLength, len(c.JWT.AccessSecret))
	}
	if len(c.JWT.RefreshSecret) < minSecretLength {
		add("JWT_REFRESH_SECRET is required and must be at least %d bytes (got %d)", minSecretLength, len(c.JWT.RefreshSecret))
	}
	if string(c.JWT.AccessSecret) == string(c.JWT.RefreshSecret) {
		add("JWT_ACCESS_SECRET and JWT_REFRESH_SECRET must be different values")
	}
	if c.JWT.AccessExpiration <= 0 {
		add("JWT_ACCESS_EXPIRATION must be positive (got %s)", c.JWT.AccessExpiration)
	}
	if c.JWT.AccessExpiration > time.Hour {
		add("JWT_ACCESS_EXPIRATION must be 1h or shorter so tokens stay short-lived (got %s)", c.JWT.AccessExpiration)
	}
	if c.JWT.RefreshExpiration <= c.JWT.AccessExpiration {
		add("JWT_REFRESH_EXPIRATION (%s) must be longer than JWT_ACCESS_EXPIRATION (%s)", c.JWT.RefreshExpiration, c.JWT.AccessExpiration)
	}
	if c.JWT.RefreshGracePeriod < 0 {
		add("JWT_REFRESH_GRACE_PERIOD cannot be negative (got %s)", c.JWT.RefreshGracePeriod)
	}

	if c.Argon2.Time < 1 {
		add("ARGON2_TIME must be at least 1 (got %d)", c.Argon2.Time)
	}
	if c.Argon2.Memory < 8*1024 {
		add("ARGON2_MEMORY_KIB must be at least 8192 KiB (got %d)", c.Argon2.Memory)
	}
	if c.Argon2.Threads < 1 {
		add("ARGON2_THREADS must be at least 1 (got %d)", c.Argon2.Threads)
	}

	if c.SMTP.Enabled {
		if c.SMTP.Host == "" {
			add("SMTP_HOST is required when SMTP is enabled")
		}
		if c.SMTP.Port < 1 || c.SMTP.Port > 65535 {
			add("SMTP_PORT must be between 1 and 65535 (got %d)", c.SMTP.Port)
		}
		if c.SMTP.From == "" {
			add("SMTP_FROM is required when SMTP is enabled")
		}
	}

	// ImageKit is required for media features but not for authentication, so a
	// partial configuration is reported as a warning-free hard error only for
	// the fields that are half-set, which almost always means a typo.
	if c.ImageKit.PrivateKey != "" && c.ImageKit.PublicKey == "" {
		add("IMAGEKIT_PUBLIC_KEY is required when IMAGEKIT_PRIVATE_KEY is set")
	}
	if c.ImageKit.PublicKey != "" && c.ImageKit.PrivateKey == "" {
		add("IMAGEKIT_PRIVATE_KEY is required when IMAGEKIT_PUBLIC_KEY is set")
	}
	if c.ImageKit.Configured() && c.ImageKit.AuthTokenTTL <= 0 {
		add("IMAGEKIT_AUTH_TOKEN_TTL must be positive (got %s)", c.ImageKit.AuthTokenTTL)
	}

	if c.RateLimit.Enabled {
		if c.RateLimit.Requests < 1 {
			add("RATE_LIMIT_REQUESTS must be at least 1 (got %d)", c.RateLimit.Requests)
		}
		if c.RateLimit.Window <= 0 {
			add("RATE_LIMIT_WINDOW must be positive (got %s)", c.RateLimit.Window)
		}
		if c.RateLimit.AuthRequests < 1 {
			add("AUTH_RATE_LIMIT_REQUESTS must be at least 1 (got %d)", c.RateLimit.AuthRequests)
		}
		if c.RateLimit.AuthWindow <= 0 {
			add("AUTH_RATE_LIMIT_WINDOW must be positive (got %s)", c.RateLimit.AuthWindow)
		}
	}

	if len(problems) > 0 {
		sortStrings(problems)
		return &ValidateError{Problems: problems}
	}
	return nil
}

// lookupEnv returns the trimmed value of key, or def when unset or blank.
func lookupEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return def
}

// lookupInt returns the integer value of key, or def when unset or unparsable.
func lookupInt(key string, def int) int {
	raw := lookupEnv(key, "")
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

// lookupBool returns the boolean value of key, or def when unset or unparsable.
func lookupBool(key string, def bool) bool {
	raw := strings.ToLower(lookupEnv(key, ""))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return v
}

// lookupDuration returns the Go duration value of key, or def when unset or
// unparsable.
func lookupDuration(key string, def time.Duration) time.Duration {
	raw := lookupEnv(key, "")
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return def
	}
	return v
}

// lookupCSV splits key on commas and drops empty entries.
func lookupCSV(key, def string) []string {
	raw := lookupEnv(key, def)
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// sortStrings is a tiny insertion sort so problem ordering stays deterministic
// without pulling the sort package into every call site.
func sortStrings(in []string) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j] < in[j-1]; j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
}

// validLinkScheme reports whether s is usable as a custom URL scheme.
//
// The rules are the scheme grammar from RFC 3986: a letter, then letters,
// digits, '+', '-' or '.'. The check is strict on purpose, because a rejected
// scheme at startup is cheaper than a dead link in someone's inbox.
func validLinkScheme(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	first := s[0]
	if !(first >= 'a' && first <= 'z') && !(first >= 'A' && first <= 'Z') {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '+', c == '-', c == '.':
		default:
			return false
		}
	}
	return true
}
