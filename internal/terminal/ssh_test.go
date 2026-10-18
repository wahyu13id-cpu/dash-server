package terminal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// sshFixture writes a tiny script that reports the argv and the environment it
// was started with, then exits with a fixed code. It stands in for the real ssh
// binary so the test can assert exactly how an SSH session is launched without
// touching the network. The fixture itself is a script, but the production path
// always receives an argv slice (see Session.start) and never a shell string.
func sshFixture(t *testing.T, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-ssh")
	script := "#!/bin/sh\n" +
		"printf 'ARG1=%s\\n' \"$1\"\n" +
		"printf 'ARGCOUNT=%s\\n' \"$#\"\n" +
		"printf 'ASKPASS=%s\\n' \"${SSH_ASKPASS-<unset>}\"\n" +
		"printf 'REQUIRE=%s\\n' \"${SSH_ASKPASS_REQUIRE-<unset>}\"\n" +
		"printf 'CUSTOM=%s\\n' \"${DASH_TEST_CUSTOM-<unset>}\"\n" +
		"printf 'TERM=%s\\n' \"${TERM-<unset>}\"\n" +
		"exit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestSSHSessionLaunchPlumbing checks the whole SSH launch path: the alias is
// passed as a bare argument (never interpolated into a shell string), the
// credential environment is appended to the base environment, the session
// metadata records kind/target, and the credential cleanup hook runs when the
// session ends.
func TestSSHSessionLaunchPlumbing(t *testing.T) {
	fixture := sshFixture(t, 3)

	var gotAlias atomic.Value
	var cleaned atomic.Int32

	mgr := newTestManager(t, ManagerConfig{
		MaxSessions:     4,
		MaxPerUser:      4,
		IdleTimeout:     time.Minute,
		ScrollbackBytes: 8192,
		SSHCommand: func(alias string) (SSHLaunch, error) {
			gotAlias.Store(alias)
			return SSHLaunch{
				Argv: []string{fixture, alias},
				Env: []string{
					"SSH_ASKPASS=/run/dash/ssh-askpass.sock",
					"SSH_ASKPASS_REQUIRE=force",
					"DASH_TEST_CUSTOM=value",
				},
				Cleanup: func() { cleaned.Add(1) },
			}, nil
		},
	})

	sess, err := mgr.Create(context.Background(), 1, "tester", SSHSpec("web1"), 100, 30)
	if err != nil {
		t.Fatalf("create ssh session: %v", err)
	}
	if alias, _ := gotAlias.Load().(string); alias != "web1" {
		t.Fatalf("SSHCommand got alias %q, want %q", alias, "web1")
	}

	meta := sess.Metadata()
	if meta.Kind != KindSSH || meta.Target != "web1" {
		t.Fatalf("metadata kind/target = %q/%q, want ssh/web1", meta.Kind, meta.Target)
	}
	if meta.Title != "ssh web1" {
		t.Fatalf("title = %q, want %q", meta.Title, "ssh web1")
	}

	replay, chunks, cancel := sess.Subscribe()
	defer cancel()

	var out strings.Builder
	out.Write(replay)
	deadline := time.After(10 * time.Second)
	exited := false
loop:
	for !exited {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				exited = true
				break loop
			}
			out.Write(chunk)
		case <-deadline:
			t.Fatalf("timed out waiting for the fixture to exit; got %q", out.String())
		}
	}

	text := out.String()
	for _, want := range []string{
		"ARG1=web1",
		"ARGCOUNT=1", // the alias is the only argument; nothing is injected
		"ASKPASS=/run/dash/ssh-askpass.sock",
		"REQUIRE=force",
		"CUSTOM=value", // launch env is appended after the base env
		"TERM=xterm",   // base env survives
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("fixture output missing %q; got:\n%s", want, text)
		}
	}

	// The cleanup hook is the only thing that revokes the credential grant, so
	// it must run exactly once when the session ends.
	if !waitFor(func() bool { return cleaned.Load() == 1 }, 5*time.Second) {
		t.Fatalf("cleanup ran %d times, want 1", cleaned.Load())
	}
	if code := sess.ExitCode(); code == nil || *code != 3 {
		t.Fatalf("exit code = %v, want 3", code)
	}
	if status := sess.Status(); status != StatusExited && status != StatusClosed {
		t.Fatalf("status = %q, want exited/closed", status)
	}
}

// TestSSHSessionRejectsBadSpecs covers the guards that keep an SSH session from
// starting without a resolver (feature disabled) or without an alias.
func TestSSHSessionRejectsBadSpecs(t *testing.T) {
	ctx := context.Background()

	// No SSHCommand configured: SSH must be refused, local shells unaffected.
	mgr := newTestManager(t, ManagerConfig{MaxSessions: 4, MaxPerUser: 4, IdleTimeout: time.Minute})
	if _, err := mgr.Create(ctx, 1, "tester", SSHSpec("web1"), 80, 24); err == nil {
		t.Fatal("expected an error when SSH is not configured")
	}

	fixture := sshFixture(t, 0)
	withSSH := newTestManager(t, ManagerConfig{
		MaxSessions: 4,
		MaxPerUser:  4,
		IdleTimeout: time.Minute,
		SSHCommand: func(alias string) (SSHLaunch, error) {
			return SSHLaunch{Argv: []string{fixture, alias}}, nil
		},
	})
	if _, err := withSSH.Create(ctx, 1, "tester", SSHSpec(""), 80, 24); err == nil {
		t.Fatal("expected an error for an empty ssh alias")
	}

	// A resolver that refuses the alias (unknown host) aborts the session.
	refusing := newTestManager(t, ManagerConfig{
		MaxSessions: 4,
		MaxPerUser:  4,
		IdleTimeout: time.Minute,
		SSHCommand:  func(string) (SSHLaunch, error) { return SSHLaunch{}, errors.New("unknown host") },
	})
	if _, err := refusing.Create(ctx, 1, "tester", SSHSpec("nope"), 80, 24); err == nil {
		t.Fatal("expected the resolver error to propagate")
	}

	// An empty argv is a programming error, not a session.
	emptyArgv := newTestManager(t, ManagerConfig{
		MaxSessions: 4,
		MaxPerUser:  4,
		IdleTimeout: time.Minute,
		SSHCommand:  func(string) (SSHLaunch, error) { return SSHLaunch{}, nil },
	})
	if _, err := emptyArgv.Create(ctx, 1, "tester", SSHSpec("web1"), 80, 24); err == nil {
		t.Fatal("expected an error for an empty argv")
	}
}

func waitFor(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}
