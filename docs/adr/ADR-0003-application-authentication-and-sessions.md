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
self-registration endpoint initially. Initial owner creation is implemented by
the local administrator-controlled `bootstrap-owner` command; whether an owner
has actually been provisioned in production remains deployment state. The data
model continues to support additional users.

Passwords are hashed with Argon2id and stored in a self-describing encoded
format containing the algorithm version, parameters, salt, and derived hash.
Password-hashing parameters and verification are centralized so new hashes can
use upgraded parameters while existing hashes remain verifiable.
All Argon2id work passes through one injected, context-aware process-local gate
(four concurrent operations by default) so an authentication flood cannot
multiply the 64 MiB derivation memory without bound. Owner bootstrap uses the
same gate. Password hashes exist only in an unexported auth credential type;
normal user and authenticated identity models cannot carry credential data.

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

Protected requests validate that the session exists, is not
revoked or expired, and belongs to an existing, enabled user. Authentication
success and failure, logout, session revocation, and other security-sensitive
account events are audited without recording passwords, password hashes,
or raw session tokens.

When security state and audit share SQLite, initial-owner creation, successful
session creation, logout, and session revocation append their audit row in the
same explicit transaction as the state change. Either both commit or neither
does. Login failure limiting remains process-local and uses a bounded
username+peer bucket plus a looser peer-wide username-spray bucket.

The username+peer policy blocks after 8 failures in 5 minutes and the peer-wide
spray policy after 40; each block lasts 15 minutes. A request that crosses a
threshold records its ordinary `login_failure` and one
`login_rate_limited` transition event identifying only the bucket kind. Requests
rejected by an already-blocked bucket perform no Argon2 work and append no audit
row. Concurrent transitions are suppressed by the limiter mutex. If transition
audit persistence fails, login still does not succeed, the process-local bucket
remains blocked, and later denials do not retry the write without bound. Restart
intentionally begins new process-local limiter windows.

The public login handler admits at most 64 concurrent requests and installs a
15-second body-read deadline before reading its bounded 64 KiB JSON body. These
route-specific controls do not impose global timeouts on streaming transfers.

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
- Argon2 concurrency is bounded per process, so multi-process deployment would
  require a separate shared resource-control design.
- Clients must protect the raw token in platform-appropriate secure storage.
- Initial session behavior stays small and predictable, at the cost of
  requiring a new login after absolute expiration.
- Append-only auth history still needs production capacity monitoring and an
  explicit retention/archive policy; the application does not silently mutate
  or prune audit rows.

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
