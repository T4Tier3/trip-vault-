// Package user holds the user domain model and its persistence layer.
package user

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tripvault/backend/internal/database"
)

// Errors returned by the repository. Callers translate these into transport
// errors; nothing above this package should inspect driver error types.
var (
	ErrNotFound     = errors.New("user: not found")
	ErrEmailTaken   = errors.New("user: email already registered")
	ErrInvalidEmail = errors.New("user: email is not valid")
	ErrNameTooLong  = errors.New("user: name is too long")
)

// Field length limits. They mirror the constraints in the schema and are
// checked before a query is issued so obviously bad input never reaches
// PostgreSQL.
const (
	MaxEmailLength = 254
	MaxNameLength  = 120
)

// User is a TripVault account.
//
// EmailVerified and DeletedAt are the two fields the rest of the system
// branches on, so they are part of the model rather than derived state.
type User struct {
	ID            uuid.UUID
	Email         string
	PasswordHash  string
	Name          string
	AvatarURL     *string
	EmailVerified bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
	DeletedAt     *time.Time
}

// IsDeleted reports whether the account has been soft deleted.
func (u *User) IsDeleted() bool { return u.DeletedAt != nil }

// Repository persists users.
//
// Every method takes a database.Execer rather than the pool, so callers can
// enlist a single read in a larger transaction without the repository knowing.
type Repository struct {
	db database.Execer
}

// NewRepository builds a Repository over db.
func NewRepository(db database.Execer) *Repository { return &Repository{db: db} }

// TxRunner exposes the underlying transaction-capable handle.
//
// A few operations (a password change, for instance) must update the user row
// and revoke sessions atomically, which needs more than a bare Execer. Callers
// outside this package use this to enlist that work in one transaction.
func (r *Repository) TxRunner() database.TxRunner {
	if tx, ok := r.db.(database.TxRunner); ok {
		return tx
	}
	return nil
}

// NormalizeEmail canonicalises an email for storage and lookup.
//
// Addresses are compared case-insensitively in practice, so storing one
// canonical form is what makes a unique index on email correct. The schema
// enforces the same invariant with a CHECK constraint.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidateEmail checks an email address is well formed enough to store.
func ValidateEmail(email string) error {
	normalized := NormalizeEmail(email)
	switch {
	case normalized == "":
		return fmt.Errorf("%w: must not be empty", ErrInvalidEmail)
	case len(normalized) > MaxEmailLength:
		return fmt.Errorf("%w: exceeds %d characters", ErrInvalidEmail, MaxEmailLength)
	}

	// Deliberately permissive: full RFC 5322 is impractical, and the only
	// authority that matters is whether a verification email is delivered.
	at := strings.LastIndex(normalized, "@")
	if at <= 0 || at == len(normalized)-1 {
		return fmt.Errorf("%w: missing local or domain part", ErrInvalidEmail)
	}

	local, domain := normalized[:at], normalized[at+1:]
	if strings.ContainsAny(local, " \t\r\n,;<>\"") || strings.Contains(local, "..") {
		return fmt.Errorf("%w: invalid local part", ErrInvalidEmail)
	}
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return fmt.Errorf("%w: invalid domain", ErrInvalidEmail)
	}
	if strings.ContainsAny(domain, " \t\r\n,;<>\"@") {
		return fmt.Errorf("%w: invalid domain", ErrInvalidEmail)
	}
	return nil
}

// ValidateName checks a display name is usable.
func ValidateName(name string) error {
	trimmed := strings.TrimSpace(name)
	switch {
	case trimmed == "":
		return fmt.Errorf("%w: must not be empty", ErrNameTooLong)
	case len([]rune(trimmed)) > MaxNameLength:
		return fmt.Errorf("%w: exceeds %d characters", ErrNameTooLong, MaxNameLength)
	}
	return nil
}

// CreateParams are the inputs for creating a user.
type CreateParams struct {
	Email        string
	PasswordHash string
	Name         string
}

// Create inserts a new user and returns it as stored.
//
// Email is normalised here rather than trusting the caller, because uniqueness
// is enforced by an index on the stored value.
func (r *Repository) Create(ctx context.Context, params CreateParams) (*User, error) {
	email := NormalizeEmail(params.Email)
	if err := ValidateEmail(email); err != nil {
		return nil, err
	}
	if err := ValidateName(params.Name); err != nil {
		return nil, err
	}
	if strings.TrimSpace(params.PasswordHash) == "" {
		return nil, errors.New("user: password hash must not be empty")
	}

	const query = `
		INSERT INTO users (email, password_hash, name)
		VALUES ($1, $2, $3)
		RETURNING id, email, password_hash, name, avatar_url,
		          email_verified, created_at, updated_at, deleted_at`

	u, err := scanUser(r.db.QueryRow(ctx, query, email, params.PasswordHash, strings.TrimSpace(params.Name)))
	if err != nil {
		// Map the unique violation onto a domain error so callers never parse
		// PostgreSQL error text.
		if database.IsUniqueViolation(err, "users_email_unique") {
			return nil, ErrEmailTaken
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}
	return u, nil
}

// GetByID loads a user by primary key, excluding soft-deleted accounts.
func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*User, error) {
	const query = `
		SELECT id, email, password_hash, name, avatar_url,
		       email_verified, created_at, updated_at, deleted_at
		FROM users
		WHERE id = $1 AND deleted_at IS NULL`

	u, err := scanUser(r.db.QueryRow(ctx, query, id))
	if errors.Is(err, database.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	return u, nil
}

// GetByEmail loads a user by email, excluding soft-deleted accounts.
//
// This is the login lookup. It returns the password hash, so the value must
// never be logged or serialised into a response.
func (r *Repository) GetByEmail(ctx context.Context, email string) (*User, error) {
	const query = `
		SELECT id, email, password_hash, name, avatar_url,
		       email_verified, created_at, updated_at, deleted_at
		FROM users
		WHERE email = $1 AND deleted_at IS NULL`

	u, err := scanUser(r.db.QueryRow(ctx, query, NormalizeEmail(email)))
	if errors.Is(err, database.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return u, nil
}

// UpdatePassword replaces the stored password hash and invalidates every
// outstanding session.
//
// Revoking refresh tokens in the same transaction as the update is what makes
// a password change actually revoke access: otherwise an attacker holding a
// stolen refresh token keeps their session until it expires.
func (r *Repository) UpdatePassword(ctx context.Context, userID uuid.UUID, passwordHash string, tx database.TxRunner) (int64, error) {
	var revoked int64

	err := tx.InTx(ctx, func(txDB database.Execer) error {
		tag, err := txDB.Exec(ctx,
			`UPDATE users SET password_hash = $2 WHERE id = $1 AND deleted_at IS NULL`,
			userID, passwordHash)
		if err != nil {
			return fmt.Errorf("update password: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}

		tag, err = txDB.Exec(ctx,
			`UPDATE refresh_tokens
			 SET revoked_at = now(), revoked_reason = 'password_changed'
			 WHERE user_id = $1 AND revoked_at IS NULL`,
			userID)
		if err != nil {
			return fmt.Errorf("revoke sessions after password change: %w", err)
		}
		revoked = tag.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return revoked, nil
}

// UpdateProfile changes the mutable parts of a profile.
type UpdateProfileParams struct {
	Name      *string
	AvatarURL *string
}

// UpdateProfile applies a partial update to a user.
func (r *Repository) UpdateProfile(ctx context.Context, userID uuid.UUID, params UpdateProfileParams) (*User, error) {
	const query = `
		UPDATE users
		SET name = COALESCE($2, name),
		    avatar_url = COALESCE($3, avatar_url)
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING id, email, password_hash, name, avatar_url,
		          email_verified, created_at, updated_at, deleted_at`

	u, err := scanUser(r.db.QueryRow(ctx, query, userID, params.Name, params.AvatarURL))
	if errors.Is(err, database.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update profile: %w", err)
	}
	return u, nil
}

// MarkEmailVerified flags an account's email as verified.
func (r *Repository) MarkEmailVerified(ctx context.Context, userID uuid.UUID) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE users SET email_verified = TRUE WHERE id = $1 AND deleted_at IS NULL`,
		userID)
	if err != nil {
		return fmt.Errorf("mark email verified: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SoftDelete marks an account deleted without removing its audit trail.
func (r *Repository) SoftDelete(ctx context.Context, userID uuid.UUID) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE users SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL`,
		userID)
	if err != nil {
		return fmt.Errorf("soft delete user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// scanUser reads one user row. It is defined here so every query above returns
// identical shapes and a schema change is a one-place edit.
func scanUser(row interface{ Scan(dest ...any) error }) (*User, error) {
	var u User
	err := row.Scan(
		&u.ID,
		&u.Email,
		&u.PasswordHash,
		&u.Name,
		&u.AvatarURL,
		&u.EmailVerified,
		&u.CreatedAt,
		&u.UpdatedAt,
		&u.DeletedAt,
	)
	if err != nil {
		return nil, err
	}
	return &u, nil
}
