# Architecture Decision Records

This directory contains durable decisions that explain why SwaDrive's locked
architecture exists. Current project state and future implementation plans live
in the root [README](../../README.md).

## Accepted Decisions

- [ADR-0001](ADR-0001-tailscale-plus-go-api.md): Tailscale provides private
  network access; the Go HTTP API carries application data; SSH is
  administration-only.
- [ADR-0002](ADR-0002-restricted-service-account.md): the backend runs as the
  dedicated restricted `personalcloud_service` identity.

New ADRs should include status, context, decision, and consequences. A changed
decision should supersede an earlier ADR rather than silently rewriting its
history.
