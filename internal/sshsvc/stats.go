package sshsvc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HostStats is a point-in-time view of the machine an SSH session is connected
// to. It is gathered by running a small read-only script on the remote host (see
// remoteStatsScript) and is deliberately limited to the numbers a session status
// strip needs: hostname, CPU, memory, network throughput and disk usage.
//
// Rate fields are pointers because a single probe cannot know them: they are
// derived from the difference between two probes, so the very first sample of a
// host reports them as nil ("measuring") instead of inventing a number.
type HostStats struct {
	Host      string  `json:"host"`
	UptimeSec int64   `json:"uptimeSec"`
	CPUCount  int     `json:"cpuCount"`
	Load1     float64 `json:"load1"`
	Load5     float64 `json:"load5"`
	Load15    float64 `json:"load15"`

	CPUPercent *float64 `json:"cpuPercent,omitempty"`

	MemTotalBytes  uint64 `json:"memTotalBytes"`
	MemUsedBytes   uint64 `json:"memUsedBytes"`
	SwapTotalBytes uint64 `json:"swapTotalBytes"`
	SwapUsedBytes  uint64 `json:"swapUsedBytes"`

	NetRxBytesPerSec *float64 `json:"netRxBytesPerSec,omitempty"`
	NetTxBytesPerSec *float64 `json:"netTxBytesPerSec,omitempty"`

	DiskTotalBytes uint64 `json:"diskTotalBytes"`
	DiskUsedBytes  uint64 `json:"diskUsedBytes"`

	CollectedAt time.Time `json:"collectedAt"`
}

const (
	// remoteStatsTimeout bounds one probe, including the SSH handshake. A probe
	// that reuses a multiplexed connection finishes in milliseconds; the budget
	// is for the cold case where ssh has to authenticate first.
	remoteStatsTimeout = 20 * time.Second
	// statsRefresh is how long a sample is reused before another probe runs, so
	// several viewers (or a fast poll) cannot turn into a probe storm.
	statsRefresh = 2 * time.Second
	// statsSampleTTL is how far apart two probes may be and still produce a
	// meaningful rate. Beyond it the counters are treated as unrelated.
	statsSampleTTL = 2 * time.Minute
	// statsMuxPersist keeps the multiplexed connection (and therefore the
	// authenticated session) alive between probes.
	statsMuxPersist = "120"
)

// remoteStatsScript prints one flat list of "KEY=VALUE" counters, read straight
// from the kernel interfaces. Design notes:
//
//   - It is a constant. No user input ever reaches it, so an alias cannot inject
//     anything into the remote command.
//   - It is fed to "sh -s" on stdin, which removes all quoting questions: any
//     remote login shell (bash, dash, fish, csh) only has to run a plain command
//     and sh reads the script itself.
//   - It only reads /proc, /proc/loadavg and df, so it costs a few milliseconds
//     and never changes anything on the remote host.
//   - Unknown lines are ignored by the parser, so a login banner or a host
//     without /proc simply yields fewer keys.
const remoteStatsScript = `awk '{printf "HOST=%s\n", $1}' /proc/sys/kernel/hostname 2>/dev/null
awk '{printf "UPTIME=%d\n", $1}' /proc/uptime 2>/dev/null
awk '/^processor/{n++} END{printf "CORES=%d\n", n+0}' /proc/cpuinfo 2>/dev/null
awk '/^cpu /{t=0; for(i=2;i<=NF;i++) t+=$i; printf "CPU_TOTAL=%s\nCPU_IDLE=%s\n", t, $5+$6}' /proc/stat 2>/dev/null
awk '/^MemTotal:/{t=$2} /^MemAvailable:/{a=$2} /^SwapTotal:/{st=$2} /^SwapFree:/{sf=$2} END{printf "MEM_TOTAL=%d\nMEM_AVAIL=%d\nSWAP_TOTAL=%d\nSWAP_FREE=%d\n", t+0, a+0, st+0, sf+0}' /proc/meminfo 2>/dev/null
awk '/:/{gsub(":"," "); if($1=="lo") next; r+=$2; w+=$10} END{printf "NET_RX=%d\nNET_TX=%d\n", r+0, w+0}' /proc/net/dev 2>/dev/null
awk '{printf "LOAD1=%s\nLOAD5=%s\nLOAD15=%s\n", $1, $2, $3}' /proc/loadavg 2>/dev/null
df -kP / 2>/dev/null | awk 'NR>1{printf "DISK_TOTAL=%d\nDISK_USED=%d\n", $2*1024, $3*1024; exit}'
`

// remoteKeys is the closed set of keys the parser accepts; anything else in the
// output (login banners, MOTD, stderr merged by a wrapper) is ignored.
var remoteKeys = map[string]bool{
	"HOST": true, "UPTIME": true, "CORES": true,
	"CPU_TOTAL": true, "CPU_IDLE": true,
	"MEM_TOTAL": true, "MEM_AVAIL": true, "SWAP_TOTAL": true, "SWAP_FREE": true,
	"NET_RX": true, "NET_TX": true,
	"LOAD1": true, "LOAD5": true, "LOAD15": true,
	"DISK_TOTAL": true, "DISK_USED": true,
}

// statsState holds the previous probe per alias (needed to turn counters into
// rates) plus the last computed sample, and serialises probes per alias so two
// concurrent viewers share one SSH round trip.
type statsState struct {
	mu      sync.Mutex
	prev    map[string]remoteCounters
	last    map[string]*HostStats
	locks   map[string]*sync.Mutex
	lastErr map[string]error
}

type remoteCounters struct {
	at       time.Time
	cpuTotal uint64
	cpuIdle  uint64
	netRx    uint64
	netTx    uint64
}

func newStatsState() *statsState {
	return &statsState{
		prev:    map[string]remoteCounters{},
		last:    map[string]*HostStats{},
		locks:   map[string]*sync.Mutex{},
		lastErr: map[string]error{},
	}
}

// aliasLock returns the per-alias mutex, creating it on first use.
func (st *statsState) aliasLock(alias string) *sync.Mutex {
	st.mu.Lock()
	defer st.mu.Unlock()
	mu, ok := st.locks[alias]
	if !ok {
		mu = &sync.Mutex{}
		st.locks[alias] = mu
	}
	return mu
}

// cached returns the last sample when it is fresh enough to reuse.
func (st *statsState) cached(alias string) (*HostStats, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	last, ok := st.last[alias]
	if !ok || time.Since(last.CollectedAt) > statsRefresh {
		return nil, false
	}
	out := *last
	return &out, true
}

// lastSample returns the most recent sample whatever its age.
func (st *statsState) lastSample(alias string) (*HostStats, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	last, ok := st.last[alias]
	if !ok {
		return nil, false
	}
	out := *last
	return &out, true
}

func (st *statsState) store(alias string, counters remoteCounters, stats *HostStats) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.prev[alias] = counters
	st.last[alias] = stats
	delete(st.lastErr, alias)
}

func (st *statsState) previous(alias string) remoteCounters {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.prev[alias]
}

// HostStats probes an SSH host and returns its current resource usage. Errors
// are ErrUpstream (or one of the other sentinels) so the HTTP layer can show the
// reason verbatim: a host that is down, a host key that is not trusted yet, or a
// host with no stored credential are all expected, reportable situations.
func (s *Service) HostStats(ctx context.Context, alias string) (*HostStats, error) {
	if !s.cfg.Enabled {
		return nil, ErrDisabled
	}
	if err := validateAlias(alias); err != nil {
		return nil, err
	}
	cfg, err := s.load()
	if err != nil {
		return nil, err
	}
	if _, ok := cfg.Lookup(alias); !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownHost, alias)
	}

	// Credentials are only ever offered for a host whose key is already trusted,
	// exactly like a session launch: an unverified host must be confirmed by a
	// human in the terminal first, and that prompt cannot be answered here.
	info, statusErr := s.HostKeyStatus(ctx, alias)
	if statusErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstream, statusErr)
	}
	if !info.Known {
		return nil, fmt.Errorf("%w: kunci host %s belum dipercaya — buka sesi SSH sekali dan jawab yes", ErrUpstream, alias)
	}

	st := s.statsInstance()
	if cached, ok := st.cached(alias); ok {
		return cached, nil
	}
	// Probes for one alias are serialised: two viewers share the sample instead
	// of racing, and a slow or failing host cannot be probed twice at once.
	mu := st.aliasLock(alias)
	mu.Lock()
	defer mu.Unlock()
	// Re-check: the probe we waited behind may have refreshed the sample.
	if cached, ok := st.cached(alias); ok {
		return cached, nil
	}

	tokenEnv := s.askpassEnv(ctx, alias)
	if token := envToken(tokenEnv); token != "" {
		defer s.broker.Revoke(token)
	}

	counters, values, err := s.probeStats(ctx, alias, tokenEnv)
	if err != nil {
		return nil, err
	}

	stats := buildHostStats(values)
	stats.CollectedAt = counters.at

	prev := st.previous(alias)
	if prev.cpuTotal > 0 && counters.cpuTotal >= prev.cpuTotal && counters.netRx >= prev.netRx &&
		counters.at.Sub(prev.at) <= statsSampleTTL && counters.at.After(prev.at) {
		elapsed := counters.at.Sub(prev.at).Seconds()
		if elapsed > 0 {
			cpu := percentDelta(prev.cpuTotal, prev.cpuIdle, counters.cpuTotal, counters.cpuIdle)
			rx := float64(counters.netRx-prev.netRx) / elapsed
			tx := float64(counters.netTx-prev.netTx) / elapsed
			stats.CPUPercent = &cpu
			stats.NetRxBytesPerSec = &rx
			stats.NetTxBytesPerSec = &tx
		}
	}
	st.store(alias, counters, stats)
	return stats, nil
}

// probeStats runs the remote script and returns both the raw counters and the
// parsed values.
func (s *Service) probeStats(ctx context.Context, alias string, extraEnv []string) (remoteCounters, map[string]string, error) {
	out, err := s.runStats(ctx, alias, extraEnv)
	if err != nil {
		return remoteCounters{}, nil, err
	}
	values := parseRemoteStats(out)
	if len(values) == 0 {
		return remoteCounters{}, nil, fmt.Errorf("%w: host tidak mengembalikan metrik (butuh /proc dan awk)", ErrUpstream)
	}
	counters, err := countersFromValues(values)
	if err != nil {
		return remoteCounters{}, nil, err
	}
	return counters, values, nil
}

// runStats executes the probe. The script travels on stdin through "sh -s", so
// no quoting rule of the remote shell can break it and nothing but a fixed
// command name is passed on the command line.
func (s *Service) runStats(ctx context.Context, alias string, extraEnv []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, remoteStatsTimeout)
	defer cancel()

	batch := "yes" // nothing may prompt: a probe is not allowed to block
	if len(extraEnv) > 0 {
		batch = "no" // askpass can answer, so a password login may proceed
	}

	argv := []string{
		s.cfg.SSHBin,
		"-F", s.cfg.ConfigPath,
		"-o", "UserKnownHostsFile=" + s.knownHostsPath(),
		"-o", "BatchMode=" + batch,
		"-o", "NumberOfPasswordPrompts=1",
		"-o", "ConnectTimeout=8",
		"-o", "ConnectionAttempts=1",
		"-o", "LogLevel=ERROR",
	}
	if sock, ok := s.muxSocket(alias); ok {
		// Reuse one authenticated connection for every probe of this host: with
		// password authentication the alternative is a fresh login (and a fresh
		// sshd log line) on every poll.
		removeStaleSocket(sock)
		argv = append(argv,
			"-o", "ControlMaster=auto",
			"-o", "ControlPath="+sock,
			"-o", "ControlPersist="+statsMuxPersist,
		)
	}
	argv = append(argv, alias, "sh", "-s")

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(s.execEnv(), extraEnv...)
	cmd.Stdin = strings.NewReader(remoteStatsScript)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := firstLine(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("%w: %s", ErrUpstream, detail)
	}
	return stdout.String(), nil
}

// muxSocket returns the control socket path for alias. The directory is private
// to root: a control socket lets a local client reach the remote host without
// authenticating again, so it must never be reachable by another user.
func (s *Service) muxSocket(alias string) (string, bool) {
	dir := ""
	for _, candidate := range []string{
		filepath.Join("/run/dashboard", "sshmux"),
		filepath.Join(os.TempDir(), "dashboard-sshmux"),
	} {
		if err := os.MkdirAll(candidate, 0o700); err == nil {
			dir = candidate
			break
		}
	}
	if dir == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(alias))
	name := hex.EncodeToString(sum[:8]) + ".sock"
	return filepath.Join(dir, name), true
}

// removeStaleSocket deletes a leftover control socket. ssh refuses to reuse a
// socket whose owner has died, but it would otherwise keep printing a warning
// instead of multiplexing, which would silently turn every probe into a new
// authentication.
func removeStaleSocket(path string) {
	conn, err := net.DialTimeout("unix", path, 300*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return
	}
	if _, statErr := os.Lstat(path); statErr == nil {
		_ = os.Remove(path)
	}
}

// parseRemoteStats turns the probe output into a map, keeping only known keys.
func parseRemoteStats(out string) map[string]string {
	values := make(map[string]string, len(remoteKeys))
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || !remoteKeys[key] {
			continue
		}
		values[key] = strings.TrimSpace(value)
	}
	return values
}

// countersFromValues extracts the monotonic counters a rate needs.
func countersFromValues(values map[string]string) (remoteCounters, error) {
	c := remoteCounters{at: time.Now()}
	if _, ok := values["HOST"]; !ok {
		return c, fmt.Errorf("%w: host tidak mengembalikan hostname", ErrUpstream)
	}
	c.cpuTotal, _ = strconv.ParseUint(values["CPU_TOTAL"], 10, 64)
	c.cpuIdle, _ = strconv.ParseUint(values["CPU_IDLE"], 10, 64)
	c.netRx, _ = strconv.ParseUint(values["NET_RX"], 10, 64)
	c.netTx, _ = strconv.ParseUint(values["NET_TX"], 10, 64)
	return c, nil
}

// buildHostStats turns one probe into a sample (rates are filled in later).
func buildHostStats(values map[string]string) *HostStats {
	stats := &HostStats{Host: values["HOST"]}
	stats.UptimeSec, _ = strconv.ParseInt(values["UPTIME"], 10, 64)
	cores, _ := strconv.Atoi(values["CORES"])
	if cores > 0 {
		stats.CPUCount = cores
	}
	stats.Load1 = parseFloat(values["LOAD1"])
	stats.Load5 = parseFloat(values["LOAD5"])
	stats.Load15 = parseFloat(values["LOAD15"])

	memTotalKB, _ := strconv.ParseUint(values["MEM_TOTAL"], 10, 64)
	memAvailKB, _ := strconv.ParseUint(values["MEM_AVAIL"], 10, 64)
	swapTotalKB, _ := strconv.ParseUint(values["SWAP_TOTAL"], 10, 64)
	swapFreeKB, _ := strconv.ParseUint(values["SWAP_FREE"], 10, 64)
	stats.MemTotalBytes = memTotalKB * 1024
	if memTotalKB > memAvailKB {
		stats.MemUsedBytes = (memTotalKB - memAvailKB) * 1024
	}
	stats.SwapTotalBytes = swapTotalKB * 1024
	if swapTotalKB > swapFreeKB {
		stats.SwapUsedBytes = (swapTotalKB - swapFreeKB) * 1024
	}

	stats.DiskTotalBytes, _ = strconv.ParseUint(values["DISK_TOTAL"], 10, 64)
	stats.DiskUsedBytes, _ = strconv.ParseUint(values["DISK_USED"], 10, 64)
	return stats
}

func parseFloat(raw string) float64 {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return v
}

// percentDelta computes busy percentage between two /proc/stat samples.
func percentDelta(prevTotal, prevIdle, total, idle uint64) float64 {
	totalDelta := float64(total - prevTotal)
	if totalDelta <= 0 {
		return 0
	}
	idleDelta := float64(idle - prevIdle)
	busy := (totalDelta - idleDelta) / totalDelta * 100
	if busy < 0 {
		return 0
	}
	if busy > 100 {
		return 100
	}
	return busy
}
