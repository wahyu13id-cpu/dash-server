package sshsecret

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Store persists encrypted credentials in SQLite.
type Store struct {
	conn *sql.DB
}

// NewStore creates a credential store.
func NewStore(conn *sql.DB) *Store { return &Store{conn: conn} }

// Record is one encrypted credential as stored.
type Record struct {
	Nonce      string
	Ciphertext string
}

// Set upserts the credential for (alias, kind).
func (s *Store) Set(ctx context.Context, alias string, kind Kind, nonce, ciphertext string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.conn.ExecContext(ctx, `
		INSERT INTO ssh_secrets (alias, kind, nonce, ciphertext, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(alias, kind) DO UPDATE SET
			nonce      = excluded.nonce,
			ciphertext = excluded.ciphertext,
			updated_at = excluded.updated_at`,
		alias, string(kind), nonce, ciphertext, now, now)
	if err != nil {
		return fmt.Errorf("simpan kredensial: %w", err)
	}
	return nil
}

// Get loads the credential for (alias, kind).
func (s *Store) Get(ctx context.Context, alias string, kind Kind) (Record, bool, error) {
	row := s.conn.QueryRowContext(ctx,
		`SELECT nonce, ciphertext FROM ssh_secrets WHERE alias = ? AND kind = ?`,
		alias, string(kind))
	var rec Record
	if err := row.Scan(&rec.Nonce, &rec.Ciphertext); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, false, nil
		}
		return Record{}, false, fmt.Errorf("baca kredensial: %w", err)
	}
	return rec, true, nil
}

// Kinds reports which kinds are stored for alias.
func (s *Store) Kinds(ctx context.Context, alias string) (map[Kind]bool, error) {
	rows, err := s.conn.QueryContext(ctx, `SELECT kind FROM ssh_secrets WHERE alias = ?`, alias)
	if err != nil {
		return nil, fmt.Errorf("baca kredensial: %w", err)
	}
	defer rows.Close()

	out := map[Kind]bool{}
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			return nil, err
		}
		out[Kind(kind)] = true
	}
	return out, rows.Err()
}

// Delete removes one credential, reporting whether a row existed.
func (s *Store) Delete(ctx context.Context, alias string, kind Kind) (bool, error) {
	res, err := s.conn.ExecContext(ctx,
		`DELETE FROM ssh_secrets WHERE alias = ? AND kind = ?`, alias, string(kind))
	if err != nil {
		return false, fmt.Errorf("hapus kredensial: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, nil
	}
	return n > 0, nil
}

// DeleteAlias removes every credential of an alias, used when a host is deleted.
func (s *Store) DeleteAlias(ctx context.Context, alias string) (int64, error) {
	res, err := s.conn.ExecContext(ctx, `DELETE FROM ssh_secrets WHERE alias = ?`, alias)
	if err != nil {
		return 0, fmt.Errorf("hapus kredensial: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return n, nil
}

// Aliases lists aliases that have at least one stored credential.
func (s *Store) Aliases(ctx context.Context) ([]string, error) {
	rows, err := s.conn.QueryContext(ctx, `SELECT DISTINCT alias FROM ssh_secrets ORDER BY alias`)
	if err != nil {
		return nil, fmt.Errorf("daftar kredensial: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return nil, err
		}
		out = append(out, alias)
	}
	return out, rows.Err()
}

// PurgeAll drops every stored credential and the key-check blob.
func (s *Store) PurgeAll(ctx context.Context) (int64, error) {
	res, err := s.conn.ExecContext(ctx, `DELETE FROM ssh_secrets`)
	if err != nil {
		return 0, fmt.Errorf("hapus semua kredensial: %w", err)
	}
	if _, err := s.conn.ExecContext(ctx, `DELETE FROM ssh_key_check`); err != nil {
		return 0, fmt.Errorf("hapus key check: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return n, nil
}

// KeyCheck loads the key-check blob.
func (s *Store) KeyCheck(ctx context.Context) (Record, bool, error) {
	row := s.conn.QueryRowContext(ctx, `SELECT nonce, ciphertext FROM ssh_key_check WHERE id = 1`)
	var rec Record
	if err := row.Scan(&rec.Nonce, &rec.Ciphertext); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, false, nil
		}
		return Record{}, false, fmt.Errorf("baca key check: %w", err)
	}
	return rec, true, nil
}

// SetKeyCheck writes the key-check blob if none exists yet.
func (s *Store) SetKeyCheck(ctx context.Context, nonce, ciphertext string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.conn.ExecContext(ctx, `
		INSERT INTO ssh_key_check (id, nonce, ciphertext, created_at)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`, nonce, ciphertext, now)
	if err != nil {
		return fmt.Errorf("simpan key check: %w", err)
	}
	return nil
}
