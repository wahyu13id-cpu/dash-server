package httpapi

import (
	"errors"
	"net/http"
	"runtime"
	"time"

	"dashboard/internal/auth"
)

// healthResponse is returned by the unauthenticated health endpoint.
type healthResponse struct {
	Status    string `json:"status"`
	Version   string `json:"version"`
	Time      string `json:"time"`
	UptimeSec int64  `json:"uptimeSec"`
}

// Version is set at build time via -ldflags. It defaults to "dev".
var Version = "dev"

var startedAt = time.Now()

// handleHealth reports liveness without authentication. It intentionally
// exposes no configuration or secrets.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Status:    "ok",
		Version:   Version,
		Time:      time.Now().UTC().Format(time.RFC3339),
		UptimeSec: int64(time.Since(startedAt).Seconds()),
	})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginResponse is returned by the two login endpoints. When Status is "ok"
// the session cookies have already been set; when it is "totp_required" the
// caller must submit Challenge with a verification code.
type loginResponse struct {
	Status    string     `json:"status"`
	User      *auth.User `json:"user,omitempty"`
	Expires   string     `json:"expiresAt,omitempty"`
	Challenge string     `json:"challenge,omitempty"`
}

// handleLogin verifies the password and either opens a session or asks for the
// second factor. No session cookie is issued until every factor has passed.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "username and password are required")
		return
	}

	res, err := s.auth.BeginLogin(r.Context(), req.Username, req.Password,
		r.Header.Get("User-Agent"), s.clientIP(r))
	if err != nil {
		switch err {
		case auth.ErrInvalidCredentials:
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid username or password")
		case auth.ErrLockedOut:
			writeError(w, http.StatusTooManyRequests, CodeRateLimited, "too many failed attempts, try again later")
		default:
			writeError(w, http.StatusInternalServerError, CodeInternal, "could not complete login")
		}
		return
	}

	if res.Challenge != "" {
		writeJSON(w, http.StatusOK, loginResponse{Status: "totp_required", Challenge: res.Challenge})
		return
	}

	s.issueSessionCookies(w, res.Token)
	writeJSON(w, http.StatusOK, loginResponse{
		Status:  "ok",
		User:    res.User,
		Expires: time.Now().Add(s.auth.SessionTTL()).UTC().Format(time.RFC3339),
	})
}

type totpLoginRequest struct {
	Challenge string `json:"challenge"`
	Code      string `json:"code"`
}

// handleLoginTOTP completes a two-step login with a TOTP or recovery code.
func (s *Server) handleLoginTOTP(w http.ResponseWriter, r *http.Request) {
	var req totpLoginRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	if req.Challenge == "" || req.Code == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "challenge and code are required")
		return
	}

	user, token, err := s.auth.CompleteTOTPLogin(r.Context(), req.Challenge, req.Code,
		r.Header.Get("User-Agent"), s.clientIP(r))
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrCodeReused):
			writeError(w, http.StatusUnauthorized, CodeUnauthorized,
				"that code was already used; wait for the next code and try again")
		case errors.Is(err, auth.ErrTOTPChallenge):
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "verification expired, please sign in again")
		case errors.Is(err, auth.ErrInvalidTOTP):
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid verification code")
		case errors.Is(err, auth.ErrTOTPDisabled):
			writeError(w, http.StatusConflict, CodeConflict, "two-factor authentication is not enabled")
		case errors.Is(err, auth.ErrLockedOut):
			writeError(w, http.StatusTooManyRequests, CodeRateLimited, "too many failed attempts, try again later")
		default:
			writeError(w, http.StatusInternalServerError, CodeInternal, "could not complete login")
		}
		return
	}

	s.issueSessionCookies(w, token)
	writeJSON(w, http.StatusOK, loginResponse{
		Status:  "ok",
		User:    user,
		Expires: time.Now().Add(s.auth.SessionTTL()).UTC().Format(time.RFC3339),
	})
}

// issueSessionCookies sets the session and CSRF cookies for a fresh login.
func (s *Server) issueSessionCookies(w http.ResponseWriter, token string) {
	csrf, err := auth.NewSessionToken()
	if err != nil {
		csrf = ""
	}
	ttl := s.auth.SessionTTL()
	s.setAuthCookie(w, token, ttl)
	if csrf != "" {
		s.setCSRFCookie(w, csrf, ttl)
	}
}

// handleLogout destroys the current session.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := tokenFrom(r.Context())
	if err := s.auth.Logout(r.Context(), token, userFrom(r.Context()), s.clientIP(r)); err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "could not complete logout")
		return
	}
	clearAuthCookies(w, s.cfg.CookieSecure)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleMe returns the authenticated user.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"user": userFrom(r.Context())})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// handleChangePassword rotates the current user's password and invalidates
// every other session of that user.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	user := userFrom(r.Context())

	token, err := s.auth.ChangePassword(r.Context(), user.ID, user.Username,
		req.CurrentPassword, req.NewPassword, r.Header.Get("User-Agent"), s.clientIP(r))
	if err != nil {
		switch err {
		case auth.ErrInvalidCredentials:
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "current password is incorrect")
		case auth.ErrWeakPassword:
			writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, CodeInternal, "could not update password")
		}
		return
	}

	ttl := s.auth.SessionTTL()
	csrf, _ := auth.NewSessionToken()
	s.setAuthCookie(w, token, ttl)
	s.setCSRFCookie(w, csrf, ttl)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// profileResponse backs the profile page: the account plus the state a user
// needs to judge their own security posture.
type profileResponse struct {
	User              *auth.User `json:"user"`
	ActiveSessions    int        `json:"activeSessions"`
	TOTPEnabled       bool       `json:"totpEnabled"`
	RecoveryRemaining int        `json:"recoveryRemaining"`
	SessionCreatedAt  string     `json:"sessionCreatedAt,omitempty"`
	SessionExpiresAt  string     `json:"sessionExpiresAt,omitempty"`
	SessionIP         string     `json:"sessionIp,omitempty"`
	SessionUserAgent  string     `json:"sessionUserAgent,omitempty"`
	SessionTTLSeconds int64      `json:"sessionTtlSeconds"`
	UsernameMinLength int        `json:"usernameMinLength"`
	UsernameMaxLength int        `json:"usernameMaxLength"`
	PasswordMinLength int        `json:"passwordMinLength"`
}

// handleGetProfile returns the current account together with the session and
// two-factor state the profile page displays.
func (s *Server) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "authentication required")
		return
	}

	resp := profileResponse{
		User:              user,
		SessionTTLSeconds: int64(s.auth.SessionTTL().Seconds()),
		UsernameMinLength: auth.UsernameMinLen,
		UsernameMaxLength: auth.UsernameMaxLen,
		PasswordMinLength: auth.PasswordMinLen,
	}

	if n, err := s.auth.ActiveSessionCount(r.Context(), user.ID); err == nil {
		resp.ActiveSessions = n
	}
	if status, err := s.auth.TOTPStatus(r.Context(), user.ID); err == nil {
		resp.TOTPEnabled = status.Enabled
		resp.RecoveryRemaining = status.RecoveryRemaining
	}
	if sess := sessionFrom(r.Context()); sess != nil {
		resp.SessionCreatedAt = sess.CreatedAt.UTC().Format(time.RFC3339)
		resp.SessionExpiresAt = sess.ExpiresAt.UTC().Format(time.RFC3339)
		resp.SessionIP = sess.IP
		resp.SessionUserAgent = sess.UserAgent
	}

	writeJSON(w, http.StatusOK, resp)
}

// updateProfileRequest carries a self-service edit. The three optional fields
// are pointers so "not sent" is distinguishable from "cleared".
type updateProfileRequest struct {
	CurrentPassword string  `json:"currentPassword"`
	Username        *string `json:"username"`
	DisplayName     *string `json:"displayName"`
	NewPassword     *string `json:"newPassword"`
}

type updateProfileResponse struct {
	OK              bool       `json:"ok"`
	User            *auth.User `json:"user"`
	UsernameChanged bool       `json:"usernameChanged"`
	PasswordChanged bool       `json:"passwordChanged"`
}

// handleUpdateProfile changes the current user's login name, display name
// and/or password. The current password is always required.
func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	var req updateProfileRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	if req.CurrentPassword == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "current password is required")
		return
	}
	user := userFrom(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "authentication required")
		return
	}

	result, err := s.auth.UpdateProfile(r.Context(), user, req.CurrentPassword, auth.ProfileUpdate{
		Username:    req.Username,
		DisplayName: req.DisplayName,
		Password:    req.NewPassword,
	}, r.Header.Get("User-Agent"), s.clientIP(r))
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidCredentials):
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "current password is incorrect")
		case errors.Is(err, auth.ErrUsernameTaken):
			writeError(w, http.StatusConflict, CodeConflict, "that username is already taken")
		case errors.Is(err, auth.ErrInvalidUsername):
			writeError(w, http.StatusBadRequest, CodeInvalidUsername, err.Error())
		case errors.Is(err, auth.ErrWeakPassword):
			writeError(w, http.StatusBadRequest, CodeWeakPassword, err.Error())
		case errors.Is(err, auth.ErrInvalidDisplayName):
			writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, CodeInternal, "could not update the profile")
		}
		return
	}

	// A password change rotates every session, so hand this browser the new
	// token; otherwise the user would be signed out by their own request.
	if result.Token != "" {
		ttl := s.auth.SessionTTL()
		csrf, err := auth.NewSessionToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, CodeInternal, "could not update the profile")
			return
		}
		s.setAuthCookie(w, result.Token, ttl)
		s.setCSRFCookie(w, csrf, ttl)
	}

	writeJSON(w, http.StatusOK, updateProfileResponse{
		OK:              true,
		User:            result.User,
		UsernameChanged: result.UsernameChanged,
		PasswordChanged: result.PasswordChanged,
	})
}

type statsResponse struct {
	UptimeSec      int64  `json:"uptimeSec"`
	GoVersion      string `json:"goVersion"`
	LiveTerminals  int    `json:"liveTerminals"`
	MaxTerminals   int    `json:"maxTerminals"`
	MyTerminals    int    `json:"myTerminals"`
	WorkspaceRoot  string `json:"workspaceRoot"`
	RootEntries    int    `json:"rootEntries"`
	MaxReadBytes   int64  `json:"maxReadBytes"`
	MaxWriteBytes  int64  `json:"maxWriteBytes"`
	MaxUploadBytes int64  `json:"maxUploadBytes"`
	ServerTime     string `json:"serverTime"`
	RecentActivity int    `json:"recentActivity"`
}

// handleStats returns a compact dashboard summary.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats := statsResponse{
		UptimeSec:     int64(time.Since(startedAt).Seconds()),
		GoVersion:     runtime.Version(),
		LiveTerminals: s.term.Count(),
		MaxTerminals:  s.cfg.TerminalMaxSessions,
		MyTerminals:   len(s.term.List(userFrom(r.Context()).ID)),
		WorkspaceRoot: s.files.Workspace().Root(),
		ServerTime:    time.Now().UTC().Format(time.RFC3339),
	}
	stats.MaxReadBytes, stats.MaxWriteBytes, stats.MaxUploadBytes = s.files.Limits()

	if n, err := s.files.RootEntryCount(r.Context()); err == nil {
		stats.RootEntries = n
	}
	if records, err := s.audit.Recent(r.Context(), 1); err == nil {
		stats.RecentActivity = len(records)
	}
	writeJSON(w, http.StatusOK, stats)
}

// handleAuditList returns recent audit entries for the dashboard activity feed.
func (s *Server) handleAuditList(w http.ResponseWriter, r *http.Request) {
	records, err := s.audit.Recent(r.Context(), 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "could not load activity")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": records})
}
