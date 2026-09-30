package httpx

import (
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Validatable is implemented by every request payload.
//
// A payload declares its own rules next to its own fields, so validation is
// visible where the field is defined and cannot drift out of sync with it. It
// also keeps the framework free of reflection: this repository declares all
// request types, so an interface is both complete and compile-time checked.
type Validatable interface {
	// ValidationFields returns the constraints for each field, keyed by the
	// JSON name the client sends.
	ValidationFields() map[string]FieldRule

	// ValidationValue returns one field's value, and whether the payload
	// actually declares that field. Returning false for an undeclared field
	// lets a shared rule set skip requests that do not use it.
	ValidationValue(field string) (FieldValue, bool)
}

// FieldRule describes the constraints on one request field.
type FieldRule struct {
	Required bool
	// MinLength and MaxLength are measured in runes so a name written in a
	// non-Latin script is not penalised for its encoding.
	MinLength int
	MaxLength int
	// Email validates an address with the standard library parser, which is
	// stricter than the storage-level check.
	Email bool
	// UUID requires a canonical UUID string.
	UUID bool
	// OneOf restricts the value to a fixed set of strings.
	OneOf []string
	// Min and Max bound a numeric field.
	Min *float64
	Max *float64
}

// FieldValue is one request field, normalised for rule evaluation.
type FieldValue struct {
	Text string
	// IsSet distinguishes "field absent" from "field present but empty", which
	// matters for optional fields.
	IsSet bool
}

// Present reports whether the field was supplied with a non-blank value.
func (v FieldValue) Present() bool { return v.IsSet && strings.TrimSpace(v.Text) != "" }

// F builds a FieldValue from a string.
func F(s string) FieldValue { return FieldValue{Text: s, IsSet: true} }

// OptF builds an optional FieldValue. An empty string is reported as unset,
// which is how an omitted optional field and an empty one are treated alike.
func OptF(s string) FieldValue {
	if s == "" {
		return FieldValue{}
	}
	return FieldValue{Text: s, IsSet: true}
}

// Validate applies a payload's rules and returns every failure it finds.
//
// Collecting all failures rather than stopping at the first one means the client
// can show every problem in one pass. Fields are still evaluated in a
// deterministic order so the same request always produces the same response.
func Validate(v Validatable) error {
	fieldNames := make([]string, 0, len(v.ValidationFields()))
	for name := range v.ValidationFields() {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)

	failures := make(map[string]string)
	for _, field := range fieldNames {
		value, present := v.ValidationValue(field)
		if !present {
			// A rule naming a field this payload does not declare is a bug in
			// the declaration, not a client error.
			continue
		}
		if fe, ok := validateField(field, value, v.ValidationFields()[field]).(*fieldError); ok {
			failures[fe.field] = fe.message
		}
	}

	if len(failures) == 0 {
		return nil
	}
	return &validationError{fields: failures}
}

// validateField applies one rule to one field value.
func validateField(field string, v FieldValue, rule FieldRule) error {
	if err := validateRequired(field, v, rule); err != nil {
		return err
	}
	if err := validateLength(field, v, rule); err != nil {
		return err
	}
	if err := validateEmail(field, v, rule); err != nil {
		return err
	}
	if err := validateUUID(field, v, rule); err != nil {
		return err
	}
	if err := validateEnum(field, v, rule); err != nil {
		return err
	}
	return validateRange(field, v, rule)
}

func validateRequired(field string, v FieldValue, rule FieldRule) error {
	if rule.Required && !v.Present() {
		return &fieldError{field: field, message: "This field is required."}
	}
	return nil
}

func validateLength(field string, v FieldValue, rule FieldRule) error {
	length := utf8.RuneCountInString(v.Text)
	if rule.MinLength > 0 && v.Present() && length < rule.MinLength {
		return &fieldError{
			field:   field,
			message: fmt.Sprintf("Must be at least %d characters.", rule.MinLength),
		}
	}
	if rule.MaxLength > 0 && length > rule.MaxLength {
		return &fieldError{
			field:   field,
			message: fmt.Sprintf("Must be at most %d characters.", rule.MaxLength),
		}
	}
	return nil
}

func validateEmail(field string, v FieldValue, rule FieldRule) error {
	if !rule.Email || !v.Present() {
		return nil
	}
	if _, err := mail.ParseAddress(v.Text); err != nil {
		return &fieldError{field: field, message: "Enter a valid email address."}
	}
	return nil
}

func validateUUID(field string, v FieldValue, rule FieldRule) error {
	if !rule.UUID || !v.Present() {
		return nil
	}
	if _, err := uuid.Parse(v.Text); err != nil {
		return &fieldError{field: field, message: "Must be a valid identifier."}
	}
	return nil
}

func validateEnum(field string, v FieldValue, rule FieldRule) error {
	if len(rule.OneOf) == 0 || !v.Present() {
		return nil
	}
	for _, allowed := range rule.OneOf {
		if v.Text == allowed {
			return nil
		}
	}
	return &fieldError{field: field, message: fmt.Sprintf("Must be one of: %s.", strings.Join(rule.OneOf, ", "))}
}

func validateRange(field string, v FieldValue, rule FieldRule) error {
	if rule.Min == nil && rule.Max == nil {
		return nil
	}
	if !v.Present() {
		return nil
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(v.Text), 64)
	if err != nil {
		return &fieldError{field: field, message: "Must be a number."}
	}
	if rule.Min != nil && n < *rule.Min {
		return &fieldError{field: field, message: fmt.Sprintf("Must be at least %g.", *rule.Min)}
	}
	if rule.Max != nil && n > *rule.Max {
		return &fieldError{field: field, message: fmt.Sprintf("Must be at most %g.", *rule.Max)}
	}
	return nil
}

// fieldError is a single field's failure, produced by the rule checks and
// aggregated by Validate.
type fieldError struct {
	field   string
	message string
}

func (e *fieldError) Error() string { return e.field + ": " + e.message }

// validationError reports every field that failed, so the client can show all
// the problems in one pass instead of one per attempt.
type validationError struct {
	fields map[string]string
}

func (e *validationError) Error() string {
	if len(e.fields) == 1 {
		for _, message := range e.fields {
			return message
		}
	}
	return fmt.Sprintf("%d fields failed validation", len(e.fields))
}

// ToAPIError converts a validation failure into the shared error envelope.
func (e *validationError) ToAPIError() *APIError {
	msg := "Some fields need attention."
	if len(e.fields) == 1 {
		msg = "One field needs attention."
	}
	return BadRequest(CodeValidationFailed, msg).WithFields(e.fields)
}

// AsValidationError extracts a validation failure from an error chain, so a
// handler can render it with the per-field messages intact.
func AsValidationError(err error) (*APIError, bool) {
	var ve *validationError
	if errors.As(err, &ve) {
		return ve.ToAPIError(), true
	}
	return nil, false
}

// Ptr returns a pointer to v, for optional numeric rule bounds.
func Ptr[T any](v T) *T { return &v }
