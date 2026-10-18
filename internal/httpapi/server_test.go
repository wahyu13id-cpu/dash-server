package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"dashboard/internal/audit"
	"dashboard/internal/auth"
	"dashboard/internal/config"
	"dashboard/internal/db"
	"dashboard/internal/files"
	"dashboard/internal/sshaskpass"
	"dashboard/internal/sshsecret"
	"dashboard/internal/sshsvc"
	"dashboard/internal/sysinfo"
	"dashboard/internal/terminal"
	"dashboard/internal/workspace"
	"dashboard/migrations"
)

const (
	testUser = "admin"
	testPass = "supersecret1"
	testOrig = "https://example.test"
)

type testEnv struct {
	ts      *httptest.Server
	srv     *Server
	cfg     *config.Config
	term    *terminal.Manager
	auth    *auth.Service
	store   *auth.Store
	monitor *sysinfo.Collector
	ssh     *sshsvc.Service
	sshHome string
	close   func()
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()

	root := t.TempDir()
	conn, err := db.Open(ctx, filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(ctx, conn, migrations.FS); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	wsRoot := filepath.Join(root, "workspace")
	ws, err := workspace.New(wsRoot)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}

	cfg := &config.Config{
		Addr:                "127.0.0.1:0",
		WorkspaceRoot:       wsRoot,
		StaticDir:           "",
		SessionSecret:       []byte("test-secret-test-secret-test-secret"),
		SessionTTL:          time.Hour,
		CookieSecure:        false,
		AllowedOrigins:      []string{testOrig},
		TrustedProxy:        false,
		Shell:               "/bin/bash",
		TerminalMaxSessions: 4,
		TerminalMaxPerUser:  4,
		TerminalIdleTimeout: time.Minute,
		TerminalScrollback:  32 * 1024,
		MonitorInterval:     time.Second,
		TOTPIssuer:          "Dashboard Test",
		MaxReadSize:         1 << 20,
		MaxWriteSize:        1 << 20,
		MaxUploadSize:       1 << 20,
		MaxListEntries:      1000,
		LogLevel:            "error",
		SSHStats:            true,
	}

	auditLog := audit.NewLogger(conn)
	authStore := auth.NewStore(conn)
	authSvc := auth.NewService(authStore, auditLog, auth.Options{
		SessionTTL: time.Hour,
		TOTPIssuer: cfg.TOTPIssuer,
	})
	hash, err := auth.HashPassword(testPass)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authStore.CreateUser(ctx, testUser, "Admin", hash, "admin"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	filesSvc := files.NewService(ws, auditLog, files.Options{MaxReadSize: cfg.MaxReadSize, MaxWriteSize: cfg.MaxWriteSize, MaxUploadSize: cfg.MaxUploadSize, MaxListEntries: cfg.MaxListEntries})

	// SSH service against a throwaway HOME so no real ~/.ssh is ever touched.
	sshHome := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(sshHome, ".ssh"), 0o700); err != nil {
		t.Fatalf("mkdir ssh home: %v", err)
	}
	// A fake ssh binary keeps the tests hermetic: it echoes the argv it was
	// handed (proving the real command line reaches the process) and answers
	// -G with a canned effective configuration.
	fakeSSH := filepath.Join(root, "fake-ssh")
	script := "#!/bin/sh\ncase \" $* \" in\n  *\" -G \"*)\n    printf 'hostname web1.example.com\\nuser deploy\\nport 2222\\n'\n    ;;\n  *)\n    echo \"FAKE-SSH $*\"\n    ;;\nesac\nexit 0\n"
	if err := os.WriteFile(fakeSSH, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	secrets, err := sshsecret.NewManager(conn, cfg.SessionSecret)
	if err != nil {
		t.Fatalf("sshsecret.NewManager: %v", err)
	}
	broker, err := sshaskpass.New(nil)
	if err != nil {
		t.Fatalf("sshaskpass.New: %v", err)
	}
	sshSvc, err := sshsvc.New(sshsvc.Config{
		Enabled:    true,
		Home:       sshHome,
		SSHBin:     fakeSSH,
		KeyscanBin: fakeSSH,
		KeygenBin:  "ssh-keygen",
		AskpassBin: "/bin/true",
	}, conn, secrets, broker)
	if err != nil {
		t.Fatalf("sshsvc.New: %v", err)
	}

	termMgr := terminal.NewManager(terminal.ManagerConfig{
		Shell: "/bin/bash", WorkingDir: ws.Root(), Env: []string{"PATH=/usr/bin:/bin", "TERM=xterm"},
		MaxSessions: 4, MaxPerUser: 4, IdleTimeout: time.Minute, ScrollbackBytes: 32 * 1024,
		SSHCommand: func(alias string) (terminal.SSHLaunch, error) {
			argv, env, cleanup, err := sshSvc.Launch(ctx, alias)
			if err != nil {
				return terminal.SSHLaunch{}, err
			}
			return terminal.SSHLaunch{Argv: argv, Env: env, Cleanup: cleanup}, nil
		},
	}, terminal.NewStore(conn), auditLog)

	mon := sysinfo.New(cfg.MonitorInterval)
	monCtx, monCancel := context.WithCancel(ctx)
	mon.Start(monCtx)

	srv := New(cfg, authSvc, filesSvc, termMgr, auditLog, mon, sshSvc)
	ts := httptest.NewServer(srv.Handler())

	return &testEnv{
		ts:      ts,
		srv:     srv,
		cfg:     cfg,
		term:    termMgr,
		auth:    authSvc,
		store:   authStore,
		monitor: mon,
		ssh:     sshSvc,
		sshHome: sshHome,
		close: func() {
			monCancel()
			termMgr.Shutdown(context.Background())
			_ = broker.Close()
			ts.Close()
			conn.Close()
		},
	}
}

func (e *testEnv) client() *http.Client {
	jar, _ := newCookieJar()
	return &http.Client{Jar: jar, Timeout: 10 * time.Second}
}

func TestHealthRequiresNoAuth(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()

	resp, err := http.Get(env.ts.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestProtectedEndpointsRequireAuth(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()

	endpoints := []struct{ method, path string }{
		{http.MethodGet, "/api/files?path="},
		{http.MethodGet, "/api/files/content?path=a.txt"},
		{http.MethodPut, "/api/files/content?path=a.txt"},
		{http.MethodPost, "/api/files"},
		{http.MethodPatch, "/api/files"},
		{http.MethodDelete, "/api/files?path=a.txt"},
		{http.MethodPost, "/api/terminal/sessions"},
		{http.MethodGet, "/api/terminal/sessions"},
		{http.MethodDelete, "/api/terminal/sessions/abc"},
		{http.MethodGet, "/api/stats"},
		{http.MethodGet, "/api/system"},
		{http.MethodPost, "/api/auth/logout"},
		{http.MethodGet, "/api/auth/totp"},
		{http.MethodPost, "/api/auth/totp/setup"},
		{http.MethodPost, "/api/auth/totp/activate"},
		{http.MethodPost, "/api/auth/totp/disable"},
		{http.MethodPost, "/api/auth/totp/recovery/regenerate"},
	}
	for _, ep := range endpoints {
		req, _ := http.NewRequest(ep.method, env.ts.URL+ep.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", ep.method, ep.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s: status = %d, want 401", ep.method, ep.path, resp.StatusCode)
		}
	}
}

// TestSystemEndpointReturnsSnapshot verifies the host-metrics endpoint is
// authenticated and eventually returns a populated snapshot.
func TestSystemEndpointReturnsSnapshot(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, _ := login(t, env)

	// The sampler warms up within ~400ms; poll briefly for the first snapshot.
	var last systemResponse
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(env.ts.URL + "/api/system")
		if err != nil {
			t.Fatal(err)
		}
		var body systemResponse
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		last = body
		if body.Ready {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !last.Ready || last.Snapshot == nil {
		t.Fatal("system endpoint never returned a ready snapshot")
	}
	if last.Snapshot.CPU.Cores < 1 {
		t.Errorf("cores = %d, want >= 1", last.Snapshot.CPU.Cores)
	}
	if last.Snapshot.Memory.TotalBytes == 0 {
		t.Error("memory total is zero")
	}
	if last.Snapshot.Processes.Total == 0 {
		t.Error("no processes reported")
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()

	body := `{"username":"admin","password":"wrong"}`
	resp, err := http.Post(env.ts.URL+"/api/auth/login", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestLoginAndCSRFEnforcement(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)

	// Mutating request without the CSRF header must be rejected.
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/files",
		strings.NewReader(`{"path":"x.txt","kind":"file"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testOrig)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF: status = %d, want 403", resp.StatusCode)
	}

	// With the header it succeeds.
	req2, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/files",
		strings.NewReader(`{"path":"x.txt","kind":"file"}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Origin", testOrig)
	req2.Header.Set(csrfHeaderName, csrf)
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("with CSRF: status = %d, want 201", resp2.StatusCode)
	}
}

func TestOriginIsEnforced(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)

	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/files",
		strings.NewReader(`{"path":"x.txt","kind":"file"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set(csrfHeaderName, csrf)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bad origin: status = %d, want 403", resp.StatusCode)
	}
}

func TestFileTraversalIsBlocked(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, _ := login(t, env)

	req, _ := http.NewRequest(http.MethodGet, env.ts.URL+"/api/files/content?path="+escape("../../etc/passwd"), nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("traversal: status = %d, want 403/400", resp.StatusCode)
	}
}

func TestTerminalSessionLifecycleAndAuth(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()

	// Unauthenticated session creation must fail.
	resp, err := http.Post(env.ts.URL+"/api/terminal/sessions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth terminal: status = %d, want 401", resp.StatusCode)
	}

	client, csrf := login(t, env)

	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/terminal/sessions",
		strings.NewReader(`{"title":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testOrig)
	req.Header.Set(csrfHeaderName, csrf)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var created terminalSessionView
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || created.ID == "" {
		t.Fatalf("create session: status = %d body = %+v", resp.StatusCode, created)
	}

	// WebSocket without the session cookie must be rejected.
	_, wsResp, err := websocket.DefaultDialer.Dial(wsURL(env.ts.URL, created.ID), nil)
	if err == nil {
		t.Fatal("unauthenticated websocket dial unexpectedly succeeded")
	}
	if wsResp == nil || wsResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth ws status = %v", wsResp)
	}

	// Authenticated websocket connects, runs a command through the PTY and
	// receives streamed output.
	headers := http.Header{}
	headers.Set("Cookie", cookieHeader(client, env.ts.URL))
	headers.Set("Origin", testOrig)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(env.ts.URL, created.ID), headers)
	if err != nil {
		t.Fatalf("authenticated websocket dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(wsMessage{Type: "input", Data: "echo dashboard-ok\n"}); err != nil {
		t.Fatalf("write input: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var got strings.Builder
	for {
		var msg wsMessage
		if err := conn.ReadJSON(&msg); err != nil {
			break
		}
		if msg.Type == "output" {
			got.WriteString(msg.Data)
			if strings.Contains(got.String(), "dashboard-ok") {
				break
			}
		}
	}
	if !strings.Contains(got.String(), "dashboard-ok") {
		t.Fatalf("terminal output did not contain expected marker: %q", got.String())
	}

	// Close the session via the REST API.
	delReq, _ := http.NewRequest(http.MethodDelete, env.ts.URL+"/api/terminal/sessions/"+created.ID, nil)
	delReq.Header.Set("Origin", testOrig)
	delReq.Header.Set(csrfHeaderName, csrf)
	delResp, err := client.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete session: status = %d", delResp.StatusCode)
	}
}

func TestWebsocketOriginIsValidated(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)

	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/terminal/sessions", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testOrig)
	req.Header.Set(csrfHeaderName, csrf)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var created terminalSessionView
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	headers := http.Header{}
	headers.Set("Cookie", cookieHeader(client, env.ts.URL))
	headers.Set("Origin", "https://evil.example")
	_, wsResp, err := websocket.DefaultDialer.Dial(wsURL(env.ts.URL, created.ID), headers)
	if err == nil {
		t.Fatal("websocket with disallowed origin unexpectedly succeeded")
	}
	if wsResp == nil || wsResp.StatusCode != http.StatusForbidden {
		t.Fatalf("bad origin ws status = %v", wsResp)
	}
}

// TestAuditRecordsActor verifies that file operations are attributed to the
// authenticated user (id, username and client IP) in the audit trail.
func TestAuditRecordsActor(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)

	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/files",
		strings.NewReader(`{"path":"audited.txt","kind":"file"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testOrig)
	req.Header.Set(csrfHeaderName, csrf)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}

	listReq, _ := http.NewRequest(http.MethodGet, env.ts.URL+"/api/audit", nil)
	listResp, err := client.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()

	var payload struct {
		Entries []audit.Record `json:"entries"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}

	var found *audit.Record
	wantTarget := filepath.ToSlash(filepath.Join(env.cfg.WorkspaceRoot, "audited.txt"))
	for i := range payload.Entries {
		e := payload.Entries[i]
		if e.Action == audit.ActionFileCreate && e.Target == wantTarget {
			found = &payload.Entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no audit entry for file.create of audited.txt: %+v", payload.Entries)
	}
	if found.Username != testUser {
		t.Fatalf("audit username = %q, want %q", found.Username, testUser)
	}
	if found.UserID == 0 {
		t.Fatal("audit entry is missing the acting user id")
	}
	if found.IP == "" {
		t.Fatal("audit entry is missing the client IP")
	}
}

// TestStreamCloseRaceDoesNotCrashServer is a regression test for a panic
// ("send on closed channel") that occurred when the websocket client
// disconnected while the session was still alive: the reader goroutine closed
// the shared outbound channel and the PTY pump then tried to send on it.
// A panic inside a goroutine is not caught by the HTTP recover middleware, so
// it used to take the entire server process down.
func TestStreamCloseRaceDoesNotCrashServer(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)

	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/terminal/sessions", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testOrig)
	req.Header.Set(csrfHeaderName, csrf)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var created terminalSessionView
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	headers := http.Header{}
	headers.Set("Cookie", cookieHeader(client, env.ts.URL))
	headers.Set("Origin", testOrig)

	// Repeat the connect/disconnect cycle so the race window is exercised.
	for i := 0; i < 5; i++ {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL(env.ts.URL, created.ID), headers)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		if err := conn.WriteJSON(wsMessage{Type: "input", Data: "echo tick\n"}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		// Abruptly drop the client while the PTY is still running.
		_ = conn.Close()
	}

	// Closing the session now would, before the fix, trigger the pump's send on
	// the already-closed channel.
	delReq, _ := http.NewRequest(http.MethodDelete, env.ts.URL+"/api/terminal/sessions/"+created.ID, nil)
	delReq.Header.Set("Origin", testOrig)
	delReq.Header.Set(csrfHeaderName, csrf)
	delResp, err := client.Do(delReq)
	if err != nil {
		t.Fatalf("delete after streaming: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d", delResp.StatusCode)
	}

	// The server must still answer requests (i.e. it did not crash).
	health, err := http.Get(env.ts.URL + "/api/health")
	if err != nil {
		t.Fatalf("server unreachable after streaming race: %v", err)
	}
	health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("health status after race = %d", health.StatusCode)
	}
}
