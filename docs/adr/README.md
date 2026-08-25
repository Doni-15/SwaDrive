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
  serves the metadata plane while user content remains on the data filesystem
  and known mutation gaps fail closed through durable state.
- [ADR-0006](ADR-0006-process-coordination-and-storage-identity.md): cooperating
  processes coordinate through the canonical database lock and bind operations
  to an administrator-provisioned storage identity.

New ADRs should include status, context, decision, and consequences. A changed
decision should supersede an earlier ADR rather than silently rewriting its
history.

Implementation-status and correctness clarifications may update an accepted ADR
without changing its decision. The Phase 2/2.1 additions to ADR-0001 through
ADR-0005 document implemented behavior and refine consequences consistent with
those accepted boundaries. The distinct process-coordination and storage-
identity decision is recorded separately as ADR-0006 rather than being silently
folded into an earlier decision.
