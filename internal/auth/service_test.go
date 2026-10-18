package auth

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"dashboard/internal/audit"
	"dashboard/internal/db"
	"dashboard/migrations"
)

type authEnv struct {
	svc   *Service
	store *Store
	conn  *sql.DB
}

func newAuthEnv(t *testing.T, ttl time.Duration) *authEnv {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(ctx, conn, migrations.FS); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	store := NewStore(conn)
	svc := NewService(store, audit.NewLogger(conn), Options{
		SessionTTL:    ttl,
		MaxLoginFails: 3,
		LockoutFor:    time.Minute,
	})
	return &authEnv{svc: svc, store: store, conn: conn}
}

func (e *authEnv) createUser(t *testing.T, username, password string) *User {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	u, err := e.store.CreateUser(context.Background(), username, "Test", hash, "admin")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return u
}

func TestLoginAndAuthenticate(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	env.createUser(t, "admin", "secretpass1")

	user, token, err := env.svc.Login(ctx, "admin", "secretpass1", "test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if user.Username != "admin" || token == "" {
		t.Fatalf("unexpected login result: %+v %q", user, token)
	}

	got, _, err := env.svc.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.ID != user.ID {
		t.Fatalf("authenticated wrong user: %d != %d", got.ID, user.ID)
	}
}

// TestSessionTokenIsHashedAtRest ensures a database leak cannot yield live
// session tokens.
func TestSessionTokenIsHashedAtRest(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	env.createUser(t, "admin", "secretpass1")

	_, token, err := env.svc.Login(ctx, "admin", "secretpass1", "ua", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	var stored string
	if err := env.conn.QueryRowContext(ctx,
		`SELECT token_hash FROM auth_sessions LIMIT 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token {
		t.Fatal("session token stored in plaintext")
	}
	if stored != hashToken(token) {
		t.Fatal("stored value is not the expected hash of the token")
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	env.createUser(t, "admin", "secretpass1")

	if _, _, err := env.svc.Login(ctx, "admin", "wrongpass99", "ua", "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
	// Unknown users must not be distinguishable from wrong passwords.
	if _, _, err := env.svc.Login(ctx, "ghost", "whatever12", "ua", "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials for unknown user, got %v", err)
	}
}

func TestLoginLockoutAfterRepeatedFailures(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	env.createUser(t, "admin", "secretpass1")

	for i := 0; i < 3; i++ {
		_, _, _ = env.svc.Login(ctx, "admin", "badpassword", "ua", "10.0.0.1")
	}
	// Even the correct password must now be refused for this ip+user pair.
	if _, _, err := env.svc.Login(ctx, "admin", "secretpass1", "ua", "10.0.0.1"); !errors.Is(err, ErrLockedOut) {
		t.Fatalf("expected ErrLockedOut, got %v", err)
	}
	// A different client IP is unaffected.
	if _, _, err := env.svc.Login(ctx, "admin", "secretpass1", "ua", "10.0.0.2"); err != nil {
		t.Fatalf("different ip should not be locked out: %v", err)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")

	_, token, err := env.svc.Login(ctx, "admin", "secretpass1", "ua", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := env.svc.Logout(ctx, token, user, "127.0.0.1"); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, _, err := env.svc.Authenticate(ctx, token); err == nil {
		t.Fatal("session still valid after logout")
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")

	token, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := env.store.CreateSession(ctx, token, Session{
		UserID:    user.ID,
		CreatedAt: now.Add(-2 * time.Hour),
		ExpiresAt: now.Add(-time.Hour),
		LastSeen:  now.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.svc.Authenticate(ctx, token); err == nil {
		t.Fatal("expired session was accepted")
	}
}

func TestDisabledUserSessionsAreRejected(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")

	_, token, err := env.svc.Login(ctx, "admin", "secretpass1", "ua", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.conn.ExecContext(ctx, `UPDATE users SET disabled = 1 WHERE id = ?`, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.svc.Authenticate(ctx, token); err == nil {
		t.Fatal("disabled user session was accepted")
	}
}

func TestChangePasswordInvalidatesOtherSessions(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	env.createUser(t, "admin", "secretpass1")

	_, oldToken, err := env.svc.Login(ctx, "admin", "secretpass1", "ua", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	newToken, err := env.svc.ChangePassword(ctx, 1, "admin", "secretpass1", "brandnewpass2", "ua", "127.0.0.1")
	if err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if _, _, err := env.svc.Authenticate(ctx, oldToken); err == nil {
		t.Fatal("old session survived a password change")
	}
	if _, _, err := env.svc.Authenticate(ctx, newToken); err != nil {
		t.Fatalf("new session is not valid: %v", err)
	}
	// Wrong current password must be rejected.
	if _, err := env.svc.ChangePassword(ctx, 1, "admin", "notmypassword", "anotherpass12", "ua", "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestEnsureBootstrapUserCreatesOnce(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)

	if _, created, err := env.svc.EnsureBootstrapUser(ctx, "admin", "bootstrappass1"); err != nil || !created {
		t.Fatalf("first bootstrap: created=%v err=%v", created, err)
	}
	if _, created, err := env.svc.EnsureBootstrapUser(ctx, "admin", "bootstrappass1"); err != nil || created {
		t.Fatalf("second bootstrap should be a no-op: created=%v err=%v", created, err)
	}
	if _, _, err := env.svc.Login(ctx, "admin", "bootstrappass1", "ua", "127.0.0.1"); err != nil {
		t.Fatalf("bootstrap user cannot log in: %v", err)
	}
}

func TestCleanupRemovesExpiredSessions(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")

	token, _ := NewSessionToken()
	now := time.Now()
	if err := env.store.CreateSession(ctx, token, Session{
		UserID: user.ID, CreatedAt: now.Add(-3 * time.Hour),
		ExpiresAt: now.Add(-2 * time.Hour), LastSeen: now.Add(-3 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	env.svc.Cleanup(ctx)

	var n int
	if err := env.conn.QueryRowContext(ctx, `SELECT COUNT(1) FROM auth_sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected expired sessions to be purged, %d remain", n)
	}
}
