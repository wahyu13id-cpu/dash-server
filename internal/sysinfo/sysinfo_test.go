//go:build linux

package sysinfo

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadCPUStat(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "stat"), `cpu  100 0 50 850 0 0 0 0 0 0
cpu0 50 0 25 425 0 0 0 0 0 0
cpu1 50 0 25 425 0 0 0 0 0 0
intr 0
ctxt 0
`)

	got, err := readCPUStat(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.cores) != 2 {
		t.Fatalf("cores = %d, want 2", len(got.cores))
	}
	if got.total != 1000 { // 100+0+50+850
		t.Errorf("total = %d, want 1000", got.total)
	}
	if got.idle != 850 {
		t.Errorf("idle = %d, want 850", got.idle)
	}
}

func TestBusyPercent(t *testing.T) {
	// 100 of 1000 ticks idle => 90% busy.
	if got := busyPercent(100, 1000); got != 90 {
		t.Errorf("busyPercent(100,1000) = %v, want 90", got)
	}
	if got := busyPercent(0, 0); got != 0 {
		t.Errorf("busyPercent(0,0) = %v, want 0", got)
	}
	// Clamp when idle exceeds total.
	if got := busyPercent(2000, 1000); got != 0 {
		t.Errorf("busyPercent(2000,1000) = %v, want 0", got)
	}
}

func TestReadMemInfo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "meminfo"), `MemTotal:       1000000 kB
MemFree:         200000 kB
MemAvailable:    400000 kB
Buffers:          50000 kB
Cached:          150000 kB
SwapTotal:       500000 kB
SwapFree:        400000 kB
`)

	got, err := readMemInfo(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalBytes != 1000000*1024 {
		t.Errorf("TotalBytes = %d", got.TotalBytes)
	}
	if got.UsedBytes != 600000*1024 { // total - available
		t.Errorf("UsedBytes = %d, want %d", got.UsedBytes, 600000*1024)
	}
	if got.UsedPercent != 60 {
		t.Errorf("UsedPercent = %v, want 60", got.UsedPercent)
	}
	if got.SwapUsedBytes != 100000*1024 {
		t.Errorf("SwapUsedBytes = %d", got.SwapUsedBytes)
	}
	if got.SwapUsedPercent != 20 {
		t.Errorf("SwapUsedPercent = %v, want 20", got.SwapUsedPercent)
	}
}

func TestReadNetDev(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "net", "dev"), `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:    1000       5    0    0    0     0          0         0     1000       5    0    0    0     0       0          0
  eth0:  500000     400    0    0    0     0          0         0   250000     300    0    0    0     0       0          0
`)

	got := readNetDev(root)
	if len(got) != 2 {
		t.Fatalf("interfaces = %d, want 2", len(got))
	}
	if got["eth0"].rx != 500000 || got["eth0"].tx != 250000 {
		t.Errorf("eth0 = %+v", got["eth0"])
	}
	if got["lo"].rx != 1000 {
		t.Errorf("lo = %+v", got["lo"])
	}
}

func TestReadLoadAvg(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "loadavg"), "0.52 0.38 0.31 2/345 12345\n")
	got := readLoadAvg(root)
	if got.Load1 != 0.52 || got.Running != 2 || got.Total != 345 {
		t.Errorf("loadavg = %+v", got)
	}
}

func TestUnescapeMount(t *testing.T) {
	cases := map[string]string{
		"/dev/sda1":            "/dev/sda1",
		`/mnt/my\040disk`:      "/mnt/my disk",
		`/a\011b`:              "/a\tb",
		`/plain`:               "/plain",
		`/trailing\`:           `/trailing\`,
	}
	for in, want := range cases {
		if got := unescapeMount(in); got != want {
			t.Errorf("unescapeMount(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTopN(t *testing.T) {
	in := make([]Process, 25)
	for i := range in {
		in[i] = Process{PID: i}
	}
	if got := topN(in, 10); len(got) != 10 {
		t.Errorf("topN len = %d, want 10", len(got))
	}
	if got := topN(in[:3], 10); len(got) != 3 {
		t.Errorf("topN short len = %d, want 3", len(got))
	}
}

// TestCollectorReadsRealProc exercises the collector against the running host's
// /proc and asserts the snapshot is self-consistent.
func TestCollectorReadsRealProc(t *testing.T) {
	c := New(100 * time.Millisecond)
	c.sample() // baseline
	time.Sleep(120 * time.Millisecond)
	c.sample()

	snap, ok := c.Latest()
	if !ok {
		t.Fatal("Latest() not ready after two samples")
	}
	if snap.Memory.TotalBytes == 0 {
		t.Error("memory total is zero")
	}
	if snap.CPU.Cores == 0 {
		t.Error("no CPU cores detected")
	}
	if snap.CPU.UsagePercent < 0 || snap.CPU.UsagePercent > 100 {
		t.Errorf("cpu usage out of range: %v", snap.CPU.UsagePercent)
	}
	if len(snap.CPU.History) == 0 {
		t.Error("cpu history is empty")
	}
	if snap.Processes.Total == 0 {
		t.Error("no processes detected")
	}
	if len(snap.Processes.ByCPU) > 10 || len(snap.Processes.ByMemory) > 10 {
		t.Error("top lists exceed 10 entries")
	}
	if snap.Host.Hostname == "" {
		t.Error("hostname is empty")
	}
	// The by-memory list must be sorted descending.
	if len(snap.Processes.ByMemory) > 1 {
		prev := snap.Processes.ByMemory[0].RSSBytes
		for _, p := range snap.Processes.ByMemory[1:] {
			if p.RSSBytes > prev {
				t.Errorf("byMemory not sorted: %d then %d", prev, p.RSSBytes)
				break
			}
			prev = p.RSSBytes
		}
	}
}
