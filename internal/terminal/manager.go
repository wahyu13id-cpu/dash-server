// Package terminal manages PTY-backed shell sessions and their lifecycle.
//
// Security notes:
//   - No shell command is ever assembled from user input. The only process
//     started is the configured shell (e.g. /bin/bash), and user input reaches
//     it exclusively through the PTY's standard input stream.
//   - Sessions are owned by a user and every lookup is scoped to that owner.
//   - Global and per-user concurrency caps plus an idle timeout bound resource
//     usage, and closing a session kills the entire process group.
package terminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"dashboard/internal/audit"
	"dashboard/internal/db"
)

// ManagerConfig holds the resource and process settings for new sessions.
type ManagerConfig struct {
	Shell           string
	Args            []string
	WorkingDir      string
	Env             []string
	MaxSessions     int
	MaxPerUser      int
	IdleTimeout     time.Duration
	ScrollbackBytes int
	// SSHCommand resolves an SSH alias into the argv and environment to run. It
	// is injected by the caller so this package carries no SSH policy of its
	// own; a nil function rejects every SSH session.
	SSHCommand func(alias string) (SSHLaunch, error)
}

// SSHLaunch describes how to start an SSH session.
type SSHLaunch struct {
	// Argv is the complete command line, for example
	// ["/usr/bin/ssh", "-o", "ServerAliveInterval=30", "web1"].
	Argv []string
	// Env is appended after the base environment.
	Env []string
	// Cleanup runs once when the session ends, to revoke a credential grant.
	Cleanup func()
}

// Session kinds.
const (
	// KindShell is a local PTY running the configured shell.
	KindShell = "shell"
	// KindSSH is a PTY running the ssh client against a host alias.
	KindSSH = "ssh"
)

// reclaimGrace is how long a session with no attached client must have been
// silent before the manager is willing to close it to make room for a new one.
// A session that is doing work (a build, a transfer, a running command) keeps
// refreshing its activity stamp and is therefore never reclaimed while it
// works; a shell sitting at a prompt is reclaimed as soon as the cap is hit.
const reclaimGrace = 2 * time.Minute

// capReason names the concurrency cap that is currently reached.
type capReason string

const (
	capNone    capReason = ""
	capGlobal  capReason = "global"
	capPerUser capReason = "per-user"
)

// LaunchSpec selects what a session runs.
type LaunchSpec struct {
	// Kind is KindShell or KindSSH.
	Kind string
	// Target is the SSH alias, empty for a local shell.
	Target string
	// Dir is the directory a local shell starts in. It is ignored for SSH
	// sessions, where the remote host decides the initial directory. Empty means
	// the manager's configured WorkingDir.
	Dir string
}

// ShellSpec returns the spec for a local shell session.
func ShellSpec() LaunchSpec { return LaunchSpec{Kind: KindShell} }

// ShellSpecIn returns the spec for a local shell that starts in dir. The caller
// is responsible for validating dir (see the HTTP layer); an unusable directory
// makes the session fail to start rather than silently falling back.
func ShellSpecIn(dir string) LaunchSpec { return LaunchSpec{Kind: KindShell, Dir: dir} }

// SSHSpec returns the spec for an SSH session against alias.
func SSHSpec(alias string) LaunchSpec { return LaunchSpec{Kind: KindSSH, Target: alias} }

// Normalize fills in a default kind.
func (l LaunchSpec) Normalize() LaunchSpec {
	if l.Kind == "" {
		l.Kind = KindShell
	}
	return l
}

// Title renders the label shown in the session list.
func (l LaunchSpec) Title() string {
	if l.Kind == KindSSH {
		return "ssh " + l.Target
	}
	return "shell"
}

// Manager owns the set of live PTY sessions.
type Manager struct {
	cfg   ManagerConfig
	store *Store
	audit *audit.Logger

	mu       sync.Mutex
	sessions map[string]*Session

	stopOnce sync.Once
	stop     chan struct{}
	wg       sync.WaitGroup
}

// NewManager creates a Manager and starts its janitor goroutine.
func NewManager(cfg ManagerConfig, store *Store, auditLog *audit.Logger) *Manager {
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = 8
	}
	if cfg.MaxPerUser <= 0 {
		cfg.MaxPerUser = 4
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 30 * time.Minute
	}
	if cfg.ScrollbackBytes <= 0 {
		cfg.ScrollbackBytes = 64 * 1024
	}
	m := &Manager{
		cfg:      cfg,
		store:    store,
		audit:    auditLog,
		sessions: make(map[string]*Session),
		stop:     make(chan struct{}),
	}
	m.wg.Add(1)
	go m.janitor()
	return m
}

// Config exposes the effective manager configuration.
func (m *Manager) Config() ManagerConfig { return m.cfg }

// Store exposes the metadata store (read-only usage by the HTTP layer).
func (m *Manager) Store() *Store { return m.store }

// Create starts a new PTY session owned by userID.
func (m *Manager) Create(ctx context.Context, userID int64, username string, spec LaunchSpec, cols, rows int) (*Session, error) {
	spec = spec.Normalize()
	if spec.Kind == KindSSH {
		if m.cfg.SSHCommand == nil {
			return nil, errors.New("terminal: sesi ssh tidak dikonfigurasi")
		}
		if spec.Target == "" {
			return nil, errors.New("terminal: alias ssh kosong")
		}
	}
	if cols <= 0 || cols > 1000 {
		cols = 80
	}
	if rows <= 0 || rows > 1000 {
		rows = 24
	}

	// Reserve a slot under the same lock that enforces the caps. A full cap is
	// first relieved by closing a session nobody is attached to (see
	// reclaimableLocked) so that sessions left behind by a page reload can never
	// lock the dashboard out; only when every remaining session is genuinely in
	// use does the request fail.
	m.mu.Lock()
	for {
		reason := m.capLocked(userID)
		if reason == capNone {
			break
		}
		victim := m.reclaimableLocked(userID, reason == capPerUser)
		if victim == nil {
			m.mu.Unlock()
			limit := m.cfg.MaxSessions
			if reason == capPerUser {
				limit = m.cfg.MaxPerUser
			}
			m.audit.Log(ctx, audit.Entry{
				UserID: userID, Username: username, Action: audit.ActionTerminalStart,
				Detail: fmt.Sprintf("%s session cap reached", reason), Success: false,
			})
			return nil, fmt.Errorf("%w (%s limit %d)", ErrTooManySessions, reason, limit)
		}
		id, owner := victim.ID(), victim.UserID()
		m.mu.Unlock()

		slog.Info("reclaiming idle terminal session to free a slot",
			"id", id, "owner", owner, "requester", userID)
		m.audit.Log(ctx, audit.Entry{
			UserID: owner, Action: audit.ActionTerminalStop,
			Target: id, Detail: "reclaimed: session cap reached", Success: true,
		})
		victim.Close()
		m.mu.Lock()
	}

	id, err := newSessionID()
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	sess := &Session{
		id:              id,
		userID:          userID,
		title:           spec.Title(),
		shell:           m.displayShell(spec),
		kind:            spec.Kind,
		target:          spec.Target,
		cols:            uint16(cols),
		rows:            uint16(rows),
		createdAt:       time.Now(),
		status:          "starting",
		scrollbackLimit: m.cfg.ScrollbackBytes,
		subscribers:     make(map[int64]chan []byte),
		pumpDone:        make(chan struct{}),
		reaped:          make(chan struct{}),
		done:            make(chan struct{}),
	}
	sess.onCloseFunc = func() { m.handleSessionClosed(sess) }
	m.sessions[id] = sess
	m.mu.Unlock()

	meta := Metadata{
		ID:        id,
		UserID:    userID,
		Title:     spec.Title(),
		Shell:     m.displayShell(spec),
		Kind:      spec.Kind,
		Target:    spec.Target,
		Status:    StatusRunning,
		Cols:      cols,
		Rows:      rows,
		CreatedAt: db.NormalizeTime(sess.createdAt),
		StartedAt: db.NormalizeTime(time.Now()),
	}
	if err := m.store.Insert(ctx, meta); err != nil {
		m.remove(id)
		return nil, fmt.Errorf("persist terminal session: %w", err)
	}

	if err := sess.start(m.cfg, spec); err != nil {
		_ = m.store.MarkEnded(ctx, id, StatusClosed, nil)
		m.remove(id)
		return nil, fmt.Errorf("start pty: %w", err)
	}

	m.audit.Log(ctx, audit.Entry{
		UserID: userID, Username: username, Action: audit.ActionTerminalStart,
		Target: id, Detail: meta.Shell, Success: true,
	})
	return sess, nil
}

// displayShell renders the program shown for a session in metadata.
func (m *Manager) displayShell(spec LaunchSpec) string {
	if spec.Kind == KindSSH {
		return "ssh " + spec.Target
	}
	return m.cfg.Shell
}

// Get returns a session by ID if it belongs to userID (or if owner is true).
func (m *Manager) Get(id string, userID int64, owner bool) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrNoSession
	}
	if !owner && s.userID != userID {
		return nil, ErrNoSession
	}
	return s, nil
}

// Delete closes a session by ID (scoped to the owner unless owner is true).
func (m *Manager) Delete(ctx context.Context, id string, userID int64, owner bool, username string) error {
	s, err := m.Get(id, userID, owner)
	if err != nil {
		return err
	}
	s.Close()
	m.audit.Log(ctx, audit.Entry{
		UserID: s.userID, Username: username, Action: audit.ActionTerminalStop,
		Target: id, Success: true,
	})
	return nil
}

// List returns live sessions owned by userID.
func (m *Manager) List(userID int64) []Metadata {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Metadata, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.userID != userID {
			continue
		}
		out = append(out, s.Metadata())
	}
	return out
}

// Count returns the number of live sessions.
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// Shutdown closes every session and stops the janitor.
func (m *Manager) Shutdown(ctx context.Context) {
	m.stopOnce.Do(func() { close(m.stop) })
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		s.Close()
		_ = m.store.MarkEnded(ctx, s.ID(), StatusClosed, s.ExitCode())
	}
	m.wg.Wait()
}

// MarkStaleSessions flips sessions left "running" by a previous process to
// "closed" so the metadata table reflects reality after a restart.
func (m *Manager) MarkStaleSessions(ctx context.Context) {
	if n, err := m.store.MarkStaleClosed(ctx); err != nil {
		slog.Error("mark stale terminal sessions failed", "error", err)
	} else if n > 0 {
		slog.Info("closed stale terminal sessions from previous run", "count", n)
	}
}

func (m *Manager) handleSessionClosed(s *Session) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.store.MarkEnded(ctx, s.ID(), s.Status(), s.ExitCode()); err != nil {
		slog.Error("persist terminal session end failed", "id", s.ID(), "error", err)
	}
	m.remove(s.ID())
}

func (m *Manager) remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
}

// capLocked reports which concurrency cap is currently reached by userID. The
// caller must hold m.mu.
func (m *Manager) capLocked(userID int64) capReason {
	if len(m.sessions) >= m.cfg.MaxSessions {
		return capGlobal
	}
	mine := 0
	for _, s := range m.sessions {
		if s.userID == userID {
			mine++
		}
	}
	if mine >= m.cfg.MaxPerUser {
		return capPerUser
	}
	return capNone
}

// reclaimableLocked returns the least recently active session that has no
// attached client and has been silent for at least reclaimGrace, or nil when
// there is nothing safe to close. The caller must hold m.mu; the returned
// session must be closed after releasing it.
//
// With ownOnly the search is limited to sessions owned by userID, which is what
// frees a per-user slot; otherwise the caller's own sessions are preferred and
// any other idle session is used as a fallback.
func (m *Manager) reclaimableLocked(userID int64, ownOnly bool) *Session {
	now := time.Now()
	var own, other *Session
	for _, s := range m.sessions {
		if s.Connections() > 0 {
			continue
		}
		if now.Sub(s.LastActivity()) < reclaimGrace {
			continue
		}
		if s.userID == userID {
			if own == nil || s.LastActivity().Before(own.LastActivity()) {
				own = s
			}
			continue
		}
		if other == nil || s.LastActivity().Before(other.LastActivity()) {
			other = s
		}
	}
	if own != nil || ownOnly {
		return own
	}
	return other
}

// janitor enforces the idle timeout for sessions with no attached client.
func (m *Manager) janitor() {
	defer m.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			m.evictIdle()
		}
	}
}

func (m *Manager) evictIdle() {
	m.mu.Lock()
	var stale []*Session
	for _, s := range m.sessions {
		s.mu.Lock()
		closed := s.closed
		conns := s.connections
		last := s.lastActivity
		s.mu.Unlock()
		if closed {
			continue
		}
		if conns == 0 && time.Since(last) > m.cfg.IdleTimeout {
			stale = append(stale, s)
		}
	}
	m.mu.Unlock()

	for _, s := range stale {
		slog.Info("closing idle terminal session", "id", s.ID())
		s.Close()
	}
}

func newSessionID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
