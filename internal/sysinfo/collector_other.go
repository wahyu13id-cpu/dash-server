//go:build !linux

package sysinfo

import (
	"context"
	"os"
	"time"
)

// Collector is a no-op stub on non-Linux platforms: the dashboard is designed
// for Linux hosts, so elsewhere it simply reports that no snapshot is
// available.
type Collector struct {
	hostname string
}

// New returns a stub collector.
func New(_ time.Duration) *Collector {
	h, _ := os.Hostname()
	return &Collector{hostname: h}
}

// Start does nothing on non-Linux platforms.
func (c *Collector) Start(ctx context.Context) { <-ctx.Done() }

// Latest reports that no snapshot is available.
func (c *Collector) Latest() (Snapshot, bool) { return Snapshot{}, false }
