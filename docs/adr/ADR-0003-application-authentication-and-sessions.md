# ADR-0003: Use Application Authentication with Opaque Server-Side Sessions

- **Status:** Accepted
- **Scope:** Locked architecture

## Context

Tailscale determines which devices may reach SwaDrive's private network
service, but network reachability does not establish an application user or
provide application-level authorization. SwaDrive also needs multiple device
sessions that can expire and be revoked independently without exposing a
reusable credential in persistent storage.

Passwords and session credentials are security-sensitive. Their formats must
support safe verification and future parameter upgrades without requiring a
JWT signing-key lifecycle or refresh-token system during the initial product
phase.

## Decision

Go provides application authentication and authorization independently from
Tailscale. Application users are stored in SQLite. There is no public
self-registration endpoint initially; an initial owner will be created later
through an administrator-controlled bootstrap mechanism, while the data model
continues to support additional users.

Passwords are hashed with Argon2id and stored in a self-describing encoded
format containing the algorithm version, parameters, salt, and derived hash.
Password-hashing parameters and verification are centralized so new hashes can
use upgraded parameters while existing hashes remain verifiable.

Authentication uses opaque server-side sessions rather than JWTs. Each login
generates a cryptographically random 256-bit token with `crypto/rand`. The raw,
unpadded URL-safe base64 token is returned only to the client and is never
logged. SQLite stores only `SHA-256(raw token)`. A protected request presents
the credential as `Authorization: Bearer <opaque-token>`.

Each login/device has its own session row. Sessions have an absolute initial
expiration of 30 days and may be revoked independently. Logout revokes only the
current session; the session-management endpoint may revoke another device
without revoking every session belonging to the user. Initial authentication
does not use refresh tokens or sliding expiration.

Protected requests must eventually validate that the session exists, is not
revoked or expired, and belongs to an existing, enabled user. Authentication
success and failure, logout, session revocation, and other security-sensitive
account events will be audited without recording passwords, password hashes,
or raw session tokens.

`GET /api/v1/health` remains unauthenticated at the application layer. Its
network reachability remains restricted by the private Tailscale boundary.

## Consequences

- Compromise of Tailscale device reachability alone does not grant application
  identity or permissions.
- A database disclosure does not directly reveal plaintext passwords or bearer
  session tokens, although password hashes and session metadata remain
  security-sensitive.
- Lost or untrusted devices can be revoked independently, and one user may use
  multiple devices concurrently.
- The server must perform a database lookup for protected requests and enforce
  expiry, revocation, disabled-user, and authorization rules.
- Clients must protect the raw token in platform-appropriate secure storage.
- Initial session behavior stays small and predictable, at the cost of
  requiring a new login after absolute expiration.

## Alternatives Rejected

- **Treat Tailscale identity as application authentication:** rejected because
  network/device authorization and application-user authorization are distinct
  security boundaries.
- **JWT access tokens:** rejected because immediate per-device revocation still
  requires server-side state and introduces signing-key and claim-lifecycle
  complexity without an initial benefit.
- **Store raw session tokens:** rejected because a database disclosure would
  immediately expose live bearer credentials.
- **Use one shared or global session:** rejected because revoking one device
  would unnecessarily sign out every device.
- **Refresh tokens or sliding expiration:** rejected for the initial phase as
  avoidable session-lifecycle complexity.
- **Public self-registration:** rejected because the initial private deployment
  uses administrator-controlled owner bootstrap and does not need an open
  account-creation surface.
