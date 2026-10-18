package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// APIError is the single error envelope used by every endpoint.
type APIError struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody carries a stable machine code and a human message.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Stable error codes.
const (
	CodeBadRequest   = "bad_request"
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeNotFound     = "not_found"
	CodeConflict     = "conflict"
	CodeTooLarge     = "payload_too_large"
	CodeRateLimited  = "rate_limited"
	CodeInternal     = "internal_error"
	CodeUnavailable  = "unavailable"
	CodeNotAllowed   = "not_allowed"
	// Profile-specific codes so the UI can explain exactly what to fix.
	CodeWeakPassword    = "weak_password"
	CodeInvalidUsername = "invalid_username"
)

// writeJSON serializes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode response", "error", err)
	}
}

// writeError sends a consistent error envelope. Internal error details are
// never leaked: callers pass a safe message.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, APIError{Error: ErrorBody{Code: code, Message: message}})
}

// decodeJSON reads a JSON body into dst with a hard size cap.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	ct := r.Header.Get("Content-Type")
	if ct != "" && !hasJSONPrefix(ct) {
		return errors.New("expected application/json content type")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

func hasJSONPrefix(ct string) bool {
	for i := 0; i < len(ct); i++ {
		if ct[i] == ';' || ct[i] == ' ' {
			ct = ct[:i]
			break
		}
	}
	return ct == "application/json" || ct == "application/json;charset=utf-8"
}
