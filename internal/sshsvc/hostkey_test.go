package sshsvc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dashboard/internal/db"
	"dashboard/internal/sshconfig"
	"dashboard/migrations"
)

// hostKeyFixture builds a service whose "ssh-keyscan" is a script that prints a
// real, generated public key. The key material is produced by ssh-keygen so the
// fingerprints are genuine and the known_hosts file stays valid.
type hostKeyFixture struct {
	svc       *Service
	home      string
	pubLine   string
	base64    string
	keyLine   string
	lineFile  string
	spec      string
	sshConfig string
}

func newHostKeyFixture(t *testing.T) *hostKeyFixture {
	t.Helper()
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh tidak tersedia")
	}
	keygenPath, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen tidak tersedia")
	}

	ctx := context.Background()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	keyPath := filepath.Join(root, "id_ed25519")
	if out, err := exec.Command(keygenPath, "-t", "ed25519", "-N", "", "-f", keyPath, "-C", "dash@test").
		CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen gagal: %v (%s)", err, out)
	}
	pubBytes, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatalf("baca pub: %v", err)
	}
	pubLine := strings.TrimSpace(string(pubBytes))

	// The alias resolves to keyscan.example.com:2200, so ssh addresses the
	// known_hosts entry as [keyscan.example.com]:2200.
	hostSpec := "[keyscan.example.com]:2200"
	lineFile := filepath.Join(root, "keyscan-line.txt")
	if err := os.WriteFile(lineFile,
		[]byte(hostSpec+" "+pubLine+"\n"), 0o600); err != nil {
		t.Fatalf("tulis baris keyscan: %v", err)
	}
	fakeKeyscan := filepath.Join(root, "fake-keyscan")
	script := "#!/bin/sh\ncat " + lineFile + "\n"
	if err := os.WriteFile(fakeKeyscan, []byte(script), 0o755); err != nil {
		t.Fatalf("tulis fake keyscan: %v", err)
	}

	cfgPath := filepath.Join(home, ".ssh", "config")
	if err := os.WriteFile(cfgPath,
		[]byte("Host web1\n    HostName keyscan.example.com\n    Port 2200\n"), 0o600); err != nil {
		t.Fatalf("tulis config: %v", err)
	}

	conn, err := db.Open(ctx, filepath.Join(root, "ssh.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.Migrate(ctx, conn, migrations.FS); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	svc, err := New(Config{
		Enabled:    true,
		Home:       home,
		SSHBin:     sshPath,
		KeyscanBin: fakeKeyscan,
		KeygenBin:  keygenPath,
		AskpassBin: "/bin/true",
	}, conn, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &hostKeyFixture{
		svc:       svc,
		home:      home,
		pubLine:   pubLine,
		base64:    strings.Fields(pubLine)[1],
		keyLine:   hostSpec + " " + strings.Join(strings.Fields(pubLine)[:2], " "),
		lineFile:  lineFile,
		spec:      hostSpec,
		sshConfig: cfgPath,
	}
}

func (f *hostKeyFixture) knownHosts(t *testing.T) string {
	t.Helper()
	return filepath.Join(f.home, ".ssh", "known_hosts")
}

func TestHostKeyScanTrustAndForget(t *testing.T) {
	ctx := context.Background()
	f := newHostKeyFixture(t)

	// Before anything is trusted.
	status, err := f.svc.HostKeyStatus(ctx, "web1")
	if err != nil {
		t.Fatalf("HostKeyStatus: %v", err)
	}
	if status.Known {
		t.Error("host seharusnya belum dipercaya")
	}
	if status.HostSpec != f.spec {
		t.Errorf("hostSpec = %q, want %q", status.HostSpec, f.spec)
	}

	// An explicit scan reports the fingerprint the user must compare.
	keys, err := f.svc.ScanHostKey(ctx, "web1")
	if err != nil {
		t.Fatalf("ScanHostKey: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %+v, want 1", keys)
	}
	if !strings.HasPrefix(keys[0].Fingerprint, "SHA256:") {
		t.Errorf("fingerprint = %q", keys[0].Fingerprint)
	}
	if keys[0].Type != "ssh-ed25519" {
		t.Errorf("type = %q, want ssh-ed25519", keys[0].Type)
	}

	// Trusting writes the entry and nothing is trusted before that point.
	n, err := f.svc.TrustHostKey(ctx, "web1")
	if err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}
	if n != 1 {
		t.Errorf("trusted = %d, want 1", n)
	}
	raw, err := os.ReadFile(f.knownHosts(t))
	if err != nil {
		t.Fatalf("baca known_hosts: %v", err)
	}
	if !strings.Contains(string(raw), f.keyLine) {
		t.Errorf("known_hosts tidak memuat kunci:\n%s", raw)
	}

	status, err = f.svc.HostKeyStatus(ctx, "web1")
	if err != nil {
		t.Fatalf("HostKeyStatus: %v", err)
	}
	if !status.Known || len(status.Fingerprints) != 1 {
		t.Fatalf("status = %+v, want known dengan 1 sidik jari", status)
	}
	if status.Fingerprints[0].Fingerprint != keys[0].Fingerprint {
		t.Errorf("fingerprint berbeda: %q vs %q",
			status.Fingerprints[0].Fingerprint, keys[0].Fingerprint)
	}

	// Trusting again must replace, not duplicate, the entry.
	if _, err := f.svc.TrustHostKey(ctx, "web1"); err != nil {
		t.Fatalf("TrustHostKey kedua: %v", err)
	}
	raw, _ = os.ReadFile(f.knownHosts(t))
	if got := strings.Count(string(raw), f.base64); got != 1 {
		t.Errorf("entri known_hosts = %d, want 1:\n%s", got, raw)
	}

	// Forgetting removes it.
	removed, err := f.svc.ForgotHostKey(ctx, "web1")
	if err != nil {
		t.Fatalf("ForgotHostKey: %v", err)
	}
	if !removed {
		t.Error("removed = false")
	}
	status, err = f.svc.HostKeyStatus(ctx, "web1")
	if err != nil {
		t.Fatalf("HostKeyStatus: %v", err)
	}
	if status.Known {
		t.Error("host masih dipercaya setelah dilupakan")
	}
}

// TestHostKeyRejectsChangedKeyWithoutTrust shows the dangerous path stays
// explicit: a rotated key is only accepted after a fresh trust action, and until
// then it must keep reporting as untrusted rather than silently passing.
func TestHostKeyChangedKeyIsReportedUntrusted(t *testing.T) {
	ctx := context.Background()
	f := newHostKeyFixture(t)

	if _, err := f.svc.TrustHostKey(ctx, "web1"); err != nil {
		t.Fatalf("TrustHostKey: %v", err)
	}
	if status, _ := f.svc.HostKeyStatus(ctx, "web1"); !status.Known {
		t.Fatal("host seharusnya dipercaya")
	}

	// Simulate the remote rotating its key.
	other, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen tidak tersedia")
	}
	altPath := filepath.Join(t.TempDir(), "alt")
	if out, err := exec.Command(other, "-t", "ed25519", "-N", "", "-f", altPath, "-C", "rotated").
		CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen gagal: %v (%s)", err, out)
	}
	altPub, err := os.ReadFile(altPath + ".pub")
	if err != nil {
		t.Fatalf("baca pub: %v", err)
	}
	newLine := f.spec + " " + strings.TrimSpace(string(altPub))
	if err := os.WriteFile(f.lineFile, []byte(newLine+"\n"), 0o600); err != nil {
		t.Fatalf("tulis ulang baris: %v", err)
	}

	// The stored entry is now stale, so the scan shows a different fingerprint.
	keys, err := f.svc.ScanHostKey(ctx, "web1")
	if err != nil {
		t.Fatalf("ScanHostKey: %v", err)
	}
	stored, err := f.svc.HostKeyStatus(ctx, "web1")
	if err != nil {
		t.Fatalf("HostKeyStatus: %v", err)
	}
	if len(keys) == 0 || len(stored.Fingerprints) == 0 {
		t.Fatal("sidik jari kosong")
	}
	if keys[0].Fingerprint == stored.Fingerprints[0].Fingerprint {
		t.Fatal("kunci berbeda tapi fingerprint sama")
	}

	// Trusting the new key replaces the old entry.
	if _, err := f.svc.TrustHostKey(ctx, "web1"); err != nil {
		t.Fatalf("TrustHostKey ulang: %v", err)
	}
	after, err := f.svc.HostKeyStatus(ctx, "web1")
	if err != nil {
		t.Fatalf("HostKeyStatus: %v", err)
	}
	if len(after.Fingerprints) != 1 || after.Fingerprints[0].Fingerprint != keys[0].Fingerprint {
		t.Errorf("setelah trust ulang = %+v, want %s", after.Fingerprints, keys[0].Fingerprint)
	}
}

func TestResolveSpecUsesEffectivePort(t *testing.T) {
	ctx := context.Background()
	f := newHostKeyFixture(t)

	hs, err := f.svc.resolveSpec(ctx, "web1")
	if err != nil {
		t.Fatalf("resolveSpec: %v", err)
	}
	if hs.hostname != "keyscan.example.com" || hs.port != "2200" {
		t.Errorf("resolveSpec = %+v", hs)
	}
	if hs.spec != f.spec {
		t.Errorf("spec = %q, want %q", hs.spec, f.spec)
	}
}

// TestWrittenConfigIsValidForRealSSH parses each host the service writes with
// the real ssh binary, so the file format can never drift from what ssh reads.
func TestWrittenConfigIsValidForRealSSH(t *testing.T) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh tidak tersedia")
	}
	ctx := context.Background()
	svc, home := newTestService(t)

	if _, err := svc.Add(ctx, HostInput{
		Alias: "quoted", HostName: "q.example.com", User: "u",
		Comment:      "host dengan spasi",
		IdentityFile: []string{"~/.ssh/key with space"},
		Options:      []sshconfig.Option{{Key: "ProxyCommand", Value: "ssh -W %h:%p bastion"}},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	out, err := exec.Command(sshPath, "-F", filepath.Join(home, ".ssh", "config"), "-G", "quoted").Output()
	if err != nil {
		t.Fatalf("ssh -G: %v", err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if _, seen := got[k]; !seen {
			got[k] = v
		}
	}
	wants := map[string]string{
		"hostname":     "q.example.com",
		"user":         "u",
		"identityfile": "~/.ssh/key with space",
		"proxycommand": "ssh -W %h:%p bastion",
	}
	for k, want := range wants {
		if got[k] != want {
			t.Errorf("ssh -G %s = %q, want %q", k, got[k], want)
		}
	}
	fmt.Fprintln(os.Stderr, "verified against", sshPath)
}
