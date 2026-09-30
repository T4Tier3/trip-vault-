package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/tripvault/backend/internal/config"
)

// Errors returned when a token is rejected. Handlers map these onto responses,
// so the distinction between "expired" and "malformed" is available without
// leaking the reason to the client.
var (
	ErrTokenMalformed   = errors.New("auth: token is malformed")
	ErrTokenSignature   = errors.New("auth: token signature is invalid")
	ErrTokenExpired     = errors.New("auth: token has expired")
	ErrTokenNotYetValid = errors.New("auth: token is not valid yet")
	ErrTokenIssuer      = errors.New("auth: token issuer is unexpected")
	ErrTokenType        = errors.New("auth: unexpected token type")
	ErrTokenSubject     = errors.New("auth: token subject is invalid")
)

// TokenTypeAccess marks a JWT as an access token. Embedding the type in the
// payload stops a refresh credential from being replayed as an access token if
// the two ever share a signing path.
const TokenTypeAccess = "access"

// AccessClaims is the payload of a TripVault access token.
//
// The token carries only what authorisation needs on every request. Anything
// that changes (a user's name, trip membership) must be read from the database
// instead, because a signed claim is a snapshot that cannot be revoked early.
type AccessClaims struct {
	jwt.RegisteredClaims
	TokenType string `json:"typ"`
	Email     string `json:"email,omitempty"`
}

// UserID returns the subject as a UUID.
func (c *AccessClaims) UserID() (uuid.UUID, error) {
	if c.Subject == "" {
		return uuid.Nil, ErrTokenSubject
	}
	id, err := uuid.Parse(c.Subject)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %v", ErrTokenSubject, err)
	}
	return id, nil
}

// TokenIssuer signs and verifies access tokens.
//
// Refresh tokens are deliberately not JWTs. They are opaque random strings
// looked up by hash, which is what makes rotation, revocation on logout, and
// reuse detection possible at all. A self-contained refresh JWT could not be
// revoked before it expired.
type TokenIssuer struct {
	accessSecret     []byte
	accessIssuer     string
	accessExpiration time.Duration
	// now is injectable so tests do not depend on wall-clock time.
	now func() time.Time
}

// NewTokenIssuer builds a TokenIssuer from configuration.
func NewTokenIssuer(cfg config.JWT) *TokenIssuer {
	return &TokenIssuer{
		accessSecret:     cfg.AccessSecret,
		accessIssuer:     cfg.AccessIssuer,
		accessExpiration: cfg.AccessExpiration,
		now:              time.Now,
	}
}

// WithClock replaces the time source, for tests only.
func (t *TokenIssuer) WithClock(now func() time.Time) *TokenIssuer {
	t.now = now
	return t
}

// IssueAccessToken signs an access token for a user.
//
// The returned token and its expiry are what the client receives. Nothing about
// the user is trusted from the request; the caller supplies the values loaded
// from the database.
func (t *TokenIssuer) IssueAccessToken(userID uuid.UUID, email string) (string, time.Time, error) {
	if userID == uuid.Nil {
		return "", time.Time{}, ErrTokenSubject
	}

	now := t.now().UTC()
	expiry := now.Add(t.accessExpiration)

	claims := &AccessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    t.accessIssuer,
			Subject:   userID.String(),
			ID:        uuid.NewString(),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiry),
		},
		TokenType: TokenTypeAccess,
		Email:     email,
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.accessSecret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return signed, expiry, nil
}

// ParseAccessToken verifies an access token's signature and claims.
//
// Every failure mode maps onto a sentinel so a handler can decide how much to
// reveal. The algorithm is pinned to HMAC: accepting whatever the token's own
// header advertises is the classic JWT confusion vulnerability.
func (t *TokenIssuer) ParseAccessToken(raw string) (*AccessClaims, error) {
	if raw == "" {
		return nil, ErrTokenMalformed
	}

	claims := &AccessClaims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(tok *jwt.Token) (any, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("%w: unexpected signing method %v", ErrTokenSignature, tok.Header["alg"])
		}
		return t.accessSecret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(t.accessIssuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(t.now),
	)
	if err != nil {
		return nil, translateJWTError(err)
	}

	if claims.TokenType != TokenTypeAccess {
		return nil, ErrTokenType
	}
	return claims, nil
}

// translateJWTError maps jwt library errors onto this package's sentinels.
func translateJWTError(err error) error {
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return ErrTokenExpired
	case errors.Is(err, jwt.ErrTokenNotValidYet):
		return ErrTokenNotYetValid
	case errors.Is(err, jwt.ErrTokenSignatureInvalid):
		return ErrTokenSignature
	case errors.Is(err, jwt.ErrTokenInvalidIssuer):
		return ErrTokenIssuer
	case errors.Is(err, jwt.ErrTokenMalformed):
		return ErrTokenMalformed
	default:
		return fmt.Errorf("%w: %v", ErrTokenMalformed, err)
	}
}
