// Package totp implements time-based one-time passwords (RFC 6238) and the
// single-use recovery codes that back them up.
//
// It deliberately depends only on the standard library plus a pure-Go QR
// encoder so the dashboard ships no heavyweight authentication framework. The
// generated secrets and otpauth:// URIs are compatible with Google
// Authenticator, Authy, 1Password and other RFC 6238 clients.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
)

// Parameters compatible with mainstream authenticator apps.
const (
	// Period is the length of one time step.
	Period = 30 * time.Second
	// Digits is the length of a generated code.
	Digits = 6
	// SecretBytes is the size of a generated shared secret (RFC 4226 suggests
	// a 160-bit key for HMAC-SHA1).
	SecretBytes = 20

	// RecoveryCodeCount is how many backup codes are issued per enrollment.
	RecoveryCodeCount = 10
	// recoveryCodeChars is the length of one recovery code, excluding the
	// separating dash.
	recoveryCodeChars = 10

	// skewSteps is how many steps of clock drift are tolerated on each side.
	skewSteps = 1
)

// recoveryAlphabet is base32 without visually ambiguous characters. Its length
// divides 256 exactly, so mapping random bytes onto it introduces no bias.
const recoveryAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// ErrInvalidCode is returned when a supplied code does not verify.
var ErrInvalidCode = errors.New("invalid verification code")

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret returns a new random base32 shared secret.
func GenerateSecret() (string, error) {
	buf := make([]byte, SecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base32NoPad.EncodeToString(buf), nil
}

// URI builds the otpauth:// URI an authenticator app consumes, normally by
// scanning it as a QR code.
func URI(issuer, account, secret string) string {
	label := account
	if issuer != "" {
		label = issuer + ":" + account
	}
	q := url.Values{}
	q.Set("secret", normalizeSecret(secret))
	if issuer != "" {
		q.Set("issuer", issuer)
	}
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprintf("%d", Digits))
	q.Set("period", fmt.Sprintf("%d", int(Period.Seconds())))
	return "otpauth://totp/" + url.PathEscape(label) + "?" + q.Encode()
}

// Validate checks code against secret, tolerating one step of clock skew in
// each direction. It returns the time step that matched so callers can reject
// replays of an already-accepted code.
func Validate(secret, code string, at time.Time) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != Digits || !allDigits(code) {
		return 0, false
	}
	key, err := decodeSecret(secret)
	if err != nil || len(key) == 0 {
		return 0, false
	}
	base := at.Unix() / int64(Period.Seconds())
	for skew := int64(-skewSteps); skew <= skewSteps; skew++ {
		step := base + skew
		if step < 0 {
			continue
		}
		if hmac.Equal([]byte(codeFor(key, step, Digits)), []byte(code)) {
			return step, true
		}
	}
	return 0, false
}

// Code returns the code a client would display for a secret at a given time.
// It is mainly useful for tests and diagnostics.
func Code(secret string, at time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	if len(key) == 0 {
		return "", ErrInvalidCode
	}
	return codeFor(key, at.Unix()/int64(Period.Seconds()), Digits), nil
}

// GenerateRecoveryCodes returns n distinct human-typable backup codes, each
// formatted as "xxxxx-xxxxx".
func GenerateRecoveryCodes(n int) ([]string, error) {
	if n <= 0 {
		n = RecoveryCodeCount
	}
	out := make([]string, 0, n)
	seen := make(map[string]bool, n)
	for len(out) < n {
		raw := make([]byte, recoveryCodeChars)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		var b strings.Builder
		for i, v := range raw {
			if i == recoveryCodeChars/2 {
				b.WriteByte('-')
			}
			b.WriteByte(recoveryAlphabet[int(v)%len(recoveryAlphabet)])
		}
		code := b.String()
		if seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	return out, nil
}

// NormalizeRecoveryCode upper-cases a code and strips the separator so user
// input tolerates dashes, spaces and lower case.
func NormalizeRecoveryCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.NewReplacer("-", "", " ", "").Replace(code)
	return code
}

// HashRecoveryCode returns the hex SHA-256 of a normalized recovery code. The
// codes are high-entropy, so an unsalted digest is sufficient for storage.
func HashRecoveryCode(code string) string {
	sum := sha256.Sum256([]byte(NormalizeRecoveryCode(code)))
	return hex.EncodeToString(sum[:])
}

// QRCodePNG renders data as a PNG QR code of roughly size x size pixels.
func QRCodePNG(data string, size int) ([]byte, error) {
	if size <= 0 {
		size = 320
	}
	code, err := qrcode.New(data, qrcode.Medium)
	if err != nil {
		return nil, fmt.Errorf("encode qr code: %w", err)
	}
	png, err := code.PNG(size)
	if err != nil {
		return nil, fmt.Errorf("render qr code: %w", err)
	}
	return png, nil
}

// codeFor computes an HOTP value (RFC 4226) for a time step.
func codeFor(key []byte, step int64, digits int) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))

	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff

	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, value%mod)
}

// normalizeSecret accepts secrets pasted with spaces, dashes or lower case.
func normalizeSecret(secret string) string {
	secret = strings.ToUpper(strings.TrimSpace(secret))
	return strings.NewReplacer(" ", "", "-", "").Replace(secret)
}

func decodeSecret(secret string) ([]byte, error) {
	s := normalizeSecret(secret)
	if key, err := base32NoPad.DecodeString(s); err == nil {
		return key, nil
	}
	// Some clients emit padded secrets; accept those too.
	return base32.StdEncoding.DecodeString(s)
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
