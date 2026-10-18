package sshsecret

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dashboard/internal/db"
	"dashboard/migrations"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func newManager(t *testing.T, secret string) (*Manager, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ssh.db")
	conn, err := db.Open(ctx, path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.Migrate(ctx, conn, migrations.FS); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	m, err := NewManager(conn, []byte(secret))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m, path
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	m, _ := newManager(t, testSecret)

	if err := m.Set(ctx, "web1", KindPassword, "s3cret-pw"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := m.Set(ctx, "web1", KindPassphrase, "key-pass"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok, err := m.Get(ctx, "web1", KindPassword)
	if err != nil || !ok || got != "s3cret-pw" {
		t.Fatalf("Get password = %q ok=%v err=%v", got, ok, err)
	}
	got, ok, err = m.Get(ctx, "web1", KindPassphrase)
	if err != nil || !ok || got != "key-pass" {
		t.Fatalf("Get passphrase = %q ok=%v err=%v", got, ok, err)
	}

	status, err := m.Status(ctx, "web1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status[KindPassword] || !status[KindPassphrase] {
		t.Errorf("status = %v", status)
	}
	if err := m.VerifyKey(ctx); err != nil {
		t.Errorf("VerifyKey: %v", err)
	}
}

func TestAADBindsAliasAndKind(t *testing.T) {
	ctx := context.Background()
	m, path := newManager(t, testSecret)
	if err := m.Set(ctx, "web1", KindPassword, "pw"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Move the ciphertext to another alias and to another kind; both must fail.
	if _, err := m.store.conn.ExecContext(ctx,
		`UPDATE ssh_secrets SET alias = 'web2' WHERE alias = 'web1'`); err != nil {
		t.Fatalf("update alias: %v", err)
	}
	if _, _, err := m.Get(ctx, "web2", KindPassword); !errors.Is(err, ErrDecrypt) {
		t.Errorf("alias swap err = %v, want ErrDecrypt", err)
	}

	if err := m.Set(ctx, "web3", KindPassword, "pw"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := m.store.conn.ExecContext(ctx,
		`UPDATE ssh_secrets SET kind = 'passphrase' WHERE alias = 'web3'`); err != nil {
		t.Fatalf("update kind: %v", err)
	}
	if _, _, err := m.Get(ctx, "web3", KindPassphrase); !errors.Is(err, ErrDecrypt) {
		t.Errorf("kind swap err = %v, want ErrDecrypt", err)
	}
	_ = path
}

func TestRotatedSessionSecretReportsKeyMismatch(t *testing.T) {
	ctx := context.Background()
	m, path := newManager(t, testSecret)
	if err := m.Set(ctx, "web1", KindPassword, "pw"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	conn, err := db.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer conn.Close()
	rotated, err := NewManager(conn, []byte("ffffffffffffffffffffffffffffffff"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := rotated.VerifyKey(ctx); !errors.Is(err, ErrKeyMismatch) {
		t.Errorf("VerifyKey = %v, want ErrKeyMismatch", err)
	}
	if _, _, err := rotated.Get(ctx, "web1", KindPassword); !errors.Is(err, ErrKeyMismatch) {
		t.Errorf("Get = %v, want ErrKeyMismatch", err)
	}
}

func TestClearAndClearAlias(t *testing.T) {
	ctx := context.Background()
	m, _ := newManager(t, testSecret)

	if err := m.Set(ctx, "web1", KindPassword, "pw"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// An empty value clears rather than storing an empty secret.
	if err := m.Set(ctx, "web1", KindPassword, ""); err != nil {
		t.Fatalf("Set empty: %v", err)
	}
	if _, ok, _ := m.Get(ctx, "web1", KindPassword); ok {
		t.Error("password seharusnya terhapus")
	}

	if err := m.Set(ctx, "web1", KindPassword, "pw"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := m.Set(ctx, "web1", KindPassphrase, "kp"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := m.ClearAlias(ctx, "web1"); err != nil {
		t.Fatalf("ClearAlias: %v", err)
	}
	status, _ := m.Status(ctx, "web1")
	if len(status) != 0 {
		t.Errorf("status = %v, want kosong", status)
	}
}

func TestUnknownKindRejected(t *testing.T) {
	ctx := context.Background()
	m, _ := newManager(t, testSecret)
	if err := m.Set(ctx, "web1", Kind("bogus"), "x"); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("Set = %v, want ErrUnknownKind", err)
	}
}

// TestPlaintextNeverTouchesTheDatabaseFile is the whole point of this package:
// the sqlite file must not contain the credential in readable form.
func TestPlaintextNeverTouchesTheDatabaseFile(t *testing.T) {
	ctx := context.Background()
	const needle = "SUPER-SECRET-NEEDLE-98765"
	m, path := newManager(t, testSecret)
	if err := m.Set(ctx, "web1", KindPassword, needle); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Force the WAL into the main file so the search covers all pages.
	if err := m.store.conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil {
			continue
		}
		if bytes.Contains(data, []byte(needle)) {
			t.Fatalf("plaintext ditemukan di %s", filepath.Base(path+suffix))
		}
	}
}

func TestVerifyKeyWithoutStoredSecrets(t *testing.T) {
	ctx := context.Background()
	m, _ := newManager(t, testSecret)
	if err := m.VerifyKey(ctx); err != nil {
		t.Errorf("VerifyKey tanpa secret = %v, want nil", err)
	}
}
