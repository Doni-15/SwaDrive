# Contributing

SwaDrive is early in development. Before changing code, read the root
[README](README.md) and the accepted decisions in [docs/adr](docs/adr/).

## Project Boundaries

- Keep the monorepo rooted at `client/` and `server/`.
- Do not conflate Linux, Tailscale, application, and service identities.
- Do not run the backend as `root` or the human administrator account.
- Do not turn client devices into Tailscale administrators or server-tagged
  machines.
- Keep SSH/SCP for administration only; application traffic uses the Go HTTP
  API over Tailscale.
- Do not add public exposure, broad grants, or `chmod 777` workarounds.
- Add packages and abstractions only when working code requires them.
- Never commit secrets or production data.

Architecture changes that conflict with an accepted ADR require a new ADR that
explicitly supersedes the old decision.

## Backend Engineering Standards

The backend uses a lightweight Clean Architecture with feature-oriented
packages such as `internal/auth`, `internal/database`, and `internal/httpapi`.
The primary dependency flow is Handler -> Service -> Repository -> Storage:

- handlers parse and validate HTTP input, call services, and translate results
  into HTTP responses; they do not query SQLite;
- services contain application rules without HTTP types, raw SQL, or knowledge
  of SQLite implementation details;
- repositories define real persistence boundaries required by services; their
  storage implementations own SQL and contain no HTTP behavior;
- `cmd/server/main.go` is limited to startup, composition, and dependency
  wiring.

Use explicit constructor injection and put interfaces only at genuine
boundaries or test seams. Keep code directly testable, with tests near the code
they verify. Centralize security-sensitive work such as password hashing and
session-token generation. Apply DRY to real shared concepts, not coincidental
small similarities, and do not create speculative package trees or
`utils`, `helpers`, `common`, or `misc` dumping grounds.

Development, dependency management, migrations, tests, and builds happen on
the workstation. Production receives reviewed build artifacts and runtime
configuration only; it is not a source-development environment.

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
