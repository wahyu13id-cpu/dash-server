// Package sshsecret stores SSH credentials (passwords and key passphrases)
// encrypted at rest.
//
// The encryption key is derived with HKDF-SHA256 from DASH_SESSION_SECRET, so a
// leaked database file alone does not expose any credential. Keys and database
// live in different files, which is the whole point of the split. The AAD binds
// every ciphertext to its alias and purpose, so a row cannot be swapped between
// hosts or between a password and a passphrase.
//
// This is encryption at rest, not protection against a fully compromised host:
// whoever can read both the env file and the database can decrypt. What it does
// protect is a stale database dump or backup.
package sshsecret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

const (
	// keyInfo is the HKDF info string; changing it invalidates stored secrets.
	keyInfo = "dash-ssh-secret-v1"
	// keyCheckPlain is a known value sealed into ssh_key_check so a rotated
	// session secret is detected instead of failing silently.
	keyCheckPlain = "dash-ssh-key-check-v1"
	// keyCheckAAD is the fixed associated data for the key-check value.
	keyCheckAAD = "keycheck"
)

// Errors surfaced to the API layer.
var (
	// ErrDecrypt means the ciphertext could not be opened, most likely because
	// DASH_SESSION_SECRET changed since the value was stored.
	ErrDecrypt = errors.New("kredensial tidak dapat didekripsi")
	// ErrKeyMismatch means the key-check failed, which pins the cause to a
	// rotated DASH_SESSION_SECRET rather than a corrupt row.
	ErrKeyMismatch = errors.New("DASH_SESSION_SECRET berubah; isi ulang password tersimpan")
	// ErrUnknownKind means an unsupported secret kind was requested.
	ErrUnknownKind = errors.New("jenis kredensial tidak dikenal")
)

// Kind identifies what a stored secret is used for.
type Kind string

const (
	// KindPassword is the account password ssh prompts for.
	KindPassword Kind = "password"
	// KindPassphrase unlocks a private key.
	KindPassphrase Kind = "passphrase"
)

// ValidKind reports whether k is a supported secret kind.
func ValidKind(k Kind) bool { return k == KindPassword || k == KindPassphrase }

// Kinds lists every supported kind.
func Kinds() []Kind { return []Kind{KindPassword, KindPassphrase} }

// Sealer encrypts and decrypts credentials with AES-256-GCM.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer derives an encryption key from the session secret.
func NewSealer(sessionSecret []byte) (*Sealer, error) {
	if len(sessionSecret) == 0 {
		return nil, errors.New("sshsecret: session secret kosong")
	}
	reader := hkdf.New(sha256.New, sessionSecret, nil, []byte(keyInfo))
	key := make([]byte, 32)
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, fmt.Errorf("sshsecret: derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("sshsecret: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("sshsecret: gcm: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// Seal encrypts plaintext and binds it to alias and kind.
func (s *Sealer) Seal(kind Kind, alias, plaintext string) (nonce, ciphertext string, err error) {
	if !ValidKind(kind) {
		return "", "", ErrUnknownKind
	}
	return s.sealBytes(aad(kind, alias), plaintext)
}

// Open reverses Seal.
func (s *Sealer) Open(kind Kind, alias, nonce, ciphertext string) (string, error) {
	if !ValidKind(kind) {
		return "", ErrUnknownKind
	}
	return s.openBytes(aad(kind, alias), nonce, ciphertext)
}

// SealKeyCheck produces the key-check blob.
func (s *Sealer) SealKeyCheck() (nonce, ciphertext string, err error) {
	return s.sealBytes([]byte(keyCheckAAD), keyCheckPlain)
}

// OpenKeyCheck verifies the key-check blob, returning ErrKeyMismatch when the
// derived key no longer matches.
func (s *Sealer) OpenKeyCheck(nonce, ciphertext string) error {
	plain, err := s.openBytes([]byte(keyCheckAAD), nonce, ciphertext)
	if err != nil || plain != keyCheckPlain {
		return ErrKeyMismatch
	}
	return nil
}

func (s *Sealer) sealBytes(associated []byte, plaintext string) (nonce, ciphertext string, err error) {
	n := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(n); err != nil {
		return "", "", fmt.Errorf("sshsecret: nonce: %w", err)
	}
	sealed := s.aead.Seal(nil, n, []byte(plaintext), associated)
	return base64.StdEncoding.EncodeToString(n),
		base64.StdEncoding.EncodeToString(sealed), nil
}

func (s *Sealer) openBytes(associated []byte, nonce, ciphertext string) (string, error) {
	n, err := base64.StdEncoding.DecodeString(nonce)
	if err != nil || len(n) != s.aead.NonceSize() {
		return "", ErrDecrypt
	}
	ct, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil || len(ct) < s.aead.Overhead() {
		return "", ErrDecrypt
	}
	plain, err := s.aead.Open(nil, n, ct, associated)
	if err != nil {
		return "", ErrDecrypt
	}
	return string(plain), nil
}

func aad(kind Kind, alias string) []byte {
	return []byte(string(kind) + "|" + alias)
}
