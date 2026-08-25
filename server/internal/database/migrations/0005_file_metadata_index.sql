CREATE TABLE file_index_generations (
    id INTEGER PRIMARY KEY,
    status TEXT NOT NULL CHECK (status IN ('building', 'active', 'obsolete')),
    started_at INTEGER NOT NULL,
    completed_at INTEGER,
    CHECK (
        (status = 'building' AND completed_at IS NULL) OR
        (status IN ('active', 'obsolete') AND completed_at IS NOT NULL)
    )
) STRICT;

CREATE UNIQUE INDEX file_index_single_active_generation_idx
    ON file_index_generations (status)
    WHERE status = 'active';

CREATE TABLE file_index_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    active_generation_id INTEGER NOT NULL REFERENCES file_index_generations(id) ON DELETE RESTRICT,
    healthy INTEGER NOT NULL DEFAULT 1 CHECK (healthy IN (0, 1)),
    unhealthy_reason TEXT,
    updated_at INTEGER NOT NULL,
    CHECK (
        (healthy = 1 AND unhealthy_reason IS NULL) OR
        (healthy = 0 AND length(unhealthy_reason) BETWEEN 1 AND 256)
    )
) STRICT;

CREATE TABLE file_entries (
    id INTEGER PRIMARY KEY,
    generation_id INTEGER NOT NULL REFERENCES file_index_generations(id) ON DELETE CASCADE,
    logical_path TEXT NOT NULL,
    parent_path TEXT NOT NULL,
    name TEXT NOT NULL,
    normalized_name TEXT NOT NULL,
    normalized_path TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('file', 'directory')),
    size INTEGER NOT NULL CHECK (size >= 0),
    modified_at INTEGER NOT NULL,
    indexed_at INTEGER NOT NULL,
    trash_entry_id TEXT,
    whole_sha256 BLOB,
    CHECK (length(CAST(logical_path AS BLOB)) BETWEEN 1 AND 4096),
    CHECK (length(CAST(parent_path AS BLOB)) BETWEEN 0 AND 4096),
    CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 255),
    CHECK (length(CAST(normalized_name AS BLOB)) BETWEEN 1 AND 255),
    CHECK (length(CAST(normalized_path AS BLOB)) BETWEEN 1 AND 4096),
    CHECK (trash_entry_id IS NULL OR length(trash_entry_id) BETWEEN 16 AND 128),
    CHECK (whole_sha256 IS NULL OR length(whole_sha256) = 32)
) STRICT;

CREATE UNIQUE INDEX file_entries_active_path_unique_idx
    ON file_entries (generation_id, logical_path)
    WHERE trash_entry_id IS NULL;

CREATE INDEX file_entries_parent_idx
    ON file_entries (generation_id, parent_path, normalized_name, name, logical_path)
    WHERE trash_entry_id IS NULL;

CREATE INDEX file_entries_normalized_name_idx
    ON file_entries (generation_id, normalized_name, logical_path)
    WHERE trash_entry_id IS NULL;

CREATE INDEX file_entries_normalized_path_idx
    ON file_entries (generation_id, normalized_path, logical_path)
    WHERE trash_entry_id IS NULL;

CREATE INDEX file_entries_modified_idx
    ON file_entries (generation_id, modified_at DESC, logical_path)
    WHERE trash_entry_id IS NULL;

CREATE INDEX file_entries_trash_idx
    ON file_entries (generation_id, trash_entry_id, logical_path)
    WHERE trash_entry_id IS NOT NULL;

CREATE INDEX file_entries_generation_idx
    ON file_entries (generation_id, id);

INSERT INTO file_index_generations (id, status, started_at, completed_at)
VALUES (1, 'active', unixepoch(), unixepoch());

INSERT INTO file_index_state (singleton, active_generation_id, healthy, unhealthy_reason, updated_at)
VALUES (1, 1, 1, NULL, unixepoch());
