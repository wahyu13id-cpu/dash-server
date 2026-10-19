package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dashboard/internal/sysops"
)

// sysopsDisabledService builds a service with the feature switched off, which
// is what a host with DASH_OPS_ENABLED=false looks like.
func sysopsDisabledService() *sysops.Service {
	return sysops.New(sysops.Config{Enabled: false})
}

// opsGet performs an authenticated GET and decodes the JSON body into out.
func opsGet(t *testing.T, env *testEnv, client *http.Client, path string, out any) int {
	t.Helper()
	status, body := doJSON(t, env, client, http.MethodGet, path, "", "")
	if out != nil && status < 300 {
		if err := json.Unmarshal(body, out); err != nil {
			t.Fatalf("decode %s: %v (body: %s)", path, err, body)
		}
	}
	return status
}

// opsPost performs an authenticated POST carrying a JSON body.
func opsPost(t *testing.T, env *testEnv, client *http.Client, csrf, path, body string) int {
	t.Helper()
	status, _ := doJSON(t, env, client, http.MethodPost, path, csrf, body)
	return status
}

func TestOpsEndpointsRequireAuth(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()

	client := env.client()
	for _, path := range []string{
		"/api/ops/availability",
		"/api/ops/services",
		"/api/ops/services/nginx.service",
		"/api/ops/processes",
		"/api/ops/logs/sources",
		"/api/ops/logs/journal",
		"/api/ops/logs/file?path=/etc/passwd",
		"/api/ops/packages",
		"/api/ops/jobs/does-not-exist",
	} {
		t.Run(path, func(t *testing.T) {
			status, _ := doJSON(t, env, client, http.MethodGet, path, "", "")
			mustStatus(t, status, http.StatusUnauthorized, nil)
		})
	}
}

func TestOpsAvailabilityAndServices(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, _ := login(t, env)

	var availability struct {
		Systemd        bool   `json:"systemd"`
		Processes      bool   `json:"processes"`
		PackageManager string `json:"packageManager"`
	}
	mustStatus(t, opsGet(t, env, client, "/api/ops/availability", &availability), http.StatusOK, nil)
	if !availability.Systemd || !availability.Processes {
		t.Errorf("availability = %+v, want systemd and processes", availability)
	}
	if availability.PackageManager != "apt" {
		t.Errorf("package manager = %q, want apt", availability.PackageManager)
	}

	var list struct {
		Units []struct {
			Name     string `json:"name"`
			Active   string `json:"active"`
			Sub      string `json:"sub"`
			UnitFile string `json:"unitFile"`
		} `json:"units"`
		Total   int `json:"total"`
		Running int `json:"running"`
		Failed  int `json:"failed"`
	}
	mustStatus(t, opsGet(t, env, client, "/api/ops/services", &list), http.StatusOK, nil)
	if list.Total != 3 {
		t.Fatalf("total = %d, want 3 (%+v)", list.Total, list.Units)
	}
	if list.Running != 1 || list.Failed != 1 {
		t.Errorf("running = %d, failed = %d, want 1 and 1", list.Running, list.Failed)
	}

	// list-units supplies the runtime state, list-unit-files the boot state and
	// the units that are installed but not loaded.
	byName := map[string]string{}
	for _, u := range list.Units {
		byName[u.Name] = u.UnitFile
	}
	if byName["nginx.service"] != "enabled" || byName["cron.service"] != "disabled" {
		t.Errorf("unit-file states = %v", byName)
	}
}

func TestOpsServiceDetailAndAction(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)

	var detail struct {
		Unit struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			MainPID     int    `json:"mainPid"`
		} `json:"unit"`
		Properties map[string]string `json:"properties"`
	}
	mustStatus(t, opsGet(t, env, client, "/api/ops/services/nginx.service", &detail), http.StatusOK, nil)
	if detail.Unit.Name != "nginx.service" || detail.Unit.MainPID != 4242 {
		t.Errorf("detail unit = %+v", detail.Unit)
	}
	if detail.Properties["ActiveState"] != "active" {
		t.Errorf("properties = %v", detail.Properties)
	}

	mustStatus(t,
		opsPost(t, env, client, csrf, "/api/ops/services/nginx.service/action", `{"action":"restart"}`),
		http.StatusOK, nil)

	// A rejected unit name or verb must never reach the command.
	mustStatus(t,
		opsPost(t, env, client, csrf, "/api/ops/services/nginx.socket/action", `{"action":"start"}`),
		http.StatusBadRequest, nil)
	mustStatus(t,
		opsPost(t, env, client, csrf, "/api/ops/services/nginx.service/action", `{"action":"rm -rf"}`),
		http.StatusBadRequest, nil)
}

func TestOpsProcessesRejectUnsafeInput(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)

	var list struct {
		Processes []struct {
			PID  int    `json:"pid"`
			Name string `json:"name"`
		} `json:"processes"`
		Total int `json:"total"`
	}
	mustStatus(t, opsGet(t, env, client, "/api/ops/processes?sort=cpu&limit=5", &list), http.StatusOK, nil)
	if list.Total == 0 || len(list.Processes) == 0 {
		t.Fatal("no processes returned")
	}
	if len(list.Processes) > 5 {
		t.Errorf("limit ignored: %d processes", len(list.Processes))
	}

	// PID 1 is always refused: stopping init cannot be undone from the UI.
	mustStatus(t,
		opsPost(t, env, client, csrf, "/api/ops/processes/1/signal", `{"signal":"KILL"}`),
		http.StatusBadRequest, nil)
	mustStatus(t,
		opsPost(t, env, client, csrf, "/api/ops/processes/1/nice", `{"nice":0}`),
		http.StatusBadRequest, nil)

	// An unknown signal never reaches the kernel (no process is really killed).
	mustStatus(t,
		opsPost(t, env, client, csrf, "/api/ops/processes/2/signal", `{"signal":"WOBBLE"}`),
		http.StatusBadRequest, nil)
	mustStatus(t,
		opsPost(t, env, client, csrf, "/api/ops/processes/2/nice", `{"nice":99}`),
		http.StatusBadRequest, nil)

	// An unknown sort key is rejected by the API layer.
	mustStatus(t,
		opsGet(t, env, client, "/api/ops/processes?sort=magic", nil),
		http.StatusBadRequest, nil)
}

func TestOpsLogFileStaysInsideLogDir(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, _ := login(t, env)

	inside := filepath.Join(env.opsLog, "app.log")
	if err := os.WriteFile(inside, []byte("satu\ndua\ntiga\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	var file struct {
		Name  string   `json:"name"`
		Lines []string `json:"lines"`
	}
	mustStatus(t, opsGet(t, env, client, "/api/ops/logs/file?path="+escape(inside)+"&lines=2", &file), http.StatusOK, nil)
	if len(file.Lines) != 2 || file.Lines[1] != "tiga" {
		t.Errorf("lines = %v", file.Lines)
	}

	// Outside the log directory, including a traversal attempt: refused.
	outside := filepath.Join(env.opsLog, "..", "outside.log")
	if err := os.WriteFile(outside, []byte("rahasia\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/etc/passwd", outside, env.opsLog + "/../outside.log"} {
		mustStatus(t, opsGet(t, env, client, "/api/ops/logs/file?path="+escape(path), nil), http.StatusBadRequest, nil)
	}

	// A file that does not exist is reported as missing, not as forbidden.
	mustStatus(t,
		opsGet(t, env, client, "/api/ops/logs/file?path="+escape(filepath.Join(env.opsLog, "nope.log")), nil),
		http.StatusNotFound, nil)

	// The source list must not leak paths from outside the log directory.
	var sources struct {
		Sources []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"sources"`
	}
	mustStatus(t, opsGet(t, env, client, "/api/ops/logs/sources", &sources), http.StatusOK, nil)
	found := false
	for _, s := range sources.Sources {
		if s.Path != "" && !strings.HasPrefix(s.Path, env.opsLog) {
			t.Errorf("source outside log dir: %s", s.Path)
		}
		if s.Path == inside {
			found = true
		}
	}
	if !found {
		t.Errorf("log file %s missing from the source list (%+v)", inside, sources.Sources)
	}
}

func TestOpsJournalValidation(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, _ := login(t, env)

	for _, path := range []string{
		"/api/ops/logs/journal?lines=abc",
		"/api/ops/logs/journal?unit=/etc/passwd",
		"/api/ops/logs/journal?priority=loud",
		"/api/ops/processes?limit=99999",
	} {
		mustStatus(t, opsGet(t, env, client, path, nil), http.StatusBadRequest, nil)
	}
}

func TestOpsUnavailableWhenDisabled(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, _ := login(t, env)

	// Simulate a host with the feature switched off.
	env.srv.ops = sysopsDisabledService()
	for _, path := range []string{"/api/ops/services", "/api/ops/processes", "/api/ops/logs/sources", "/api/ops/packages"} {
		mustStatus(t, opsGet(t, env, client, path, nil), http.StatusServiceUnavailable, nil)
	}
	// Availability still answers, with everything unsupported.
	var availability struct {
		Systemd bool `json:"systemd"`
	}
	mustStatus(t, opsGet(t, env, client, "/api/ops/availability", &availability), http.StatusOK, nil)
	if availability.Systemd {
		t.Error("availability reported systemd on a disabled service")
	}
}
