package sshconfig

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newHome creates a temp home with a .ssh dir and returns (home, sshDir).
func newHome(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return home, sshDir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func load(t *testing.T, home string) *Config {
	t.Helper()
	cfg, err := Load(LoadOptions{Home: home})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestParseRealWorldConfig(t *testing.T) {
	home, sshDir := newHome(t)
	writeFile(t, filepath.Join(sshDir, "d", "extra.conf"), "Host inc1\n    HostName inc1.example.com\n    User inced\n")
	writeFile(t, filepath.Join(sshDir, "config"), `# global defaults
Compression yes
ServerAliveInterval 30

Host a1 a2
    HostName a1.example.com
    User root
    Port 2222

# blok komentar
Host b1
    HostName=b1.example.com
    User="bob"
    IdentityFile ~/.ssh/id_ed25519
    IdentityFile="~/.ssh/other key"

Host c1
    HostName c1.example.com # trailing comment
    Port 2200

Host *.wild
    User wild

Include d/*.conf
`)

	cfg := load(t, home)

	// Implied global block.
	if len(cfg.Blocks) == 0 || !cfg.Blocks[0].Implied {
		t.Fatalf("blok global implied tidak ditemukan: %+v", cfg.Blocks)
	}
	if v, _ := cfg.Blocks[0].Option("Compression"); v != "yes" {
		t.Errorf("Compression = %q, want yes", v)
	}

	hosts := cfg.Hosts()
	got := map[string]*Host{}
	for _, h := range hosts {
		got[h.Alias] = h
	}
	wantAliases := []string{"a1", "a2", "b1", "c1", "inc1"}
	for _, want := range wantAliases {
		if got[want] == nil {
			t.Errorf("alias %q tidak ditemukan; dapat %v", want, cfg.Aliases())
		}
	}
	if got["a1"].HostName != "a1.example.com" || got["a1"].Port != "2222" {
		t.Errorf("a1 salah: %+v", got["a1"])
	}
	if got["a2"].HostName != "a1.example.com" {
		t.Errorf("a2 harus berbagi blok dengan a1: %+v", got["a2"])
	}
	if got["b1"].User != "bob" {
		t.Errorf("b1 user = %q, want bob (quoted)", got["b1"].User)
	}
	if len(got["b1"].IdentityFile) != 2 || got["b1"].IdentityFile[1] != "~/.ssh/other key" {
		t.Errorf("b1 identityFile = %v", got["b1"].IdentityFile)
	}
	if got["c1"].HostName != "c1.example.com" {
		t.Errorf("c1 hostName = %q (trailing comment harus dibuang)", got["c1"].HostName)
	}
	if got["c1"].Comment != "" {
		t.Errorf("c1 comment = %q, want empty", got["c1"].Comment)
	}
	if got["b1"].Comment != "blok komentar" {
		t.Errorf("b1 comment = %q, want 'blok komentar'", got["b1"].Comment)
	}
	// Included file is never writable.
	if got["inc1"].Writable {
		t.Error("inc1 dari include seharusnya read-only")
	}
	if !got["b1"].Writable {
		t.Error("b1 di file utama seharusnya writable")
	}
	if cfg.Files[0].Path != mustAbs(t, filepath.Join(sshDir, "config")) {
		t.Errorf("file utama bukan yang pertama: %+v", cfg.Files)
	}

	// Wildcard block is a default, not a host.
	for _, h := range hosts {
		if h.Alias == "*.wild" {
			t.Errorf("wildcard muncul sebagai host")
		}
	}
	defaults := cfg.Defaults()
	var sawWild bool
	for _, b := range defaults {
		for _, p := range b.Patterns {
			if p == "*.wild" {
				sawWild = true
			}
		}
	}
	if !sawWild {
		t.Errorf("blok wildcard tidak muncul di Defaults: %+v", defaults)
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}

func TestHashInsideTokenIsData(t *testing.T) {
	home, sshDir := newHome(t)
	writeFile(t, filepath.Join(sshDir, "config"), "Host h1\n    User u1#v\n    HostName a#b.example.com\n")
	cfg := load(t, home)
	h, ok := cfg.Lookup("h1")
	if !ok {
		t.Fatal("h1 hilang")
	}
	if h.User != "u1#v" {
		t.Errorf("user = %q, want u1#v", h.User)
	}
	if h.HostName != "a#b.example.com" {
		t.Errorf("hostName = %q, want a#b.example.com", h.HostName)
	}
}

func TestCommaIsNotPatternSeparator(t *testing.T) {
	home, sshDir := newHome(t)
	writeFile(t, filepath.Join(sshDir, "config"), "Host m1,m2\n    User comma\n")
	cfg := load(t, home)
	if len(cfg.Hosts()) != 1 || cfg.Hosts()[0].Alias != "m1,m2" {
		t.Fatalf("koma seharusnya bagian dari satu pola: %v", cfg.Aliases())
	}
	// The pseudo-alias is not a valid alias to write, so it is not connectable.
	if aliasRe.MatchString("m1,m2") {
		t.Error("alias dengan koma harus ditolak oleh validasi")
	}
}

func TestMissingConfigIsNotAnError(t *testing.T) {
	home, _ := newHome(t)
	cfg := load(t, home)
	if len(cfg.Hosts()) != 0 {
		t.Errorf("host = %v, want kosong", cfg.Aliases())
	}
	if len(cfg.Files) != 1 || cfg.Files[0].Exists {
		t.Errorf("file utama harus tercatat belum ada: %+v", cfg.Files)
	}
}

func TestIncludeCycleAndMissing(t *testing.T) {
	home, sshDir := newHome(t)
	writeFile(t, filepath.Join(sshDir, "config"), "Host a\n    User u\nInclude loop.conf missing.conf\n")
	writeFile(t, filepath.Join(sshDir, "loop.conf"), "Host b\n    User u\nInclude config\n")
	cfg := load(t, home)
	aliases := cfg.Aliases()
	if len(aliases) != 2 {
		t.Errorf("alias = %v, want [a b]", aliases)
	}
	var sawLoop, sawMissing bool
	for _, w := range cfg.Warnings {
		if strings.Contains(w, "siklus") {
			sawLoop = true
		}
		if strings.Contains(w, "tanpa hasil") {
			sawMissing = true
		}
	}
	if !sawLoop || !sawMissing {
		t.Errorf("warning tidak lengkap: %v", cfg.Warnings)
	}
}

func TestAppendCreatesFileWith0600(t *testing.T) {
	home, sshDir := newHome(t)
	cfg := load(t, home)

	host, err := cfg.Append(HostSpec{
		Alias:        "web1",
		HostName:     "web1.example.com",
		User:         "deploy",
		Port:         "2222",
		IdentityFile: []string{"~/.ssh/id_ed25519"},
		ProxyJump:    "bastion",
		Comment:      "produksi utama",
		Options:      []Option{{Key: "ForwardAgent", Value: "yes"}},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if host == nil || host.HostName != "web1.example.com" {
		t.Fatalf("host hasil Append salah: %+v", host)
	}

	path := filepath.Join(sshDir, "config")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %04o, want 0600", perm)
	}

	raw, _ := os.ReadFile(path)
	want := "# produksi utama\nHost web1\n    HostName web1.example.com\n    User deploy\n    Port 2222\n    IdentityFile ~/.ssh/id_ed25519\n    ProxyJump bastion\n    ForwardAgent yes\n"
	if string(raw) != want {
		t.Errorf("isi config:\n%q\nwant:\n%q", raw, want)
	}

	// Reload and confirm it parses back to the same thing.
	again := load(t, home)
	h, ok := again.Lookup("web1")
	if !ok {
		t.Fatal("web1 hilang setelah reload")
	}
	if h.Comment != "produksi utama" || h.ProxyJump != "bastion" {
		t.Errorf("round trip salah: %+v", h)
	}
	if len(h.Options) != 1 || h.Options[0].Key != "ForwardAgent" {
		t.Errorf("opsi tambahan hilang: %+v", h.Options)
	}
}

func TestAppendRejectsDuplicateAndInvalid(t *testing.T) {
	home, sshDir := newHome(t)
	writeFile(t, filepath.Join(sshDir, "config"), "Host dup\n    User u\n")
	cfg := load(t, home)

	if _, err := cfg.Append(HostSpec{Alias: "dup"}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("err = %v, want ErrDuplicate", err)
	}
	if _, err := cfg.Append(HostSpec{Alias: "-oProxyCommand=evil"}); err == nil {
		t.Error("alias berbahaya harus ditolak")
	}
	if _, err := cfg.Append(HostSpec{Alias: "ok", HostName: "a\nHost evil"}); err == nil {
		t.Error("nilai dengan baris baru harus ditolak")
	}
	if _, err := cfg.Append(HostSpec{Alias: "ok", Port: "99999"}); err == nil {
		t.Error("port di luar rentang harus ditolak")
	}
	if _, err := cfg.Append(HostSpec{Alias: "ok", Options: []Option{{Key: "Bad Key", Value: "x"}}}); err == nil {
		t.Error("nama opsi tidak valid harus ditolak")
	}
}

func TestUpdatePreservesRestOfFile(t *testing.T) {
	home, sshDir := newHome(t)
	original := `# header komentar
Host keep1
    HostName keep1.example.com
    User keepuser

Host edit
    HostName old.example.com
    User olduser

Host keep2
    HostName keep2.example.com
`
	writeFile(t, filepath.Join(sshDir, "config"), original)
	cfg := load(t, home)

	if _, err := cfg.Update("edit", HostSpec{Alias: "edit", HostName: "new.example.com", User: "newuser", Port: "2200"}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	raw, _ := os.ReadFile(filepath.Join(sshDir, "config"))
	got := string(raw)
	for _, want := range []string{
		"# header komentar\nHost keep1\n    HostName keep1.example.com\n    User keepuser\n",
		"Host keep2\n    HostName keep2.example.com\n",
		"Host edit\n    HostName new.example.com\n    User newuser\n    Port 2200\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config kehilangan bagian:\n%s\n\ntidak memuat:\n%s", got, want)
		}
	}
	if strings.Contains(got, "old.example.com") {
		t.Errorf("nilai lama masih ada:\n%s", got)
	}
}

func TestUpdateRenamePreservesSiblingPatterns(t *testing.T) {
	home, sshDir := newHome(t)
	writeFile(t, filepath.Join(sshDir, "config"), "Host alpha beta\n    User shared\n")
	cfg := load(t, home)

	if _, err := cfg.Update("alpha", HostSpec{Alias: "gamma", User: "shared"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(sshDir, "config"))
	if !strings.Contains(string(raw), "Host gamma beta") {
		t.Errorf("pola saudara hilang: %s", raw)
	}
	cfg = load(t, home)
	if _, ok := cfg.Lookup("gamma"); !ok {
		t.Error("gamma hilang")
	}
	if _, ok := cfg.Lookup("beta"); !ok {
		t.Error("beta hilang")
	}
}

func TestDeleteRemovesOnlyTheBlock(t *testing.T) {
	home, sshDir := newHome(t)
	writeFile(t, filepath.Join(sshDir, "config"), `Host first
    User a

# akan dihapus
Host gone
    HostName gone.example.com

Host last
    User c
`)
	cfg := load(t, home)
	if err := cfg.Delete("gone"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(sshDir, "config"))
	got := string(raw)
	if strings.Contains(got, "gone") {
		t.Errorf("blok belum terhapus:\n%s", got)
	}
	if !strings.Contains(got, "Host first") || !strings.Contains(got, "Host last") {
		t.Errorf("blok lain ikut terhapus:\n%s", got)
	}
}

func TestDeleteOneTokenFromSharedBlock(t *testing.T) {
	home, sshDir := newHome(t)
	writeFile(t, filepath.Join(sshDir, "config"), "Host alpha beta\n    User shared\n")
	cfg := load(t, home)
	if err := cfg.Delete("alpha"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(sshDir, "config"))
	if !strings.Contains(string(raw), "Host beta") || !strings.Contains(string(raw), "User shared") {
		t.Errorf("sisa blok salah:\n%s", raw)
	}
	cfg = load(t, home)
	if len(cfg.Hosts()) != 1 || cfg.Hosts()[0].Alias != "beta" {
		t.Errorf("aliases = %v, want [beta]", cfg.Aliases())
	}
}

func TestWritesToIncludeAreRefused(t *testing.T) {
	home, sshDir := newHome(t)
	writeFile(t, filepath.Join(sshDir, "config"), "Include d/*.conf\n")
	writeFile(t, filepath.Join(sshDir, "d", "x.conf"), "Host inc\n    User u\n")
	cfg := load(t, home)

	if _, err := cfg.Update("inc", HostSpec{Alias: "inc"}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Update err = %v, want ErrReadOnly", err)
	}
	if err := cfg.Delete("inc"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Delete err = %v, want ErrReadOnly", err)
	}
	if err := cfg.Delete("tidak-ada"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete err = %v, want ErrNotFound", err)
	}
}

func TestConflictWhenFileChangedExternally(t *testing.T) {
	home, sshDir := newHome(t)
	path := filepath.Join(sshDir, "config")
	writeFile(t, path, "Host target\n    User before\n")
	cfg := load(t, home)

	// Simulate an external rewrite that moves the block elsewhere.
	writeFile(t, path, "Host other\n    User o\n\nHost target\n    User after\n")

	_, err := cfg.Update("target", HostSpec{Alias: "target", User: "new"})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

func TestBackupAndCRLFPreserved(t *testing.T) {
	home, sshDir := newHome(t)
	path := filepath.Join(sshDir, "config")
	crlf := "# c\r\nHost win\r\n    HostName win.example.com\r\n"
	if err := os.WriteFile(path, []byte(crlf), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg := load(t, home)
	if _, err := cfg.Update("win", HostSpec{Alias: "win", HostName: "win.example.com", User: "w"}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "Host win\r\n    HostName win.example.com\r\n    User w\r\n") {
		t.Errorf("CRLF tidak dipertahankan:\n%q", raw)
	}

	backups, _ := filepath.Glob(path + ".bak.*")
	if len(backups) == 0 {
		t.Fatal("backup tidak dibuat")
	}
	data, _ := os.ReadFile(backups[0])
	if string(data) != crlf {
		t.Errorf("backup bukan salinan asli:\n%q", data)
	}
	st, _ := os.Stat(backups[0])
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode backup = %04o, want 0600", st.Mode().Perm())
	}
}

func TestQuoteValueRoundTrip(t *testing.T) {
	cases := []string{"simple", "with space", "#hash", `has"quote`, "a\tb"}
	for _, in := range cases {
		q := quoteValue(in)
		// Re-parse the quoted form as an IdentityFile and confirm equality.
		fields := splitFields(q)
		if len(fields) != 1 {
			t.Errorf("quoteValue(%q) = %q -> fields %v", in, q, fields)
			continue
		}
		if fields[0] != in {
			t.Errorf("quoteValue(%q) = %q -> %q (tidak sama)", in, q, fields[0])
		}
	}
}

// TestAgainstRealSSHAppend cross-checks a block written by this package against
// what the real ssh binary resolves, so the written format cannot drift.
func TestAgainstRealSSHAppend(t *testing.T) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh tidak tersedia")
	}
	home, _ := newHome(t)
	cfg := load(t, home)

	if _, err := cfg.Append(HostSpec{
		Alias:        "cross",
		HostName:     "cross.example.com",
		User:         "deploy",
		Port:         "2222",
		IdentityFile: []string{"~/.ssh/id_ed25519"},
		Options:      []Option{{Key: "ForwardAgent", Value: "yes"}},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	out, err := exec.Command(sshPath, "-F", cfg.Primary, "-G", "cross").Output()
	if err != nil {
		t.Fatalf("ssh -G: %v", err)
	}
	effective := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if _, seen := effective[k]; !seen {
			effective[k] = v
		}
	}
	for k, want := range map[string]string{
		"hostname":     "cross.example.com",
		"user":         "deploy",
		"port":         "2222",
		"identityfile": "~/.ssh/id_ed25519",
		"forwardagent": "yes",
	} {
		if got := effective[k]; got != want {
			t.Errorf("ssh -G %s = %q, want %q", k, got, want)
		}
	}
}
