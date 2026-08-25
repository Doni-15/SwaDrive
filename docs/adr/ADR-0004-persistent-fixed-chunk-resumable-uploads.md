# ADR-0004: Use Persistent Fixed-Chunk Resumable Uploads

- **Status:** Accepted
- **Scope:** Backend v1 architecture

## Context

SwaDrive must upload both small files and files much larger than available
memory across unstable mobile networks. Interrupted transfers need to resume
after a request failure or server restart without exposing partial files in the
normal file tree. Completion must reject missing or corrupt content and must
not silently replace an existing destination.

Adopting a separate small-file route would duplicate transfer and integrity
rules. Storing one temporary file per chunk would also multiply filesystem
objects and require a second assembly pass or copy at completion.

## Decision

Use one server-side upload session protocol for all file sizes. Upload creation
persists the logical target path, total size, a fixed chunk size, total chunk
count, expiration, status, and an optional whole-file SHA-256. Allowed chunk
sizes are 1, 2, 4, 8, and 16 MiB; 4 MiB is the default and 16 MiB is the
maximum.

Each upload owns one randomly named `.part` file below the internal `uploads/`
root. Chunks are written at deterministic offsets. SQLite records each verified
chunk's index, offset, size, SHA-256, and receipt time under a composite primary
key. The server verifies exact expected length and the client-supplied chunk
checksum before recording progress. Retrying the same index with the same
length and checksum succeeds idempotently; conflicting content is rejected.
The body is streamed through a 64 KiB buffer into a deterministic offset; a
complete chunk is never buffered in RAM. Different indexes may hold shared
per-upload access and write concurrently. A temporary per-index gate
serializes same-index retries, while completion, cancellation, and cleanup take
exclusive per-upload access and wait for in-flight chunks.

Completion is serialized per upload. It requires every chunk, verifies the
part-file size and optional whole-file SHA-256, syncs the file, changes the
database status to `finalizing`, and atomically renames the part file within the
same configured storage root into `files/`. It then marks the upload
`completed` and inserts the active metadata-index row. The intermediate status
lets a restarted service reconcile a rename that succeeded before the final
status/index update. Existing destinations are never overwritten. The
completed status, file-index insertion, and completion audit event commit in
one SQLite transaction, so a failure leaves the durable `finalizing` recovery
state and marks known metadata disagreement unhealthy.

Before HTTP serving, startup inspects only known `finalizing` uploads in bounded
batches. Part present/destination absent resets to pending; part absent/
destination present confirms the index and completes with audit; both present
or both absent stops startup. This is targeted reconciliation, not a content
tree scan. A shared process-local mutation coordinator prevents a concurrent
file move/trash from interleaving between publication and index commit.

Unfinished uploads expire after 24 hours. A cancellable periodic worker removes
expired part files and marks their database sessions expired. Creation checks
space for the complete upload plus a configurable reserve, and chunk receipt
rechecks space. The default reserve is 1 GiB.
These checks are advisory rather than reservations. Server-wide chunk streams
default to eight, active uploads are capped at 100 per user, and each upload is
capped at 1,000,000 chunks. With the smallest chunk this still permits roughly
1 TiB; larger chunk selections permit proportionally larger files.

Creation necessarily has a narrow filesystem-before-SQLite window in which an
internal `.part` can exist without an upload row. It is never published or
visible through metadata APIs. Recovery is an explicit offline local-admin
operation: `reconcile-upload-parts` first reports a dry-run, scans only the
internal uploads directory in bounded batches, and considers only strict
lowercase 128-bit-hex `.part` names that are regular files, older than the
configured minimum age, and absent from SQLite. Deletion requires `-apply`.
The command never reads content or prints internal names/host paths. A scan-limit
failure performs no partial deletion.

If cancel/expiration removes a known part and the process dies before updating
SQLite, a restarted expiration cleanup treats the missing part as already
removed and durably expires the known pending row. It never creates an index
entry.

Downloads use standard HTTP byte Range behavior rather than a separate
download-session protocol.

## Consequences

- Small and large files share one integrity and publication path.
- Upload progress survives process restarts without storing file bytes in
  SQLite or retaining a separate file for every chunk.
- Chunk-copy memory is about 64 KiB per active request rather than proportional
  to the selected 1-16 MiB chunk size.
- Partial content remains outside normal listing, search, and download APIs
  until atomic publication succeeds.
- Normal upload-status reads and partial uploads remain on the SQLite metadata
  plane and never require an HDD tree scan.
- Clients must retain the upload ID, selected chunk size, and source-file
  identity needed to resume safely.
- Per-upload synchronization is process-local; backend v1 assumes one server
  process owns a configured database and storage root.
- Preflight checks reduce disk-exhaustion risk but are not quota reservation;
  concurrent uploads and unrelated writers may consume space between checks.

## Alternatives Rejected

- **Separate direct upload for small files:** rejected because it duplicates
  validation, authorization, audit, conflict, and finalization behavior.
- **JWT or client-only upload progress:** rejected because restart-safe progress
  and authoritative integrity state belong on the server.
- **One temporary file per chunk:** rejected because it creates more filesystem
  objects and requires assembly work at completion.
- **A fixed 8 MiB chunk for every client:** rejected because unstable mobile
  links benefit from 1-4 MiB chunks while stable clients may choose 8 or 16 MiB.
- **Silent destination overwrite:** rejected because an upload must not destroy
  an existing file without an explicit future overwrite policy.
- **TUS in backend v1:** rejected as unnecessary protocol/framework complexity
  for the current private single-server requirements; it may be reconsidered
  through a superseding decision if interoperability needs change.
