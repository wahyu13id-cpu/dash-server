// Package sshsvc is the SSH connection manager. It reads and edits the OpenSSH
// client configuration, lists keys, verifies host keys, and launches sessions
// through the real ssh binary.
//
// Using the real client is a deliberate choice: ProxyJump, IdentityFile, agent
// forwarding, port forwarding, host-key prompts and keyboard-interactive
// authentication all keep working exactly as they do on the command line, and
// nothing is reimplemented. The service only ever passes a validated alias to
// ssh, so no option or argument can be injected from the UI.
package sshsvc

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dashboard/internal/sshaskpass"
	"dashboard/internal/sshconfig"
	"dashboard/internal/sshsecret"
)

// Errors surfaced to the HTTP layer.
var (
	// ErrDisabled means DASH_SSH_ENABLED is false.
	ErrDisabled = errors.New("fitur SSH dimatikan")
	// ErrUnknownHost means no concrete host matches the alias.
	ErrUnknownHost = errors.New("host ssh tidak ditemukan")
	// ErrReadOnly means the host lives in an included file.
	ErrReadOnly = errors.New("host berada di file include (hanya baca)")
	// ErrNoAlias means an SSH session was requested without an alias.
	ErrNoAlias = errors.New("alias ssh wajib diisi")
	// ErrInvalidAlias means the alias is not a plain host name. Because the
	// alias is the only user input that reaches the ssh command line, anything
	// else is refused outright.
	ErrInvalidAlias = errors.New("alias ssh tidak valid")
	// ErrUpstream means the failure came from the remote host or an external
	// ssh tool rather than from this server, so its detail is worth showing to
	// the user (and is not an internal bug).
	ErrUpstream = errors.New("kegagalan dari host atau perkakas ssh")
)

// Config configures the SSH service.
type Config struct {
	// Enabled gates every SSH endpoint and session.
	Enabled bool
	// Home is the user home directory used for ~ expansion and $HOME of the
	// ssh child.
	Home string
	// ConfigPath is the writable main config file.
	ConfigPath string
	// SSHBin, KeyscanBin and KeygenBin are the external tools used.
	SSHBin     string
	KeyscanBin string
	KeygenBin  string
	// AskpassBin is the absolute path of this binary, used as SSH_ASKPASS.
	AskpassBin string
	// Log receives operational logs; secrets and prompts are never logged.
	Log *slog.Logger
}

// Service implements the SSH connection manager.
type Service struct {
	cfg     Config
	secrets *sshsecret.Manager
	broker  *sshaskpass.Broker
	store   *Store
	log     *slog.Logger

	// writeMu serialises config edits so two saves cannot interleave.
	writeMu sync.Mutex

	// statsMu guards stats, the lazily created cache used by HostStats.
	statsMu sync.Mutex
	stats   *statsState
}

// statsInstance returns the remote probe cache, creating it on first use.
func (s *Service) statsInstance() *statsState {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	if s.stats == nil {
		s.stats = newStatsState()
	}
	return s.stats
}

// New builds the service. secrets and broker may be nil, in which case SSH
// sessions simply prompt in the terminal instead of using stored credentials.
func New(cfg Config, conn *sql.DB, secrets *sshsecret.Manager, broker *sshaskpass.Broker) (*Service, error) {
	if cfg.SSHBin == "" {
		cfg.SSHBin = "ssh"
	}
	if cfg.KeyscanBin == "" {
		cfg.KeyscanBin = "ssh-keyscan"
	}
	if cfg.KeygenBin == "" {
		cfg.KeygenBin = "ssh-keygen"
	}
	if cfg.Home == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return nil, errors.New("sshsvc: tidak dapat menentukan home directory")
		}
		cfg.Home = home
	}
	cfg.Home = filepath.Clean(cfg.Home)
	if cfg.ConfigPath == "" {
		cfg.ConfigPath = filepath.Join(cfg.Home, ".ssh", "config")
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Service{
		cfg:     cfg,
		secrets: secrets,
		broker:  broker,
		store:   NewStore(conn),
		log:     cfg.Log,
	}, nil
}

// Enabled reports whether the feature is turned on.
func (s *Service) Enabled() bool { return s.cfg.Enabled }

// Config returns the effective configuration.
func (s *Service) Config() Config { return s.cfg }

// Close releases the askpass broker.
func (s *Service) Close() error {
	if s.broker != nil {
		return s.broker.Close()
	}
	return nil
}

// load parses the current configuration.
func (s *Service) load() (*sshconfig.Config, error) {
	return sshconfig.Load(sshconfig.LoadOptions{Path: s.cfg.ConfigPath, Home: s.cfg.Home})
}

// SSHDir returns the ~/.ssh directory.
func (s *Service) SSHDir() string { return filepath.Join(s.cfg.Home, ".ssh") }

// knownHostsPath is the host-key file the dashboard both inspects and writes.
// Sessions pin it with -o UserKnownHostsFile so ssh cannot end up reading a
// different file: ssh expands "~/.ssh/known_hosts" from the passwd entry of the
// running user, not from $HOME.
func (s *Service) knownHostsPath() string {
	return filepath.Join(s.SSHDir(), "known_hosts")
}

// execEnv is the minimal environment for the external ssh tools.
func (s *Service) execEnv() []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	env := []string{
		"HOME=" + s.cfg.Home,
		"PATH=" + path,
		"TERM=dumb",
		"LANG=C",
	}
	if user := os.Getenv("USER"); user != "" {
		env = append(env, "USER="+user)
	}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		env = append(env, "SSH_AUTH_SOCK="+sock)
	}
	return env
}

// run executes one of the ssh tools with stdin closed, so nothing can block on
// a prompt. Output is captured for classification but never logged verbatim.
func (s *Service) run(ctx context.Context, timeout time.Duration, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = s.execEnv()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// Effective returns the fully resolved configuration for an alias, exactly as
// ssh would compute it. This is authoritative: it includes values coming from
// included files and ssh's own defaults.
func (s *Service) Effective(ctx context.Context, alias string) (*EffectiveResult, error) {
	if !s.cfg.Enabled {
		return nil, ErrDisabled
	}
	if err := validateAlias(alias); err != nil {
		return nil, err
	}
	// The same UserKnownHostsFile pin the session uses is applied here so the
	// viewer shows the file ssh will really consult.
	out, err := s.run(ctx, 8*time.Second, s.cfg.SSHBin,
		"-F", s.cfg.ConfigPath,
		"-o", "UserKnownHostsFile="+s.knownHostsPath(),
		"-G", alias)
	if err != nil && strings.TrimSpace(out) == "" {
		return nil, fmt.Errorf("ssh -G: %w", err)
	}
	return &EffectiveResult{Options: parseG(out), Raw: out}, nil
}

// EffectiveResult is the parsed output of ssh -G.
type EffectiveResult struct {
	Options []sshconfig.Option `json:"options"`
	Raw     string             `json:"raw"`
}

// parseG turns "key value" lines into options, preserving ssh's own order.
func parseG(out string) []sshconfig.Option {
	var opts []sshconfig.Option
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok || key == "" {
			continue
		}
		opts = append(opts, sshconfig.Option{Key: key, Value: value})
	}
	return opts
}

// Launch resolves an alias into everything needed to start an interactive SSH
// session: the argv, the extra environment, and a cleanup callback that revokes
// the credential grant once the session ends.
func (s *Service) Launch(ctx context.Context, alias string) (argv, env []string, cleanup func(), err error) {
	if !s.cfg.Enabled {
		return nil, nil, nil, ErrDisabled
	}
	if err := validateAlias(alias); err != nil {
		return nil, nil, nil, err
	}
	cfg, err := s.load()
	if err != nil {
		return nil, nil, nil, err
	}
	if _, ok := cfg.Lookup(alias); !ok {
		return nil, nil, nil, fmt.Errorf("%w: %s", ErrUnknownHost, alias)
	}

	// known_hosts is pinned explicitly: ssh expands "~/.ssh/known_hosts" from the
	// passwd entry, not from $HOME, so relying on the default would make the
	// dashboard read a different file than the one it manages.
	knownHostsOpt := "UserKnownHostsFile=" + s.knownHostsPath()

	// Stored credentials are only offered once the host key is trusted. On a
	// first connection ssh asks the user to confirm the fingerprint, and that
	// question cannot be answered by the askpass helper; leaving askpass out
	// keeps the session interactive exactly like the command line, so the user
	// types "yes" and then their password once. Afterwards the key is in
	// known_hosts and every later session is fully automatic.
	switch info, statusErr := s.HostKeyStatus(ctx, alias); {
	case statusErr != nil:
		s.log.Warn("ssh: tidak dapat memeriksa kunci host",
			"alias", alias, "error", statusErr)
	case info.Known:
		env = s.askpassEnv(ctx, alias)
	}

	// Connecting through the alias means every ssh_config directive applies.
	argv = []string{
		s.cfg.SSHBin,
		"-F", s.cfg.ConfigPath,
		"-o", knownHostsOpt,
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-o", "TCPKeepAlive=yes",
		alias,
	}
	if err := s.store.Touch(ctx, alias); err != nil {
		s.log.Warn("ssh: gagal mencatat pemakaian host", "alias", alias, "error", err)
	}
	if token := envToken(env); token != "" {
		cleanup = func() { s.broker.Revoke(token) }
	}
	return argv, env, cleanup, nil
}

// askpassEnv returns the environment that lets ssh fetch a stored credential.
// Any failure degrades to "no stored credential", which leaves ssh to prompt in
// the terminal rather than blocking the connection.
func (s *Service) askpassEnv(ctx context.Context, alias string) []string {
	if s.broker == nil || s.secrets == nil || s.cfg.AskpassBin == "" {
		return nil
	}
	if err := s.secrets.VerifyKey(ctx); err != nil {
		s.log.Warn("ssh: kredensial tersimpan tidak dapat dibuka",
			"alias", alias, "error", err)
		return nil
	}
	var password, passphrase string
	if v, ok, err := s.secrets.Get(ctx, alias, sshsecret.KindPassword); err == nil && ok {
		password = v
	}
	if v, ok, err := s.secrets.Get(ctx, alias, sshsecret.KindPassphrase); err == nil && ok {
		passphrase = v
	}
	if password == "" && passphrase == "" {
		return nil
	}
	token, err := s.broker.Grant(alias, password, passphrase)
	if err != nil || token == "" {
		if err != nil {
			s.log.Error("ssh: gagal menyiapkan askpass", "alias", alias, "error", err)
		}
		return nil
	}
	env := sshaskpass.Environment(s.cfg.AskpassBin, s.broker.Socket(), token)
	if env == nil {
		s.broker.Revoke(token)
	}
	return env
}

// envToken extracts the broker token from an environment slice.
func envToken(env []string) string {
	prefix := sshaskpass.EnvToken + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return strings.TrimPrefix(kv, prefix)
		}
	}
	return ""
}

// validateAlias rejects anything that is not a plain host alias, which is the
// only user input that ever reaches the ssh command line.
func validateAlias(alias string) error {
	if alias == "" {
		return ErrNoAlias
	}
	if len(alias) > 128 {
		return fmt.Errorf("%w: terlalu panjang (maks 128)", ErrInvalidAlias)
	}
	for i, r := range alias {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
			if i == 0 {
				return fmt.Errorf("%w: tidak boleh diawali tanda baca", ErrInvalidAlias)
			}
		default:
			return fmt.Errorf("%w: karakter %q tidak diizinkan", ErrInvalidAlias, r)
		}
	}
	return nil
}
