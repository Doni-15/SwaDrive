# Changelog

All notable SwaDrive project changes are recorded here. The project has not
published a release yet.

## Unreleased

### Changed

- Adopted the SwaDrive product identity while preserving the existing
  `client/`, `server/`, Android package, Go module, and Git remote identities.
- Consolidated project state, requirements, architecture, API and data-model
  direction, workflows, testing, and roadmap into the root README.
- Retained only durable architecture decisions under `docs/adr/`.
- Synchronized the roadmap with verified infrastructure and network state:
  Phases 0-3 complete, with Phase 4 and later phases not started.
- Recorded the Android phone as a normal Tailscale Member device.

### Backend v1 — local implementation and Phase 2 hardening

- Added Argon2id application authentication, opaque independently revocable
  sessions, owner authorization, and transactional auth/audit mutations.
- Added bounded username+peer and peer-wide login limiting. A durable
  `auth.login_rate_limited` event is now emitted only when a bucket crosses its
  threshold; already-blocked requests do not append one SQLite row each.
- Added logical-path file APIs backed by an NVMe SQLite metadata index for normal
  listing, metadata, search, trash listing, and upload status.
- Added generation-safe explicit reindex and incremental mkdir/move/trash/
  restore/upload-completion index maintenance.
- Added streaming Range downloads and persistent fixed-chunk resumable uploads
  with parallel chunk integrity, bounded concurrency, atomic no-overwrite
  publication, and startup finalizing reconciliation.
- Added durable unhealthy index intent around mkdir/move filesystem mutations;
  an interrupted operation fails startup closed until explicit reindex.
- Added local-admin, bounded, age/name/type-gated orphan upload-part dry-run and
  explicit cleanup.
- Added a canonical database process lock, storage volume identity validation,
  same-filesystem checks for `files/`, `uploads/`, and `trash/`, login-only body
  deadline/admission, strict duplicate security-header rejection, safe startup
  log categories, and a bounded cleanup-worker shutdown wait.
- Detached mkdir/move consistency finalization from client cancellation with a
  short bounded internal repair context; request disconnect alone no longer
  strands a successfully finalized or compensated durable mutation intent.
- Documented that `.swadrive-volume` is identity rather than mount proof;
  production mount/ownership/Tailscale controls remain a later deployment phase.
- Clarified that Argon2/SHA-256 are hashing controls. User files and SQLite are
  not application-encrypted at rest, and the Go app does not terminate TLS.

### Phase 3 - Go Foundation (completed 2026-08-24)

- Added the minimal Go server entry point and automated health handler test for
  `GET /api/v1/health`; the endpoint returns HTTP 200 with `{"status":"ok"}`.
- Verified `go test ./...`, `go vet ./...`, and `go test -race ./...`.
- Built a statically linked Linux amd64 production artifact with CGO disabled
  from a clean VCS build state.
- Installed an administrator-owned binary under a dedicated release directory
  and deployed it as an enabled systemd service running as the restricted
  service account.
- Verified service restart and reboot persistence and health access through
  MagicDNS and direct Tailscale IPv4, while direct Ethernet access to the
  application port remains blocked.

### Infrastructure Recorded

- Established the Debian 13 server baseline, separated human administration
  from the restricted backend runtime identity, and created the production
  filesystem layout with least-privilege ownership.
- Rebuilt the Tailscale foundation around a tagged storage server, normal
  Member clients, MagicDNS, and a narrowly scoped application-port grant.
- Selected the Arch Linux workstation for all development and removed Go, Git,
  and the temporary source workspace from production.

### Not Yet Implemented

- Flutter login/file-browser product features.
- Production deployment and verification of backend v1 mount, ownership,
  Tailscale, backup, monitoring, capacity/retention, and at-rest encryption
  decisions.
