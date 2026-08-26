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

The administrator chooses the state path, but the runtime account must be able
to create and write SQLite's database, WAL, and SHM files there. The adjacent
database flock coordinates cooperating SwaDrive processes; it is not a security
boundary against a malicious process running with the same UID or another actor
that can replace files in that writable state area.

The mounted storage root and its `.swadrive-volume` marker remain
administrator-controlled and must not be replaceable by the runtime account.
The pre-provisioned `files/`, `uploads/`, and `trash/` content subdirectories are
the service-writable data boundary. Exact production ownership, modes, mount
ordering, and systemd writable-path rules are deployment responsibilities and
must support these boundaries without granting broader access.

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
