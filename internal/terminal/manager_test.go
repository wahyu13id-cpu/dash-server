package terminal

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"dashboard/internal/db"
	"dashboard/migrations"
)

func newTestManager(t *testing.T, cfg ManagerConfig) *Manager {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "term.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(ctx, conn, migrations.FS); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	// The manager's store has a foreign key to users, so create one.
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, display_name, role, created_at, updated_at)
		 VALUES (1, 'tester', 'x', 'Tester', 'admin', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	if cfg.Shell == "" {
		cfg.Shell = "/bin/sh"
	}
	if cfg.WorkingDir == "" {
		cfg.WorkingDir = t.TempDir()
	}
	if cfg.Env == nil {
		cfg.Env = []string{"PATH=/usr/bin:/bin", "TERM=xterm"}
	}
	mgr := NewManager(cfg, NewStore(conn), nil)
	t.Cleanup(func() { mgr.Shutdown(context.Background()) })
	return mgr
}

func TestCreateReclaimsIdleDetachedSession(t *testing.T) {
	mgr := newTestManager(t, ManagerConfig{MaxSessions: 1, MaxPerUser: 1, IdleTimeout: time.Minute, ScrollbackBytes: 4096})
	ctx := context.Background()

	first, err := mgr.Create(ctx, 1, "tester", ShellSpec(), 80, 24)
	if err != nil {
		t.Fatalf("first session: %v", err)
	}

	// A session that is still fresh is never sacrificed: the cap is enforced.
	if _, err := mgr.Create(ctx, 1, "tester", ShellSpec(), 80, 24); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("expected ErrTooManySessions, got %v", err)
	}

	// A session nobody is attached to that has been silent for a while — what a
	// page reload leaves behind — is reclaimed instead of locking the user out.
	first.mu.Lock()
	first.lastActivity = time.Now().Add(-2 * reclaimGrace)
	first.mu.Unlock()

	second, err := mgr.Create(ctx, 1, "tester", ShellSpec(), 80, 24)
	if err != nil {
		t.Fatalf("reclaim should have freed a slot: %v", err)
	}
	if second.ID() == first.ID() {
		t.Fatal("expected a fresh session id")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && mgr.Count() != 1 {
		time.Sleep(20 * time.Millisecond)
	}
	if n := mgr.Count(); n != 1 {
		t.Fatalf("expected exactly 1 live session after reclaim, got %d", n)
	}
}

func TestCreateNeverReclaimsAttachedSession(t *testing.T) {
	mgr := newTestManager(t, ManagerConfig{MaxSessions: 1, MaxPerUser: 1, IdleTimeout: time.Minute, ScrollbackBytes: 4096})
	ctx := context.Background()

	first, err := mgr.Create(ctx, 1, "tester", ShellSpec(), 80, 24)
	if err != nil {
		t.Fatalf("first session: %v", err)
	}
	_, _, cancel := first.Subscribe()
	defer cancel()

	first.mu.Lock()
	first.lastActivity = time.Now().Add(-2 * reclaimGrace)
	first.mu.Unlock()

	if _, err := mgr.Create(ctx, 1, "tester", ShellSpec(), 80, 24); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("attached session must not be reclaimed, got %v", err)
	}
	if mgr.Count() != 1 {
		t.Fatalf("attached session was closed, count=%d", mgr.Count())
	}
}

func TestSessionCapPerUser(t *testing.T) {
	mgr := newTestManager(t, ManagerConfig{MaxSessions: 10, MaxPerUser: 2, IdleTimeout: time.Minute, ScrollbackBytes: 4096})
	ctx := context.Background()

	first, err := mgr.Create(ctx, 1, "tester", ShellSpec(), 80, 24)
	if err != nil {
		t.Fatalf("first session: %v", err)
	}
	if _, err := mgr.Create(ctx, 1, "tester", ShellSpec(), 80, 24); err != nil {
		t.Fatalf("second session: %v", err)
	}
	if _, err := mgr.Create(ctx, 1, "tester", ShellSpec(), 80, 24); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("expected ErrTooManySessions, got %v", err)
	}

	// Closing one frees a slot.
	first.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := mgr.Create(ctx, 1, "tester", ShellSpec(), 80, 24); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("slot was not freed after closing a session")
}

func TestSessionLifecycleProducesOutput(t *testing.T) {
	mgr := newTestManager(t, ManagerConfig{MaxSessions: 4, MaxPerUser: 4, IdleTimeout: time.Minute, ScrollbackBytes: 8192})
	ctx := context.Background()

	sess, err := mgr.Create(ctx, 1, "tester", ShellSpec(), 80, 24)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	replay, chunks, cancel := sess.Subscribe()
	defer cancel()
	_ = replay

	if _, err := sess.Write([]byte("echo LIFECYCLE_MARKER\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.After(10 * time.Second)
	var out strings.Builder
	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				t.Fatal("output channel closed before marker was seen")
			}
			out.Write(chunk)
			if strings.Contains(out.String(), "LIFECYCLE_MARKER") {
				goto done
			}
		case <-deadline:
			t.Fatalf("timed out waiting for output; got %q", out.String())
		}
	}

done:
	sess.Close()
	if sess.Status() != StatusExited && sess.Status() != StatusClosed {
		t.Fatalf("unexpected status after close: %s", sess.Status())
	}
	if mgr.Count() != 0 {
		t.Fatalf("session still registered after close: %d", mgr.Count())
	}
}

// TestCloseKillsProcessGroup ensures children spawned by the shell do not
// survive after the session is closed.
func TestCloseKillsProcessGroup(t *testing.T) {
	mgr := newTestManager(t, ManagerConfig{MaxSessions: 4, MaxPerUser: 4, IdleTimeout: time.Minute, ScrollbackBytes: 8192})
	ctx := context.Background()

	sess, err := mgr.Create(ctx, 1, "tester", ShellSpec(), 80, 24)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, chunks, cancel := sess.Subscribe()
	defer cancel()

	// Start a long-lived grandchild and print its PID so we can verify it dies.
	if _, err := sess.Write([]byte("sh -c 'echo CHILD_PID=$$; sleep 600' &\nsleep 600\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	var pid int
	var out strings.Builder
	timeout := time.After(10 * time.Second)
collect:
	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				break collect
			}
			out.Write(chunk)
			// Use the last occurrence: an interactive shell echoes the command
			// text itself, so "CHILD_PID=" appears twice.
			if idx := strings.LastIndex(out.String(), "CHILD_PID="); idx >= 0 {
				rest := out.String()[idx+len("CHILD_PID="):]
				end := strings.IndexAny(rest, "\r\n ")
				if end > 0 {
					if _, err := fmtSscan(rest[:end], &pid); err == nil {
						break collect
					}
				}
			}
		case <-timeout:
			break collect
		}
	}
	if pid == 0 {
		t.Skipf("could not capture child pid from output %q", out.String())
	}

	sess.Close()

	// The child (a member of the session's process group) must be gone shortly
	// after Close.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return // process is gone
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("child process %d survived session close", pid)
}

// TestCreateShellInDirectory checks that a local shell session can be started
// in a specific directory ("Buka di Terminal") instead of the manager default.
func TestCreateShellInDirectory(t *testing.T) {
	mgr := newTestManager(t, ManagerConfig{MaxSessions: 4, MaxPerUser: 4, IdleTimeout: time.Minute, ScrollbackBytes: 8192})
	ctx := context.Background()

	start := t.TempDir()
	want, err := filepath.EvalSymlinks(start)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	sess, err := mgr.Create(ctx, 1, "tester", ShellSpecIn(start), 80, 24)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	_, chunks, cancel := sess.Subscribe()
	defer cancel()

	if _, err := sess.Write([]byte("pwd\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	timeout := time.After(10 * time.Second)
	var out strings.Builder
	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				t.Fatalf("output channel closed; got %q", out.String())
			}
			out.Write(chunk)
			if strings.Contains(out.String(), want) {
				return
			}
		case <-timeout:
			t.Fatalf("shell did not start in %q; output %q", want, out.String())
		}
	}
}

// TestCreateFailsForUnusableDirectory pins the failure mode: an unusable
// directory fails the session instead of silently starting somewhere else.
func TestCreateFailsForUnusableDirectory(t *testing.T) {
	mgr := newTestManager(t, ManagerConfig{MaxSessions: 4, MaxPerUser: 4, IdleTimeout: time.Minute, ScrollbackBytes: 4096})
	ctx := context.Background()

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := mgr.Create(ctx, 1, "tester", ShellSpecIn(missing), 80, 24); err == nil {
		t.Fatal("session started in a directory that does not exist")
	}
	if mgr.Count() != 0 {
		t.Fatalf("failed session stayed registered: %d", mgr.Count())
	}
}

// fmtSscan is a tiny integer parse helper to avoid another import.
func fmtSscan(s string, out *int) (int, error) {
	n := 0
	if s == "" {
		return 0, errors.New("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int(r-'0')
	}
	*out = n
	return 1, nil
}
