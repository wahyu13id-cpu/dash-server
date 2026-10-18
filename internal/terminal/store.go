package terminal

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"dashboard/internal/db"
)

// Session statuses.
const (
	StatusRunning = "running"
	StatusExited  = "exited"
	StatusClosed  = "closed"
)

// Metadata is the persisted description of a terminal session.
type Metadata struct {
	ID     string `json:"id"`
	UserID int64  `json:"userId"`
	Title  string `json:"title"`
	Shell  string `json:"shell"`
	// Kind is "shell" for a local PTY and "ssh" for a session that runs the ssh
	// client against a host alias.
	Kind string `json:"kind"`
	// Target is the SSH alias for kind "ssh"; empty for a local shell.
	Target    string `json:"target"`
	Status    string `json:"status"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
	CreatedAt string `json:"createdAt"`
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt"`
	ExitCode  *int   `json:"exitCode"`
}

// Store persists terminal session metadata. PTY I/O is never stored.
type Store struct {
	conn *sql.DB
}

// NewStore creates a metadata store.
func NewStore(conn *sql.DB) *Store { return &Store{conn: conn} }

// Insert records a newly created session.
func (s *Store) Insert(ctx context.Context, m Metadata) error {
	if m.Kind == "" {
		m.Kind = KindShell
	}
	_, err := s.conn.ExecContext(ctx,
		`INSERT INTO terminal_sessions (id, user_id, title, shell, kind, target, status, cols, rows, created_at, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.UserID, m.Title, m.Shell, m.Kind, m.Target, m.Status, m.Cols, m.Rows, m.CreatedAt, nullStr(m.StartedAt))
	return err
}

// MarkRunning sets status to running and stamps started_at.
func (s *Store) MarkRunning(ctx context.Context, id string) error {
	_, err := s.conn.ExecContext(ctx,
		`UPDATE terminal_sessions SET status = ?, started_at = ? WHERE id = ?`,
		StatusRunning, db.NormalizeTime(time.Now()), id)
	return err
}

// MarkEnded records the terminal status and exit code.
func (s *Store) MarkEnded(ctx context.Context, id, status string, exitCode *int) error {
	var code any
	if exitCode != nil {
		code = *exitCode
	}
	_, err := s.conn.ExecContext(ctx,
		`UPDATE terminal_sessions SET status = ?, ended_at = ?, exit_code = ? WHERE id = ?`,
		status, db.NormalizeTime(time.Now()), code, id)
	return err
}

// MarkStaleClosed closes sessions left "running" by a previous server run.
func (s *Store) MarkStaleClosed(ctx context.Context) (int64, error) {
	res, err := s.conn.ExecContext(ctx,
		`UPDATE terminal_sessions SET status = ?, ended_at = ? WHERE status IN (?, ?)`,
		StatusClosed, db.NormalizeTime(time.Now()), StatusRunning, "starting")
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ListByUser returns recent sessions for a user.
func (s *Store) ListByUser(ctx context.Context, userID int64, limit int) ([]Metadata, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := s.conn.QueryContext(ctx,
		`SELECT id, user_id, title, shell, kind, target, status, cols, rows,
		        created_at, COALESCE(started_at, ''), COALESCE(ended_at, ''), exit_code
		 FROM terminal_sessions WHERE user_id = ? ORDER BY created_at DESC LIMIT ?`,
		userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMetadata(rows)
}

// Get loads a single session's metadata.
func (s *Store) Get(ctx context.Context, id string) (*Metadata, error) {
	rows, err := s.conn.QueryContext(ctx,
		`SELECT id, user_id, title, shell, kind, target, status, cols, rows,
		        created_at, COALESCE(started_at, ''), COALESCE(ended_at, ''), exit_code
		 FROM terminal_sessions WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list, err := scanMetadata(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, sql.ErrNoRows
	}
	return &list[0], nil
}

// CountLive counts sessions persisted as running for a user.
func (s *Store) CountLive(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.conn.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM terminal_sessions WHERE user_id = ? AND status = ?`,
		userID, StatusRunning).Scan(&n)
	return n, err
}

func scanMetadata(rows *sql.Rows) ([]Metadata, error) {
	var out []Metadata
	for rows.Next() {
		var (
			m        Metadata
			exitCode sql.NullInt64
		)
		if err := rows.Scan(&m.ID, &m.UserID, &m.Title, &m.Shell, &m.Kind, &m.Target,
			&m.Status, &m.Cols, &m.Rows, &m.CreatedAt, &m.StartedAt, &m.EndedAt, &exitCode); err != nil {
			return nil, err
		}
		if exitCode.Valid {
			c := int(exitCode.Int64)
			m.ExitCode = &c
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ErrNoSession indicates the requested session does not exist.
var ErrNoSession = errors.New("terminal session not found")

// ErrTooManySessions indicates a session cap was reached.
var ErrTooManySessions = errors.New("terminal session limit reached")
