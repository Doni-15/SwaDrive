# ADR-0001: Use Tailscale for Private Network Access and Go HTTP for Application Data

- **Status:** Accepted
- **Scope:** Locked architecture

## Context

SwaDrive needs private access from Linux and Android clients without exposing
the storage server through public port forwarding. Network reachability,
application authentication, and Linux administration solve different problems
and must remain separate.

SSH/SCP can support temporary bootstrap administration and deployment, but an
application built on SSH would put administrative credentials and server access
concerns into the Flutter client.

## Decision

Use Tailscale for private device-to-server network reachability and a Go HTTP
API for all application data operations.

```text
Tailscale          device/network access
Go HTTP API        application authentication, authorization, and file data
SSH/SCP            administrator-only bootstrap and deployment
```

The initial API prefix is `/api/v1` and the initial listener is TCP 8080. The
intended Tailscale grant permits only an explicitly selected normal Member
identity to reach the tagged storage server on TCP 8080. Flutter clients remain
normal Member devices.

Tailscale Serve is not required by this decision. Backend v1 implements its
resumable-upload protocol over the Go HTTP API. HTTPS termination may be
evaluated later without changing the boundary between private network access
and application authorization.

Backend v1 does not terminate TLS in the Go process. Transport confidentiality
therefore depends on production actually using and verifying the intended
Tailscale path and ACLs. This is not an application-level encryption
claim and does not provide encryption at rest for files or SQLite.

## Consequences

- No public port forwarding is required.
- Flutter never needs an SSH private key or SFTP/SCP data path.
- The API can support streaming, Range requests, progress, and resumable
  transfers using HTTP semantics.
- Tailscale reachability does not grant application permissions; Go must still
  authenticate and authorize every protected operation.
- Client devices need a working Tailscale connection to use the private API.
- Any future listener or TLS change must preserve narrow network access and be
  documented explicitly.
