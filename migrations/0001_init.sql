-- Initial schema for the dashboard server.
-- All timestamps are stored as UTC RFC3339 strings for portability.

CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT    NOT NULL,
    display_name  TEXT    NOT NULL DEFAULT '',
    role          TEXT    NOT NULL DEFAULT 'admin',
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
);

-- Server-side login sessions. Only a SHA-256 hash of the token is stored so a
-- database leak does not expose live session tokens.
CREATE TABLE IF NOT EXISTS auth_sessions (
    token_hash  TEXT    PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at  TEXT    NOT NULL,
    expires_at  TEXT    NOT NULL,
    last_seen   TEXT    NOT NULL,
    user_agent  TEXT    NOT NULL DEFAULT '',
    ip          TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_auth_sessions_user ON auth_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_expires ON auth_sessions(expires_at);

-- Metadata about PTY terminal sessions. PTY I/O is never persisted.
CREATE TABLE IF NOT EXISTS terminal_sessions (
    id          TEXT    PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title       TEXT    NOT NULL DEFAULT '',
    shell       TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'starting',
    cols        INTEGER NOT NULL DEFAULT 80,
    rows        INTEGER NOT NULL DEFAULT 24,
    created_at  TEXT    NOT NULL,
    started_at  TEXT,
    ended_at    TEXT,
    exit_code   INTEGER
);

CREATE INDEX IF NOT EXISTS idx_terminal_sessions_user ON terminal_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_terminal_sessions_status ON terminal_sessions(status);

-- Audit trail for security-relevant actions. Never stores command output or
-- file contents, only metadata.
CREATE TABLE IF NOT EXISTS audit_logs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at TEXT    NOT NULL,
    user_id    INTEGER,
    username   TEXT    NOT NULL DEFAULT '',
    action     TEXT    NOT NULL,
    target     TEXT    NOT NULL DEFAULT '',
    detail     TEXT    NOT NULL DEFAULT '',
    ip         TEXT    NOT NULL DEFAULT '',
    success    INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS idx_audit_logs_created ON audit_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_logs_action ON audit_logs(action);

-- Simple key/value store for application settings.
CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
