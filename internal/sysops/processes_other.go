//go:build !linux

package sysops

import "context"

// The dashboard targets Linux servers: process inspection reads procfs and the
// signal set is Linux's. Other platforms compile (so `go build ./...` works on a
// development machine) but report the feature as unavailable.

// ValidSignal always reports false where signalling is unsupported.
func ValidSignal(string) bool { return false }

// ListProcesses is unsupported outside Linux.
func (s *Service) ListProcesses(context.Context, string, string, string, int) (ProcessList, error) {
	return ProcessList{}, ErrUnsupported
}

// SignalProcess is unsupported outside Linux.
func (s *Service) SignalProcess(int, string) error { return ErrUnsupported }

// ReniceProcess is unsupported outside Linux.
func (s *Service) ReniceProcess(int, int) error { return ErrUnsupported }
