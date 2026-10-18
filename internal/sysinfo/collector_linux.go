//go:build linux

package sysinfo

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// historyLen is how many CPU samples are retained for sparklines.
const historyLen = 60

// Collector samples host metrics on a fixed interval in the background and
// keeps the most recent snapshot. Reads are cheap enough (a pass over /proc)
// that the HTTP layer can serve the cached snapshot without blocking.
type Collector struct {
	procRoot string
	interval time.Duration

	pageSize int
	hostname string
	model    string
	kernel   string
	arch     string

	mu     sync.RWMutex
	latest Snapshot
	ready  bool

	// Previous raw counters, used to derive rates between samples.
	prevCPU    cpuTimes
	prevNet    map[string]netCounters
	prevProc   map[int]procCounters
	prevTime   time.Time
	prevTotal  uint64
	history    []float64
	haveSample bool
}

type cpuTimes struct {
	total uint64
	idle  uint64
	cores []coreTimes
}

type coreTimes struct {
	total uint64
	idle  uint64
}

type netCounters struct {
	rx uint64
	tx uint64
}

type procCounters struct {
	ticks   uint64
	rss     uint64
	threads int
}

// New creates a collector that samples every interval. A non-positive interval
// falls back to 2 seconds.
func New(interval time.Duration) *Collector {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	c := &Collector{
		procRoot: "/proc",
		interval: interval,
		pageSize: os.Getpagesize(),
		prevNet:  make(map[string]netCounters),
		prevProc: make(map[int]procCounters),
	}
	if h, err := os.Hostname(); err == nil {
		c.hostname = h
	}
	c.model = readCPUModel(c.procRoot)
	if rel, machine, err := uname(); err == nil {
		c.kernel, c.arch = rel, machine
	}
	return c
}

// Start launches the background sampler. The first useful snapshot (with rates)
// is available a few hundred milliseconds after Start.
func (c *Collector) Start(ctx context.Context) {
	go func() {
		c.sample() // baseline: establishes previous counters
		timer := time.NewTimer(400 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		c.sample() // first snapshot with real deltas

		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.sample()
			}
		}
	}()
}

// Latest returns the most recent snapshot. The boolean is false until the first
// sample completes.
func (c *Collector) Latest() (Snapshot, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.latest, c.ready
}

func (c *Collector) sample() {
	now := time.Now()

	stat, err := readCPUStat(c.procRoot)
	if err != nil {
		return
	}
	mem, err := readMemInfo(c.procRoot)
	if err != nil {
		return
	}

	snap := Snapshot{
		CollectedAt: now,
		Host: Host{
			Hostname:  c.hostname,
			Kernel:    c.kernel,
			Arch:      c.arch,
			UptimeSec: readUptime(c.procRoot),
		},
		Memory: mem,
		Load:   readLoadAvg(c.procRoot),
		Disks:  readDisks(c.procRoot),
	}

	// --- CPU ---------------------------------------------------------------
	var deltaTotal uint64
	cpu := CPU{Model: c.model, Cores: len(stat.cores)}
	if c.haveSample && stat.total >= c.prevCPU.total {
		deltaTotal = stat.total - c.prevCPU.total
		cpu.UsagePercent = round1(busyPercent(stat.idle-c.prevCPU.idle, deltaTotal))
		cpu.PerCore = make([]float64, len(stat.cores))
		for i := range stat.cores {
			if i >= len(c.prevCPU.cores) {
				continue
			}
			dt := stat.cores[i].total - c.prevCPU.cores[i].total
			di := stat.cores[i].idle - c.prevCPU.cores[i].idle
			cpu.PerCore[i] = round1(busyPercent(di, dt))
		}
	}
	c.history = append(c.history, cpu.UsagePercent)
	if len(c.history) > historyLen {
		c.history = c.history[len(c.history)-historyLen:]
	}
	cpu.History = append([]float64(nil), c.history...)
	snap.CPU = cpu

	// --- Network -----------------------------------------------------------
	snap.Network = c.readNetwork(now)

	// --- Processes ---------------------------------------------------------
	cores := len(stat.cores)
	if cores < 1 {
		cores = 1
	}
	procs := c.readProcesses(deltaTotal, cores, mem.TotalBytes)
	snap.Processes = procs

	// --- Persist state -----------------------------------------------------
	c.prevCPU = stat
	c.prevTotal = stat.total
	c.prevTime = now
	c.haveSample = true

	c.mu.Lock()
	c.latest = snap
	c.ready = true
	c.mu.Unlock()
}

// readNetwork computes per-interface and aggregate throughput from /proc/net/dev.
func (c *Collector) readNetwork(now time.Time) Network {
	raw := readNetDev(c.procRoot)
	out := Network{Interfaces: make([]NetIface, 0, len(raw))}

	elapsed := now.Sub(c.prevTime).Seconds()
	if !c.haveSample || elapsed <= 0 {
		elapsed = 0
	}

	for name, cur := range raw {
		iface := NetIface{Name: name, RxBytes: cur.rx, TxBytes: cur.tx}
		if prev, ok := c.prevNet[name]; ok && elapsed > 0 {
			if cur.rx >= prev.rx {
				iface.RxBytesPerSec = round2(float64(cur.rx-prev.rx) / elapsed)
			}
			if cur.tx >= prev.tx {
				iface.TxBytesPerSec = round2(float64(cur.tx-prev.tx) / elapsed)
			}
		}
		out.Interfaces = append(out.Interfaces, iface)

		if name == "lo" {
			continue
		}
		out.RxTotalBytes += cur.rx
		out.TxTotalBytes += cur.tx
		out.RxBytesPerSec += iface.RxBytesPerSec
		out.TxBytesPerSec += iface.TxBytesPerSec
	}
	out.RxBytesPerSec = round2(out.RxBytesPerSec)
	out.TxBytesPerSec = round2(out.TxBytesPerSec)

	sort.Slice(out.Interfaces, func(i, j int) bool {
		return out.Interfaces[i].Name < out.Interfaces[j].Name
	})

	c.prevNet = raw
	return out
}

// readProcesses ranks processes by CPU and by memory.
func (c *Collector) readProcesses(deltaTotal uint64, cores int, memTotal uint64) Processes {
	entries, err := os.ReadDir(c.procRoot)
	if err != nil {
		return Processes{}
	}

	next := make(map[int]procCounters, len(entries))
	list := make([]Process, 0, len(entries))

	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		name, ticks, rss, threads, ok := readProcStat(c.procRoot, pid)
		if !ok {
			continue
		}
		next[pid] = procCounters{ticks: ticks, rss: rss, threads: threads}

		p := Process{PID: pid, Name: name, RSSBytes: rss, Threads: threads}
		if prev, ok := c.prevProc[pid]; ok && deltaTotal > 0 && ticks >= prev.ticks {
			delta := ticks - prev.ticks
			// top(1) semantics: a full core reads as 100%.
			p.CPUPercent = round1(float64(delta) / float64(deltaTotal) * 100 * float64(cores))
		}
		if memTotal > 0 {
			p.MemPercent = round1(float64(rss) / float64(memTotal) * 100)
		}
		p.Command = readProcCmdline(c.procRoot, pid, name)
		list = append(list, p)
	}

	byCPU := append([]Process(nil), list...)
	sort.Slice(byCPU, func(i, j int) bool {
		if byCPU[i].CPUPercent != byCPU[j].CPUPercent {
			return byCPU[i].CPUPercent > byCPU[j].CPUPercent
		}
		return byCPU[i].RSSBytes > byCPU[j].RSSBytes
	})

	byMem := append([]Process(nil), list...)
	sort.Slice(byMem, func(i, j int) bool {
		if byMem[i].RSSBytes != byMem[j].RSSBytes {
			return byMem[i].RSSBytes > byMem[j].RSSBytes
		}
		return byMem[i].CPUPercent > byMem[j].CPUPercent
	})

	c.prevProc = next
	return Processes{Total: len(list), ByCPU: topN(byCPU, 10), ByMemory: topN(byMem, 10)}
}

func topN(in []Process, n int) []Process {
	if len(in) > n {
		return in[:n]
	}
	return in
}

// --- /proc parsers ---------------------------------------------------------

// readCPUStat parses /proc/stat: the aggregate "cpu" line and each per-core
// line. guest/guest_nice are excluded because they are already counted in
// user/nice.
func readCPUStat(root string) (cpuTimes, error) {
	f, err := os.Open(filepath.Join(root, "stat"))
	if err != nil {
		return cpuTimes{}, err
	}
	defer f.Close()

	var out cpuTimes
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu") {
			if strings.HasPrefix(line, "intr") {
				break
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		vals := make([]uint64, 0, 8)
		for _, fv := range fields[1:] {
			n, err := strconv.ParseUint(fv, 10, 64)
			if err != nil {
				n = 0
			}
			vals = append(vals, n)
		}
		// user nice system idle iowait irq softirq steal [guest guest_nice]
		var total, idle uint64
		for i, v := range vals {
			if i >= 8 {
				break
			}
			total += v
		}
		if len(vals) > 3 {
			idle = vals[3]
		}
		if len(vals) > 4 {
			idle += vals[4] // iowait counts as idle time
		}
		ct := coreTimes{total: total, idle: idle}
		if fields[0] == "cpu" {
			out.total, out.idle = total, idle
		} else {
			out.cores = append(out.cores, ct)
		}
	}
	return out, nil
}

// readMemInfo parses /proc/meminfo into a Memory breakdown.
func readMemInfo(root string) (Memory, error) {
	f, err := os.Open(filepath.Join(root, "meminfo"))
	if err != nil {
		return Memory{}, err
	}
	defer f.Close()

	kv := make(map[string]uint64, 16)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		n, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		kv[key] = n * 1024 // values are in kB
	}

	m := Memory{
		TotalBytes:     kv["MemTotal"],
		FreeBytes:      kv["MemFree"],
		AvailableBytes: kv["MemAvailable"],
		CachedBytes:    kv["Cached"],
		BuffersBytes:   kv["Buffers"],
		SwapTotalBytes: kv["SwapTotal"],
	}
	if m.AvailableBytes == 0 {
		m.AvailableBytes = m.FreeBytes + m.CachedBytes + m.BuffersBytes
	}
	if m.TotalBytes > m.AvailableBytes {
		m.UsedBytes = m.TotalBytes - m.AvailableBytes
	}
	if m.TotalBytes > 0 {
		m.UsedPercent = round1(float64(m.UsedBytes) / float64(m.TotalBytes) * 100)
	}
	if m.SwapTotalBytes > 0 {
		free := kv["SwapFree"]
		if m.SwapTotalBytes > free {
			m.SwapUsedBytes = m.SwapTotalBytes - free
		}
		m.SwapUsedPercent = round1(float64(m.SwapUsedBytes) / float64(m.SwapTotalBytes) * 100)
	}
	return m, nil
}

// readLoadAvg parses /proc/loadavg.
func readLoadAvg(root string) Load {
	data, err := os.ReadFile(filepath.Join(root, "loadavg"))
	if err != nil {
		return Load{}
	}
	fields := strings.Fields(string(data))
	var l Load
	if len(fields) > 0 {
		l.Load1, _ = strconv.ParseFloat(fields[0], 64)
	}
	if len(fields) > 1 {
		l.Load5, _ = strconv.ParseFloat(fields[1], 64)
	}
	if len(fields) > 2 {
		l.Load15, _ = strconv.ParseFloat(fields[2], 64)
	}
	if len(fields) > 3 {
		if a, b, ok := strings.Cut(fields[3], "/"); ok {
			l.Running, _ = strconv.Atoi(a)
			l.Total, _ = strconv.Atoi(b)
		}
	}
	return l
}

// readUptime parses /proc/uptime and returns whole seconds.
func readUptime(root string) int64 {
	data, err := os.ReadFile(filepath.Join(root, "uptime"))
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	sec, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return int64(sec)
}

// readNetDev parses /proc/net/dev into per-interface byte counters.
func readNetDev(root string) map[string]netCounters {
	f, err := os.Open(filepath.Join(root, "net", "dev"))
	if err != nil {
		return map[string]netCounters{}
	}
	defer f.Close()

	out := make(map[string]netCounters)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" || name == "Inter-|" || name == "face" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)
		out[name] = netCounters{rx: rx, tx: tx}
	}
	return out
}

// pseudoFSTypes are virtual filesystems that carry no meaningful capacity.
var pseudoFSTypes = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true,
	"tmpfs": true, "cgroup": true, "cgroup2": true, "securityfs": true,
	"pstore": true, "efivarfs": true, "bpf": true, "configfs": true,
	"debugfs": true, "tracefs": true, "fusectl": true, "hugetlbfs": true,
	"mqueue": true, "ramfs": true, "autofs": true, "binfmt_misc": true,
	"rpc_pipefs": true, "nsfs": true, "squashfs": true, "fuse.lxcfs": true,
}

// readDisks lists real mounted filesystems with their capacity via statfs.
func readDisks(root string) Disks {
	f, err := os.Open(filepath.Join(root, "mounts"))
	if err != nil {
		return Disks{}
	}
	defer f.Close()

	var disks []Disk
	seen := make(map[string]bool)

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		device := unescapeMount(fields[0])
		mount := unescapeMount(fields[1])
		fstype := fields[2]
		if pseudoFSTypes[fstype] {
			continue
		}
		if seen[device] {
			continue
		}

		var st syscall.Statfs_t
		if err := syscall.Statfs(mount, &st); err != nil || st.Blocks == 0 {
			continue
		}
		bsize := uint64(st.Bsize)
		total := st.Blocks * bsize
		free := st.Bavail * bsize
		used := (st.Blocks - st.Bfree) * bsize
		d := Disk{
			Device:     device,
			Mount:      mount,
			FSType:     fstype,
			TotalBytes: total,
			UsedBytes:  used,
			FreeBytes:  free,
		}
		if denom := used + free; denom > 0 {
			d.UsedPercent = round1(float64(used) / float64(denom) * 100)
		}
		seen[device] = true
		disks = append(disks, d)
	}

	sort.Slice(disks, func(i, j int) bool { return disks[i].Mount < disks[j].Mount })

	var out Disks
	out.Filesystems = disks
	for _, d := range disks {
		out.TotalBytes += d.TotalBytes
		out.UsedBytes += d.UsedBytes
		out.FreeBytes += d.FreeBytes
	}
	if denom := out.UsedBytes + out.FreeBytes; denom > 0 {
		out.UsedPercent = round1(float64(out.UsedBytes) / float64(denom) * 100)
	}
	return out
}

// readProcStat returns a process's name, CPU ticks (utime+stime), resident
// bytes and thread count from /proc/<pid>/stat.
func readProcStat(root string, pid int) (name string, ticks, rss uint64, threads int, ok bool) {
	data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", 0, 0, 0, false
	}
	line := string(data)
	open := strings.IndexByte(line, '(')
	closeIdx := strings.LastIndexByte(line, ')')
	if open < 0 || closeIdx < open {
		return "", 0, 0, 0, false
	}
	name = line[open+1 : closeIdx]
	fields := strings.Fields(line[closeIdx+2:])
	// After comm: state(3) ... utime(14) stime(15) ... num_threads(20) ... rss(24)
	if len(fields) < 22 {
		return name, 0, 0, 0, true
	}
	utime, _ := strconv.ParseUint(fields[11], 10, 64)
	stime, _ := strconv.ParseUint(fields[12], 10, 64)
	threads, _ = strconv.Atoi(fields[17])
	rssPages, _ := strconv.ParseUint(fields[21], 10, 64)
	ticks = utime + stime
	rss = rssPages * uint64(os.Getpagesize())
	return name, ticks, rss, threads, true
}

// readProcCmdline returns the full command line, falling back to the process
// name for kernel threads (which have an empty cmdline).
func readProcCmdline(root string, pid int, name string) string {
	data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "cmdline"))
	if err != nil || len(data) == 0 {
		return "[" + name + "]"
	}
	cmd := strings.ReplaceAll(string(data), "\x00", " ")
	cmd = strings.TrimSpace(cmd)
	const maxLen = 300
	if len(cmd) > maxLen {
		cmd = cmd[:maxLen] + "…"
	}
	return cmd
}

func readCPUModel(root string) string {
	f, err := os.Open(filepath.Join(root, "cpuinfo"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if k, v, ok := strings.Cut(line, ":"); ok {
			switch strings.TrimSpace(k) {
			case "model name", "Hardware", "Processor":
				if s := strings.TrimSpace(v); s != "" {
					return s
				}
			}
		}
		if strings.HasPrefix(line, "processor") {
			continue
		}
	}
	return ""
}

// unescapeMount decodes the octal escapes the kernel uses for spaces and other
// special characters in /proc/mounts (e.g. \040 for a space).
func unescapeMount(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func uname() (release, machine string, err error) {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return "", "", err
	}
	return utsString(u.Release[:]), utsString(u.Machine[:]), nil
}

func utsString(b []int8) string {
	var sb strings.Builder
	for _, c := range b {
		if c == 0 {
			break
		}
		sb.WriteByte(byte(c))
	}
	return sb.String()
}

// --- math helpers ----------------------------------------------------------

// busyPercent returns utilisation as a percentage from an idle delta and a
// total delta, clamped to [0, 100].
func busyPercent(idle, total uint64) float64 {
	if total == 0 {
		return 0
	}
	if idle > total {
		idle = total
	}
	return (1 - float64(idle)/float64(total)) * 100
}

func round1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
