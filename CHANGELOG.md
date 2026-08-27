# Changelog

Semua perubahan penting pada proyek SwaDrive dicatat di sini.

## [Unreleased]

Target major berikutnya adalah `v2.0.0`.

Belum ada fitur `v2.0.0` yang dicatat sebagai selesai. Lihat
[penanda rilis `NEXT`](docs/releases/NEXT.md) untuk status perencanaan.

## [v1.0.1] - 2026-08-27

Patch keandalan untuk menjaga control plane dan HTTP API tetap dapat
dijangkau ketika content storage tidak tersedia.

### Diperbaiki

- Backend tetap berjalan saat content storage tidak dapat dibuka atau
  diverifikasi pada startup.
- Health endpoint kini melaporkan `storage=available` atau
  `storage=unavailable` dan memakai status aplikasi `degraded` untuk kondisi
  kedua.
- Operasi file dan upload yang membutuhkan HDD tetap fail-closed dengan HTTP
  503 dan kode `storage_unavailable`, bukan `server_busy`.
- Runtime storage loss menghentikan content access berikutnya tanpa menjatuhkan
  proses dan tanpa memakai directory lokal di balik mount point sebagai
  fallback.

### Operasional

- Pemulihan content storage memerlukan restart backend agar validasi identitas,
  reconciliation trash/upload, dan pemeriksaan kesehatan metadata dijalankan
  ulang sebelum operasi content dibuka kembali.

## [v1.0.0] - 2026-08-26

Rilis production pertama. Deskripsi kanonis dan provenance rilis
tersedia dalam [catatan rilis `v1.0.0`](docs/releases/v1.0.0.md).

### Ditambahkan

- Menambahkan backend Go `/api/v1` dengan autentikasi aplikasi, otorisasi owner,
  server-side session berbasis opaque token yang dapat dicabut secara
  independen, dan health endpoint.
- Menambahkan API berbasis logical path khusus owner untuk listing file,
  metadata, pencarian, pembuatan folder, pemindahan, trash, restore, dan
  download streaming dengan HTTP Range.
- Menambahkan metadata index SQLite yang dapat dibangun ulang, dengan reindex
  eksplisit yang aman terhadap pergantian generation dan pemeliharaan
  inkremental untuk mutasi file.
- Menambahkan resumable upload berbasis fixed chunk yang persisten, dengan
  SHA-256 per chunk, verifikasi keseluruhan file secara opsional, recovery
  setelah restart, concurrency terbatas, dan publikasi atomik tanpa overwrite.
- Menambahkan audit event yang bersifat append-only dan dapat dibaca owner untuk
  aktivitas autentikasi, session, file, dan upload.
- Menambahkan command lokal `swadrive-admin` untuk bootstrap owner awal, reindex
  metadata, dan reconciliation terbatas terhadap orphan upload part dengan
  dry-run sebagai default.

### Keamanan

- Menambahkan hash password Argon2id dan hanya menyimpan digest SHA-256 dari
  opaque session token acak kriptografis 256-bit.
- Menambahkan resource gate terbatas untuk autentikasi dan transfer, pembatasan
  penyalahgunaan login, penanganan security header yang ketat, serta log
  operasional yang telah disamarkan.
- Menambahkan otorisasi owner dan containment logical path yang menolak absolute
  path, traversal, encoded traversal, null byte, dan symlink escape.
- Menambahkan process lock kanonis untuk database, validasi identitas volume
  storage, verifikasi satu filesystem, reconciliation terarah saat startup, dan
  penanganan konsistensi metadata secara fail-closed.
- Memisahkan keterjangkauan jaringan Tailscale, identitas aplikasi, akses
  administratif ke Linux, dan runtime service account terbatas.

### Infrastruktur

- Men-deploy backend `v1.0.0` dari commit rilis yang telah ditetapkan ke server
  production berbasis Debian pada 2026-08-26 sebagai service yang dikelola
  `systemd` dan berjalan menggunakan `personalcloud_service`.
- Membatasi akses backend ke jaringan privat Tailscale pada TCP `8080`, tanpa
  mengekspos port aplikasi ke publik.
- Menetapkan control/metadata plane pada NVMe untuk SQLite dan content plane
  pada HDD dengan root `/srv/personalcloud` untuk file, upload, dan trash.
- Mengonfigurasi SwaDrive agar fail-closed ketika mount storage di production
  tidak tersedia, sekaligus tetap memungkinkan Debian melakukan boot tanpa HDD.

### Keterbatasan yang Diketahui

- Proyek Flutter untuk Android dan Linux masih berupa scaffold minimal tanpa UX
  login, file browser, atau transfer.
- API file, upload, dan audit pada `v1.0.0` hanya tersedia untuk owner dan
  mendukung satu proses backend per pasangan database dan storage root.
- Encryption at rest pada level aplikasi, berbagi file secara publik, content
  indexing, thumbnail, OCR, serta mekanisme retention otomatis untuk audit dan
  riwayat belum tersedia.
