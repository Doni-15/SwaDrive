ALTER TABLE audit_events
    ADD COLUMN resource_path TEXT
    CHECK (resource_path IS NULL OR length(CAST(resource_path AS BLOB)) BETWEEN 1 AND 4096);

ALTER TABLE audit_events
    ADD COLUMN destination_path TEXT
    CHECK (destination_path IS NULL OR length(CAST(destination_path AS BLOB)) BETWEEN 1 AND 4096);
