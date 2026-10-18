package httpapi

import (
	"encoding/base64"
	"errors"
	"net/http"

	"dashboard/internal/auth"
)

// totpStatusResponse describes the caller's two-factor state.
type totpStatusResponse struct {
	Enabled           bool   `json:"enabled"`
	RecoveryRemaining int    `json:"recoveryRemaining"`
	Issuer            string `json:"issuer"`
	Account           string `json:"account"`
}

// handleTOTPStatus returns whether two-factor authentication is enabled.
func (s *Server) handleTOTPStatus(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	status, err := s.auth.TOTPStatus(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "could not load two-factor status")
		return
	}
	writeJSON(w, http.StatusOK, totpStatusResponse{
		Enabled:           status.Enabled,
		RecoveryRemaining: status.RecoveryRemaining,
		Issuer:            s.cfg.TOTPIssuer,
		Account:           user.Username,
	})
}

// totpSetupResponse carries the one-time enrollment material. The QR code is
// inlined as a data URI so it never leaves this origin.
type totpSetupResponse struct {
	Secret string `json:"secret"`
	URI    string `json:"uri"`
	QRPNG  string `json:"qrPng"`
}

// handleTOTPSetup starts (or restarts) enrollment by generating a pending
// secret. Nothing is enforced until it is confirmed with a valid code.
func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	setup, err := s.auth.BeginTOTPSetup(r.Context(), user)
	if err != nil {
		if errors.Is(err, auth.ErrTOTPEnabled) {
			writeError(w, http.StatusConflict, CodeConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, CodeInternal, "could not start two-factor setup")
		return
	}
	writeJSON(w, http.StatusOK, totpSetupResponse{
		Secret: setup.Secret,
		URI:    setup.URI,
		QRPNG:  "data:image/png;base64," + base64.StdEncoding.EncodeToString(setup.QRPNG),
	})
}

type totpCodeRequest struct {
	Code string `json:"code"`
}

type recoveryCodesResponse struct {
	RecoveryCodes []string `json:"recoveryCodes"`
}

// handleTOTPActivate confirms the pending secret and returns the one-time
// recovery codes, which are never retrievable again.
func (s *Server) handleTOTPActivate(w http.ResponseWriter, r *http.Request) {
	var req totpCodeRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	if req.Code == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "a verification code is required")
		return
	}

	user := userFrom(r.Context())
	codes, err := s.auth.ConfirmTOTPSetup(r.Context(), user, req.Code)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidTOTP):
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid verification code")
		case errors.Is(err, auth.ErrTOTPEnabled):
			writeError(w, http.StatusConflict, CodeConflict, err.Error())
		default:
			writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, recoveryCodesResponse{RecoveryCodes: codes})
}

type totpPasswordRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

// handleTOTPDisable turns two-factor authentication off after re-checking the
// password and a current code (or recovery code).
func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	var req totpPasswordRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	if req.Password == "" || req.Code == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "password and code are required")
		return
	}

	user := userFrom(r.Context())
	if err := s.auth.DisableTOTP(r.Context(), user, req.Password, req.Code); err != nil {
		writeTOTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTOTPRegenerateRecovery replaces the recovery codes with a fresh set.
func (s *Server) handleTOTPRegenerateRecovery(w http.ResponseWriter, r *http.Request) {
	var req totpPasswordRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	if req.Password == "" || req.Code == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "password and code are required")
		return
	}

	user := userFrom(r.Context())
	codes, err := s.auth.RegenerateRecoveryCodes(r.Context(), user, req.Password, req.Code)
	if err != nil {
		writeTOTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, recoveryCodesResponse{RecoveryCodes: codes})
}

// writeTOTPError maps TOTP management errors onto API responses.
func writeTOTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "password is incorrect")
	case errors.Is(err, auth.ErrCodeReused):
		writeError(w, http.StatusUnauthorized, CodeUnauthorized,
			"that code was already used; wait for the next code and try again")
	case errors.Is(err, auth.ErrInvalidTOTP):
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid verification code")
	case errors.Is(err, auth.ErrTOTPDisabled):
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, CodeInternal, "could not complete the request")
	}
}
