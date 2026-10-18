// Package httpapi wires the HTTP layer: routing, middleware and handlers.
// It depends on the domain packages (auth, files, terminal, audit) but none of
// them depend on it, keeping the dependency direction clean.
package httpapi

import (
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"dashboard/internal/audit"
	"dashboard/internal/auth"
	"dashboard/internal/config"
	"dashboard/internal/files"
	"dashboard/internal/sshsvc"
	"dashboard/internal/sysinfo"
	"dashboard/internal/terminal"
)

// Server holds the HTTP dependencies.
type Server struct {
	cfg     *config.Config
	auth    *auth.Service
	files   *files.Service
	term    *terminal.Manager
	audit   *audit.Logger
	monitor *sysinfo.Collector
	ssh     *sshsvc.Service

	upgrader websocket.Upgrader
}

// New builds a Server. monitor may be nil, in which case the system metrics
// endpoint reports that no snapshot is available. sshSvc may be nil, in which
// case every SSH endpoint reports that the feature is unavailable.
func New(cfg *config.Config, authSvc *auth.Service, filesSvc *files.Service, termMgr *terminal.Manager, auditLog *audit.Logger, monitor *sysinfo.Collector, sshSvc *sshsvc.Service) *Server {
	s := &Server{cfg: cfg, auth: authSvc, files: filesSvc, term: termMgr, audit: auditLog, monitor: monitor, ssh: sshSvc}
	s.upgrader = websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		// Origin is validated explicitly against the allow-list; never allow
		// all origins.
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true // non-browser clients (no Origin header)
			}
			return s.originAllowed(origin, s.requestOrigin(r))
		},
	}
	return s
}

// Handler returns the fully wired HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// --- Public routes -----------------------------------------------------
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/login/totp", s.handleLoginTOTP)

	// --- Authenticated routes ---------------------------------------------
	protect := func(h http.HandlerFunc) http.Handler {
		return chain(h, s.authMiddleware, s.csrfMiddleware)
	}

	mux.Handle("POST /api/auth/logout", protect(s.handleLogout))
	mux.Handle("GET /api/auth/me", protect(s.handleMe))
	mux.Handle("POST /api/auth/password", protect(s.handleChangePassword))

	// Profile (username, display name, password) for the signed-in user.
	mux.Handle("GET /api/auth/profile", protect(s.handleGetProfile))
	mux.Handle("PATCH /api/auth/profile", protect(s.handleUpdateProfile))

	// Two-factor authentication
	mux.Handle("GET /api/auth/totp", protect(s.handleTOTPStatus))
	mux.Handle("POST /api/auth/totp/setup", protect(s.handleTOTPSetup))
	mux.Handle("POST /api/auth/totp/activate", protect(s.handleTOTPActivate))
	mux.Handle("POST /api/auth/totp/disable", protect(s.handleTOTPDisable))
	mux.Handle("POST /api/auth/totp/recovery/regenerate", protect(s.handleTOTPRegenerateRecovery))

	mux.Handle("GET /api/stats", protect(s.handleStats))
	mux.Handle("GET /api/system", protect(s.handleSystem))
	mux.Handle("GET /api/audit", protect(s.handleAuditList))

	// Files
	mux.Handle("GET /api/files", protect(s.handleFilesList))
	mux.Handle("GET /api/files/content", protect(s.handleFileContentGet))
	// Downloads stream from the file itself (range requests supported).
	mux.Handle("GET /api/files/download", protect(s.handleFileDownload))
	mux.Handle("PUT /api/files/content", protect(s.handleFileContentPut))
	mux.Handle("POST /api/files", protect(s.handleFilesCreate))
	// Uploads are streamed straight to disk; the multipart-free body is the file
	// itself (see handleFileUpload).
	mux.Handle("POST /api/files/upload", protect(s.handleFileUpload))
	mux.Handle("PATCH /api/files", protect(s.handleFilesRename))
	mux.Handle("DELETE /api/files", protect(s.handleFilesDelete))
	// Copy, move, permissions, properties and archives.
	mux.Handle("POST /api/files/copy", protect(s.handleFilesCopy))
	mux.Handle("POST /api/files/move", protect(s.handleFilesMove))
	mux.Handle("PATCH /api/files/mode", protect(s.handleFileChmod))
	mux.Handle("GET /api/files/stat", protect(s.handleFileStat))
	// Archives stream while they are built (see handleFileZip).
	mux.Handle("GET /api/files/zip", protect(s.handleFileZip))
	mux.Handle("POST /api/files/unzip", protect(s.handleFileUnzip))

	// Terminal
	mux.Handle("GET /api/terminal/sessions", protect(s.handleTerminalList))
	mux.Handle("POST /api/terminal/sessions", protect(s.handleTerminalCreate))
	mux.Handle("DELETE /api/terminal/sessions/{id}", protect(s.handleTerminalDelete))
	mux.Handle("GET /api/terminal/sessions/{id}/stats", protect(s.handleTerminalStats))
	// The websocket stream is authenticated by cookie and origin-checked by the
	// upgrader; it cannot carry the CSRF header, so csrfMiddleware is omitted.
	mux.Handle("GET /api/terminal/sessions/{id}/stream",
		s.authMiddleware(http.HandlerFunc(s.handleTerminalStream)))

	// SSH connection manager
	mux.Handle("GET /api/ssh/state", protect(s.handleSSHState))
	mux.Handle("GET /api/ssh/keys", protect(s.handleSSHKeys))
	mux.Handle("POST /api/ssh/parse", protect(s.handleSSHParse))
	mux.Handle("POST /api/ssh/hosts", protect(s.handleSSHHostCreate))
	mux.Handle("GET /api/ssh/hosts/{alias}", protect(s.handleSSHHostGet))
	mux.Handle("PATCH /api/ssh/hosts/{alias}", protect(s.handleSSHHostUpdate))
	mux.Handle("DELETE /api/ssh/hosts/{alias}", protect(s.handleSSHHostDelete))
	mux.Handle("POST /api/ssh/hosts/{alias}/favorite", protect(s.handleSSHFavorite))
	mux.Handle("PUT /api/ssh/hosts/{alias}/note", protect(s.handleSSHNote))
	mux.Handle("GET /api/ssh/hosts/{alias}/effective", protect(s.handleSSHEffective))
	mux.Handle("POST /api/ssh/hosts/{alias}/test", protect(s.handleSSHTest))
	mux.Handle("PUT /api/ssh/hosts/{alias}/secret", protect(s.handleSSHSecretSet))
	mux.Handle("DELETE /api/ssh/hosts/{alias}/secret", protect(s.handleSSHSecretClear))
	mux.Handle("GET /api/ssh/hosts/{alias}/hostkey", protect(s.handleSSHHostKeyStatus))
	mux.Handle("POST /api/ssh/hosts/{alias}/hostkey/scan", protect(s.handleSSHHostKeyScan))
	mux.Handle("POST /api/ssh/hosts/{alias}/hostkey/trust", protect(s.handleSSHHostKeyTrust))
	mux.Handle("DELETE /api/ssh/hosts/{alias}/hostkey", protect(s.handleSSHHostKeyForget))

	// --- Static SPA (optional) --------------------------------------------
	if s.cfg.StaticDir != "" {
		s.mountStatic(mux)
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				writeJSON(w, http.StatusOK, map[string]string{
					"service": "dashboard-api",
					"note":    "frontend build not found; set DASH_STATIC_DIR",
				})
				return
			}
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
		})
	}

	bodyLimit := int64(2 << 20)
	if s.cfg.MaxWriteSize*2 > bodyLimit {
		bodyLimit = s.cfg.MaxWriteSize*2 + (64 << 10)
	}

	return chain(mux,
		recoverMiddleware,
		requestIDMiddleware,
		s.loggingMiddleware,
		securityHeadersMiddleware,
		s.originMiddleware,
		bodyLimitMiddleware(bodyLimit),
	)
}

// --- cookie helpers --------------------------------------------------------

func (s *Server) setAuthCookie(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		Expires:  time.Now().Add(ttl),
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) setCSRFCookie(w http.ResponseWriter, token string, ttl time.Duration) {
	// Readable by JS on purpose: the double-submit pattern requires the client
	// to echo it back in a header.
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		Expires:  time.Now().Add(ttl),
		HttpOnly: false,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearAuthCookies(w http.ResponseWriter, secure bool) {
	for _, name := range []string{sessionCookieName, csrfCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			Expires:  time.Unix(0, 0),
			HttpOnly: name == sessionCookieName,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		})
	}
}
