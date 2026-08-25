CREATE TABLE audit_events (
    id INTEGER PRIMARY KEY,
    occurred_at INTEGER NOT NULL,
    actor_user_id INTEGER REFERENCES users(id) ON DELETE RESTRICT,
    actor_session_id INTEGER REFERENCES sessions(id) ON DELETE RESTRICT,
    event_type TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('success', 'failure', 'denied')),
    resource_type TEXT,
    resource_id TEXT,
    request_id TEXT,
    remote_ip TEXT,
    metadata_json TEXT,
    CHECK (length(event_type) BETWEEN 1 AND 128),
    CHECK (resource_type IS NULL OR length(resource_type) BETWEEN 1 AND 64),
    CHECK (resource_id IS NULL OR length(resource_id) BETWEEN 1 AND 256),
    CHECK (request_id IS NULL OR length(request_id) BETWEEN 1 AND 128),
    CHECK (remote_ip IS NULL OR length(remote_ip) BETWEEN 1 AND 64),
    CHECK (metadata_json IS NULL OR (length(metadata_json) <= 4096 AND json_valid(metadata_json)))
) STRICT;

CREATE INDEX audit_events_occurred_idx
    ON audit_events (occurred_at DESC, id DESC);

CREATE INDEX audit_events_type_idx
    ON audit_events (event_type, id DESC);

CREATE INDEX audit_events_actor_idx
    ON audit_events (actor_user_id, id DESC);

CREATE INDEX audit_events_outcome_idx
    ON audit_events (outcome, id DESC);

CREATE TRIGGER audit_events_reject_update
BEFORE UPDATE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events is append-only');
END;

CREATE TRIGGER audit_events_reject_delete
BEFORE DELETE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events is append-only');
END;

CREATE TABLE trash_entries (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    original_path TEXT NOT NULL,
    trash_name TEXT NOT NULL UNIQUE,
    trashed_at INTEGER NOT NULL,
    CHECK (length(id) BETWEEN 16 AND 128),
    CHECK (length(original_path) BETWEEN 1 AND 4096),
    CHECK (length(trash_name) BETWEEN 1 AND 256)
) STRICT;

CREATE INDEX trash_entries_user_time_idx
    ON trash_entries (user_id, trashed_at DESC, id DESC);

CREATE TABLE uploads (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    target_path TEXT NOT NULL,
    part_name TEXT NOT NULL UNIQUE,
    total_size INTEGER NOT NULL CHECK (total_size >= 0),
    chunk_size INTEGER NOT NULL CHECK (chunk_size IN (1048576, 2097152, 4194304, 8388608, 16777216)),
    total_chunks INTEGER NOT NULL CHECK (total_chunks >= 0),
    whole_sha256 BLOB,
    status TEXT NOT NULL CHECK (status IN ('pending', 'finalizing', 'completed', 'cancelled', 'expired')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    CHECK (length(id) BETWEEN 16 AND 128),
    CHECK (length(target_path) BETWEEN 1 AND 4096),
    CHECK (length(part_name) BETWEEN 1 AND 256),
    CHECK (whole_sha256 IS NULL OR length(whole_sha256) = 32),
    CHECK (total_chunks = CASE
        WHEN total_size = 0 THEN 0
        ELSE ((total_size - 1) / chunk_size) + 1
    END),
    CHECK (updated_at >= created_at),
    CHECK (expires_at > created_at)
) STRICT;

CREATE INDEX uploads_user_time_idx
    ON uploads (user_id, created_at DESC, id DESC);

CREATE INDEX uploads_cleanup_idx
    ON uploads (expires_at, id)
    WHERE status = 'pending';

CREATE TABLE upload_chunks (
    upload_id TEXT NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
    chunk_index INTEGER NOT NULL CHECK (chunk_index >= 0),
    byte_offset INTEGER NOT NULL CHECK (byte_offset >= 0),
    byte_size INTEGER NOT NULL CHECK (byte_size > 0 AND byte_size <= 16777216),
    sha256 BLOB NOT NULL CHECK (length(sha256) = 32),
    received_at INTEGER NOT NULL,
    PRIMARY KEY (upload_id, chunk_index)
) STRICT, WITHOUT ROWID;
