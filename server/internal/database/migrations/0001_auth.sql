-- Application timestamps are UTC Unix seconds throughout the Phase 4 schema.
CREATE TABLE users (
    id INTEGER PRIMARY KEY,
    username TEXT NOT NULL COLLATE NOCASE UNIQUE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('owner', 'member')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    disabled_at INTEGER,
    CHECK (length(trim(username)) BETWEEN 1 AND 128),
    CHECK (username = trim(username)),
    CHECK (length(password_hash) > 0),
    CHECK (updated_at >= created_at),
    CHECK (disabled_at IS NULL OR disabled_at >= created_at)
) STRICT;

CREATE TABLE sessions (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash BLOB NOT NULL UNIQUE,
    client_name TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    revoked_at INTEGER,
    last_seen_at INTEGER NOT NULL,
    CHECK (length(token_hash) = 32),
    CHECK (length(trim(client_name)) BETWEEN 1 AND 256),
    CHECK (client_name = trim(client_name)),
    CHECK (expires_at > created_at),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at),
    CHECK (last_seen_at >= created_at)
) STRICT;

CREATE INDEX sessions_user_created_idx
    ON sessions (user_id, created_at DESC);

CREATE INDEX sessions_active_expiry_idx
    ON sessions (expires_at)
    WHERE revoked_at IS NULL;
