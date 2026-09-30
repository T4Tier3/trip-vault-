package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/tripvault/backend/internal/config"
	"github.com/tripvault/backend/internal/database"
	"github.com/tripvault/backend/internal/user"
)

// Service-level errors. Handlers map these to HTTP responses; the mapping is
// centralised in handler.go so no transport code has to reason about internals.
var (
	// ErrInvalidCredentials is deliberately identical for an unknown email and
	// a wrong password. Distinguishing them would allow account enumeration.
	ErrInvalidCredentials = errors.New("auth: invalid email or password")
	ErrEmailNotVerified   = errors.New("auth: email address is not verified")
	ErrAccountDisabled    = errors.New("auth: account is not available")

	// ErrInvalidSession covers every way a refresh can fail: unknown, expired,
	// revoked, or already rotated. They share one error on purpose, so a caller
	// cannot use the response to learn whether a token it holds ever existed.
	//
	// It is separate from ErrInvalidCredentials because the two endpoints need
	// different wording. Telling someone that their email or password is
	// incorrect on a request that carried neither is worse than useless: it
	// sends them to a sign-in form when the actual answer is that this session
	// is gone.
	ErrInvalidSession = errors.New("auth: session is no longer valid")
)

// Token lifetimes for the emailed one-time tokens. They are short because the
// only reason to keep one alive is that the owner has not clicked the link yet.
const (
	EmailVerificationTTL = 24 * time.Hour
	PasswordResetTTL     = 1 * time.Hour
)

// Notifier delivers the emails that carry verification and reset tokens.
//
// It is an interface so the service can be tested without an SMTP server, and
// so a future provider (SES, Postmark) slots in without touching auth.
type Notifier interface {
	SendEmailVerification(ctx context.Context, to, name, token string) error
	SendPasswordReset(ctx context.Context, to, name, token string) error
}

// Service implements the authentication flows.
type Service struct {
	users  *user.Repository
	tokens *TokenRepository
	issuer *TokenIssuer
	params HashParams
	jwt    config.JWT
	notify Notifier
	log    *slog.Logger
	// now is injectable so tests do not depend on wall-clock time.
	now func() time.Time
}

// ServiceDeps are the collaborators a Service needs.
type ServiceDeps struct {
	Users    *user.Repository
	Tokens   *TokenRepository
	Issuer   *TokenIssuer
	Params   HashParams
	JWT      config.JWT
	Notifier Notifier
	Logger   *slog.Logger
}

// NewService builds an authentication Service.
func NewService(deps ServiceDeps) (*Service, error) {
	switch {
	case deps.Users == nil:
		return nil, errors.New("auth: users repository is required")
	case deps.Tokens == nil:
		return nil, errors.New("auth: tokens repository is required")
	case deps.Issuer == nil:
		return nil, errors.New("auth: token issuer is required")
	case deps.Notifier == nil:
		return nil, errors.New("auth: notifier is required")
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Service{
		users:  deps.Users,
		tokens: deps.Tokens,
		issuer: deps.Issuer,
		params: deps.Params,
		jwt:    deps.JWT,
		notify: deps.Notifier,
		log:    deps.Logger,
		now:    time.Now,
	}, nil
}

// RegisterInput is the payload for account creation.
type RegisterInput struct {
	Email    string
	Password string
	Name     string
}

// TokenPair is the credential set returned to the client.
type TokenPair struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	TokenType    string    `json:"tokenType"`
	ExpiresAt    time.Time `json:"expiresAt"`
	ExpiresIn    int64     `json:"expiresIn"`
}

// NewTokenPair builds a TokenPair with the standard bearer metadata.
func NewTokenPair(accessToken, refreshToken string, expiresAt time.Time) TokenPair {
	return TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		TokenType:    "Bearer",
		ExpiresAt:    expiresAt,
		ExpiresIn:    int64(time.Until(expiresAt).Seconds()),
	}
}

// RegisterResult is returned by Register.
type RegisterResult struct {
	UserID      uuid.UUID
	Email       string
	Name        string
	NeedsVerify bool
}

// Register creates an account and emails a verification link.
//
// No tokens are returned: the account is unverified, so there is nothing to log
// in with yet. Requiring verification before the first login keeps a typo'd or
// mistyped address from accumulating someone else's trip data.
func (s *Service) Register(ctx context.Context, input RegisterInput) (*RegisterResult, error) {
	email := user.NormalizeEmail(input.Email)
	if err := user.ValidateEmail(email); err != nil {
		return nil, err
	}
	if err := user.ValidateName(input.Name); err != nil {
		return nil, err
	}
	if err := ValidatePasswordStrength(input.Password); err != nil {
		return nil, err
	}

	hash, err := HashPassword(input.Password, s.params)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	created, err := s.users.Create(ctx, user.CreateParams{
		Email:        email,
		PasswordHash: hash,
		Name:         input.Name,
	})
	if errors.Is(err, user.ErrEmailTaken) {
		return nil, user.ErrEmailTaken
	}
	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}

	// Email delivery must not roll back a successful registration. A user whose
	// verification email bounced still has an account and can request another
	// link; a user whose account was deleted by a mail failure has neither.
	if _, err := s.issueEmailVerification(ctx, created.ID, created.Email, created.Name); err != nil {
		s.log.Error("issue email verification token",
			"user_id", created.ID, "error", err)
	}

	return &RegisterResult{
		UserID:      created.ID,
		Email:       created.Email,
		Name:        created.Name,
		NeedsVerify: true,
	}, nil
}

// issueEmailVerification creates, persists and sends a verification token.
func (s *Service) issueEmailVerification(ctx context.Context, userID uuid.UUID, email, name string) (string, error) {
	if _, err := s.tokens.InvalidateAuthTokens(ctx, userID, TokenTypeEmailVerification); err != nil {
		return "", err
	}

	token, err := GenerateOpaqueToken()
	if err != nil {
		return "", err
	}
	expiresAt := s.now().Add(EmailVerificationTTL)
	if _, err := s.tokens.CreateAuthToken(ctx, userID, TokenTypeEmailVerification, HashOpaqueToken(token), expiresAt); err != nil {
		return "", err
	}
	if err := s.notify.SendEmailVerification(ctx, email, name, token); err != nil {
		return "", fmt.Errorf("send verification email: %w", err)
	}
	return token, nil
}

// LoginInput is the payload for a sign-in attempt.
type LoginInput struct {
	Email    string
	Password string
	// DeviceID links the session to a registered push target. Optional, so the
	// API can be exercised without a device registration.
	DeviceID *uuid.UUID
}

// LoginResult is returned by Login on success.
type LoginResult struct {
	Tokens TokenPair
	UserID uuid.UUID
	Email  string
	Name   string
}

// Login authenticates an email and password.
//
// Two properties matter here beyond the credential check itself:
//
//   - A missing account and a wrong password take the same path and return the
//     same error, and the missing-account case still pays for a hash
//     verification. Response latency therefore does not disclose which
//     addresses are registered.
//   - A password that verifies against a weaker hash than the current
//     configuration is re-hashed on the spot, which upgrades existing accounts
//     without a migration.
func (s *Service) Login(ctx context.Context, input LoginInput) (*LoginResult, error) {
	found, err := s.users.GetByEmail(ctx, input.Email)
	if err != nil {
		if errors.Is(err, user.ErrNotFound) {
			// Equalise timing against the real path.
			DummyVerify(input.Password)
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("login lookup: %w", err)
	}
	if found.IsDeleted() {
		DummyVerify(input.Password)
		return nil, ErrInvalidCredentials
	}

	ok, err := VerifyPassword(input.Password, found.PasswordHash)
	if err != nil {
		// A malformed stored hash is a server-side data problem, not a bad
		// password. It is logged and reported as invalid credentials so the
		// detail does not leak to the caller.
		s.log.Error("stored password hash is unusable", "user_id", found.ID, "error", err)
		return nil, ErrInvalidCredentials
	}
	if !ok {
		return nil, ErrInvalidCredentials
	}

	if NeedsRehash(found.PasswordHash, s.params) {
		// Best effort: failing to upgrade the hash must not block a valid login.
		if hash, err := HashPassword(input.Password, s.params); err == nil {
			if _, err := s.users.UpdatePassword(ctx, found.ID, hash, s.txRunner()); err != nil {
				s.log.Error("rehash password on login failed", "user_id", found.ID, "error", err)
			}
		}
	}

	if !found.EmailVerified {
		return nil, ErrEmailNotVerified
	}

	pair, err := s.issueSession(ctx, found, input.DeviceID)
	if err != nil {
		return nil, err
	}

	return &LoginResult{
		Tokens: pair,
		UserID: found.ID,
		Email:  found.Email,
		Name:   found.Name,
	}, nil
}

// issueSession mints an access token and a first refresh token in a new family.
func (s *Service) issueSession(ctx context.Context, u *user.User, deviceID *uuid.UUID) (TokenPair, error) {
	accessToken, expiresAt, err := s.issuer.IssueAccessToken(u.ID, u.Email)
	if err != nil {
		return TokenPair{}, fmt.Errorf("issue access token: %w", err)
	}

	refreshToken, err := GenerateOpaqueToken()
	if err != nil {
		return TokenPair{}, err
	}

	record := RefreshTokenRecord{
		UserID:    u.ID,
		FamilyID:  uuid.New(),
		TokenHash: HashOpaqueToken(refreshToken),
		DeviceID:  deviceID,
		ExpiresAt: s.now().Add(s.jwt.RefreshExpiration),
	}
	if _, err := s.tokens.CreateRefreshToken(ctx, record); err != nil {
		return TokenPair{}, err
	}

	return NewTokenPair(accessToken, refreshToken, expiresAt), nil
}

// RefreshResult is returned by Refresh.
type RefreshResult struct {
	Tokens   TokenPair
	UserID   uuid.UUID
	Replaced bool
}

// Refresh rotates a refresh token and issues a new pair.
//
// Rotation is enforced: the presented token is marked rotated and a successor is
// created in one transaction. Presenting an already rotated token is treated as
// compromise - the whole family is revoked and the caller must sign in again.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*RefreshResult, error) {
	if refreshToken == "" {
		return nil, ErrInvalidSession
	}

	hash := HashOpaqueToken(refreshToken)
	record, err := s.tokens.GetRefreshToken(ctx, hash)
	if err != nil {
		if errors.Is(err, ErrTokenNotFound) {
			return nil, ErrInvalidSession
		}
		return nil, fmt.Errorf("lookup refresh token: %w", err)
	}

	switch {
	case record.RevokedAt != nil:
		// A revoked token being presented is also suspicious, but the session
		// was already ended, so rejecting is sufficient.
		return nil, ErrInvalidSession
	case record.RotatedAt != nil:
		// Reuse. End the entire family: the attacker and the legitimate client
		// are now indistinguishable, and only one of them can be right.
		if revoked, err := s.tokens.RevokeFamily(ctx, record.FamilyID, "refresh_token_reuse"); err != nil {
			return nil, fmt.Errorf("revoke family on reuse: %w", err)
		} else {
			s.log.Warn("refresh token reuse detected, family revoked",
				"user_id", record.UserID,
				"family_id", record.FamilyID,
				"sessions_revoked", revoked,
			)
		}
		return nil, ErrInvalidSession
	case !record.ExpiresAt.After(s.now()):
		return nil, ErrInvalidSession
	}

	owner, err := s.users.GetByID(ctx, record.UserID)
	if err != nil {
		if errors.Is(err, user.ErrNotFound) {
			return nil, ErrInvalidSession
		}
		return nil, fmt.Errorf("load user during refresh: %w", err)
	}
	if owner.IsDeleted() {
		return nil, ErrInvalidSession
	}

	accessToken, expiresAt, err := s.issuer.IssueAccessToken(owner.ID, owner.Email)
	if err != nil {
		return nil, fmt.Errorf("issue access token: %w", err)
	}

	nextToken, err := GenerateOpaqueToken()
	if err != nil {
		return nil, err
	}
	next := RefreshTokenRecord{
		UserID:    owner.ID,
		FamilyID:  record.FamilyID, // the family carries over to the successor
		TokenHash: HashOpaqueToken(nextToken),
		DeviceID:  record.DeviceID,
		ExpiresAt: s.now().Add(s.jwt.RefreshExpiration),
	}

	rotated, err := s.tokens.RotateRefreshToken(ctx, hash, &next)
	if err != nil {
		return nil, fmt.Errorf("rotate refresh token: %w", err)
	}
	if !rotated {
		// Lost the race to a concurrent refresh with the same token. The other
		// request already rotated it, so this one must not mint a second pair.
		return nil, ErrInvalidSession
	}

	return &RefreshResult{
		Tokens:   NewTokenPair(accessToken, nextToken, expiresAt),
		UserID:   owner.ID,
		Replaced: true,
	}, nil
}

// Logout revokes a single refresh token.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return nil
	}
	if _, err := s.tokens.RevokeRefreshToken(ctx, HashOpaqueToken(refreshToken), "logout"); err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	return nil
}

// VerifyEmail consumes a verification token and marks the account verified.
func (s *Service) VerifyEmail(ctx context.Context, token string) error {
	if token == "" {
		return ErrTokenNotFound
	}

	userID, err := s.tokens.ConsumeAuthToken(ctx, HashOpaqueToken(token), TokenTypeEmailVerification)
	if err != nil {
		return err
	}
	if err := s.users.MarkEmailVerified(ctx, userID); err != nil {
		return fmt.Errorf("mark email verified: %w", err)
	}
	return nil
}

// ResendVerificationEmail issues a fresh verification link.
func (s *Service) ResendVerificationEmail(ctx context.Context, email string) error {
	found, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, user.ErrNotFound) {
			// Report success for unknown addresses so this endpoint cannot be
			// used to enumerate registered emails.
			return nil
		}
		return fmt.Errorf("resend verification: %w", err)
	}
	if found.EmailVerified {
		return nil
	}
	if _, err := s.issueEmailVerification(ctx, found.ID, found.Email, found.Name); err != nil {
		return err
	}
	return nil
}

// ForgotPassword issues a password reset link.
//
// The response is always successful, whether or not the address is registered.
// A "no such user" difference would turn this endpoint into an account
// enumeration oracle.
func (s *Service) ForgotPassword(ctx context.Context, email string) error {
	found, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, user.ErrNotFound) {
			s.log.Info("password reset requested for unknown address", "email", user.NormalizeEmail(email))
			return nil
		}
		return fmt.Errorf("forgot password: %w", err)
	}

	if _, err := s.tokens.InvalidateAuthTokens(ctx, found.ID, TokenTypePasswordReset); err != nil {
		return err
	}

	token, err := GenerateOpaqueToken()
	if err != nil {
		return err
	}
	if _, err := s.tokens.CreateAuthToken(ctx, found.ID, TokenTypePasswordReset, HashOpaqueToken(token), s.now().Add(PasswordResetTTL)); err != nil {
		return err
	}
	if err := s.notify.SendPasswordReset(ctx, found.Email, found.Name, token); err != nil {
		return fmt.Errorf("send password reset email: %w", err)
	}
	return nil
}

// ResetPassword consumes a reset token and sets a new password.
//
// Every existing session is revoked in the same transaction as the update, so a
// stolen refresh token does not survive a password reset.
func (s *Service) ResetPassword(ctx context.Context, token, newPassword string) error {
	if err := ValidatePasswordStrength(newPassword); err != nil {
		return err
	}
	if token == "" {
		return ErrTokenNotFound
	}

	userID, err := s.tokens.ConsumeAuthToken(ctx, HashOpaqueToken(token), TokenTypePasswordReset)
	if err != nil {
		return err
	}

	hash, err := HashPassword(newPassword, s.params)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if _, err := s.users.UpdatePassword(ctx, userID, hash, s.txRunner()); err != nil {
		return fmt.Errorf("reset password: %w", err)
	}
	return nil
}

// txRunner returns the transaction-capable database handle, or nil when the
// repository was built over a non-transactional stub (which only happens in
// unit tests).
func (s *Service) txRunner() database.TxRunner { return s.users.TxRunner() }
