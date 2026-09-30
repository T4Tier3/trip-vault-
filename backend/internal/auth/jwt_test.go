package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/tripvault/backend/internal/config"
)

func testIssuer(t *testing.T) *TokenIssuer {
	t.Helper()
	return NewTokenIssuer(config.JWT{
		AccessSecret:     []byte("test-access-secret-that-is-long-enough-32"),
		AccessIssuer:     "tripvault-test",
		AccessExpiration: 15 * time.Minute,
	})
}

func TestIssueAndParseAccessToken_RoundTrip(t *testing.T) {
	issuer := testIssuer(t)
	userID := uuid.New()

	token, expiresAt, err := issuer.IssueAccessToken(userID, "saurabh@example.com")
	if err != nil {
		t.Fatalf("IssueAccessToken() error = %v", err)
	}
	if token == "" {
		t.Fatal("IssueAccessToken() returned an empty token")
	}
	if !expiresAt.After(time.Now()) {
		t.Errorf("expiry = %s, want a future time", expiresAt)
	}

	claims, err := issuer.ParseAccessToken(token)
	if err != nil {
		t.Fatalf("ParseAccessToken() error = %v", err)
	}
	if claims.TokenType != TokenTypeAccess {
		t.Errorf("TokenType = %q, want %q", claims.TokenType, TokenTypeAccess)
	}
	if claims.Email != "saurabh@example.com" {
		t.Errorf("Email = %q, want the issued address", claims.Email)
	}

	got, err := claims.UserID()
	if err != nil {
		t.Fatalf("UserID() error = %v", err)
	}
	if got != userID {
		t.Errorf("UserID() = %s, want %s", got, userID)
	}
}

func TestIssueAccessToken_RejectsNilSubject(t *testing.T) {
	if _, _, err := testIssuer(t).IssueAccessToken(uuid.Nil, "x@example.com"); err == nil {
		t.Error("IssueAccessToken(uuid.Nil) error = nil, want ErrTokenSubject")
	}
}

func TestParseAccessToken_RejectsExpired(t *testing.T) {
	issuer := testIssuer(t)
	past := time.Now().Add(-2 * time.Hour)

	// A clock in the past issues a token that is already expired.
	issuer.WithClock(func() time.Time { return past })

	token, _, err := issuer.IssueAccessToken(uuid.New(), "saurabh@example.com")
	if err != nil {
		t.Fatalf("IssueAccessToken() error = %v", err)
	}

	issuer.WithClock(time.Now)
	if _, err := issuer.ParseAccessToken(token); err == nil {
		t.Error("ParseAccessToken() error = nil, want ErrTokenExpired")
	}
}

func TestParseAccessToken_RejectsWrongSecret(t *testing.T) {
	issuer := testIssuer(t)
	token, _, err := issuer.IssueAccessToken(uuid.New(), "saurabh@example.com")
	if err != nil {
		t.Fatalf("IssueAccessToken() error = %v", err)
	}

	attacker := NewTokenIssuer(config.JWT{
		AccessSecret:     []byte("a-completely-different-secret-value!!"),
		AccessIssuer:     "tripvault-test",
		AccessExpiration: 15 * time.Minute,
	})

	if _, err := attacker.ParseAccessToken(token); err == nil {
		t.Error("ParseAccessToken() error = nil, want a signature error")
	}
}

func TestParseAccessToken_RejectsForeignIssuer(t *testing.T) {
	issuer := testIssuer(t)
	token, _, err := issuer.IssueAccessToken(uuid.New(), "saurabh@example.com")
	if err != nil {
		t.Fatalf("IssueAccessToken() error = %v", err)
	}

	// Same secret, different issuer: a token minted for another service in the
	// same trust domain must not be accepted.
	other := NewTokenIssuer(config.JWT{
		AccessSecret:     []byte("test-access-secret-that-is-long-enough-32"),
		AccessIssuer:     "some-other-service",
		AccessExpiration: 15 * time.Minute,
	})

	if _, err := other.ParseAccessToken(token); err == nil {
		t.Error("ParseAccessToken() error = nil, want an issuer error")
	}
}

// TestParseAccessToken_RejectsAlgNone is the classic JWT confusion attack: a
// token claiming "alg":"none" must never verify, because that would let anyone
// mint a valid token.
func TestParseAccessToken_RejectsAlgNone(t *testing.T) {
	issuer := testIssuer(t)

	claims := AccessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "tripvault-test",
			Subject:   uuid.NewString(),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
		TokenType: TokenTypeAccess,
	}
	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("build unsigned token: %v", err)
	}

	if _, err := issuer.ParseAccessToken(unsigned); err == nil {
		t.Error("ParseAccessToken() accepted an unsigned token, want rejection")
	}
}

// TestParseAccessToken_RejectsRefreshTypedJWT proves the token type claim is
// enforced, so a credential minted for another purpose cannot be replayed as an
// access token.
func TestParseAccessToken_RejectsRefreshTypedJWT(t *testing.T) {
	issuer := testIssuer(t)

	claims := AccessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "tripvault-test",
			Subject:   uuid.NewString(),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		TokenType: "refresh",
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(issuer.accessSecret)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	if _, err := issuer.ParseAccessToken(signed); err == nil {
		t.Error("ParseAccessToken() accepted a refresh-typed token, want rejection")
	}
}

func TestParseAccessToken_RejectsMalformed(t *testing.T) {
	tests := []string{
		"",
		"not.a.token",
		"a.b",
		"a.b.c.d",
		"eyJhbGciOiJIUzI1NiJ9",
	}

	for _, raw := range tests {
		if _, err := testIssuer(t).ParseAccessToken(raw); err == nil {
			t.Errorf("ParseAccessToken(%q) error = nil, want rejection", raw)
		}
	}
}

func TestAccessClaims_UserIDRejectsBadSubject(t *testing.T) {
	claims := &AccessClaims{}
	claims.Subject = ""
	if _, err := claims.UserID(); err == nil {
		t.Error("UserID() error = nil for an empty subject")
	}

	claims.Subject = "not-a-uuid"
	if _, err := claims.UserID(); err == nil {
		t.Error("UserID() error = nil for a non-UUID subject")
	}
}

func TestGenerateOpaqueToken_IsUnpredictableAndHashable(t *testing.T) {
	first, err := GenerateOpaqueToken()
	if err != nil {
		t.Fatalf("GenerateOpaqueToken() error = %v", err)
	}
	second, err := GenerateOpaqueToken()
	if err != nil {
		t.Fatalf("GenerateOpaqueToken() error = %v", err)
	}

	if first == second {
		t.Error("two generated tokens are identical")
	}
	// base64url of 32 bytes is 43 characters.
	if len(first) != 43 {
		t.Errorf("token length = %d, want 43", len(first))
	}
	if strings.ContainsAny(first, "+/=") {
		t.Error("token is not URL-safe, it must survive being placed in a link unescaped")
	}

	// The stored value must not be the token itself.
	hash := HashOpaqueToken(first)
	if hash == first {
		t.Error("HashOpaqueToken() returned the token unchanged")
	}
	if len(hash) != 64 {
		t.Errorf("hash length = %d, want 64 hex characters", len(hash))
	}
	if HashOpaqueToken(first) != hash {
		t.Error("HashOpaqueToken() is not deterministic")
	}

	if !ConstantTimeTokenEqual(first, hash) {
		t.Error("ConstantTimeTokenEqual() = false for the matching token")
	}
	if ConstantTimeTokenEqual(second, hash) {
		t.Error("ConstantTimeTokenEqual() = true for a different token")
	}
}
