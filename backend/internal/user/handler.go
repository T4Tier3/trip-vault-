package user

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/tripvault/backend/internal/httpx"
)

// Handler serves the authenticated profile routes.
//
// Every route here reads the caller's id from the verified token set by the auth
// middleware. No handler accepts a user id from the client, because a client can
// edit anything it sends.
type Handler struct {
	repo *Repository
}

// NewHandler builds a Handler over repo.
func NewHandler(repo *Repository) *Handler { return &Handler{repo: repo} }

// RegisterRoutesOn mounts the profile routes onto an authenticated group.
func (h *Handler) RegisterRoutesOn(group *gin.RouterGroup) {
	group.GET("/users/me", h.Me)
	group.PATCH("/users/me", h.UpdateMe)
}

// ProfileResponse is the public shape of a user.
//
// The password hash has no field here. It is not omitted from a shared struct
// by accident: this is the only user shape that crosses the API boundary.
type ProfileResponse struct {
	ID            uuid.UUID `json:"id"`
	Email         string    `json:"email"`
	Name          string    `json:"name"`
	AvatarURL     *string   `json:"avatarUrl,omitempty"`
	EmailVerified bool      `json:"emailVerified"`
	CreatedAt     string    `json:"createdAt"`
	UpdatedAt     string    `json:"updatedAt"`
}

// UpdateProfileRequest is the body of PATCH /users/me.
//
// Every field is a pointer so an absent field can be told apart from an empty
// one: omitting avatarUrl leaves it alone, while sending null clears it.
type UpdateProfileRequest struct {
	Name      *string `json:"name"`
	AvatarURL *string `json:"avatarUrl"`
}

// ValidationFields declares the profile rules.
func (r UpdateProfileRequest) ValidationFields() map[string]httpx.FieldRule {
	return map[string]httpx.FieldRule{
		"name": {MinLength: 1, MaxLength: MaxNameLength},
		// A URL is not strictly validated here; the CDN or avatar service owns
		// that. Only over-long values are rejected to keep the column sane.
		"avatarUrl": {MaxLength: 2048},
	}
}

// ValidationValue returns one field's value.
func (r UpdateProfileRequest) ValidationValue(field string) (httpx.FieldValue, bool) {
	switch field {
	case "name":
		if r.Name == nil {
			return httpx.FieldValue{}, true
		}
		return httpx.F(*r.Name), true
	case "avatarUrl":
		if r.AvatarURL == nil {
			return httpx.FieldValue{}, true
		}
		return httpx.F(*r.AvatarURL), true
	}
	return httpx.FieldValue{}, false
}

// Me handles GET /users/me.
//
// The mobile client calls this on launch to decide whether a stored session is
// still usable, which is why it is a GET rather than a token-introspection POST.
func (h *Handler) Me(c *gin.Context) {
	userID, ok := httpx.CurrentUser(c)
	if !ok {
		httpx.Fail(c, httpx.Unauthorized(httpx.CodeUnauthorized, "Authentication is required."))
		return
	}

	found, err := h.repo.GetByID(c.Request.Context(), userID)
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, toProfile(found))
}

// UpdateMe handles PATCH /users/me.
func (h *Handler) UpdateMe(c *gin.Context) {
	userID, ok := httpx.CurrentUser(c)
	if !ok {
		httpx.Fail(c, httpx.Unauthorized(httpx.CodeUnauthorized, "Authentication is required."))
		return
	}

	var req UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.BadRequest(httpx.CodeInvalidRequest, "Request body is not valid JSON.").WithCause(err))
		return
	}
	if err := httpx.Validate(req); err != nil {
		if apiErr, ok := httpx.AsValidationError(err); ok {
			httpx.Fail(c, apiErr)
		} else {
			httpx.Fail(c, httpx.Internal("Request could not be validated.", err))
		}
		return
	}

	if req.Name != nil {
		if err := ValidateName(*req.Name); err != nil {
			httpx.Fail(c, httpx.BadRequest(httpx.CodeValidationFailed, "Some fields need attention.").
				WithFields(map[string]string{"name": "Must be between 1 and 120 characters."}))
			return
		}
	}

	updated, err := h.repo.UpdateProfile(c.Request.Context(), userID, UpdateProfileParams{
		Name:      req.Name,
		AvatarURL: req.AvatarURL,
	})
	if err != nil {
		h.fail(c, err)
		return
	}
	httpx.OK(c, toProfile(updated))
}

// fail maps a repository error onto the shared envelope.
func (h *Handler) fail(c *gin.Context, err error) {
	if errors.Is(err, ErrNotFound) {
		// The token is valid but the account is gone, which means the session
		// cannot be used. Telling the client to sign in again is correct.
		httpx.Fail(c, httpx.Unauthorized(httpx.CodeTokenInvalid, "Your session is no longer valid."))
		return
	}
	httpx.Fail(c, httpx.Internal("Something went wrong. Please try again.", err))
}

// toProfile projects a user onto its public shape.
func toProfile(u *User) ProfileResponse {
	return ProfileResponse{
		ID:            u.ID,
		Email:         u.Email,
		Name:          u.Name,
		AvatarURL:     u.AvatarURL,
		EmailVerified: u.EmailVerified,
		CreatedAt:     u.CreatedAt.UTC().Format(timeFormat),
		UpdatedAt:     u.UpdatedAt.UTC().Format(timeFormat),
	}
}

// timeFormat is the timestamp layout used in API responses. RFC 3339 with a
// numeric offset is what JavaScript's Date constructor expects.
const timeFormat = "2006-01-02T15:04:05.000Z07:00"
