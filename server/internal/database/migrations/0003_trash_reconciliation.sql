ALTER TABLE trash_entries
    ADD COLUMN state TEXT NOT NULL DEFAULT 'trashed'
    CHECK (state IN ('trashing', 'trashed', 'restoring'));

ALTER TABLE trash_entries
    ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;

CREATE INDEX trash_entries_reconciliation_idx
    ON trash_entries (state, updated_at, id)
    WHERE state != 'trashed';
