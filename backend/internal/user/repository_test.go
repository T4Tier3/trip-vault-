package user

import (
	"strings"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Saurabh@Example.COM", "saurabh@example.com"},
		{"  spaced@example.com  ", "spaced@example.com"},
		{"MiXeD@Example.Com", "mixed@example.com"},
		{"already@lower.com", "already@lower.com"},
	}

	for _, tt := range tests {
		if got := NormalizeEmail(tt.in); got != tt.want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestValidateEmail(t *testing.T) {
	valid := []string{
		"saurabh@example.com",
		"first.last@example.co.uk",
		"user+tag@example.com",
		"user_name@example-domain.com",
		"a@b.co",
	}
	for _, email := range valid {
		if err := ValidateEmail(email); err != nil {
			t.Errorf("ValidateEmail(%q) error = %v, want nil", email, err)
		}
	}

	invalid := []string{
		"",
		"   ",
		"no-at-sign",
		"@example.com",
		"user@",
		"user@nodot",
		"user@.com",
		"user@com.",
		"us er@example.com",
		"user@exa mple.com",
		"a..b@example.com",
		"user@exam<ple.com",
		strings.Repeat("a", 250) + "@example.com",
	}
	for _, email := range invalid {
		if err := ValidateEmail(email); err == nil {
			t.Errorf("ValidateEmail(%q) error = nil, want a validation failure", email)
		}
	}
}

func TestValidateEmail_IsCaseInsensitive(t *testing.T) {
	// A case difference must not change validity, because it does not change the
	// stored value either.
	if err := ValidateEmail("SAURABH@EXAMPLE.COM"); err != nil {
		t.Errorf("ValidateEmail() error = %v, want nil for an upper-case address", err)
	}
}

func TestValidateName(t *testing.T) {
	if err := ValidateName("Saurabh"); err != nil {
		t.Errorf("ValidateName() error = %v, want nil", err)
	}
	if err := ValidateName("  Saurabh  "); err != nil {
		t.Errorf("ValidateName() error = %v, want nil for a padded name", err)
	}

	invalid := []string{"", "   ", strings.Repeat("a", MaxNameLength+1)}
	for _, name := range invalid {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) error = nil, want a failure", name)
		}
	}
}

func TestValidateName_CountsRunesNotBytes(t *testing.T) {
	// 120 multi-byte characters is a legal name; 120 bytes would not be.
	name := strings.Repeat("न", 120)
	if err := ValidateName(name); err != nil {
		t.Errorf("ValidateName() error = %v, want nil: length is measured in characters", err)
	}
}
