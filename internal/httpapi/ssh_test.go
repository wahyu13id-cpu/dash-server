package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSSHConfig writes the throwaway ssh config used by the SSH tests.
func (e *testEnv) writeSSHConfig(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(e.sshHome, ".ssh", "config")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write ssh config: %v", err)
	}
}

// doJSON performs an authenticated request and returns the status and body.
func doJSON(t *testing.T, env *testEnv, client *http.Client, method, path, csrf, body string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, env.ts.URL+path, reader)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, path, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", testOrig)
	if csrf != "" {
		req.Header.Set(csrfHeaderName, csrf)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %s %s: %v", method, path, err)
	}
	return resp.StatusCode, data
}

func mustStatus(t *testing.T, got, want int, body []byte) {
	t.Helper()
	if got != want {
		t.Fatalf("status = %d, want %d (body: %s)", got, want, body)
	}
}

func TestSSHEndpointsRequireAuth(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()

	for _, path := range []string{"/api/ssh/state", "/api/ssh/keys"} {
		resp, err := http.Get(env.ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s tanpa login = %d, want 401", path, resp.StatusCode)
		}
	}
}

func TestSSHStateListsHostsAndDefaults(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	env.writeSSHConfig(t, "Compression yes\n\nHost web1\n    HostName web1.example.com\n    User deploy\n\nHost *.internal\n    User shared\n")

	status, body := doJSON(t, env, client, http.MethodGet, "/api/ssh/state", "", "")
	mustStatus(t, status, http.StatusOK, body)

	var view struct {
		Enabled bool   `json:"enabled"`
		Exists  bool   `json:"exists"`
		Mode    string `json:"mode"`
		Hosts   []struct {
			Alias    string `json:"alias"`
			HostName string `json:"hostName"`
			User     string `json:"user"`
			Writable bool   `json:"writable"`
		} `json:"hosts"`
		Defaults []struct {
			Patterns []string `json:"patterns"`
			Implied  bool     `json:"implied"`
		} `json:"defaults"`
		SSHDir string `json:"sshDir"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if !view.Enabled || !view.Exists {
		t.Fatalf("enabled=%v exists=%v (body: %s)", view.Enabled, view.Exists, body)
	}
	if view.Mode != "0600" {
		t.Errorf("mode = %q, want 0600", view.Mode)
	}
	if len(view.Hosts) != 1 || view.Hosts[0].Alias != "web1" {
		t.Fatalf("hosts = %+v", view.Hosts)
	}
	if view.Hosts[0].HostName != "web1.example.com" || view.Hosts[0].User != "deploy" {
		t.Errorf("host salah: %+v", view.Hosts[0])
	}
	if !view.Hosts[0].Writable {
		t.Error("host di file utama harus writable")
	}
	// The global block and the wildcard block must both appear as defaults.
	var sawImplied, sawWildcard bool
	for _, d := range view.Defaults {
		if d.Implied {
			sawImplied = true
		}
		for _, p := range d.Patterns {
			if p == "*.internal" {
				sawWildcard = true
			}
		}
	}
	if !sawImplied || !sawWildcard {
		t.Errorf("defaults tidak lengkap: %+v", view.Defaults)
	}
	if csrf == "" {
		t.Fatal("csrf tidak ada")
	}
}

func TestSSHHostCRUDOverHTTP(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	env.writeSSHConfig(t, "Host existing\n    HostName existing.example.com\n")

	// Create.
	status, body := doJSON(t, env, client, http.MethodPost, "/api/ssh/hosts", csrf,
		`{"alias":"web2","hostName":"web2.example.com","user":"deploy","port":"2222","identityFile":["~/.ssh/id_ed25519"]}`)
	mustStatus(t, status, http.StatusCreated, body)

	// The file must be 0600 and the previous content untouched.
	path := filepath.Join(env.sshHome, ".ssh", "config")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode config = %04o, want 0600", perm)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "Host existing") {
		t.Errorf("isi lama hilang:\n%s", raw)
	}
	if !strings.Contains(string(raw), "Host web2") {
		t.Errorf("host baru tidak tertulis:\n%s", raw)
	}
	backups, _ := filepath.Glob(path + ".bak.*")
	if len(backups) == 0 {
		t.Error("backup tidak dibuat")
	}

	// Duplicate alias is a conflict.
	status, body = doJSON(t, env, client, http.MethodPost, "/api/ssh/hosts", csrf, `{"alias":"web2"}`)
	mustStatus(t, status, http.StatusConflict, body)

	// Invalid input is rejected as unprocessable.
	status, body = doJSON(t, env, client, http.MethodPost, "/api/ssh/hosts", csrf, `{"alias":"-oProxyCommand=evil"}`)
	mustStatus(t, status, http.StatusUnprocessableEntity, body)
	status, body = doJSON(t, env, client, http.MethodPost, "/api/ssh/hosts", csrf, `{"alias":"ok","hostName":"a\nHost evil"}`)
	mustStatus(t, status, http.StatusUnprocessableEntity, body)

	// Read a single host.
	status, body = doJSON(t, env, client, http.MethodGet, "/api/ssh/hosts/web2", "", "")
	mustStatus(t, status, http.StatusOK, body)

	// Update.
	status, body = doJSON(t, env, client, http.MethodPatch, "/api/ssh/hosts/web2", csrf,
		`{"alias":"web2","hostName":"web2.example.com","user":"root"}`)
	mustStatus(t, status, http.StatusOK, body)
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), "User root") || strings.Contains(string(raw), "User deploy") {
		t.Errorf("update tidak diterapkan:\n%s", raw)
	}

	// Invalid method on a missing host.
	status, body = doJSON(t, env, client, http.MethodPatch, "/api/ssh/hosts/nope", csrf, `{"alias":"nope"}`)
	mustStatus(t, status, http.StatusNotFound, body)

	// Delete.
	status, body = doJSON(t, env, client, http.MethodDelete, "/api/ssh/hosts/web2", csrf, "")
	mustStatus(t, status, http.StatusOK, body)
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "web2") {
		t.Errorf("host belum terhapus:\n%s", raw)
	}
	status, body = doJSON(t, env, client, http.MethodDelete, "/api/ssh/hosts/web2", csrf, "")
	mustStatus(t, status, http.StatusNotFound, body)
}

func TestSSHIncludedHostIsReadOnlyOverHTTP(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	env.writeSSHConfig(t, "Include d/*.conf\n")
	dir := filepath.Join(env.sshHome, ".ssh", "d")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.conf"), []byte("Host inc\n    User u\n"), 0o600); err != nil {
		t.Fatalf("write include: %v", err)
	}

	status, body := doJSON(t, env, client, http.MethodPatch, "/api/ssh/hosts/inc", csrf, `{"alias":"inc","user":"x"}`)
	mustStatus(t, status, http.StatusBadRequest, body)
	if !strings.Contains(string(body), "hanya baca") {
		t.Errorf("pesan error tidak jelas: %s", body)
	}
	status, body = doJSON(t, env, client, http.MethodDelete, "/api/ssh/hosts/inc", csrf, "")
	mustStatus(t, status, http.StatusBadRequest, body)

	// The include file must be untouched.
	data, _ := os.ReadFile(filepath.Join(dir, "x.conf"))
	if string(data) != "Host inc\n    User u\n" {
		t.Errorf("file include berubah: %q", data)
	}
}

func TestSSHSecretsAreWriteOnly(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	env.writeSSHConfig(t, "Host web1\n    HostName web1.example.com\n")

	const password = "SUPER-SECRET-PASSWORD-42"
	status, body := doJSON(t, env, client, http.MethodPut, "/api/ssh/hosts/web1/secret", csrf,
		`{"kind":"password","value":"`+password+`"}`)
	mustStatus(t, status, http.StatusOK, body)
	if strings.Contains(string(body), password) {
		t.Fatal("respons memantulkan nilai kredensial")
	}

	// State reports the credential exists but never its value.
	status, body = doJSON(t, env, client, http.MethodGet, "/api/ssh/state", "", "")
	mustStatus(t, status, http.StatusOK, body)
	if strings.Contains(string(body), password) {
		t.Fatal("state memantulkan nilai kredensial")
	}
	if !strings.Contains(string(body), `"hasPassword":true`) {
		t.Errorf("state tidak melaporkan hasPassword: %s", body)
	}

	// A passphrase is a separate kind.
	status, body = doJSON(t, env, client, http.MethodPut, "/api/ssh/hosts/web1/secret", csrf,
		`{"kind":"passphrase","value":"key-pass"}`)
	mustStatus(t, status, http.StatusOK, body)
	status, body = doJSON(t, env, client, http.MethodGet, "/api/ssh/state", "", "")
	mustStatus(t, status, http.StatusOK, body)
	if !strings.Contains(string(body), `"hasPassphrase":true`) {
		t.Errorf("state tidak melaporkan hasPassphrase: %s", body)
	}

	// An unknown kind is rejected.
	status, body = doJSON(t, env, client, http.MethodPut, "/api/ssh/hosts/web1/secret", csrf,
		`{"kind":"apiKey","value":"x"}`)
	mustStatus(t, status, http.StatusBadRequest, body)

	// An empty value is rejected rather than stored.
	status, body = doJSON(t, env, client, http.MethodPut, "/api/ssh/hosts/web1/secret", csrf,
		`{"kind":"password","value":""}`)
	mustStatus(t, status, http.StatusBadRequest, body)

	// ssh reads the helper's answer one line at a time, so a value containing a
	// newline would be silently truncated: refuse it up front.
	status, body = doJSON(t, env, client, http.MethodPut, "/api/ssh/hosts/web1/secret", csrf,
		`{"kind":"password","value":"half\nrest"}`)
	mustStatus(t, status, http.StatusBadRequest, body)
	status, body = doJSON(t, env, client, http.MethodGet, "/api/ssh/state", "", "")
	mustStatus(t, status, http.StatusOK, body)
	if !strings.Contains(string(body), `"hasPassword":true`) {
		t.Errorf("nilai berbaris baru sempat menghapus password lama: %s", body)
	}

	// A passphrase for an unknown host is a 404.
	status, body = doJSON(t, env, client, http.MethodPut, "/api/ssh/hosts/ghost/secret", csrf,
		`{"kind":"password","value":"x"}`)
	mustStatus(t, status, http.StatusNotFound, body)

	// Clearing only the passphrase leaves the password.
	status, body = doJSON(t, env, client, http.MethodDelete, "/api/ssh/hosts/web1/secret?kind=passphrase", csrf, "")
	mustStatus(t, status, http.StatusOK, body)
	status, body = doJSON(t, env, client, http.MethodGet, "/api/ssh/state", "", "")
	mustStatus(t, status, http.StatusOK, body)
	if !strings.Contains(string(body), `"hasPassword":true`) ||
		strings.Contains(string(body), `"hasPassphrase":true`) {
		t.Errorf("hasil clear salah: %s", body)
	}

	// Clearing without a kind removes both.
	status, body = doJSON(t, env, client, http.MethodDelete, "/api/ssh/hosts/web1/secret", csrf, "")
	mustStatus(t, status, http.StatusOK, body)
	status, body = doJSON(t, env, client, http.MethodGet, "/api/ssh/state", "", "")
	mustStatus(t, status, http.StatusOK, body)
	if strings.Contains(string(body), `"hasPassword":true`) {
		t.Errorf("kredensial belum terhapus: %s", body)
	}
}

func TestSSHQuickAddEndpoint(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)

	status, body := doJSON(t, env, client, http.MethodPost, "/api/ssh/parse", csrf,
		`{"command":"ssh deploy@web9.example.com -p 2200 -i ~/.ssh/id_ed25519"}`)
	mustStatus(t, status, http.StatusOK, body)

	var got struct {
		Alias        string `json:"alias"`
		HostName     string `json:"hostName"`
		User         string `json:"user"`
		Port         string `json:"port"`
		IdentityFile string `json:"identityFile"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.HostName != "web9.example.com" || got.User != "deploy" || got.Port != "2200" ||
		got.IdentityFile != "~/.ssh/id_ed25519" || got.Alias != "web9.example.com" {
		t.Errorf("hasil parse salah: %+v", got)
	}
}

func TestSSHEffectiveUsesResolvedConfig(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, _ := login(t, env)
	env.writeSSHConfig(t, "Host web1\n    HostName ignored.example.com\n")

	status, body := doJSON(t, env, client, http.MethodGet, "/api/ssh/hosts/web1/effective", "", "")
	mustStatus(t, status, http.StatusOK, body)

	var res struct {
		Options []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"options"`
		Raw string `json:"raw"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The values come from the fake ssh -G, proving the endpoint delegates to
	// the real client rather than reimplementing resolution.
	if !strings.Contains(res.Raw, "hostname web1.example.com") {
		t.Errorf("raw = %q", res.Raw)
	}
	found := false
	for _, o := range res.Options {
		if o.Key == "port" && o.Value == "2222" {
			found = true
		}
	}
	if !found {
		t.Errorf("options tidak memuat port dari ssh -G: %+v", res.Options)
	}

	status, body = doJSON(t, env, client, http.MethodGet, "/api/ssh/hosts/ghost/effective", "", "")
	mustStatus(t, status, http.StatusNotFound, body)
}

func TestSSHHostKeyStatusEndpoint(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, _ := login(t, env)
	env.writeSSHConfig(t, "Host web1\n    HostName web1.example.com\n")

	status, body := doJSON(t, env, client, http.MethodGet, "/api/ssh/hosts/web1/hostkey", "", "")
	mustStatus(t, status, http.StatusOK, body)

	var info struct {
		Known    bool   `json:"known"`
		HostSpec string `json:"hostSpec"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if info.Known {
		t.Error("known_hosts belum ada isinya, Known seharusnya false")
	}
	// Port 2222 comes from the fake ssh -G, so the spec must be bracketed.
	if info.HostSpec != "[web1.example.com]:2222" {
		t.Errorf("hostSpec = %q, want [web1.example.com]:2222", info.HostSpec)
	}
}

// TestSSHHostKeyScanUpstreamFailure checks that a failure coming from the
// scanned host (or from ssh-keyscan itself) is reported as a gateway problem
// with its detail intact, rather than as an opaque 500 that hides the cause.
func TestSSHHostKeyScanUpstreamFailure(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	env.writeSSHConfig(t, "Host web1\n    HostName web1.example.com\n")

	// The fake keyscan prints an informational line, not a key, so the service
	// reports "host tidak memberikan kunci apa pun".
	status, body := doJSON(t, env, client, http.MethodPost, "/api/ssh/hosts/web1/hostkey/scan", csrf, "")
	mustStatus(t, status, http.StatusBadGateway, body)
	if !strings.Contains(string(body), "host tidak memberikan kunci") {
		t.Errorf("detail kegagalan tidak diteruskan: %s", body)
	}

	// An unknown alias is still a 404, not a gateway error.
	status, body = doJSON(t, env, client, http.MethodPost, "/api/ssh/hosts/ghost/hostkey/scan", csrf, "")
	mustStatus(t, status, http.StatusNotFound, body)
}

func TestSSHSessionKindAndTarget(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	env.writeSSHConfig(t, "Host web1\n    HostName web1.example.com\n    User deploy\n")

	// An SSH session records its kind and target.
	status, body := doJSON(t, env, client, http.MethodPost, "/api/terminal/sessions", csrf,
		`{"kind":"ssh","target":"web1","cols":80,"rows":24}`)
	mustStatus(t, status, http.StatusCreated, body)

	var created terminalSessionView
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Kind != "ssh" || created.Target != "web1" {
		t.Fatalf("sesi dibuat dengan kind=%q target=%q", created.Kind, created.Target)
	}

	// It is visible in the session list with the same kind and target.
	status, body = doJSON(t, env, client, http.MethodGet, "/api/terminal/sessions", "", "")
	mustStatus(t, status, http.StatusOK, body)
	if !strings.Contains(string(body), `"target":"web1"`) || !strings.Contains(string(body), `"kind":"ssh"`) {
		t.Errorf("daftar sesi tidak memuat sesi ssh: %s", body)
	}

	// An unknown alias is rejected before anything is launched.
	status, body = doJSON(t, env, client, http.MethodPost, "/api/terminal/sessions", csrf,
		`{"kind":"ssh","target":"ghost"}`)
	mustStatus(t, status, http.StatusBadRequest, body)

	// An alias that could smuggle an ssh option is rejected.
	status, body = doJSON(t, env, client, http.MethodPost, "/api/terminal/sessions", csrf,
		`{"kind":"ssh","target":"-oProxyCommand=touch /tmp/pwned"}`)
	mustStatus(t, status, http.StatusBadRequest, body)
	if _, err := os.Stat("/tmp/pwned"); err == nil {
		t.Fatal("command injection berhasil")
	}

	// A missing alias and an unknown kind are both client errors.
	status, body = doJSON(t, env, client, http.MethodPost, "/api/terminal/sessions", csrf, `{"kind":"ssh"}`)
	mustStatus(t, status, http.StatusBadRequest, body)
	status, body = doJSON(t, env, client, http.MethodPost, "/api/terminal/sessions", csrf, `{"kind":"k8s"}`)
	mustStatus(t, status, http.StatusBadRequest, body)

	// A plain shell session keeps working and is tagged kind=shell.
	status, body = doJSON(t, env, client, http.MethodPost, "/api/terminal/sessions", csrf, `{}`)
	mustStatus(t, status, http.StatusCreated, body)
	var shell terminalSessionView
	if err := json.Unmarshal(body, &shell); err != nil {
		t.Fatalf("decode shell: %v", err)
	}
	if shell.Kind != "shell" || shell.Target != "" {
		t.Errorf("shell kind=%q target=%q", shell.Kind, shell.Target)
	}
}

func TestSSHFavoriteAndNote(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	env.writeSSHConfig(t, "Host web1\n    HostName web1.example.com\n")

	status, body := doJSON(t, env, client, http.MethodPost, "/api/ssh/hosts/web1/favorite", csrf, `{"favorite":true}`)
	mustStatus(t, status, http.StatusOK, body)
	status, body = doJSON(t, env, client, http.MethodPut, "/api/ssh/hosts/web1/note", csrf, `{"note":"server produksi"}`)
	mustStatus(t, status, http.StatusOK, body)

	status, body = doJSON(t, env, client, http.MethodGet, "/api/ssh/state", "", "")
	mustStatus(t, status, http.StatusOK, body)
	if !strings.Contains(string(body), `"favorite":true`) || !strings.Contains(string(body), "server produksi") {
		t.Errorf("metadata tidak tersimpan: %s", body)
	}
	// Metadata must not leak into the ssh config file itself.
	raw, _ := os.ReadFile(filepath.Join(env.sshHome, ".ssh", "config"))
	if strings.Contains(string(raw), "produksi") || strings.Contains(string(raw), "favorite") {
		t.Errorf("metadata bocor ke ~/.ssh/config:\n%s", raw)
	}

	// Favouriting an unknown host is a 404.
	status, body = doJSON(t, env, client, http.MethodPost, "/api/ssh/hosts/ghost/favorite", csrf, `{"favorite":true}`)
	mustStatus(t, status, http.StatusNotFound, body)
}

func TestSSHKeysEndpoint(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, _ := login(t, env)

	status, body := doJSON(t, env, client, http.MethodGet, "/api/ssh/keys", "", "")
	mustStatus(t, status, http.StatusOK, body)
	if !strings.Contains(string(body), `"keys"`) {
		t.Errorf("respons tanpa field keys: %s", body)
	}
}
