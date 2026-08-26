# SwaDrive

SwaDrive adalah proyek pribadi untuk membangun private cloud sederhana yang
dikelola sendiri. Backend Go berjalan pada storage server Linux dan diakses oleh
client Flutter melalui tailnet privat, tanpa membuka layanan file ke internet
publik.

Proyek ini menjadi sarana untuk mempraktikkan administrasi Linux, networking,
isolasi service, least privilege, Go, Flutter, dan secure software engineering.
Backend untuk `v1.0.0` telah menjadi baseline production, sedangkan alur produk
Flutter masih berupa scaffold dan belum diimplementasikan.

## Status Proyek

| Area | Status | Bukti/status |
| --- | --- | --- |
| Fondasi HTTP backend Go | Selesai | Health endpoint dan test handler |
| Fondasi service Linux | production | `systemd` dan service account terbatas |
| Jaringan privat | production | Akses Tailscale dengan rule yang sempit |
| Autentikasi aplikasi | Baseline production `v1.0.0` | Argon2id, server-side session berbasis opaque token, pencabutan, limiter, dan audit log |
| File API | Baseline production `v1.0.0` | API berbasis logical path, metadata index SQLite, dan otorisasi owner |
| Resumable transfer | Baseline production `v1.0.0` | Streaming berbasis fixed chunk, recovery setelah restart, dan publikasi atomik |
| Client Flutter | Scaffold | Belum memiliki login atau file browser |
| Deployment backend di production | Dirilis 2026-08-26 | Debian, `systemd`, metadata pada NVMe, isi file pada HDD, dan Tailscale |

## Rilis

Rilis stabil saat ini: [SwaDrive `v1.0.0`](docs/releases/v1.0.0.md)

Target major berikutnya: [SwaDrive `v2.0.0`](docs/releases/NEXT.md)

Ringkasan versi tersedia dalam [changelog](CHANGELOG.md). Source code backend
untuk rilis `v1.0.0` dibekukan pada commit
`accbe9223e2591fe7a88fe4652031f3bdfc529a9` sebagai baseline production.

Proyek belum memiliki lisensi open source. Lihat [SECURITY.md](SECURITY.md)
untuk pelaporan masalah keamanan.

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
- memakai jaringan privat tanpa public port forwarding;
- memisahkan identitas administrator, runtime service account, perangkat
  jaringan, dan akun aplikasi SwaDrive;
- mendukung operasi file dengan batas filesystem yang ketat;
- mentransfer file secara streaming agar penggunaan memori tidak mengikuti
  ukuran file;
- menyediakan session per perangkat yang dapat dicabut secara terpisah.

SwaDrive tidak ditujukan sebagai distributed storage, enterprise SSO, public
file-sharing service, atau pengganti administrasi melalui SSH.

## Fitur Utama

- autentikasi aplikasi dengan hash password Argon2id dan server-side session
  berbasis opaque token yang dapat dicabut secara independen;
- operasi file berbasis logical path untuk listing, metadata, pencarian,
  pemindahan, trash, restore, serta download streaming dengan dukungan HTTP
  Range;
- resumable upload berbasis fixed chunk dengan checksum, recovery setelah
  restart, dan publikasi atomik tanpa overwrite;
- metadata index SQLite yang dapat dibangun ulang untuk listing, pencarian, dan
  metadata tanpa melakukan tree scan pada HDD dalam operasi normal;
- audit log append-only untuk aktivitas autentikasi, session, file, dan upload;
- command lokal bagi administrator untuk melakukan bootstrap owner, reindex
  metadata, dan reconciliation terhadap orphan upload part.

Backend telah melewati unit test dan integration test, `go vet`, race test,
build statis untuk Linux, serta vulnerability scan yang tidak menemukan
kerentanan yang diketahui dan dapat dijangkau pada saat verifikasi backend
`v1.0.0`.

## Arsitektur

```text
client Linux                       client Android
anggota tailnet biasa              anggota tailnet biasa
       |                                  |
       +----------- tailnet privat -------+
                          |
                    storage server
                    Debian/Linux
                          |
                 Go API via systemd
                service account terbatas
                    |             |
               NVMe/state        HDD/content
                 SQLite       files/uploads/trash
         listing/search/meta     byte pengguna
```

Empat lapisan identitas sengaja dipisahkan:

1. akun administrator untuk pengelolaan OS dan deployment;
2. identitas tailnet privat untuk membatasi perangkat yang dapat mencapai
   service;
3. akun aplikasi SwaDrive untuk autentikasi dan otorisasi operasi pengguna;
4. service account untuk membatasi dampak jika proses backend dikompromikan.

Jaringan privat hanya mengatur keterjangkauan. Batas ini tidak menggantikan
autentikasi atau otorisasi pada level aplikasi.

Penjelasan lebih lengkap tersedia dalam [arsitektur](docs/architecture.md) dan
[model keamanan](docs/security-model.md).

## Prinsip Keamanan

- backend tidak berjalan sebagai `root` atau akun administrator;
- binary dan konfigurasi deployment tidak dapat ditulis oleh runtime service
  account;
- client tidak membawa SSH private key dan tidak memakai SSH sebagai data
  protocol;
- server hanya dapat dicapai melalui tailnet privat pada port aplikasi yang
  diperlukan;
- File API memakai logical path yang divalidasi; physical path pada host bukan
  input atau output API;
- traversal, symlink escape, absolute path, dan encoded traversal ditolak;
- password disimpan sebagai hash, sedangkan session token tidak disimpan atau
  dicatat dalam bentuk mentah;
- upload yang belum selesai dipisahkan dari file yang sudah valid;
- penghapusan normal diarahkan ke trash agar data dapat dipulihkan;
- secret, database, backup, log, dan data pengguna production tidak boleh masuk
  repository.

Invariant file API:

> Setiap operasi terhadap byte pengguna MUST mewajibkan autentikasi aplikasi
> dan otorisasi owner, terlepas dari keterjangkauan melalui tailnet.

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
│   ├── adr/
│   └── releases/
├── SECURITY.md
└── README.md
```

Struktur baru hanya ditambahkan ketika fitur nyata membutuhkannya. Repository
tidak menyiapkan abstraksi, container, atau layer deployment spekulatif.

## Menjalankan Backend

Persyaratan: Go sesuai versi module pada `server/go.mod`.

Backend memerlukan path database, content root, dan identitas volume yang
eksplisit. Contoh berikut memakai placeholder lokal; administrator harus
menyediakan marker sebelum service account menjalankan server:

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

Konfigurasi opsional berikut memiliki default yang terbatas:

```text
SWADRIVE_LISTEN_ADDRESS=:8080
SWADRIVE_STORAGE_RESERVE_BYTES=1073741824
SWADRIVE_UPLOAD_CLEANUP_INTERVAL=15m
SWADRIVE_MAX_CONCURRENT_ARGON2=4
SWADRIVE_MAX_CONCURRENT_CHUNKS=8
SWADRIVE_MAX_CONCURRENT_DOWNLOADS=32
```

Marker `.swadrive-volume` memverifikasi identitas yang diharapkan aplikasi,
bukan membuktikan bahwa HDD benar-benar ter-mount. Deployment production tetap
wajib memastikan mount, urutan, dan ownership melalui OS serta `systemd`.
Storage root yang ter-mount dan marker harus dikendalikan administrator,
sedangkan `files/`, `uploads/`, dan `trash/` merupakan content boundary yang
dapat ditulis service serta harus berada pada filesystem yang sama. Area state
harus memungkinkan service membuat dan menulis file SQLite DB/WAL/SHM serta
coordination lock. Flock mengoordinasikan proses SwaDrive yang bekerja sama,
bukan melindungi dari writer berbahaya dengan UID yang sama. Tata letak
permission yang tepat tetap menjadi keputusan deployment dan harus diuji.

Command administrasi lokal berikut bukan HTTP endpoint:

```bash
go run ./cmd/swadrive-admin bootstrap-owner -database '<state-dir>/swadrive.db' -username '<owner>'
go run ./cmd/swadrive-admin reindex -database '<state-dir>/swadrive.db' -storage '<storage-root>' -volume-id '<volume-id>'
go run ./cmd/swadrive-admin reconcile-upload-parts -database '<state-dir>/swadrive.db' -storage '<storage-root>' -volume-id '<volume-id>'
# Tinjau dry-run; tambahkan -apply hanya untuk menghapus orphan part yang lolos age/name/type policy.
```

Secara default, server mendengarkan TCP `8080`. Health endpoint dapat diperiksa
dari host yang diizinkan oleh kebijakan jaringan privat:

```bash
curl http://127.0.0.1:8080/api/v1/health
```

## Menjalankan Scaffold Flutter

Persyaratan: Flutter SDK dan toolchain platform target.

```bash
cd client
flutter pub get
flutter analyze
flutter run -d linux
```

Client saat ini masih berupa scaffold. Tidak ada credential, alamat server
production, atau identifier operasional privat yang disimpan dalam source.

## Dokumentasi

- [Arsitektur](docs/architecture.md) menjelaskan komponen, alur data, model
  storage, batas service, serta runtime dan deployment.
- [Model keamanan](docs/security-model.md) menjelaskan trust boundary,
  autentikasi, otorisasi, keamanan storage dan jaringan, serta keterbatasan yang
  diketahui.
- [Architecture Decision Records](docs/adr/README.md) menyimpan keputusan
  arsitektur historis.
- [Catatan rilis](docs/releases/README.md) memuat status dan provenance rilis.
- [Kebijakan keamanan](SECURITY.md) menjelaskan pelaporan kerentanan.

## Roadmap

Target major berikutnya adalah `v2.0.0`. Cakupan fiturnya belum ditetapkan
sebagai komitmen dan hanya akan ditambahkan setelah keputusan implementasi
disetujui. Lihat [penanda perencanaan `v2.0.0`](docs/releases/NEXT.md).

Setiap endpoint yang dilindungi harus memiliki negative authorization test.
Implementasi path file harus menguji traversal, symlink escape, conflict,
permission failure, dan cancellation.

## Yang Saya Pelajari

- memisahkan akses jaringan dari otorisasi aplikasi;
- menjalankan service dengan identitas Linux terbatas;
- membedakan kewenangan deployment dan runtime;
- mengelola service `systemd` dan batas jaringan privat;
- menulis HTTP handler Go dengan test terfokus;
- mendokumentasikan fitur yang sudah diimplementasikan, diverifikasi, dan
  direncanakan secara terpisah.
