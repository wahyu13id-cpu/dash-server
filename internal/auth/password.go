package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is deliberately above the library default to slow down offline
// brute-force attempts against a leaked hash.
const bcryptCost = 12

// Password policy bounds, exported so the API can advertise the same numbers
// the validator enforces.
const (
	PasswordMinLen = 10
	PasswordMaxLen = 200
)

// ErrInvalidCredentials is returned for both unknown users and wrong
// passwords so callers cannot distinguish the two.
var ErrInvalidCredentials = errors.New("invalid username or password")

// HashPassword returns a bcrypt hash of the given plaintext password.
func HashPassword(plain string) (string, error) {
	if err := ValidatePasswordStrength(plain); err != nil {
		return "", err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(h), nil
}

// VerifyPassword compares a plaintext password against a stored hash in
// constant time.
func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// ValidatePasswordStrength enforces a minimal password policy.
func ValidatePasswordStrength(pw string) error {
	if len(pw) < PasswordMinLen {
		return fmt.Errorf("password must be at least %d characters long", PasswordMinLen)
	}
	if len(pw) > PasswordMaxLen {
		return fmt.Errorf("password must be at most %d characters long", PasswordMaxLen)
	}
	var hasLetter, hasDigit bool
	for _, r := range pw {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return errors.New("password must contain at least one letter and one digit")
	}
	return nil
}

// randomBytes returns n cryptographically random bytes.
func randomBytes(n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("read random bytes: %w", err)
	}
	return buf, nil
}

// NewSessionToken generates a cryptographically random, URL-safe token.
func NewSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ConstantTimeEqual compares two strings without leaking timing information.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// NormalizeUsername trims and lowercases a username for storage/lookup.
func NormalizeUsername(u string) string {
	return strings.ToLower(strings.TrimSpace(u))
}
