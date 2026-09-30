package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/tripvault/backend/internal/database"
)

// Repository-level errors for token handling.
var (
	ErrTokenNotFound    = errors.New("auth: token not found")
	ErrTokenConsumed    = errors.New("auth: token already used")
	ErrAuthTokenExpired = errors.New("auth: token expired")
	ErrTokenReused      = errors.New("auth: refresh token already rotated")
	ErrTokenRevoked     = errors.New("auth: token revoked")
)

// TokenRecord is a stored email verification or password reset token.
type TokenRecord struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	Purpose    string
	TokenHash  string
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	CreatedAt  time.Time
}

// RefreshTokenRecord is a stored rotating refresh token.
type RefreshTokenRecord struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	FamilyID      uuid.UUID
	TokenHash     string
	DeviceID      *uuid.UUID
	ExpiresAt     time.Time
	RotatedAt     *time.Time
	RevokedAt     *time.Time
	RevokedReason *string
	CreatedAt     time.Time
}

// TokenRepository persists auth tokens and refresh tokens.
//
// Every method takes a database.Execer so it can be enlisted in a caller's
// transaction. Rotation in particular must be transactional: marking the old
// token rotated and inserting the new one together is what prevents a replay
// window from opening between the two writes.
type TokenRepository struct {
	db database.TxRunner
}

// NewTokenRepository builds a TokenRepository over db.
func NewTokenRepository(db database.TxRunner) *TokenRepository {
	return &TokenRepository{db: db}
}

// CreateAuthToken stores a hashed one-time token for a purpose.
func (r *TokenRepository) CreateAuthToken(ctx context.Context, userID uuid.UUID, purpose, tokenHash string, expiresAt time.Time) (*TokenRecord, error) {
	const query = `
		INSERT INTO auth_tokens (user_id, purpose, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, purpose, token_hash, expires_at, consumed_at, created_at`

	record, err := scanAuthToken(r.db.QueryRow(ctx, query, userID, purpose, tokenHash, expiresAt))
	if err != nil {
		return nil, fmt.Errorf("create auth token: %w", err)
	}
	return record, nil
}

// InvalidateAuthTokens marks every live token of a purpose as consumed.
//
// Issuing a new verification or reset email must retire the previous one,
// otherwise an older (possibly already forwarded) link stays usable.
func (r *TokenRepository) InvalidateAuthTokens(ctx context.Context, userID uuid.UUID, purpose string) (int64, error) {
	tag, err := r.db.Exec(ctx,
		`UPDATE auth_tokens
		 SET consumed_at = now()
		 WHERE user_id = $1 AND purpose = $2 AND consumed_at IS NULL`,
		userID, purpose)
	if err != nil {
		return 0, fmt.Errorf("invalidate auth tokens: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ConsumeAuthToken atomically claims a single-use token.
//
// The UPDATE is conditional on consumed_at IS NULL, so two concurrent requests
// presenting the same token cannot both succeed: the row is claimed by exactly
// one of them. Returns the token's user id.
func (r *TokenRepository) ConsumeAuthToken(ctx context.Context, tokenHash, purpose string) (uuid.UUID, error) {
	const query = `
		UPDATE auth_tokens
		SET consumed_at = now()
		WHERE token_hash = $1
		  AND purpose = $2
		  AND consumed_at IS NULL
		  AND expires_at > now()
		RETURNING user_id`

	var userID uuid.UUID
	err := r.db.QueryRow(ctx, query, tokenHash, purpose).Scan(&userID)
	switch {
	case errors.Is(err, database.ErrNoRows):
		// Distinguish "never existed" from "already used or expired" so the
		// handler can respond without confirming the token was real.
		return uuid.Nil, r.classifyTokenFailure(ctx, tokenHash, purpose)
	case err != nil:
		return uuid.Nil, fmt.Errorf("consume auth token: %w", err)
	}
	return userID, nil
}

// classifyTokenFailure explains why a token could not be consumed.
func (r *TokenRepository) classifyTokenFailure(ctx context.Context, tokenHash, purpose string) error {
	var (
		consumedAt *time.Time
		expiresAt  time.Time
		purposeCol string
	)
	err := r.db.QueryRow(ctx,
		`SELECT consumed_at, expires_at, purpose FROM auth_tokens WHERE token_hash = $1`,
		tokenHash).Scan(&consumedAt, &expiresAt, &purposeCol)
	if errors.Is(err, database.ErrNoRows) {
		return ErrTokenNotFound
	}
	if err != nil {
		return fmt.Errorf("classify auth token failure: %w", err)
	}

	// A token presented under the wrong purpose must be reported as not found.
	// Reporting the real reason would tell an attacker which token types exist.
	if purposeCol != purpose {
		return ErrTokenNotFound
	}
	if consumedAt != nil {
		return ErrTokenConsumed
	}
	if !expiresAt.After(time.Now()) {
		return ErrAuthTokenExpired
	}
	return ErrTokenNotFound
}

// CreateRefreshToken stores a new refresh token.
//
// familyID ties every token descended from one login together, which is what
// makes reuse detection possible.
func (r *TokenRepository) CreateRefreshToken(ctx context.Context, rec RefreshTokenRecord) (*RefreshTokenRecord, error) {
	const query = `
		INSERT INTO refresh_tokens (user_id, family_id, token_hash, device_id, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, user_id, family_id, token_hash, device_id, expires_at,
		          rotated_at, revoked_at, revoked_reason, created_at`

	record, err := scanRefreshToken(r.db.QueryRow(ctx, query,
		rec.UserID, rec.FamilyID, rec.TokenHash, rec.DeviceID, rec.ExpiresAt))
	if err != nil {
		return nil, fmt.Errorf("create refresh token: %w", err)
	}
	return record, nil
}

// GetRefreshToken loads a refresh token by hash, including revoked and expired
// ones.
//
// Callers must inspect the returned state: a revoked or expired token is
// returned rather than hidden so that reuse detection can act on it.
func (r *TokenRepository) GetRefreshToken(ctx context.Context, tokenHash string) (*RefreshTokenRecord, error) {
	const query = `
		SELECT id, user_id, family_id, token_hash, device_id, expires_at,
		       rotated_at, revoked_at, revoked_reason, created_at
		FROM refresh_tokens
		WHERE token_hash = $1`

	record, err := scanRefreshToken(r.db.QueryRow(ctx, query, tokenHash))
	if errors.Is(err, database.ErrNoRows) {
		return nil, ErrTokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get refresh token: %w", err)
	}
	return record, nil
}

// RotateRefreshToken swaps one live token for its successor, atomically.
//
// The UPDATE is conditional on the token still being live and unrotated, so a
// replayed token finds nothing to claim and is reported as reuse. If any row
// was updated the returned bool is true.
func (r *TokenRepository) RotateRefreshToken(ctx context.Context, tokenHash string, next *RefreshTokenRecord) (bool, error) {
	var rotated bool

	err := r.db.InTx(ctx, func(tx database.Execer) error {
		tag, err := tx.Exec(ctx,
			`UPDATE refresh_tokens
			 SET rotated_at = now()
			 WHERE token_hash = $1
			   AND rotated_at IS NULL
			   AND revoked_at IS NULL
			   AND expires_at > now()`,
			tokenHash)
		if err != nil {
			return fmt.Errorf("mark refresh token rotated: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Nothing was live; leave the transaction without inserting.
			return nil
		}
		rotated = true

		const insert = `
			INSERT INTO refresh_tokens (user_id, family_id, token_hash, device_id, expires_at)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id, user_id, family_id, token_hash, device_id, expires_at,
			          rotated_at, revoked_at, revoked_reason, created_at`
		if _, err := scanRefreshToken(tx.QueryRow(ctx, insert,
			next.UserID, next.FamilyID, next.TokenHash, next.DeviceID, next.ExpiresAt)); err != nil {
			return fmt.Errorf("insert rotated refresh token: %w", err)
		}
		return nil
	})
	return rotated, err
}

// RevokeRefreshToken revokes a single token, for logout.
func (r *TokenRepository) RevokeRefreshToken(ctx context.Context, tokenHash, reason string) (int64, error) {
	tag, err := r.db.Exec(ctx,
		`UPDATE refresh_tokens
		 SET revoked_at = now(), revoked_reason = $2
		 WHERE token_hash = $1 AND revoked_at IS NULL`,
		tokenHash, reason)
	if err != nil {
		return 0, fmt.Errorf("revoke refresh token: %w", err)
	}
	return tag.RowsAffected(), nil
}

// RevokeFamily revokes every token descended from one login.
//
// This is the response to reuse detection: if a token that was already rotated
// is presented again, either the token was stolen or the legitimate client is
// replaying an old value. Either way the safe action is to end the session.
func (r *TokenRepository) RevokeFamily(ctx context.Context, familyID uuid.UUID, reason string) (int64, error) {
	tag, err := r.db.Exec(ctx,
		`UPDATE refresh_tokens
		 SET revoked_at = now(), revoked_reason = $2
		 WHERE family_id = $1 AND revoked_at IS NULL`,
		familyID, reason)
	if err != nil {
		return 0, fmt.Errorf("revoke token family: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteExpired prunes tokens that can no longer do anything. Safe to call on a
// schedule from the worker.
func (r *TokenRepository) DeleteExpired(ctx context.Context) (authTokens int64, refreshTokens int64, err error) {
	authTag, err := r.db.Exec(ctx, `DELETE FROM auth_tokens WHERE expires_at < now()`)
	if err != nil {
		return 0, 0, fmt.Errorf("prune auth tokens: %w", err)
	}
	refreshTag, err := r.db.Exec(ctx, `DELETE FROM refresh_tokens WHERE expires_at < now()`)
	if err != nil {
		return 0, 0, fmt.Errorf("prune refresh tokens: %w", err)
	}
	return authTag.RowsAffected(), refreshTag.RowsAffected(), nil
}

// scanAuthToken reads one auth_tokens row.
func scanAuthToken(row interface{ Scan(dest ...any) error }) (*TokenRecord, error) {
	var rec TokenRecord
	err := row.Scan(
		&rec.ID,
		&rec.UserID,
		&rec.Purpose,
		&rec.TokenHash,
		&rec.ExpiresAt,
		&rec.ConsumedAt,
		&rec.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// scanRefreshToken reads one refresh_tokens row.
func scanRefreshToken(row interface{ Scan(dest ...any) error }) (*RefreshTokenRecord, error) {
	var rec RefreshTokenRecord
	err := row.Scan(
		&rec.ID,
		&rec.UserID,
		&rec.FamilyID,
		&rec.TokenHash,
		&rec.DeviceID,
		&rec.ExpiresAt,
		&rec.RotatedAt,
		&rec.RevokedAt,
		&rec.RevokedReason,
		&rec.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}
