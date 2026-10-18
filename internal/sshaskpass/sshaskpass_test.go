package sshaskpass

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newBroker(t *testing.T) *Broker {
	t.Helper()
	b, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// use wires the broker's socket and token into the environment Ask reads.
func use(t *testing.T, b *Broker, token string) {
	t.Helper()
	t.Setenv(EnvSocket, b.Socket())
	t.Setenv(EnvToken, token)
}

func TestAskReturnsPasswordAndPassphrase(t *testing.T) {
	b := newBroker(t)
	token, err := b.Grant("web1", "s3cret", "keypass")
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if token == "" {
		t.Fatal("token kosong")
	}
	use(t, b, token)

	got, err := Ask("deploy@web1's password: ")
	if err != nil || got != "s3cret" {
		t.Fatalf("Ask password = %q err=%v", got, err)
	}
	got, err = Ask("Enter passphrase for key '/root/.ssh/id_ed25519': ")
	if err != nil || got != "keypass" {
		t.Fatalf("Ask passphrase = %q err=%v", got, err)
	}
}

func TestHostKeyPromptIsNeverAnswered(t *testing.T) {
	b := newBroker(t)
	token, err := b.Grant("web1", "s3cret", "keypass")
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	use(t, b, token)

	// Only "password" and "passphrase" prompts may ever be answered; anything
	// else must be refused so a fingerprint prompt is never auto-accepted.
	prompts := []string{
		"Are you sure you want to continue connecting (yes/no/[fingerprint])? ",
		"The authenticity of host 'web1' can't be established.",
		"Verification code: ",
		"Enter 2FA code: ",
	}
	for _, p := range prompts {
		if _, err := Ask(p); !errors.Is(err, ErrNoCredentials) {
			t.Errorf("Ask(%q) = %v, want penolakan", p, err)
		}
	}
}

func TestUnknownTokenIsRefused(t *testing.T) {
	b := newBroker(t)
	use(t, b, "deadbeef")
	if _, err := Ask("password: "); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("err = %v, want ErrNoCredentials", err)
	}
}

func TestEmptyGrantMeansNoCredentials(t *testing.T) {
	b := newBroker(t)
	token, err := b.Grant("web1", "", "")
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if token != "" {
		t.Errorf("token = %q, want kosong supaya ssh bertanya di terminal", token)
	}
	if env := Environment("/usr/local/bin/dashboard", b.Socket(), token); env != nil {
		t.Errorf("Environment = %v, want nil", env)
	}
}

func TestGrantWithoutStoredKindIsRefused(t *testing.T) {
	b := newBroker(t)
	token, err := b.Grant("web1", "only-password", "")
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	use(t, b, token)
	if _, err := Ask("Enter passphrase for key '/root/.ssh/id_ed25519': "); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("err = %v, want penolakan untuk passphrase yang tidak tersimpan", err)
	}
}

func TestMaxUsesIsEnforced(t *testing.T) {
	b := newBroker(t)
	token, err := b.Grant("web1", "pw", "")
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	use(t, b, token)

	for i := 0; i < defaultMaxUses; i++ {
		if _, err := Ask("password: "); err != nil {
			t.Fatalf("pemakaian ke-%d gagal: %v", i+1, err)
		}
	}
	if _, err := Ask("password: "); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("pemakaian melebihi batas = %v, want ditolak", err)
	}
}

func TestRevokeAndExpiry(t *testing.T) {
	b := newBroker(t)
	token, err := b.Grant("web1", "pw", "")
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	use(t, b, token)
	b.Revoke(token)
	if _, err := Ask("password: "); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("setelah revoke = %v, want ditolak", err)
	}

	token2, err := b.Grant("web1", "pw", "")
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	use(t, b, token2)
	b.mu.Lock()
	b.grants[token2].expires = time.Now().Add(-time.Minute)
	b.mu.Unlock()
	if _, err := Ask("password: "); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("setelah kedaluwarsa = %v, want ditolak", err)
	}
}

func TestSocketAndDirectoryPermissions(t *testing.T) {
	b := newBroker(t)
	st, err := os.Stat(b.Socket())
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode socket = %04o, want 0600", perm)
	}
	dst, err := os.Stat(filepath.Dir(b.Socket()))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dst.Mode().Perm(); perm != 0o700 {
		t.Errorf("mode dir = %04o, want 0700", perm)
	}
}

func TestCloseRemovesDirectory(t *testing.T) {
	b, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dir := filepath.Dir(b.Socket())
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("direktori masih ada: %v", err)
	}
	// Closing twice is harmless.
	if err := b.Close(); err != nil {
		t.Errorf("Close kedua = %v", err)
	}
}

func TestEnvironmentShape(t *testing.T) {
	env := Environment("/usr/local/bin/dashboard", "/tmp/x/sock", "tok")
	want := map[string]string{
		AskpassEnv: "/usr/local/bin/dashboard",
		RequireEnv: "force",
		EnvSocket:  "/tmp/x/sock",
		EnvToken:   "tok",
		HelperEnv:  HelperValue,
	}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if want[k] != v {
			t.Errorf("%s = %q, want %q", k, v, want[k])
		}
		delete(want, k)
	}
	if len(want) != 0 {
		t.Errorf("variabel hilang: %v", want)
	}

	// OpenSSH exec()s SSH_ASKPASS, so it must be a bare executable path:
	// "<path> <args>" is looked up as a single (nonexistent) file name.
	if strings.ContainsAny(env[0], " \t") {
		t.Errorf("SSH_ASKPASS harus berupa path saja tanpa argumen: %q", env[0])
	}
}

// TestIsHelperInvocation covers the marker that keeps a helper invocation from
// ever starting the server or running a CLI subcommand.
func TestIsHelperInvocation(t *testing.T) {
	t.Setenv(HelperEnv, "")
	if IsHelperInvocation() {
		t.Error("tanpa marker seharusnya bukan helper")
	}
	t.Setenv(HelperEnv, HelperValue)
	if !IsHelperInvocation() {
		t.Error("dengan marker seharusnya helper")
	}
	t.Setenv(HelperEnv, "0")
	if IsHelperInvocation() {
		t.Error("nilai lain seharusnya bukan helper")
	}
}
