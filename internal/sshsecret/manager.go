package sshsecret

import (
	"context"
	"database/sql"
)

// Manager ties the sealer and the store together and is what the rest of the
// application talks to.
type Manager struct {
	store  *Store
	sealer *Sealer
}

// NewManager builds a Manager from the database and the session secret.
func NewManager(conn *sql.DB, sessionSecret []byte) (*Manager, error) {
	sealer, err := NewSealer(sessionSecret)
	if err != nil {
		return nil, err
	}
	return &Manager{store: NewStore(conn), sealer: sealer}, nil
}

// Set stores (or replaces) a credential. An empty value clears it, so the UI can
// treat "leave blank" as "do not change" by simply not calling Set.
func (m *Manager) Set(ctx context.Context, alias string, kind Kind, plaintext string) error {
	if !ValidKind(kind) {
		return ErrUnknownKind
	}
	if plaintext == "" {
		return m.Clear(ctx, alias, kind)
	}
	nonce, ciphertext, err := m.sealer.Seal(kind, alias, plaintext)
	if err != nil {
		return err
	}
	if err := m.ensureKeyCheck(ctx); err != nil {
		return err
	}
	return m.store.Set(ctx, alias, kind, nonce, ciphertext)
}

// Get decrypts a credential. The bool reports whether one is stored.
func (m *Manager) Get(ctx context.Context, alias string, kind Kind) (string, bool, error) {
	rec, ok, err := m.store.Get(ctx, alias, kind)
	if err != nil || !ok {
		return "", false, err
	}
	plain, err := m.sealer.Open(kind, alias, rec.Nonce, rec.Ciphertext)
	if err != nil {
		// A failed key check pins the cause on a rotated session secret, which
		// is a far more actionable message than "decrypt failed".
		if kerr := m.VerifyKey(ctx); kerr != nil {
			return "", false, kerr
		}
		return "", false, err
	}
	return plain, true, nil
}

// Status reports which kinds are stored, without decrypting anything.
func (m *Manager) Status(ctx context.Context, alias string) (map[Kind]bool, error) {
	return m.store.Kinds(ctx, alias)
}

// Clear removes one credential.
func (m *Manager) Clear(ctx context.Context, alias string, kind Kind) error {
	if !ValidKind(kind) {
		return ErrUnknownKind
	}
	_, err := m.store.Delete(ctx, alias, kind)
	return err
}

// ClearAlias removes every credential of an alias.
func (m *Manager) ClearAlias(ctx context.Context, alias string) error {
	_, err := m.store.DeleteAlias(ctx, alias)
	return err
}

// VerifyKey confirms that stored credentials can still be decrypted with the
// current session secret. No stored secrets means nothing to verify.
func (m *Manager) VerifyKey(ctx context.Context) error {
	rec, ok, err := m.store.KeyCheck(ctx)
	if err != nil || !ok {
		return err
	}
	return m.sealer.OpenKeyCheck(rec.Nonce, rec.Ciphertext)
}

// ensureKeyCheck writes the key-check blob on first use.
func (m *Manager) ensureKeyCheck(ctx context.Context) error {
	if _, ok, err := m.store.KeyCheck(ctx); err != nil || ok {
		return err
	}
	nonce, ciphertext, err := m.sealer.SealKeyCheck()
	if err != nil {
		return err
	}
	return m.store.SetKeyCheck(ctx, nonce, ciphertext)
}
