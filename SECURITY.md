# Security Policy

SwaDrive has not published a stable release. Security fixes currently target
the latest `main` branch only.

## Reporting a Vulnerability

Do not open a public issue containing exploit details, credentials, private
infrastructure data, or user files.

Use GitHub private vulnerability reporting if it is enabled for the repository.
No fallback private reporting address has been designated yet; one must be
chosen before a public or stable release. Until then, do not publish sensitive
details in issues or discussions.

Include the affected revision, impact, reproduction conditions, and a minimal
proof of concept with secrets removed. Allow maintainers time to investigate
before public disclosure.

## Security Scope

High-priority areas include:

- authentication and authorization bypass;
- path traversal or symlink escape;
- unintended file disclosure or modification;
- privilege escalation beyond `personalcloud_service`;
- session/token leakage or revocation failure;
- unsafe upload processing;
- unintended network exposure.

## Sensitive Information

Never submit live auth keys, passwords, private keys, raw session tokens,
recovery codes, production database copies, logs containing secrets, or private
user data. Redact host details that are not necessary to reproduce the issue.
