CREATE TRIGGER uploads_reject_excessive_chunk_count_insert
BEFORE INSERT ON uploads
WHEN NEW.total_chunks > 1000000
BEGIN
    SELECT RAISE(ABORT, 'upload chunk count exceeds server bound');
END;

CREATE TRIGGER uploads_reject_excessive_chunk_count_update
BEFORE UPDATE OF total_size, chunk_size, total_chunks ON uploads
WHEN NEW.total_chunks > 1000000
BEGIN
    SELECT RAISE(ABORT, 'upload chunk count exceeds server bound');
END;
