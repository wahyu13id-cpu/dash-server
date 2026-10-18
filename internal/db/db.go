// Package db opens the SQLite database and applies embedded migrations.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, no cgo required
)

// Open opens (and creates) the SQLite database at path and applies migrations.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)",
		path,
	)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single writer connection avoids SQLITE_BUSY contention on the WAL and
	// is more than enough for this workload.
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	conn.SetConnMaxLifetime(0)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := conn.PingContext(pingCtx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return conn, nil
}

// Migrate applies every embedded migration that has not run yet.
func Migrate(ctx context.Context, conn *sql.DB, files fs.FS) error {
	if _, err := conn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.Glob(files, "*.sql")
	if err != nil {
		return fmt.Errorf("glob migrations: %w", err)
	}
	sort.Strings(entries)

	applied := map[string]bool{}
	rows, err := conn.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("scan migration row: %w", err)
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate applied migrations: %w", err)
	}

	for _, name := range entries {
		if applied[name] {
			continue
		}
		body, err := fs.ReadFile(files, name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := applyOne(ctx, conn, name, string(body)); err != nil {
			return err
		}
		slog.Info("applied migration", "version", name)
	}
	return nil
}

func applyOne(ctx context.Context, conn *sql.DB, name, body string) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("execute migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		name, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, err)
	}
	return nil
}

// IsNoRows reports whether err is sql.ErrNoRows.
func IsNoRows(err error) bool { return err == sql.ErrNoRows }

// NormalizeTime truncates to seconds for stable RFC3339 storage.
func NormalizeTime(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// ParseTime parses an RFC3339 timestamp produced by NormalizeTime.
func ParseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, strings.TrimSpace(s))
}
