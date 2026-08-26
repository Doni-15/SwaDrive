# ADR-0006: Coordinate One Process and Verify Storage-Root Identity

- **Status:** Accepted
- **Scope:** Backend v1 operational boundary

## Context

Backend v1 uses process-local mutation locks and resource gates while SQLite
serializes writers. Running an unaware second server or local administrator
command against the same database could bypass those process-local guarantees.
Starting against an ordinary fallback directory when the intended content disk
is absent could also place user bytes on the wrong filesystem.

SQLite must be able to create its database, WAL, and SHM in the configured state
area. A file lock beside that database can coordinate cooperating processes, but
cannot defend against a hostile process with the same UID or another actor able
to replace entries in the writable state directory.

## Decision

The server and local administrator commands take a nonblocking flock derived
from the canonical database path. This excludes supported concurrent SwaDrive
processes using the same database, including normal symlink aliases. Backend v1
supports one process and one database/storage-root pair. Hard-link database
aliases and distinct databases aimed at one storage root are unsupported.

Before opening or initializing content subdirectories, the storage manager opens
the configured root through `os.Root` and requires `.swadrive-volume` to be a
bounded regular file whose value exactly matches `SWADRIVE_STORAGE_VOLUME_ID`.
The marker is an application identity check, not proof that the intended HDD is
mounted.

Production must keep the mounted storage root and marker administrator-
controlled and not replaceable by the runtime service. The `files/`, `uploads/`,
and `trash/` subdirectories are the service-writable content boundary. The state
area must remain writable enough for SQLite database/WAL/SHM operation and its
adjacent coordination lock. The flock is not a security boundary against a
malicious same-UID writer. Exact ownership, permission modes, mount ordering,
and systemd restrictions are deployment responsibilities and must be verified
independently from source-level controls.

## Consequences

- Cooperating server/admin processes fail fast instead of knowingly sharing one
  database with independent in-process locks.
- A wrong, missing, oversized, malformed, or nonregular volume marker prevents
  storage initialization before content directories are created.
- The marker detects ordinary configuration/fallback mistakes but cannot detect
  every bind-mount or privileged filesystem substitution.
- Administrator-controlled storage identity and service-writable content paths
  require separate ownership boundaries at deployment.
- A hostile root or same-UID process remains outside this coordination model;
  OS ownership and service isolation must provide that security boundary.

## Alternatives Rejected

- **Treat the marker as mount proof:** rejected because a pathname and marker
  cannot establish the complete kernel mount/ownership state.
- **Treat flock as hostile-process isolation:** rejected because an actor able
  to replace same-UID state files can bypass file-based coordination.
- **Support multiple server processes in backend v1:** rejected because
  process-local upload/file locks and SQLite writer behavior need a separate
  shared-coordination design.
