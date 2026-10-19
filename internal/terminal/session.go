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

	// pumpDone is closed by the PTY reader once it has drained the master,
	// reaped once the process exit code is known and done when the teardown
	// finished. Closing a session waits on the first two (see settle) so the
	// last output and the exit code are never lost, and on done so concurrent
	// closers observe a session that is fully released.
	pumpDone chan struct{}
	reaped   chan struct{}
	done     chan struct{}
	// closing marks the goroutine that owns the teardown.
	closing bool

	waitOnce sync.Once
	waitErr  error
}

// ptyDrainGrace bounds the wait for the PTY reader when the process is already
// gone. A read on a master whose child has exited returns within microseconds,
// so this is only a safety net for a file descriptor that a surviving process
// (a background job that inherited the slave) keeps open.
const ptyDrainGrace = 500 * time.Millisecond

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
			break
		}
	}
	// The PTY is drained: every byte the process wrote is in the scrollback
	// now, so the session can be closed without losing output. Close the gate
	// before finish() so a concurrent settle() returns immediately.
	close(s.pumpDone)
	s.finish()
}

// settle waits until the session's terminal state is complete: the exit code of
// the process is known and the reader has handed over everything the process
// wrote before it ended. Closing the session before that would hand a client an
// empty terminal (a refused ssh connection, a one-shot command) or an end of
// stream without an exit code. Both waits are bounded, so a process that
// ignores signals or a descriptor held open by a straggler cannot block a
// close.
func (s *Session) settle() {
	s.mu.Lock()
	started := s.cmd != nil
	pumpDone, reaped := s.pumpDone, s.reaped
	s.mu.Unlock()
	if !started {
		return
	}
	for _, gate := range []chan struct{}{pumpDone, reaped} {
		if gate == nil {
			continue
		}
		timer := time.NewTimer(ptyDrainGrace)
		select {
		case <-gate:
		case <-timer.C:
		}
		timer.Stop()
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
	// The exit code is known: release anyone waiting on it before closing.
	close(s.reaped)
	// Signal subscribers that the process ended.
	s.notifyExit(code)
	s.finish()
}

// finish closes the PTY once and notifies listeners.
func (s *Session) finish() {
	s.mu.Lock()
	if s.closing {
		// Another goroutine (the reader, the reaper or Close) owns the
		// teardown; wait for it to finish so callers never observe a session
		// that is half closed.
		done := s.done
		s.mu.Unlock()
		if done != nil {
			<-done
		}
		return
	}
	s.closing = true
	s.mu.Unlock()

	// Let the exit code and the last output arrive before the subscribers are
	// closed. Note that s.closed stays false until then, so output the reader
	// still has in flight is broadcast rather than dropped.
	s.settle()

	s.mu.Lock()
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
	close(s.done)
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

// Close terminates the process session and releases the PTY. It returns once
// the session has been released, so a caller that closes a session and then
// looks it up again always finds it gone.
func (s *Session) Close() {
	s.mu.Lock()
	if s.closing {
		// The reader or the reaper is already closing this session; killing
		// the process again could hit a recycled PID. Just wait for the
		// teardown to complete.
		done := s.done
		s.mu.Unlock()
		if done != nil {
			<-done
		}
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
