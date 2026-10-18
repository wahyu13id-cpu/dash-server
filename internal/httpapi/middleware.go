package httpapi

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"dashboard/internal/audit"
	"dashboard/internal/auth"
)

// Cookie and header names.
const (
	sessionCookieName = "dash_session"
	csrfCookieName    = "dash_csrf"
	csrfHeaderName    = "X-CSRF-Token"
	requestIDHeader   = "X-Request-ID"
)

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxSessionToken
	ctxRequestID
	ctxSession
)

func userFrom(ctx context.Context) *auth.User {
	u, _ := ctx.Value(ctxUser).(*auth.User)
	return u
}

func tokenFrom(ctx context.Context) string {
	t, _ := ctx.Value(ctxSessionToken).(string)
	return t
}

// sessionFrom returns the current login session (expiry, IP, client), which
// the profile page shows so a user can judge where they are signed in.
func sessionFrom(ctx context.Context) *auth.Session {
	s, _ := ctx.Value(ctxSession).(*auth.Session)
	return s
}

func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxRequestID).(string)
	return id
}

// middleware is a standard HTTP middleware signature.
type middleware func(http.Handler) http.Handler

// chain applies middlewares so the first listed runs outermost.
func chain(h http.Handler, mws ...middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// recoverMiddleware converts panics into 500 responses without leaking stacks.
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic recovered",
					"requestId", requestIDFrom(r.Context()),
					"path", r.URL.Path,
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				writeError(w, http.StatusInternalServerError, CodeInternal, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestIDMiddleware attaches a random request id and echoes it back.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" || len(id) > 64 {
			id = newRandomID()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

// statusRecorder captures status/bytes and preserves optional interfaces the
// websocket upgrade needs (Hijacker, Flusher).
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijacking not supported")
	}
	return hj.Hijack()
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the wrapped writer to http.ResponseController, so handlers can
// still reach per-connection features such as SetReadDeadline (the upload route
// needs it for bodies that outlive the server's read timeout).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// loggingMiddleware logs method, path, status and duration. It never logs
// request/response bodies or headers that may contain secrets.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		// The session status strip refreshes every few seconds; keeping those
		// successful polls out of the log is what makes the log readable. A
		// failed poll (404, 401, 500, ...) is still recorded.
		if status == http.StatusOK && isStatsPoll(r.URL.Path) {
			return
		}
		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"bytes", rec.bytes,
			"durationMs", time.Since(start).Milliseconds(),
			"requestId", requestIDFrom(r.Context()),
		)
	})
}

// isStatsPoll reports whether path is the terminal status strip endpoint, whose
// routine refresh would otherwise dominate the request log.
func isStatsPoll(path string) bool {
	return strings.HasPrefix(path, "/api/terminal/sessions/") && strings.HasSuffix(path, "/stats")
}

// securityHeadersMiddleware sets conservative security headers.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		// The SPA is self-hosted; xterm.js injects inline styles, so style-src
		// must allow 'unsafe-inline'. No third-party origins are permitted.
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; font-src 'self' data:; connect-src 'self' ws: wss:; "+
				"object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// originMiddleware validates the Origin/Referer of state-changing requests so
// a cross-site page cannot drive the API even with cookies present.
func (s *Server) originMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin == "" {
			if ref := r.Header.Get("Referer"); ref != "" {
				if u, err := parseOrigin(ref); err == nil {
					origin = u
				}
			}
		}
		if origin != "" && !s.originAllowed(origin, s.requestOrigin(r)) {
			s.audit.Log(r.Context(), s.denyEntry(r, "blocked origin "+origin))
			writeError(w, http.StatusForbidden, CodeForbidden, "request origin is not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// csrfMiddleware implements the double-submit cookie pattern for mutating
// requests made with cookie auth.
func (s *Server) csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if tokenFrom(r.Context()) == "" {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(csrfCookieName)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusForbidden, CodeForbidden, "missing CSRF cookie")
			return
		}
		header := r.Header.Get(csrfHeaderName)
		if header == "" || !auth.ConstantTimeEqual(header, cookie.Value) {
			s.audit.Log(r.Context(), s.denyEntry(r, "csrf token mismatch"))
			writeError(w, http.StatusForbidden, CodeForbidden, "invalid CSRF token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authMiddleware resolves the session cookie and stores the user in context.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "authentication required")
			return
		}
		user, sess, err := s.auth.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			clearAuthCookies(w, s.cfg.CookieSecure)
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "session is invalid or expired")
			return
		}
		s.auth.Touch(r.Context(), cookie.Value)
		ctx := context.WithValue(r.Context(), ctxUser, user)
		ctx = context.WithValue(ctx, ctxSessionToken, cookie.Value)
		ctx = context.WithValue(ctx, ctxSession, sess)
		ctx = audit.WithActor(ctx, audit.Actor{
			UserID:   user.ID,
			Username: user.Username,
			IP:       s.clientIP(r),
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// bodyLimitMiddleware caps request bodies for non-upload routes.
func bodyLimitMiddleware(max int64) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, max)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// denyEntry builds an audit entry for a blocked request, attributing it to the
// current user when known.
func (s *Server) denyEntry(r *http.Request, detail string) audit.Entry {
	e := audit.Entry{
		Action:  audit.ActionSecurityDenied,
		Target:  r.URL.Path,
		Detail:  detail,
		IP:      s.clientIP(r),
		Success: false,
	}
	if u := userFrom(r.Context()); u != nil {
		e.UserID = u.ID
		e.Username = u.Username
	}
	return e
}

// originAllowed accepts the request when its origin matches an explicit
// allow-list entry or is the same origin the request was served from.
func (s *Server) originAllowed(origin, requestOrigin string) bool {
	origin = strings.TrimRight(origin, "/")
	if requestOrigin != "" && strings.EqualFold(strings.TrimRight(requestOrigin, "/"), origin) {
		return true
	}
	for _, allowed := range s.cfg.AllowedOrigins {
		if strings.EqualFold(strings.TrimRight(allowed, "/"), origin) {
			return true
		}
	}
	return false
}

// requestOrigin reconstructs the origin the client used, honoring
// X-Forwarded-Proto when running behind a trusted TLS-terminating proxy.
func (s *Server) requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if s.cfg.TrustedProxy {
		if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto == "https" || proto == "http" {
			scheme = proto
		}
	}
	if r.Host == "" {
		return ""
	}
	return scheme + "://" + r.Host
}

// parseOrigin extracts scheme://host from a URL string.
func parseOrigin(raw string) (string, error) {
	i := strings.Index(raw, "://")
	if i < 0 {
		return "", errors.New("no scheme")
	}
	rest := raw[i+3:]
	slash := strings.IndexByte(rest, '/')
	host := rest
	if slash >= 0 {
		host = rest[:slash]
	}
	if host == "" {
		return "", errors.New("no host")
	}
	return raw[:i+3] + host, nil
}

func newRandomID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// clientIP returns the best-effort client address, honoring X-Forwarded-For
// only when a trusted proxy is configured.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustedProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[0]); ip != "" && len(ip) < 64 && isValidIP(ip) {
				return ip
			}
		}
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" && len(xr) < 64 && isValidIP(xr) {
			return xr
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isValidIP(s string) bool {
	return net.ParseIP(s) != nil
}
