# Security Policy

SwaDrive `v1.0.0` is the current stable production baseline. Security fixes are
developed against the latest applicable source and must preserve the documented
v1 security invariants.

## Reporting a Vulnerability

Do not open a public issue containing exploit details, credentials, private
infrastructure data, or user files.

Use GitHub private vulnerability reporting if it is enabled for the repository.
No fallback private reporting address has been designated; do not publish
sensitive details in issues or discussions.

Include the affected revision, impact, reproduction conditions, and a minimal
proof of concept with secrets removed. Allow maintainers time to investigate
before public disclosure.

## Security Scope

High-priority areas include:

- authentication and authorization bypass;
- path traversal or symlink escape;
- unintended file disclosure or modification;
- privilege escalation beyond the restricted service account;
- session/token leakage or revocation failure;
- unsafe upload processing;
- unintended network exposure.

The current backend uses Argon2id password hashes and stores only SHA-256
digests of opaque session tokens. It does not provide application-level
encryption for user files or SQLite and does not terminate TLS itself.
Production access uses the private Tailscale network boundary. Reports should
not describe hashing as encryption.

The `.swadrive-volume` marker is an application identity check, not proof that
the intended HDD is mounted. Production mount, ownership, and `systemd`
controls remain independent security boundaries even when source tests pass.

The database flock coordinates cooperating SwaDrive processes; it is not a
security boundary against a malicious same-UID process. SQLite requires a
service-writable state area for its DB/WAL/SHM files. Production must separately
keep the mounted storage root and marker administrator-controlled while making
only the `files/`, `uploads/`, and `trash/` content boundary writable by the
runtime account. Exact permission modes are deployment-specific and are not
published in this repository.

## Sensitive Information

Never submit live auth keys, passwords, private keys, raw session tokens,
recovery codes, production database copies, logs containing secrets, or private
user data. Redact host details that are not necessary to reproduce the issue.
