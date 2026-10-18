package auth

import (
	"strings"
	"testing"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse 123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "correct horse 123" {
		t.Fatal("password was not hashed")
	}
	if !VerifyPassword(hash, "correct horse 123") {
		t.Fatal("verification failed for correct password")
	}
	if VerifyPassword(hash, "wrong password 1") {
		t.Fatal("verification succeeded for wrong password")
	}
}

func TestHashPasswordProducesUniqueSalts(t *testing.T) {
	h1, err := HashPassword("samepassword1")
	if err != nil {
		t.Fatal(err)
	}
	h2, err := HashPassword("samepassword1")
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("expected different hashes due to random salt")
	}
}

func TestValidatePasswordStrength(t *testing.T) {
	cases := []struct {
		pw      string
		wantErr bool
	}{
		{"short1", true},
		{"alllettersnodigits", true},
		{"1234567890123", true},
		{"validpassword1", false},
		{strings.Repeat("a1", 101), true},
	}
	for _, c := range cases {
		err := ValidatePasswordStrength(c.pw)
		if c.wantErr && err == nil {
			t.Fatalf("expected error for %q", c.pw)
		}
		if !c.wantErr && err != nil {
			t.Fatalf("unexpected error for %q: %v", c.pw, err)
		}
	}
}

func TestNewSessionTokenIsRandom(t *testing.T) {
	a, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if a == b || len(a) < 32 {
		t.Fatalf("tokens look weak: %q %q", a, b)
	}
}

func TestConstantTimeEqual(t *testing.T) {
	if !ConstantTimeEqual("abc", "abc") {
		t.Fatal("equal strings reported unequal")
	}
	if ConstantTimeEqual("abc", "abd") {
		t.Fatal("different strings reported equal")
	}
}

func TestNormalizeUsername(t *testing.T) {
	if got := NormalizeUsername("  Admin  "); got != "admin" {
		t.Fatalf("got %q", got)
	}
}
