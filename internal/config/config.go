// Package config loads and validates runtime configuration from environment
// variables. Secrets are never hard-coded: the application refuses to start
// when a required secret is missing or too weak.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MinSessionSecretLen is the minimum accepted length for DASH_SESSION_SECRET.
const MinSessionSecretLen = 32

// Config holds every runtime setting for the dashboard server.
type Config struct {
	// Addr is the TCP address the HTTP server listens on, e.g. ":8080".
	Addr string
	// DBPath is the SQLite database file location.
	DBPath string
	// WorkspaceRoot is the top-most directory tree file operations may touch.
	// Set it to "/" for unrestricted filesystem access.
	WorkspaceRoot string
	// HomeDir is the working directory (and HOME) for new PTY shells.
	HomeDir string
	// StaticDir optionally serves the built frontend (SPA) from disk.
	StaticDir string

	// SessionSecret signs/derives internal tokens (CSRF). Required.
	SessionSecret []byte
	// SessionTTL is how long a login session stays valid.
	SessionTTL time.Duration
	// CookieSecure marks auth cookies Secure (mandatory behind HTTPS).
	CookieSecure bool
	// AllowedOrigins is the allow-list used for origin checks (REST + WS).
	AllowedOrigins []string
	// TrustedProxy enables reading X-Forwarded-* headers.
	TrustedProxy bool

	// Shell is the interpreter launched inside the PTY. Never built from
	// user input; user input only flows through the PTY stdin stream.
	Shell string
	// ShellArgs are fixed arguments passed to Shell.
	ShellArgs []string
	// TerminalEnv is the sanitized environment for PTY processes.
	TerminalEnv []string

	// TerminalMaxSessions is the global concurrent PTY cap.
	TerminalMaxSessions int
	// TerminalMaxPerUser is the per-user concurrent PTY cap.
	TerminalMaxPerUser int
	// TerminalIdleTimeout closes PTY sessions with no attached client.
	TerminalIdleTimeout time.Duration
	// TerminalScrollback is the replay buffer retained per session (bytes).
	TerminalScrollback int
	// TerminalHistory persists the shell command history across sessions in
	// $HOME/.bash_history (like a normal login shell). When false, history is
	// discarded (HISTFILE=/dev/null).
	TerminalHistory bool

	// MonitorInterval is how often host metrics (CPU, memory, disks, network,
	// processes) are sampled for the dashboard.
	MonitorInterval time.Duration

	// TOTPIssuer is the label shown next to the account name in authenticator
	// apps (Google Authenticator, Authy, ...).
	TOTPIssuer string

	// SSHEnabled turns the SSH connection manager on or off.
	SSHEnabled bool
	// SSHConfigPath is the writable OpenSSH client config. Empty means
	// $HOME/.ssh/config.
	SSHConfigPath string
	// SSHBin is the ssh executable used for sessions and diagnostics.
	SSHBin string
	// SSHKeyscanBin and SSHKeygenBin are used for host-key and fingerprint
	// operations.
	SSHKeyscanBin string
	SSHKeygenBin  string
	// SSHStats enables the remote half of the terminal status strip: a small
	// read-only probe over ssh that reports CPU, memory, network and disk usage
	// of the host a session is connected to. Metrics for a local shell come from
	// the built-in sampler and are unaffected by this switch.
	SSHStats bool

	// MaxReadSize is the maximum bytes returned by the file content endpoint.
	MaxReadSize int64
	// MaxWriteSize is the maximum bytes accepted by the file write endpoint.
	MaxWriteSize int64
	// MaxListEntries caps directory listings to avoid huge responses.
	MaxListEntries int
	// MaxUploadSize is the maximum bytes accepted by the file upload endpoint.
	// It is also advertised to the UI and must stay in step with the reverse
	// proxy's client_max_body_size, otherwise the proxy rejects the request
	// before this server ever sees it.
	MaxUploadSize int64

	// AuditRetentionDays controls how long audit rows are kept (<=0 disables
	// pruning).
	AuditRetentionDays int

	// Bootstrap user is created on first start when no user exists.
	BootstrapUser     string
	BootstrapPassword string

	// LogLevel controls the slog level (debug, info, warn, error).
	LogLevel string
}

// Load reads configuration from the environment and validates it.
func Load() (*Config, error) {
	// Populate the environment from a local .env file first (without overriding
	// variables already provided by the shell or systemd).
	loadDotEnv()

	c := &Config{
		Addr:                env("DASH_ADDR", ":8080"),
		DBPath:              env("DASH_DB_PATH", "./data/dashboard.db"),
		WorkspaceRoot:       env("DASH_WORKSPACE_ROOT", "./workspace"),
		HomeDir:             env("DASH_HOME_DIR", "/root"),
		StaticDir:           env("DASH_STATIC_DIR", "./frontend/dist"),
		SessionTTL:          envDuration("DASH_SESSION_TTL", 12*time.Hour),
		CookieSecure:        envBool("DASH_COOKIE_SECURE", true),
		AllowedOrigins:      envList("DASH_ALLOWED_ORIGINS", "https://9.wahyu-hidayat.my.id"),
		TrustedProxy:        envBool("DASH_TRUSTED_PROXY", true),
		Shell:               env("DASH_SHELL", "/bin/bash"),
		TerminalMaxSessions: envInt("DASH_TERMINAL_MAX_SESSIONS", 8),
		TerminalMaxPerUser:  envInt("DASH_TERMINAL_MAX_PER_USER", 8),
		TerminalIdleTimeout: envDuration("DASH_TERMINAL_IDLE_TIMEOUT", 30*time.Minute),
		TerminalScrollback:  envInt("DASH_TERMINAL_SCROLLBACK_BYTES", 64*1024),
		TerminalHistory:     envBool("DASH_TERMINAL_HISTORY", true),
		MonitorInterval:     envDuration("DASH_MONITOR_INTERVAL", 2*time.Second),
		TOTPIssuer:          env("DASH_TOTP_ISSUER", "Server Dashboard"),
		SSHEnabled:          envBool("DASH_SSH_ENABLED", true),
		SSHConfigPath:       env("DASH_SSH_CONFIG", ""),
		SSHBin:              env("DASH_SSH_BIN", "ssh"),
		SSHKeyscanBin:       env("DASH_SSH_KEYSCAN_BIN", "ssh-keyscan"),
		SSHKeygenBin:        env("DASH_SSH_KEYGEN_BIN", "ssh-keygen"),
		SSHStats:            envBool("DASH_SSH_STATS", true),
		MaxReadSize:         envBytes("DASH_MAX_READ_SIZE", 2<<20),
		MaxWriteSize:        envBytes("DASH_MAX_WRITE_SIZE", 5<<20),
		MaxListEntries:      envInt("DASH_MAX_LIST_ENTRIES", 5000),
		MaxUploadSize:       envBytes("DASH_MAX_UPLOAD_SIZE", 512<<20),
		AuditRetentionDays:  envInt("DASH_AUDIT_RETENTION_DAYS", 30),
		BootstrapUser:       env("DASH_BOOTSTRAP_USER", "admin"),
		BootstrapPassword:   os.Getenv("DASH_BOOTSTRAP_PASSWORD"),
		LogLevel:            env("DASH_LOG_LEVEL", "info"),
	}

	// Session secret: required, or generated once for local dev with a loud
	// warning. Generated secrets invalidate sessions on restart, so they are
	// only acceptable outside production.
	if raw := os.Getenv("DASH_SESSION_SECRET"); raw != "" {
		if len(raw) < MinSessionSecretLen {
			return nil, fmt.Errorf("DASH_SESSION_SECRET must be at least %d characters", MinSessionSecretLen)
		}
		c.SessionSecret = []byte(raw)
	} else {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, fmt.Errorf("generate ephemeral session secret: %w", err)
		}
		c.SessionSecret = []byte(hex.EncodeToString(secret))
		slog.Warn("DASH_SESSION_SECRET is not set; generated an ephemeral secret. " +
			"Existing sessions will be invalidated on restart. Set it for production.")
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	var errs []error

	if c.Addr == "" {
		errs = append(errs, errors.New("DASH_ADDR must not be empty"))
	}
	if c.SessionTTL <= 0 {
		errs = append(errs, errors.New("DASH_SESSION_TTL must be positive"))
	}
	if c.TerminalMaxSessions < 1 {
		errs = append(errs, errors.New("DASH_TERMINAL_MAX_SESSIONS must be >= 1"))
	}
	if c.TerminalMaxPerUser < 1 {
		errs = append(errs, errors.New("DASH_TERMINAL_MAX_PER_USER must be >= 1"))
	}
	if c.MonitorInterval <= 0 {
		errs = append(errs, errors.New("DASH_MONITOR_INTERVAL must be positive"))
	}
	if c.MaxReadSize < 1 {
		errs = append(errs, errors.New("DASH_MAX_READ_SIZE must be >= 1"))
	}
	if c.MaxWriteSize < 1 {
		errs = append(errs, errors.New("DASH_MAX_WRITE_SIZE must be >= 1"))
	}
	if c.MaxListEntries < 1 {
		errs = append(errs, errors.New("DASH_MAX_LIST_ENTRIES must be >= 1"))
	}
	if c.MaxUploadSize < 1 {
		errs = append(errs, errors.New("DASH_MAX_UPLOAD_SIZE must be >= 1"))
	}
	if len(c.AllowedOrigins) == 0 {
		errs = append(errs, errors.New("DASH_ALLOWED_ORIGINS must contain at least one origin"))
	}
	if _, err := os.Stat(c.Shell); err != nil {
		errs = append(errs, fmt.Errorf("DASH_SHELL %q is not executable: %w", c.Shell, err))
	}
	if c.BootstrapUser == "" {
		errs = append(errs, errors.New("DASH_BOOTSTRAP_USER must not be empty"))
	}
	if c.BootstrapPassword != "" && len(c.BootstrapPassword) < 8 {
		errs = append(errs, errors.New("DASH_BOOTSTRAP_PASSWORD must be at least 8 characters"))
	}

	// Resolve and create the workspace root so downstream code can assume it
	// exists and is a directory.
	abs, err := filepath.Abs(c.WorkspaceRoot)
	if err != nil {
		errs = append(errs, fmt.Errorf("resolve workspace root: %w", err))
	} else {
		if err := os.MkdirAll(abs, 0o750); err != nil {
			errs = append(errs, fmt.Errorf("create workspace root %q: %w", abs, err))
		} else {
			c.WorkspaceRoot = abs
		}
	}

	if err := os.MkdirAll(filepath.Dir(c.DBPath), 0o750); err != nil {
		errs = append(errs, fmt.Errorf("create database directory: %w", err))
	}

	// Resolve and create the terminal home/working directory.
	if abs, err := filepath.Abs(c.HomeDir); err != nil {
		errs = append(errs, fmt.Errorf("resolve home dir: %w", err))
	} else if err := os.MkdirAll(abs, 0o700); err != nil {
		errs = append(errs, fmt.Errorf("create home dir %q: %w", abs, err))
	} else {
		c.HomeDir = abs
	}

	return errors.Join(errs...)
}

// TerminalEnvironment builds a sanitized environment for PTY processes. It
// deliberately starts from a minimal set instead of os.Environ() so that
// secrets from the server process do not leak into interactive shells.
//
// Command history is persisted in $HOME/.bash_history by default so the
// terminal behaves like a normal login shell (Up arrow recalls previous
// commands across sessions). Because sessions are closed with SIGKILL, bash
// would not get to flush its history on exit; PROMPT_COMMAND runs
// `history -a` after every command so entries are appended immediately, and
// histappend stops a clean exit from truncating older entries. Set
// DASH_TERMINAL_HISTORY=false to keep history in memory only (HISTFILE=/dev/null).
func (c *Config) TerminalEnvironment() []string {
	histFile := "/dev/null"
	histSize := "0"
	histExtra := []string{}
	if c.TerminalHistory {
		histFile = filepath.Join(c.HomeDir, ".bash_history")
		histSize = "10000"
		histExtra = []string{
			"HISTFILESIZE=20000",
			"HISTCONTROL=ignoreboth",
			"BASHOPTS=histappend",
			"PROMPT_COMMAND=history -a",
		}
	}
	env := []string{
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"HOME=" + c.HomeDir,
		"PWD=" + c.HomeDir,
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"HISTFILE=" + histFile,
		"HISTSIZE=" + histSize,
		"PS1=\\u@\\h:\\w\\$ ",
	}
	env = append(env, histExtra...)
	if p := os.Getenv("PATH"); p != "" {
		env = append(env, "PATH="+p)
	} else {
		env = append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	if h := os.Getenv("HOSTNAME"); h != "" {
		env = append(env, "HOSTNAME="+h)
	}
	return env
}

// ResolveDBPath reports the configured database path for administrative
// subcommands. It reads .env / DASH_ENV_FILE like Load does but skips
// validation and does not create any directories.
func ResolveDBPath() string {
	loadDotEnv()
	return env("DASH_DB_PATH", "./data/dashboard.db")
}

// --- env helpers -----------------------------------------------------------

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envBytes(key string, def int64) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	// Accept plain bytes or human suffixes: 2M, 512K, 1G.
	mult := int64(1)
	upper := strings.ToUpper(v)
	switch {
	case strings.HasSuffix(upper, "K"):
		mult, v = 1<<10, v[:len(v)-1]
	case strings.HasSuffix(upper, "M"):
		mult, v = 1<<20, v[:len(v)-1]
	case strings.HasSuffix(upper, "G"):
		mult, v = 1<<30, v[:len(v)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return def
	}
	return n * mult
}

func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func envList(key, def string) []string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		v = def
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, strings.TrimRight(p, "/"))
		}
	}
	return out
}

// --- .env loading ----------------------------------------------------------

// loadDotEnv loads KEY=VALUE pairs from a .env file into the process
// environment, without overriding variables that are already set. The file is
// taken from DASH_ENV_FILE, or "./.env" when present. This makes `make run` and
// direct `./bin/dashboard` invocations pick up a local .env, while systemd
// (which injects real environment variables) always takes precedence.
func loadDotEnv() {
	path := strings.TrimSpace(os.Getenv("DASH_ENV_FILE"))
	if path == "" {
		path = ".env"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return // a missing .env is not an error
	}
	for key, val := range parseEnvFile(data) {
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
}

// parseEnvFile parses a minimal .env format: blank lines and lines starting
// with '#' are ignored, an optional leading "export " is stripped, keys are
// trimmed, and a single layer of matching surrounding quotes is removed.
func parseEnvFile(data []byte) map[string]string {
	out := map[string]string{}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, " \t") {
			continue
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		out[key] = val
	}
	return out
}

