package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"dashboard/internal/audit"
	"dashboard/internal/totp"
)

// Service implements authentication use-cases on top of Store.
type Service struct {
	store        *Store
	audit        *audit.Logger
	sessionTTL   time.Duration
	maxLoginFail int
	lockoutFor   time.Duration
	totpIssuer   string
	challengeTTL time.Duration

	mu         sync.Mutex
	attempts   map[string]*attemptRecord
	challenges map[string]*challengeEntry
}

type attemptRecord struct {
	count       int
	firstAt     time.Time
	lockedUntil time.Time
}

// challengeEntry is a password-verified login awaiting its second factor. It
// lives in memory only: a restart mid-login simply asks the user to start over.
type challengeEntry struct {
	userID    int64
	username  string
	ip        string
	expiresAt time.Time
}

// Options configures a Service.
type Options struct {
	SessionTTL    time.Duration
	MaxLoginFails int
	LockoutFor    time.Duration
	// TOTPIssuer is the label shown next to the account in authenticator apps.
	TOTPIssuer string
	// TOTPChallengeTTL bounds how long a pending second-factor challenge stays
	// valid. Defaults to 5 minutes.
	TOTPChallengeTTL time.Duration
}

// NewService builds an auth service.
func NewService(store *Store, auditLog *audit.Logger, opts Options) *Service {
	if opts.SessionTTL <= 0 {
		opts.SessionTTL = 12 * time.Hour
	}
	if opts.MaxLoginFails <= 0 {
		opts.MaxLoginFails = 5
	}
	if opts.LockoutFor <= 0 {
		opts.LockoutFor = 15 * time.Minute
	}
	if opts.TOTPChallengeTTL <= 0 {
		opts.TOTPChallengeTTL = 5 * time.Minute
	}
	return &Service{
		store:        store,
		audit:        auditLog,
		sessionTTL:   opts.SessionTTL,
		maxLoginFail: opts.MaxLoginFails,
		lockoutFor:   opts.LockoutFor,
		totpIssuer:   opts.TOTPIssuer,
		challengeTTL: opts.TOTPChallengeTTL,
		attempts:     make(map[string]*attemptRecord),
		challenges:   make(map[string]*challengeEntry),
	}
}

// SessionTTL exposes the configured session lifetime.
func (s *Service) SessionTTL() time.Duration { return s.sessionTTL }

// TOTPIssuer exposes the configured authenticator label.
func (s *Service) TOTPIssuer() string { return s.totpIssuer }

// ErrLockedOut is returned when too many failed attempts occurred recently.
var ErrLockedOut = errors.New("too many failed login attempts; try again later")

// ErrWeakPassword is returned when a new password fails the policy.
var ErrWeakPassword = errors.New("password does not meet the strength policy")

// ErrTOTPRequired is returned by Login when the account needs a second factor;
// callers must use BeginLogin and CompleteTOTPLogin instead.
var ErrTOTPRequired = errors.New("a verification code is required")

// ErrTOTPChallenge is returned when a pending challenge is unknown, expired or
// was issued to a different client.
var ErrTOTPChallenge = errors.New("verification challenge is invalid or expired")

// ErrTOTPDisabled is returned when two-factor authentication is not active for
// the account.
var ErrTOTPDisabled = errors.New("two-factor authentication is not enabled")

// ErrInvalidTOTP is returned when a verification code (or recovery code) does
// not verify.
var ErrInvalidTOTP = totp.ErrInvalidCode

// ErrCodeReused is returned when a still-valid code was already accepted. The
// caller should wait for the next code to be generated.
var ErrCodeReused = fmt.Errorf("%w: this code has already been used, wait for the next one", totp.ErrInvalidCode)

// ErrTOTPEnabled is returned when enrollment is attempted twice.
var ErrTOTPEnabled = errors.New("two-factor authentication is already enabled")

// LoginResult is the outcome of a password check. Exactly one of Token and
// Challenge is set: Token when the session is fully established, Challenge
// when a second factor is still required.
type LoginResult struct {
	User      *User
	Token     string
	Challenge string
}

// BeginLogin verifies a password and either opens a session or returns a
// challenge for the second factor.
func (s *Service) BeginLogin(ctx context.Context, username, password, userAgent, ip string) (*LoginResult, error) {
	key := NormalizeUsername(username) + "|" + ip
	if s.lockedOut(key) {
		s.audit.Log(ctx, audit.Entry{
			Username: username, Action: audit.ActionLoginFailed, IP: ip,
			Detail: "rate limited", Success: false,
		})
		return nil, ErrLockedOut
	}

	user, hash, err := s.store.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Perform a dummy comparison to reduce timing differences between
			// unknown-user and wrong-password responses.
			VerifyPassword("$2a$12$0000000000000000000000000000000000000000000000000000", password)
			s.recordFailure(key)
			s.audit.Log(ctx, audit.Entry{
				Username: username, Action: audit.ActionLoginFailed, IP: ip,
				Detail: "unknown user", Success: false,
			})
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if user.Disabled || !VerifyPassword(hash, password) {
		s.recordFailure(key)
		s.audit.Log(ctx, audit.Entry{
			UserID: user.ID, Username: user.Username, Action: audit.ActionLoginFailed, IP: ip,
			Detail: "bad credentials", Success: false,
		})
		return nil, ErrInvalidCredentials
	}

	s.clearFailures(key)

	state, err := s.store.GetTOTPState(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	if state.Enabled && state.Secret != "" {
		challenge, err := s.newChallenge(user.ID, user.Username, ip)
		if err != nil {
			return nil, err
		}
		s.audit.Log(ctx, audit.Entry{
			UserID: user.ID, Username: user.Username, Action: audit.ActionTOTPChallenge,
			IP: ip, Detail: "second factor required", Success: true,
		})
		return &LoginResult{User: user, Challenge: challenge}, nil
	}

	token, err := s.createSession(ctx, user, userAgent, ip)
	if err != nil {
		return nil, err
	}
	s.audit.Log(ctx, audit.Entry{
		UserID: user.ID, Username: user.Username, Action: audit.ActionLogin, IP: ip, Success: true,
	})
	return &LoginResult{User: user, Token: token}, nil
}

// Login authenticates a user in a single step. It fails with ErrTOTPRequired
// when the account has two-factor authentication enabled.
func (s *Service) Login(ctx context.Context, username, password, userAgent, ip string) (*User, string, error) {
	res, err := s.BeginLogin(ctx, username, password, userAgent, ip)
	if err != nil {
		return nil, "", err
	}
	if res.Challenge != "" {
		return nil, "", ErrTOTPRequired
	}
	return res.User, res.Token, nil
}

// CompleteTOTPLogin consumes a pending challenge and verifies the second
// factor (a TOTP code or a recovery code), then opens a session.
func (s *Service) CompleteTOTPLogin(ctx context.Context, challenge, code, userAgent, ip string) (*User, string, error) {
	entry := s.challenge(challenge)
	if entry == nil || entry.ip != ip || time.Now().After(entry.expiresAt) {
		return nil, "", ErrTOTPChallenge
	}

	key := "totp|" + NormalizeUsername(entry.username) + "|" + ip
	if s.lockedOut(key) {
		s.audit.Log(ctx, audit.Entry{
			UserID: entry.userID, Username: entry.username, Action: audit.ActionTOTPFailed,
			IP: ip, Detail: "rate limited", Success: false,
		})
		return nil, "", ErrLockedOut
	}

	user, err := s.store.GetUserByID(ctx, entry.userID)
	if err != nil || user.Disabled {
		s.deleteChallenge(challenge)
		return nil, "", ErrTOTPChallenge
	}

	if err := s.verifySecondFactor(ctx, user, code, ip); err != nil {
		s.recordFailure(key)
		return nil, "", err
	}
	s.clearFailures(key)

	token, err := s.createSession(ctx, user, userAgent, ip)
	if err != nil {
		return nil, "", err
	}
	s.deleteChallenge(challenge)
	s.audit.Log(ctx, audit.Entry{
		UserID: user.ID, Username: user.Username, Action: audit.ActionLogin, IP: ip,
		Detail: "second factor verified", Success: true,
	})
	return user, token, nil
}

// ActiveSessionCount reports how many non-expired sessions the user has, which
// the profile page shows as "signed in on N devices".
func (s *Service) ActiveSessionCount(ctx context.Context, userID int64) (int, error) {
	return s.store.CountActiveSessions(ctx, userID, time.Now())
}

// TOTPStatus describes a user's two-factor state for the security page.
type TOTPStatus struct {
	Enabled           bool
	RecoveryRemaining int
}

// TOTPStatus reports whether two-factor authentication is active and how many
// recovery codes remain.
func (s *Service) TOTPStatus(ctx context.Context, userID int64) (*TOTPStatus, error) {
	state, err := s.store.GetTOTPState(ctx, userID)
	if err != nil {
		return nil, err
	}
	left, err := s.store.CountRecoveryCodes(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &TOTPStatus{Enabled: state.Enabled && state.Secret != "", RecoveryRemaining: left}, nil
}

// TOTPSetup is the one-time enrollment material shown to a user.
type TOTPSetup struct {
	Secret string
	URI    string
	QRPNG  []byte
}

// BeginTOTPSetup generates and stores a pending secret for the user. The
// secret only becomes active after ConfirmTOTPSetup.
func (s *Service) BeginTOTPSetup(ctx context.Context, user *User) (*TOTPSetup, error) {
	state, err := s.store.GetTOTPState(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	if state.Enabled && state.Secret != "" {
		return nil, ErrTOTPEnabled
	}

	secret, err := totp.GenerateSecret()
	if err != nil {
		return nil, err
	}
	if err := s.store.SetTOTPSecret(ctx, user.ID, secret); err != nil {
		return nil, err
	}

	uri := totp.URI(s.totpIssuer, user.Username, secret)
	png, err := totp.QRCodePNG(uri, 320)
	if err != nil {
		return nil, err
	}
	s.audit.Log(ctx, audit.Entry{
		UserID: user.ID, Username: user.Username, Action: audit.ActionTOTPSetup,
		Detail: "enrollment started", Success: true,
	})
	return &TOTPSetup{Secret: secret, URI: uri, QRPNG: png}, nil
}

// ConfirmTOTPSetup activates the pending secret after the user proves they can
// generate valid codes, and returns the one-time recovery codes in plaintext.
func (s *Service) ConfirmTOTPSetup(ctx context.Context, user *User, code string) ([]string, error) {
	state, err := s.store.GetTOTPState(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	if state.Enabled {
		return nil, ErrTOTPEnabled
	}
	if state.Secret == "" {
		return nil, errors.New("no pending enrollment; start the setup again")
	}

	counter, ok := totp.Validate(state.Secret, code, time.Now())
	if !ok {
		s.audit.Log(ctx, audit.Entry{
			UserID: user.ID, Username: user.Username, Action: audit.ActionTOTPFailed,
			Detail: "enrollment confirmation failed", Success: false,
		})
		return nil, totp.ErrInvalidCode
	}

	if err := s.store.EnableTOTP(ctx, user.ID, time.Now()); err != nil {
		return nil, err
	}
	if err := s.store.UpdateTOTPCounter(ctx, user.ID, counter); err != nil {
		return nil, err
	}

	codes, hashes, err := newRecoveryCodes(totp.RecoveryCodeCount)
	if err != nil {
		return nil, err
	}
	if err := s.store.ReplaceRecoveryCodes(ctx, user.ID, hashes); err != nil {
		return nil, err
	}

	s.audit.Log(ctx, audit.Entry{
		UserID: user.ID, Username: user.Username, Action: audit.ActionTOTPEnabled, Success: true,
	})
	return codes, nil
}

// DisableTOTP turns two-factor authentication off after re-checking the
// password and a current code (or a recovery code).
func (s *Service) DisableTOTP(ctx context.Context, user *User, password, code string) error {
	if err := s.reauth(ctx, user, password, actionPasswordChange); err != nil {
		return err
	}
	if err := s.verifySecondFactor(ctx, user, code, ""); err != nil {
		return err
	}
	if err := s.store.DisableTOTP(ctx, user.ID); err != nil {
		return err
	}
	s.audit.Log(ctx, audit.Entry{
		UserID: user.ID, Username: user.Username, Action: audit.ActionTOTPDisabled, Success: true,
	})
	return nil
}

// RegenerateRecoveryCodes issues a fresh set of recovery codes, invalidating
// every previous one, after re-checking the password and a current code.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, user *User, password, code string) ([]string, error) {
	if err := s.reauth(ctx, user, password, actionPasswordChange); err != nil {
		return nil, err
	}
	if err := s.verifySecondFactor(ctx, user, code, ""); err != nil {
		return nil, err
	}
	codes, hashes, err := newRecoveryCodes(totp.RecoveryCodeCount)
	if err != nil {
		return nil, err
	}
	if err := s.store.ReplaceRecoveryCodes(ctx, user.ID, hashes); err != nil {
		return nil, err
	}
	s.audit.Log(ctx, audit.Entry{
		UserID: user.ID, Username: user.Username, Action: audit.ActionTOTPRecovery,
		Detail: "recovery codes regenerated", Success: true,
	})
	return codes, nil
}

// reauth re-checks the account password for sensitive operations. action is
// used for the audit entry so a failed attempt is attributed to the right
// feature instead of always looking like a password change.
func (s *Service) reauth(ctx context.Context, user *User, password, action string) error {
	_, hash, err := s.store.GetUserByUsername(ctx, user.Username)
	if err != nil || !VerifyPassword(hash, password) {
		s.audit.Log(ctx, audit.Entry{
			UserID: user.ID, Username: user.Username, Action: action,
			Detail: "re-authentication failed", Success: false,
		})
		return ErrInvalidCredentials
	}
	return nil
}

// verifySecondFactor accepts either a TOTP code or an unused recovery code.
// A recovery code is always consumed; ip is used for audit attribution only.
func (s *Service) verifySecondFactor(ctx context.Context, user *User, code, ip string) error {
	state, err := s.store.GetTOTPState(ctx, user.ID)
	if err != nil {
		return err
	}
	if !state.Enabled || state.Secret == "" {
		return ErrTOTPDisabled
	}

	if counter, ok := totp.Validate(state.Secret, code, time.Now()); ok {
		if counter <= state.LastCounter {
			s.audit.Log(ctx, audit.Entry{
				UserID: user.ID, Username: user.Username, Action: audit.ActionTOTPFailed,
				IP: ip, Detail: "replayed code", Success: false,
			})
			return ErrCodeReused
		}
		return s.store.UpdateTOTPCounter(ctx, user.ID, counter)
	}

	consumed, err := s.store.ConsumeRecoveryCode(ctx, user.ID, totp.HashRecoveryCode(code))
	if err != nil {
		return err
	}
	if consumed {
		s.audit.Log(ctx, audit.Entry{
			UserID: user.ID, Username: user.Username, Action: audit.ActionRecoveryUsed,
			IP: ip, Success: true,
		})
		return nil
	}

	s.audit.Log(ctx, audit.Entry{
		UserID: user.ID, Username: user.Username, Action: audit.ActionTOTPFailed,
		IP: ip, Detail: "invalid code", Success: false,
	})
	return totp.ErrInvalidCode
}

// createSession issues and persists a session token for a user.
func (s *Service) createSession(ctx context.Context, user *User, userAgent, ip string) (string, error) {
	token, err := NewSessionToken()
	if err != nil {
		return "", err
	}
	now := time.Now()
	if err := s.store.CreateSession(ctx, token, Session{
		UserID:    user.ID,
		CreatedAt: now,
		ExpiresAt: now.Add(s.sessionTTL),
		LastSeen:  now,
		UserAgent: truncateUA(userAgent),
		IP:        ip,
	}); err != nil {
		return "", err
	}
	return token, nil
}

// newRecoveryCodes returns both the plaintext codes (shown once) and their
// hashes (stored).
func newRecoveryCodes(n int) ([]string, []string, error) {
	codes, err := totp.GenerateRecoveryCodes(n)
	if err != nil {
		return nil, nil, err
	}
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = totp.HashRecoveryCode(c)
	}
	return codes, hashes, nil
}

// --- challenge bookkeeping -------------------------------------------------

func (s *Service) newChallenge(userID int64, username, ip string) (string, error) {
	token, err := NewSessionToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneChallengesLocked()
	s.challenges[token] = &challengeEntry{
		userID:    userID,
		username:  username,
		ip:        ip,
		expiresAt: time.Now().Add(s.challengeTTL),
	}
	return token, nil
}

// challenge looks up a pending challenge without consuming it, so a mistyped
// code does not force the user back to the password step.
func (s *Service) challenge(token string) *challengeEntry {
	if token == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneChallengesLocked()
	return s.challenges[token]
}

func (s *Service) deleteChallenge(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.challenges, token)
}

func (s *Service) pruneChallengesLocked() {
	now := time.Now()
	for token, entry := range s.challenges {
		if now.After(entry.expiresAt) {
			delete(s.challenges, token)
		}
	}
}

// Authenticate resolves a raw session token to a user.
func (s *Service) Authenticate(ctx context.Context, token string) (*User, *Session, error) {
	if token == "" {
		return nil, nil, sql.ErrNoRows
	}
	user, sess, err := s.store.LookupSession(ctx, token)
	if err != nil {
		return nil, nil, err
	}
	if user.Disabled {
		_ = s.store.DeleteSession(ctx, token)
		return nil, nil, sql.ErrNoRows
	}
	return user, sess, nil
}

// Logout deletes the session for the given token.
func (s *Service) Logout(ctx context.Context, token string, user *User, ip string) error {
	if err := s.store.DeleteSession(ctx, token); err != nil {
		return err
	}
	var uid int64
	var uname string
	if user != nil {
		uid, uname = user.ID, user.Username
	}
	s.audit.Log(ctx, audit.Entry{
		UserID: uid, Username: uname, Action: audit.ActionLogout, IP: ip, Success: true,
	})
	return nil
}

// Touch updates the last-seen timestamp of a session (best effort).
func (s *Service) Touch(ctx context.Context, token string) {
	if err := s.store.TouchSession(ctx, token, time.Now()); err != nil {
		slog.Debug("touch session failed", "error", err)
	}
}

// ChangePassword verifies the current password, stores a new hash, invalidates
// every existing session for the user and returns a fresh session token.
func (s *Service) ChangePassword(ctx context.Context, userID int64, username, current, newPassword, userAgent, ip string) (string, error) {
	_, hash, err := s.store.GetUserByUsername(ctx, username)
	if err != nil {
		return "", ErrInvalidCredentials
	}
	if !VerifyPassword(hash, current) {
		s.audit.Log(ctx, audit.Entry{
			UserID: userID, Username: username, Action: actionPasswordChange,
			IP: ip, Detail: "current password mismatch", Success: false,
		})
		return "", ErrInvalidCredentials
	}
	newHash, err := HashPassword(newPassword)
	if err != nil {
		return "", ErrWeakPassword
	}
	if err := s.store.UpdatePassword(ctx, userID, newHash); err != nil {
		return "", err
	}
	if err := s.store.DeleteUserSessions(ctx, userID); err != nil {
		return "", err
	}
	token, err := NewSessionToken()
	if err != nil {
		return "", err
	}
	now := time.Now()
	if err := s.store.CreateSession(ctx, token, Session{
		UserID:    userID,
		CreatedAt: now,
		ExpiresAt: now.Add(s.sessionTTL),
		LastSeen:  now,
		UserAgent: truncateUA(userAgent),
		IP:        ip,
	}); err != nil {
		return "", err
	}
	s.audit.Log(ctx, audit.Entry{
		UserID: userID, Username: username, Action: actionPasswordChange,
		IP: ip, Success: true,
	})
	return token, nil
}

// Cleanup removes expired sessions.
func (s *Service) Cleanup(ctx context.Context) {
	n, err := s.store.DeleteExpiredSessions(ctx, time.Now())
	if err != nil {
		slog.Error("cleanup sessions failed", "error", err)
		return
	}
	if n > 0 {
		slog.Info("purged expired sessions", "count", n)
	}
}

// EnsureBootstrapUser creates the initial admin account when the users table
// is empty. If no bootstrap password is configured, a random one is generated
// and returned so it can be printed once at startup.
func (s *Service) EnsureBootstrapUser(ctx context.Context, username, password string) (generatedPassword string, created bool, err error) {
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return "", false, err
	}
	if n > 0 {
		return "", false, nil
	}
	if password == "" {
		pw, genErr := generateReadablePassword()
		if genErr != nil {
			return "", false, genErr
		}
		password = pw
		generatedPassword = pw
	}
	hash, err := HashPassword(password)
	if err != nil {
		return "", false, err
	}
	if _, err := s.store.CreateUser(ctx, username, "Administrator", hash, "admin"); err != nil {
		return "", false, err
	}
	s.audit.Log(ctx, audit.Entry{
		Username: username, Action: audit.ActionFileCreate, Target: "user",
		Detail: "bootstrap admin user created", Success: true,
	})
	return generatedPassword, true, nil
}

func (s *Service) lockedOut(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.attempts[key]
	if !ok {
		return false
	}
	if time.Now().Before(rec.lockedUntil) {
		return true
	}
	if time.Since(rec.firstAt) > s.lockoutFor {
		delete(s.attempts, key)
	}
	return false
}

func (s *Service) recordFailure(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.attempts[key]
	if !ok || time.Since(rec.firstAt) > s.lockoutFor {
		rec = &attemptRecord{firstAt: time.Now()}
		s.attempts[key] = rec
	}
	rec.count++
	if rec.count >= s.maxLoginFail {
		rec.lockedUntil = time.Now().Add(s.lockoutFor)
	}
}

func (s *Service) clearFailures(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.attempts, key)
}

func truncateUA(ua string) string {
	if len(ua) > 256 {
		return ua[:256]
	}
	return ua
}

func generateReadablePassword() (string, error) {
	// base32-ish alphabet without ambiguous characters
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	buf := make([]byte, 20)
	raw, err := randomBytes(len(buf))
	if err != nil {
		return "", err
	}
	for i := range buf {
		buf[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(buf), nil
}
