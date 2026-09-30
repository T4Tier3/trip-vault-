package auth

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Password policy limits. The rules are deliberately strength-based rather than
// composition-based: a long passphrase beats a short string with a symbol, and
// forcing symbol rules pushes people toward predictable substitutions.
const (
	// MinPasswordLength is the shortest accepted password.
	MinPasswordLength = 10
	// MaxPasswordLength bounds the input. Argon2 cost is independent of length,
	// but an unbounded input is an easy memory and CPU denial of service.
	MaxPasswordLength = 256
	// MaxPasswordBytes rejects pathological multi-byte padding before hashing.
	MaxPasswordBytes = 1024
)

// ErrWeakPassword is returned when a password fails validation.
var ErrWeakPassword = errors.New("auth: password does not meet requirements")

// PasswordPolicyError names the specific rule that failed, so the client can
// show actionable guidance instead of a generic rejection.
type PasswordPolicyError struct {
	Rule string
}

// Message returns the human-readable explanation without the internal sentinel
// prefix. This is what reaches the client; Error() stays for logs.
func (e *PasswordPolicyError) Message() string {
	switch e.Rule {
	case "length":
		return fmt.Sprintf("Must be at least %d characters.", MinPasswordLength)
	case "max_length":
		return fmt.Sprintf("Must be at most %d characters.", MaxPasswordLength)
	case "bytes":
		return "Is too long to process."
	case "variety":
		return "Must mix letters, digits or symbols."
	case "common":
		return "This password appears in common breach lists."
	default:
		return "Does not meet requirements."
	}
}

func (e *PasswordPolicyError) Error() string {
	return ErrWeakPassword.Error() + ": " + e.Message()
}

// Unwrap lets callers match ErrWeakPassword with errors.Is.
func (e *PasswordPolicyError) Unwrap() error { return ErrWeakPassword }

// commonPasswords is a deliberately small denylist.
//
// It is not a complete breach corpus - that would mean shipping megabytes of
// hashes and keeping them updated. This covers the passwords that dominate
// credential stuffing, which a length-only policy would otherwise accept.
var commonPasswords = map[string]struct{}{
	"password":     {},
	"password1":    {},
	"password123":  {},
	"12345678":     {},
	"123456789":    {},
	"1234567890":   {},
	"qwerty123":    {},
	"qwertyuiop":   {},
	"iloveyou":     {},
	"letmein123":   {},
	"welcome123":   {},
	"admin12345":   {},
	"tripvault":    {},
	"tripvault1":   {},
	"changeme123":  {},
	"trustno1234":  {},
	"monkey12345":  {},
	"football1234": {},
	"baseball1234": {},
	"sunshine1234": {},
	"princess1234": {},
	"dragon12345":  {},
	"starwars1234": {},
	"p@ssw0rd123":  {},
	"p@ssword123":  {},
}

// ValidatePasswordStrength checks a candidate password against the policy.
//
// It never reports how close a password came to passing: returning "needs one
// more symbol" tells an attacker exactly which rule is missing.
func ValidatePasswordStrength(password string) error {
	switch {
	case password == "":
		return &PasswordPolicyError{Rule: "length"}
	case len(password) > MaxPasswordLength:
		return &PasswordPolicyError{Rule: "max_length"}
	case len(password) > MaxPasswordBytes:
		return &PasswordPolicyError{Rule: "bytes"}
	case utf8RuneCount(password) < MinPasswordLength:
		return &PasswordPolicyError{Rule: "length"}
	}

	if _, common := commonPasswords[strings.ToLower(password)]; common {
		return &PasswordPolicyError{Rule: "common"}
	}

	if variety(password) < 2 {
		return &PasswordPolicyError{Rule: "variety"}
	}
	return nil
}

// utf8RuneCount counts characters rather than bytes, so a password of
// non-ASCII characters is measured the way a person counts it.
func utf8RuneCount(s string) int { return len([]rune(s)) }

// variety counts how many character classes a password touches. Two is the
// threshold: requiring three would reject a perfectly strong long passphrase
// built from words.
func variety(password string) int {
	var hasLower, hasUpper, hasDigit, hasSymbol bool

	for _, r := range password {
		switch {
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsDigit(r):
			hasDigit = true
		default:
			hasSymbol = true
		}
	}

	count := 0
	for _, present := range []bool{hasLower, hasUpper, hasDigit, hasSymbol} {
		if present {
			count++
		}
	}
	return count
}
