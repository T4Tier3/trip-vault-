package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// TokenTypeEmailVerification and TokenTypePasswordReset are the purposes an
// opaque token can carry. A token is bound to one purpose at issue time and
// can never be replayed as the other.
const (
	TokenTypeEmailVerification = "email_verification"
	TokenTypePasswordReset     = "password_reset"
)

// opaqueTokenBytes is the entropy behind an emailed token. 32 bytes gives a
// 256-bit token, which is not guessable even though tokens are stored hashed
// and could otherwise be brute forced offline.
const opaqueTokenBytes = 32

// GenerateOpaqueToken returns a URL-safe random token for email verification or
// password reset.
//
// The value is only ever shown to its owner. What gets persisted is
// HashOpaqueToken, so a database dump does not yield usable reset links.
func GenerateOpaqueToken() (string, error) {
	buf := make([]byte, opaqueTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashOpaqueToken returns the SHA-256 hex digest of a token for storage.
//
// SHA-256 rather than a password hash is correct here: the input is 256 bits of
// CSPRNG output, so there is no dictionary to attack, and verification happens
// as a single indexed lookup on the digest column. Argon2 on a random token
// would only add latency to every verification.
func HashOpaqueToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ConstantTimeTokenEqual compares a presented token against a stored digest in
// constant time.
func ConstantTimeTokenEqual(presented, storedHash string) bool {
	return subtle.ConstantTimeCompare([]byte(HashOpaqueToken(presented)), []byte(storedHash)) == 1
}
