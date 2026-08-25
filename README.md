# SwaDrive

SwaDrive adalah project personal untuk membangun private cloud sederhana yang saya kelola sendiri. Backend Go dirancang berjalan pada storage server Linux dan diakses client Flutter melalui private tailnet, tanpa membuka layanan file ke internet publik.

Project ini menjadi tempat saya mempraktikkan Linux administration, networking, service isolation, least privilege, Go, Flutter, dan secure software engineering. Backend v1 sudah diimplementasikan dan diuji secara lokal; Flutter product flow dan deployment production berikutnya masih terpisah.

## Status Project

| Area | Status | Bukti pada repository |
| --- | --- | --- |
| Go HTTP foundation | Selesai | Health endpoint dan handler test |
| Linux service foundation | Terverifikasi di lingkungan pemilik | systemd dan restricted service account |
| Private networking | Terverifikasi di lingkungan pemilik | akses melalui Tailscale dengan rule sempit |
| Application authentication | Backend v1 selesai secara lokal | Argon2id, opaque sessions, revocation, limiter, audit |
| File API | Backend v1 selesai secara lokal | logical-path API, SQLite metadata index, owner authorization |
| Resumable transfer | Backend v1 selesai secara lokal | streaming fixed chunks, restart recovery, atomic publication |
| Flutter client | Scaffold | belum memiliki login atau file browser |
| Production deployment backend v1 | Belum dilakukan | memerlukan review mount, ownership, Tailscale, backup, dan capacity |

Health endpoint yang tersedia saat ini:

```http
GET /api/v1/health
```

```json
{"status":"ok"}
```

## Tujuan

- menyimpan file pada server Linux yang dikendalikan sendiri;
- menyediakan client Linux dan Android melalui Flutter;
- memakai private networking tanpa public port forwarding;
- memisahkan identitas administrator, runtime service, network device, dan application user;
- mendukung operasi file dengan batas filesystem yang ketat;
- mentransfer file secara streaming agar penggunaan memori tidak mengikuti ukuran file;
- menyediakan session per perangkat yang dapat dicabut secara terpisah.

SwaDrive tidak ditujukan sebagai distributed storage, enterprise SSO, public file-sharing service, atau pengganti administrasi SSH.

## Arsitektur

```text
Linux client                       Android client
normal tailnet member              normal tailnet member
       |                                  |
       +---------- private tailnet -------+
                          |
                    storage server
                    Debian/Linux
                          |
              Go API under systemd
              restricted service account
                    |             |
               NVMe/state        HDD/content
                 SQLite       files/uploads/trash
           list/search/meta      user bytes
```

Empat lapisan identitas sengaja dipisahkan:

1. administrator account untuk pengelolaan OS dan deployment;
2. private-tailnet identity untuk membatasi perangkat yang dapat mencapai service;
3. application identity untuk autentikasi dan otorisasi operasi pengguna;
4. service account untuk membatasi dampak bila proses backend dikompromikan.

Private network hanya mengatur reachability. Ia tidak menggantikan application-level authentication atau authorization.

Penjelasan lebih lengkap tersedia di [arsitektur](docs/architecture.md) dan [security model](docs/security-model.md).

## Prinsip Keamanan

- backend tidak berjalan sebagai `root` atau administrator account;
- binary dan konfigurasi deployment tidak dapat ditulis oleh runtime service account;
- client tidak membawa SSH private key dan tidak memakai SSH sebagai data protocol;
- server hanya dapat dicapai melalui private tailnet pada port aplikasi yang diperlukan;
- File API memakai logical path yang divalidasi; physical host path tidak menjadi input atau output API;
- traversal, symlink escape, absolute path, dan encoded traversal harus ditolak;
- password disimpan sebagai hash, sementara session token tidak disimpan atau dicatat dalam bentuk mentah;
- incomplete upload dipisahkan dari file yang sudah valid;
- normal delete dirancang menuju trash agar dapat dipulihkan;
- secret, database, backup, log, dan data pengguna production tidak boleh masuk repository.

Invariant file API:

> Every user-byte operation MUST require application authentication and owner authorization, independent of tailnet reachability.

## Struktur Repository

```text
SwaDrive/
├── client/                 scaffold Flutter untuk Linux dan Android
├── server/
│   ├── cmd/server/
│   ├── cmd/swadrive-admin/
│   ├── internal/{auth,audit,database,files,httpapi,storage,uploads}/
│   └── go.mod
├── docs/
│   ├── architecture.md
│   ├── security-model.md
│   └── adr/
├── SECURITY.md
└── README.md
```

Struktur baru hanya akan ditambah ketika fitur nyata membutuhkannya. Repository tidak menyiapkan abstraction, container, atau deployment layer spekulatif.

## Menjalankan Backend

Persyaratan: Go sesuai versi module pada `server/go.mod`.

Backend memerlukan database path, content root, dan volume identity eksplisit.
Contoh berikut memakai placeholder lokal; marker harus diprovisikan administrator
sebelum service account menjalankan server:

```bash
printf '%s\n' '<volume-id>' > '<storage-root>/.swadrive-volume'

cd server
go test ./...
go vet ./...
SWADRIVE_DATABASE_PATH='<state-dir>/swadrive.db' \
SWADRIVE_STORAGE_ROOT='<storage-root>' \
SWADRIVE_STORAGE_VOLUME_ID='<volume-id>' \
go run ./cmd/server
```

Konfigurasi opsional yang memiliki default terbatas:

```text
SWADRIVE_LISTEN_ADDRESS=:8080
SWADRIVE_STORAGE_RESERVE_BYTES=1073741824
SWADRIVE_UPLOAD_CLEANUP_INTERVAL=15m
SWADRIVE_MAX_CONCURRENT_ARGON2=4
SWADRIVE_MAX_CONCURRENT_CHUNKS=8
SWADRIVE_MAX_CONCURRENT_DOWNLOADS=32
```

Marker `.swadrive-volume` membuktikan identity yang diharapkan oleh aplikasi,
bukan bahwa HDD benar-benar mounted. Deployment production tetap wajib
memastikan mount/order/ownership melalui OS dan systemd. Mounted storage root
dan marker harus administrator-controlled, sedangkan `files/`, `uploads/`, dan
`trash/` adalah service-writable content boundary dan harus berada pada
filesystem yang sama. State area harus memungkinkan service membuat/menulis
SQLite DB/WAL/SHM dan coordination lock; flock mengoordinasikan proses SwaDrive
yang bekerja sama, bukan melindungi dari hostile same-UID writer. Exact
permission layout tetap keputusan deployment yang harus diuji.

Admin command lokal (bukan HTTP endpoint):

```bash
go run ./cmd/swadrive-admin bootstrap-owner -database '<state-dir>/swadrive.db' -username '<owner>'
go run ./cmd/swadrive-admin reindex -database '<state-dir>/swadrive.db' -storage '<storage-root>' -volume-id '<volume-id>'
go run ./cmd/swadrive-admin reconcile-upload-parts -database '<state-dir>/swadrive.db' -storage '<storage-root>' -volume-id '<volume-id>'
# Tinjau dry-run; tambahkan -apply hanya untuk menghapus orphan part yang lolos age/name/type policy.
```

Secara default server mendengarkan TCP `8080`. Endpoint health dapat dicek dari host yang memang diizinkan oleh private-network policy:

```bash
curl http://127.0.0.1:8080/api/v1/health
```

## Menjalankan Flutter Scaffold

Persyaratan: Flutter SDK dan toolchain platform target.

```bash
cd client
flutter pub get
flutter analyze
flutter run -d linux
```

Client saat ini masih scaffold. Tidak ada credential, server address production, atau private operational identifier yang disimpan di source.

## Roadmap

1. selesaikan final independent review dan freeze backend Go v1;
2. review/deploy OS storage, mount, ownership, Tailscale, backup, dan monitoring;
3. Flutter login dan file browser;
4. integrasi client dengan Range dan resumable upload yang sudah tersedia;
5. hardening dan observability berdasarkan penggunaan nyata.

Setiap endpoint protected harus memiliki negative authorization tests. Implementasi file path harus menguji traversal, symlink escape, conflict, permission failure, dan cancellation.

## Yang Saya Pelajari

- memisahkan network access dari application authorization;
- menjalankan service dengan Linux identity terbatas;
- membedakan deployment authority dan runtime authority;
- mengelola systemd service dan private-network boundary;
- menulis HTTP handler Go dengan test terfokus;
- mendokumentasikan fitur implemented, verified, dan planned secara terpisah.

## Status Publikasi

Repository ini adalah engineering work-in-progress, bukan produk selesai. Backend v1 memiliki tested security controls, tetapi belum dinyatakan production-ready; Flutter product flow juga belum tersedia.

Project belum memiliki stable release atau lisensi open-source. Lihat [SECURITY.md](SECURITY.md) untuk pelaporan masalah keamanan.

## Backend v1

Backend v1 is implemented and verified locally but has not been deployed to
production.

The current Go backend includes:

- application authentication with Argon2id password hashing;
- opaque, independently revocable server-side sessions;
- bounded login-abuse protection and security audit events;
- one transition audit per newly blocked account/IP bucket, so already-blocked
  requests do not amplify append-only SQLite rows;
- authenticated file listing, metadata, search, move, trash, and restore;
- streaming downloads with HTTP Range support;
- persistent fixed-chunk resumable uploads;
- a rebuildable SQLite metadata index for normal list/search/metadata reads;
- explicit local-admin owner bootstrap and metadata reindex commands;
- explicit, age-gated dry-run/apply reconciliation for unknown upload parts;
- one-process database ownership lock and durable unhealthy index intent around
  mkdir/move filesystem-to-SQLite crash windows;
- verified `.swadrive-volume` identity and same-filesystem content directories;
- path-traversal and symlink-escape protections;
- bounded resource usage for expensive authentication and transfer work.

Normal metadata operations are served from SQLite. User file bytes remain on
the filesystem and are accessed only when content I/O or a filesystem mutation
is required.

The backend has passed unit/integration tests, `go vet`, race testing, static
Linux builds, and vulnerability scanning with no reachable known
vulnerabilities at the time of the backend-v1 verification.

See ADR-0003 through ADR-0006 for the authentication, resumable-upload,
metadata-plane, process-coordination, and storage-identity decisions.

Passwords are one-way Argon2id hashes and raw session tokens are represented in
SQLite only by SHA-256 digests. This is not application encryption of user
files or SQLite. The Go app does not terminate TLS; transport confidentiality is
assigned to the later verified Tailscale deployment. Filesystem/database
encryption at rest remains an OS/storage product decision.
