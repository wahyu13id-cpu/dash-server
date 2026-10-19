// Package sysops exposes host operations to the dashboard: systemd service
// control, process inspection and signalling, log reading and package-update
// information.
//
// Two rules keep this package safe:
//
//  1. External commands always run through exec with a fixed argument vector
//     (never through a shell), so request data can never turn into syntax.
//  2. Anything coming from a request (unit name, signal, pid, log path) is
//     validated against an allow-list before it reaches the operating system.
//
// The package has no HTTP knowledge: the API layer owns transport concerns and
// this package owns the operational rules.
package sysops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrDisabled is returned when the operations feature is switched off.
var ErrDisabled = errors.New("operasi sistem dinonaktifkan")

// ErrUnsupported is returned when the host cannot provide the requested data
// (for example, no systemd or no package manager).
var ErrUnsupported = errors.New("tidak didukung oleh sistem ini")

// ErrInvalidInput marks rejected values (bad unit name, pid, signal, path).
var ErrInvalidInput = errors.New("nilai tidak valid")

// Config configures the operations service.
type Config struct {
	// Enabled turns the whole feature on or off.
	Enabled bool
	// SystemctlBin and JournalctlBin are the executables used for service
	// control and log reads.
	SystemctlBin  string
	JournalctlBin string
	// LogDir is the only directory the log reader may read from.
	LogDir string
	// ProcRoot is the procfs mount point (normally "/proc").
	ProcRoot string
	// RuntimeDir is the directory that only exists while systemd is PID 1
	// (normally "/run/systemd/system"). It is a seam for tests.
	RuntimeDir string
	// PackageManager forces a package manager ("apt", "dnf" or "yum"). Empty
	// means "detect what is installed".
	PackageManager string
	// Timeout bounds every short external command.
	Timeout time.Duration
}

// Service implements the host operations.
type Service struct {
	cfg     Config
	execEnv []string

	// Sampling cache for CPU percentages (needs two readings).
	mu        sync.Mutex
	prevProc  *procSample
	userNames map[uint32]string

	// Background jobs (package refresh/upgrade).
	jobsMu sync.Mutex
	jobs   map[string]*Job
	jobSeq uint64

	// Package update cache (the probe shells out, so results are reused).
	pkgMu    sync.Mutex
	pkgCache *PackageStatus
}

// New builds the operations service. binDir is the directory holding the
// dashboard binary (unused for now, kept for symmetry with other packages).
func New(cfg Config) *Service {
	if cfg.SystemctlBin == "" {
		cfg.SystemctlBin = "systemctl"
	}
	if cfg.JournalctlBin == "" {
		cfg.JournalctlBin = "journalctl"
	}
	if cfg.LogDir == "" {
		cfg.LogDir = "/var/log"
	}
	if cfg.ProcRoot == "" {
		cfg.ProcRoot = "/proc"
	}
	if cfg.RuntimeDir == "" {
		cfg.RuntimeDir = "/run/systemd/system"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	return &Service{
		cfg:       cfg,
		execEnv:   execEnv(),
		userNames: make(map[uint32]string),
		jobs:      make(map[string]*Job),
	}
}

// Enabled reports whether the feature is switched on in configuration.
func (s *Service) Enabled() bool { return s != nil && s.cfg.Enabled }

// Availability reports which capabilities this host offers, so the UI can hide
// what cannot work instead of showing broken pages.
type Availability struct {
	Systemd        bool   `json:"systemd"`
	Journal        bool   `json:"journal"`
	Processes      bool   `json:"processes"`
	PackageManager string `json:"packageManager"`
	RebootRequired bool   `json:"rebootRequired"`
	LogDir         string `json:"logDir"`
}

// Availability probes the host for systemd, journald and a package manager.
func (s *Service) Availability(ctx context.Context) Availability {
	av := Availability{}
	if !s.Enabled() {
		return av
	}
	if s.hasSystemd() {
		av.Systemd = true
	}
	if _, err := exec.LookPath(s.cfg.JournalctlBin); err == nil {
		av.Journal = true
	}
	if _, err := os.Stat(filepath.Join(s.cfg.ProcRoot, "stat")); err == nil {
		av.Processes = true
	}
	av.PackageManager = s.packageManager()
	av.RebootRequired = s.RebootRequired()
	av.LogDir = s.cfg.LogDir
	return av
}

// hasSystemd reports whether systemd is both installed and running (PID 1).
func (s *Service) hasSystemd() bool {
	if _, err := exec.LookPath(s.cfg.SystemctlBin); err != nil {
		return false
	}
	info, err := os.Stat(s.cfg.RuntimeDir)
	return err == nil && info.IsDir()
}

// packageManager detects the supported manager: "apt", "dnf" or "".
func (s *Service) packageManager() string {
	if cfg := s.cfg.PackageManager; cfg != "" {
		return cfg
	}
	if _, err := exec.LookPath("apt-get"); err == nil {
		return "apt"
	}
	if _, err := exec.LookPath("dnf"); err == nil {
		return "dnf"
	}
	if _, err := exec.LookPath("yum"); err == nil {
		return "yum"
	}
	return ""
}

// --- command helpers -------------------------------------------------------

// result is the outcome of one external command.
type result struct {
	Stdout string
	Stderr string
	Err    error
}

// combined returns stdout plus stderr, which is what users expect to read for
// administrative commands.
func (r result) combined() string {
	switch {
	case r.Stdout == "":
		return r.Stderr
	case r.Stderr == "":
		return r.Stdout
	default:
		return r.Stdout + r.Stderr
	}
}

// run executes bin with args, capturing both streams. The command never sees a
// shell: args are passed verbatim to the kernel.
func (s *Service) run(ctx context.Context, bin string, args ...string) result {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = s.execEnv
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("perintah melebihi batas waktu %s", s.cfg.Timeout)
	}
	return result{Stdout: out.String(), Stderr: errBuf.String(), Err: err}
}

// execEnv is a minimal, predictable environment for administrative commands.
func execEnv() []string {
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C",
		"LC_ALL=C",
		"DEBIAN_FRONTEND=noninteractive",
		"SYSTEMD_COLORS=0",
		"SYSTEMD_PAGER=",
		"PAGER=cat",
	}
	if v := os.Getenv("HOME"); v != "" {
		env = append(env, "HOME="+v)
	}
	return env
}

// --- jobs -------------------------------------------------------------------

// Job is a long-running administrative command (package refresh or upgrade)
// whose output is streamed into a buffer the UI can poll.
type Job struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Command    string     `json:"command"`
	Running    bool       `json:"running"`
	ExitCode   int        `json:"exitCode"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	// Output holds the last maxJobLines lines of combined output.
	Output    []string `json:"output"`
	Truncated bool     `json:"truncated"`
	Error     string   `json:"error,omitempty"`
}

const (
	maxJobLines = 4000
	maxJobs     = 20
)

// startJob runs a command in the background and returns the job record.
// The request context is deliberately not used: the command must survive the
// HTTP response.
func (s *Service) startJob(kind, command string, timeout time.Duration, bin string, args ...string) *Job {
	job := &Job{
		ID:        s.nextJobID(),
		Kind:      kind,
		Command:   command,
		Running:   true,
		StartedAt: time.Now(),
	}
	s.storeJob(job)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = s.execEnv

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			s.finishJob(job.ID, 1, err.Error())
			return
		}
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			s.finishJob(job.ID, 1, err.Error())
			return
		}

		buf := make([]byte, 4096)
		var pending string
		for {
			n, readErr := stdout.Read(buf)
			if n > 0 {
				pending += string(buf[:n])
				for {
					line, rest, ok := strings.Cut(pending, "\n")
					if !ok {
						break
					}
					s.appendJobLine(job.ID, strings.TrimRight(line, "\r"))
					pending = rest
				}
			}
			if readErr != nil {
				break
			}
		}
		if pending != "" {
			s.appendJobLine(job.ID, strings.TrimRight(pending, "\r"))
		}

		waitErr := cmd.Wait()
		code := 0
		msg := ""
		if waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				code = exitErr.ExitCode()
			} else {
				code = 1
			}
			msg = waitErr.Error()
		}
		if ctx.Err() == context.DeadlineExceeded {
			code = 124
			msg = fmt.Sprintf("perintah melebihi batas waktu %s", timeout)
		}
		s.finishJob(job.ID, code, msg)
	}()

	return job
}

func (s *Service) nextJobID() string {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	s.jobSeq++
	return "job-" + strconv.FormatUint(s.jobSeq, 10) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func (s *Service) storeJob(job *Job) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	s.jobs[job.ID] = job
	// Drop the oldest finished jobs beyond the cap.
	if len(s.jobs) > maxJobs {
		var oldest *Job
		for _, j := range s.jobs {
			if j.Running {
				continue
			}
			if oldest == nil || j.StartedAt.Before(oldest.StartedAt) {
				oldest = j
			}
		}
		if oldest != nil {
			delete(s.jobs, oldest.ID)
		}
	}
}

func (s *Service) appendJobLine(id, line string) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return
	}
	if len(job.Output) >= maxJobLines {
		// Keep the most recent lines: drop the head and remember the loss.
		job.Output = append(job.Output[:0], job.Output[1:]...)
		job.Truncated = true
	}
	job.Output = append(job.Output, line)
}

func (s *Service) finishJob(id string, code int, errMsg string) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return
	}
	now := time.Now()
	job.Running = false
	job.ExitCode = code
	job.Error = errMsg
	job.FinishedAt = &now
}

// Job returns a copy of a stored job.
func (s *Service) Job(id string) (*Job, bool) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return nil, false
	}
	clone := *job
	clone.Output = append([]string(nil), job.Output...)
	return &clone, true
}

// --- small helpers ----------------------------------------------------------

// cleanPath returns the absolute, symlink-resolved form of p.
func cleanPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("%w: path kosong", ErrInvalidInput)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Wrap both so callers can tell "not found" from "rejected".
		return "", fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return resolved, nil
}

// within reports whether path is root itself or lives inside root.
func within(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(os.PathSeparator))
}

var unitNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.@\\-]{0,127}\.service$`)

// ValidUnitName reports whether name is a syntactically valid .service unit.
// The ".service" suffix is enforced on purpose: the dashboard manages services,
// not mounts, sockets or timers.
func ValidUnitName(name string) bool {
	return unitNameRe.MatchString(name)
}

// --- processes: shared types -------------------------------------------------
//
// The types live here (not in the platform file) so a build for a system
// without procfs still compiles and simply reports the feature as unsupported.

// ProcessInfo is one process row.
type ProcessInfo struct {
	PID         int       `json:"pid"`
	PPID        int       `json:"ppid"`
	UID         uint32    `json:"uid"`
	User        string    `json:"user"`
	State       string    `json:"state"`
	Nice        int       `json:"nice"`
	CPUPercent  float64   `json:"cpuPercent"`
	MemPercent  float64   `json:"memPercent"`
	RSSBytes    uint64    `json:"rssBytes"`
	VMSizeBytes uint64    `json:"vmSizeBytes"`
	Threads     int       `json:"threads"`
	StartedAt   time.Time `json:"startedAt"`
	ElapsedSec  int64     `json:"elapsedSec"`
	Name        string    `json:"name"`
	Command     string    `json:"command"`
}

// ProcessList is the payload of the processes endpoint.
type ProcessList struct {
	Processes   []ProcessInfo `json:"processes"`
	Total       int           `json:"total"`
	CPUCores    int           `json:"cpuCores"`
	MemTotal    uint64        `json:"memTotalBytes"`
	CollectedAt time.Time     `json:"collectedAt"`
}

// Sort keys accepted by ListProcesses.
const (
	ProcSortCPU  = "cpu"
	ProcSortMem  = "mem"
	ProcSortPID  = "pid"
	ProcSortName = "name"
	ProcSortTime = "time"
)

// procSample is the previous reading used to compute CPU deltas.
type procSample struct {
	at         time.Time
	totalTicks uint64
	ticks      map[int]uint64
}

// procStat is the subset of /proc/<pid>/stat the dashboard needs.
type procStat struct {
	pid       int
	ppid      int
	name      string
	state     string
	nice      int
	threads   int
	utime     uint64
	stime     uint64
	starttime uint64
	rssPages  uint64
	vsize     uint64
}

// clockTicks is Linux USER_HZ, the unit of the starttime field in procfs.
func clockTicks() int64 { return 100 }

// round1 rounds to one decimal, which is enough for percentages.
func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

// userName resolves a uid to a login name, caching the result (the lookup hits
// /etc/passwd, which would otherwise be read once per process).
func (s *Service) userName(uid uint32) string {
	s.mu.Lock()
	if name, ok := s.userNames[uid]; ok {
		s.mu.Unlock()
		return name
	}
	s.mu.Unlock()

	name := strconv.FormatUint(uint64(uid), 10)
	if u, err := user.LookupId(name); err == nil {
		name = u.Username
	}

	s.mu.Lock()
	s.userNames[uid] = name
	s.mu.Unlock()
	return name
}
