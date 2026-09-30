package auth

import (
	"errors"
	"io"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/tripvault/backend/internal/httpx"
	"github.com/tripvault/backend/internal/user"
)

// Handler serves the authentication routes.
type Handler struct {
	service *Service
}

// NewHandler builds an auth Handler over service.
func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// RegisterRoutesOn mounts the auth endpoints onto group.
//
// The caller owns the group so it can attach route-specific middleware (the
// stricter credential-endpoint rate limit) before the routes exist. Mounting
// onto /api/v1/auth directly avoids a second nested /auth segment.
//
// Every route here is unauthenticated by definition; the refresh route carries
// its own credential in the body instead of a bearer header, because an expired
// access token is exactly the situation it exists to handle.
func (h *Handler) RegisterRoutesOn(group *gin.RouterGroup) {
	group.POST("/register", h.Register)
	group.POST("/login", h.Login)
	group.POST("/refresh", h.Refresh)
	group.POST("/logout", h.Logout)
	group.POST("/verify-email", h.VerifyEmail)
	group.POST("/resend-verification", h.ResendVerification)
	group.POST("/forgot-password", h.ForgotPassword)
	group.POST("/reset-password", h.ResetPassword)
}

// ---------------------------------------------------------------------------
// Request payloads. Each declares its own rules next to its own fields.
// ---------------------------------------------------------------------------

// RegisterRequest is the body of POST /auth/register.
type RegisterRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

// ValidationFields declares the register rules.
func (r RegisterRequest) ValidationFields() map[string]httpx.FieldRule {
	return map[string]httpx.FieldRule{
		"email": {Required: true, Email: true, MaxLength: user.MaxEmailLength},
		// The minimum length is expressed here as well as in the policy so a
		// short password is reported alongside the other field errors rather
		// than as a separate failure the client has to handle twice.
		"password": {Required: true, MinLength: MinPasswordLength, MaxLength: MaxPasswordLength},
		"name":     {Required: true, MinLength: 1, MaxLength: user.MaxNameLength},
	}
}

// ValidationValue returns one field's value.
func (r RegisterRequest) ValidationValue(field string) (httpx.FieldValue, bool) {
	switch field {
	case "email":
		return httpx.F(r.Email), true
	case "password":
		return httpx.F(r.Password), true
	case "name":
		return httpx.F(r.Name), true
	}
	return httpx.FieldValue{}, false
}

// LoginRequest is the body of POST /auth/login.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	DeviceID string `json:"deviceId"`
}

// ValidationFields declares the login rules.
func (r LoginRequest) ValidationFields() map[string]httpx.FieldRule {
	return map[string]httpx.FieldRule{
		"email":    {Required: true, Email: true, MaxLength: user.MaxEmailLength},
		"password": {Required: true, MinLength: MinPasswordLength, MaxLength: MaxPasswordLength},
		"deviceId": {UUID: true},
	}
}

// ValidationValue returns one field's value.
func (r LoginRequest) ValidationValue(field string) (httpx.FieldValue, bool) {
	switch field {
	case "email":
		return httpx.F(r.Email), true
	case "password":
		return httpx.F(r.Password), true
	case "deviceId":
		return httpx.OptF(r.DeviceID), true
	}
	return httpx.FieldValue{}, false
}

// RefreshRequest is the body of POST /auth/refresh.
type RefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// ValidationFields declares the refresh rules.
func (r RefreshRequest) ValidationFields() map[string]httpx.FieldRule {
	return map[string]httpx.FieldRule{
		"refreshToken": {Required: true, MinLength: 32, MaxLength: 512},
	}
}

// ValidationValue returns one field's value.
func (r RefreshRequest) ValidationValue(field string) (httpx.FieldValue, bool) {
	if field == "refreshToken" {
		return httpx.F(r.RefreshToken), true
	}
	return httpx.FieldValue{}, false
}

// LogoutRequest is the body of POST /auth/logout.
type LogoutRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// ValidationFields declares the logout rules.
func (r LogoutRequest) ValidationFields() map[string]httpx.FieldRule {
	return map[string]httpx.FieldRule{
		"refreshToken": {Required: true, MinLength: 32, MaxLength: 512},
	}
}

// ValidationValue returns one field's value.
func (r LogoutRequest) ValidationValue(field string) (httpx.FieldValue, bool) {
	if field == "refreshToken" {
		return httpx.F(r.RefreshToken), true
	}
	return httpx.FieldValue{}, false
}

// VerifyEmailRequest is the body of POST /auth/verify-email.
type VerifyEmailRequest struct {
	Token string `json:"token"`
}

// ValidationFields declares the verify-email rules.
func (r VerifyEmailRequest) ValidationFields() map[string]httpx.FieldRule {
	return map[string]httpx.FieldRule{
		"token": {Required: true, MinLength: 32, MaxLength: 512},
	}
}

// ValidationValue returns one field's value.
func (r VerifyEmailRequest) ValidationValue(field string) (httpx.FieldValue, bool) {
	if field == "token" {
		return httpx.F(r.Token), true
	}
	return httpx.FieldValue{}, false
}

// EmailRequest is the body of endpoints that only need an address.
type EmailRequest struct {
	Email string `json:"email"`
}

// ValidationFields declares the email rules.
func (r EmailRequest) ValidationFields() map[string]httpx.FieldRule {
	return map[string]httpx.FieldRule{
		"email": {Required: true, Email: true, MaxLength: user.MaxEmailLength},
	}
}

// ValidationValue returns one field's value.
func (r EmailRequest) ValidationValue(field string) (httpx.FieldValue, bool) {
	if field == "email" {
		return httpx.F(r.Email), true
	}
	return httpx.FieldValue{}, false
}

// ResetPasswordRequest is the body of POST /auth/reset-password.
type ResetPasswordRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// ValidationFields declares the reset-password rules.
func (r ResetPasswordRequest) ValidationFields() map[string]httpx.FieldRule {
	return map[string]httpx.FieldRule{
		"token":    {Required: true, MinLength: 32, MaxLength: 512},
		"password": {Required: true, MinLength: MinPasswordLength, MaxLength: MaxPasswordLength},
	}
}

// ValidationValue returns one field's value.
func (r ResetPasswordRequest) ValidationValue(field string) (httpx.FieldValue, bool) {
	switch field {
	case "token":
		return httpx.F(r.Token), true
	case "password":
		return httpx.F(r.Password), true
	}
	return httpx.FieldValue{}, false
}

// ---------------------------------------------------------------------------
// Responses
// ---------------------------------------------------------------------------

// RegisterResponse is returned by Register.
type RegisterResponse struct {
	UserID      uuid.UUID `json:"userId"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	NeedsVerify bool      `json:"needsVerification"`
	Message     string    `json:"message"`
}

// TokenResponse is returned by login and refresh.
type TokenResponse struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	TokenType    string    `json:"tokenType"`
	ExpiresAt    time.Time `json:"expiresAt"`
	ExpiresIn    int64     `json:"expiresIn"`
}

// AuthUserResponse is the public shape of a user. The password hash has no
// representation here by construction, not by omission: no response type
// includes it.
type AuthUserResponse struct {
	ID            uuid.UUID `json:"id"`
	Email         string    `json:"email"`
	Name          string    `json:"name"`
	AvatarURL     *string   `json:"avatarUrl,omitempty"`
	EmailVerified bool      `json:"emailVerified"`
	CreatedAt     time.Time `json:"createdAt"`
}

// LoginResponse bundles a session with the signed-in user.
type LoginResponse struct {
	Tokens TokenResponse    `json:"tokens"`
	User   AuthUserResponse `json:"user"`
}

// MessageResponse is the acknowledgement shape for endpoints with no payload.
type MessageResponse struct {
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// Register handles POST /auth/register.
func (h *Handler) Register(c *gin.Context) {
	var req RegisterRequest
	if !h.bind(c, &req) {
		return
	}

	result, err := h.service.Register(c.Request.Context(), RegisterInput{
		Email:    req.Email,
		Password: req.Password,
		Name:     req.Name,
	})
	if err != nil {
		h.fail(c, err)
		return
	}

	httpx.Created(c, RegisterResponse{
		UserID:      result.UserID,
		Email:       result.Email,
		Name:        result.Name,
		NeedsVerify: result.NeedsVerify,
		Message:     "Account created. Check your email to verify your address.",
	})
}

// Login handles POST /auth/login.
func (h *Handler) Login(c *gin.Context) {
	var req LoginRequest
	if !h.bind(c, &req) {
		return
	}

	deviceID, err := parseOptionalUUID(req.DeviceID)
	if err != nil {
		h.fail(c, httpx.BadRequest(httpx.CodeValidationFailed, "Some fields need attention.").
			WithFields(map[string]string{"deviceId": "Must be a valid identifier."}))
		return
	}

	result, err := h.service.Login(c.Request.Context(), LoginInput{
		Email:    req.Email,
		Password: req.Password,
		DeviceID: deviceID,
	})
	if err != nil {
		h.fail(c, err)
		return
	}

	account, err := h.service.users.GetByID(c.Request.Context(), result.UserID)
	if err != nil {
		h.fail(c, err)
		return
	}

	httpx.OK(c, LoginResponse{
		Tokens: toTokenResponse(result.Tokens),
		User:   toAuthUser(account),
	})
}

// Refresh handles POST /auth/refresh.
func (h *Handler) Refresh(c *gin.Context) {
	var req RefreshRequest
	if !h.bind(c, &req) {
		return
	}

	result, err := h.service.Refresh(c.Request.Context(), req.RefreshToken)
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, toTokenResponse(result.Tokens))
}

// Logout handles POST /auth/logout.
func (h *Handler) Logout(c *gin.Context) {
	var req LogoutRequest
	if !h.bind(c, &req) {
		return
	}

	if err := h.service.Logout(c.Request.Context(), req.RefreshToken); err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, MessageResponse{Message: "Signed out."})
}

// VerifyEmail handles POST /auth/verify-email.
//
// The token is also accepted as a query parameter, because the link in the
// verification email points at this endpoint and following it in a browser must
// work without a form post.
func (h *Handler) VerifyEmail(c *gin.Context) {
	var req VerifyEmailRequest

	token := c.Query("token")
	if token == "" {
		if !h.bind(c, &req) {
			return
		}
		token = req.Token
	}
	if strings.TrimSpace(token) == "" {
		h.fail(c, httpx.BadRequest(httpx.CodeValidationFailed, "Some fields need attention.").
			WithFields(map[string]string{"token": "This field is required."}))
		return
	}

	if err := h.service.VerifyEmail(c.Request.Context(), token); err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, MessageResponse{Message: "Email verified. You can sign in now."})
}

// ResendVerification handles POST /auth/resend-verification.
func (h *Handler) ResendVerification(c *gin.Context) {
	var req EmailRequest
	if !h.bind(c, &req) {
		return
	}

	if err := h.service.ResendVerificationEmail(c.Request.Context(), req.Email); err != nil {
		h.fail(c, err)
		return
	}
	// The response is identical whether or not the address exists, so this
	// endpoint cannot be used to discover who has an account.
	httpx.OK(c, MessageResponse{Message: "If that address needs verification, we have sent a link."})
}

// ForgotPassword handles POST /auth/forgot-password.
func (h *Handler) ForgotPassword(c *gin.Context) {
	var req EmailRequest
	if !h.bind(c, &req) {
		return
	}

	if err := h.service.ForgotPassword(c.Request.Context(), req.Email); err != nil {
		h.fail(c, err)
		return
	}
	// Identical response for known and unknown addresses, for the same reason.
	httpx.OK(c, MessageResponse{Message: "If that address has an account, we have sent a reset link."})
}

// ResetPassword handles POST /auth/reset-password.
func (h *Handler) ResetPassword(c *gin.Context) {
	var req ResetPasswordRequest
	if !h.bind(c, &req) {
		return
	}

	if err := h.service.ResetPassword(c.Request.Context(), req.Token, req.Password); err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, MessageResponse{Message: "Password updated. Sign in with your new password."})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// bind decodes and validates a request body, rendering the failure itself.
//
// An absent body is not a parse error: it leaves the payload zeroed, so
// validation reports which fields are missing. That is a far more useful
// message than telling a caller their empty request was malformed JSON.
func (h *Handler) bind(c *gin.Context, req httpx.Validatable) bool {
	if err := c.ShouldBindJSON(req); err != nil && !errors.Is(err, io.EOF) {
		httpx.Fail(c, httpx.BadRequest(httpx.CodeInvalidRequest, "Request body is not valid JSON.").WithCause(err))
		return false
	}
	if err := httpx.Validate(req); err != nil {
		if apiErr, ok := httpx.AsValidationError(err); ok {
			httpx.Fail(c, apiErr)
		} else {
			httpx.Fail(c, httpx.Internal("Request could not be validated.", err))
		}
		return false
	}
	return true
}

// fail maps a service error onto the shared error envelope.
//
// This is the only place that knows how domain errors become HTTP status codes,
// so adding an error to the service does not require touching every handler.
func (h *Handler) fail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, user.ErrEmailTaken):
		httpx.Fail(c, httpx.Conflict(httpx.CodeEmailTaken, "An account with that email already exists."))
	case errors.Is(err, user.ErrInvalidEmail):
		httpx.Fail(c, httpx.BadRequest(httpx.CodeValidationFailed, "Some fields need attention.").
			WithFields(map[string]string{"email": "Enter a valid email address."}))
	case errors.Is(err, user.ErrNameTooLong):
		httpx.Fail(c, httpx.BadRequest(httpx.CodeValidationFailed, "Some fields need attention.").
			WithFields(map[string]string{"name": "Must be at most 120 characters."}))
	case errors.Is(err, user.ErrNotFound):
		httpx.Fail(c, httpx.NotFound("That account could not be found."))

	case errors.Is(err, ErrWeakPassword):
		httpx.Fail(c, weakPasswordError(err))
	case errors.Is(err, ErrInvalidCredentials):
		httpx.Fail(c, httpx.Unauthorized(httpx.CodeInvalidLogin, "Email or password is incorrect."))
	case errors.Is(err, ErrInvalidSession):
		httpx.Fail(c, httpx.Unauthorized(httpx.CodeTokenInvalid,
			"Your session has ended. Please sign in again."))
	case errors.Is(err, ErrEmailNotVerified):
		httpx.Fail(c, httpx.Unauthorized(httpx.CodeEmailNotVerified,
			"Verify your email address before signing in."))

	case errors.Is(err, ErrTokenNotFound), errors.Is(err, ErrTokenConsumed), errors.Is(err, ErrAuthTokenExpired):
		httpx.Fail(c, httpx.BadRequest(httpx.CodeTokenInvalid, "This link is no longer valid. Request a new one."))
	case errors.Is(err, ErrTokenExpired), errors.Is(err, ErrTokenRevoked):
		httpx.Fail(c, httpx.Unauthorized(httpx.CodeTokenExpired, "This link has expired."))
	case errors.Is(err, ErrTokenReused):
		httpx.Fail(c, httpx.Unauthorized(httpx.CodeTokenInvalid, "This session is no longer valid. Sign in again."))

	default:
		httpx.Fail(c, httpx.Internal("Something went wrong. Please try again.", err))
	}
}

// weakPasswordError renders the specific policy rule that failed, so the client
// can tell the user what to change.
func weakPasswordError(err error) error {
	var policyErr *PasswordPolicyError
	if errors.As(err, &policyErr) {
		return httpx.BadRequest(httpx.CodeWeakPassword, "Password does not meet requirements.").
			WithFields(map[string]string{"password": policyErr.Message()})
	}
	return httpx.BadRequest(httpx.CodeWeakPassword, "Password does not meet requirements.")
}

// parseOptionalUUID parses an optional identifier, rejecting a malformed one.
func parseOptionalUUID(raw string) (*uuid.UUID, error) {
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// toTokenResponse adapts the service pair to its wire shape.
func toTokenResponse(pair TokenPair) TokenResponse {
	return TokenResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		TokenType:    pair.TokenType,
		ExpiresAt:    pair.ExpiresAt,
		ExpiresIn:    pair.ExpiresIn,
	}
}

// toAuthUser projects a user onto the public response shape.
func toAuthUser(u *user.User) AuthUserResponse {
	return AuthUserResponse{
		ID:            u.ID,
		Email:         u.Email,
		Name:          u.Name,
		AvatarURL:     u.AvatarURL,
		EmailVerified: u.EmailVerified,
		CreatedAt:     u.CreatedAt,
	}
}
