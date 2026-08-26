# Changelog

All notable SwaDrive project changes are recorded here.

## [Unreleased]

Next major target: `v2.0.0`.

No `v2.0.0` feature is recorded as completed. See the
[`NEXT` release flag](docs/releases/NEXT.md) for planning status.

## [1.0.0] - 2026-08-26

First production release. The canonical release description and artifact
provenance are available in the [`v1.0.0` release notes](docs/releases/v1.0.0.md).

### Added

- Added the Go `/api/v1` backend with application authentication, owner
  authorization, opaque independently revocable server-side sessions, and a
  health endpoint.
- Added owner-only logical-path APIs for file listing, metadata, search, folder
  creation, move, trash, restore, and streaming HTTP Range downloads.
- Added a rebuildable SQLite metadata index with generation-safe explicit
  reindex and incremental maintenance for file mutations.
- Added persistent fixed-chunk resumable uploads with per-chunk SHA-256,
  optional whole-file verification, restart recovery, bounded concurrency, and
  atomic no-overwrite publication.
- Added append-only, owner-readable audit events for authentication, session,
  file, and upload activity.
- Added local `swadrive-admin` commands for initial owner bootstrap, metadata
  reindex, and bounded orphan upload-part reconciliation with dry-run by
  default.

### Security

- Added Argon2id password hashing and storage of only SHA-256 digests for
  cryptographically random 256-bit opaque session tokens.
- Added bounded authentication and transfer resource gates, login abuse
  limiting, strict security-header handling, and redacted operational logging.
- Added owner authorization plus logical-path containment that rejects absolute
  paths, traversal, encoded traversal, null bytes, and symlink escape.
- Added canonical database process locking, storage volume identity validation,
  same-filesystem checks, targeted startup reconciliation, and fail-closed
  metadata consistency handling.
- Separated Tailscale network reachability, application identity, human Linux
  administration, and the restricted runtime service identity.

### Infrastructure

- Deployed the frozen backend v1 baseline to a Debian production server on
  2026-08-26 as a `systemd`-managed service running under
  `personalcloud_service`.
- Restricted backend access to the Tailscale private network on TCP `8080`,
  without public exposure of the application port.
- Established an NVMe control/metadata plane for SQLite and an HDD content
  plane rooted at `/srv/personalcloud` for files, uploads, and trash.
- Configured SwaDrive to fail closed when the production storage mount is
  unavailable while allowing Debian itself to boot without the HDD.

### Known Limitations

- The Flutter Android/Linux project remains a minimal scaffold without login,
  file-browser, or transfer UX.
- Backend v1 file, upload, and audit APIs are owner-only and support one server
  process per database/storage-root pair.
- Application-level encryption at rest, public sharing, content indexing,
  thumbnails, OCR, and automatic audit/history retention are not provided.
