//go:build linux

package sysops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Signals the dashboard is allowed to send. Everything else is rejected before
// it reaches the kernel.
var allowedSignals = map[string]syscall.Signal{
	"TERM": syscall.SIGTERM,
	"KILL": syscall.SIGKILL,
	"INT":  syscall.SIGINT,
	"HUP":  syscall.SIGHUP,
	"QUIT": syscall.SIGQUIT,
	"USR1": syscall.SIGUSR1,
	"USR2": syscall.SIGUSR2,
	"STOP": syscall.SIGSTOP,
	"CONT": syscall.SIGCONT,
	"ABRT": syscall.SIGABRT,
	"ALRM": syscall.SIGALRM,
}

// ValidSignal reports whether name is an allowed signal.
func ValidSignal(name string) bool {
	_, ok := allowedSignals[strings.ToUpper(strings.TrimSpace(name))]
	return ok
}

// ListProcesses returns a snapshot of every process, sorted by key.
func (s *Service) ListProcesses(ctx context.Context, sortKey, query, userFilter string, limit int) (ProcessList, error) {
	if !s.Enabled() {
		return ProcessList{}, ErrDisabled
	}
	switch sortKey {
	case "", ProcSortCPU, ProcSortMem, ProcSortPID, ProcSortName, ProcSortTime:
	default:
		return ProcessList{}, fmt.Errorf("%w: urutan %q", ErrInvalidInput, sortKey)
	}

	memTotal := readMemTotal(s.cfg.ProcRoot)
	cores := runtime.NumCPU()

	s.mu.Lock()
	prev := s.prevProc
	s.mu.Unlock()

	stats, total := readAllProc(s.cfg.ProcRoot)

	// CPU percentages need two readings. The first request (and any request
	// after a long pause) takes a short second sample and uses the first one as
	// the baseline, instead of reporting 0% until the next poll.
	prevTicks := map[int]uint64{}
	prevTotal := uint64(0)
	if prev == nil || time.Since(prev.at) > 5*time.Second {
		baselineTicks := ticksOf(stats)
		baselineTotal := total
		time.Sleep(250 * time.Millisecond)
		stats, total = readAllProc(s.cfg.ProcRoot)
		prevTicks = baselineTicks
		prevTotal = baselineTotal
	} else {
		prevTicks = prev.ticks
		prevTotal = prev.totalTicks
	}

	s.mu.Lock()
	s.prevProc = &procSample{at: time.Now(), totalTicks: total, ticks: ticksOf(stats)}
	s.mu.Unlock()

	deltaTotal := uint64(0)
	if total > prevTotal {
		deltaTotal = total - prevTotal
	}

	now := time.Now()
	boot := bootTime(s.cfg.ProcRoot)
	needle := strings.ToLower(strings.TrimSpace(query))
	userNeedle := strings.ToLower(strings.TrimSpace(userFilter))

	out := make([]ProcessInfo, 0, len(stats))
	for pid, st := range stats {
		uid := readProcUID(s.cfg.ProcRoot, pid)
		username := s.userName(uid)

		p := ProcessInfo{
			PID:         pid,
			PPID:        st.ppid,
			UID:         uid,
			User:        username,
			State:       st.state,
			Nice:        st.nice,
			RSSBytes:    st.rssPages * uint64(os.Getpagesize()),
			VMSizeBytes: st.vsize,
			Threads:     st.threads,
			Name:        st.name,
			Command:     readProcCmdline(s.cfg.ProcRoot, pid, st.name),
		}
		if deltaTotal > 0 {
			if before, ok := prevTicks[pid]; ok && st.ticks() >= before {
				p.CPUPercent = round1(float64(st.ticks()-before) / float64(deltaTotal) * 100 * float64(cores))
			}
		}
		if memTotal > 0 {
			p.MemPercent = round1(float64(p.RSSBytes) / float64(memTotal) * 100)
		}
		if !boot.IsZero() && st.starttime > 0 {
			start := boot.Add(time.Duration(st.starttime) * time.Second / time.Duration(clockTicks()))
			p.StartedAt = start
			p.ElapsedSec = int64(now.Sub(start).Seconds())
		}

		if needle != "" {
			haystack := strings.ToLower(p.Name + " " + p.Command + " " + p.User + " " + strconv.Itoa(p.PID))
			if !strings.Contains(haystack, needle) {
				continue
			}
		}
		if userNeedle != "" && strings.ToLower(p.User) != userNeedle {
			continue
		}
		out = append(out, p)
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch sortKey {
		case ProcSortMem:
			if a.RSSBytes != b.RSSBytes {
				return a.RSSBytes > b.RSSBytes
			}
		case ProcSortPID:
			return a.PID < b.PID
		case ProcSortName:
			if a.Name != b.Name {
				return a.Name < b.Name
			}
		case ProcSortTime:
			if a.ElapsedSec != b.ElapsedSec {
				return a.ElapsedSec > b.ElapsedSec
			}
		default: // cpu
			if a.CPUPercent != b.CPUPercent {
				return a.CPUPercent > b.CPUPercent
			}
			if a.RSSBytes != b.RSSBytes {
				return a.RSSBytes > b.RSSBytes
			}
		}
		return a.PID < b.PID
	})

	total2 := len(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return ProcessList{
		Processes:   out,
		Total:       total2,
		CPUCores:    cores,
		MemTotal:    memTotal,
		CollectedAt: now,
	}, nil
}

// SignalProcess sends one allowed signal to a pid. PID 1 (the init system) is
// refused: stopping it cannot be undone from the dashboard.
func (s *Service) SignalProcess(pid int, signal string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	if pid <= 1 {
		return fmt.Errorf("%w: pid %d tidak boleh dikirim sinyal", ErrInvalidInput, pid)
	}
	sig, ok := allowedSignals[strings.ToUpper(strings.TrimSpace(signal))]
	if !ok {
		return fmt.Errorf("%w: sinyal %q", ErrInvalidInput, signal)
	}
	if err := syscall.Kill(pid, sig); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return nil
}

// ReniceProcess changes the scheduling priority of a process (-20..19).
func (s *Service) ReniceProcess(pid, nice int) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	if pid <= 1 {
		return fmt.Errorf("%w: pid %d tidak boleh diubah", ErrInvalidInput, pid)
	}
	if nice < -20 || nice > 19 {
		return fmt.Errorf("%w: nice %d di luar rentang -20..19", ErrInvalidInput, nice)
	}
	if err := syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return nil
}

// --- /proc parsing ----------------------------------------------------------

func (p procStat) ticks() uint64 { return p.utime + p.stime }

// readAllProc reads every numeric /proc entry plus the aggregate CPU ticks.
func readAllProc(root string) (map[int]procStat, uint64) {
	out := make(map[int]procStat, 256)
	entries, err := os.ReadDir(root)
	if err != nil {
		return out, 0
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		st, err := readProcStat(root, pid)
		if err != nil {
			continue
		}
		out[pid] = st
	}
	return out, readTotalTicks(root)
}

func ticksOf(stats map[int]procStat) map[int]uint64 {
	out := make(map[int]uint64, len(stats))
	for pid, st := range stats {
		out[pid] = st.ticks()
	}
	return out
}

// readProcStat parses /proc/<pid>/stat, whose second field is a parenthesized
// name that may itself contain spaces and parentheses.
func readProcStat(root string, pid int) (procStat, error) {
	data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return procStat{}, err
	}
	s := string(data)
	open := strings.IndexByte(s, '(')
	closeIdx := strings.LastIndexByte(s, ')')
	if open < 0 || closeIdx < open {
		return procStat{}, fmt.Errorf("stat tidak terbaca")
	}
	name := s[open+1 : closeIdx]
	rest := strings.Fields(strings.TrimSpace(s[closeIdx+1:]))
	// rest[0] = state, then fields 4.. with the standard offsets.
	if len(rest) < 22 {
		return procStat{}, fmt.Errorf("stat terlalu pendek")
	}
	st := procStat{pid: pid, name: name, state: rest[0]}
	st.ppid, _ = strconv.Atoi(rest[1])
	st.utime, _ = strconv.ParseUint(rest[11], 10, 64)
	st.stime, _ = strconv.ParseUint(rest[12], 10, 64)
	st.nice, _ = strconv.Atoi(rest[16])
	st.threads, _ = strconv.Atoi(rest[17])
	st.starttime, _ = strconv.ParseUint(rest[19], 10, 64)
	st.vsize, _ = strconv.ParseUint(rest[20], 10, 64)
	st.rssPages, _ = strconv.ParseUint(rest[21], 10, 64)
	return st, nil
}

// readProcCmdline joins the NUL-separated argv, falling back to the stat name.
func readProcCmdline(root string, pid int, fallback string) string {
	data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "cmdline"))
	if err != nil || len(data) == 0 {
		// Kernel threads have an empty cmdline.
		return "[" + fallback + "]"
	}
	parts := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	cmd := strings.Join(parts, " ")
	if len(cmd) > 500 {
		cmd = cmd[:500] + "…"
	}
	return cmd
}

// readProcUID reads the real uid of a process from /proc/<pid>/status.
func readProcUID(root string, pid int) uint32 {
	data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "status"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(line[4:])
		if len(fields) == 0 {
			return 0
		}
		uid, err := strconv.ParseUint(fields[0], 10, 32)
		if err != nil {
			return 0
		}
		return uint32(uid)
	}
	return 0
}

// readTotalTicks sums the CPU time of every core from /proc/stat.
//
// All of user nice system idle iowait irq softirq steal are counted — idle
// included, exactly like top(1) — so that dividing a process delta by this
// value and multiplying by the core count yields "one busy core = 100%".
// guest/guest_nice are skipped because the kernel already counts them in
// user/nice.
func readTotalTicks(root string) uint64 {
	data, err := os.ReadFile(filepath.Join(root, "stat"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		var total uint64
		for i, f := range strings.Fields(line)[1:] {
			if i >= 8 {
				break
			}
			if v, err := strconv.ParseUint(f, 10, 64); err == nil {
				total += v
			}
		}
		return total
	}
	return 0
}

// readMemTotal returns MemTotal in bytes.
func readMemTotal(root string) uint64 {
	data, err := os.ReadFile(filepath.Join(root, "meminfo"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}

// bootTime reads the boot timestamp from /proc/stat (btime, in seconds).
func bootTime(root string) time.Time {
	data, err := os.ReadFile(filepath.Join(root, "stat"))
	if err != nil {
		return time.Time{}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "btime ") {
			continue
		}
		secs, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
		if err != nil {
			return time.Time{}
		}
		return time.Unix(secs, 0)
	}
	return time.Time{}
}
