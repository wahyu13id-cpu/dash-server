package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"dashboard/internal/audit"
	"dashboard/internal/sshsvc"
	"dashboard/internal/sysinfo"
	"dashboard/internal/terminal"
)

const (
	wsWriteWait      = 10 * time.Second
	wsPongWait       = 75 * time.Second
	wsPingPeriod     = 25 * time.Second
	wsMaxMessageSize = 1 << 20 // 1 MiB per client message (large pastes)
)

// wsMessage is the JSON envelope exchanged over the terminal socket. Terminal
// payloads are carried as opaque strings and never logged.
type wsMessage struct {
	Type    string `json:"type"`
	Data    string `json:"data,omitempty"`
	Cols    uint16 `json:"cols,omitempty"`
	Rows    uint16 `json:"rows,omitempty"`
	Code    *int   `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	ID      string `json:"id,omitempty"`
}

type terminalSessionView struct {
	terminal.Metadata
	Live        bool `json:"live"`
	Connections int  `json:"connections"`
}

// handleTerminalList returns live and recent sessions for the current user.
func (s *Server) handleTerminalList(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	live := s.term.List(user.ID)
	seen := make(map[string]int, len(live))
	views := make([]terminalSessionView, 0, len(live))
	for _, m := range live {
		seen[m.ID] = len(views)
		views = append(views, terminalSessionView{Metadata: m, Live: true})
	}
	for i := range views {
		if sess, err := s.term.Get(views[i].ID, user.ID, false); err == nil {
			views[i].Connections = sess.Connections()
		}
	}

	history, err := s.term.Store().ListByUser(r.Context(), user.ID, 25)
	if err == nil {
		for _, m := range history {
			if _, ok := seen[m.ID]; ok {
				continue
			}
			views = append(views, terminalSessionView{Metadata: m, Live: false})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sessions": views,
		"limits": map[string]int{
			"maxGlobal":  s.cfg.TerminalMaxSessions,
			"maxPerUser": s.cfg.TerminalMaxPerUser,
			"live":       s.term.Count(),
		},
	})
}

type createSessionRequest struct {
	Title  string `json:"title"`
	Kind   string `json:"kind"`
	Target string `json:"target"`
	// Dir is the directory a local shell starts in; ignored for SSH.
	Dir  string `json:"dir"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// handleTerminalCreate starts a new PTY session, either a local shell or an SSH
// session against a host alias.
func (s *Server) handleTerminalCreate(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	user := userFrom(r.Context())

	spec, err := s.launchSpec(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}

	sess, err := s.term.Create(r.Context(), user.ID, user.Username, spec, req.Cols, req.Rows)
	if err != nil {
		switch {
		case errors.Is(err, terminal.ErrTooManySessions):
			writeError(w, http.StatusConflict, CodeConflict, err.Error())
		case errors.Is(err, sshsvc.ErrUnknownHost),
			errors.Is(err, sshsvc.ErrDisabled),
			errors.Is(err, sshsvc.ErrNoAlias),
			errors.Is(err, sshsvc.ErrInvalidAlias):
			writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
		default:
			slog.Error("create terminal session failed", "error", err)
			writeError(w, http.StatusInternalServerError, CodeInternal, "could not start terminal session")
		}
		return
	}

	writeJSON(w, http.StatusCreated, terminalSessionView{Metadata: sess.Metadata(), Live: true})
}

// launchSpec turns a request into a session spec, validating the kind so an
// unknown value can never reach the process launcher.
func (s *Server) launchSpec(req createSessionRequest) (terminal.LaunchSpec, error) {
	switch strings.ToLower(strings.TrimSpace(req.Kind)) {
	case "", terminal.KindShell:
		dir, err := s.shellDir(req.Dir)
		if err != nil {
			return terminal.LaunchSpec{}, err
		}
		if dir == "" {
			return terminal.ShellSpec(), nil
		}
		return terminal.ShellSpecIn(dir), nil
	case terminal.KindSSH:
		alias := strings.TrimSpace(req.Target)
		if alias == "" {
			return terminal.LaunchSpec{}, errors.New("alias ssh wajib diisi")
		}
		return terminal.SSHSpec(alias), nil
	default:
		return terminal.LaunchSpec{}, fmt.Errorf("jenis sesi %q tidak dikenal", req.Kind)
	}
}

// shellDir validates a requested starting directory for a local shell. The path
// is resolved through the same workspace sandbox as the file manager, so a
// session can only ever start somewhere the dashboard is allowed to work.
// An empty request keeps the manager's default directory.
func (s *Server) shellDir(dir string) (string, error) {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return "", nil
	}
	abs, err := s.files.Workspace().Resolve(trimmed)
	if err != nil {
		return "", errors.New("direktori awal tidak valid")
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", errors.New("direktori awal tidak ditemukan")
	}
	if !info.IsDir() {
		return "", errors.New("path awal bukan direktori")
	}
	return abs, nil
}

// sessionStatsView is the status strip shown above a session: the resource usage
// of the machine that session is connected to.
//
// Available is false when the numbers cannot be gathered (host key not trusted
// yet, no usable credential, unreachable host, non-Linux remote). The endpoint
// still answers 200 in that case so the strip can explain itself instead of
// looking like a failed request, and Message carries the reason.
type sessionStatsView struct {
	Available   bool   `json:"available"`
	Scope       string `json:"scope"` // "local" for a shell, "remote" for ssh
	Host        string `json:"host"`
	Alias       string `json:"alias,omitempty"`
	Message     string `json:"message,omitempty"`
	CollectedAt string `json:"collectedAt,omitempty"`

	CPUPercent *float64 `json:"cpuPercent,omitempty"`
	CPUCount   int      `json:"cpuCount,omitempty"`
	Load1      float64  `json:"load1,omitempty"`

	MemTotalBytes  uint64 `json:"memTotalBytes,omitempty"`
	MemUsedBytes   uint64 `json:"memUsedBytes,omitempty"`
	SwapTotalBytes uint64 `json:"swapTotalBytes,omitempty"`
	SwapUsedBytes  uint64 `json:"swapUsedBytes,omitempty"`

	NetRxBytesPerSec *float64 `json:"netRxBytesPerSec,omitempty"`
	NetTxBytesPerSec *float64 `json:"netTxBytesPerSec,omitempty"`

	DiskTotalBytes uint64 `json:"diskTotalBytes,omitempty"`
	DiskUsedBytes  uint64 `json:"diskUsedBytes,omitempty"`

	UptimeSec int64 `json:"uptimeSec,omitempty"`
}

// handleTerminalStats reports the resource usage of the host a session runs on:
// the dashboard host for a local shell, the remote host for an SSH session.
func (s *Server) handleTerminalStats(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user := userFrom(r.Context())
	sess, err := s.term.Get(id, user.ID, false)
	if err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "terminal session not found")
		return
	}
	meta := sess.Metadata()

	if meta.Kind == terminal.KindSSH {
		writeJSON(w, http.StatusOK, s.remoteStatsView(r.Context(), meta.Target))
		return
	}
	writeJSON(w, http.StatusOK, s.localStatsView())
}

// remoteStatsView probes an SSH host through the connection manager.
func (s *Server) remoteStatsView(ctx context.Context, alias string) sessionStatsView {
	view := sessionStatsView{Scope: "remote", Alias: alias}
	if !s.cfg.SSHStats {
		view.Message = "Metrik host remote dimatikan (DASH_SSH_STATS=false)."
		return view
	}
	if s.ssh == nil || !s.ssh.Enabled() {
		view.Message = "Manajer SSH tidak aktif."
		return view
	}
	stats, err := s.ssh.HostStats(ctx, alias)
	if err != nil {
		view.Message = err.Error()
		return view
	}
	view.Available = true
	view.Host = stats.Host
	view.CollectedAt = stats.CollectedAt.UTC().Format(time.RFC3339)
	view.CPUPercent = stats.CPUPercent
	view.CPUCount = stats.CPUCount
	view.Load1 = stats.Load1
	view.MemTotalBytes = stats.MemTotalBytes
	view.MemUsedBytes = stats.MemUsedBytes
	view.SwapTotalBytes = stats.SwapTotalBytes
	view.SwapUsedBytes = stats.SwapUsedBytes
	view.NetRxBytesPerSec = stats.NetRxBytesPerSec
	view.NetTxBytesPerSec = stats.NetTxBytesPerSec
	view.DiskTotalBytes = stats.DiskTotalBytes
	view.DiskUsedBytes = stats.DiskUsedBytes
	view.UptimeSec = stats.UptimeSec
	return view
}

// localStatsView reuses the background host sampler: a local shell runs on the
// dashboard host, so those numbers are already the right ones and cost nothing.
func (s *Server) localStatsView() sessionStatsView {
	view := sessionStatsView{Scope: "local"}
	if s.monitor == nil {
		view.Message = "Metrik host tidak tersedia."
		return view
	}
	snap, ok := s.monitor.Latest()
	if !ok {
		view.Message = "Metrik host belum siap."
		return view
	}
	view.Available = true
	view.Host = snap.Host.Hostname
	view.CollectedAt = snap.CollectedAt.UTC().Format(time.RFC3339)
	cpu := snap.CPU.UsagePercent
	view.CPUPercent = &cpu
	view.CPUCount = snap.CPU.Cores
	view.Load1 = snap.Load.Load1
	view.MemTotalBytes = snap.Memory.TotalBytes
	view.MemUsedBytes = snap.Memory.UsedBytes
	view.SwapTotalBytes = snap.Memory.SwapTotalBytes
	view.SwapUsedBytes = snap.Memory.SwapUsedBytes
	rx, tx := snap.Network.RxBytesPerSec, snap.Network.TxBytesPerSec
	view.NetRxBytesPerSec = &rx
	view.NetTxBytesPerSec = &tx
	view.DiskTotalBytes, view.DiskUsedBytes = rootDiskUsage(snap.Disks)
	view.UptimeSec = snap.Host.UptimeSec
	return view
}

// rootDiskUsage prefers the filesystem mounted on /, falling back to the totals
// of every mounted filesystem when / is not one of them (containers, chroots).
func rootDiskUsage(disks sysinfo.Disks) (total, used uint64) {
	for _, fs := range disks.Filesystems {
		if fs.Mount == "/" {
			return fs.TotalBytes, fs.UsedBytes
		}
	}
	return disks.TotalBytes, disks.UsedBytes
}

// handleTerminalDelete closes a session and its process group.
func (s *Server) handleTerminalDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user := userFrom(r.Context())
	if err := s.term.Delete(r.Context(), id, user.ID, false, user.Username); err != nil {
		if errors.Is(err, terminal.ErrNoSession) {
			writeError(w, http.StatusNotFound, CodeNotFound, "terminal session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, CodeInternal, "could not close terminal session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// handleTerminalStream upgrades the connection and pipes bytes between the
// websocket client and the session's PTY.
//
// Concurrency contract:
//   - exactly one goroutine writes to the websocket (gorilla requirement);
//   - the outbound channel is never closed, because a panic inside a
//     background goroutine is not caught by the HTTP recover middleware and
//     would crash the process. Both goroutines exit on context cancellation.
func (s *Server) handleTerminalStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user := userFrom(r.Context())

	sess, err := s.term.Get(id, user.ID, false)
	if err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "terminal session not found")
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written an error response.
		slog.Warn("websocket upgrade failed", "id", id, "error", err)
		return
	}
	defer conn.Close()

	conn.SetReadLimit(wsMaxMessageSize)
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})

	replay, chunks, unsubscribe := sess.Subscribe()
	defer unsubscribe()

	s.audit.Log(r.Context(), audit.Entry{
		UserID: user.ID, Username: user.Username, Action: audit.ActionTerminalConnect,
		Target: id, IP: s.clientIP(r), Success: true,
	})

	ctx, cancelCtx := context.WithCancel(r.Context())
	defer cancelCtx()

	outbound := make(chan wsMessage, 256)

	var wg sync.WaitGroup

	// Single writer goroutine: gorilla allows only one concurrent writer.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer recoverGoroutine("terminal-writer")
		ticker := time.NewTicker(wsPingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-outbound:
				_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
				if err := conn.WriteJSON(msg); err != nil {
					return
				}
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait)); err != nil {
					return
				}
			}
		}
	}()

	// PTY output pump. It stops as soon as the context is cancelled, so it can
	// neither outlive the connection nor block forever on a full buffer.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer recoverGoroutine("terminal-pump")
		send := func(m wsMessage) bool {
			select {
			case outbound <- m:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if len(replay) > 0 && !send(wsMessage{Type: "output", Data: string(replay)}) {
			return
		}
		for chunk := range chunks {
			if !send(wsMessage{Type: "output", Data: string(chunk)}) {
				return
			}
		}
		// chunks closed: the process exited or the session was closed.
		send(wsMessage{Type: "exit", Code: sess.ExitCode()})
	}()

	// Announce readiness with the current geometry.
	select {
	case outbound <- wsMessage{Type: "ready", ID: id}:
	case <-ctx.Done():
	}

	// Reader loop (runs in this goroutine).
	alive := true
	for alive {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure, websocket.CloseAbnormalClosure) {
				slog.Debug("terminal websocket closed", "id", id, "error", err)
			}
			break
		}
		var msg wsMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "input":
			if msg.Data == "" {
				continue
			}
			if _, err := sess.Write([]byte(msg.Data)); err != nil {
				alive = false
			}
		case "resize":
			_ = sess.Resize(msg.Cols, msg.Rows)
		case "ping":
			select {
			case outbound <- wsMessage{Type: "pong"}:
			case <-ctx.Done():
				alive = false
			}
		}
	}

	// Cancel first so both goroutines stop, then wait for a clean teardown.
	cancelCtx()
	wg.Wait()
}

// recoverGoroutine converts a panic inside a background goroutine into a log
// entry instead of a process-wide crash.
func recoverGoroutine(name string) {
	if rec := recover(); rec != nil {
		slog.Error("background goroutine panic recovered",
			"goroutine", name, "panic", rec, "stack", string(debug.Stack()))
	}
}
