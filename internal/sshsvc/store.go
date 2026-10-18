package sshsvc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// HostMeta is presentation-only metadata. It deliberately lives in the database
// rather than in ~/.ssh/config so the config file stays standard OpenSSH and
// interchangeable with other tools.
type HostMeta struct {
	Alias           string `json:"alias"`
	Favorite        bool   `json:"favorite"`
	Note            string `json:"note"`
	LastConnectedAt string `json:"lastConnectedAt,omitempty"`
	ConnectCount    int    `json:"connectCount"`
}

// Store persists host metadata.
type Store struct {
	conn *sql.DB
}

// NewStore creates a metadata store.
func NewStore(conn *sql.DB) *Store { return &Store{conn: conn} }

// All loads every row keyed by alias.
func (s *Store) All(ctx context.Context) (map[string]HostMeta, error) {
	rows, err := s.conn.QueryContext(ctx, `
		SELECT alias, favorite, note, COALESCE(last_connected_at, ''), connect_count
		FROM ssh_host_meta`)
	if err != nil {
		return nil, fmt.Errorf("baca metadata host: %w", err)
	}
	defer rows.Close()

	out := map[string]HostMeta{}
	for rows.Next() {
		var (
			m    HostMeta
			fav  int
			last string
		)
		if err := rows.Scan(&m.Alias, &fav, &m.Note, &last, &m.ConnectCount); err != nil {
			return nil, err
		}
		m.Favorite = fav != 0
		m.LastConnectedAt = last
		out[m.Alias] = m
	}
	return out, rows.Err()
}

// Get loads one row.
func (s *Store) Get(ctx context.Context, alias string) (HostMeta, bool, error) {
	var (
		m    HostMeta
		fav  int
		last string
	)
	err := s.conn.QueryRowContext(ctx, `
		SELECT alias, favorite, note, COALESCE(last_connected_at, ''), connect_count
		FROM ssh_host_meta WHERE alias = ?`, alias).
		Scan(&m.Alias, &fav, &m.Note, &last, &m.ConnectCount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return HostMeta{}, false, nil
		}
		return HostMeta{}, false, fmt.Errorf("baca metadata host: %w", err)
	}
	m.Favorite = fav != 0
	m.LastConnectedAt = last
	return m, true, nil
}

// Upsert writes a full metadata row, replacing any existing one.
func (s *Store) Upsert(ctx context.Context, m HostMeta) error {
	_, err := s.conn.ExecContext(ctx, `
		INSERT INTO ssh_host_meta (alias, favorite, note, last_connected_at, connect_count)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(alias) DO UPDATE SET
			favorite          = excluded.favorite,
			note              = excluded.note,
			last_connected_at = excluded.last_connected_at,
			connect_count     = excluded.connect_count`,
		m.Alias, boolInt(m.Favorite), truncate(m.Note, 500), nullTime(m.LastConnectedAt), m.ConnectCount)
	if err != nil {
		return fmt.Errorf("simpan metadata host: %w", err)
	}
	return nil
}

// SetFavorite toggles the favourite flag, creating the row when needed.
func (s *Store) SetFavorite(ctx context.Context, alias string, favorite bool) error {
	_, err := s.conn.ExecContext(ctx, `
		INSERT INTO ssh_host_meta (alias, favorite, note, connect_count)
		VALUES (?, ?, '', 0)
		ON CONFLICT(alias) DO UPDATE SET favorite = excluded.favorite`,
		alias, boolInt(favorite))
	if err != nil {
		return fmt.Errorf("simpan favorit: %w", err)
	}
	return nil
}

// SetNote stores a free-form note.
func (s *Store) SetNote(ctx context.Context, alias, note string) error {
	note = truncate(strings.TrimSpace(note), 500)
	_, err := s.conn.ExecContext(ctx, `
		INSERT INTO ssh_host_meta (alias, favorite, note, connect_count)
		VALUES (?, 0, ?, 0)
		ON CONFLICT(alias) DO UPDATE SET note = excluded.note`,
		alias, note)
	if err != nil {
		return fmt.Errorf("simpan catatan: %w", err)
	}
	return nil
}

// Touch records a connection attempt.
func (s *Store) Touch(ctx context.Context, alias string) error {
	_, err := s.conn.ExecContext(ctx, `
		INSERT INTO ssh_host_meta (alias, favorite, note, last_connected_at, connect_count)
		VALUES (?, 0, '', ?, 1)
		ON CONFLICT(alias) DO UPDATE SET
			last_connected_at = excluded.last_connected_at,
			connect_count     = ssh_host_meta.connect_count + 1`,
		alias, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("catat pemakaian host: %w", err)
	}
	return nil
}

// Delete removes the metadata for a deleted host.
func (s *Store) Delete(ctx context.Context, alias string) error {
	if _, err := s.conn.ExecContext(ctx, `DELETE FROM ssh_host_meta WHERE alias = ?`, alias); err != nil {
		return fmt.Errorf("hapus metadata host: %w", err)
	}
	return nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// nullTime maps an empty timestamp to NULL.
func nullTime(s string) any {
	if s == "" {
		return nil
	}
	return s
}
