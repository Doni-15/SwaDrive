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

### Phase 3 - Go Foundation (completed 2026-08-24)

- Added the minimal Go server entry point and automated health handler test for
  `GET /api/v1/health`; the endpoint returns HTTP 200 with `{"status":"ok"}`.
- Verified `go test ./...`, `go vet ./...`, and `go test -race ./...`.
- Built a statically linked Linux amd64 production artifact with CGO disabled
  from commit `83b7c73` and a clean VCS build state.
- Installed the root-owned binary under `/opt/personalcloud` and
  deployed it as the enabled, active `swadrive.service` running as
  `personalcloud_service`.
- Verified service restart and reboot persistence and health access through
  MagicDNS and direct Tailscale IPv4, while direct Ethernet access to the
  application port remains blocked.

### Infrastructure Recorded

- Established the Debian 13 server baseline, separated human administration
  from the restricted backend runtime identity, and created the production
  filesystem layout with least-privilege ownership.
- Rebuilt the Tailscale foundation around `tag:storage-server`, normal Member
  clients, MagicDNS, and the intended TCP 8080 grant.
- Selected the Arch Linux workstation for all development and removed Go, Git,
  and the temporary source workspace from production.

### Not Yet Implemented

- Application authentication, file APIs, and Flutter product features.
- Phase 4 and later roadmap work.
