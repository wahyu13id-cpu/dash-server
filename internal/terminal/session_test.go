package terminal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSessionKeepsOutputOfFastExit guards a race between the PTY reader and the
// reaper. A program that writes and exits immediately -- a refused ssh
// connection, a one-shot command -- can be gone before the reader has had a
// chance to run. The reaper used to close the session (and the PTY) right there,
// which discarded whatever the process had written: the client got an empty
// terminal instead of the program's output.
//
// The window is a scheduling race, so the session is created many times; every
// run must deliver the marker, not just most of them.
func TestSessionKeepsOutputOfFastExit(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "fast-exit")
	script := "#!/bin/sh\nprintf 'MARKER=halo\\n'\nexit 0\n"
	if err := os.WriteFile(fixture, []byte(script), 0o755); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	mgr := newTestManager(t, ManagerConfig{
		Shell:           fixture,
		MaxSessions:     8,
		MaxPerUser:      8,
		IdleTimeout:     time.Minute,
		ScrollbackBytes: 8192,
	})

	const attempts = 60
	lost := 0
	for i := 0; i < attempts; i++ {
		sess, err := mgr.Create(context.Background(), 1, "tester", ShellSpec(), 100, 30)
		if err != nil {
			t.Fatalf("create session %d: %v", i, err)
		}
		if !waitFor(func() bool { return sess.Status() == StatusExited }, 5*time.Second) {
			t.Fatalf("session %d never exited, status = %q", i, sess.Status())
		}

		// Attach after the exit was recorded: everything the process wrote must
		// still be there, either as replay or as a pending chunk.
		replay, chunks, cancel := sess.Subscribe()
		var out strings.Builder
		out.Write(replay)
		for chunk := range chunks {
			out.Write(chunk)
		}
		cancel()

		if !strings.Contains(out.String(), "MARKER=halo") {
			lost++
		}
	}
	if lost > 0 {
		t.Fatalf("output was lost in %d of %d sessions", lost, attempts)
	}
}
