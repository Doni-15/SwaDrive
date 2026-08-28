# SwaDrive

SwaDrive adalah proyek pribadi untuk membangun private cloud sederhana yang
dikelola sendiri. Backend Go berjalan pada server Linux yang menangani storage
dan diakses oleh client Flutter melalui tailnet privat, tanpa membuka layanan
file ke internet publik.

Proyek ini menjadi sarana untuk mempraktikkan administrasi Linux, networking,
isolasi service, least privilege, Go, Flutter, dan secure software engineering.
Backend `v1.0.0` telah menjadi baseline untuk production. Source terbaru
`v1.0.2` menambahkan hardening konsistensi, timeout transfer, pemulihan
credential owner, CI, serta deployment Docker yang reproducible. Alur produk
Flutter masih berupa scaffold dan belum diimplementasikan.

## Status Proyek

| Area | Status | Bukti/status |
| --- | --- | --- |
| Fondasi HTTP backend Go | Source `v1.0.2` | Health/readiness terpisah, timeout request dan transfer |
| Fondasi service di Linux | Production `v1.0.0` | `systemd` dan service account terbatas |
| Jaringan privat | Production `v1.0.0` | Akses Tailscale dengan rule akses yang ketat |
| Autentikasi aplikasi | Production `v1.0.0` | Argon2id, server-side session berbasis opaque token, pencabutan, limiter, dan audit log |
| File API | Source `v1.0.2` | Operasi content dan metadata fail-closed sesuai state reconciliation |
| Resumable transfer | Source `v1.0.2` | Stored chunk di-hash ulang dan repair pascapublikasi tidak mengikuti cancellation client |
| Client Flutter | Scaffold | Belum memiliki login atau file browser |
| Deployment backend | `systemd` production + Docker opsional | Image distroless non-root, bind mount persisten, dan host binding privat |

## Rilis

Baseline production yang terdokumentasi: [SwaDrive `v1.0.0`](docs/releases/v1.0.0.md)

Source release terbaru: [SwaDrive `v1.0.2`](docs/releases/v1.0.2.md)

Target major berikutnya: [SwaDrive `v2.0.0`](docs/releases/NEXT.md)

Ringkasan versi tersedia dalam [changelog](CHANGELOG.md). Source code backend
untuk rilis `v1.0.0` dibekukan pada commit
`accbe9223e2591fe7a88fe4652031f3bdfc529a9` sebagai baseline production.

Repository dapat tersedia secara publik untuk portofolio dan transparansi.
SwaDrive tetap merupakan proyek pribadi yang dikelola satu maintainer;
kontribusi eksternal dan pull request tidak sedang diterima. Repository ini
saat ini tidak menyediakan lisensi open source. Pelaporan kerentanan tetap
ditangani secara terpisah melalui [kebijakan keamanan](SECURITY.md).

Health endpoint melaporkan kesehatan proses dan ketersediaan content
storage secara terpisah:

```http
GET /api/v1/health
```

```json
{"status":"ok","storage":"available"}
```

Jika content storage tidak dapat diverifikasi, endpoint yang sama tetap
merespons HTTP 200 dengan
`{"status":"degraded","storage":"unavailable"}`.

`GET /api/v1/ready` memeriksa database, kesehatan metadata index, dan startup
reconciliation gate. Endpoint tersebut mengembalikan HTTP 503 `not_ready`
ketika control/metadata plane belum aman untuk dilayani. Storage yang degraded
tetap dilaporkan sebagai field terpisah karena proses sengaja mempertahankan
API autentikasi saat HDD tidak tersedia.

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
- command lokal bagi administrator untuk bootstrap owner, mengganti password
  owner sambil mencabut seluruh session, reindex metadata, dan melakukan
  reconciliation terhadap orphan upload part;
- degraded storage mode yang menjaga autentikasi, session, dan health tetap
  dapat dijangkau, sementara operasi content ditolak dengan HTTP 503 dan kode
  `storage_unavailable`.

Workflow CI menjalankan build, test, race detector, vet, format Go, format dan
analyzer Flutter, widget test, serta build image Docker. Hasil suatu commit
hanya boleh disebut lulus setelah workflow atau command lokal untuk commit
tersebut benar-benar selesai.

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
               Go API melalui systemd
                service account terbatas
                    |             |
               NVMe/state        HDD/content
                 SQLite       files/uploads/trash
         listing/search/meta   isi file pengguna
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
- client tidak membawa SSH private key dan tidak memakai SSH sebagai protokol
  transfer data;
- server hanya dapat dicapai melalui tailnet privat pada port aplikasi yang
  diperlukan;
- File API memakai logical path yang divalidasi; physical path pada host bukan
  input atau output API;
- traversal, symlink escape, absolute path, dan encoded traversal ditolak;
- password disimpan sebagai hash, sedangkan session token tidak disimpan atau
  dicatat dalam bentuk mentah;
- upload yang belum selesai dipisahkan dari file yang sudah valid;
- penghapusan normal diarahkan ke trash agar data dapat dipulihkan;
- secret, database, backup, log, dan data pengguna di production tidak boleh
  masuk repository.

Invariant file API:

> Setiap operasi terhadap byte pengguna MUST mewajibkan autentikasi aplikasi
> dan otorisasi owner, terlepas dari keterjangkauan melalui tailnet.

## Struktur Repository

```text
SwaDrive/
├── .github/workflows/ci.yml
├── CHANGELOG.md
├── client/                 scaffold Flutter untuk Linux dan Android
├── server/
│   ├── cmd/server/
│   ├── cmd/swadrive-admin/
│   ├── cmd/swadrive-healthcheck/
│   ├── internal/{admincli,audit,auth,config,database,files,httpapi,storage,uploads}/
│   ├── Dockerfile
│   └── go.mod
├── compose.yaml
├── docs/
│   ├── architecture.md
│   ├── security-model.md
│   ├── adr/
│   └── releases/
├── SECURITY.md
└── README.md
```

Struktur baru hanya ditambahkan ketika fitur nyata membutuhkannya.

## Menjalankan Backend

Persyaratan: Go sesuai versi module pada `server/go.mod`.

Backend memerlukan path database, content root, dan identitas volume yang
eksplisit. Contoh berikut memakai placeholder lokal. Marker harus tersedia dan
sesuai sebelum operasi content diaktifkan; tanpa marker yang valid, proses tetap
berjalan dalam degraded storage mode:

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
SWADRIVE_LISTEN_ADDRESS=127.0.0.1:8080
SWADRIVE_STORAGE_RESERVE_BYTES=1073741824
SWADRIVE_UPLOAD_CLEANUP_INTERVAL=15m
SWADRIVE_MAX_CONCURRENT_ARGON2=4
SWADRIVE_MAX_CONCURRENT_CHUNKS=8
SWADRIVE_MAX_CONCURRENT_DOWNLOADS=32
```

Catatan upgrade native `v1.0.2`: versi sebelumnya memakai default `:8080`
(seluruh interface), sedangkan default baru adalah `127.0.0.1:8080`. Deployment
native yang diakses langsung melalui alamat Tailscale harus menetapkan
`SWADRIVE_LISTEN_ADDRESS='<tailscale-ip>:8080'` atau alamat lain yang sudah
diaudit. Perubahan ini tidak memutus method/path/JSON API, tetapi mengubah
default konfigurasi jaringan.

Marker `.swadrive-volume` memverifikasi identitas yang diharapkan aplikasi,
bukan membuktikan bahwa HDD benar-benar ter-mount. Deployment di production
tetap wajib memastikan mount, urutan, dan ownership melalui OS serta `systemd`.
Storage root yang ter-mount dan marker harus dikendalikan administrator,
sedangkan `files/`, `uploads/`, dan `trash/` merupakan batas penyimpanan yang
dapat ditulis service serta harus berada pada filesystem yang sama. Area state
harus memungkinkan service membuat dan menulis file SQLite DB/WAL/SHM serta
lock untuk koordinasi proses. Flock mengoordinasikan proses SwaDrive yang
bekerja sama, bukan melindungi dari writer berbahaya dengan UID yang sama. Tata
letak permission yang tepat tetap menjadi keputusan deployment dan harus diuji.

Provider storage `v1.0.1` memverifikasi marker, directory wajib, dan batas satu
filesystem sebelum setiap operasi content. Jika verifikasi gagal saat startup
atau runtime, storage ditandai tidak tersedia hingga proses di-restart. Aplikasi
tidak melakukan pemulihan otomatis di dalam proses agar reconciliation
trash/upload dan pemeriksaan kesehatan metadata selalu dijalankan sebelum
content access dibuka kembali. Unit `systemd` aktual tidak boleh memiliki
dependensi wajib pada mount yang mencegah proses backend dimulai; perubahan ini
tidak mengurangi service account, permission, sandbox, firewall, atau batas
Tailscale.

Command administrasi lokal berikut bukan HTTP endpoint:

```bash
go run ./cmd/swadrive-admin bootstrap-owner -database '<state-dir>/swadrive.db' -username '<owner>'
go run ./cmd/swadrive-admin set-owner-password -database '<state-dir>/swadrive.db' -username '<owner>'
go run ./cmd/swadrive-admin reindex -database '<state-dir>/swadrive.db' -storage '<storage-root>' -volume-id '<volume-id>'
go run ./cmd/swadrive-admin reconcile-upload-parts -database '<state-dir>/swadrive.db' -storage '<storage-root>' -volume-id '<volume-id>'
# Tinjau dry-run; tambahkan -apply hanya untuk menghapus orphan part yang lolos age/name/type policy.
```

Secara default, server hanya mendengarkan `127.0.0.1:8080`. Untuk akses langsung
melalui tailnet, set alamat Tailscale host secara eksplisit; jangan memakai
all-interface listener tanpa firewall dan batas jaringan yang telah diverifikasi.
Health endpoint dapat diperiksa dari host:

```bash
curl http://127.0.0.1:8080/api/v1/health
```

Client harus membedakan kegagalan koneksi dari error API. Kontrak ringkas untuk
scaffold Flutter tersedia dalam [README client](client/README.md).

## Menjalankan Backend dengan Docker

Image server dibangun secara multi-stage dari `server/Dockerfile`; runtime
distroless hanya membawa tiga binary, berjalan sebagai UID/GID `65532`, dan
tidak memuat source, Flutter output, `.git`, atau secret. `compose.yaml`
menambahkan root filesystem read-only, drop seluruh capability, persistent bind
mount untuk SQLite dan content, serta port host yang default ke loopback.

Panduan persiapan directory/ownership, build, konfigurasi, Tailscale,
start/stop, command admin, backup, dan update tersedia dalam
[deployment Docker](docs/deployment-docker.md). Android/mobile Flutter tidak
dijalankan sebagai container production.

## Menjalankan Scaffold Flutter

Persyaratan: Flutter SDK dan toolchain platform target.

```bash
cd client
flutter pub get
flutter analyze
flutter run -d linux
```

Client saat ini masih berupa scaffold. Tidak ada credential, alamat server di
production, atau identifier operasional privat yang disimpan dalam source code.

## Dokumentasi

- [Arsitektur](docs/architecture.md) menjelaskan komponen, alur data, model
  storage, batas service, serta runtime dan deployment.
- [Model keamanan](docs/security-model.md) menjelaskan trust boundary,
  autentikasi, otorisasi, keamanan storage dan jaringan, serta keterbatasan yang
  diketahui.
- [Architecture Decision Records](docs/adr/README.md) menyimpan keputusan
  arsitektur historis.
- [Changelog](CHANGELOG.md) memuat ringkasan perubahan secara kronologis.
- [Catatan rilis](docs/releases/README.md) memuat detail dan provenance setiap
  rilis.
- [Kebijakan keamanan](SECURITY.md) menjelaskan pelaporan kerentanan.

## Roadmap

Target major berikutnya adalah `v2.0.0`. Cakupan fiturnya belum ditetapkan
sebagai komitmen dan hanya akan ditambahkan setelah keputusan implementasi
disetujui. Lihat [penanda perencanaan `v2.0.0`](docs/releases/NEXT.md).

## Yang Saya Pelajari

- memisahkan akses jaringan dari otorisasi aplikasi;
- menjalankan service dengan identitas Linux terbatas;
- membedakan kewenangan deployment dan runtime;
- mengelola service `systemd` dan batas jaringan privat;
- menulis HTTP handler Go dengan test terfokus;
- mendokumentasikan fitur yang sudah diimplementasikan, diverifikasi, dan
  direncanakan secara terpisah.
