package middleware

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tripvault/backend/internal/auth"
	"github.com/tripvault/backend/internal/httpx"
)

// TokenVerifier checks an access token. It is an interface so the middleware
// does not depend on the concrete token issuer.
type TokenVerifier interface {
	ParseAccessToken(raw string) (*auth.AccessClaims, error)
}

// RequireAuth rejects requests without a valid access token.
//
// Identity is taken from the verified token, never from a header, body field or
// query parameter the client controls. That is the rule the whole authorisation
// story rests on: a caller cannot become another user by editing a request.
func RequireAuth(verifier TokenVerifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, err := bearerToken(c)
		if err != nil {
			httpx.Fail(c, err)
			return
		}

		claims, err := verifier.ParseAccessToken(raw)
		if err != nil {
			httpx.Fail(c, tokenError(err))
			return
		}

		userID, err := claims.UserID()
		if err != nil {
			httpx.Fail(c, httpx.Unauthorized(httpx.CodeTokenInvalid, "Your session is not valid."))
			return
		}

		httpx.WithCurrentUser(c, userID)
		c.Next()
	}
}

// bearerToken extracts the token from the Authorization header.
//
// Only the Bearer scheme is accepted. Accepting a bare token, or a scheme the
// client invented, would widen the ways a credential can be misread.
func bearerToken(c *gin.Context) (string, error) {
	header := c.GetHeader("Authorization")
	if header == "" {
		return "", httpx.Unauthorized(httpx.CodeUnauthorized, "Authentication is required.")
	}

	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", httpx.Unauthorized(httpx.CodeUnauthorized, "Authorization header must use the Bearer scheme.")
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return "", httpx.Unauthorized(httpx.CodeUnauthorized, "Authentication is required.")
	}
	return token, nil
}

// tokenError maps a verification failure onto a response.
//
// An expired token is called out separately so the mobile client knows to
// refresh and retry rather than sending the user back to the login screen.
func tokenError(err error) error {
	switch {
	case errors.Is(err, auth.ErrTokenExpired):
		return httpx.Unauthorized(httpx.CodeTokenExpired, "Your session has expired.")
	default:
		return httpx.Unauthorized(httpx.CodeTokenInvalid, "Your session is not valid.")
	}
}
