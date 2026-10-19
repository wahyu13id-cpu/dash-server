//go:build linux

package sysops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadTotalTicksCountsIdle(t *testing.T) {
	root := t.TempDir()
	body := "cpu  100 0 50 850 0 0 0 0 0 0\n" +
		"cpu0 50 0 25 425 0 0 0 0 0 0\n"
	if err := os.WriteFile(filepath.Join(root, "stat"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// user+nice+system+idle+iowait+irq+softirq+steal = 1000. Idle must be part
	// of the total, otherwise a busy process on an idle machine looks absurd.
	if got := readTotalTicks(root); got != 1000 {
		t.Errorf("readTotalTicks = %d, want 1000", got)
	}
}

func TestReadTotalTicksMissingFile(t *testing.T) {
	if got := readTotalTicks(t.TempDir()); got != 0 {
		t.Errorf("readTotalTicks without /proc/stat = %d, want 0", got)
	}
}

func TestReadProcStatFieldOffsets(t *testing.T) {
	root := t.TempDir()
	pidDir := filepath.Join(root, "1234")
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// rest[] indexes used by the parser: 11 utime, 12 stime, 16 nice,
	// 17 threads, 19 starttime, 20 vsize, 21 rss.
	rest := []string{
		"S", "99", "1234", "1234", "0", "-1", "4194560", "100",
		"0", "0", "0", "15", "7", "0", "0", "20", "-1", "5",
		"0", "9999", "12345678", "321",
	}
	line := "1234 (nama (aneh)) " + strings.Join(rest, " ") + "\n"
	if err := os.WriteFile(filepath.Join(pidDir, "stat"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := readProcStat(root, 1234)
	if err != nil {
		t.Fatalf("readProcStat: %v", err)
	}
	if st.name != "nama (aneh)" {
		t.Errorf("name = %q, want %q", st.name, "nama (aneh)")
	}
	if st.ppid != 99 {
		t.Errorf("ppid = %d, want 99", st.ppid)
	}
	if st.state != "S" {
		t.Errorf("state = %q, want S", st.state)
	}
	if st.utime != 15 || st.stime != 7 {
		t.Errorf("utime/stime = %d/%d, want 15/7", st.utime, st.stime)
	}
	if st.ticks() != 22 {
		t.Errorf("ticks = %d, want 22", st.ticks())
	}
	if st.nice != -1 {
		t.Errorf("nice = %d, want -1", st.nice)
	}
	if st.threads != 5 {
		t.Errorf("threads = %d, want 5", st.threads)
	}
	if st.starttime != 9999 {
		t.Errorf("starttime = %d, want 9999", st.starttime)
	}
	if st.vsize != 12345678 {
		t.Errorf("vsize = %d, want 12345678", st.vsize)
	}
	if st.rssPages != 321 {
		t.Errorf("rssPages = %d, want 321", st.rssPages)
	}
}

func TestReadProcStatRejectsTruncatedLine(t *testing.T) {
	root := t.TempDir()
	pidDir := filepath.Join(root, "7")
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pidDir, "stat"), []byte("7 (x) R 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readProcStat(root, 7); err == nil {
		t.Error("readProcStat accepted a truncated stat line")
	}
}

func TestValidSignal(t *testing.T) {
	for _, name := range []string{"TERM", "term", " kill ", "INT", "HUP", "USR1", "STOP", "CONT"} {
		if !ValidSignal(name) {
			t.Errorf("ValidSignal(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "NINE", "SEGV", "1", "TERM; rm"} {
		if ValidSignal(name) {
			t.Errorf("ValidSignal(%q) = true, want false", name)
		}
	}
}

func TestReniceAndSignalGuards(t *testing.T) {
	svc := New(Config{Enabled: true, ProcRoot: "/proc"})

	// PID 1 is init: never signalled, never reniced.
	if err := svc.SignalProcess(1, "TERM"); err == nil {
		t.Error("SignalProcess(1) was allowed")
	}
	if err := svc.ReniceProcess(1, 0); err == nil {
		t.Error("ReniceProcess(1) was allowed")
	}
	// Unknown signal: rejected before the kernel sees it.
	if err := svc.SignalProcess(4242, "WOBBLE"); err == nil {
		t.Error("SignalProcess with an unknown signal was allowed")
	}
	// Out-of-range nice values are rejected.
	for _, nice := range []int{-21, 20, 100} {
		if err := svc.ReniceProcess(4242, nice); err == nil {
			t.Errorf("ReniceProcess(%d) was allowed", nice)
		}
	}
}
