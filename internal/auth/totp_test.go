package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"dashboard/internal/totp"
)

// enableTOTP runs the full enrollment for a user and returns the secret plus
// the one-time recovery codes.
func enableTOTP(t *testing.T, env *authEnv, user *User) (string, []string) {
	t.Helper()
	ctx := context.Background()

	setup, err := env.svc.BeginTOTPSetup(ctx, user)
	if err != nil {
		t.Fatalf("BeginTOTPSetup: %v", err)
	}
	if setup.Secret == "" || setup.URI == "" || len(setup.QRPNG) == 0 {
		t.Fatalf("incomplete setup material: %+v", setup)
	}
	code, err := totp.Code(setup.Secret, time.Now())
	if err != nil {
		t.Fatalf("totp.Code: %v", err)
	}
	codes, err := env.svc.ConfirmTOTPSetup(ctx, user, code)
	if err != nil {
		t.Fatalf("ConfirmTOTPSetup: %v", err)
	}
	if len(codes) != totp.RecoveryCodeCount {
		t.Fatalf("got %d recovery codes, want %d", len(codes), totp.RecoveryCodeCount)
	}
	return setup.Secret, codes
}

func TestTOTPSetupRequiresValidConfirmation(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")

	if _, err := env.svc.BeginTOTPSetup(ctx, user); err != nil {
		t.Fatalf("BeginTOTPSetup: %v", err)
	}
	if _, err := env.svc.ConfirmTOTPSetup(ctx, user, "000000"); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatalf("wrong code: err = %v, want ErrInvalidTOTP", err)
	}
	status, err := env.svc.TOTPStatus(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled {
		t.Fatal("two-factor became active without a valid confirmation")
	}
}

func TestTOTPLoginRequiresSecondFactor(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")
	secret, _ := enableTOTP(t, env, user)

	// A correct password alone must not open a session.
	if _, _, err := env.svc.Login(ctx, "admin", "secretpass1", "ua", "127.0.0.1"); !errors.Is(err, ErrTOTPRequired) {
		t.Fatalf("Login err = %v, want ErrTOTPRequired", err)
	}

	res, err := env.svc.BeginLogin(ctx, "admin", "secretpass1", "ua", "127.0.0.1")
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	if res.Challenge == "" || res.Token != "" {
		t.Fatalf("expected a challenge and no token, got %+v", res)
	}

	code, err := totp.Code(secret, time.Now().Add(totp.Period))
	if err != nil {
		t.Fatal(err)
	}
	got, token, err := env.svc.CompleteTOTPLogin(ctx, res.Challenge, code, "ua", "127.0.0.1")
	if err != nil {
		t.Fatalf("CompleteTOTPLogin: %v", err)
	}
	if got.ID != user.ID || token == "" {
		t.Fatalf("unexpected result: %+v %q", got, token)
	}
	if _, _, err := env.svc.Authenticate(ctx, token); err != nil {
		t.Fatalf("session from second step is not valid: %v", err)
	}
}

func TestTOTPLoginRejectsWrongCode(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")
	enableTOTP(t, env, user)

	res, err := env.svc.BeginLogin(ctx, "admin", "secretpass1", "ua", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.svc.CompleteTOTPLogin(ctx, res.Challenge, "000000", "ua", "127.0.0.1"); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatalf("err = %v, want ErrInvalidTOTP", err)
	}
	// The challenge survives a typo so the user does not restart the login.
	if _, err := env.svc.BeginLogin(ctx, "admin", "secretpass1", "ua", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
}

func TestTOTPChallengeIsBoundToClientAndSingleUse(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")
	secret, _ := enableTOTP(t, env, user)

	res, err := env.svc.BeginLogin(ctx, "admin", "secretpass1", "ua", "10.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.Code(secret, time.Now().Add(totp.Period))
	if err != nil {
		t.Fatal(err)
	}
	// A different client IP may not redeem the challenge.
	if _, _, err := env.svc.CompleteTOTPLogin(ctx, res.Challenge, code, "ua", "10.9.9.9"); !errors.Is(err, ErrTOTPChallenge) {
		t.Fatalf("foreign IP: err = %v, want ErrTOTPChallenge", err)
	}
	if _, _, err := env.svc.CompleteTOTPLogin(ctx, res.Challenge, code, "ua", "10.1.1.1"); err != nil {
		t.Fatalf("CompleteTOTPLogin: %v", err)
	}
	// The challenge is consumed on success.
	next, err := totp.Code(secret, time.Now().Add(2*totp.Period))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.svc.CompleteTOTPLogin(ctx, res.Challenge, next, "ua", "10.1.1.1"); !errors.Is(err, ErrTOTPChallenge) {
		t.Fatalf("reused challenge: err = %v, want ErrTOTPChallenge", err)
	}
}

func TestTOTPRejectsReplayedCode(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")
	secret, _ := enableTOTP(t, env, user)

	code, err := totp.Code(secret, time.Now().Add(totp.Period))
	if err != nil {
		t.Fatal(err)
	}
	first, err := env.svc.BeginLogin(ctx, "admin", "secretpass1", "ua", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.svc.CompleteTOTPLogin(ctx, first.Challenge, code, "ua", "127.0.0.1"); err != nil {
		t.Fatalf("first login: %v", err)
	}

	second, err := env.svc.BeginLogin(ctx, "admin", "secretpass1", "ua", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.svc.CompleteTOTPLogin(ctx, second.Challenge, code, "ua", "127.0.0.1"); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatalf("replayed code: err = %v, want ErrInvalidTOTP", err)
	}
}

func TestTOTPRecoveryCodeIsSingleUse(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")
	_, codes := enableTOTP(t, env, user)

	login := func(code string) error {
		res, err := env.svc.BeginLogin(ctx, "admin", "secretpass1", "ua", "127.0.0.1")
		if err != nil {
			return err
		}
		_, _, err = env.svc.CompleteTOTPLogin(ctx, res.Challenge, code, "ua", "127.0.0.1")
		return err
	}

	if err := login(codes[0]); err != nil {
		t.Fatalf("recovery code rejected: %v", err)
	}
	if err := login(codes[0]); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatalf("recovery code reused: err = %v, want ErrInvalidTOTP", err)
	}
	if err := login(codes[1]); err != nil {
		t.Fatalf("second recovery code rejected: %v", err)
	}

	status, err := env.svc.TOTPStatus(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.RecoveryRemaining != totp.RecoveryCodeCount-2 {
		t.Fatalf("recoveryRemaining = %d, want %d", status.RecoveryRemaining, totp.RecoveryCodeCount-2)
	}
}

func TestDisableTOTPRequiresPasswordAndCode(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")
	secret, _ := enableTOTP(t, env, user)

	if err := env.svc.DisableTOTP(ctx, user, "wrongpassword1", "000000"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: err = %v, want ErrInvalidCredentials", err)
	}
	if err := env.svc.DisableTOTP(ctx, user, "secretpass1", "000000"); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatalf("wrong code: err = %v, want ErrInvalidTOTP", err)
	}
	// Still enforced after the failed attempts.
	if _, _, err := env.svc.Login(ctx, "admin", "secretpass1", "ua", "127.0.0.1"); !errors.Is(err, ErrTOTPRequired) {
		t.Fatalf("two-factor unexpectedly disabled: %v", err)
	}

	code, err := totp.Code(secret, time.Now().Add(totp.Period))
	if err != nil {
		t.Fatal(err)
	}
	if err := env.svc.DisableTOTP(ctx, user, "secretpass1", code); err != nil {
		t.Fatalf("DisableTOTP: %v", err)
	}
	// A single step now suffices again.
	if _, token, err := env.svc.Login(ctx, "admin", "secretpass1", "ua", "127.0.0.1"); err != nil || token == "" {
		t.Fatalf("login after disable: token=%q err=%v", token, err)
	}
	status, err := env.svc.TOTPStatus(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.RecoveryRemaining != 0 {
		t.Fatalf("state not cleared: %+v", status)
	}
}

func TestRegenerateRecoveryCodesInvalidatesOldSet(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")
	secret, old := enableTOTP(t, env, user)

	code, err := totp.Code(secret, time.Now().Add(totp.Period))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := env.svc.RegenerateRecoveryCodes(ctx, user, "secretpass1", code)
	if err != nil {
		t.Fatalf("RegenerateRecoveryCodes: %v", err)
	}
	if len(fresh) != totp.RecoveryCodeCount {
		t.Fatalf("got %d codes, want %d", len(fresh), totp.RecoveryCodeCount)
	}

	res, err := env.svc.BeginLogin(ctx, "admin", "secretpass1", "ua", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.svc.CompleteTOTPLogin(ctx, res.Challenge, old[0], "ua", "127.0.0.1"); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatalf("old recovery code still works: %v", err)
	}
}

func TestResetTOTPByUsername(t *testing.T) {
	ctx := context.Background()
	env := newAuthEnv(t, time.Hour)
	user := env.createUser(t, "admin", "secretpass1")
	enableTOTP(t, env, user)

	if _, _, found, err := env.store.ResetTOTPByUsername(ctx, "ghost"); err != nil || found {
		t.Fatalf("unknown user: found=%v err=%v", found, err)
	}

	id, name, found, err := env.store.ResetTOTPByUsername(ctx, "ADMIN")
	if err != nil || !found {
		t.Fatalf("ResetTOTPByUsername: found=%v err=%v", found, err)
	}
	if id != user.ID || name != "admin" {
		t.Fatalf("unexpected match: id=%d name=%q", id, name)
	}
	if _, token, err := env.svc.Login(ctx, "admin", "secretpass1", "ua", "127.0.0.1"); err != nil || token == "" {
		t.Fatalf("login after reset: token=%q err=%v", token, err)
	}
	status, err := env.svc.TOTPStatus(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.RecoveryRemaining != 0 {
		t.Fatalf("state not cleared: %+v", status)
	}
}
