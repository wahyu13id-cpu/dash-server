package totp

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// rfcSecret is the shared secret from RFC 6238 Appendix B, base32 encoded.
const (
	rfcSecret   = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // "12345678901234567890"
	rfcSecret6  = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	knownIssuer = "Server Dashboard"
)

// TestHOTPMatchesRFC6238Vectors checks the core HOTP computation against the
// published SHA-1 test vectors (8 digits).
func TestHOTPMatchesRFC6238Vectors(t *testing.T) {
	key := []byte("12345678901234567890")
	vectors := []struct {
		unix int64
		want string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
	for _, v := range vectors {
		step := v.unix / int64(Period.Seconds())
		if got := codeFor(key, step, 8); got != v.want {
			t.Errorf("codeFor(step=%d) = %s, want %s", step, got, v.want)
		}
	}
}

// TestValidateAcceptsSixDigitCodeDerivedFromVector confirms the 6-digit code
// is the truncated form of the RFC value.
func TestValidateAcceptsSixDigitCodeDerivedFromVector(t *testing.T) {
	// T=59 -> 94287082 -> last six digits 287082, time step 1.
	step, ok := Validate(rfcSecret6, "287082", time.Unix(59, 0))
	if !ok {
		t.Fatal("expected the RFC vector derived code to validate")
	}
	if step != 1 {
		t.Fatalf("step = %d, want 1", step)
	}
}

func TestValidateRejectsWrongAndMalformedCodes(t *testing.T) {
	at := time.Unix(1111111111, 0)
	cases := []string{"", "000000", "12345", "abcdef", "1234567"}
	for _, code := range cases {
		if _, ok := Validate(rfcSecret6, code, at); ok {
			t.Errorf("code %q was accepted", code)
		}
	}
}

// TestValidateToleratesClockSkew accepts one step of drift on each side but
// nothing beyond that.
func TestValidateToleratesClockSkew(t *testing.T) {
	key := []byte("12345678901234567890")
	const at = int64(300) // step 10
	base := at / int64(Period.Seconds())

	within := map[int64]bool{base - 1: true, base: true, base + 1: true}
	for step, want := range within {
		code := codeFor(key, step, Digits)
		_, ok := Validate(rfcSecret6, code, time.Unix(at, 0))
		if ok != want {
			t.Errorf("step %d: validated=%v, want %v", step, ok, want)
		}
	}
	for _, step := range []int64{base - 2, base + 2, base + 5} {
		code := codeFor(key, step, Digits)
		if _, ok := Validate(rfcSecret6, code, time.Unix(at, 0)); ok {
			t.Errorf("step %d should be outside the accepted skew window", step)
		}
	}
}

// TestValidateAcceptsFormattedSecret tolerates lower case and separators.
func TestValidateAcceptsFormattedSecret(t *testing.T) {
	at := time.Unix(1234567890, 0)
	code, err := Code(rfcSecret6, at)
	if err != nil {
		t.Fatal(err)
	}
	messy := strings.ToLower(rfcSecret6[:8] + " " + rfcSecret6[8:])
	if _, ok := Validate(messy, code, at); !ok {
		t.Fatal("formatted secret was rejected")
	}
}

func TestGenerateSecretIsDecodableAndUnique(t *testing.T) {
	a, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two generated secrets are identical")
	}
	if len(a) != 32 {
		t.Fatalf("secret length = %d, want 32 base32 chars", len(a))
	}
	if _, err := decodeSecret(a); err != nil {
		t.Fatalf("generated secret does not decode: %v", err)
	}
}

func TestURIContainsRequiredParameters(t *testing.T) {
	uri := URI(knownIssuer, "admin", rfcSecret6)
	for _, want := range []string{
		"otpauth://totp/",
		"secret=" + rfcSecret6,
		"issuer=Server+Dashboard",
		"algorithm=SHA1",
		"digits=6",
		"period=30",
	} {
		if !strings.Contains(uri, want) {
			t.Errorf("URI %q is missing %q", uri, want)
		}
	}
}

func TestGenerateRecoveryCodes(t *testing.T) {
	codes, err := GenerateRecoveryCodes(RecoveryCodeCount)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != RecoveryCodeCount {
		t.Fatalf("got %d codes, want %d", len(codes), RecoveryCodeCount)
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if seen[c] {
			t.Fatalf("duplicate recovery code %q", c)
		}
		seen[c] = true
		if len(c) != recoveryCodeChars+1 || c[recoveryCodeChars/2] != '-' {
			t.Fatalf("recovery code %q has an unexpected shape", c)
		}
		for _, r := range NormalizeRecoveryCode(c) {
			if !strings.ContainsRune(recoveryAlphabet, r) {
				t.Fatalf("recovery code %q contains %q", c, r)
			}
		}
	}
}

func TestHashRecoveryCodeNormalizesInput(t *testing.T) {
	a := HashRecoveryCode("abcde-fghij")
	b := HashRecoveryCode("ABCDE FGHIJ")
	if a != b {
		t.Fatalf("hash depends on formatting: %s != %s", a, b)
	}
	if HashRecoveryCode("abcde-fghik") == a {
		t.Fatal("different codes hashed identically")
	}
}

func TestQRCodePNGIsAPNG(t *testing.T) {
	png, err := QRCodePNG(URI(knownIssuer, "admin", rfcSecret6), 200)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(png, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal("output is not a PNG")
	}
	if len(png) < 100 {
		t.Fatalf("PNG is suspiciously small (%d bytes)", len(png))
	}
}
