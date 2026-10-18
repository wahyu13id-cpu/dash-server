package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestTerminalStatsLocalShell checks the status strip of a local shell: the
// machine it reports on is the dashboard host, so the background sampler is the
// source and no extra work happens per request.
func TestTerminalStatsLocalShell(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)

	status, body := doJSON(t, env, client, http.MethodPost, "/api/terminal/sessions", csrf, `{}`)
	mustStatus(t, status, http.StatusCreated, body)
	var created terminalSessionView
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	path := "/api/terminal/sessions/" + created.ID + "/stats"
	var view sessionStatsView
	// The sampler publishes its first snapshot about 400ms after startup, so the
	// endpoint is allowed to answer "not ready yet" for a moment.
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, body = doJSON(t, env, client, http.MethodGet, path, "", "")
		mustStatus(t, status, http.StatusOK, body)
		if err := json.Unmarshal(body, &view); err != nil {
			t.Fatalf("decode stats: %v (body: %s)", err, body)
		}
		if view.Available || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !view.Available {
		t.Fatalf("metrik host lokal tidak tersedia: %s", body)
	}
	if view.Scope != "local" {
		t.Errorf("scope = %q, want local", view.Scope)
	}
	if view.Host == "" {
		t.Error("hostname kosong")
	}
	if view.CPUPercent == nil {
		t.Error("cpuPercent hilang untuk sesi lokal")
	}
	if view.MemTotalBytes == 0 || view.MemUsedBytes > view.MemTotalBytes {
		t.Errorf("memori tidak masuk akal: %d/%d", view.MemUsedBytes, view.MemTotalBytes)
	}
	if view.DiskTotalBytes == 0 || view.DiskUsedBytes > view.DiskTotalBytes {
		t.Errorf("disk tidak masuk akal: %d/%d", view.DiskUsedBytes, view.DiskTotalBytes)
	}
	if view.NetRxBytesPerSec == nil || view.NetTxBytesPerSec == nil {
		t.Error("laju jaringan hilang untuk sesi lokal")
	}
	if view.CollectedAt == "" {
		t.Error("collectedAt kosong")
	}
}

// TestTerminalStatsSSHUntrustedHostKey checks that a remote probe refuses to run
// before the host key is trusted. The credential would otherwise be handed to an
// unknown host, so this is the same gate a session launch uses; the strip is
// still told why instead of showing nothing.
//
// The handler branch is exercised directly: a session in the test environment is
// backed by a fake ssh that exits immediately, so a live SSH session cannot be
// held open here (the session-scoped lookup itself is covered by the local test
// and by TestTerminalStatsScopeAndAuth).
func TestTerminalStatsSSHUntrustedHostKey(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	env.writeSSHConfig(t, "Host web1\n    HostName web1.example.com\n    User deploy\n")

	view := env.srv.remoteStatsView(context.Background(), "web1")
	if view.Available {
		t.Errorf("metrik remote tersedia padahal kunci host belum dipercaya: %+v", view)
	}
	if view.Scope != "remote" || view.Alias != "web1" {
		t.Errorf("scope/alias = %q/%q, want remote/web1", view.Scope, view.Alias)
	}
	if !strings.Contains(view.Message, "belum dipercaya") {
		t.Errorf("pesan tidak menjelaskan sebabnya: %q", view.Message)
	}

	// An unknown alias must not be probed at all.
	view = env.srv.remoteStatsView(context.Background(), "ghost")
	if view.Available {
		t.Errorf("alias tak dikenal dilaporkan tersedia: %+v", view)
	}
	if !strings.Contains(view.Message, "tidak ditemukan") {
		t.Errorf("pesan alias tak dikenal tidak jelas: %q", view.Message)
	}
}

// TestTerminalStatsScopeAndAuth covers the error paths: an unknown session is a
// 404 and an anonymous caller is rejected before anything is probed.
func TestTerminalStatsScopeAndAuth(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()

	resp, err := http.Get(env.ts.URL + "/api/terminal/sessions/apa-saja/stats")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("stats tanpa login: status = %d, want 401", resp.StatusCode)
	}

	client, csrf := login(t, env)
	status, body := doJSON(t, env, client, http.MethodGet,
		"/api/terminal/sessions/tidak-ada/stats", "", "")
	mustStatus(t, status, http.StatusNotFound, body)
	_ = csrf
}
