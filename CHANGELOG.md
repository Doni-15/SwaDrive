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
  Phases 0-2 complete and Phase 3 in progress.
- Recorded the Android phone as a normal Tailscale Member device.

### Infrastructure Recorded

- Established the Debian 13 server baseline, separated human administration
  from the restricted backend runtime identity, and created the production
  filesystem layout with least-privilege ownership.
- Rebuilt the Tailscale foundation around `tag:storage-server`, normal Member
  clients, MagicDNS, and the intended TCP 8080 grant.
- Selected the Arch Linux workstation for all development and removed Go, Git,
  and the temporary source workspace from production.

### Not Yet Implemented

- Go server entry point and `GET /api/v1/health`.
- Backend tests, production artifact, configuration, and systemd service.
- Application authentication, file APIs, and Flutter product features.
