// Package auth implements authentication: password hashing, token issue and
// rotation, and the HTTP surface for the auth routes.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/tripvault/backend/internal/config"
)

// Errors returned by password handling. They are deliberately coarse: a caller
// must not be able to distinguish "no such user" from "wrong password".
var (
	// ErrInvalidHash means the stored hash was not produced by this package and
	// cannot be verified. It signals corrupt data, not a bad password.
	ErrInvalidHash = errors.New("auth: password hash is malformed")
	// ErrPasswordMismatch means the password did not match the stored hash.
	ErrPasswordMismatch = errors.New("auth: password does not match")
	// ErrEmptyPassword means a password was required but not supplied.
	ErrEmptyPassword = errors.New("auth: password must not be empty")
)

// dummyHashParams mirrors the cost of a real verification so DummyVerify does
// the same amount of work as a genuine attempt.
var dummyHashParams = HashParams{Time: 3, Memory: 64 * 1024, Threads: 2, SaltLen: 16, KeyLen: 32}

// DummyVerify performs a throwaway password verification.
//
// The login flow calls this when no account matches the supplied email, so the
// request costs roughly the same whether or not the account exists. Without it,
// a caller could enumerate registered emails purely from response latency.
func DummyVerify(password string) {
	if password == "" {
		return
	}
	// A fixed, well-formed hash of a random value. Verification always fails;
	// only the work done matters here.
	const placeholder = "$argon2id$v=19$m=65536,t=3,p=2$Y2FudG9yYmFzZTAxMjM0NTY3$Y2FudG9yYmFzZTAxMjM0NTY3NDU2Nzg5MDEyMw"
	_, _ = VerifyPassword(password, placeholder)
}

// HashParams are the Argon2id parameters used for hashing and verification.
type HashParams struct {
	Time    uint32
	Memory  uint32 // KiB
	Threads uint8
	SaltLen uint32
	KeyLen  uint32
}

// HashParamsFromConfig maps application configuration onto hashing parameters.
func HashParamsFromConfig(cfg config.Argon2) HashParams {
	return HashParams{
		Time:    cfg.Time,
		Memory:  cfg.Memory,
		Threads: cfg.Threads,
		SaltLen: cfg.SaltLen,
		KeyLen:  cfg.KeyLen,
	}
}

// HashPassword returns a PHC-formatted Argon2id hash using a fresh random salt.
//
// The output is self-describing (it embeds the parameters that produced it),
// so raising the cost later does not invalidate existing hashes: verification
// reads the parameters back out of the stored string.
func HashPassword(password string, params HashParams) (string, error) {
	if password == "" {
		return "", ErrEmptyPassword
	}
	if err := params.validate(); err != nil {
		return "", err
	}

	salt := make([]byte, params.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, params.Time, params.Memory, params.Threads, params.KeyLen)

	// PHC string format: $argon2id$v=19$m=<memory>,t=<time>,p=<threads>$<salt>$<hash>
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		params.Memory,
		params.Time,
		params.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether password matches the stored PHC hash.
//
// The comparison is constant time. Equalising the timing of a missing user
// with the timing of a wrong password is the caller's job, because only the
// caller knows whether a user exists: see DummyVerify and the login flow in
// service.go.
func VerifyPassword(password string, storedHash string) (bool, error) {
	if password == "" {
		return false, ErrEmptyPassword
	}

	params, salt, want, err := DecodeHash(storedHash)
	if err != nil {
		return false, err
	}

	got := argon2.IDKey([]byte(password), salt, params.Time, params.Memory, params.Threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) == 1 {
		return true, nil
	}
	return false, nil
}

// NeedsRehash reports whether storedHash was produced with weaker parameters
// than the current configuration, meaning the caller should re-hash on the
// next successful login.
func NeedsRehash(storedHash string, current HashParams) bool {
	params, _, _, err := DecodeHash(storedHash)
	if err != nil {
		// An unparsable hash will fail verification anyway; treat it as needing
		// a rehash rather than silently keeping it.
		return true
	}
	return params.Time < current.Time ||
		params.Memory < current.Memory ||
		params.Threads < current.Threads
}

// DecodeHash parses a PHC-formatted Argon2id hash into its parameters, salt and
// derived key.
func DecodeHash(storedHash string) (params HashParams, salt, key []byte, err error) {
	parts := strings.Split(storedHash, "$")
	if len(parts) != 6 || parts[0] != "" {
		return HashParams{}, nil, nil, ErrInvalidHash
	}
	if parts[1] != "argon2id" {
		// Only Argon2id is accepted. Argon2i and argon2d predate it and are not
		// what this service writes.
		return HashParams{}, nil, nil, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return HashParams{}, nil, nil, ErrInvalidHash
	}
	if version != argon2.Version {
		return HashParams{}, nil, nil, fmt.Errorf("%w: unsupported version %d", ErrInvalidHash, version)
	}

	params = HashParams{}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.Memory, &params.Time, &params.Threads); err != nil {
		return HashParams{}, nil, nil, ErrInvalidHash
	}

	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return HashParams{}, nil, nil, fmt.Errorf("%w: salt is not base64", ErrInvalidHash)
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return HashParams{}, nil, nil, fmt.Errorf("%w: key is not base64", ErrInvalidHash)
	}

	// A zero parameter would make argon2 panic or return a degenerate result.
	if params.Time < 1 || params.Memory < 8*1024 || params.Threads < 1 || len(salt) == 0 || len(key) == 0 {
		return HashParams{}, nil, nil, ErrInvalidHash
	}
	return params, salt, key, nil
}

// validate rejects parameter combinations argon2 cannot honour.
func (p HashParams) validate() error {
	switch {
	case p.Time < 1:
		return fmt.Errorf("auth: argon2 time must be at least 1, got %d", p.Time)
	case p.Memory < 8*1024:
		return fmt.Errorf("auth: argon2 memory must be at least 8192 KiB, got %d", p.Memory)
	case p.Threads < 1:
		return fmt.Errorf("auth: argon2 threads must be at least 1, got %d", p.Threads)
	case p.SaltLen < 8:
		return fmt.Errorf("auth: argon2 salt must be at least 8 bytes, got %d", p.SaltLen)
	case p.KeyLen < 16:
		return fmt.Errorf("auth: argon2 key must be at least 16 bytes, got %d", p.KeyLen)
	}
	return nil
}
