package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

// sampleRequest is a payload with a mix of optional and required fields, used
// to exercise the rule checks.
type sampleRequest struct {
	Email    string  `json:"email"`
	Name     *string `json:"name"`
	Age      *int    `json:"age"`
	Role     string  `json:"role"`
	DeviceID string  `json:"deviceId"`
}

func (r sampleRequest) ValidationFields() map[string]FieldRule {
	return map[string]FieldRule{
		"email":    {Required: true, Email: true, MaxLength: 254},
		"name":     {MaxLength: 10},
		"age":      {Min: Ptr(18.0), Max: Ptr(120.0)},
		"role":     {OneOf: []string{"owner", "admin", "member"}},
		"deviceId": {UUID: true},
	}
}

func (r sampleRequest) ValidationValue(field string) (FieldValue, bool) {
	switch field {
	case "email":
		return F(r.Email), true
	case "name":
		if r.Name == nil {
			return FieldValue{}, true
		}
		return F(*r.Name), true
	case "age":
		if r.Age == nil {
			return FieldValue{}, true
		}
		return F(itoa(*r.Age)), true
	case "role":
		return OptF(r.Role), true
	case "deviceId":
		return OptF(r.DeviceID), true
	}
	return FieldValue{}, false
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestValidate_AcceptsAValidPayload(t *testing.T) {
	name := "Saurabh"
	req := sampleRequest{
		Email:    "saurabh@example.com",
		Name:     &name,
		Age:      Ptr(30),
		Role:     "owner",
		DeviceID: "550e8400-e29b-41d4-a716-446655440000",
	}
	if err := Validate(req); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

// TestValidate_ReportsEveryFailure is the property the mobile client depends on:
// one round trip surfaces every problem with the form.
func TestValidate_ReportsEveryFailure(t *testing.T) {
	age := 5
	name := "A name far too long for the limit"
	req := sampleRequest{
		Email:    "not-an-email",
		Name:     &name,
		Age:      &age,
		Role:     "wizard",
		DeviceID: "not-a-uuid",
	}

	err := Validate(req)
	if err == nil {
		t.Fatal("Validate() error = nil, want failures")
	}

	apiErr, ok := AsValidationError(err)
	if !ok {
		t.Fatalf("AsValidationError() ok = false, want true (err = %v)", err)
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", apiErr.Status)
	}
	if apiErr.Code != CodeValidationFailed {
		t.Errorf("code = %q, want %q", apiErr.Code, CodeValidationFailed)
	}

	for _, field := range []string{"email", "name", "age", "role", "deviceId"} {
		if _, reported := apiErr.Fields[field]; !reported {
			t.Errorf("field %q not reported, got %v", field, apiErr.Fields)
		}
	}
}

func TestValidate_SkipsUndeclaredFields(t *testing.T) {
	// A rule naming a field this payload does not declare must be ignored, not
	// reported as a client error.
	req := sampleRequest{Email: "saurabh@example.com"}
	if err := Validate(req); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

func TestValidate_OptionalFieldsMayBeAbsent(t *testing.T) {
	req := sampleRequest{Email: "saurabh@example.com"}
	if err := Validate(req); err != nil {
		t.Errorf("Validate() error = %v, want nil for absent optional fields", err)
	}
}

func TestValidate_MinLengthIsMeasuredInRunes(t *testing.T) {
	// A Devanagari name is one visual character per few bytes. Counting bytes
	// would reject names a person considers short.
	name := "नमस्ते"
	req := sampleRequest{Email: "saurabh@example.com", Name: &name}
	if err := Validate(req); err != nil {
		t.Errorf("Validate() error = %v, want nil: rune counting must not penalise non-ASCII", err)
	}
}

func TestFieldValue_Present(t *testing.T) {
	tests := []struct {
		value  FieldValue
		absent bool
		want   bool
	}{
		{value: FieldValue{}, want: false},
		{value: F("x"), want: true},
		{value: OptF(""), want: false},
		{value: OptF("   "), want: false},
		{value: F("   "), want: false},
	}

	for _, tt := range tests {
		if got := tt.value.Present(); got != tt.want {
			t.Errorf("%+v Present() = %v, want %v", tt.value, got, tt.want)
		}
	}
}

func TestFail_MapsUnexpectedErrorsToGeneric500(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)

	// An error carrying internal detail must never reach the client verbatim.
	Fail(c, errString("connection to postgres://user:hunter2@db failed"))

	var body ErrorEnvelope
	decode(t, rec, &body)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if body.Error.Code != CodeInternal {
		t.Errorf("code = %q, want %q", body.Error.Code, CodeInternal)
	}
	if strings.Contains(body.Error.Message, "hunter2") {
		t.Errorf("internal detail leaked to the client: %q", body.Error.Message)
	}
}

func TestFail_UsesAuthoredAPIError(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)

	Fail(c, Unauthorized(CodeTokenExpired, "Your session has expired.").WithCause(errString("expired")))

	var body ErrorEnvelope
	decode(t, rec, &body)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if body.Error.Code != CodeTokenExpired {
		t.Errorf("code = %q, want %q", body.Error.Code, CodeTokenExpired)
	}
	if body.Error.Message != "Your session has expired." {
		t.Errorf("message = %q, want the authored message", body.Error.Message)
	}
}

// TestFail_5xxNeverLeaksCause confirms the split between what is logged and what
// is returned holds even when the cause is attached.
func TestFail_5xxNeverLeaksCause(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)

	Fail(c, Internal("Something went wrong.", errString("dial tcp 10.0.0.5:5432 refused")))

	var body ErrorEnvelope
	decode(t, rec, &body)
	if strings.Contains(body.Error.Message, "10.0.0.5") {
		t.Errorf("cause leaked into the response: %q", body.Error.Message)
	}
}

func TestJSON_WrapsDataInAnEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)

	OK(c, map[string]any{"id": "abc"})

	var env Envelope
	decode(t, rec, &env)

	data, ok := env.Data.(map[string]any)
	if !ok {
		t.Fatalf("data is not an object: %#v", env.Data)
	}
	if data["id"] != "abc" {
		t.Errorf("data.id = %v, want abc", data["id"])
	}
}

// errString is a minimal error type for tests.
type errString string

func (e errString) Error() string { return string(e) }

// decode unmarshals a recorded response body.
func decode(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
}
