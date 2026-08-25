# SwaDrive

SwaDrive adalah project personal untuk membangun private cloud sederhana yang saya kelola sendiri. Backend Go dirancang berjalan pada storage server Linux dan diakses client Flutter melalui private tailnet, tanpa membuka layanan file ke internet publik.

Project ini menjadi tempat saya mempraktikkan Linux administration, networking, service isolation, least privilege, Go, Flutter, dan secure software engineering. Status implementasi ditulis apa adanya; sebagian besar fitur file masih berupa rancangan.

## Status Project

| Area | Status | Bukti pada repository |
| --- | --- | --- |
| Go HTTP foundation | Selesai | Health endpoint dan handler test |
| Linux service foundation | Terverifikasi di lingkungan pemilik | systemd dan restricted service account |
| Private networking | Terverifikasi di lingkungan pemilik | akses melalui Tailscale dengan rule sempit |
| Application authentication | Belum diimplementasikan | invariant keamanan sudah ditetapkan |
| File API | Belum diimplementasikan | kontrak dan batasan masih dirancang |
| Flutter client | Scaffold | belum memiliki login atau file browser |
| Sync dan resumable transfer | Belum dimulai | roadmap jangka lanjut |

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
             application state   storage directory
             planned             planned file API
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
- path API harus menggunakan resource ID dan selalu berada di dalam configured storage root;
- traversal, symlink escape, absolute path, dan encoded traversal harus ditolak;
- password disimpan sebagai hash, sementara session token tidak disimpan atau dicatat dalam bentuk mentah;
- incomplete upload dipisahkan dari file yang sudah valid;
- normal delete dirancang menuju trash agar dapat dipulihkan;
- secret, database, backup, log, dan data pengguna production tidak boleh masuk repository.

Invariant sebelum file API boleh menyajikan data:

> File API MUST implement application-level authentication and authorization before serving user data.

## Struktur Repository

```text
SwaDrive/
├── client/                 scaffold Flutter untuk Linux dan Android
├── server/
│   ├── cmd/server/main.go
│   ├── cmd/server/main_test.go
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

```bash
cd server
go test ./...
go vet ./...
go run ./cmd/server
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

1. application authentication dan independently revocable sessions;
2. resource model serta filesystem boundary tests;
3. list, upload, stream, download, rename, move, trash, dan restore;
4. Flutter login dan file browser;
5. Range request serta resumable transfer;
6. hardening dan observability berdasarkan fitur yang benar-benar tersedia.

Setiap endpoint protected harus memiliki negative authorization tests. Implementasi file path harus menguji traversal, symlink escape, conflict, permission failure, dan cancellation.

## Yang Saya Pelajari

- memisahkan network access dari application authorization;
- menjalankan service dengan Linux identity terbatas;
- membedakan deployment authority dan runtime authority;
- mengelola systemd service dan private-network boundary;
- menulis HTTP handler Go dengan test terfokus;
- mendokumentasikan fitur implemented, verified, dan planned secara terpisah.

## Status Publikasi

Repository ini layak menjadi flagship sebagai engineering work-in-progress, bukan produk selesai. Foundation dan security model sudah nyata, tetapi application authentication, file API, dan Flutter product flow belum tersedia.

Project belum memiliki stable release atau lisensi open-source. Lihat [SECURITY.md](SECURITY.md) untuk pelaporan masalah keamanan.

## Backend v1

Backend v1 is implemented and verified locally but has not been deployed to
production.

The current Go backend includes:

- application authentication with Argon2id password hashing;
- opaque, independently revocable server-side sessions;
- bounded login-abuse protection and security audit events;
- authenticated file listing, metadata, search, move, trash, and restore;
- streaming downloads with HTTP Range support;
- persistent fixed-chunk resumable uploads;
- a rebuildable SQLite metadata index for normal list/search/metadata reads;
- explicit local-admin owner bootstrap and metadata reindex commands;
- path-traversal and symlink-escape protections;
- bounded resource usage for expensive authentication and transfer work.

Normal metadata operations are served from SQLite. User file bytes remain on
the filesystem and are accessed only when content I/O or a filesystem mutation
is required.

The backend has passed unit/integration tests, `go vet`, race testing, static
Linux builds, and vulnerability scanning with no reachable known
vulnerabilities at the time of the backend-v1 verification.

See ADR-0003 through ADR-0005 for the authentication, resumable-upload, and
metadata-plane decisions.
