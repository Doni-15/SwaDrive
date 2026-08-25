# ADR-0005: Use an NVMe Metadata Plane and HDD Content Plane

- **Status:** Accepted
- **Scope:** Backend v1 storage architecture

## Context

SwaDrive's production target separates fast always-on NVMe storage from a
large HDD. Directory browsing, metadata lookup, filename/path search, upload
status, trash listing, sessions, and audit history are frequent small reads.
Walking or statting the user filesystem for those operations would wake the
HDD and turn ordinary navigation into random data-disk I/O.

Filesystem bytes remain authoritative and SQLite cannot atomically commit with
a filesystem rename. The metadata view must therefore be rebuildable without
discarding the last complete view if an administrator rebuild is interrupted.

## Decision

Use `/var/lib/personalcloud` on NVMe as the control and metadata plane. SQLite
stores authentication/session state, audit state, upload/trash operational
state, and a metadata-only `file_entries` index. Entries contain logical paths,
parent/name/search values, kind, size, timestamps, an optional checksum only
when already known, and an optional trash association. They never contain file
content or physical `/srv` paths.

Use `/srv/personalcloud` on HDD as the content plane. Its `files/`, `uploads/`,
and `trash/` directories must share one filesystem so upload publication,
trash, and restore can use no-overwrite same-filesystem rename semantics. The
storage manager compares their filesystem device IDs at open time and rejects a
split configuration.

The invariant is **metadata plane must not wake the data disk**. Normal list,
metadata, search, trash listing, and upload-status requests use SQLite only.
Download first resolves SQLite metadata and then opens the requested file.
Uploads, mkdir, move, trash, restore, bounded recovery of known interrupted
objects, and explicit administrator reindex may access the HDD.

The metadata index uses generations. Reindex creates a `building` generation,
walks `files/` and known trash objects sequentially outside transactions,
inserts in bounded short transactions, validates the row count, and atomically
switches the singleton active-generation pointer. The old active generation is
then obsolete and cleaned in bounded batches. A failure before the switch
leaves the prior generation active. Reindex is a local `swadrive-admin`
operation, has no HTTP endpoint, excludes `uploads/`, and is never launched by
normal startup or browsing.

Normal mutations maintain the active generation incrementally. Mkdir inserts
one row; move updates the root and descendant paths; trash associates and hides
the retained subtree; restore clears that association; upload completion adds
the file row only after publication. Each operation combines its SQLite index,
operational state, and audit changes in an explicit transaction after the
filesystem step. A shared process-local mutation coordinator prevents visible
file operations and upload publication from interleaving across that boundary.

Because filesystem and SQLite cannot share a transaction, safe compensation is
attempted when the SQLite step fails. If compensation also fails, the index is
marked unhealthy and metadata reads fail closed until explicit reindex. Startup
reconciliation examines only durable trash and `finalizing` upload records in
bounded batches; it never performs a general HDD scan.

Backend v1 indexes metadata only. It does not extract document text, EXIF,
thumbnails, media frames, embeddings, faces, or background full-file hashes.
The bundled pure-Go SQLite includes FTS5, but ordinary parameterized metadata
search is used to avoid a second synchronized index for the initial product.

## Consequences

- Normal browsing/search/status traffic remains on the NVMe/SQLite plane and
  does not wake the HDD.
- Reindex can wake and traverse the HDD, but only as an explicit exceptional
  local administrator action.
- A crash during generation construction cannot replace the last complete
  active metadata view with a partial one.
- Direct out-of-band changes to `/srv/personalcloud/files` are not visible
  until reindex; application mutations must use the service/repository paths.
- Contains-search may scan the active SQLite generation even though results and
  pages are bounded.
- Locks and index-health state assume one backend process owns each database
  and storage root. Multi-process storage ownership requires a later design.
- SQLite indexes are derived and repairable, but temporary filesystem/index
  disagreement remains possible across crashes; the backend detects known
  states, compensates where safe, and fails closed rather than claiming perfect
  consistency.

## Alternatives Rejected

- **Walk the HDD for every list/search:** rejected because browsing would wake
  and randomly scan the content disk.
- **Delete and rebuild the current index in place:** rejected because a crash
  would leave the only metadata view partial.
- **Automatically reindex at every startup:** rejected because ordinary restart
  must not cause a full HDD scan.
- **Store file bytes or extracted content in SQLite:** rejected because NVMe is
  the control/metadata plane, not a duplicate content store.
- **Add FTS5 plus synchronization triggers immediately:** rejected because
  ordinary metadata search is sufficient for v1 and has fewer derived states.
