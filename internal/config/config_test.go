package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnvFile(t *testing.T) {
	data := []byte(`
# a comment
DASH_A=plain

export DASH_B=exported
DASH_C="quoted value"
DASH_D='single'
DASH_E=
not a valid line
`)
	got := parseEnvFile(data)
	want := map[string]string{
		"DASH_A": "plain",
		"DASH_B": "exported",
		"DASH_C": "quoted value",
		"DASH_D": "single",
		"DASH_E": "",
	}
	if len(got) != len(want) {
		t.Fatalf("parseEnvFile returned %d keys, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("parseEnvFile[%s] = %q, want %q", k, got[k], v)
		}
	}
}

func TestLoadDotEnvDoesNotOverrideExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "DASH_LOADTEST_NEW=fresh\nDASH_LOADTEST_EXISTING=from-file\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DASH_ENV_FILE", path)
	t.Setenv("DASH_LOADTEST_EXISTING", "from-env")
	_ = os.Unsetenv("DASH_LOADTEST_NEW")
	defer func() { _ = os.Unsetenv("DASH_LOADTEST_NEW") }()

	loadDotEnv()

	if v := os.Getenv("DASH_LOADTEST_NEW"); v != "fresh" {
		t.Fatalf("DASH_LOADTEST_NEW = %q, want fresh", v)
	}
	if v := os.Getenv("DASH_LOADTEST_EXISTING"); v != "from-env" {
		t.Fatalf("DASH_LOADTEST_EXISTING = %q, want from-env (existing env must win)", v)
	}
}

func TestTerminalEnvironmentHistory(t *testing.T) {
	envMap := func(env []string) map[string]string {
		m := make(map[string]string, len(env))
		for _, kv := range env {
			if i := strings.IndexByte(kv, '='); i >= 0 {
				m[kv[:i]] = kv[i+1:]
			}
		}
		return m
	}

	// History enabled: persistent file, live append, histappend.
	c := &Config{HomeDir: "/root", TerminalHistory: true}
	got := envMap(c.TerminalEnvironment())
	if want := "/root/.bash_history"; got["HISTFILE"] != want {
		t.Errorf("HISTFILE = %q, want %q", got["HISTFILE"], want)
	}
	if got["HISTSIZE"] == "0" || got["HISTSIZE"] == "" {
		t.Errorf("HISTSIZE = %q, want a positive value", got["HISTSIZE"])
	}
	if got["PROMPT_COMMAND"] != "history -a" {
		t.Errorf("PROMPT_COMMAND = %q, want %q", got["PROMPT_COMMAND"], "history -a")
	}
	if got["BASHOPTS"] != "histappend" {
		t.Errorf("BASHOPTS = %q, want histappend", got["BASHOPTS"])
	}

	// History disabled: nothing persisted, no live-append hook.
	c.TerminalHistory = false
	got = envMap(c.TerminalEnvironment())
	if got["HISTFILE"] != "/dev/null" {
		t.Errorf("HISTFILE = %q, want /dev/null when history disabled", got["HISTFILE"])
	}
	if got["HISTSIZE"] != "0" {
		t.Errorf("HISTSIZE = %q, want 0 when history disabled", got["HISTSIZE"])
	}
	if _, ok := got["PROMPT_COMMAND"]; ok {
		t.Errorf("PROMPT_COMMAND should be unset when history disabled, got %q", got["PROMPT_COMMAND"])
	}
}
