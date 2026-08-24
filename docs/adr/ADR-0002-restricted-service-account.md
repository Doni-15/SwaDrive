# ADR-0002: Run the Backend as a Dedicated Restricted Service Account

- **Status:** Accepted
- **Scope:** Locked architecture

## Context

The backend will process user-controlled names, uploads, downloads, metadata,
and filesystem operations. A backend compromise must not automatically grant
root authority, human administrator authority, deployment authority, or broad
filesystem access.

## Decision

Run the Go backend as a dedicated restricted Linux service account, not as
`root` or the human administrator account.

The service account uses a non-interactive shell, has no sudo, interactive or
SSH login, and has no Tailscale administration. It may write only the
application storage, state, and log paths that require runtime mutation and may
read administrator-provided configuration as needed.

The production binary remains administrator-owned under a release directory,
and configuration remains administrator-controlled under a separate config
directory. The runtime account must not be able to replace its own executable.

## Consequences

- Backend compromise has a smaller Linux filesystem and privilege boundary.
- Human administration, release installation, and application runtime remain
  auditable as separate authorities.
- Ownership and systemd writable-path rules require deliberate maintenance.
- Permission failures must be fixed narrowly; `chmod 777`, sudo access, or
  running the service as an administrator are not acceptable workarounds.
- Additional systemd sandboxing can be added after the minimal service works.
