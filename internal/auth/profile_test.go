package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func strptr(v string) *string { return &v }

func TestValidateUsername(t *testing.T) {
	valid := []string{"abc", "admin", "root2", "a.b-c_d", "user.name", "user-1", "012"}
	for _, name := range valid {
		if err := ValidateUsername(name); err != nil {
			t.Errorf("ValidateUsername(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"", "ab", "Admin", "admin ", " admin", ".admin", "admin.", "-admin", "admin-",
		"ad min", "admin!", "über", "nama_yang_sangat_panjang_sekali_ya_123456",
	}
	for _, name := range invalid {
		if err := ValidateUsername(name); err == nil {
			t.Errorf("ValidateUsername(%q) = nil, want error", name)
		}
	}
}

// TestUpdateProfileRenamesUser covers the whole path: the stored row changes,
// the old login name stops working and the new one works.
func TestUpdateProfileRenamesUser(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")

	res, err := env.svc.UpdateProfile(ctx, user, "secretpass1", ProfileUpdate{
		Username:    strptr("wahyu"),
		DisplayName: strptr("Wahyu Hidayat"),
	}, "test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if !res.UsernameChanged || !res.DisplayChanged {
		t.Fatalf("expected username and display name to change, got %+v", res)
	}
	if res.Token != "" {
		t.Error("renaming must not rotate sessions")
	}
	if res.User.Username != "wahyu" || res.User.DisplayName != "Wahyu Hidayat" {
		t.Fatalf("unexpected user after update: %+v", res.User)
	}

	if _, _, err := env.svc.Login(ctx, "admin", "secretpass1", "ua", "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("login with the old name = %v, want ErrInvalidCredentials", err)
	}
	if _, _, err := env.svc.Login(ctx, "wahyu", "secretpass1", "ua", "127.0.0.1"); err != nil {
		t.Errorf("login with the new name: %v", err)
	}

	var n int
	if err := env.conn.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM audit_logs WHERE action = ?`, "auth.profile_update").Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if n == 0 {
		t.Error("profile update was not audited")
	}
}

// TestUpdateProfileNormalizesUsername ensures "  ADMIN " renames to "admin"
// and is treated as a no-op when it matches the stored name.
func TestUpdateProfileNormalizesUsername(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")

	res, err := env.svc.UpdateProfile(ctx, user, "secretpass1", ProfileUpdate{
		Username: strptr("  ADMIN  "),
	}, "ua", "127.0.0.1")
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if res.UsernameChanged || res.Changed() {
		t.Fatalf("expected a no-op, got %+v", res)
	}
	if res.Token != "" {
		t.Error("a no-op must not rotate sessions")
	}
}

func TestUpdateProfileRequiresCurrentPassword(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")

	if _, err := env.svc.UpdateProfile(ctx, user, "wrongpass1", ProfileUpdate{
		Username: strptr("wahyu"),
	}, "ua", "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("UpdateProfile with a wrong password = %v, want ErrInvalidCredentials", err)
	}
	stored, _, err := env.store.GetUserByUsername(ctx, "admin")
	if err != nil || stored.ID != user.ID {
		t.Fatalf("the user was changed despite a failed re-authentication: %v", err)
	}
}

func TestUpdateProfileRejectsTakenAndInvalidUsernames(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")
	env.createUser(t, "operator", "operator12")

	if _, err := env.svc.UpdateProfile(ctx, user, "secretpass1", ProfileUpdate{
		Username: strptr("Operator"),
	}, "ua", "127.0.0.1"); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("taken username = %v, want ErrUsernameTaken", err)
	}
	if _, err := env.svc.UpdateProfile(ctx, user, "secretpass1", ProfileUpdate{
		Username: strptr("bad name!"),
	}, "ua", "127.0.0.1"); !errors.Is(err, ErrInvalidUsername) {
		t.Errorf("invalid username = %v, want ErrInvalidUsername", err)
	}
	if _, err := env.svc.UpdateProfile(ctx, user, "secretpass1", ProfileUpdate{
		DisplayName: strptr(string(make([]rune, maxDisplayNameRunes+1))),
	}, "ua", "127.0.0.1"); !errors.Is(err, ErrInvalidDisplayName) {
		t.Errorf("long display name = %v, want ErrInvalidDisplayName", err)
	}
}

// TestUpdateProfilePasswordRotatesSessions is the important one: a password
// change must end every other session but keep the current browser usable.
func TestUpdateProfilePasswordRotatesSessions(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")

	_, first, err := env.svc.Login(ctx, "admin", "secretpass1", "laptop", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := env.svc.Login(ctx, "admin", "secretpass1", "phone", "10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}

	res, err := env.svc.UpdateProfile(ctx, user, "secretpass1", ProfileUpdate{
		Password: strptr("brandnewpass2"),
	}, "laptop", "10.0.0.1")
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if !res.PasswordChanged || res.Token == "" {
		t.Fatalf("expected a rotated session token, got %+v", res)
	}

	for name, token := range map[string]string{"first": first, "second": second} {
		if _, _, err := env.svc.Authenticate(ctx, token); err == nil {
			t.Errorf("%s session still valid after the password change", name)
		}
	}
	if _, _, err := env.svc.Authenticate(ctx, res.Token); err != nil {
		t.Errorf("the rotated token does not authenticate: %v", err)
	}
	if _, _, err := env.svc.Login(ctx, "admin", "secretpass1", "ua", "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("the old password still logs in: %v", err)
	}
	if _, _, err := env.svc.Login(ctx, "admin", "brandnewpass2", "ua", "127.0.0.1"); err != nil {
		t.Errorf("the new password does not log in: %v", err)
	}
}

func TestUpdateProfileRejectsWeakPassword(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")

	if _, err := env.svc.UpdateProfile(ctx, user, "secretpass1", ProfileUpdate{
		Password: strptr("short"),
	}, "ua", "127.0.0.1"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak password = %v, want ErrWeakPassword", err)
	}
	// The stored hash must still be the original one.
	_, hash, err := env.store.GetUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "secretpass1") {
		t.Error("the original password stopped working after a rejected change")
	}
}

// TestUpdateProfileAuditNotesRename checks the audit trail records the rename
// without ever storing a password.
func TestUpdateProfileAuditNotesRename(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")

	if _, err := env.svc.UpdateProfile(ctx, user, "secretpass1", ProfileUpdate{
		Username: strptr("wahyu"),
		Password: strptr("brandnewpass2"),
	}, "ua", "127.0.0.1"); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}

	var detail string
	err := env.conn.QueryRowContext(ctx,
		`SELECT detail FROM audit_logs WHERE action = ? ORDER BY id DESC LIMIT 1`, "auth.profile_update").
		Scan(&detail)
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatal("no audit entry for the profile update")
	}
	if err != nil {
		t.Fatal(err)
	}
	if detail != "username admin -> wahyu, password" {
		t.Errorf("audit detail = %q, want the changed fields", detail)
	}
	if strings.Contains(detail, "brandnewpass2") {
		t.Error("the audit detail leaked the password")
	}
}
