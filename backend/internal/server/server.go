// Package server wires the HTTP router, dependencies and process lifecycle.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tripvault/backend/internal/auth"
	"github.com/tripvault/backend/internal/config"
	"github.com/tripvault/backend/internal/database"
	"github.com/tripvault/backend/internal/emailserver"
	"github.com/tripvault/backend/internal/httpx"
	"github.com/tripvault/backend/internal/middleware"
	"github.com/tripvault/backend/internal/storage"
	"github.com/tripvault/backend/internal/user"
)

// APIPrefix is the versioned path every route lives under.
const APIPrefix = "/api/v1"

// Deps are the collaborators the router needs.
type Deps struct {
	Config *config.Config
	DB     *database.Pool
	Redis  *storage.Redis
	Logger *slog.Logger
	Mailer *emailserver.Mailer

	// Notifier overrides the email transport. Production leaves it nil and the
	// router builds an SMTP notifier from the mailer; tests supply a capturing
	// notifier so verification and reset flows can be completed without a mail
	// server.
	Notifier auth.Notifier
}

// Server owns the HTTP listener.
type Server struct {
	http   *http.Server
	cfg    *config.Config
	log    *slog.Logger
	tokens *auth.TokenRepository
}

// DeleteExpiredTokens removes tokens that can no longer be used and reports how
// many rows of each kind went away. The sweeper in main calls this on a timer.
func (s *Server) DeleteExpiredTokens(ctx context.Context) (authTokens, refreshTokens int64, err error) {
	return s.tokens.DeleteExpired(ctx)
}

// New builds the router and the HTTP server.
func New(deps Deps) (*Server, error) {
	if deps.Config == nil {
		return nil, errors.New("server: config is required")
	}
	if deps.DB == nil {
		return nil, errors.New("server: database pool is required")
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}

	router, err := buildRouter(deps)
	if err != nil {
		return nil, err
	}

	return &Server{
		log:    deps.Logger,
		cfg:    deps.Config,
		tokens: auth.NewTokenRepository(deps.DB),
		http: &http.Server{
			Addr:              deps.Config.App.Address(),
			Handler:           router,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       120 * time.Second,
			// TripVault does not proxy media, so no streaming responses are
			// expected; capping the header size keeps a malformed request from
			// allocating heavily.
			MaxHeaderBytes: 1 << 20,
		},
	}, nil
}

// Handler exposes the router, for tests.
func (s *Server) Handler() http.Handler { return s.http.Handler }

// buildRouter assembles the middleware chain and mounts every route.
func buildRouter(deps Deps) (*gin.Engine, error) {
	cfg := deps.Config

	if !cfg.App.IsDevelopment() {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	// A path that collides with a method is a routing bug, not a 404. Letting
	// gin redirect hides it.
	router.RedirectTrailingSlash = true
	router.HandleMethodNotAllowed = true
	router.MaxMultipartMemory = middleware.MaxRequestBodyBytes

	router.Use(
		middleware.RequestID(),
		middleware.Recovery(deps.Logger),
		middleware.Logger(deps.Logger),
		middleware.SecurityHeaders(),
		middleware.NoStore(),
		middleware.CORS(cfg.App, cfg.App.AllowedOrigins),
		middleware.BodyLimit(middleware.MaxRequestBodyBytes),
	)

	router.NoRoute(func(c *gin.Context) {
		httpx.Fail(c, httpx.NotFound("That endpoint does not exist."))
	})
	router.NoMethod(func(c *gin.Context) {
		httpx.Fail(c, &httpx.APIError{
			Status:  http.StatusMethodNotAllowed,
			Code:    httpx.CodeInvalidRequest,
			Message: "That method is not supported on this endpoint.",
		})
	})

	var limiter middleware.RateLimiter
	if cfg.RateLimit.Enabled && deps.Redis != nil {
		limiter = storage.NewRateLimiter(deps.Redis.Client(), cfg.Redis.Prefix)
		router.Use(middleware.RateLimit(limiter, cfg.RateLimit.Requests, cfg.RateLimit.Window,
			cfg.RateLimit.ExcludedPaths, cfg.RateLimit.TrustForward))
	} else {
		deps.Logger.Warn("rate limiting disabled")
	}

	// Health endpoints are registered outside the versioned API: orchestrators
	// probe a fixed, unversioned path.
	health := NewHealthHandler(deps)
	router.GET("/health", health.Liveness)
	router.GET("/ready", health.Readiness)

	api := router.Group(APIPrefix)
	usersRepo := user.NewRepository(deps.DB)

	authHandler, err := buildAuthHandler(deps, usersRepo)
	if err != nil {
		return nil, err
	}

	// Credential endpoints get their own tighter budget. The limiter is
	// attached to the group before the routes exist so it runs first.
	authGroup := api.Group("/auth")
	if limiter != nil {
		authGroup.Use(middleware.StrictRateLimit(limiter, cfg.RateLimit.AuthRequests, cfg.RateLimit.AuthWindow))
	}
	authHandler.RegisterRoutesOn(authGroup)

	// Authenticated routes. Identity comes from the verified token only.
	secured := api.Group("")
	secured.Use(middleware.RequireAuth(auth.NewTokenIssuer(cfg.JWT)))
	user.NewHandler(usersRepo).RegisterRoutesOn(secured)

	return router, nil
}

// buildAuthHandler constructs the authentication stack.
func buildAuthHandler(deps Deps, users *user.Repository) (*auth.Handler, error) {
	tokens := auth.NewTokenRepository(deps.DB)
	issuer := auth.NewTokenIssuer(deps.Config.JWT)

	notifier := deps.Notifier
	if notifier == nil {
		mailer := deps.Mailer
		if mailer == nil {
			// Build an inert mailer so a process started without SMTP still
			// runs; verification and reset mails are reported as undeliverable
			// instead of taking the API down.
			mailer = emailserver.New(config.SMTP{Enabled: false}, deps.Logger)
		}
		notifier = emailserver.NewNotifier(mailer, deps.Config.App, deps.Logger)
	}

	service, err := auth.NewService(auth.ServiceDeps{
		Users:    users,
		Tokens:   tokens,
		Issuer:   issuer,
		Params:   auth.HashParamsFromConfig(deps.Config.Argon2),
		JWT:      deps.Config.JWT,
		Notifier: notifier,
		Logger:   deps.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("build auth service: %w", err)
	}
	return auth.NewHandler(service), nil
}

// Run starts the server and blocks until ctx is cancelled.
//
// On shutdown the listener is drained before returning so in-flight requests
// complete. Clients that abandon a request mid-body are handled by the recovery
// middleware, which is why the shutdown grace period is generous.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)

	go func() {
		s.log.Info("http server starting",
			"addr", s.http.Addr,
			"env", s.cfg.App.Env,
			"api_prefix", APIPrefix,
		)
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("listen and serve: %w", err)
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.log.Info("shutdown signal received, draining connections")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	if err := s.http.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	s.log.Info("http server stopped")
	return nil
}
