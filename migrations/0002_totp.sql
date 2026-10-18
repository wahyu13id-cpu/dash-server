-- Two-factor authentication (TOTP, RFC 6238) state.
--
-- The shared secret must be stored to verify codes; recovery codes are stored
-- only as SHA-256 hashes because they are high-entropy, single-use tokens.
-- totp_last_counter remembers the last accepted time step so a captured code
-- cannot be replayed within its validity window.

ALTER TABLE users ADD COLUMN totp_secret       TEXT    NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN totp_enabled      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN totp_confirmed_at TEXT;
ALTER TABLE users ADD COLUMN totp_last_counter INTEGER NOT NULL DEFAULT 0;

-- One-time backup codes, shown once at enrollment and consumed on first use.
CREATE TABLE IF NOT EXISTS totp_recovery_codes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash  TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    used_at    TEXT
);

CREATE INDEX IF NOT EXISTS idx_totp_recovery_user ON totp_recovery_codes(user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_totp_recovery_hash ON totp_recovery_codes(user_id, code_hash);
