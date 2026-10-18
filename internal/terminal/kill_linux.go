//go:build linux

package terminal

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// killSession terminates every process belonging to the PTY session identified
// by sid (the session id, which equals the shell's PID because pty.Start makes
// the shell a session leader).
//
// Killing only the shell's process group is not enough: interactive shells
// enable job control, so each job runs in its own process group and would be
// left behind as an orphan. Walking /proc by session id catches them all.
func killSession(sid int) {
	if sid <= 0 {
		return
	}
	// Best-effort group kill first (fast path for non-job-control shells).
	if err := syscall.Kill(-sid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(sid, syscall.SIGKILL)
	}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if processSessionID(pid) == sid {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// processSessionID reads the session id (field 6) from /proc/<pid>/stat, or -1
// when it cannot be determined.
func processSessionID(pid int) int {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return -1
	}
	line := string(data)
	// The comm field (field 2) is wrapped in parentheses and may itself contain
	// spaces or parentheses, so parse from the last ')'.
	end := strings.LastIndexByte(line, ')')
	if end < 0 || end+2 >= len(line) {
		return -1
	}
	fields := strings.Fields(line[end+2:])
	// After comm: state, ppid, pgrp, session, ...
	if len(fields) < 4 {
		return -1
	}
	session, err := strconv.Atoi(fields[3])
	if err != nil {
		return -1
	}
	return session
}
