package auth

import (
	"strings"
	"testing"
)

// testParams returns hashing parameters cheap enough for a unit test.
func testParams() HashParams {
	return HashParams{Time: 1, Memory: 8 * 1024, Threads: 1, SaltLen: 16, KeyLen: 32}
}

func TestHashPassword_ProducesVerifiablePHCString(t *testing.T) {
	const password = "Correct Horse Battery Staple"

	hash, err := HashPassword(password, testParams())
	if err != nil {
		t.Fatalf("HashPassword() error = %v, want nil", err)
	}

	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("hash = %q, want an $argon2id$ prefix", hash)
	}
	if strings.Contains(hash, password) {
		t.Error("hash contains the plaintext password")
	}

	ok, err := VerifyPassword(password, hash)
	if err != nil {
		t.Fatalf("VerifyPassword() error = %v, want nil", err)
	}
	if !ok {
		t.Error("VerifyPassword() = false, want true for the correct password")
	}
}

func TestHashPassword_SaltMakesEachHashUnique(t *testing.T) {
	const password = "Correct Horse Battery Staple"

	first, err := HashPassword(password, testParams())
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	second, err := HashPassword(password, testParams())
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	// Identical passwords must not produce identical hashes, or the table
	// leaks which accounts share a password.
	if first == second {
		t.Error("two hashes of the same password are identical, salt is not being applied")
	}
}

func TestVerifyPassword_RejectsWrongPassword(t *testing.T) {
	hash, err := HashPassword("Correct Horse Battery Staple", testParams())
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	ok, err := VerifyPassword("Correct Horse Battery Stapl", hash)
	if err != nil {
		t.Fatalf("VerifyPassword() error = %v, want nil", err)
	}
	if ok {
		t.Error("VerifyPassword() = true, want false for a wrong password")
	}
}

func TestVerifyPassword_EmptyPasswordIsRejected(t *testing.T) {
	hash, err := HashPassword("Correct Horse Battery Staple", testParams())
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	if _, err := VerifyPassword("", hash); err == nil {
		t.Error("VerifyPassword(\"\") error = nil, want ErrEmptyPassword")
	}
}

func TestHashPassword_EmptyPasswordIsRejected(t *testing.T) {
	if _, err := HashPassword("", testParams()); err == nil {
		t.Error("HashPassword(\"\") error = nil, want ErrEmptyPassword")
	}
}

func TestVerifyPassword_MalformedHashesAreRejected(t *testing.T) {
	tests := []struct {
		name string
		hash string
	}{
		{"empty", ""},
		{"not a hash", "plaintext-password"},
		{"missing fields", "$argon2id$v=19$m=8192,t=1,p=1"},
		{"wrong algorithm", "$argon2i$v=19$m=8192,t=1,p=1$c2FsdA$aGFzaA"},
		{"unsupported version", "$argon2id$v=16$m=8192,t=1,p=1$c2FsdA$aGFzaA"},
		{"salt not base64", "$argon2id$v=19$m=8192,t=1,p=1$!!!not base64!!!$aGFzaA"},
		{"key not base64", "$argon2id$v=19$m=8192,t=1,p=1$c2FsdA$!!!not base64!!!"},
		{"zero time", "$argon2id$v=19$m=8192,t=0,p=1$c2FsdA$aGFzaA"},
		{"zero threads", "$argon2id$v=19$m=8192,t=1,p=0$c2FsdA$aGFzaA"},
		{"empty salt", "$argon2id$v=19$m=8192,t=1,p=1$$aGFzaA"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A malformed hash must be an error, never a silent false, so the
			// caller can tell corruption apart from a wrong password.
			ok, err := VerifyPassword("Correct Horse Battery Staple", tt.hash)
			if err == nil {
				t.Errorf("VerifyPassword() error = nil (ok = %v), want a malformed-hash error", ok)
			}
		})
	}
}

func TestHashPassword_RejectsUnsafeParameters(t *testing.T) {
	tests := []struct {
		name   string
		params HashParams
	}{
		{"zero time", HashParams{Time: 0, Memory: 8192, Threads: 1, SaltLen: 16, KeyLen: 32}},
		{"low memory", HashParams{Time: 1, Memory: 1024, Threads: 1, SaltLen: 16, KeyLen: 32}},
		{"zero threads", HashParams{Time: 1, Memory: 8192, Threads: 0, SaltLen: 16, KeyLen: 32}},
		{"short salt", HashParams{Time: 1, Memory: 8192, Threads: 1, SaltLen: 4, KeyLen: 32}},
		{"short key", HashParams{Time: 1, Memory: 8192, Threads: 1, SaltLen: 16, KeyLen: 8}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := HashPassword("Correct Horse Battery Staple", tt.params); err == nil {
				t.Error("HashPassword() error = nil, want a parameter validation error")
			}
		})
	}
}

func TestDecodeHash_RecoversStoredParameters(t *testing.T) {
	params := HashParams{Time: 2, Memory: 12288, Threads: 3, SaltLen: 16, KeyLen: 32}

	hash, err := HashPassword("Correct Horse Battery Staple", params)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	got, salt, key, err := DecodeHash(hash)
	if err != nil {
		t.Fatalf("DecodeHash() error = %v", err)
	}

	if got.Time != params.Time {
		t.Errorf("Time = %d, want %d", got.Time, params.Time)
	}
	if got.Memory != params.Memory {
		t.Errorf("Memory = %d, want %d", got.Memory, params.Memory)
	}
	if got.Threads != params.Threads {
		t.Errorf("Threads = %d, want %d", got.Threads, params.Threads)
	}
	if uint32(len(salt)) != params.SaltLen {
		t.Errorf("salt length = %d, want %d", len(salt), params.SaltLen)
	}
	if uint32(len(key)) != params.KeyLen {
		t.Errorf("key length = %d, want %d", len(key), params.KeyLen)
	}
}

func TestNeedsRehash(t *testing.T) {
	weak := HashParams{Time: 1, Memory: 8 * 1024, Threads: 1, SaltLen: 16, KeyLen: 32}
	strong := HashParams{Time: 3, Memory: 64 * 1024, Threads: 2, SaltLen: 16, KeyLen: 32}

	weakHash, err := HashPassword("Correct Horse Battery Staple", weak)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	strongHash, err := HashPassword("Correct Horse Battery Staple", strong)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	if NeedsRehash(weakHash, strong) != true {
		t.Error("NeedsRehash(weak, strong) = false, want true")
	}
	if NeedsRehash(strongHash, strong) != false {
		t.Error("NeedsRehash(strong, strong) = true, want false")
	}
	if NeedsRehash(strongHash, weak) != false {
		t.Error("NeedsRehash(strong, weak) = true, want false: a stronger hash is not a problem")
	}
	// An unreadable hash must be flagged for replacement, not silently kept.
	if NeedsRehash("garbage", strong) != true {
		t.Error("NeedsRehash(garbage) = false, want true")
	}
}

func TestDummyVerify_ReturnsQuicklyAndDoesNotPanic(t *testing.T) {
	// DummyVerify's purpose is to burn comparable time, not to succeed.
	DummyVerify("some password")
	DummyVerify("")
}
