# Architecture Decision Records

This directory contains durable decisions that explain why SwaDrive's locked
architecture exists. Current project state and future implementation plans live
in the root [README](../../README.md).

## Accepted Decisions

- [ADR-0001](ADR-0001-tailscale-plus-go-api.md): Tailscale provides private
  network access; the Go HTTP API carries application data; SSH is
  administration-only.
- [ADR-0002](ADR-0002-restricted-service-account.md): the backend runs as a
  dedicated restricted service account.

- [ADR-0003](ADR-0003-application-authentication-and-sessions.md): application
  authentication remains separate from Tailscale and uses Argon2id passwords
  with opaque, independently revocable server-side sessions.
- [ADR-0004](ADR-0004-persistent-fixed-chunk-resumable-uploads.md): uploads use
  persistent fixed-size chunks with verified writes and atomic publication.
- [ADR-0005](ADR-0005-nvme-metadata-plane-and-hdd-content-plane.md): SQLite
  serves the metadata plane while user content remains on the data filesystem.

New ADRs should include status, context, decision, and consequences. A changed
decision should supersede an earlier ADR rather than silently rewriting its
history.
