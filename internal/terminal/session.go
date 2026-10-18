package terminal

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
)

// Session is a live PTY-backed shell. PTY output is never logged; only a
// bounded in-memory scrollback is kept so a reconnecting client can repaint.
// (The shell itself may persist its command history to $HOME/.bash_history —
// see config.TerminalHistory.)
type Session struct {
	id     string
	userID int64
	title  string
	shell  string
	// kind is "shell" or "ssh" and target is the SSH alias for kind "ssh".
	kind   string
	target string

	ptmx *os.File
	cmd  *exec.Cmd

	scrollbackLimit int

	mu           sync.Mutex
	cols         uint16
	rows         uint16
	createdAt    time.Time
	lastActivity time.Time
	status       string
	exitCode     *int
	closed       bool
	scrollback   []byte
	subscribers  map[int64]chan []byte
	nextSubID    int64
	connections  int
	lastAttached time.Time
	onCloseFunc  func()
	// cleanup releases per-session resources such as a credential grant; it
	// runs exactly once when the session ends.
	cleanup   func()
	closeOnce sync.Once

	waitOnce sync.Once
	waitErr  error
}

// ID returns the session identifier.
func (s *Session) ID() string { return s.id }

// UserID returns the owning user.
func (s *Session) UserID() int64 { return s.userID }

// Status returns the lifecycle status.
func (s *Session) Status() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// ExitCode returns the process exit code once known.
func (s *Session) ExitCode() *int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitCode
}

// Metadata returns a snapshot of the session's descriptive fields.
func (s *Session) Metadata() Metadata {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := Metadata{
		ID:        s.id,
		UserID:    s.userID,
		Title:     s.title,
		Shell:     s.shell,
		Kind:      s.kind,
		Target:    s.target,
		Status:    s.status,
		Cols:      int(s.cols),
		Rows:      int(s.rows),
		CreatedAt: s.createdAt.UTC().Format(time.RFC3339),
	}
	if s.exitCode != nil {
		c := *s.exitCode
		m.ExitCode = &c
	}
	return m
}

// start launches the PTY and the reader pump. For an SSH session the command
// line comes from cfg.SSHCommand, which is the only place a program other than
// the configured shell is started; it is always an argv slice and never a shell
// string, so a host alias can never be interpreted as code.
func (s *Session) start(cfg ManagerConfig, spec LaunchSpec) error {
	argv := []string{s.shell}
	env := cfg.Env
	if spec.Kind == KindSSH {
		if cfg.SSHCommand == nil {
			return errors.New("terminal: sesi ssh tidak dikonfigurasi")
		}
		launch, err := cfg.SSHCommand(spec.Target)
		if err != nil {
			return err
		}
		if len(launch.Argv) == 0 {
			return errors.New("terminal: argv ssh kosong")
		}
		argv = launch.Argv
		if len(launch.Env) > 0 {
			env = append(append([]string{}, cfg.Env...), launch.Env...)
		}
		s.cleanup = launch.Cleanup
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cfg.WorkingDir
	// A local shell can be asked to start somewhere specific ("Buka di
	// Terminal"). SSH sessions keep the configured directory: the remote side
	// owns their initial path.
	if spec.Kind == KindShell && spec.Dir != "" {
		cmd.Dir = spec.Dir
	}
	cmd.Env = env

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: s.cols, Rows: s.rows})
	if err != nil {
		s.runCleanup()
		return err
	}
	s.ptmx = ptmx
	s.cmd = cmd

	s.mu.Lock()
	s.status = StatusRunning
	s.lastActivity = time.Now()
	s.lastAttached = time.Now()
	s.mu.Unlock()

	go s.pump()
	go s.reap()
	return nil
}

// pump reads PTY output and fans it out to subscribers and the scrollback.
func (s *Session) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			s.broadcast(chunk)
		}
		if err != nil {
			s.finish()
			return
		}
	}
}

// reap waits for the child process and records the exit code.
func (s *Session) reap() {
	err := s.cmd.Wait()

	var code int
	if s.cmd.ProcessState != nil {
		code = s.cmd.ProcessState.ExitCode()
	}
	s.mu.Lock()
	s.exitCode = &code
	s.status = StatusExited
	s.mu.Unlock()

	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			s.waitErr = err
		}
	}
	s.waitOnce.Do(func() {})
	// Signal subscribers that the process ended.
	s.notifyExit(code)
	s.finish()
}

// finish closes the PTY once and notifies listeners.
func (s *Session) finish() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	subs := s.subscribers
	s.subscribers = map[int64]chan []byte{}
	if s.status == StatusRunning {
		s.status = StatusExited
	}
	onClose := s.onCloseFunc
	s.mu.Unlock()

	if s.ptmx != nil {
		_ = s.ptmx.Close()
	}
	s.runCleanup()
	for _, ch := range subs {
		close(ch)
	}
	if onClose != nil {
		onClose()
	}
}

// runCleanup releases per-session resources exactly once.
func (s *Session) runCleanup() {
	s.closeOnce.Do(func() {
		if s.cleanup != nil {
			s.cleanup()
		}
	})
}

func (s *Session) notifyExit(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Nothing to do here beyond state; subscribers observe EOF on their
	// channel. Exit code is exposed via ExitCode().
	_ = code
}

// broadcast appends to the bounded scrollback and pushes to all subscribers.
func (s *Session) broadcast(chunk []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.lastActivity = time.Now()
	s.scrollback = append(s.scrollback, chunk...)
	if len(s.scrollback) > s.scrollbackLimit {
		s.scrollback = s.scrollback[len(s.scrollback)-s.scrollbackLimit:]
	}
	for _, ch := range s.subscribers {
		select {
		case ch <- chunk:
		default:
			// Slow consumer: drop this chunk rather than blocking the PTY.
		}
	}
}

// Subscribe registers a new output consumer and returns a replay snapshot plus
// a channel of subsequent chunks. Call the returned cancel func to detach.
func (s *Session) Subscribe() (replay []byte, ch chan []byte, cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()

	replay = append([]byte(nil), s.scrollback...)
	id := s.nextSubID
	s.nextSubID++
	c := make(chan []byte, 256)
	if s.closed {
		close(c)
		return replay, c, func() {}
	}
	s.subscribers[id] = c
	s.connections++
	s.lastAttached = time.Now()

	var once sync.Once
	cancel = func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if existing, ok := s.subscribers[id]; ok {
				delete(s.subscribers, id)
				close(existing)
			}
			s.connections--
			s.lastAttached = time.Now()
		})
	}
	return replay, c, cancel
}

// Write forwards raw bytes to the PTY stdin. It never constructs a shell
// command; bytes go straight to the running process.
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	closed := s.closed
	s.lastActivity = time.Now()
	s.mu.Unlock()
	if closed {
		return 0, io.ErrClosedPipe
	}
	return s.ptmx.Write(p)
}

// Resize changes the PTY window size.
func (s *Session) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return nil
	}
	s.mu.Lock()
	s.cols, s.rows = cols, rows
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return io.ErrClosedPipe
	}
	return pty.Setsize(s.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

// Connections returns the number of attached subscribers.
func (s *Session) Connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connections
}

// LastActivity returns the timestamp of the last PTY or client activity.
func (s *Session) LastActivity() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastActivity
}

// Close terminates the process session and releases the PTY.
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	pid := 0
	if s.cmd != nil && s.cmd.Process != nil {
		pid = s.cmd.Process.Pid
	}
	s.mu.Unlock()

	if pid > 0 {
		// Kill every process in the PTY session (see killSession) so that
		// background jobs spawned inside the shell do not outlive it.
		killSession(pid)
	}
	s.finish()
}
