//go:build !linux

package terminal

import "syscall"

// killSession is the portable fallback: it kills the process group led by sid
// and then the process itself. On non-Linux systems background jobs started
// with job control may survive, which is why Linux uses the /proc-based
// implementation in kill_linux.go.
func killSession(sid int) {
	if sid <= 0 {
		return
	}
	if err := syscall.Kill(-sid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(sid, syscall.SIGKILL)
	}
}
