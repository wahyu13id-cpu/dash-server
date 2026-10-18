package sshsvc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dashboard/internal/db"
	"dashboard/internal/sshaskpass"
	"dashboard/internal/sshconfig"
	"dashboard/internal/sshsecret"
	"dashboard/migrations"
)

const testSecret = "0123456789abcdef0123456789abcdef"

// newTestService builds a service pointed at a throwaway HOME so no real ~/.ssh
// is ever read or written.
func newTestService(t *testing.T) (*Service, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	conn, err := db.Open(ctx, filepath.Join(root, "ssh.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.Migrate(ctx, conn, migrations.FS); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	secrets, err := sshsecret.NewManager(conn, []byte(testSecret))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// A real broker is needed for the askpass path (stored passwords are handed
	// to ssh through it), so the tests exercise the same wiring as production.
	broker, err := sshaskpass.New(nil)
	if err != nil {
		t.Fatalf("sshaskpass.New: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	svc, err := New(Config{
		Enabled:    true,
		Home:       home,
		SSHBin:     "ssh",
		KeyscanBin: "ssh-keyscan",
		KeygenBin:  "ssh-keygen",
		AskpassBin: "/bin/true",
	}, conn, secrets, broker)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc, home
}

func writeConfig(t *testing.T, home, content string) {
	t.Helper()
	path := filepath.Join(home, ".ssh", "config")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestViewListsHostsWithMetaAndSecrets(t *testing.T) {
	ctx := context.Background()
	svc, home := newTestService(t)
	writeConfig(t, home, "Host web1\n    HostName web1.example.com\n    User deploy\n\nHost *.internal\n    User shared\n")

	if _, err := svc.Add(ctx, HostInput{Alias: "db1", HostName: "db1.example.com", User: "dba"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.SetSecret(ctx, "web1", sshsecret.KindPassword, "pw"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	if err := svc.SetFavorite(ctx, "web1", true); err != nil {
		t.Fatalf("SetFavorite: %v", err)
	}

	view, err := svc.View(ctx)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if !view.Enabled || !view.Exists {
		t.Fatalf("view enabled=%v exists=%v", view.Enabled, view.Exists)
	}
	if len(view.Hosts) != 2 {
		t.Fatalf("hosts = %d, want 2: %+v", len(view.Hosts), view.Hosts)
	}
	// Favourite sorts first.
	if view.Hosts[0].Alias != "web1" {
		t.Errorf("host pertama = %q, want web1 (favorit)", view.Hosts[0].Alias)
	}
	if !view.Hosts[0].Favorite || !view.Hosts[0].HasPassword {
		t.Errorf("web1 meta salah: %+v", view.Hosts[0])
	}
	var sawDefault bool
	for _, d := range view.Defaults {
		for _, p := range d.Patterns {
			if p == "*.internal" {
				sawDefault = true
			}
		}
	}
	if !sawDefault {
		t.Errorf("blok wildcard tidak terdeteksi: %+v", view.Defaults)
	}
}

func TestAddUpdateDeleteRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	host, err := svc.Add(ctx, HostInput{
		Alias: "web1", HostName: "web1.example.com", User: "deploy",
		Port: "2222", IdentityFile: []string{"~/.ssh/id_ed25519"},
		Options: []sshconfig.Option{{Key: "ForwardAgent", Value: "yes"}},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if host.Alias != "web1" || host.Port != "2222" {
		t.Fatalf("host = %+v", host)
	}
	if !host.Writable {
		t.Error("host di file utama harus writable")
	}

	if _, err := svc.Update(ctx, "web1", HostInput{
		Alias: "web1", HostName: "web1.example.com", User: "root",
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := svc.Lookup(ctx, "web1")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.User != "root" || got.Port != "" {
		t.Errorf("setelah update: %+v", got)
	}

	if err := svc.Delete(ctx, "web1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Lookup(ctx, "web1"); !errors.Is(err, ErrUnknownHost) {
		t.Errorf("Lookup setelah delete = %v, want ErrUnknownHost", err)
	}
}

func TestRenameKeepsSecretAndMeta(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	if _, err := svc.Add(ctx, HostInput{Alias: "old", HostName: "old.example.com"}); err != nil {

		t.Fatalf("Add: %v", err)
	}
	if err := svc.SetSecret(ctx, "old", sshsecret.KindPassword, "s3cret"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	if err := svc.SetNote(ctx, "old", "catatan penting"); err != nil {
		t.Fatalf("SetNote: %v", err)
	}

	if _, err := svc.Update(ctx, "old", HostInput{Alias: "new", HostName: "old.example.com"}); err != nil {
		t.Fatalf("Update rename: %v", err)
	}

	// The credential must still decrypt, which only works if it was re-sealed
	// with the new alias as associated data.
	secrets, err := svc.SecretStatus(ctx, "new")
	if err != nil {
		t.Fatalf("SecretStatus: %v", err)
	}
	if !secrets[sshsecret.KindPassword] {
		t.Fatalf("password hilang setelah rename: %v", secrets)
	}
	if err := svc.secrets.VerifyKey(ctx); err != nil {
		t.Fatalf("VerifyKey: %v", err)
	}
	value, ok, err := svc.secrets.Get(ctx, "new", sshsecret.KindPassword)
	if err != nil || !ok || value != "s3cret" {
		t.Errorf("Get setelah rename = %q ok=%v err=%v", value, ok, err)
	}
	meta, ok, err := svc.store.Get(ctx, "new")
	if err != nil || !ok || meta.Note != "catatan penting" {
		t.Errorf("meta setelah rename = %+v ok=%v err=%v", meta, ok, err)
	}
	if _, ok, _ := svc.store.Get(ctx, "old"); ok {
		t.Error("metadata lama masih ada")
	}
}

func TestDeleteRemovesSidecarState(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	if _, err := svc.Add(ctx, HostInput{Alias: "gone", HostName: "gone.example.com"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.SetSecret(ctx, "gone", sshsecret.KindPassword, "pw"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	if err := svc.SetNote(ctx, "gone", "hapus aku"); err != nil {
		t.Fatalf("SetNote: %v", err)
	}
	if err := svc.Delete(ctx, "gone"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	secrets, _ := svc.SecretStatus(ctx, "gone")
	if len(secrets) != 0 {
		t.Errorf("kredensial belum terhapus: %v", secrets)
	}
	if _, ok, _ := svc.store.Get(ctx, "gone"); ok {
		t.Error("metadata belum terhapus")
	}
}

func TestReadOnlyHostRejected(t *testing.T) {
	ctx := context.Background()
	svc, home := newTestService(t)
	writeConfig(t, home, "Include d/*.conf\n")
	if err := os.MkdirAll(filepath.Join(home, ".ssh", "d"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "d", "x.conf"),
		[]byte("Host inc\n    User u\n"), 0o600); err != nil {
		t.Fatalf("write include: %v", err)
	}

	if _, err := svc.Update(ctx, "inc", HostInput{Alias: "inc"}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Update = %v, want ErrReadOnly", err)
	}
	if err := svc.Delete(ctx, "inc"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Delete = %v, want ErrReadOnly", err)
	}
	if err := svc.SetSecret(ctx, "inc", sshsecret.KindPassword, "pw"); err != nil {
		t.Errorf("SetSecret untuk host read-only = %v, want nil (kredensial tidak di config)", err)
	}
}

func TestValidationRejectedAndMapped(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	cases := []struct {
		name string
		in   HostInput
	}{
		{"alias kosong", HostInput{Alias: ""}},
		{"alias dengan spasi", HostInput{Alias: "web 1"}},
		{"alias option injection", HostInput{Alias: "-oProxyCommand=evil"}},
		{"port bukan angka", HostInput{Alias: "ok", Port: "abc"}},
		{"port di luar rentang", HostInput{Alias: "ok", Port: "70000"}},
		{"nilai dengan newline", HostInput{Alias: "ok", HostName: "a\nHost evil"}},
		{"nama opsi tidak valid", HostInput{Alias: "ok", Options: []sshconfig.Option{{Key: "Bad Key", Value: "x"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Add(ctx, tc.in)
			if err == nil {
				t.Fatal("input berbahaya harus ditolak")
			}
			var verr *sshconfig.ValidationError
			if !errors.As(err, &verr) {
				t.Errorf("err = %v, want ValidationError", err)
			}
		})
	}

	if _, err := svc.Add(ctx, HostInput{Alias: "dup"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := svc.Add(ctx, HostInput{Alias: "dup"}); !errors.Is(err, sshconfig.ErrDuplicate) {
		t.Errorf("duplikat = %v, want ErrDuplicate", err)
	}
	if _, err := svc.Update(ctx, "tidak-ada", HostInput{Alias: "tidak-ada"}); !errors.Is(err, ErrUnknownHost) {
		t.Errorf("update host hilang = %v, want ErrUnknownHost", err)
	}
}

func TestLaunchBuildsSafeArgv(t *testing.T) {
	ctx := context.Background()
	svc, home := newTestService(t)
	writeConfig(t, home, "Host web1\n    HostName web1.example.com\n    User deploy\n")

	argv, env, cleanup, err := svc.Launch(ctx, "web1")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if len(argv) == 0 || argv[0] == "" {
		t.Fatalf("argv kosong: %v", argv)
	}
	// The alias must be a single argument, never merged into a shell string.
	if argv[len(argv)-1] != "web1" {
		t.Errorf("argv terakhir = %q, want alias web1", argv[len(argv)-1])
	}
	joined := strings.Join(argv, " ")
	for _, forbidden := range []string{";", "&&", "|", "`", "$("} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("argv memuat %q: %s", forbidden, joined)
		}
	}
	// No stored credential means no askpass environment at all.
	if len(env) != 0 {
		t.Errorf("env = %v, want kosong tanpa kredensial tersimpan", env)
	}
	if cleanup != nil {
		t.Error("cleanup harus nil tanpa kredensial")
	}

	// known_hosts must be pinned: ssh expands "~/.ssh/known_hosts" from the
	// passwd entry rather than $HOME, so the default would point somewhere else
	// than the file the dashboard manages.
	wantKnownHosts := "UserKnownHostsFile=" + filepath.Join(home, ".ssh", "known_hosts")
	if !containsArg(argv, wantKnownHosts) {
		t.Errorf("argv tidak memuat %q: %v", wantKnownHosts, argv)
	}

	if _, _, _, err := svc.Launch(ctx, "tidak-ada"); !errors.Is(err, ErrUnknownHost) {
		t.Errorf("Launch alias hilang = %v, want ErrUnknownHost", err)
	}
	for _, bad := range []string{"-oProxyCommand=x", "web1; rm -rf /", "web 1", ""} {
		if _, _, _, err := svc.Launch(ctx, bad); err == nil {
			t.Errorf("Launch(%q) harus ditolak", bad)
		}
	}
}

// TestLaunchOnlyOffersCredentialsForTrustedHosts checks the host-key gate: an
// untrusted host must stay interactive (the fingerprint question cannot be
// answered by the askpass helper), while a trusted one gets the stored
// credential injected.
func TestLaunchOnlyOffersCredentialsForTrustedHosts(t *testing.T) {
	ctx := context.Background()
	svc, home := newTestService(t)
	writeConfig(t, home, "Host web1\n    HostName web1.example.com\n    User deploy\n")

	// Store a credential directly so the only remaining variable is trust.
	if err := svc.SetSecret(ctx, "web1", sshsecret.KindPassword, "rahasia"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}

	_, env, cleanup, err := svc.Launch(ctx, "web1")
	if err != nil {
		t.Fatalf("Launch (untrusted): %v", err)
	}
	if len(env) != 0 {
		t.Errorf("kunci host belum dipercaya, env harus kosong: %v", env)
	}
	if cleanup != nil {
		t.Error("tanpa askpass tidak boleh ada cleanup")
	}

	// Trust the host key the way the UI does, then the credential is offered.
	hostKey := filepath.Join(home, ".ssh", "known_hosts")
	if err := os.MkdirAll(filepath.Dir(hostKey), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Port 22 means the bare host name is the known_hosts pattern.
	if err := os.WriteFile(hostKey, []byte("web1.example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	_, env, cleanup, err = svc.Launch(ctx, "web1")
	if err != nil {
		t.Fatalf("Launch (trusted): %v", err)
	}
	if len(env) == 0 {
		t.Fatal("kunci host sudah dipercaya, env askpass harus ada")
	}
	joined := strings.Join(env, "\n")
	for _, want := range []string{"SSH_ASKPASS=", "SSH_ASKPASS_REQUIRE=force", sshaskpass.HelperEnv + "="} {
		if !strings.Contains(joined, want) {
			t.Errorf("env askpass tidak memuat %q:\n%s", want, joined)
		}
	}
	if cleanup == nil {
		t.Error("cleanup harus ada saat kredensial dipakai")
	} else {
		cleanup()
	}
}

func containsArg(argv []string, want string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i]+argv[i+1] == want || argv[i] == want {
			return true
		}
	}
	return false
}

func TestQuickAddParsesCommand(t *testing.T) {
	svc := &Service{}
	cases := []struct {
		in   string
		want QuickAdd
	}{
		{
			in:   "ssh deploy@web1.example.com -p 2222",
			want: QuickAdd{Alias: "web1.example.com", HostName: "web1.example.com", User: "deploy", Port: "2222"},
		},
		{
			in:   "ssh -i ~/.ssh/id_ed25519 -p2222 -l root db1.internal",
			want: QuickAdd{Alias: "db1.internal", HostName: "db1.internal", User: "root", Port: "2222", IdentityFile: "~/.ssh/id_ed25519"},
		},
		{
			in:   "web3.example.com",
			want: QuickAdd{Alias: "web3.example.com", HostName: "web3.example.com"},
		},
		{
			in:   "ssh -J jump1 web4.example.com",
			want: QuickAdd{Alias: "web4.example.com", HostName: "web4.example.com", ProxyJump: "jump1"},
		},
		{
			in:   "ssh user@host:2222",
			want: QuickAdd{Alias: "host", HostName: "host", User: "user", Port: "2222"},
		},
	}
	for _, tc := range cases {
		got := svc.QuickAdd(tc.in)
		if got.Alias != tc.want.Alias || got.HostName != tc.want.HostName ||
			got.User != tc.want.User || got.Port != tc.want.Port ||
			got.IdentityFile != tc.want.IdentityFile || got.ProxyJump != tc.want.ProxyJump {
			t.Errorf("QuickAdd(%q) =\n  %+v\nwant\n  %+v", tc.in, got, tc.want)
		}
	}

	// Unsafe characters in a hostname must be sanitised into a safe alias.
	got := svc.QuickAdd("ssh 'we!rd host'")
	if got.Alias != "" && !sshAliasSafe(got.Alias) {
		t.Errorf("alias %q tidak aman", got.Alias)
	}
	if empty := svc.QuickAdd("ssh -o Foo=bar"); len(empty.Warnings) == 0 {
		t.Error("perintah tanpa host harus memberi peringatan")
	}
}

// sshAliasSafe mirrors validateAlias for the test's own assertion.
func sshAliasSafe(alias string) bool { return validateAlias(alias) == nil }

func TestClassifySSHError(t *testing.T) {
	cases := []struct {
		out  string
		want string
	}{
		{"Host key verification failed.", "host_key_unknown"},
		{"WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!", "host_key_unknown"},
		{"deploy@web1: Permission denied (publickey).", "auth_required"},
		{"Connection refused", "unreachable"},
		{"ssh: connect to host web1 port 22: Connection timed out", "unreachable"},
		{"ssh: Could not resolve hostname web1: Name or service not known", "dns_error"},
		{"something entirely unexpected", "error"},
	}
	for _, tc := range cases {
		got, _ := classifySSHError(tc.out, errors.New("exit status 255"))
		if got != tc.want {
			t.Errorf("classifySSHError(%q) = %q, want %q", tc.out, got, tc.want)
		}
	}
}

func TestValidateAlias(t *testing.T) {
	good := []string{"web1", "web-1.example.com", "a_b", "A1"}
	for _, a := range good {
		if err := validateAlias(a); err != nil {
			t.Errorf("validateAlias(%q) = %v, want nil", a, err)
		}
	}
	bad := []string{"", "-web", "web 1", "web;1", "web$1", "*", "web/1", "web|1"}
	for _, a := range bad {
		if err := validateAlias(a); err == nil {
			t.Errorf("validateAlias(%q) harus gagal", a)
		}
	}
}

func TestDisabledServiceRejectsEverything(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	conn, err := db.Open(ctx, filepath.Join(root, "off.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()
	if err := db.Migrate(ctx, conn, migrations.FS); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	svc, err := New(Config{Enabled: false, Home: root}, conn, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if svc.Enabled() {
		t.Fatal("service seharusnya nonaktif")
	}
	if _, err := svc.View(ctx); err != nil {
		t.Errorf("View saat nonaktif = %v, want nil (halaman tetap tampil)", err)
	}
	if _, err := svc.Add(ctx, HostInput{Alias: "x"}); !errors.Is(err, ErrDisabled) {
		t.Errorf("Add = %v, want ErrDisabled", err)
	}
	if _, _, _, err := svc.Launch(ctx, "x"); !errors.Is(err, ErrDisabled) {
		t.Errorf("Launch = %v, want ErrDisabled", err)
	}
	if _, err := svc.Effective(ctx, "x"); !errors.Is(err, ErrDisabled) {
		t.Errorf("Effective = %v, want ErrDisabled", err)
	}
}

func TestKeysListsOnlyPrivateKeys(t *testing.T) {
	ctx := context.Background()
	svc, home := newTestService(t)
	sshDir := filepath.Join(home, ".ssh")
	// A realistic key pair, plus files that must not be mistaken for keys.
	pub := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKq7Rk9y0m0V0Q4kPLQ0m0K2m0m0m0m0m0m0m0m0m0m0 dash@test"
	key := "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZWQyNTUx\n-----END OPENSSH PRIVATE KEY-----\n"
	files := map[string]string{
		"id_ed25519":      key,
		"id_ed25519.pub":  pub,
		"authorized_keys": "ssh-ed25519 AAAA\n",
		"known_hosts":     "example.com ssh-ed25519 AAAA\n",
		"config":          "Host a\n",
		"environment":     "FOO=bar\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(sshDir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	keys, err := svc.Keys(ctx)
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %+v, want hanya id_ed25519", keys)
	}
	if keys[0].Name != "id_ed25519" || !keys[0].HasPublic {
		t.Errorf("kunci salah: %+v", keys[0])
	}
	if !strings.HasPrefix(keys[0].Fingerprint, "SHA256:") {
		t.Errorf("fingerprint = %q, want awalan SHA256:", keys[0].Fingerprint)
	}
	if keys[0].Type == "" {
		t.Errorf("tipe kunci kosong: %+v", keys[0])
	}
}

func TestMissingConfigIsEmptyView(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	view, err := svc.View(ctx)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if view.Exists {
		t.Error("config belum ada tapi Exists true")
	}
	if len(view.Hosts) != 0 {
		t.Errorf("hosts = %+v, want kosong", view.Hosts)
	}
}
