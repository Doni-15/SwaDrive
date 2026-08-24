# SwaDrive

SwaDrive is a private, self-hosted personal cloud for browsing and transferring files from Flutter clients on Linux and Android. Files remain on a personally controlled Debian server and are exposed to client devices through a Go HTTP API over Tailscale.

> **Locked principle:** Server manages itself. Client devices only consume services exposed by the server.

## Project Status

**As of:** 2026-08-24

**Latest completed phase:** Phase 3 - Go Foundation (**COMPLETE**)

**Next roadmap phase:** Phase 4 - Application Authentication (**NOT STARTED**)

This document uses these labels deliberately:

- **VERIFIED** means the state was checked on the relevant machine or service and supplied as verified project state.
- **CURRENT REPOSITORY** means the state is directly visible in this repository.
- **PLANNED** means a design direction or roadmap item that is not implemented yet.

### Current summary

| Area | Status | Current state |
| --- | --- | --- |
| Clean rebuild | **COMPLETE** | Linux administration and legacy Tailscale state were cleaned up. |
| Server foundation | **COMPLETE** | Restricted runtime identity and production filesystem paths were verified. |
| Tailscale foundation | **COMPLETE** | Server tag, Member clients, MagicDNS, and the locked TCP 8080 authorization boundary were verified. |
| Go foundation | **COMPLETE** | The tested health endpoint is deployed under systemd as the restricted runtime identity and is reachable over Tailscale. |
| Application authentication | **NOT STARTED** | Design direction only. |
| File API | **NOT STARTED** | Design direction only. |
| Flutter MVP | **NOT STARTED** | Generated Flutter scaffold exists, but product features are not implemented. |
| Large files, sync, hardening | **NOT STARTED** | Long-term requirements and roadmap only. |

The Phase 3 backend foundation is deployed. `swadrive.service` is enabled and
active, and `GET http://storage-server:8080/api/v1/health` returns HTTP 200 with
`{"status":"ok"}` over Tailscale. Application authentication, file APIs, and
Flutter product features remain not started.

## Technology Stack

| Layer | Choice | State |
| --- | --- | --- |
| Production OS | Debian GNU/Linux 13 (trixie) | **VERIFIED** |
| Private network | Tailscale | **VERIFIED foundation** |
| Backend | Go HTTP API | **VERIFIED Phase 3 foundation** |
| Client | Flutter for Linux and Android | **CURRENT scaffold** |
| Initial database | SQLite | **PLANNED** |
| File storage | Linux filesystem, not database BLOBs | **LOCKED direction** |
| Service manager | systemd | **VERIFIED; `swadrive.service` enabled and active** |
| Development OS | Arch Linux | **VERIFIED** |

## Goals

- Provide a private file-explorer experience on Linux and Android.
- Keep file data on a personally controlled server.
- Support application authentication independently from Tailscale identity.
- Browse, search, upload, download, rename, move, trash, and restore files.
- Support one application account across multiple independently revocable device sessions initially, while leaving room for more application users later.
- Stream file contents with memory use that does not grow with file size.
- Eventually resume interrupted large uploads and downloads.
- Operate without public port forwarding or a public application endpoint.
- Keep server administration, deployment, and runtime authority separate.

## Non-Goals

The initial product is not intended to provide:

- public anonymous file links;
- public internet exposure or router port forwarding;
- client-side server administration;
- SSH/SFTP as the application data protocol;
- distributed storage, clustering, or multi-region high availability;
- Kubernetes or containers solely for packaging;
- S3 compatibility;
- enterprise SSO;
- collaborative document editing;
- zero-knowledge or end-to-end file encryption;
- Flutter Web.

## Architecture

```text
Tailscale control plane
  Single Owner
          |
          | owns tag:storage-server
          v
+------------------------------------------------------+
| swadrive / storage-server                            |
| Debian GNU/Linux 13                                  |
|                                                      |
| root                  emergency authority only       |
| admin_home_server     human sudo administrator       |
|                                                      |
| admin-owned release artifact and configuration       |
|                 |                                    |
|                 v                                    |
| Go HTTP API on TCP 8080                 DEPLOYED      |
| runs as personalcloud_service                        |
|        |                       |                     |
|        v                       v                     |
| SQLite/application state      Linux filesystem       |
| PLANNED                       foundation verified    |
| /var/lib/personalcloud        /srv/personalcloud     |
+---------------------------+--------------------------+
                            ^
                            | Go HTTP API over Tailscale
                            | application auth still required
                +-----------+-----------+
                |                       |
        Arch Linux client         Android client
        normal Member             normal Member
        Flutter                   Flutter
```

`swadrive` is the Linux hostname. `storage-server` is the Tailscale machine
name and storage-server role. The different names are intentional and do not
represent different production machines.

The four identity and security layers are intentionally distinct:

1. Linux OS identity controls operating-system administration.
2. Tailscale identity controls which device can reach a network service.
3. Go application identity controls which application user may perform an operation.
4. Go service/runtime identity limits what a compromised backend process can do on Linux.

Network reachability never replaces application authentication or authorization.

## Identity Model

### Linux and runtime identities - VERIFIED

| Identity | Authority | Restrictions |
| --- | --- | --- |
| `root` | Emergency Linux superuser | Not used for normal backend runtime. |
| `admin_home_server` | Human Linux administrator and deployer | Sudo capable; backend must not run as this user. |
| `personalcloud_service` | Dedicated Go runtime identity, UID/GID 988 | Home `/var/lib/personalcloud`; shell `/usr/sbin/nologin`; no sudo, interactive login, SSH login, or Tailscale administration. |

The runtime account may manage user storage, application state, and logs, and may read administrator-provided configuration when required. It must not replace the production binary, administer the OS, modify Tailscale, or gain broad filesystem access.

### Tailscale identities - VERIFIED

| Identity | Role |
| --- | --- |
| Tailnet Owner | Single control-plane administrator and tag owner. |
| Client Member identity | Normal network identity used by the laptop and phone. |
| `tag:storage-server` | Server machine identity. |
| Arch Linux laptop | Normal Member device; no machine tag. |
| Android phone | Normal Member device; no server tag. |

The laptop and phone must remain normal client devices. They must not be tagged as servers or promoted to Owner/Admin.

### Application identity - PLANNED

Application users will be managed by Go and SQLite. An initial owner may look like:

```text
username: doni
role: owner
```

This application identity is not any Linux account, Tailscale account, or machine tag. A single application user may have separate sessions for Linux, Android, and future devices so that a lost device can be revoked without ending every other session.

## Verified Current State

The following records describe the verified external state supplied to the
project. They were not re-queried or modified during this documentation update.

### Production server

| Property | Value |
| --- | --- |
| Linux hostname | `swadrive` |
| OS | Debian GNU/Linux 13 (trixie) |
| Tailscale hostname | `storage-server` |
| Machine identity | `tag:storage-server` |
| Operating mode | Headless/TTY-oriented; default target `multi-user.target` |
| Package state | Cleanup complete; no residual package configurations observed |
| systemd state | `swadrive.service` enabled and active; restart and full-reboot validation passed; no failed units observed |
| Active foundation services | `ssh`, `nftables`, `tailscaled`, `wpa_supplicant` |
| Application listener | Go HTTP API on TCP 8080 |
| Health endpoint | `GET /api/v1/health` returns HTTP 200 with `{"status":"ok"}` through MagicDNS and direct Tailscale IPv4 access |
| Installed binary | `/opt/personalcloud/swadrive-server`, owned by `root:root`, mode `0755`; executable but not writable by `personalcloud_service` |
| Production artifact | Statically linked Linux amd64 binary built with `CGO_ENABLED=0` from commit `83b7c73` with a clean VCS build state |
| Development tools | Go, Git, and the temporary development workspace were removed |

Cleanup removed unnecessary laptop, reporting, Bluetooth, and development
packages while retaining the administration, firewall, Tailscale, Wi-Fi,
storage-health, SQLite, transfer, and diagnostic tools required for production.
The server remains minimal and runtime-only.

### Client devices

| Device | Account/role | Verified state |
| --- | --- | --- |
| Arch Linux laptop | Normal Member | MagicDNS resolution for `storage-server` was verified; no machine tag. |
| Android phone | Normal Member | Joined as a normal client device; no server tag. |

### Development workstation

| Tool | Verified value |
| --- | --- |
| Go | `go1.27.0-X:nodwarf5 linux/amd64` at `/usr/bin/go` |
| Git | `2.55.0` at `/usr/bin/git` |
| Docker | `29.7.2`; workstation tool only, not a production dependency |
| Flutter | `3.44.9` stable |
| Dart | `3.12.2` |
| Android toolchain | SDK/toolchain verified healthy |
| Java | OpenJDK 17 system default; Flutter Android uses Android Studio's bundled JBR |

`doni-arch` is a normal daily-use Arch Linux laptop and the current SwaDrive
development workstation. It may build, test, and perform administrator-approved
deployment during development, but it is not the permanent administration
authority for `swadrive`.

## Tailscale Network Design

The Tailscale authorization rule remains narrowly scoped to the specific
Member identity, the tagged server, and the application port:

```json
{
  "tagOwners": {
    "tag:storage-server": ["autogroup:owner"]
  },
  "grants": [
    {
      "src": ["kanhantam3@gmail.com"],
      "dst": ["tag:storage-server"],
      "ip": ["tcp:8080"]
    }
  ]
}
```

```text
kanhantam3@gmail.com devices
        |
        | TCP 8080 only
        v
tag:storage-server
```

The host nftables default-drop policy remains in place. Its persistent and live
application-path rule permits new TCP 8080 connections only through
`tailscale0`:

```nft
iifname "tailscale0" tcp dport 8080 ct state new accept
```

MagicDNS and direct Tailscale IPv4 access to the health endpoint return HTTP
200, while direct Ethernet access to TCP 8080 is blocked. Do not
broaden the Tailscale grant to all Members without an explicit architecture
decision. Tailscale Serve is not mandatory; the deployed foundation uses a Go
listener on TCP 8080 under the locked Tailscale authorization boundary.

## Server Filesystem and Ownership

These paths and permissions are **VERIFIED**:

| Path | Purpose | Owner | Mode |
| --- | --- | --- | --- |
| `/etc/personalcloud` | Administrator-controlled configuration | `root:personalcloud_service` | `750` |
| `/opt/personalcloud` | Production application and binary area | `root:root` | `755` |
| `/srv/personalcloud` | Storage root | `personalcloud_service:personalcloud_service` | `750` |
| `/srv/personalcloud/files` | User files | `personalcloud_service:personalcloud_service` | `750` |
| `/srv/personalcloud/uploads` | Incomplete upload workspace | `personalcloud_service:personalcloud_service` | `750` |
| `/srv/personalcloud/trash` | Recycle bin | `personalcloud_service:personalcloud_service` | `750` |
| `/var/lib/personalcloud` | SQLite and persistent application state | `personalcloud_service:personalcloud_service` | `750` |
| `/var/log/personalcloud` | Application logs | `personalcloud_service:personalcloud_service` | `750` |

The administrator controls releases and configuration. The runtime identity owns only the data and state it must mutate. Permission problems must never be solved with `chmod 777`.

The verified boundary allows `personalcloud_service` to write within
`/srv/personalcloud`, `/var/lib/personalcloud`, and `/var/log/personalcloud`,
including the `files`, `uploads`, and `trash` storage directories. It cannot
write administrator or system authority paths:

```text
/etc/personalcloud
/opt/personalcloud
/etc/systemd/system
/etc/ssh
/etc/sudoers
/root
/home/admin_home_server
/home/admin_home_server/.ssh
```

## Security Principles

- Never run the backend as `root` or `admin_home_server`.
- Run the backend as `personalcloud_service`, with no sudo or interactive login.
- Keep the production binary administrator-owned and not writable by the runtime account.
- Keep configuration administrator-controlled and readable by the service only as necessary.
- Keep client devices as normal Tailscale Members without control-plane authority.
- Require application authentication and authorization even inside Tailscale.
- Keep application users distinct from Tailscale and Linux identities.
- Store password hashes, never plaintext passwords.
- Do not log passwords, raw session tokens, file contents, or other secrets.
- Prevent absolute-path access, `..` traversal, encoded traversal, and symlink escape from the storage root.
- Never expose raw host filesystem paths through the API.
- Stream large content instead of fully buffering it in RAM.
- Check available space before large writes and keep incomplete uploads out of the visible file tree.
- Keep production free of source-development requirements such as Git or a Go compiler.
- Do not require public port forwarding.
- Audit unexpected listeners, privileges, configuration changes, and security-sensitive application events.

### Secret handling

Never commit:

- Tailscale auth keys or recovery codes;
- SSH private keys;
- passwords or password hashes copied from production;
- application access or refresh tokens;
- signing keys, certificates with private material, or keystores;
- populated `.env` files;
- production databases, backups, logs, or private user data.

Use placeholders in examples. Review staged changes and history before any public release. Repository ignore rules reduce accidents but are not a substitute for secret review.

## SSH Administration Policy

SSH/SCP may temporarily support administration and release transfer from the
Arch workstation to Debian. Current bootstrap access from `doni-arch` uses
normal SSH password authentication to `admin_home_server`. This does not make
`doni-arch` a permanent server administration authority.

- Flutter must never contain an SSH private key.
- Flutter must not use SCP or SFTP for normal file operations.
- `personalcloud_service` must not receive SSH login access.
- No dedicated permanent SwaDrive administrator key is currently part of the
  architecture.
- Administrative SSH access must be reviewed and intentionally hardened before
  a stable release without assuming a particular key scheme now.

SSH is not the Flutter protocol or normal application data plane. The
application data path is the Go HTTP API over Tailscale.

## Development and Production Separation

This separation is **LOCKED**:

```text
doni-arch (daily-use laptop + development workstation)
  source, Git, Go, Flutter, Android tools, tests, builds
          |
          | produce release artifact + checksum
          v
administrator-approved SSH/SCP deployment during development
          |
          v
swadrive (production runtime only)
  /opt/personalcloud   admin-owned binary
  /etc/personalcloud   admin-controlled config
  systemd              service supervision
  /var/lib/...         application state
  /srv/...             file storage
  /var/log/...         logs
          |
          v
personalcloud_service (runtime)
```

Production does not need the repository, Git, or the Go compiler.
The human Linux administrator remains `admin_home_server`; the development
workstation is a build and temporary deployment origin, not permanent server
authority.

## Repository Structure

**CURRENT REPOSITORY:**

```text
SwaDrive/
|-- client/                         Flutter client
|   |-- android/                    Flutter-managed Android project
|   |-- linux/                      Flutter-managed Linux project
|   |-- lib/main.dart               Current minimal scaffold
|   |-- pubspec.yaml
|   `-- pubspec.lock
|-- server/
|   |-- cmd/server/main.go          Go HTTP server entry point
|   |-- cmd/server/main_test.go     Health endpoint test
|   `-- go.mod                      Go module
|-- docs/
|   `-- adr/                        Durable architecture decisions only
|-- .github/
|   |-- ISSUE_TEMPLATE/
|   |   |-- bug_report.md
|   |   `-- feature_request.md
|   `-- PULL_REQUEST_TEMPLATE.md
|-- .gitignore
|-- README.md                       Primary project source of truth
|-- CONTRIBUTING.md
|-- SECURITY.md
|-- CHANGELOG.md
`-- CODE_OF_CONDUCT.md
```

The local checkout may be named `personal-cloud`; this does not change the product, repository, package, or module identity.

| Identity | Value |
| --- | --- |
| Product | SwaDrive |
| Git remote | `git@github.com:Doni-15/SwaDrive.git` |
| Expected GitHub path | `github.com/Doni-15/SwaDrive` |
| Go module | `github.com/Doni-15/SwaDrive/server` |
| Flutter directory | `client/` |
| Android application ID | `id.donirs.swadrive` |

The backend foundation remains intentionally minimal:

```text
server/
|-- go.mod
`-- cmd/
    `-- server/
        |-- main.go
        `-- main_test.go
```

Packages under `server/internal/` and migrations under `server/migrations/` should be added only when working code needs them. Flutter should likewise evolve from the current `client/lib/main.dart` toward feature-oriented boundaries only as features appear.

## Functional Requirements - PLANNED

| ID | Requirement |
| --- | --- |
| FR-001 | Authenticate application users. |
| FR-002 | Log out and invalidate the active session. |
| FR-003 | List and revoke device sessions independently. |
| FR-004 | List hierarchical files and folders. |
| FR-005 | Navigate folders and create directories. |
| FR-006 | Upload files. |
| FR-007 | Stream and download files. |
| FR-008 | Rename files and folders. |
| FR-009 | Move files and folders. |
| FR-010 | Move normal deletions to trash. |
| FR-011 | Restore retained trash entries, including defined conflict handling. |
| FR-012 | Search indexed file metadata. |
| FR-013 | Record useful metadata without storing file bytes as database BLOBs. |
| FR-014 | Record security-sensitive audit events without secrets. |
| FR-015 | Support client/server version compatibility checks. |
| FR-016 | Support HTTP Range and resumable downloads. |
| FR-017 | Support resumable/chunked uploads and interrupted transfer recovery. |

The MVP succeeds when authenticated Linux and Android clients can perform the core file operations through the Go API; the backend starts under systemd as `personalcloud_service`; no direct filesystem, SSH, or unrelated server-port access is required by the clients; and deletion is recoverable through trash.

## Backend and API

API prefix: `/api/v1`

Listener: TCP `8080`

The deployed Phase 3 endpoint is:

```http
GET /api/v1/health
```

```json
{
  "status": "ok"
}
```

It returns HTTP 200 with `{"status":"ok"}` and has a focused automated handler
test. It is a minimal process-health endpoint; application authentication and
file operations are not implemented.

Future endpoint direction:

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/api/v1/auth/login` | Create an application session. |
| `POST` | `/api/v1/auth/logout` | Revoke the active session. |
| `GET` | `/api/v1/auth/sessions` | List sessions/devices. |
| `DELETE` | `/api/v1/auth/sessions/{id}` | Revoke one session. |
| `GET` | `/api/v1/files` | List files and folders. |
| `GET` | `/api/v1/files/{id}` | Return metadata. |
| `GET` | `/api/v1/files/{id}/content` | Stream file content and later support Range. |
| `POST` | `/api/v1/folders` | Create a folder. |
| `POST` | `/api/v1/files/upload` | Initial upload path. |
| `PATCH` | `/api/v1/files/{id}` | Rename or update metadata. |
| `POST` | `/api/v1/files/{id}/move` | Move a resource. |
| `DELETE` | `/api/v1/files/{id}` | Move a resource to trash. |
| `POST` | `/api/v1/files/{id}/restore` | Restore a trashed resource. |
| `GET` | `/api/v1/search?q=...` | Search metadata. |

API rules:

- use stable resource IDs rather than expose arbitrary server paths;
- authenticate and authorize each protected resource operation;
- return a stable error shape without internal stack traces;
- validate request sizes and user input;
- honor cancellation where practical;
- stream file content;
- keep partial uploads separate until validation and atomic finalization.

### Version handshake

A client/server compatibility handshake remains a useful future concept. Compatibility should depend on the API version and a configured minimum client version, not require client and server release numbers to match exactly. Possible outcomes include `compatible`, `update_available`, `update_required`, `incompatible_api`, and `maintenance`.

This handshake is not implemented, and its exact request/response contract is not locked.

## Data Model Direction - PLANNED

SQLite will initially store application state and metadata under `/var/lib/personalcloud`. File bytes remain regular files under `/srv/personalcloud`.

Possible tables:

| Table | Candidate fields |
| --- | --- |
| `users` | `id`, `username`, `password_hash`, `role`, `created_at`, `updated_at`, `disabled_at` |
| `sessions` | `id`, `user_id`, `token_hash`, `client_name`, `created_at`, `expires_at`, `revoked_at`, `last_seen_at` |
| `files` | `id`, `parent_id`, `name`, `relative_path`, `size`, `mime_type`, `checksum`, `is_directory`, `created_at`, `modified_at`, `trashed_at` |
| `uploads` | `id`, `user_id`, `target_parent_id`, `filename`, `total_size`, `chunk_size`, `received_bytes`, `created_at`, `expires_at`, `status` |
| `audit_events` | `id`, `user_id`, `event_type`, `resource_id`, `timestamp`, `metadata_json` |

These names and columns are design directions, not an implemented schema. Migrations must be versioned once a schema exists.

Metadata indexing should eventually make filename search responsive without scanning the entire storage tree on every request. API file operations and periodic reconciliation may keep the index synchronized. Audit events should cover authentication, session revocation, upload outcomes, rename/move, trash/restore, and other sensitive actions without recording tokens, passwords, or file contents.

## File and Trash Semantics - PLANNED

- Normal delete moves a resource into `/srv/personalcloud/trash`; it is not an immediate permanent purge.
- Trash metadata must preserve enough information to restore the original path.
- Restore conflicts need an explicit policy such as reject, rename, or authorized overwrite.
- Rename/move should use atomic filesystem operations when source and destination share a filesystem.
- Internal upload and trash locations must not appear as ordinary user folders.
- Paths must remain relative to and contained by the configured storage root.
- Symlinks must not permit escape from that root.

Retention duration, overwrite rules, and permanent-purge behavior are not yet locked.

## Large-File Strategy - PLANNED

Large files are a long-term requirement, including transfers much larger than available RAM.

- Stream uploads and downloads through bounded buffers.
- Support `Range` requests for resume and media seeking.
- Persist enough client and server transfer state to recover after network or application interruption.
- Keep incomplete uploads in `/srv/personalcloud/uploads` and out of normal listings.
- Validate final size and, where appropriate, checksums before publishing a file.
- Use atomic finalization so partial content never appears as complete.
- Check free space before accepting large uploads.
- Detect source or server-file changes before resuming a transfer.
- Keep default concurrency conservative until measured.
- On Android, account for lifecycle, secure file access, foreground/background constraints, and connectivity changes.

The resumable-upload protocol is deliberately **not locked**. TUS may be evaluated later, but is not mandatory. Chunk sizing, hashing algorithms, retention, and checkpoint formats also remain implementation decisions.

## Flutter Client Direction

**CURRENT REPOSITORY:** `client/` is a generated Flutter application with Android and Linux platforms, `id.donirs.swadrive` as the Android application ID, and a minimal `Hello World` entry point. There is no API client, authentication flow, secure token storage, file browser, or transfer manager yet.

**PLANNED:** Keep the client small initially. Add real boundaries only when needed, for example:

```text
client/lib/
|-- main.dart
|-- core/
|   |-- api/
|   |-- config/
|   `-- errors/
`-- features/
    |-- auth/
    `-- files/
```

Expected client behavior includes health/version checks, application login, independently revocable sessions, folder navigation, file operations, search, transfer progress, interruption recovery, and clear error states on both Linux and Android. Tokens must use an appropriate secure-storage mechanism; they must not be embedded in source or ordinary preferences.

## Development Workflow

All source editing, dependency management, tests, and builds happen on the Arch Linux workstation.

### Backend

From `server/`:

```bash
gofmt -w cmd/server/main.go
go test ./...
go vet ./...
go build ./cmd/server
```

Use the standard library where reasonable. Avoid empty packages, speculative interfaces, unnecessary dependency-injection frameworks, and source layouts that do not yet serve working code.

### Flutter

From `client/`:

```bash
dart format .
flutter analyze
flutter test
flutter run -d linux
```

Use Flutter tooling to manage generated Android and Linux structures. Keep `client/pubspec.lock` tracked. Do not commit `.dart_tool`, IDE metadata, Android `local.properties`, signing keys, or build output.

See [CONTRIBUTING.md](CONTRIBUTING.md) before proposing changes.

## Deployment Model

```text
edit/test/build on doni-arch
          |
          v
statically linked Linux amd64 artifact
          |
          | administrator SSH/SCP
          v
install /opt/personalcloud/swadrive-server as root:root, mode 0755
          |
          v
systemd starts backend as personalcloud_service
```

The Phase 3 artifact is a statically linked Linux amd64 binary built with
`CGO_ENABLED=0` from Git commit `83b7c73` with `vcs.modified=false`. The
production host receives the release artifact, not the development workspace,
and does not require Git or a Go compiler.

The root-owned binary cannot be modified by
`personalcloud_service`. The enabled `swadrive.service` unit uses the following
verified runtime contract:

| Setting | Value |
| --- | --- |
| `User` / `Group` | `personalcloud_service` |
| `WorkingDirectory` | `/var/lib/personalcloud` |
| `ExecStart` | `/opt/personalcloud/swadrive-server` |
| `Restart` | `on-failure` |
| `WantedBy` | `multi-user.target` |

Restart and full-reboot validation passed, with the service returning to the
active/running state. Rollback and database migration procedures must be
defined before changes that need them.

## Testing Strategy

The Phase 3 automated health test is tracked at
`server/cmd/server/main_test.go`. The verified backend checks all pass from
`server/`:

```text
go test ./...
go vet ./...
go test -race ./...
```

Test depth should continue to grow with implementation and risk:

- **Go unit/handler tests:** authentication, version compatibility, errors, and request cancellation.
- **Filesystem security tests:** `..`, absolute paths, encoded traversal, null bytes, symlink escape, root enforcement, and permission failures.
- **Authentication tests:** correct/wrong password, disabled users, expiration, revocation, rate limiting, and token-log redaction.
- **File API tests:** listing, upload/download, conflicts, rename/move, trash/restore, search, and interrupted operations.
- **Large-file tests:** bounded memory, Range behavior, resume, connection loss, changed source/content, final-size and integrity checks.
- **Flutter tests:** startup, login, secure session handling, navigation, transfer state, retries, progress, and error/empty/loading states.
- **Deployment tests:** correct runtime user, filesystem access boundaries, listener exposure, restart, reboot, rollback, backup, and restore.
- **Tailscale verification:** Member clients, server tag, MagicDNS, TCP 8080 reachability, unintended-port denial, and no unintended admins.

The Flutter scaffold includes the Flutter test dependency but currently has no tracked test files.

## Roadmap

The checkboxes below record verified project state, not aspirational completion.

### Phase 0 - Clean Rebuild

**STATUS: COMPLETE**

- [x] identify server environment as native Debian 13
- [x] remove confusing previous regular human-user setup
- [x] retain root as emergency authority
- [x] create admin_home_server
- [x] verify sudo
- [x] clean old Tailscale local installation/state
- [x] delete old Tailscale tailnet/control plane

### Phase 1 - Server Foundation

**STATUS: COMPLETE**

- [x] create personalcloud_service
- [x] configure /usr/sbin/nologin
- [x] verify no sudo
- [x] create /etc/personalcloud
- [x] create /opt/personalcloud
- [x] create /srv/personalcloud/files
- [x] create /srv/personalcloud/uploads
- [x] create /srv/personalcloud/trash
- [x] establish /var/lib/personalcloud
- [x] create /var/log/personalcloud
- [x] configure ownership
- [x] configure baseline permissions
- [x] audit filesystem
- [x] audit service account
- [x] audit human admin account

### Phase 2 - Tailscale Foundation

**STATUS: COMPLETE**

- [x] remove old Tailscale state
- [x] create fresh tailnet
- [x] keep one Tailscale Owner
- [x] define tag:storage-server
- [x] provision storage-server
- [x] verify server tag
- [x] add Member account `kanhantam3@gmail.com`
- [x] join Arch Linux as Member
- [x] verify Arch has no machine tag
- [x] join Android phone as Member
- [x] verify peer visibility
- [x] verify MagicDNS
- [x] establish intended Member -> storage-server TCP 8080 policy
- [x] keep client devices non-admin
- [x] keep server as tagged machine identity

### Phase 3 - Go Foundation

**STATUS: COMPLETE**

- [x] choose doni-arch as development workstation
- [x] verify Go on development workstation
- [x] verify Git on development workstation
- [x] establish monorepo
- [x] configure GitHub remote
- [x] initialize server Go module
- [x] verify server TCP 8080 is free
- [x] remove Go compiler from production server
- [x] remove Git from production server
- [x] remove server-side development workspace
- [x] create server/cmd/server/main.go
- [x] implement GET /api/v1/health
- [x] run backend locally on Arch
- [x] add initial backend tests
- [x] build Linux amd64 production artifact
- [x] generate/check artifact checksum
- [x] transfer artifact to swadrive
- [x] install binary into /opt/personalcloud
- [ ] create server config under /etc/personalcloud
- [x] create systemd unit
- [x] run systemd service as personalcloud_service
- [x] confirm process does not run as root
- [x] test health endpoint through Tailscale
- [x] verify unintended ports are not exposed
- [ ] add initial systemd hardening
- [x] test restart behavior
- [x] test reboot persistence

**Exit criteria:** `GET http://storage-server:8080/api/v1/health` returns `200 OK`, and the backend process runs as `personalcloud_service`.

Unchecked items above remain unverified or deferred; they are not marked
complete merely because the phase exit criteria passed.

### Phase 4 - Application Authentication

**STATUS: NOT STARTED**

- [ ] choose password hashing implementation
- [ ] create migration mechanism
- [ ] create users schema
- [ ] create initial application owner
- [ ] implement login
- [ ] implement logout
- [ ] implement session/token model
- [ ] expiration
- [ ] session revocation
- [ ] authorization middleware
- [ ] failed-login protection/rate limiting
- [ ] authentication audit events
- [ ] multi-device session support

### Phase 5 - File API MVP

**STATUS: NOT STARTED**

- [ ] list folders/files
- [ ] create folder
- [ ] metadata endpoint
- [ ] upload
- [ ] download
- [ ] rename
- [ ] move
- [ ] trash
- [ ] restore
- [ ] search
- [ ] storage-root enforcement
- [ ] path traversal prevention
- [ ] symlink escape prevention
- [ ] disk-space/preflight checks
- [ ] stable API error model
- [ ] permission tests

### Phase 6 - Flutter MVP

**STATUS: NOT STARTED**

- [ ] review client project structure
- [ ] API client
- [ ] server base URL/config
- [ ] login screen
- [ ] secure session/token storage
- [ ] file browser
- [ ] folder navigation
- [ ] upload UI
- [ ] download UI
- [ ] rename
- [ ] move
- [ ] trash
- [ ] restore
- [ ] search
- [ ] transfer progress
- [ ] error handling
- [ ] Linux verification
- [ ] Android verification

### Phase 7 - Large Files & Media

**STATUS: NOT STARTED**

- [ ] upload sessions
- [ ] chunked upload
- [ ] resumable upload
- [ ] interrupted upload recovery
- [ ] resumable download
- [ ] HTTP Range
- [ ] video seeking/streaming
- [ ] large-file integrity checks
- [ ] image preview
- [ ] PDF preview
- [ ] thumbnail generation
- [ ] transfer state persistence

### Phase 8 - Sync & Mobile Enhancements

**STATUS: NOT STARTED**

- [ ] offline cache
- [ ] camera backup
- [ ] background upload
- [ ] retry queue
- [ ] connectivity-aware behavior
- [ ] conflict handling
- [ ] file versioning
- [ ] mobile lifecycle handling

### Phase 9 - Production Hardening

**STATUS: NOT STARTED**

- [ ] final Tailscale Users audit
- [ ] final Tailscale Grants audit
- [ ] final Linux users/groups audit
- [ ] final filesystem ownership audit
- [ ] final filesystem permission audit
- [ ] systemd sandboxing
- [ ] final listening-port audit
- [ ] secret review
- [ ] production logging review
- [ ] backup automation
- [ ] SQLite backup procedure
- [ ] storage backup procedure
- [ ] restore test
- [ ] disaster-recovery notes
- [ ] review and intentionally harden administrative SSH access
- [ ] document the permanent administration policy before stable release
- [ ] dependency review
- [ ] final release checklist
- [ ] choose actual open-source license
- [ ] stable version tag

## Current Next Step

Phase 3 is complete. Phase 4 - Application Authentication is the next roadmap
phase and remains **NOT STARTED**.

## Architecture Decisions

Durable rationale is kept separately from this project-state document:

- [ADR-0001: Use Tailscale for private network access and Go HTTP API for application data](docs/adr/ADR-0001-tailscale-plus-go-api.md)
- [ADR-0002: Run the backend as a dedicated restricted service account](docs/adr/ADR-0002-restricted-service-account.md)

## License

License has not yet been selected. No `LICENSE` file is present, and SwaDrive must not be described as officially open source until an actual license is chosen and added.
