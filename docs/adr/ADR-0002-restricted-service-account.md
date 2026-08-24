# ADR-0002: Run the Backend as a Dedicated Restricted Service Account

- **Status:** Accepted
- **Scope:** Locked architecture

## Context

The backend will process user-controlled names, uploads, downloads, metadata,
and filesystem operations. A backend compromise must not automatically grant
root authority, human administrator authority, deployment authority, or broad
filesystem access.

## Decision

Run the Go backend as the dedicated Linux identity
`personalcloud_service`, not as `root` or `admin_home_server`.

The service account has UID/GID 988, home `/var/lib/personalcloud`, shell
`/usr/sbin/nologin`, no sudo, no interactive or SSH login, and no Tailscale
administration. It may write only the application storage, state, and log paths
that require runtime mutation and may read administrator-provided configuration
as needed.

The production binary remains administrator-owned under `/opt/personalcloud`,
and configuration remains administrator-controlled under `/etc/personalcloud`.
The runtime account must not be able to replace its own executable.

## Consequences

- Backend compromise has a smaller Linux filesystem and privilege boundary.
- Human administration, release installation, and application runtime remain
  auditable as separate authorities.
- Ownership and systemd writable-path rules require deliberate maintenance.
- Permission failures must be fixed narrowly; `chmod 777`, sudo access, or
  running the service as an administrator are not acceptable workarounds.
- Additional systemd sandboxing can be added after the minimal service works.
