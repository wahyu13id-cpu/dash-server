package sysops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidUnitName(t *testing.T) {
	valid := []string{
		"nginx.service",
		"ssh.service",
		"getty@tty1.service",
		"systemd-resolved.service",
		"a-b_c.d.service",
		"user@1000.service",
	}
	for _, name := range valid {
		if !ValidUnitName(name) {
			t.Errorf("ValidUnitName(%q) = false, want true", name)
		}
	}

	invalid := []string{
		"",
		"nginx",
		"nginx.socket",
		"nginx.target",
		"nginx.service/x",
		"../etc/passwd",
		"nginx service",
		"nginx.service; rm -rf /",
		"$(id).service",
		"-nginx.service",
		strings.Repeat("a", 200) + ".service",
	}
	for _, name := range invalid {
		if ValidUnitName(name) {
			t.Errorf("ValidUnitName(%q) = true, want false", name)
		}
	}
}

func TestNormalizeSince(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"1h":        "-1h",
		"30m":       "-30m",
		"7d":        "-7d",
		"boot":      "boot",
		"today":     "today",
		"yesterday": "yesterday",
	}
	for in, want := range cases {
		got, ok := normalizeSince(in)
		if !ok || got != want {
			t.Errorf("normalizeSince(%q) = (%q, %v), want (%q, true)", in, got, ok, want)
		}
	}
	for _, bad := range []string{"2024-01-01", "-1h", "1 hour ago", "5x", "99999h"} {
		if _, ok := normalizeSince(bad); ok {
			t.Errorf("normalizeSince(%q) accepted an unsupported window", bad)
		}
	}
}

func TestParseJournalLine(t *testing.T) {
	line := `{"__CURSOR":"s=abc;i=1f2;b=3","__REALTIME_TIMESTAMP":"1699999999123456",` +
		`"PRIORITY":"3","_SYSTEMD_UNIT":"nginx.service","SYSLOG_IDENTIFIER":"nginx",` +
		`"_PID":"4242","MESSAGE":"worker process started"}`
	entry, ok := parseJournalLine(line)
	if !ok {
		t.Fatal("parseJournalLine returned ok=false for a valid line")
	}
	if entry.Message != "worker process started" {
		t.Errorf("message = %q", entry.Message)
	}
	if entry.Unit != "nginx.service" {
		t.Errorf("unit = %q", entry.Unit)
	}
	if entry.Priority != 3 {
		t.Errorf("priority = %d", entry.Priority)
	}
	if entry.PID != 4242 {
		t.Errorf("pid = %d", entry.PID)
	}
	if entry.Time.IsZero() {
		t.Error("time was not parsed")
	}

	// Byte-array MESSAGE values come from journald for binary payloads.
	binary := `{"__REALTIME_TIMESTAMP":"1699999999123456","MESSAGE":[104,105]}`
	entry, ok = parseJournalLine(binary)
	if !ok || entry.Message != "hi" {
		t.Errorf("byte-array message = (%q, %v), want (\"hi\", true)", entry.Message, ok)
	}

	if _, ok := parseJournalLine("not json"); ok {
		t.Error("parseJournalLine accepted invalid JSON")
	}
}

func TestAptInstRegex(t *testing.T) {
	line := "Inst libc6 [2.36-9+deb12u4] (2.36-9+deb12u7 Debian-Security:12/stable [amd64])"
	m := aptInstRe.FindStringSubmatch(line)
	if m == nil {
		t.Fatal("regex did not match an Inst line")
	}
	if m[1] != "libc6" || m[2] != "2.36-9+deb12u4" {
		t.Errorf("captured %v", m)
	}
	field := strings.TrimSpace(m[3])
	if !strings.Contains(strings.ToLower(field), "security") {
		t.Errorf("security origin not detected in %q", field)
	}

	// New installs have no [current] version.
	m = aptInstRe.FindStringSubmatch("Inst curl (8.5.0-2 Debian:12/stable [amd64])")
	if m == nil || m[1] != "curl" || m[2] != "" {
		t.Errorf("install line parsed as %v", m)
	}

	if aptInstRe.MatchString("Conf libc6 (2.36)") {
		t.Error("regex matched a Conf line")
	}
}

func TestReadLogFileRejectsOutsideLogDir(t *testing.T) {
	dir := t.TempDir()
	svc := New(Config{Enabled: true, LogDir: dir, ProcRoot: "/proc"})

	inside := filepath.Join(dir, "app.log")
	if err := os.WriteFile(inside, []byte("line one\nline two\nline three\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	got, err := svc.ReadLogFile(context.Background(), inside, 2, "")
	if err != nil {
		t.Fatalf("ReadLogFile(inside) = %v", err)
	}
	if len(got.Lines) != 2 || got.Lines[1] != "line three" {
		t.Errorf("lines = %v, want the last two", got.Lines)
	}
	if !got.Truncated {
		t.Error("expected truncated=true when older lines are dropped")
	}

	if _, err := svc.ReadLogFile(context.Background(), "/etc/passwd", 10, ""); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("ReadLogFile(/etc/passwd) = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.ReadLogFile(context.Background(), filepath.Join(dir, "missing.log"), 10, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadLogFile(missing) = %v, want ErrNotFound", err)
	}
}

func TestReadLogFileSearch(t *testing.T) {
	dir := t.TempDir()
	svc := New(Config{Enabled: true, LogDir: dir, ProcRoot: "/proc"})
	path := filepath.Join(dir, "auth.log")
	body := "Accepted password for root\nFailed password for admin\naccepted key\n"
	if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ReadLogFile(context.Background(), path, 100, "ACCEPTED")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 2 {
		t.Fatalf("lines = %v, want the two matching lines", got.Lines)
	}
}

func TestDisabledServiceRefusesWork(t *testing.T) {
	svc := New(Config{Enabled: false})
	ctx := context.Background()

	if _, err := svc.ListServices(ctx); !errors.Is(err, ErrDisabled) {
		t.Errorf("ListServices = %v, want ErrDisabled", err)
	}
	if _, err := svc.ListProcesses(ctx, "", "", "", 0); !errors.Is(err, ErrDisabled) {
		t.Errorf("ListProcesses = %v, want ErrDisabled", err)
	}
	if _, err := svc.Journal(ctx, JournalQuery{}); !errors.Is(err, ErrDisabled) {
		t.Errorf("Journal = %v, want ErrDisabled", err)
	}
	if err := svc.SignalProcess(123, "TERM"); !errors.Is(err, ErrDisabled) {
		t.Errorf("SignalProcess = %v, want ErrDisabled", err)
	}
	if _, err := svc.PackageStatus(ctx, false); !errors.Is(err, ErrDisabled) {
		t.Errorf("PackageStatus = %v, want ErrDisabled", err)
	}
}

func TestControlServiceValidatesInput(t *testing.T) {
	svc := New(Config{Enabled: true})
	ctx := context.Background()

	if _, err := svc.ControlService(ctx, "../evil", "start"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bad unit = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.ControlService(ctx, "nginx.service", "rm -rf"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bad action = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.ServiceDetail(ctx, "nginx.socket"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bad detail unit = %v, want ErrInvalidInput", err)
	}
}

func TestJournalQueryValidation(t *testing.T) {
	svc := New(Config{Enabled: true})
	ctx := context.Background()
	if _, err := svc.Journal(ctx, JournalQuery{Unit: "bad unit"}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bad unit = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.Journal(ctx, JournalQuery{Priority: "loud"}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bad priority = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.Journal(ctx, JournalQuery{Since: "since forever"}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bad since = %v, want ErrInvalidInput", err)
	}
}

func TestWithin(t *testing.T) {
	if !within("/var/log", "/var/log/syslog") {
		t.Error("file directly inside root was rejected")
	}
	if !within("/var/log", "/var/log") {
		t.Error("root itself was rejected")
	}
	if within("/var/log", "/var/logs/syslog") {
		t.Error("sibling directory with a shared prefix was accepted")
	}
	if within("/var/log", "/etc/passwd") {
		t.Error("outside path was accepted")
	}
}

func TestJobLifecycle(t *testing.T) {
	svc := New(Config{Enabled: true, Timeout: 5 * time.Second})
	job := svc.startJob("test", "echo hi", 5*time.Second, "/bin/echo", "hi")
	if job == nil {
		t.Fatal("startJob returned nil")
	}

	// Poll until the job finishes; /bin/echo returns immediately.
	for attempts := 0; attempts < 500; attempts++ {
		got, ok := svc.Job(job.ID)
		if !ok {
			t.Fatalf("job %s disappeared", job.ID)
		}
		if !got.Running {
			if got.ExitCode != 0 {
				t.Errorf("exit code = %d", got.ExitCode)
			}
			if len(got.Output) == 0 || got.Output[0] != "hi" {
				t.Errorf("output = %v", got.Output)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job never finished")
}

func TestJobOutputIsCapped(t *testing.T) {
	svc := New(Config{Enabled: true})
	svc.jobsMu.Lock()
	svc.jobs["job-x"] = &Job{ID: "job-x"}
	svc.jobsMu.Unlock()

	for i := 0; i < maxJobLines+10; i++ {
		svc.appendJobLine("job-x", "line")
	}
	got, ok := svc.Job("job-x")
	if !ok {
		t.Fatal("job disappeared")
	}
	if len(got.Output) > maxJobLines {
		t.Errorf("output grew to %d lines, cap is %d", len(got.Output), maxJobLines)
	}
	if !got.Truncated {
		t.Error("expected truncated=true after exceeding the cap")
	}
}

func TestRebootRequired(t *testing.T) {
	svc := New(Config{Enabled: true})
	// The result depends on the host; the call must simply not panic and must
	// agree with the presence of the marker file.
	_, err := os.Stat("/var/run/reboot-required")
	want := err == nil
	if err != nil {
		if _, err2 := os.Stat("/run/reboot-required"); err2 == nil {
			want = true
		}
	}
	if got := svc.RebootRequired(); got != want {
		t.Errorf("RebootRequired() = %v, want %v", got, want)
	}
}
