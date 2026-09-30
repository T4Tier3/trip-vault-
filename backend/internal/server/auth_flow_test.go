package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tripvault/backend/internal/config"
	"github.com/tripvault/backend/internal/database"
	"github.com/tripvault/backend/internal/httpx"
	"github.com/tripvault/backend/internal/server"
	"github.com/tripvault/backend/internal/testsupport"
)

// captureNotifier records the tokens the service tried to email, so a test can
// complete a verification or reset flow without a live SMTP server.
type captureNotifier struct {
	verification []string
	resets       []string
	lastEmail    string
}

func (c *captureNotifier) SendEmailVerification(_ context.Context, to, _, token string) error {
	c.verification = append(c.verification, token)
	c.lastEmail = to
	return nil
}

func (c *captureNotifier) SendPasswordReset(_ context.Context, to, _, token string) error {
	c.resets = append(c.resets, token)
	c.lastEmail = to
	return nil
}

// harness is a running API plus the collaborators a test needs to inspect.
type harness struct {
	server   *httptest.Server
	cfg      *config.Config
	notifier *captureNotifier
	// app is the production Server behind the test listener, and pool is the
	// database, so a test can assert on maintenance work that has no route.
	app  *server.Server
	pool *database.Pool
}

// newHarness builds the real router against real infrastructure.
//
// The router, middleware, repositories and service are the production ones; only
// the mail transport is substituted, so the test exercises the actual request
// path rather than a stubbed approximation.
func newHarness(t *testing.T) *harness {
	t.Helper()

	pool := testsupport.Pool(t)
	redisStore := testsupport.Redis(t)
	testsupport.Truncate(t, pool, redisStore)

	cfg := testsupport.TestConfig(t)
	cfg.App.AllowedOrigins = nil
	// Enable rate limiting so the tests that assert on it run for real.
	cfg.RateLimit = config.RateLimit{
		Enabled:       true,
		Requests:      1000,
		Window:        time.Minute,
		AuthRequests:  1000,
		AuthWindow:    time.Minute,
		ExcludedPaths: []string{"/health", "/ready"},
	}

	notifier := &captureNotifier{}

	srv, err := server.New(server.Deps{
		Config:   cfg,
		DB:       pool,
		Redis:    redisStore,
		Logger:   testsupport.Logger(),
		Notifier: notifier,
	})
	if err != nil {
		t.Fatalf("build server: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return &harness{server: ts, cfg: cfg, notifier: notifier, app: srv, pool: pool}
}

// response is a decoded API reply.
type response struct {
	Status int
	Body   map[string]any
	Raw    string
	Header http.Header
}

// do issues a request against the harness and decodes the envelope.
func (h *harness) do(t *testing.T, method, path string, body any, headers ...string) response {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, h.server.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	res, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	decoded := response{Status: res.StatusCode, Raw: string(raw), Header: res.Header}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded.Body); err != nil {
			t.Fatalf("decode response %q: %v", raw, err)
		}
	}
	return decoded
}

// errorCode pulls the error code out of the envelope.
func (r response) errorCode() string {
	errObj, ok := r.Body["error"].(map[string]any)
	if !ok {
		return ""
	}
	code, _ := errObj["code"].(string)
	return code
}

// data pulls the data half of the envelope.
func (r response) data() map[string]any {
	obj, ok := r.Body["data"].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return obj
}

// tokens extracts the token pair from a login or refresh reply.
func (r response) tokens(t *testing.T) (access, refresh string) {
	t.Helper()

	tokens, ok := r.data()["tokens"].(map[string]any)
	if !ok {
		tokens = r.data()
	}
	access, _ = tokens["accessToken"].(string)
	refresh, _ = tokens["refreshToken"].(string)
	if access == "" || refresh == "" {
		t.Fatalf("response has no token pair: %s", r.Raw)
	}
	return access, refresh
}

// verificationToken pulls the token out of a capture.
func (n *captureNotifier) lastVerification(t *testing.T) string {
	t.Helper()
	if len(n.verification) == 0 {
		t.Fatal("no verification email was captured")
	}
	return n.verification[len(n.verification)-1]
}

func (n *captureNotifier) lastReset(t *testing.T) string {
	t.Helper()
	if len(n.resets) == 0 {
		t.Fatal("no reset email was captured")
	}
	return n.resets[len(n.resets)-1]
}

// registeredUser creates a verified account and returns its credentials.
func (h *harness) registeredUser(t *testing.T, prefix string) (email, password string) {
	t.Helper()

	email = testsupport.UniqueEmail(prefix)
	password = "Correct Horse Battery 42"

	if res := h.do(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": email, "password": password, "name": "Test User",
	}); res.Status != http.StatusCreated {
		t.Fatalf("register status = %d, want 201: %s", res.Status, res.Raw)
	}
	if res := h.do(t, http.MethodPost, "/api/v1/auth/verify-email",
		map[string]string{"token": h.notifier.lastVerification(t)}); res.Status != http.StatusOK {
		t.Fatalf("verify status = %d, want 200: %s", res.Status, res.Raw)
	}
	return email, password
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

func TestHealth_LivenessAndReadiness(t *testing.T) {
	h := newHarness(t)

	res := h.do(t, http.MethodGet, "/health", nil)
	if res.Status != http.StatusOK {
		t.Fatalf("GET /health status = %d, want 200", res.Status)
	}
	if res.Body["status"] != "ok" {
		t.Errorf("/health status field = %v, want ok", res.Body["status"])
	}

	// Readiness must actually check dependencies, and must report them by name.
	res = h.do(t, http.MethodGet, "/ready", nil)
	if res.Status != http.StatusOK {
		t.Fatalf("GET /ready status = %d, want 200: %s", res.Status, res.Raw)
	}
	deps, ok := res.Body["dependencies"].(map[string]any)
	if !ok {
		t.Fatalf("/ready has no dependencies map: %s", res.Raw)
	}
	for _, name := range []string{"postgres", "redis"} {
		entry, ok := deps[name].(map[string]any)
		if !ok {
			t.Errorf("/ready does not report %q", name)
			continue
		}
		if entry["status"] != "ok" {
			t.Errorf("/ready %s = %v, want ok", name, entry["status"])
		}
	}
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

func TestRegister_CreatesUnverifiedAccount(t *testing.T) {
	h := newHarness(t)

	email := testsupport.UniqueEmail("register")
	res := h.do(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": email, "password": "Correct Horse Battery 42", "name": "Saurabh",
	})

	if res.Status != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", res.Status, res.Raw)
	}
	data := res.data()
	if data["needsVerification"] != true {
		t.Error("needsVerification = false, want true")
	}
	// Registration must not hand out a session.
	if _, ok := data["tokens"]; ok {
		t.Error("register returned tokens, an unverified account must not be able to sign in")
	}
	if h.notifier.lastEmail != strings.ToLower(email) {
		t.Errorf("verification email sent to %q, want the registered address %q", h.notifier.lastEmail, email)
	}
}

func TestRegister_NormalisesEmailToLowerCase(t *testing.T) {
	h := newHarness(t)

	res := h.do(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": "  Saurabh.MIXED@Example.COM ", "password": "Correct Horse Battery 42", "name": "Saurabh",
	})
	if res.Status != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", res.Status, res.Raw)
	}
	if got := res.data()["email"]; got != "saurabh.mixed@example.com" {
		t.Errorf("stored email = %v, want the normalised lower-case form", got)
	}
}

func TestRegister_RejectsDuplicateEmailCaseInsensitively(t *testing.T) {
	h := newHarness(t)
	email := testsupport.UniqueEmail("dup")

	body := map[string]string{"email": email, "password": "Correct Horse Battery 42", "name": "A"}
	if res := h.do(t, http.MethodPost, "/api/v1/auth/register", body); res.Status != http.StatusCreated {
		t.Fatalf("first register status = %d, want 201: %s", res.Status, res.Raw)
	}

	// The unique index is on the normalised value, so a differently cased
	// duplicate must collide rather than create a second account.
	body["email"] = strings.ToUpper(email)
	res := h.do(t, http.MethodPost, "/api/v1/auth/register", body)
	if res.Status != http.StatusConflict {
		t.Fatalf("duplicate register status = %d, want 409: %s", res.Status, res.Raw)
	}
	if res.errorCode() != "email_taken" {
		t.Errorf("error code = %q, want email_taken", res.errorCode())
	}
}

func TestRegister_RejectsWeakPassword(t *testing.T) {
	h := newHarness(t)

	// These are long enough to pass field validation but fail the strength
	// policy, which is a separate, later check.
	tests := []struct {
		name     string
		password string
	}{
		{"breach list", "password123"},
		{"single variety", "aaaaaaaaaaaaaaa"},
		{"all lowercase", "alllowercaseletters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := h.do(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
				"email": testsupport.UniqueEmail("weak"), "password": tt.password, "name": "Weak",
			})
			if res.Status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", res.Status, res.Raw)
			}
			if res.errorCode() != "weak_password" {
				t.Errorf("error code = %q, want weak_password", res.errorCode())
			}
			// The client must learn which rule failed, not just that one did.
			errObj := res.Body["error"].(map[string]any)
			fields, _ := errObj["fields"].(map[string]any)
			if _, ok := fields["password"]; !ok {
				t.Errorf("no per-field guidance for the password: %s", res.Raw)
			}
		})
	}
}

// TestRegister_ShortPasswordIsAFieldError pins where the minimum length is
// enforced: as a field error, so it appears with the other field problems.
func TestRegister_ShortPasswordIsAFieldError(t *testing.T) {
	h := newHarness(t)

	res := h.do(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": testsupport.UniqueEmail("short"), "password": "short", "name": "Short",
	})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", res.Status, res.Raw)
	}
	if res.errorCode() != "validation_failed" {
		t.Errorf("error code = %q, want validation_failed", res.errorCode())
	}
	errObj := res.Body["error"].(map[string]any)
	fields, _ := errObj["fields"].(map[string]any)
	if _, ok := fields["password"]; !ok {
		t.Errorf("password field not reported: %s", res.Raw)
	}
}

// TestRegister_PasswordHashIsNotReturned guards the single most damaging
// mistake available to this endpoint.
func TestRegister_PasswordHashIsNotReturned(t *testing.T) {
	h := newHarness(t)

	res := h.do(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": testsupport.UniqueEmail("leak"), "password": "Correct Horse Battery 42", "name": "Leaky",
	})
	if strings.Contains(strings.ToLower(res.Raw), "hash") {
		t.Errorf("response mentions a hash field: %s", res.Raw)
	}
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func TestValidation_ReportsEveryBadFieldAtOnce(t *testing.T) {
	h := newHarness(t)

	res := h.do(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": "not-an-email", "password": "short",
	})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", res.Status, res.Raw)
	}
	if res.errorCode() != "validation_failed" {
		t.Errorf("error code = %q, want validation_failed", res.errorCode())
	}

	errObj := res.Body["error"].(map[string]any)
	fields, ok := errObj["fields"].(map[string]any)
	if !ok {
		t.Fatalf("no fields map: %s", res.Raw)
	}
	// Reporting only the first failure would force the client into a retry loop.
	for _, field := range []string{"email", "password", "name"} {
		if _, reported := fields[field]; !reported {
			t.Errorf("field %q not reported, want every failure at once: %s", field, res.Raw)
		}
	}
}

func TestValidation_RejectsOversizedBody(t *testing.T) {
	h := newHarness(t)

	huge := strings.Repeat("x", 70*1024)
	res := h.do(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": "big@example.com", "password": "Correct Horse Battery 42", "name": huge,
	})
	if res.Status != http.StatusBadRequest && res.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 400 or 413: %s", res.Status, res.Raw)
	}
}

// ---------------------------------------------------------------------------
// Email verification
// ---------------------------------------------------------------------------

func TestVerifyEmail_ConsumesTokenOnce(t *testing.T) {
	h := newHarness(t)
	email := testsupport.UniqueEmail("verify")
	password := "Correct Horse Battery 42"

	if res := h.do(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": email, "password": password, "name": "Verifier",
	}); res.Status != http.StatusCreated {
		t.Fatalf("register status = %d: %s", res.Status, res.Raw)
	}
	token := h.notifier.lastVerification(t)

	if res := h.do(t, http.MethodPost, "/api/v1/auth/verify-email", map[string]string{"token": token}); res.Status != http.StatusOK {
		t.Fatalf("first verify status = %d, want 200: %s", res.Status, res.Raw)
	}

	// Single use: a leaked link must not be replayable.
	res := h.do(t, http.MethodPost, "/api/v1/auth/verify-email", map[string]string{"token": token})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("replayed verify status = %d, want 400: %s", res.Status, res.Raw)
	}
	if res.errorCode() != "token_invalid" {
		t.Errorf("error code = %q, want token_invalid", res.errorCode())
	}
}

func TestVerifyEmail_RejectsUnknownToken(t *testing.T) {
	h := newHarness(t)

	res := h.do(t, http.MethodPost, "/api/v1/auth/verify-email",
		map[string]string{"token": "totally-made-up-token-value-that-is-long"})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", res.Status, res.Raw)
	}
}

func TestVerifyEmail_RejectsGarbageToken(t *testing.T) {
	h := newHarness(t)

	res := h.do(t, http.MethodPost, "/api/v1/auth/verify-email", map[string]string{"token": "short"})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", res.Status, res.Raw)
	}
}

func TestVerifyEmail_AcceptsQueryParameter(t *testing.T) {
	h := newHarness(t)

	if res := h.do(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": testsupport.UniqueEmail("query"), "password": "Correct Horse Battery 42", "name": "Query",
	}); res.Status != http.StatusCreated {
		t.Fatalf("register status = %d: %s", res.Status, res.Raw)
	}
	token := h.notifier.lastVerification(t)

	// The emailed link is opened in a browser with no body to post.
	res := h.do(t, http.MethodPost, "/api/v1/auth/verify-email?token="+token, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("verify status = %d, want 200: %s", res.Status, res.Raw)
	}
}

// TestVerifyEmail_ResetTokenIsNotAcceptedAsVerification pins the purpose
// separation between the two token types.
func TestVerifyEmail_ResetTokenIsNotAcceptedAsVerification(t *testing.T) {
	h := newHarness(t)
	email, _ := h.registeredUser(t, "purpose")

	if res := h.do(t, http.MethodPost, "/api/v1/auth/forgot-password",
		map[string]string{"email": email}); res.Status != http.StatusOK {
		t.Fatalf("forgot-password status = %d: %s", res.Status, res.Raw)
	}
	resetToken := h.notifier.lastReset(t)

	res := h.do(t, http.MethodPost, "/api/v1/auth/verify-email", map[string]string{"token": resetToken})
	if res.Status != http.StatusBadRequest {
		t.Errorf("a reset token was accepted for verification: status = %d", res.Status)
	}
}

// ---------------------------------------------------------------------------
// Login
// ---------------------------------------------------------------------------

func TestLogin_SucceedsAfterVerification(t *testing.T) {
	h := newHarness(t)
	email, password := h.registeredUser(t, "login")

	res := h.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": email, "password": password,
	})
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Status, res.Raw)
	}

	access, refresh := res.tokens(t)
	if access == "" || refresh == "" {
		t.Fatal("login returned no tokens")
	}

	userObj := res.data()["user"].(map[string]any)
	if userObj["emailVerified"] != true {
		t.Error("emailVerified = false, want true")
	}
	// The hash must never appear in a login response.
	if strings.Contains(strings.ToLower(res.Raw), "hash") {
		t.Errorf("login response leaks a hash field: %s", res.Raw)
	}
}

func TestLogin_RefusesUnverifiedAccount(t *testing.T) {
	h := newHarness(t)
	email := testsupport.UniqueEmail("unverified")

	if res := h.do(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": email, "password": "Correct Horse Battery 42", "name": "Unverified",
	}); res.Status != http.StatusCreated {
		t.Fatalf("register status = %d: %s", res.Status, res.Raw)
	}

	res := h.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": email, "password": "Correct Horse Battery 42",
	})
	if res.Status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", res.Status, res.Raw)
	}
	if res.errorCode() != "email_not_verified" {
		t.Errorf("error code = %q, want email_not_verified", res.errorCode())
	}
}

// TestLogin_UnknownEmailAndWrongPasswordAreIndistinguishable is the
// anti-enumeration guarantee: if the two responses differ, this API leaks which
// addresses have accounts.
func TestLogin_UnknownEmailAndWrongPasswordAreIndistinguishable(t *testing.T) {
	h := newHarness(t)
	email, _ := h.registeredUser(t, "enum")

	unknown := h.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": "definitely-not-registered@example.com", "password": "Correct Horse Battery 42",
	})
	wrongPassword := h.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": email, "password": "Wrong Password Entirely",
	})

	if unknown.Status != wrongPassword.Status {
		t.Errorf("status codes differ: unknown = %d, wrong password = %d",
			unknown.Status, wrongPassword.Status)
	}
	if unknown.errorCode() != wrongPassword.errorCode() {
		t.Errorf("error codes differ: unknown = %q, wrong password = %q",
			unknown.errorCode(), wrongPassword.errorCode())
	}

	unknownErr := unknown.Body["error"].(map[string]any)
	wrongErr := wrongPassword.Body["error"].(map[string]any)
	if unknownErr["message"] != wrongErr["message"] {
		t.Errorf("messages differ: %q vs %q", unknownErr["message"], wrongErr["message"])
	}
}

func TestLogin_RejectsMalformedCredentials(t *testing.T) {
	h := newHarness(t)

	tests := []struct {
		name string
		body map[string]string
	}{
		{"missing password", map[string]string{"email": "a@example.com"}},
		{"missing email", map[string]string{"password": "Correct Horse Battery 42"}},
		{"malformed email", map[string]string{"email": "nope", "password": "Correct Horse Battery 42"}},
		{"bad device id", map[string]string{"email": "a@example.com", "password": "Correct Horse Battery 42", "deviceId": "not-a-uuid"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := h.do(t, http.MethodPost, "/api/v1/auth/login", tt.body)
			if res.Status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", res.Status, res.Raw)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Refresh and rotation
// ---------------------------------------------------------------------------

func TestRefresh_RotatesToken(t *testing.T) {
	h := newHarness(t)
	email, password := h.registeredUser(t, "refresh")

	login := h.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": password})
	_, refresh := login.tokens(t)

	res := h.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refreshToken": refresh})
	if res.Status != http.StatusOK {
		t.Fatalf("refresh status = %d, want 200: %s", res.Status, res.Raw)
	}
	_, newRefresh := res.tokens(t)

	if newRefresh == refresh {
		t.Error("refresh returned the same token, rotation did not happen")
	}
}

// TestRefresh_OldTokenStopsWorking is the property rotation exists for.
func TestRefresh_OldTokenStopsWorking(t *testing.T) {
	h := newHarness(t)
	email, password := h.registeredUser(t, "rotate")

	login := h.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": password})
	_, refresh := login.tokens(t)

	if res := h.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refreshToken": refresh}); res.Status != http.StatusOK {
		t.Fatalf("first refresh failed: %s", res.Raw)
	}

	// Using the old token again must fail: it is a guessable, static secret if
	// it keeps working.
	res := h.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refreshToken": refresh})
	if res.Status != http.StatusUnauthorized {
		t.Fatalf("replayed refresh status = %d, want 401: %s", res.Status, res.Raw)
	}
}

// TestRefresh_ReuseRevokesTheWholeFamily covers token theft: replaying a
// rotated token must terminate the session rather than merely failing.
func TestRefresh_ReuseRevokesTheWholeFamily(t *testing.T) {
	h := newHarness(t)
	email, password := h.registeredUser(t, "reuse")

	login := h.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": password})
	_, original := login.tokens(t)

	rotated := h.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refreshToken": original})
	if rotated.Status != http.StatusOK {
		t.Fatalf("rotation failed: %s", rotated.Raw)
	}
	_, successor := rotated.tokens(t)

	// The attacker replays the stolen token.
	if res := h.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refreshToken": original}); res.Status != http.StatusUnauthorized {
		t.Fatalf("reuse was not rejected: %d", res.Status)
	}

	// The legitimate client's successor token is dead too, which is the point:
	// the server can no longer tell the two apart.
	res := h.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refreshToken": successor})
	if res.Status != http.StatusUnauthorized {
		t.Errorf("successor token still works after reuse detected: status = %d", res.Status)
	}
}

func TestRefresh_RejectsUnknownToken(t *testing.T) {
	h := newHarness(t)

	res := h.do(t, http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refreshToken": strings.Repeat("x", 43)})
	if res.Status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", res.Status, res.Raw)
	}
}

// TestRefresh_FailureDoesNotBlameThePassword pins the wording of a failed
// refresh. The request carries no email and no password, so a message about
// either sends the user to the sign-in form for a reason that is not the cause
// of their problem. The code is also the client's cue to sign out, so it must
// not be the login-failure code.
func TestRefresh_FailureDoesNotBlameThePassword(t *testing.T) {
	h := newHarness(t)
	email, password := h.registeredUser(t, "refreshmsg")

	login := h.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": password})
	_, refresh := login.tokens(t)

	// Rotate, then replay, so this is the reuse path rather than an unknown
	// token. Both must read the same to a caller.
	if res := h.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refreshToken": refresh}); res.Status != http.StatusOK {
		t.Fatalf("rotation failed: %s", res.Raw)
	}
	reuse := h.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refreshToken": refresh})

	if got := reuse.errorCode(); got != string(httpx.CodeTokenInvalid) {
		t.Errorf("reuse error code = %q, want %q", got, httpx.CodeTokenInvalid)
	}

	errObj, _ := reuse.Body["error"].(map[string]any)
	message, _ := errObj["message"].(string)
	for _, forbidden := range []string{"email", "password", "Email", "Password"} {
		if strings.Contains(message, forbidden) {
			t.Errorf("refresh failure message %q mentions %q, but the request had neither", message, forbidden)
		}
	}
}

// TestRefresh_FailuresAreIndistinguishable is the anti-enumeration property for
// the refresh endpoint: an unknown token, a replayed one, and a revoked one all
// have to answer identically, or the response becomes a way to test whether a
// token was ever real.
func TestRefresh_FailuresAreIndistinguishable(t *testing.T) {
	h := newHarness(t)
	email, password := h.registeredUser(t, "refreshenum")

	unknown := h.do(t, http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refreshToken": strings.Repeat("x", 43)})

	login := h.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": password})
	_, refresh := login.tokens(t)
	if res := h.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refreshToken": refresh}); res.Status != http.StatusOK {
		t.Fatalf("rotation failed: %s", res.Raw)
	}
	replayed := h.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refreshToken": refresh})

	if unknown.errorCode() != replayed.errorCode() {
		t.Errorf("unknown token answered %q but a replayed one answered %q",
			unknown.errorCode(), replayed.errorCode())
	}
	unknownErr, _ := unknown.Body["error"].(map[string]any)
	replayErr, _ := replayed.Body["error"].(map[string]any)
	if unknownErr["message"] != replayErr["message"] {
		t.Errorf("messages differ:\n  unknown:  %v\n  replayed: %v",
			unknownErr["message"], replayErr["message"])
	}
}

// ---------------------------------------------------------------------------
// Logout
// ---------------------------------------------------------------------------

func TestLogout_RevokesTheSession(t *testing.T) {
	h := newHarness(t)
	email, password := h.registeredUser(t, "logout")

	login := h.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": password})
	_, refresh := login.tokens(t)

	if res := h.do(t, http.MethodPost, "/api/v1/auth/logout", map[string]string{"refreshToken": refresh}); res.Status != http.StatusOK {
		t.Fatalf("logout status = %d, want 200: %s", res.Status, res.Raw)
	}

	// A revoked token must be dead immediately.
	res := h.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refreshToken": refresh})
	if res.Status != http.StatusUnauthorized {
		t.Errorf("token still refreshes after logout: status = %d", res.Status)
	}
}

func TestLogout_OnlyRevokesTheSuppliedSession(t *testing.T) {
	h := newHarness(t)
	email, password := h.registeredUser(t, "sessions")

	phone := h.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": password})
	laptop := h.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": password})
	_, phoneRefresh := phone.tokens(t)
	_, laptopRefresh := laptop.tokens(t)

	if res := h.do(t, http.MethodPost, "/api/v1/auth/logout", map[string]string{"refreshToken": phoneRefresh}); res.Status != http.StatusOK {
		t.Fatalf("logout failed: %s", res.Raw)
	}

	// Signing out of a phone must not sign the user out of a laptop.
	if res := h.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refreshToken": laptopRefresh}); res.Status != http.StatusOK {
		t.Errorf("the other session was revoked too: status = %d", res.Status)
	}
}

// ---------------------------------------------------------------------------
// Password reset
// ---------------------------------------------------------------------------

func TestForgotPassword_RespondsIdenticallyForUnknownAddress(t *testing.T) {
	h := newHarness(t)
	email, _ := h.registeredUser(t, "forgot")

	known := h.do(t, http.MethodPost, "/api/v1/auth/forgot-password", map[string]string{"email": email})
	unknown := h.do(t, http.MethodPost, "/api/v1/auth/forgot-password",
		map[string]string{"email": "nobody-at-all@example.com"})

	if known.Status != unknown.Status {
		t.Errorf("status codes differ: known = %d, unknown = %d", known.Status, unknown.Status)
	}
	knownMsg := known.data()["message"]
	unknownMsg := unknown.data()["message"]
	if knownMsg != unknownMsg {
		t.Errorf("messages differ and leak account existence: %q vs %q", knownMsg, unknownMsg)
	}
	if len(h.notifier.resets) != 1 {
		t.Errorf("sent %d reset emails, want 1 (only the known address)", len(h.notifier.resets))
	}
}

func TestResetPassword_ChangesCredentialAndRevokesSessions(t *testing.T) {
	h := newHarness(t)
	email, oldPassword := h.registeredUser(t, "reset")

	login := h.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": oldPassword})
	_, refresh := login.tokens(t)

	if res := h.do(t, http.MethodPost, "/api/v1/auth/forgot-password", map[string]string{"email": email}); res.Status != http.StatusOK {
		t.Fatalf("forgot-password failed: %s", res.Raw)
	}
	token := h.notifier.lastReset(t)
	const newPassword = "A Whole New Passphrase 91"

	if res := h.do(t, http.MethodPost, "/api/v1/auth/reset-password",
		map[string]string{"token": token, "password": newPassword}); res.Status != http.StatusOK {
		t.Fatalf("reset status = %d, want 200: %s", res.Status, res.Raw)
	}

	// The old password must stop working.
	if res := h.do(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": email, "password": oldPassword}); res.Status != http.StatusUnauthorized {
		t.Errorf("old password still works: status = %d", res.Status)
	}
	// The new one must work.
	if res := h.do(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": email, "password": newPassword}); res.Status != http.StatusOK {
		t.Errorf("new password rejected: status = %d, %s", res.Status, res.Raw)
	}
	// A stolen session must not survive a password reset.
	if res := h.do(t, http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refreshToken": refresh}); res.Status != http.StatusUnauthorized {
		t.Errorf("session survived a password reset: status = %d", res.Status)
	}
}

func TestResetPassword_TokenIsSingleUse(t *testing.T) {
	h := newHarness(t)
	email, _ := h.registeredUser(t, "resetonce")

	if res := h.do(t, http.MethodPost, "/api/v1/auth/forgot-password", map[string]string{"email": email}); res.Status != http.StatusOK {
		t.Fatalf("forgot-password failed: %s", res.Raw)
	}
	token := h.notifier.lastReset(t)

	if res := h.do(t, http.MethodPost, "/api/v1/auth/reset-password",
		map[string]string{"token": token, "password": "First Reset Passphrase 1"}); res.Status != http.StatusOK {
		t.Fatalf("first reset failed: %s", res.Raw)
	}

	res := h.do(t, http.MethodPost, "/api/v1/auth/reset-password",
		map[string]string{"token": token, "password": "Second Reset Passphrase 2"})
	if res.Status != http.StatusBadRequest {
		t.Errorf("reset token was reusable: status = %d", res.Status)
	}
}

func TestResetPassword_RejectsWeakPassword(t *testing.T) {
	h := newHarness(t)
	email, _ := h.registeredUser(t, "weakreset")

	if res := h.do(t, http.MethodPost, "/api/v1/auth/forgot-password", map[string]string{"email": email}); res.Status != http.StatusOK {
		t.Fatalf("forgot-password failed: %s", res.Raw)
	}

	// Long enough to pass field validation, but all one character class, so the
	// strength policy is what rejects it.
	const oneClass = "aaaaaaaaaaaaaaa"
	res := h.do(t, http.MethodPost, "/api/v1/auth/reset-password",
		map[string]string{"token": h.notifier.lastReset(t), "password": oneClass})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", res.Status, res.Raw)
	}
	if res.errorCode() != "weak_password" {
		t.Errorf("error code = %q, want weak_password", res.errorCode())
	}
}

func TestForgotPassword_NewRequestRetiresThePreviousLink(t *testing.T) {
	h := newHarness(t)
	email, _ := h.registeredUser(t, "retire")

	if res := h.do(t, http.MethodPost, "/api/v1/auth/forgot-password", map[string]string{"email": email}); res.Status != http.StatusOK {
		t.Fatalf("first forgot-password failed: %s", res.Raw)
	}
	firstToken := h.notifier.lastReset(t)

	if res := h.do(t, http.MethodPost, "/api/v1/auth/forgot-password", map[string]string{"email": email}); res.Status != http.StatusOK {
		t.Fatalf("second forgot-password failed: %s", res.Raw)
	}

	// An earlier link that was forwarded or left in an inbox must stop working.
	res := h.do(t, http.MethodPost, "/api/v1/auth/reset-password",
		map[string]string{"token": firstToken, "password": "Should Not Apply 12"})
	if res.Status != http.StatusBadRequest {
		t.Errorf("the superseded reset link still works: status = %d", res.Status)
	}
}

// ---------------------------------------------------------------------------
// Authorisation
// ---------------------------------------------------------------------------

func TestProtectedRoute_RejectsMissingAndMalformedCredentials(t *testing.T) {
	h := newHarness(t)

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong scheme", "Basic dXNlcjpwYXNz", http.StatusUnauthorized},
		{"bearer with no token", "Bearer ", http.StatusUnauthorized},
		{"garbage token", "Bearer not.a.jwt", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// /api/v1/users/me is the canonical authenticated endpoint. It is
			// registered here so the guard has something real to protect.
			res := h.do(t, http.MethodGet, "/api/v1/users/me", nil, "Authorization", tt.authHeader)
			if res.Status != tt.wantStatus {
				t.Errorf("status = %d, want %d: %s", res.Status, tt.wantStatus, res.Raw)
			}
		})
	}
}

func TestProtectedRoute_AcceptsAValidToken(t *testing.T) {
	h := newHarness(t)
	email, password := h.registeredUser(t, "guard")

	login := h.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": password})
	access, _ := login.tokens(t)

	res := h.do(t, http.MethodGet, "/api/v1/users/me", nil, "Authorization", "Bearer "+access)
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Status, res.Raw)
	}
	if got := res.data()["email"]; got != email {
		t.Errorf("email = %v, want %q", got, email)
	}
}

// TestProtectedRoute_TamperedTokenIsRejected covers the case where a client
// edits a JWT to impersonate another user.
func TestProtectedRoute_TamperedTokenIsRejected(t *testing.T) {
	h := newHarness(t)
	email, password := h.registeredUser(t, "tamper")

	login := h.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": password})
	access, _ := login.tokens(t)

	parts := strings.Split(access, ".")
	if len(parts) != 3 {
		t.Fatalf("token is not a JWT: %q", access)
	}
	tampered := parts[0] + "." + parts[1] + ".aW52YWxpZC1zaWduYXR1cmU"

	res := h.do(t, http.MethodGet, "/api/v1/users/me", nil, "Authorization", "Bearer "+tampered)
	if res.Status != http.StatusUnauthorized {
		t.Errorf("a tampered token was accepted: status = %d", res.Status)
	}
}

// ---------------------------------------------------------------------------
// Transport level behaviour
// ---------------------------------------------------------------------------

func TestUnknownRoute_ReturnsStructured404(t *testing.T) {
	h := newHarness(t)

	res := h.do(t, http.MethodGet, "/api/v1/nope", nil)
	if res.Status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.Status)
	}
	if res.errorCode() != "not_found" {
		t.Errorf("error code = %q, want not_found", res.errorCode())
	}
	// Every response carries a correlation id so a client can quote it in a bug
	// report and an operator can find the matching log line.
	if res.Body["error"].(map[string]any)["requestId"] == "" {
		t.Error("error response has no requestId")
	}
	if res.Header.Get("X-Request-ID") == "" {
		t.Error("X-Request-ID header is missing")
	}
}

func TestSecurityHeaders_ArePresent(t *testing.T) {
	h := newHarness(t)

	res := h.do(t, http.MethodGet, "/health", nil)
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := res.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestCORS_DeniesUnlistedOriginsByDefault(t *testing.T) {
	h := newHarness(t)

	res := h.do(t, http.MethodGet, "/health", nil, "Origin", "https://evil.example.com")
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for an unlisted origin", got)
	}
}

func TestMalformedJSON_IsRejectedCleanly(t *testing.T) {
	h := newHarness(t)

	req, err := http.NewRequest(http.MethodPost, h.server.URL+"/api/v1/auth/login",
		strings.NewReader("{not json"))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.StatusCode)
	}
}

// TestRateLimit_BlocksOnceTheAllowanceIsSpent exercises the Redis limiter
// through the real middleware chain.
func TestRateLimit_BlocksOnceTheAllowanceIsSpent(t *testing.T) {
	pool := testsupport.Pool(t)
	redisStore := testsupport.Redis(t)
	testsupport.Truncate(t, pool, redisStore)

	cfg := testsupport.TestConfig(t)
	cfg.RateLimit = config.RateLimit{
		Enabled:       true,
		Requests:      1000,
		Window:        time.Minute,
		AuthRequests:  3, // deliberately tiny so the test does not need many calls
		AuthWindow:    time.Minute,
		ExcludedPaths: []string{"/health", "/ready"},
	}

	srv, err := server.New(server.Deps{
		Config: cfg,
		DB:     pool,
		Redis:  redisStore,
		Logger: testsupport.Logger(),
	})
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	h := &harness{server: ts, cfg: cfg, notifier: &captureNotifier{}}

	var statuses []int
	for i := 0; i < 5; i++ {
		res := h.do(t, http.MethodPost, "/api/v1/auth/forgot-password",
			map[string]string{"email": "ratelimit@example.com"})
		statuses = append(statuses, res.Status)
	}

	if statuses[len(statuses)-1] != http.StatusTooManyRequests {
		t.Errorf("statuses = %v, want the last request limited with 429", statuses)
	}

	// The health probes must stay reachable: an orchestrator must not be locked
	// out by a client's rate limit.
	if res := h.do(t, http.MethodGet, "/health", nil); res.Status != http.StatusOK {
		t.Errorf("/health = %d, want 200 even when a client is limited", res.Status)
	}
}

// TestDatabaseConstraints_AreEnforced guards the schema itself, so a future
// migration cannot quietly drop a guarantee the service depends on.
func TestDatabaseConstraints_AreEnforced(t *testing.T) {
	pool := testsupport.Pool(t)
	testsupport.Truncate(t, pool, nil)
	ctx := context.Background()

	t.Run("email is stored lower case", func(t *testing.T) {
		_, err := pool.Exec(ctx,
			`INSERT INTO users (email, password_hash, name) VALUES ('Mixed@Example.com', 'x', 'N')`)
		if err == nil {
			t.Error("insert with mixed case email succeeded, the CHECK constraint is missing")
		}
	})

	t.Run("soft deleted email is reusable", func(t *testing.T) {
		email := testsupport.UniqueEmail("softdelete")
		id := uuid.New()
		if _, err := pool.Exec(ctx,
			`INSERT INTO users (id, email, password_hash, name, deleted_at)
			 VALUES ($1, $2, 'x', 'N', now())`, id, email); err != nil {
			t.Fatalf("insert soft deleted user: %v", err)
		}
		// The partial unique index ignores deleted rows, so the address frees up.
		if _, err := pool.Exec(ctx,
			`INSERT INTO users (email, password_hash, name) VALUES ($1, 'x', 'N')`, email); err != nil {
			t.Errorf("reusing a soft deleted email failed: %v", err)
		}
	})

	t.Run("no binary media columns", func(t *testing.T) {
		// Media lives in ImageKit. A bytea column would mean large objects in
		// PostgreSQL, which the architecture rules out.
		var found []string
		rows, err := pool.Query(ctx,
			`SELECT table_name FROM information_schema.columns
			 WHERE table_schema = 'public' AND data_type = 'bytea'`)
		if err != nil {
			t.Fatalf("query bytea columns: %v", err)
		}
		defer rows.Close()

		for rows.Next() {
			var table string
			if err := rows.Scan(&table); err != nil {
				t.Fatalf("scan table name: %v", err)
			}
			found = append(found, table)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate rows: %v", err)
		}
		if len(found) > 0 {
			t.Errorf("tables with bytea columns = %v, want none: media belongs in ImageKit", found)
		}
	})
}

// TestExpiredTokenSweep_DeletesOnlyUnusableRows pins the sweeper's contract. A
// token that has not aged out must survive: deleting one early would log a user
// out, and would erase the record a stolen token's reuse is detected from.
func TestExpiredTokenSweep_DeletesOnlyUnusableRows(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	email, password := h.registeredUser(t, "sweep")
	login := h.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": password})
	_, liveRefresh := login.tokens(t)

	// One token of each kind that has aged out, alongside the live session. The
	// schema only requires expires_at to be after created_at, so backdating both
	// columns models a lifetime that simply ran out.
	if _, err := h.pool.Exec(ctx,
		`INSERT INTO auth_tokens (user_id, purpose, token_hash, created_at, expires_at)
		 SELECT id, 'password_reset', 'aged-auth-hash',
		        now() - interval '2 hours', now() - interval '1 hour'
		 FROM users WHERE email = $1`, email); err != nil {
		t.Fatalf("insert aged auth token: %v", err)
	}
	if _, err := h.pool.Exec(ctx,
		`INSERT INTO refresh_tokens (user_id, family_id, token_hash, created_at, expires_at)
		 SELECT id, gen_random_uuid(), 'aged-refresh-hash',
		        now() - interval '2 hours', now() - interval '1 hour'
		 FROM users WHERE email = $1`, email); err != nil {
		t.Fatalf("insert aged refresh token: %v", err)
	}

	countTokens := func() (auth, refresh int64) {
		t.Helper()
		if err := h.pool.QueryRow(ctx,
			`SELECT (SELECT count(*) FROM auth_tokens), (SELECT count(*) FROM refresh_tokens)`).
			Scan(&auth, &refresh); err != nil {
			t.Fatalf("count tokens: %v", err)
		}
		return auth, refresh
	}

	beforeAuth, beforeRefresh := countTokens()

	authTokens, refreshTokens, err := h.app.DeleteExpiredTokens(ctx)
	if err != nil {
		t.Fatalf("DeleteExpiredTokens() error = %v", err)
	}

	afterAuth, afterRefresh := countTokens()

	if authTokens != 1 {
		t.Errorf("swept %d auth tokens, want exactly the 1 that aged out", authTokens)
	}
	if refreshTokens != 1 {
		t.Errorf("swept %d refresh tokens, want exactly the 1 that aged out", refreshTokens)
	}
	// The reported counts must match what actually left the tables, so the log
	// line from the sweeper can be trusted during an incident.
	if beforeAuth-authTokens != afterAuth {
		t.Errorf("reported %d auth tokens swept but %d rows went (before %d, after %d)",
			authTokens, beforeAuth-afterAuth, beforeAuth, afterAuth)
	}
	if beforeRefresh-refreshTokens != afterRefresh {
		t.Errorf("reported %d refresh tokens swept but %d rows went (before %d, after %d)",
			refreshTokens, beforeRefresh-afterRefresh, beforeRefresh, afterRefresh)
	}

	// The live session must still work afterwards.
	if res := h.do(t, http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refreshToken": liveRefresh}); res.Status != http.StatusOK {
		t.Errorf("live session did not survive the sweep: status = %d, %s", res.Status, res.Raw)
	}
}

// TestErrorCodeStatusPairs pins the code and status that the API documentation
// promises for the auth surface, so docs/api.md cannot drift from the code.
//
// The note worth reading is the login case. A password that is long enough but
// not varied enough answers validation_failed-style feedback on register, but at
// sign-in it is reported as invalid_credentials like any wrong password, because
// "that password is too weak" would confirm both that the account exists and
// that the password matched something.
func TestErrorCodeStatusPairs(t *testing.T) {
	h := newHarness(t)
	email, password := h.registeredUser(t, "statuspairs")

	cases := []struct {
		name   string
		method string
		path   string
		body   map[string]string
		status int
		code   string
	}{
		{
			name:   "short password is a shape error",
			method: http.MethodPost, path: "/api/v1/auth/login",
			body:   map[string]string{"email": email, "password": "short"},
			status: http.StatusBadRequest, code: string(httpx.CodeValidationFailed),
		},
		{
			name:   "long but monotonous password on register is a policy error",
			method: http.MethodPost, path: "/api/v1/auth/register",
			body: map[string]string{
				"email":    testsupport.UniqueEmail("policy"),
				"password": "aaaaaaaaaaaaaa",
				"name":     "Policy",
			},
			status: http.StatusBadRequest, code: string(httpx.CodeWeakPassword),
		},
		{
			// The counterpart to the case above, and the reason for it. Reporting
			// "that password is too weak" at sign-in would confirm the account
			// exists and that the password matched something, so a weak password
			// is reported the same way as any wrong password.
			name:   "long but monotonous password on login is not disclosed",
			method: http.MethodPost, path: "/api/v1/auth/login",
			body:   map[string]string{"email": email, "password": "aaaaaaaaaaaaaa"},
			status: http.StatusUnauthorized, code: string(httpx.CodeInvalidLogin),
		},
		{
			name:   "wrong password on a verified account",
			method: http.MethodPost, path: "/api/v1/auth/login",
			body:   map[string]string{"email": email, "password": "not the right passphrase"},
			status: http.StatusUnauthorized, code: string(httpx.CodeInvalidLogin),
		},
		{
			name:   "refresh token that never existed",
			method: http.MethodPost, path: "/api/v1/auth/refresh",
			body:   map[string]string{"refreshToken": strings.Repeat("x", 43)},
			status: http.StatusUnauthorized, code: string(httpx.CodeTokenInvalid),
		},
		{
			name:   "malformed email",
			method: http.MethodPost, path: "/api/v1/auth/login",
			body:   map[string]string{"email": "not-an-email", "password": password},
			status: http.StatusBadRequest, code: string(httpx.CodeValidationFailed),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := h.do(t, tc.method, tc.path, tc.body)
			if res.Status != tc.status {
				t.Errorf("status = %d, want %d: %s", res.Status, tc.status, res.Raw)
			}
			if got := res.errorCode(); got != tc.code {
				t.Errorf("error code = %q, want %q", got, tc.code)
			}
		})
	}
}

// unverifiedUser creates an account that has not confirmed its address yet.
func (h *harness) unverifiedUser(t *testing.T, prefix string) (email, password string) {
	t.Helper()

	email = testsupport.UniqueEmail(prefix)
	password = "Correct Horse Battery 42"

	if res := h.do(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"email": email, "password": password, "name": "Test User",
	}); res.Status != http.StatusCreated {
		t.Fatalf("register status = %d, want 201: %s", res.Status, res.Raw)
	}
	return email, password
}

// ---------------------------------------------------------------------------
// Resend verification
// ---------------------------------------------------------------------------

func TestResendVerification_SendsAFreshTokenToAnUnverifiedAccount(t *testing.T) {
	h := newHarness(t)
	email, _ := h.unverifiedUser(t, "resend")

	before := len(h.notifier.verification)
	res := h.do(t, http.MethodPost, "/api/v1/auth/resend-verification", map[string]string{"email": email})
	if res.Status != http.StatusOK {
		t.Fatalf("resend status = %d, want 200: %s", res.Status, res.Raw)
	}
	if got := len(h.notifier.verification); got != before+1 {
		t.Fatalf("verification emails sent = %d, want %d", got, before+1)
	}
	// The newest token has to be the one just issued, or a resend would leave
	// the user holding two links and unsure which is current.
	if got := h.notifier.lastVerification(t); got == "" {
		t.Error("resent token is empty")
	}
}

func TestResendVerification_IssuedTokenWorks(t *testing.T) {
	h := newHarness(t)
	email, _ := h.unverifiedUser(t, "resenduse")

	if res := h.do(t, http.MethodPost, "/api/v1/auth/resend-verification", map[string]string{"email": email}); res.Status != http.StatusOK {
		t.Fatalf("resend status = %d, want 200: %s", res.Status, res.Raw)
	}

	res := h.do(t, http.MethodPost, "/api/v1/auth/verify-email",
		map[string]string{"token": h.notifier.lastVerification(t)})
	if res.Status != http.StatusOK {
		t.Fatalf("verify with the resent token = %d, want 200: %s", res.Status, res.Raw)
	}
}

func TestResendVerification_AnswersTheSameForAnUnknownAddress(t *testing.T) {
	h := newHarness(t)

	res := h.do(t, http.MethodPost, "/api/v1/auth/resend-verification",
		map[string]string{"email": "nobody-" + testsupport.NewID() + "@example.com"})

	// A different status or body for an unregistered address would make this
	// endpoint a way to find out who has an account.
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200 for an unknown address: %s", res.Status, res.Raw)
	}
	if len(h.notifier.verification) != 0 {
		t.Error("an unknown address should not have produced an email")
	}
}

func TestResendVerification_IsSilentForAnAlreadyVerifiedAccount(t *testing.T) {
	h := newHarness(t)
	email, _ := h.registeredUser(t, "resendverified")
	before := len(h.notifier.verification)
	res := h.do(t, http.MethodPost, "/api/v1/auth/resend-verification", map[string]string{"email": email})
	if res.Status != http.StatusOK {
		t.Fatalf("resend status = %d, want 200: %s", res.Status, res.Raw)
	}
	// A verification link for an address that is already verified is noise, and
	// a second route into the "does this account exist" question.
	if got := len(h.notifier.verification); got != before {
		t.Errorf("verification emails sent = %d, want %d", got, before)
	}
}

func TestResendVerification_RejectsAMalformedAddress(t *testing.T) {
	h := newHarness(t)

	res := h.do(t, http.MethodPost, "/api/v1/auth/resend-verification", map[string]string{"email": "not-an-email"})

	if res.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", res.Status, res.Raw)
	}
	if got := res.errorCode(); got != string(httpx.CodeValidationFailed) {
		t.Errorf("error code = %q, want %q", got, httpx.CodeValidationFailed)
	}
}
