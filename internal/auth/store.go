package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"dashboard/internal/db"
)

// User is an authenticated principal.
type User struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	Disabled    bool   `json:"disabled"`
	CreatedAt   string `json:"createdAt"`
}

// Session is a persisted login session (never exposes the raw token).
type Session struct {
	UserID    int64
	CreatedAt time.Time
	ExpiresAt time.Time
	LastSeen  time.Time
	UserAgent string
	IP        string
}

// Store handles user and session persistence.
type Store struct {
	conn *sql.DB
}

// NewStore creates a Store.
func NewStore(conn *sql.DB) *Store { return &Store{conn: conn} }

// CountUsers returns the number of user rows.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.conn.QueryRowContext(ctx, `SELECT COUNT(1) FROM users`).Scan(&n)
	return n, err
}

// CreateUser inserts a user with an already-hashed password.
func (s *Store) CreateUser(ctx context.Context, username, displayName, passwordHash, role string) (*User, error) {
	username = NormalizeUsername(username)
	if username == "" {
		return nil, errors.New("username must not be empty")
	}
	if role == "" {
		role = "admin"
	}
	now := db.NormalizeTime(time.Now())
	res, err := s.conn.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, display_name, role, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		username, passwordHash, displayName, role, now, now)
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &User{ID: id, Username: username, DisplayName: displayName, Role: role, CreatedAt: now}, nil
}

// GetUserByUsername loads a user and its password hash.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (*User, string, error) {
	username = NormalizeUsername(username)
	var (
		u    User
		hash string
		dis  int
	)
	err := s.conn.QueryRowContext(ctx,
		`SELECT id, username, password_hash, display_name, role, disabled, created_at
		 FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &hash, &u.DisplayName, &u.Role, &dis, &u.CreatedAt)
	if err != nil {
		return nil, "", err
	}
	u.Disabled = dis == 1
	return &u, hash, nil
}

// GetUserByID loads a user by primary key.
func (s *Store) GetUserByID(ctx context.Context, id int64) (*User, error) {
	var (
		u   User
		dis int
	)
	err := s.conn.QueryRowContext(ctx,
		`SELECT id, username, display_name, role, disabled, created_at
		 FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &dis, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	u.Disabled = dis == 1
	return &u, nil
}

// UpdatePassword sets a new password hash for a user.
func (s *Store) UpdatePassword(ctx context.Context, userID int64, passwordHash string) error {
	_, err := s.conn.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, db.NormalizeTime(time.Now()), userID)
	return err
}

// UsernameTaken reports whether a different account already uses the name.
// The comparison happens inside SQLite so it follows the column's NOCASE
// collation, exactly like the UNIQUE index does.
func (s *Store) UsernameTaken(ctx context.Context, username string, exceptID int64) (bool, error) {
	var id int64
	err := s.conn.QueryRowContext(ctx,
		`SELECT id FROM users WHERE username = ?`, NormalizeUsername(username)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return id != exceptID, nil
}

// UpdateProfile writes the self-service profile fields of a user in a single
// transaction. An empty passwordHash leaves the stored hash untouched, so a
// rename can never half-apply next to a password change.
func (s *Store) UpdateProfile(ctx context.Context, userID int64, username, displayName, passwordHash string) error {
	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	now := db.NormalizeTime(time.Now())
	if passwordHash == "" {
		_, err = tx.ExecContext(ctx,
			`UPDATE users SET username = ?, display_name = ?, updated_at = ? WHERE id = ?`,
			username, displayName, now, userID)
	} else {
		_, err = tx.ExecContext(ctx,
			`UPDATE users SET username = ?, display_name = ?, password_hash = ?, updated_at = ? WHERE id = ?`,
			username, displayName, passwordHash, now, userID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// CreateSession stores a hashed session token.
func (s *Store) CreateSession(ctx context.Context, rawToken string, sess Session) error {
	_, err := s.conn.ExecContext(ctx,
		`INSERT INTO auth_sessions (token_hash, user_id, created_at, expires_at, last_seen, user_agent, ip)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hashToken(rawToken), sess.UserID,
		db.NormalizeTime(sess.CreatedAt), db.NormalizeTime(sess.ExpiresAt),
		db.NormalizeTime(sess.LastSeen), sess.UserAgent, sess.IP)
	return err
}

// LookupSession resolves a raw token to a user, rejecting expired sessions.
func (s *Store) LookupSession(ctx context.Context, rawToken string) (*User, *Session, error) {
	var (
		u         User
		sess      Session
		expires   string
		createdAt string
		lastSeen  string
		dis       int
	)
	err := s.conn.QueryRowContext(ctx,
		`SELECT u.id, u.username, u.display_name, u.role, u.disabled, u.created_at,
		        s.user_id, s.created_at, s.expires_at, s.last_seen, s.user_agent, s.ip
		 FROM auth_sessions s
		 JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = ?`, hashToken(rawToken)).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &dis, &u.CreatedAt,
			&sess.UserID, &createdAt, &expires, &lastSeen, &sess.UserAgent, &sess.IP)
	if err != nil {
		return nil, nil, err
	}
	u.Disabled = dis == 1

	if sess.CreatedAt, err = db.ParseTime(createdAt); err != nil {
		return nil, nil, err
	}
	if sess.ExpiresAt, err = db.ParseTime(expires); err != nil {
		return nil, nil, err
	}
	if sess.LastSeen, err = db.ParseTime(lastSeen); err != nil {
		return nil, nil, err
	}
	if time.Now().After(sess.ExpiresAt) {
		_ = s.DeleteSession(ctx, rawToken)
		return nil, nil, sql.ErrNoRows
	}
	return &u, &sess, nil
}

// TouchSession updates the last_seen timestamp.
func (s *Store) TouchSession(ctx context.Context, rawToken string, at time.Time) error {
	_, err := s.conn.ExecContext(ctx,
		`UPDATE auth_sessions SET last_seen = ? WHERE token_hash = ?`,
		db.NormalizeTime(at), hashToken(rawToken))
	return err
}

// DeleteSession removes one session by raw token.
func (s *Store) DeleteSession(ctx context.Context, rawToken string) error {
	_, err := s.conn.ExecContext(ctx,
		`DELETE FROM auth_sessions WHERE token_hash = ?`, hashToken(rawToken))
	return err
}

// DeleteUserSessions removes every session belonging to a user.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.conn.ExecContext(ctx, `DELETE FROM auth_sessions WHERE user_id = ?`, userID)
	return err
}

// DeleteExpiredSessions purges sessions whose expiry has passed.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.conn.ExecContext(ctx,
		`DELETE FROM auth_sessions WHERE expires_at < ?`, db.NormalizeTime(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountActiveSessions counts non-expired sessions for a user.
func (s *Store) CountActiveSessions(ctx context.Context, userID int64, now time.Time) (int, error) {
	var n int
	err := s.conn.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM auth_sessions WHERE user_id = ? AND expires_at >= ?`,
		userID, db.NormalizeTime(now)).Scan(&n)
	return n, err
}

// --- two-factor (TOTP) state ----------------------------------------------

// TOTPState is the persisted two-factor state of a user.
type TOTPState struct {
	Secret      string
	Enabled     bool
	ConfirmedAt string
	LastCounter int64
}

// GetTOTPState loads the TOTP columns for a user.
func (s *Store) GetTOTPState(ctx context.Context, userID int64) (*TOTPState, error) {
	var (
		st        TOTPState
		enabled   int
		confirmed sql.NullString
	)
	err := s.conn.QueryRowContext(ctx,
		`SELECT totp_secret, totp_enabled, totp_confirmed_at, totp_last_counter
		 FROM users WHERE id = ?`, userID).
		Scan(&st.Secret, &enabled, &confirmed, &st.LastCounter)
	if err != nil {
		return nil, err
	}
	st.Enabled = enabled == 1
	if confirmed.Valid {
		st.ConfirmedAt = confirmed.String
	}
	return &st, nil
}

// SetTOTPSecret stores a not-yet-confirmed secret, replacing any pending one.
func (s *Store) SetTOTPSecret(ctx context.Context, userID int64, secret string) error {
	_, err := s.conn.ExecContext(ctx,
		`UPDATE users SET totp_secret = ?, totp_enabled = 0, totp_confirmed_at = NULL,
		        totp_last_counter = 0, updated_at = ?
		 WHERE id = ?`,
		secret, db.NormalizeTime(time.Now()), userID)
	return err
}

// EnableTOTP flips a confirmed secret into the active state.
func (s *Store) EnableTOTP(ctx context.Context, userID int64, at time.Time) error {
	_, err := s.conn.ExecContext(ctx,
		`UPDATE users SET totp_enabled = 1, totp_confirmed_at = ?, updated_at = ?
		 WHERE id = ?`,
		db.NormalizeTime(at), db.NormalizeTime(time.Now()), userID)
	return err
}

// UpdateTOTPCounter records the last accepted time step (replay protection).
func (s *Store) UpdateTOTPCounter(ctx context.Context, userID, counter int64) error {
	_, err := s.conn.ExecContext(ctx,
		`UPDATE users SET totp_last_counter = ? WHERE id = ?`, counter, userID)
	return err
}

// DisableTOTP clears the secret and removes every recovery code.
func (s *Store) DisableTOTP(ctx context.Context, userID int64) error {
	if _, err := s.conn.ExecContext(ctx,
		`UPDATE users SET totp_secret = '', totp_enabled = 0, totp_confirmed_at = NULL,
		        totp_last_counter = 0, updated_at = ?
		 WHERE id = ?`,
		db.NormalizeTime(time.Now()), userID); err != nil {
		return err
	}
	return s.DeleteRecoveryCodes(ctx, userID)
}

// DeleteRecoveryCodes removes every stored recovery code for a user.
func (s *Store) DeleteRecoveryCodes(ctx context.Context, userID int64) error {
	_, err := s.conn.ExecContext(ctx, `DELETE FROM totp_recovery_codes WHERE user_id = ?`, userID)
	return err
}

// ReplaceRecoveryCodes swaps the user's recovery codes for a new set.
func (s *Store) ReplaceRecoveryCodes(ctx context.Context, userID int64, hashes []string) error {
	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	if _, err := tx.ExecContext(ctx, `DELETE FROM totp_recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	now := db.NormalizeTime(time.Now())
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO totp_recovery_codes (user_id, code_hash, created_at) VALUES (?, ?, ?)`,
			userID, h, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ConsumeRecoveryCode marks a recovery code as used, returning whether it was
// still valid (i.e. not previously consumed).
func (s *Store) ConsumeRecoveryCode(ctx context.Context, userID int64, hash string) (bool, error) {
	res, err := s.conn.ExecContext(ctx,
		`UPDATE totp_recovery_codes SET used_at = ?
		 WHERE user_id = ? AND code_hash = ? AND used_at IS NULL`,
		db.NormalizeTime(time.Now()), userID, hash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// CountRecoveryCodes returns how many recovery codes remain unused.
func (s *Store) CountRecoveryCodes(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.conn.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM totp_recovery_codes WHERE user_id = ? AND used_at IS NULL`,
		userID).Scan(&n)
	return n, err
}

// ResetTOTPByUsername disables two-factor authentication for a named account.
// It exists as a break-glass path for the CLI when both the phone and the
// recovery codes are lost. It reports whether the account was found.
func (s *Store) ResetTOTPByUsername(ctx context.Context, username string) (int64, string, bool, error) {
	username = NormalizeUsername(username)
	var id int64
	var stored string
	err := s.conn.QueryRowContext(ctx,
		`SELECT id, username FROM users WHERE username = ?`, username).Scan(&id, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	if err := s.DisableTOTP(ctx, id); err != nil {
		return 0, "", false, err
	}
	return id, stored, true, nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
