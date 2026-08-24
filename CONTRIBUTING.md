# Contributing

SwaDrive is early in development. Before changing code, read the root
[README](README.md) and the accepted decisions in [docs/adr](docs/adr/).

## Project Boundaries

- Keep the monorepo rooted at `client/` and `server/`.
- Do not conflate Linux, Tailscale, application, and service identities.
- Do not run the backend as `root` or `admin_home_server`.
- Do not turn client devices into Tailscale administrators or server-tagged
  machines.
- Keep SSH/SCP for administration only; application traffic uses the Go HTTP
  API over Tailscale.
- Do not add public exposure, broad grants, or `chmod 777` workarounds.
- Add packages and abstractions only when working code requires them.
- Never commit secrets or production data.

Architecture changes that conflict with an accepted ADR require a new ADR that
explicitly supersedes the old decision.

## Development Checks

Run checks for the area changed.

Backend, from `server/` once packages exist:

```bash
gofmt -w cmd/server/main.go
go test ./...
go vet ./...
```

Flutter, from `client/`:

```bash
dart format .
flutter analyze
flutter test
```

Do not deploy, alter Tailscale policy, or modify production as part of an
ordinary code contribution.

## Pull Requests

Keep changes focused. The pull request should explain:

- what changed and why;
- checks performed and any test gaps;
- security and permission impact;
- configuration, schema, or deployment impact;
- documentation updated.

## Security-Sensitive Areas

Changes involving authentication, authorization, filesystem path handling,
uploads, session/token handling, Tailscale policy, systemd privileges, or
secrets require focused tests and explicit review.

Report suspected vulnerabilities privately according to
[SECURITY.md](SECURITY.md).
