// Package httpx holds the shared HTTP conventions: the response envelope, the
// error codes every endpoint uses, and small helpers for binding requests.
//
// Keeping these in one place is what makes the API predictable for the mobile
// client: the same envelope on success and failure, the same error codes
// regardless of which handler produced them.
package httpx

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ErrorCode is a stable, machine-readable identifier for a failure.
//
// Codes are part of the API contract: the mobile client switches on them, so
// they must not change meaning between releases. Messages are for humans and
// may be reworded freely.
type ErrorCode string

const (
	CodeValidationFailed ErrorCode = "validation_failed"
	CodeInvalidRequest   ErrorCode = "invalid_request"
	CodeUnauthorized     ErrorCode = "unauthorized"
	CodeTokenExpired     ErrorCode = "token_expired"
	CodeTokenInvalid     ErrorCode = "token_invalid"
	CodeForbidden        ErrorCode = "forbidden"
	CodeNotFound         ErrorCode = "not_found"
	CodeConflict         ErrorCode = "conflict"
	CodeEmailTaken       ErrorCode = "email_taken"
	CodeWeakPassword     ErrorCode = "weak_password"
	CodeEmailNotVerified ErrorCode = "email_not_verified"
	CodeInvalidLogin     ErrorCode = "invalid_credentials"
	CodeRateLimited      ErrorCode = "rate_limited"
	CodeRequestTooLarge  ErrorCode = "request_too_large"
	CodeInternal         ErrorCode = "internal_error"
)

// Envelope is the top level shape of every JSON response.
type Envelope struct {
	Data any `json:"data,omitempty"`
	Meta any `json:"meta,omitempty"`
}

// ErrorBody is the error half of the envelope.
type ErrorBody struct {
	Code      ErrorCode         `json:"code"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
	RequestID string            `json:"requestId,omitempty"`
}

// ErrorEnvelope is the top level shape of every failed JSON response.
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

// APIError is an error carrying an HTTP status and a stable code.
type APIError struct {
	Status  int
	Code    ErrorCode
	Message string
	Fields  map[string]string
	// cause is the underlying error. It is logged but never sent to a client,
	// so internal detail does not leak.
	cause error
}

// Error implements error.
func (e *APIError) Error() string {
	if e.cause != nil {
		return e.Message + ": " + e.cause.Error()
	}
	return e.Message
}

// Unwrap exposes the underlying error to errors.Is and errors.As.
func (e *APIError) Unwrap() error { return e.cause }

// WithCause attaches an internal error that will be logged but not returned.
func (e *APIError) WithCause(err error) *APIError {
	e.cause = err
	return e
}

// WithFields attaches per-field validation messages.
func (e *APIError) WithFields(fields map[string]string) *APIError {
	e.Fields = fields
	return e
}

// Constructors for the errors handlers raise most often.

// BadRequest builds a 400.
func BadRequest(code ErrorCode, msg string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: code, Message: msg}
}

// Unauthorized builds a 401.
func Unauthorized(code ErrorCode, msg string) *APIError {
	return &APIError{Status: http.StatusUnauthorized, Code: code, Message: msg}
}

// NotFound builds a 404.
func NotFound(msg string) *APIError {
	return &APIError{Status: http.StatusNotFound, Code: CodeNotFound, Message: msg}
}

// Conflict builds a 409.
func Conflict(code ErrorCode, msg string) *APIError {
	return &APIError{Status: http.StatusConflict, Code: code, Message: msg}
}

// TooManyRequests builds a 429.
func TooManyRequests(msg string) *APIError {
	return &APIError{Status: http.StatusTooManyRequests, Code: CodeRateLimited, Message: msg}
}

// Internal builds a 500 whose cause stays server side.
func Internal(msg string, cause error) *APIError {
	return &APIError{
		Status:  http.StatusInternalServerError,
		Code:    CodeInternal,
		Message: msg,
		cause:   cause,
	}
}

// JSON writes a successful response.
func JSON(c *gin.Context, status int, data any) {
	c.JSON(status, Envelope{Data: data})
}

// Created writes a 201 response.
func Created(c *gin.Context, data any) { JSON(c, http.StatusCreated, data) }

// OK writes a 200 response.
func OK(c *gin.Context, data any) { JSON(c, http.StatusOK, data) }

// Fail writes an error response.
//
// An *APIError is used as authored. Anything else is reported as a generic 500:
// an unexpected error type must never put its own text in front of a client.
func Fail(c *gin.Context, err error) {
	apiErr, ok := asAPIError(err)
	if !ok {
		apiErr = Internal("Something went wrong. Please try again.", err)
	}

	if apiErr.Status >= http.StatusInternalServerError {
		if log := loggerFrom(c); log != nil {
			log.Error("request failed",
				"error", apiErr.cause,
				"path", c.Request.URL.Path,
				"method", c.Request.Method,
			)
		}
	}

	c.AbortWithStatusJSON(apiErr.Status, ErrorEnvelope{Error: ErrorBody{
		Code:      apiErr.Code,
		Message:   apiErr.Message,
		Fields:    apiErr.Fields,
		RequestID: RequestID(c),
	}})
}

// asAPIError normalises any error into an *APIError.
func asAPIError(err error) (*APIError, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}

// loggerKey is where the request logger is stashed on the gin context.
const loggerKey = "httpx.logger"

// WithLogger stores a logger on the request context.
func WithLogger(c *gin.Context, log *slog.Logger) { c.Set(loggerKey, log) }

// loggerFrom retrieves the request logger, if the logging middleware ran.
func loggerFrom(c *gin.Context) *slog.Logger {
	if v, ok := c.Get(loggerKey); ok {
		if log, ok := v.(*slog.Logger); ok {
			return log
		}
	}
	return nil
}

// requestIDKey is where the correlation id is stashed on the gin context.
const requestIDKey = "httpx.request_id"

// WithRequestID stores a correlation id on the request context.
func WithRequestID(c *gin.Context, id string) { c.Set(requestIDKey, id) }

// RequestID returns the correlation id for the current request.
func RequestID(c *gin.Context) string {
	if v, ok := c.Get(requestIDKey); ok {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return ""
}

// currentUserKey is where the verified caller id is stashed on the gin context.
//
// The accessors live here rather than in the middleware package because both
// sides need them: the auth guard writes the id, and the profile handlers read
// it. The middleware package imports the auth package, and auth imports the
// profile package, so putting the accessor in middleware would make the
// handlers depend on middleware and close an import cycle.
const currentUserKey = "httpx.current_user"

// WithCurrentUser stores the verified caller id on the request context.
func WithCurrentUser(c *gin.Context, id uuid.UUID) { c.Set(currentUserKey, id) }

// CurrentUser returns the authenticated caller's id.
//
// It reports false when the route was not mounted behind the auth guard, so a
// handler cannot accidentally treat an anonymous request as an identified one.
func CurrentUser(c *gin.Context) (uuid.UUID, bool) {
	v, ok := c.Get(currentUserKey)
	if !ok {
		return uuid.Nil, false
	}
	id, ok := v.(uuid.UUID)
	return id, ok && id != uuid.Nil
}
