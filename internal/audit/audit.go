// Package audit records security-relevant actions in a durable, structured
// trail. It never stores terminal output or file contents.
package audit

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"dashboard/internal/db"
)

// Action identifiers used across the application.
const (
	ActionLogin           = "auth.login"
	ActionLogout          = "auth.logout"
	ActionLoginFailed     = "auth.login_failed"
	ActionTOTPChallenge   = "auth.totp_challenge"
	ActionTOTPSetup       = "auth.totp_setup"
	ActionTOTPEnabled     = "auth.totp_enabled"
	ActionTOTPDisabled    = "auth.totp_disabled"
	ActionTOTPFailed      = "auth.totp_failed"
	ActionTOTPRecovery    = "auth.totp_recovery"
	ActionRecoveryUsed    = "auth.recovery_used"
	ActionProfileUpdate   = "auth.profile_update"
	ActionFileList        = "file.list"
	ActionFileRead        = "file.read"
	ActionFileWrite       = "file.write"
	ActionFileCreate      = "file.create"
	ActionFileMkdir       = "file.mkdir"
	ActionFileRename      = "file.rename"
	ActionFileUpload      = "file.upload"
	ActionFileDownload    = "file.download"
	ActionFileDelete      = "file.delete"
	ActionFileCopy        = "file.copy"
	ActionFileMove        = "file.move"
	ActionFileZip         = "file.zip"
	ActionFileUnzip       = "file.unzip"
	ActionFileChmod       = "file.chmod"
	ActionFileStat        = "file.stat"
	ActionTerminalStart   = "terminal.start"
	ActionTerminalStop    = "terminal.stop"
	ActionTerminalConnect = "terminal.connect"
	ActionSecurityDenied  = "security.denied"
	ActionSSHHostCreate   = "ssh.host_create"
	ActionSSHHostUpdate   = "ssh.host_update"
	ActionSSHHostDelete   = "ssh.host_delete"
	ActionSSHConnect      = "ssh.connect"
	ActionSSHHostKeyTrust = "ssh.hostkey_trust"
	ActionSSHSecretSet    = "ssh.secret_set"
	ActionSSHSecretClear  = "ssh.secret_clear"
	ActionSSHConfigWrite  = "ssh.config_write"
)

// Entry describes a single audited event.
type Entry struct {
	UserID   int64
	Username string
	Action   string
	Target   string
	Detail   string
	IP       string
	Success  bool
}

// Actor identifies who performed an action. It travels in the request context
// so business-logic packages can log attributed events without the HTTP layer
// threading user details through every call.
type Actor struct {
	UserID   int64
	Username string
	IP       string
}

type actorKey struct{}

// WithActor attaches actor information to a context.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom extracts actor information from a context.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	return a, ok
}

// Logger writes audit entries. Writes are best-effort: a failure to persist an
// audit row is logged but never blocks the user operation that triggered it.
type Logger struct {
	conn *sql.DB
}

// NewLogger creates an audit logger backed by conn.
func NewLogger(conn *sql.DB) *Logger { return &Logger{conn: conn} }

// Log persists a single entry, filling in any missing attribution from the
// request context actor.
func (l *Logger) Log(ctx context.Context, e Entry) {
	if l == nil || l.conn == nil {
		return
	}
	if actor, ok := ActorFrom(ctx); ok {
		if e.UserID == 0 {
			e.UserID = actor.UserID
		}
		if e.Username == "" {
			e.Username = actor.Username
		}
		if e.IP == "" {
			e.IP = actor.IP
		}
	}
	success := 1
	if !e.Success {
		success = 0
	}
	_, err := l.conn.ExecContext(ctx,
		`INSERT INTO audit_logs (created_at, user_id, username, action, target, detail, ip, success)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		db.NormalizeTime(time.Now()), nullInt(e.UserID), e.Username, e.Action,
		truncate(e.Target, 512), truncate(e.Detail, 512), truncate(e.IP, 128), success,
	)
	if err != nil {
		slog.Error("failed to write audit log", "action", e.Action, "error", err)
	}
}

// Prune deletes audit rows older than the given time and returns the number of
// removed rows. It keeps the audit table from growing without bound.
func (l *Logger) Prune(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := l.conn.ExecContext(ctx,
		`DELETE FROM audit_logs WHERE created_at < ?`, db.NormalizeTime(olderThan))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Recent returns the newest audit entries, capped at limit.
func (l *Logger) Recent(ctx context.Context, limit int) ([]Record, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := l.conn.QueryContext(ctx,
		`SELECT id, created_at, COALESCE(user_id, 0), username, action, target, detail, ip, success
		 FROM audit_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Record, 0, limit)
	for rows.Next() {
		var (
			r       Record
			success int
		)
		if err := rows.Scan(&r.ID, &r.CreatedAt, &r.UserID, &r.Username, &r.Action,
			&r.Target, &r.Detail, &r.IP, &success); err != nil {
			return nil, err
		}
		r.Success = success == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// Record is an audit entry read back from storage.
type Record struct {
	ID        int64  `json:"id"`
	CreatedAt string `json:"createdAt"`
	UserID    int64  `json:"userId"`
	Username  string `json:"username"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	Detail    string `json:"detail"`
	IP        string `json:"ip"`
	Success   bool   `json:"success"`
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
