-- SSH connection manager.
--
-- Session kind/target let the UI tell a local shell apart from an SSH session
-- and show which host alias it connects to.
ALTER TABLE terminal_sessions ADD COLUMN kind   TEXT NOT NULL DEFAULT 'shell';
ALTER TABLE terminal_sessions ADD COLUMN target TEXT NOT NULL DEFAULT '';

-- Stored SSH credentials. ciphertext is AES-256-GCM sealed with a key derived
-- from DASH_SESSION_SECRET; the AAD binds each row to its alias+kind so a
-- ciphertext cannot be moved between hosts.
CREATE TABLE IF NOT EXISTS ssh_secrets (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    alias      TEXT    NOT NULL,
    kind       TEXT    NOT NULL,
    nonce      TEXT    NOT NULL,
    ciphertext TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_ssh_secrets_alias_kind ON ssh_secrets(alias, kind);

-- Key-check value proving the current DASH_SESSION_SECRET can still decrypt the
-- stored secrets, so a rotated secret produces a clear message instead of a
-- silent authentication failure.
CREATE TABLE IF NOT EXISTS ssh_key_check (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    nonce      TEXT    NOT NULL,
    ciphertext TEXT    NOT NULL,
    created_at TEXT    NOT NULL
);

-- Presentation-only metadata. Kept out of ~/.ssh/config on purpose so the file
-- stays standard OpenSSH and interchangeable with other tools.
CREATE TABLE IF NOT EXISTS ssh_host_meta (
    alias             TEXT    PRIMARY KEY,
    favorite          INTEGER NOT NULL DEFAULT 0,
    note              TEXT    NOT NULL DEFAULT '',
    last_connected_at TEXT,
    connect_count     INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_terminal_sessions_kind ON terminal_sessions(kind);
