package sshsvc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeStatsSSH installs a stand-in for the ssh client that answers a stats probe
// with counters that advance on every call, so the rate arithmetic can be
// verified without a network. It records the argv and the script it received on
// stdin next to itself.
//
// The "-G" queries the service uses to resolve an alias are passed through to the
// real client, so host-key resolution (and therefore the known_hosts lookup)
// behaves exactly as it does in production.
func fakeStatsSSH(t *testing.T, dir string) (bin, argvFile, stdinFile string) {
	t.Helper()
	realSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh tidak tersedia")
	}
	bin = filepath.Join(dir, "fake-ssh-stats")
	argvFile = filepath.Join(dir, "argv.txt")
	stdinFile = filepath.Join(dir, "stdin.txt")
	stateFile := filepath.Join(dir, "state.txt")

	// The service resolves an alias with "ssh -G" before anything else, so the
	// fake must answer that identically to the real client.
	script := `#!/bin/sh
case " $* " in
  *" -G "*)
    exec ` + realSSH + ` "$@"
    ;;
esac
echo "$@" >> ` + argvFile + `
cat >> ` + stdinFile + `
n=$(cat ` + stateFile + ` 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" > ` + stateFile + `
echo "HOST=remote.example.com"
echo "UPTIME=$((n * 10))"
echo "CORES=4"
echo "CPU_TOTAL=$((n * 1000))"
echo "CPU_IDLE=$((n * 500))"
echo "MEM_TOTAL=$((8 * 1024 * 1024))"
echo "MEM_AVAIL=$((2 * 1024 * 1024))"
echo "SWAP_TOTAL=$((1024 * 1024))"
echo "SWAP_FREE=$((512 * 1024))"
echo "NET_RX=$((n * 100000))"
echo "NET_TX=$((n * 50000))"
echo "LOAD1=0.50"
echo "LOAD5=0.40"
echo "LOAD15=0.30"
echo "DISK_TOTAL=107374182400"
echo "DISK_USED=42949672960"
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("tulis fake ssh: %v", err)
	}
	return bin, argvFile, stdinFile
}

// forgetSamples drops the cached sample of a host so the next call probes again
// (the cache is what keeps a 3s poll from becoming a probe storm).
func forgetSamples(t *testing.T, svc *Service, alias string) {
	t.Helper()
	st := svc.statsInstance()
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.last, alias)
}

func TestHostStatsProbeAndRates(t *testing.T) {
	ctx := context.Background()
	f := newHostKeyFixture(t)
	fakeSSH, argvFile, stdinFile := fakeStatsSSH(t, filepath.Dir(f.home))

	// Trust first: HostKeyStatus resolves the alias with the real ssh client, so
	// the fake may only take over once the host key is in known_hosts.
	if _, err := f.svc.TrustHostKey(ctx, "web1"); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}
	f.svc.cfg.SSHBin = fakeSSH

	// First probe: counters only, so the rate fields must be absent instead of
	// reporting a made-up number.
	first, err := f.svc.HostStats(ctx, "web1")
	if err != nil {
		t.Fatalf("HostStats pertama: %v", err)
	}
	if first.Host != "remote.example.com" {
		t.Errorf("host = %q", first.Host)
	}
	if first.CPUPercent != nil || first.NetRxBytesPerSec != nil {
		t.Errorf("sampel pertama seharusnya belum punya rate: %+v", first)
	}
	if first.CPUCount != 4 {
		t.Errorf("cpuCount = %d, want 4", first.CPUCount)
	}
	if first.MemTotalBytes != 8*1024*1024*1024 {
		t.Errorf("memTotal = %d", first.MemTotalBytes)
	}
	if first.MemUsedBytes != 6*1024*1024*1024 {
		t.Errorf("memUsed = %d, want 6 GiB", first.MemUsedBytes)
	}
	if first.DiskTotalBytes != 100*1024*1024*1024 || first.DiskUsedBytes != 40*1024*1024*1024 {
		t.Errorf("disk = %d/%d", first.DiskUsedBytes, first.DiskTotalBytes)
	}
	if first.Load1 != 0.5 {
		t.Errorf("load1 = %v", first.Load1)
	}

	// Second probe: 500 of 1000 ticks busy is exactly 50%.
	forgetSamples(t, f.svc, "web1")
	second, err := f.svc.HostStats(ctx, "web1")
	if err != nil {
		t.Fatalf("HostStats kedua: %v", err)
	}
	if second.CPUPercent == nil {
		t.Fatal("sampel kedua harus punya CPU")
	}
	if diff := *second.CPUPercent - 50; diff > 0.01 || diff < -0.01 {
		t.Errorf("cpu = %v, want 50", *second.CPUPercent)
	}
	if second.NetRxBytesPerSec == nil || *second.NetRxBytesPerSec <= 0 {
		t.Errorf("rx rate = %v", second.NetRxBytesPerSec)
	}
	if second.NetTxBytesPerSec == nil || *second.NetTxBytesPerSec <= 0 {
		t.Errorf("tx rate = %v", second.NetTxBytesPerSec)
	}

	// The probe must never pass a shell string: the script travels on stdin and
	// the command line only carries our fixed options plus "sh -s".
	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("baca argv: %v", err)
	}
	got := strings.TrimSpace(string(argv))
	if !strings.Contains(got, "-F ") || !strings.Contains(got, "web1 sh -s") {
		t.Errorf("argv tidak seperti yang diharapkan: %s", got)
	}
	if strings.Contains(got, "HOST=") {
		t.Errorf("skrip bocor ke argv: %s", got)
	}
	if !strings.Contains(got, "ControlMaster=auto") || !strings.Contains(got, "UserKnownHostsFile=") {
		t.Errorf("opsi mux/known_hosts hilang: %s", got)
	}
	stdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("baca stdin: %v", err)
	}
	if !strings.Contains(string(stdin), `printf "HOST=%s\n"`) {
		t.Errorf("skrip tidak dikirim lewat stdin: %s", string(stdin))
	}
}

func TestHostStatsRequiresTrustedHostKey(t *testing.T) {
	ctx := context.Background()
	f := newHostKeyFixture(t)
	fakeSSH, argvFile, _ := fakeStatsSSH(t, filepath.Dir(f.home))
	f.svc.cfg.SSHBin = fakeSSH

	_, err := f.svc.HostStats(ctx, "web1")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
	if !strings.Contains(err.Error(), "belum dipercaya") {
		t.Errorf("pesan tidak menjelaskan sebabnya: %v", err)
	}
	if _, statErr := os.Stat(argvFile); statErr == nil {
		t.Error("ssh dijalankan padahal kunci host belum dipercaya")
	}
}

func TestHostStatsRejectsBadAliasAndUnknownHost(t *testing.T) {
	ctx := context.Background()
	f := newHostKeyFixture(t)

	if _, err := f.svc.HostStats(ctx, "-oProxyCommand=boom"); !errors.Is(err, ErrInvalidAlias) {
		t.Errorf("alias jahat: err = %v", err)
	}
	if _, err := f.svc.HostStats(ctx, "ghost"); !errors.Is(err, ErrUnknownHost) {
		t.Errorf("host tak dikenal: err = %v", err)
	}
}

func TestHostStatsCachesSample(t *testing.T) {
	ctx := context.Background()
	f := newHostKeyFixture(t)
	fakeSSH, argvFile, _ := fakeStatsSSH(t, filepath.Dir(f.home))
	if _, err := f.svc.TrustHostKey(ctx, "web1"); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}
	f.svc.cfg.SSHBin = fakeSSH

	if _, err := f.svc.HostStats(ctx, "web1"); err != nil {
		t.Fatalf("HostStats: %v", err)
	}
	if _, err := f.svc.HostStats(ctx, "web1"); err != nil {
		t.Fatalf("HostStats kedua: %v", err)
	}
	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("baca argv: %v", err)
	}
	if calls := strings.Count(strings.TrimSpace(string(argv)), "\n") + 1; calls != 1 {
		t.Errorf("probe dijalankan %d kali, want 1 (cache 2s)", calls)
	}
}

func TestParseRemoteStatsIgnoresNoise(t *testing.T) {
	out := "Welcome to the server\r\n" +
		"HOST=web9\r\n" +
		"NOT_A_KEY=1\n" +
		"CPU_TOTAL=42\n" +
		"  MEM_TOTAL=99  \n" +
		"\n"
	values := parseRemoteStats(out)
	if values["HOST"] != "web9" || values["CPU_TOTAL"] != "42" || values["MEM_TOTAL"] != "99" {
		t.Errorf("nilai tidak terbaca: %+v", values)
	}
	if _, ok := values["NOT_A_KEY"]; ok {
		t.Errorf("kunci asing ikut tersimpan: %+v", values)
	}
	if _, ok := values["Welcome to the server"]; ok {
		t.Errorf("banner ikut tersimpan: %+v", values)
	}
}

func TestPercentDelta(t *testing.T) {
	// 500 of 1000 ticks busy.
	if got := percentDelta(1000, 500, 2000, 1000); got != 50 {
		t.Errorf("percentDelta = %v, want 50", got)
	}
	// A counter reset must not produce a negative or absurd value.
	if got := percentDelta(5000, 4000, 10, 5); got < 0 || got > 100 {
		t.Errorf("percentDelta reset = %v", got)
	}
	if got := percentDelta(10, 5, 10, 5); got != 0 {
		t.Errorf("percentDelta tanpa perubahan = %v", got)
	}
}

func TestHostStatsSerialisesProbesAndKeepsOneSample(t *testing.T) {
	// Two callers asking at the same time must share a single probe, and the
	// cache must keep serving that sample briefly afterwards.
	ctx := context.Background()
	f := newHostKeyFixture(t)
	fakeSSH, argvFile, _ := fakeStatsSSH(t, filepath.Dir(f.home))
	if _, err := f.svc.TrustHostKey(ctx, "web1"); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}
	f.svc.cfg.SSHBin = fakeSSH

	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := f.svc.HostStats(ctx, "web1")
			done <- err
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatalf("HostStats bersamaan: %v", err)
		}
	}
	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("baca argv: %v", err)
	}
	if calls := strings.Count(strings.TrimSpace(string(argv)), "\n") + 1; calls != 1 {
		t.Errorf("probe dijalankan %d kali untuk dua pemanggil, want 1", calls)
	}
}
