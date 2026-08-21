# Private Storage Explorer

> Dokumen spesifikasi proyek, arsitektur, tech stack, keamanan, API, penyimpanan, checklist MVP, dan roadmap versi penuh.

| Informasi | Nilai |
| --- | --- |
| Nama kerja | **Private Storage Explorer** |
| Status dokumen | Draft arsitektur v1 |
| Tanggal | 21 Agustus 2026 |
| Pemilik sistem | Doni |
| Server authority | Laptop Debian |
| Client | Arch Linux dan Android |
| Cakupan akun | Satu akun user, banyak perangkat/sesi |
| Akses jaringan | Tailscale privat; Ethernet sebagai jalur fisik lokal |
| Domain berbayar | Tidak diperlukan |

---

## 1. Ringkasan proyek

Private Storage Explorer adalah sistem penyimpanan pribadi yang mengubah laptop lama dengan Debian menjadi server file privat. File dapat dilihat, dicari, diunggah, diunduh, dipindahkan, diubah namanya, dimasukkan ke trash, dan dipulihkan melalui aplikasi Flutter di Arch Linux maupun Android.

Debian adalah satu-satunya **authority**. Arch dan HP tidak memiliki hak administrasi server; keduanya merupakan perangkat dari akun user biasa yang sama. Administrasi dilakukan langsung dari Debian melalui CLI lokal, bukan melalui halaman atau endpoint admin di jaringan.

Sistem tidak memerlukan domain berbayar, IP publik statis, cloud hosting, atau port forwarding. API hanya tersedia di jaringan privat Tailscale melalui alamat HTTPS `*.ts.net`. Ketika Arch dan Debian berada di rumah, koneksi Tailscale diharapkan berjalan peer-to-peer secara langsung melalui Ethernet.

File berukuran sangat besar, termasuk target 100 GB, ditransfer secara streaming dengan protokol resumable upload. Transfer dapat dilanjutkan dari offset terakhir setelah jaringan terputus atau aplikasi dibuka ulang.

---

## 2. Keputusan arsitektur yang sudah dikunci

| Topik | Keputusan |
| --- | --- |
| Pusat kontrol | Debian mengontrol konfigurasi, user, sesi, versi, storage, dan kebijakan |
| Hak Arch dan HP | Sama-sama client user biasa |
| Jumlah akun aplikasi | Satu akun user |
| Sesi perangkat | Terpisah untuk Arch, HP, dan perangkat lain |
| Admin jaringan | Tidak ada |
| Admin lokal | CLI `storagectl` hanya di Debian |
| Protokol aplikasi | HTTPS REST API |
| Protokol upload | TUS resumable upload |
| Protokol download | HTTP streaming dan Range |
| Database server | SQLite |
| Penyimpanan file | File biasa pada HDD, bukan BLOB database |
| Backend | Go |
| Client | Flutter Android dan Linux |
| Jaringan privat | Tailscale |
| HTTPS privat | Tailscale Serve |
| SFTP | Bukan protokol aplikasi; hanya opsional untuk maintenance lokal |
| Public exposure | Tidak ada; Tailscale Funnel tidak digunakan |
| Domain | Menggunakan hostname gratis `*.ts.net`, bukan domain berbayar |
| Container | Tidak menggunakan Docker untuk MVP |
| Database eksternal | Tidak menggunakan PostgreSQL/MySQL untuk MVP |

---

## 3. Kondisi infrastruktur saat ini

### 3.1 Laptop Debian

- Menjadi server dan authority.
- NVMe sekitar 256 GB digunakan untuk OS, aplikasi server, konfigurasi, log, dan database.
- HDD sekitar 500 GB digunakan untuk data pengguna.
- HDD saat ini masih menggunakan NTFS.
- Data HDD sekitar 201 GiB dengan kurang lebih 130.734 file telah disalin ke Arch.
- Dry-run `rsync` tidak menemukan transfer tertinggal, tetapi verifikasi checksum akhir tetap wajib sebelum perubahan filesystem.

### 3.2 Arch Linux

- Menjadi client user biasa.
- Digunakan untuk pengembangan Flutter.
- Terhubung ke Debian melalui Ethernet.
- Baseline transfer sebelumnya sekitar 78,84 MB/s.
- Akan menjalankan aplikasi Flutter Linux dengan hak sama seperti Android.

### 3.3 Android

- Menjadi client user biasa.
- Mengakses server melalui Tailscale ketika berada di luar rumah.
- Menggunakan akun yang sama dengan Arch tetapi mendapat session token dan device ID berbeda.

### 3.4 Prasyarat keselamatan data

Sebelum HDD Debian diformat ulang:

- [ ] Verifikasi checksum data sumber dan salinan di Arch.
- [ ] Buka sampel file penting dan file besar secara manual.
- [ ] Simpan salinan Arch sampai server baru stabil dan lolos pengujian.
- [ ] Pastikan tidak ada file unik yang hanya tersisa di HDD Debian.
- [ ] Catat UUID partisi dan struktur disk.
- [ ] Baru setelah semua verifikasi berhasil, pertimbangkan migrasi HDD dari NTFS ke ext4.

> **Peringatan:** format filesystem menghapus data. Jangan menjalankan `mkfs` sebelum semua pemeriksaan selesai.

---

## 4. Tujuan proyek

### 4.1 Tujuan utama

- Menyediakan file explorer pribadi untuk Android dan Linux.
- Mengakses file dengan aman dari rumah maupun luar rumah.
- Tidak membayar domain atau server cloud.
- Menjadikan Debian satu-satunya authority.
- Mendukung satu akun user pada banyak perangkat.
- Mendukung upload dan download file sangat besar.
- Mendukung resume setelah koneksi terputus.
- Menyediakan pemeriksaan kompatibilitas versi client dan server.
- Menyediakan pencarian nama file dari server.
- Menyediakan trash agar penghapusan tidak langsung permanen.
- Menyimpan metadata aplikasi di Debian.
- Menjaga penggunaan RAM tidak bertambah mengikuti ukuran file.

### 4.2 Sasaran nonfungsional

- Aman secara default.
- Tetap sederhana untuk dipelihara sendiri.
- Tidak bergantung pada domain berbayar.
- Tidak membuka port publik.
- Dapat berjalan otomatis setelah Debian reboot.
- Kerusakan satu transfer tidak memengaruhi file lain.
- File parsial tidak pernah tampil sebagai file final.
- Semua operasi sensitif tercatat tanpa mencatat password atau token.
- Backend tetap ringan untuk laptop lama.

### 4.3 Bukan tujuan MVP

- Kolaborasi banyak user.
- Berbagi link ke internet publik.
- Flutter Web.
- Sinkronisasi dua arah seperti Dropbox.
- Edit dokumen kolaboratif.
- Streaming/transcoding video server-side.
- Enkripsi file end-to-end di sisi client.
- Deduplication tingkat blok.
- High availability atau cluster.
- Object storage S3.

---

## 5. Aktor dan trust model

### 5.1 Debian sebagai authority

Debian bukan akun aplikasi. Debian adalah batas kepercayaan tertinggi dan mengontrol:

- Service backend.
- Database.
- Folder storage.
- User aplikasi.
- Password reset.
- Device dan session revocation.
- Versi minimum aplikasi.
- Mode maintenance.
- Quota dan batas ukuran file.
- Indeks pencarian.
- Backup, audit, dan pemeriksaan kesehatan HDD.

Administrasi hanya melalui terminal lokal Debian dan CLI `storagectl`.

### 5.2 User aplikasi

Hanya ada satu akun user, misalnya `doni`. User ini dapat:

- Login dari Arch dan Android.
- Melihat file dan folder.
- Upload dan download.
- Membuat folder.
- Rename dan pindah file.
- Mencari nama file.
- Memindahkan file ke trash.
- Memulihkan file dari trash.
- Melihat dan mencabut sesi perangkatnya sendiri.

User tidak dapat:

- Mengubah konfigurasi server.
- Mengubah versi minimum aplikasi.
- Membuka path di luar storage root.
- Melihat database.
- Menjalankan command OS.
- Mengakses terminal Debian.
- Menonaktifkan audit atau health check.

### 5.3 Akun service internal

Service Go dijalankan oleh akun Linux internal `storage-api`:

- Tidak memiliki password.
- Tidak memiliki shell login.
- Tidak memiliki `sudo`.
- Hanya memiliki akses baca/tulis ke path yang diperlukan.
- Bukan akun manusia dan bukan akun aplikasi.

### 5.4 Perangkat dan sesi

Satu akun dapat mempunyai beberapa perangkat:

~~~text
User doni
├── Arch Linux   → device-id-arch   → session-arch
├── HP Android   → device-id-phone  → session-phone
└── Laptop lain  → device-id-laptop → session-laptop
~~~

Jika HP hilang, Debian atau user dapat mencabut hanya session HP tanpa mengeluarkan Arch.

---

## 6. Arsitektur sistem

~~~mermaid
flowchart TD
    A["Arch Flutter (user)"] -->|Tailscale direct via Ethernet| C["Tailscale Serve HTTPS"]
    B["Android Flutter (user)"] -->|Tailscale via Internet| C
    C --> D["Go Storage API (127.0.0.1:8080)"]
    E["Debian local authority"] -->|storagectl + config| D
    D --> F["SQLite pada NVMe"]
    D --> G["File pada HDD ext4"]
~~~

### 6.1 Jalur aplikasi

~~~text
Flutter
  → Tailscale
  → HTTPS *.ts.net
  → Tailscale Serve
  → Go API localhost
  → SQLite dan HDD
~~~

### 6.2 Jalur administrasi

~~~text
Terminal lokal Debian
  → sudo storagectl
  → konfigurasi/database/service
~~~

Tidak ada admin panel atau admin API yang dapat diakses Arch/HP.

### 6.3 Kenapa API tidak membutuhkan domain berbayar

API cukup berjalan pada IP dan port. SQLite hanyalah file lokal dan tidak berhubungan dengan DNS. Tailscale menyediakan hostname privat:

~~~text
https://storage-debian.<tailnet>.ts.net
~~~

Tailscale Serve menyediakan HTTPS dan sertifikat untuk hostname tersebut. Hanya perangkat di tailnet yang dapat mengaksesnya. Jangan mengaktifkan Tailscale Funnel karena Funnel membuat service dapat diakses dari internet publik.

---

## 7. Desain jaringan

### 7.1 Endpoint tunggal aplikasi

Semua client menggunakan satu base URL:

~~~text
https://storage-debian.<tailnet>.ts.net/api/v1
~~~

Keuntungan:

- Tidak perlu mengganti URL ketika berpindah dari rumah ke luar rumah.
- Tidak perlu menyimpan IP publik.
- Tidak perlu DDNS.
- Tidak perlu port forwarding.
- Sertifikat HTTPS dikelola otomatis.

### 7.2 Arch di rumah

- Arch dan Debian tetap terhubung secara fisik melalui Ethernet.
- Tailscale diharapkan membentuk jalur `direct` menggunakan koneksi lokal.
- Verifikasi dengan `tailscale status` dan `tailscale ping storage-debian`.
- Jika hasilnya `relay`, performa harus diperbaiki sebelum uji file 100 GB.

### 7.3 Android di luar rumah

- Android menjalankan client Tailscale.
- Aplikasi mengakses hostname yang sama.
- Kecepatan upload/download dibatasi koneksi HP, koneksi rumah, kondisi NAT, CPU server, dan HDD.
- Jika koneksi tidak direct, Tailscale dapat menggunakan relay sehingga throughput mungkin lebih rendah.

### 7.4 Firewall

- Go API hanya bind ke `127.0.0.1`.
- HTTPS masuk melalui Tailscale Serve.
- Tidak ada port API yang dibuka ke Wi-Fi publik.
- SSH, jika tetap dipasang, dibatasi untuk maintenance dan tidak digunakan aplikasi.
- Tailscale ACL/grants hanya mengizinkan perangkat milik user mengakses service.

---

## 8. Alur startup, handshake, dan login

~~~mermaid
sequenceDiagram
    participant App as Flutter
    participant API as Debian API
    participant DB as SQLite

    App->>API: GET /health
    API-->>App: server/storage status
    App->>API: POST /handshake
    API-->>App: compatibility result
    App->>API: POST /auth/login
    API->>DB: verify Argon2id hash
    DB-->>API: user valid
    API-->>App: access + refresh token
~~~

### 8.1 State startup client

1. `checkingNetwork`
2. `serverUnavailable` atau `checkingCompatibility`
3. `updateRequired`, `maintenance`, atau `readyForLogin`
4. `authenticating`
5. `authenticated`
6. `loadingFiles`

### 8.2 Health check

Endpoint:

~~~http
GET /api/v1/health
~~~

Respons minimal:

~~~json
{
  "status": "ok",
  "server_id": "storage-debian-01",
  "storage_ready": true,
  "storage_writable": true,
  "maintenance": false
}
~~~

Health check wajib memverifikasi bahwa HDD benar-benar ter-mount. Backend tidak boleh menerima upload apabila `/srv/storage` hanya menjadi folder kosong pada filesystem NVMe akibat HDD gagal mount.

### 8.3 Handshake versi

Request:

~~~json
{
  "client_version": "0.2.0",
  "client_build": 12,
  "api_version": 1,
  "platform": "android",
  "device_id": "uuid-random"
}
~~~

Response:

~~~json
{
  "server_id": "storage-debian-01",
  "server_version": "0.3.0",
  "api_version": 1,
  "minimum_client_version": "0.2.0",
  "latest_client_version": "0.3.0",
  "result": "update_available"
}
~~~

Nilai `result`:

- `compatible`
- `update_available`
- `update_required`
- `incompatible_api`
- `maintenance`

Versi client dan server tidak harus sama. Yang menentukan adalah kompatibilitas API dan `minimum_client_version`.

### 8.4 Login dan sesi

- User memasukkan username/password.
- Password hanya dikirim melalui HTTPS.
- Server menyimpan hash Argon2id, bukan password asli.
- Login menghasilkan access token pendek dan refresh token.
- Token disimpan pada secure storage client.
- Token server disimpan dalam bentuk hash agar kebocoran database tidak langsung menghasilkan token siap pakai.
- Setiap device memiliki sesi sendiri.

Default awal:

| Pengaturan | Nilai awal |
| --- | --- |
| Access token | 15 menit |
| Refresh token | 30 hari |
| Inaktivitas maksimum | Dapat dikonfigurasi |
| Percobaan login | Rate-limited |
| Password minimum | 12 karakter |

---

## 9. Penyimpanan server

### 9.1 Pembagian disk

| Data | Lokasi | Alasan |
| --- | --- | --- |
| Debian dan binary | NVMe | Boot dan service cepat |
| Konfigurasi | NVMe | Tidak bergantung mount HDD |
| SQLite | NVMe | Latensi rendah dan izin POSIX |
| Log | journald/NVMe | Tetap ada jika HDD bermasalah |
| File user | HDD | Kapasitas utama |
| Upload parsial | HDD | Tidak menggandakan data ke NVMe |
| Trash | HDD | Rename dalam filesystem yang sama |

### 9.2 Layout filesystem

~~~text
/etc/storage-api/
├── config.toml
└── secrets.env

/var/lib/storage-api/
├── app.db
├── app.db-wal
├── app.db-shm
└── migrations/

/srv/storage/
├── .storage-marker
├── files/
├── .uploads/
├── .trash/
└── .lost-found-metadata/
~~~

### 9.3 Marker mount

File `.storage-marker` berisi server ID unik. Pada startup, API memastikan:

- `/srv/storage` merupakan mount point yang benar.
- Marker ada dan cocok.
- Filesystem dapat dibaca.
- Filesystem dapat ditulis.
- Ruang bebas memenuhi batas minimum.

Jika salah satu gagal, status menjadi `storage_unavailable` dan semua operasi mutasi ditolak.

### 9.4 Filesystem HDD

Target akhir adalah ext4 karena:

- Permission dan ownership Linux konsisten.
- Atomic rename bekerja baik.
- Tidak membutuhkan mapping permission NTFS.
- Lebih cocok untuk service Linux jangka panjang.

Migrasi hanya dilakukan setelah checksum backup selesai.

---

## 10. Tech stack final

### 10.1 Server

| Komponen | Pilihan | Peran |
| --- | --- | --- |
| OS | Debian | Server authority |
| Bahasa backend | Go stable | API ringan dan single binary |
| HTTP server | `net/http` | REST API dan streaming |
| Logging | `log/slog` | Structured logging |
| Database | SQLite | User, device, session, settings, upload, index |
| DB access | `database/sql` | Query dan transaksi |
| Password hashing | Argon2id | Penyimpanan password aman |
| Token | Random opaque 256-bit | Sesi yang mudah dicabut |
| Resumable upload | `tusd` v2 embedded | Implementasi referensi TUS |
| Download | HTTP Range | Resume download |
| HTTPS privat | Tailscale Serve | TLS dan reverse proxy privat |
| Service manager | systemd | Autostart, restart, hardening |
| Firewall | nftables | Membatasi permukaan jaringan |
| Disk monitoring | smartmontools | SMART HDD/NVMe |
| Backup | rsync + checksum | Salinan data dan verifikasi |
| Filesystem HDD | ext4 | Storage Linux |

### 10.2 Flutter client

| Komponen | Pilihan | Peran |
| --- | --- | --- |
| Framework | Flutter stable | Android dan Linux satu codebase |
| Bahasa | Dart | Client logic |
| State management | Riverpod | Auth, browser, transfer, settings |
| HTTP client | Dio | Streaming, timeout, cancel, interceptor |
| TUS client | Adapter internal di atas Dio | Kontrol resume tanpa bergantung paket lemah |
| Secret storage | flutter_secure_storage | Access/refresh token |
| DB lokal | Drift/SQLite | Antrean transfer dan checkpoint |
| Pilih file | file_picker atau file selector | Memilih sumber upload |
| Direktori aplikasi | path_provider | Cache dan file parsial download |
| Informasi versi | package_info_plus | Handshake versi |
| UUID device | UUID v4 acak | Identitas instalasi |
| Checksum | crypto/streaming hash | Integritas opsional |
| Testing | flutter_test + integration_test | Unit, widget, dan end-to-end |

### 10.3 Development dan quality

| Komponen | Pilihan |
| --- | --- |
| Version control | Git |
| Repository | Monorepo |
| Format Go | `gofmt` |
| Static analysis Go | `go vet`; golangci-lint opsional |
| Format Dart | `dart format` |
| Static analysis Dart | `flutter analyze` |
| API test | Go `httptest` |
| Contract documentation | OpenAPI 3 |
| CI lokal awal | Script Makefile/justfile |

### 10.4 Yang tidak digunakan pada MVP

- Domain berbayar.
- Cloud VPS.
- Port forwarding router.
- Tailscale Funnel.
- Nginx, Apache, atau Caddy.
- Docker.
- Kubernetes.
- PostgreSQL/MySQL.
- SFTP sebagai API aplikasi.
- Firebase/Supabase.
- Flutter Web.

---

## 11. Modul backend

~~~text
storage-api
├── bootstrap
├── config
├── health
├── handshake
├── auth
├── devices
├── sessions
├── storage
├── files
├── uploads
├── downloads
├── search
├── trash
├── indexer
├── audit
└── admincli
~~~

### 11.1 `health`

- Memeriksa API hidup.
- Memeriksa mount marker.
- Memeriksa read/write storage.
- Memeriksa ruang bebas.
- Menampilkan mode maintenance.

### 11.2 `handshake`

- Membaca versi client.
- Membandingkan API version.
- Memutuskan compatible/update required.
- Mencatat versi perangkat terakhir.

### 11.3 `auth`

- Login.
- Refresh session.
- Logout.
- Hash password.
- Rate limit dan delay login gagal.

### 11.4 `devices` dan `sessions`

- Register device setelah login.
- Daftar sesi aktif.
- Revoke satu perangkat.
- Mencatat last seen tanpa menyimpan data perangkat sensitif.

### 11.5 `storage` dan `files`

- Normalisasi path.
- List folder.
- Stat file.
- Mkdir.
- Rename dan move.
- Trash/restore.
- Menolak akses di luar root.

### 11.6 `uploads`

- Membungkus handler `tusd`.
- Memvalidasi token dan tujuan upload.
- Memeriksa kapasitas.
- Menyimpan status upload.
- Atomic finalize.
- Membersihkan upload kedaluwarsa.

### 11.7 `downloads`

- Streaming file.
- Mendukung Range.
- Mendukung cancel.
- Memberi header ukuran, ETag, dan waktu modifikasi.

### 11.8 `search`

- Mencari berdasarkan nama.
- Mengembalikan hasil terbatas dan terurut.
- Menjaga indeks agar sesuai filesystem.

### 11.9 `admincli`

- Hanya tersedia lokal.
- Menggunakan permission OS.
- Tidak membuka admin HTTP endpoint.

---

## 12. Skema database

### 12.1 `users`

| Kolom | Fungsi |
| --- | --- |
| `id` | UUID user |
| `username` | Nama login unik |
| `password_hash` | Encoded Argon2id hash |
| `enabled` | Status akun |
| `created_at` | Waktu dibuat |
| `password_changed_at` | Rotasi password |

Hanya ada satu row user pada desain awal.

### 12.2 `devices`

| Kolom | Fungsi |
| --- | --- |
| `id` | UUID instalasi |
| `user_id` | Pemilik device |
| `display_name` | Contoh `Arch Laptop`, `HP Android` |
| `platform` | android/linux |
| `app_version` | Versi terakhir |
| `first_seen_at` | Pertama login |
| `last_seen_at` | Aktivitas terakhir |
| `revoked_at` | Null jika aktif |

### 12.3 `sessions`

| Kolom | Fungsi |
| --- | --- |
| `id` | UUID sesi |
| `user_id` | User |
| `device_id` | Device |
| `access_token_hash` | Hash access token |
| `refresh_token_hash` | Hash refresh token |
| `access_expires_at` | Kedaluwarsa access |
| `refresh_expires_at` | Kedaluwarsa refresh |
| `created_at` | Waktu dibuat |
| `last_used_at` | Aktivitas terakhir |
| `revoked_at` | Status revoke |

### 12.4 `server_settings`

Contoh key:

- `server_id`
- `server_version`
- `api_version`
- `minimum_client_version`
- `latest_client_version`
- `maintenance_enabled`
- `maintenance_message`
- `trash_retention_days`
- `max_upload_bytes`
- `minimum_free_bytes`

### 12.5 `uploads`

| Kolom | Fungsi |
| --- | --- |
| `id` | Upload ID/TUS ID |
| `user_id` | Pemilik |
| `device_id` | Pengirim |
| `destination_path` | Path final relatif |
| `temporary_path` | Path parsial |
| `total_bytes` | Ukuran file |
| `uploaded_bytes` | Offset terakhir |
| `source_fingerprint` | Identitas file lokal |
| `checksum_algorithm` | Opsional |
| `checksum_value` | Opsional |
| `state` | queued/uploading/verifying/completed/failed/canceled |
| `created_at` | Dibuat |
| `updated_at` | Diperbarui |
| `expires_at` | Cleanup parsial |

### 12.6 `file_index`

| Kolom | Fungsi |
| --- | --- |
| `path` | Path relatif unik |
| `parent_path` | Folder induk |
| `name` | Nama asli |
| `name_folded` | Nama untuk pencarian case-insensitive |
| `type` | file/folder |
| `size_bytes` | Ukuran |
| `modified_at` | Waktu modifikasi |
| `indexed_at` | Waktu scan |

### 12.7 `audit_events` — versi penuh

- Login berhasil/gagal.
- Device register/revoke.
- Upload complete/fail.
- Download dimulai.
- Rename/move.
- Trash/restore/purge.
- Perubahan setting oleh CLI lokal.

Audit tidak boleh menyimpan password, access token, refresh token, atau isi file.

---

## 13. Kontrak REST API v1

Semua respons error menggunakan format:

~~~json
{
  "error": {
    "code": "STORAGE_UNAVAILABLE",
    "message": "Storage sementara tidak tersedia.",
    "request_id": "uuid"
  }
}
~~~

### 13.1 Endpoint publik dalam tailnet

| Method | Endpoint | Fungsi |
| --- | --- | --- |
| GET | `/api/v1/health` | Health server dan storage |
| POST | `/api/v1/handshake` | Kompatibilitas client/server |
| POST | `/api/v1/auth/login` | Login |
| POST | `/api/v1/auth/refresh` | Refresh token |

### 13.2 Endpoint terautentikasi

| Method | Endpoint | Fungsi |
| --- | --- | --- |
| POST | `/api/v1/auth/logout` | Revoke sesi saat ini |
| GET | `/api/v1/devices` | Daftar perangkat |
| DELETE | `/api/v1/devices/{id}` | Revoke perangkat |
| GET | `/api/v1/files?path=` | List folder |
| GET | `/api/v1/files/stat?path=` | Metadata file |
| POST | `/api/v1/folders` | Membuat folder |
| PATCH | `/api/v1/files/rename` | Rename/move |
| DELETE | `/api/v1/files?path=` | Pindah ke trash |
| GET | `/api/v1/trash` | Daftar trash |
| POST | `/api/v1/trash/restore` | Restore |
| GET | `/api/v1/search?q=` | Cari nama file |
| GET | `/api/v1/download?path=` | Download/Range |
| POST/HEAD/PATCH | `/api/v1/uploads/*` | TUS resumable upload |

### 13.3 Status code penting

| Code | Arti |
| --- | --- |
| 200/201/204 | Berhasil |
| 400 | Input tidak valid |
| 401 | Belum login/token tidak valid |
| 403 | Tidak memiliki akses |
| 404 | File/resource tidak ada |
| 409 | Konflik nama/offset |
| 412 | Versi protokol tidak didukung |
| 413 | File terlalu besar |
| 416 | Range tidak valid |
| 423 | Resource terkunci |
| 429 | Terlalu banyak request/login |
| 503 | Maintenance/storage unavailable |

### 13.4 Aturan path

- Client hanya mengirim path relatif.
- Path absolut ditolak.
- Segmen `..` ditolak.
- Null byte ditolak.
- Symlink tidak diikuti pada MVP.
- Path dibersihkan dan diverifikasi tetap berada di storage root.
- Nama file harus valid UTF-8.
- Nama internal `.uploads`, `.trash`, dan `.storage-marker` tidak boleh diakses langsung.

---

## 14. Semantik operasi file

### 14.1 List

- Mengembalikan nama, tipe, ukuran, mtime, dan permission aplikasi.
- Folder besar dikembalikan secara paginated/cursor.
- Sorting dilakukan server atau client berdasarkan nama, ukuran, tanggal, dan tipe.

### 14.2 Rename/move

- Menggunakan atomic rename jika masih dalam filesystem yang sama.
- Konflik nama menghasilkan `409`.
- Client menawarkan `skip`, `overwrite`, atau `rename automatically`.

### 14.3 Delete

Delete default bukan penghapusan permanen:

~~~text
/srv/storage/files/foto.jpg
→
/srv/storage/.trash/<trash-id>/foto.jpg
~~~

Metadata trash menyimpan path asli dan tanggal hapus.

### 14.4 Restore

- Mengembalikan ke path asli jika kosong.
- Jika ada konflik, client memilih nama baru atau overwrite.

### 14.5 Permanent purge

- Hanya dilakukan setelah retention period.
- Default yang disarankan: 30 hari.
- Dapat dijalankan systemd timer.
- CLI lokal Debian dapat purge manual.

---

## 15. Transfer file besar

### 15.1 Prinsip

- File tidak dibaca seluruhnya ke RAM.
- Transfer selalu streaming.
- Upload parsial disimpan di `.uploads`.
- Download parsial disimpan dengan suffix `.downloading`.
- Progres disimpan pada DB client dan server.
- File final hanya muncul setelah finalize berhasil.

### 15.2 Upload

Backend menyematkan `tusd` v2 sebagai handler resmi TUS. Alur:

1. Client memeriksa kapasitas server.
2. Client membuat upload resource.
3. Server mengembalikan upload URL/ID.
4. Client meminta offset saat ini dengan `HEAD`.
5. Client mengirim byte berikutnya dengan `PATCH`.
6. Server mengembalikan offset baru.
7. Jika putus, client mengulang `HEAD` dan melanjutkan.
8. Setelah lengkap, server memvalidasi ukuran.
9. Server memindahkan file parsial secara atomic ke path final.

### 15.3 Ukuran chunk

- Checkpoint logis awal: 16 MiB.
- Buffer jaringan aktual lebih kecil dan tetap streaming.
- Upload awal sequential dengan concurrency 1.
- Concurrency 2 hanya diaktifkan setelah benchmark membuktikan manfaat.
- Parallel multipart bukan default karena menambah kompleksitas lock, manifest, dan validasi.

Untuk file 100 GB, 16 MiB menghasilkan kira-kira 6.000 checkpoint. RAM tetap kecil karena chunk tidak perlu dimuat seluruhnya sekaligus.

### 15.4 Preflight

Sebelum upload:

- Pastikan HDD mounted dan writable.
- Pastikan ruang bebas lebih besar dari ukuran upload + safety margin.
- Pastikan nama tujuan valid.
- Pastikan file sumber belum berubah.
- Pastikan user/session aktif.
- Pastikan ukuran tidak melewati batas server.

Jika overwrite aman dilakukan dengan mempertahankan file lama sampai upload baru selesai, ruang yang diperlukan dapat mendekati dua kali ukuran file.

### 15.5 Resume setelah restart

Client Drift menyimpan:

- Upload ID.
- URI/path file lokal.
- Destination.
- Total bytes.
- Offset terakhir.
- Ukuran dan mtime sumber.
- Status.
- Retry count.

Saat aplikasi dibuka kembali:

1. Baca antrean yang belum selesai.
2. Pastikan file lokal masih sama.
3. Hubungi `HEAD` upload resource.
4. Cocokkan offset.
5. Lanjutkan dari offset server.

### 15.6 Integritas

MVP:

- TUS offset harus cocok.
- Final size harus cocok.
- Transport dilindungi HTTPS/Tailscale.
- Upload parsial tidak pernah dianggap final.

Versi penuh:

- Checksum per chunk bila extension tersedia.
- Background SHA-256/BLAKE3 untuk file penting.
- Status `verifying` sebelum file final dipublikasikan.
- Endpoint verifikasi manual.

### 15.7 Download

- API mendukung `Range`.
- File lokal ditulis ke `filename.downloading`.
- Ukuran file parsial menjadi offset resume.
- ETag/mtime diperiksa agar client tidak menyambung ke file server yang sudah berubah.
- Setelah lengkap, file di-rename ke nama final.

### 15.8 State transfer

~~~mermaid
stateDiagram-v2
    [*] --> Queued
    Queued --> Transferring
    Transferring --> Paused
    Paused --> Transferring
    Transferring --> Verifying
    Verifying --> Completed
    Transferring --> Failed
    Failed --> Queued
    Queued --> Canceled
~~~

### 15.9 Android

MVP dapat mensyaratkan aplikasi tetap foreground, tetapi resume setelah aplikasi dibuka ulang wajib bekerja.

Versi penuh:

- Foreground service dengan notifikasi progres.
- Persistable Storage Access Framework URI.
- Kebijakan hanya Wi-Fi/izinkan data seluler.
- Pause saat baterai rendah.
- Retry setelah pergantian jaringan.
- Tailscale tetap aktif.

---

## 16. Pencarian

### 16.1 MVP

- Pencarian berdasarkan nama file/folder.
- Server mencari `file_index`.
- Case-insensitive.
- Hasil dibatasi, misalnya 100 item per halaman.
- Hasil menampilkan path, tipe, ukuran, dan mtime.
- Initial index dibangun oleh `storagectl index rebuild`.
- Operasi API memperbarui indeks secara langsung.
- systemd timer melakukan reconciliation berkala.

### 16.2 Versi penuh

- FTS/fuzzy search.
- Filter tipe, ukuran, tanggal.
- Sort relevansi/nama/tanggal.
- Search suggestions.
- Recently accessed.
- Favorite/bookmark.
- Optional content indexing untuk tipe dokumen tertentu.

Content indexing tidak dijalankan otomatis pada semua file karena biaya CPU, privasi, dan kompleksitas.

---

## 17. CLI administrasi lokal

Nama tool: `storagectl`.

~~~bash
sudo storagectl status
sudo storagectl health
sudo storagectl user create doni
sudo storagectl user reset-password doni
sudo storagectl user disable doni
sudo storagectl devices list
sudo storagectl devices revoke <device-id>
sudo storagectl sessions revoke-all
sudo storagectl maintenance enable --message "Sedang diperbaiki"
sudo storagectl maintenance disable
sudo storagectl version show
sudo storagectl version set-minimum 0.2.0
sudo storagectl version set-latest 0.3.0
sudo storagectl index rebuild
sudo storagectl index verify
sudo storagectl uploads list
sudo storagectl uploads cleanup
sudo storagectl trash purge --older-than 30d
sudo storagectl db backup
sudo storagectl db integrity-check
~~~

Tool tidak memiliki listener jaringan.

---

## 18. Keamanan

### 18.1 Network

- API bind localhost.
- Hanya Tailscale Serve yang mengekspos HTTPS ke tailnet.
- Jangan gunakan Funnel.
- Jangan membuka port router.
- Tailscale ACL/grants menerapkan least privilege.
- Verifikasi koneksi direct untuk performa.

### 18.2 Password

- Argon2id dengan salt unik.
- Parameter minimal mengikuti rekomendasi OWASP, lalu benchmark pada laptop Debian.
- Password tidak pernah dicatat di log.
- Reset password mencabut seluruh refresh token.
- Login gagal diberi rate limit dan delay.

### 18.3 Session

- Token dibuat dari random cryptographic bytes.
- Token server disimpan sebagai hash.
- Access token singkat.
- Refresh token dirotasi.
- Reuse refresh token lama memicu revoke session.
- Token client disimpan di secure storage.

### 18.4 Filesystem

- Service bukan root.
- Root storage tidak dapat diubah client.
- Path traversal ditolak.
- Symlink ditolak pada MVP.
- Internal folders disembunyikan.
- Upload final menggunakan atomic rename.
- Permission default tidak world-readable.

### 18.5 API

- Validasi JSON dan ukuran body.
- Timeout header/read/write.
- Request ID.
- Tidak mengembalikan stack trace ke client.
- Query SQLite memakai parameter binding.
- Idempotency key untuk operasi yang dapat terulang.
- CORS tidak diperlukan untuk native client dan dimatikan.

### 18.6 systemd hardening

Target konfigurasi:

- `User=storage-api`
- `Group=storage-api`
- `NoNewPrivileges=true`
- `PrivateTmp=true`
- `ProtectSystem=strict`
- `ProtectHome=true`
- `ReadWritePaths=/var/lib/storage-api /srv/storage`
- `Restart=on-failure`
- Batas file descriptor yang sesuai untuk transfer.

### 18.7 Identitas aplikasi dan versi

- Version check hanya untuk kompatibilitas, bukan bukti keamanan.
- APK release harus ditandatangani dengan signing key yang sama.
- Jika APK didistribusikan server pada versi penuh, manifest update harus memuat hash/signature.
- Server TLS identity berasal dari hostname tailnet.

---

## 19. Konfigurasi server

Contoh `/etc/storage-api/config.toml`:

~~~toml
server_id = "storage-debian-01"
listen_address = "127.0.0.1:8080"
api_version = 1

database_path = "/var/lib/storage-api/app.db"
storage_root = "/srv/storage/files"
upload_root = "/srv/storage/.uploads"
trash_root = "/srv/storage/.trash"
storage_marker = "/srv/storage/.storage-marker"

minimum_free_bytes = 10737418240
max_upload_bytes = 2199023255552
trash_retention_days = 30
upload_expiration_hours = 168

access_token_minutes = 15
refresh_token_days = 30

minimum_client_version = "0.1.0"
latest_client_version = "0.1.0"
maintenance_enabled = false
~~~

Secret/pepper, jika digunakan, disimpan terpisah pada file root-readable dan tidak masuk Git.

---

## 20. Struktur repository

~~~text
private-storage-explorer/
├── README.md
├── LICENSE
├── Makefile
├── docs/
│   ├── architecture.md
│   ├── api.md
│   ├── security.md
│   └── operations.md
├── apps/
│   └── flutter_client/
│       ├── android/
│       ├── linux/
│       ├── lib/
│       │   ├── app/
│       │   ├── core/
│       │   │   ├── api/
│       │   │   ├── auth/
│       │   │   ├── database/
│       │   │   ├── errors/
│       │   │   └── security/
│       │   └── features/
│       │       ├── startup/
│       │       ├── login/
│       │       ├── browser/
│       │       ├── search/
│       │       ├── transfers/
│       │       ├── trash/
│       │       └── devices/
│       └── test/
├── services/
│   └── storage_api/
│       ├── cmd/
│       │   ├── storage-api/
│       │   └── storagectl/
│       ├── internal/
│       │   ├── auth/
│       │   ├── config/
│       │   ├── database/
│       │   ├── devices/
│       │   ├── files/
│       │   ├── health/
│       │   ├── search/
│       │   ├── sessions/
│       │   ├── trash/
│       │   ├── uploads/
│       │   └── version/
│       ├── migrations/
│       └── tests/
└── deploy/
    ├── systemd/
    ├── tailscale/
    ├── nftables/
    └── scripts/
~~~

---

## 21. Layar aplikasi

### 21.1 MVP

1. Splash/server check.
2. Server unavailable.
3. Update required/update available.
4. Login.
5. File browser.
6. Create folder.
7. Upload picker.
8. Transfer queue.
9. Search.
10. File detail/actions.
11. Trash dan restore.
12. Settings sederhana.
13. Device sessions.

### 21.2 Versi penuh

- Grid/list toggle.
- Thumbnail.
- Multi-select.
- Batch move/delete/download.
- Favorites.
- Recent files.
- Sorting/filter lengkap.
- Preview gambar/audio/video/dokumen.
- Background transfer settings.
- Storage usage visualization.
- Update center.
- Audit/activity sederhana.

---

## 22. Checklist MVP

### Phase 0 — Keselamatan data dan storage

- [ ] Selesaikan checksum source vs backup Arch.
- [ ] Verifikasi jumlah file dan ukuran.
- [ ] Buka sampel file acak.
- [ ] Periksa SMART HDD dan NVMe.
- [ ] Simpan hasil pemeriksaan.
- [ ] Tentukan apakah HDD benar-benar dedicated Debian.
- [ ] Setelah aman, format HDD ext4.
- [ ] Mount dengan UUID ke `/srv/storage`.
- [ ] Buat `.storage-marker`.
- [ ] Tambahkan entry `/etc/fstab`.
- [ ] Uji reboot dan mount otomatis.
- [ ] Pastikan kegagalan mount tidak membuat API menulis ke NVMe.

### Phase 1 — Fondasi repository

- [ ] Buat monorepo Git.
- [ ] Buat README awal.
- [ ] Buat struktur Flutter dan Go.
- [ ] Tetapkan format/lint.
- [ ] Tambahkan `.gitignore`.
- [ ] Buat configuration template.
- [ ] Buat migration framework SQLite.
- [ ] Dokumentasikan command development.

### Phase 2 — Backend dasar

- [ ] Buat `storage-api` Go.
- [ ] Bind hanya `127.0.0.1:8080`.
- [ ] Implementasi config loader.
- [ ] Implementasi structured logging.
- [ ] Implementasi request ID.
- [ ] Implementasi graceful shutdown.
- [ ] Implementasi `/health`.
- [ ] Implementasi mount marker check.
- [ ] Implementasi free-space check.
- [ ] Implementasi maintenance mode.
- [ ] Buat systemd service.
- [ ] Terapkan systemd hardening.

### Phase 3 — Database, user, device, auth

- [ ] Buat schema `users`.
- [ ] Buat schema `devices`.
- [ ] Buat schema `sessions`.
- [ ] Buat schema `server_settings`.
- [ ] Implementasi Argon2id.
- [ ] Buat `storagectl user create`.
- [ ] Buat `storagectl user reset-password`.
- [ ] Implementasi login.
- [ ] Implementasi access token.
- [ ] Implementasi rotating refresh token.
- [ ] Implementasi logout.
- [ ] Implementasi device registration.
- [ ] Implementasi device list/revoke.
- [ ] Implementasi rate limit login.
- [ ] Pastikan token/password tidak muncul di log.

### Phase 4 — Handshake dan versioning

- [ ] Implementasi `/handshake`.
- [ ] Baca versi client dan build number.
- [ ] Validasi API version.
- [ ] Implementasi `compatible`.
- [ ] Implementasi `update_available`.
- [ ] Implementasi `update_required`.
- [ ] Implementasi `incompatible_api`.
- [ ] Implementasi maintenance message.
- [ ] Buat CLI set minimum/latest version.

### Phase 5 — File API

- [ ] Implementasi safe relative path.
- [ ] Tolak path traversal.
- [ ] Tolak symlink.
- [ ] Implementasi list folder.
- [ ] Implementasi stat.
- [ ] Implementasi mkdir.
- [ ] Implementasi rename/move.
- [ ] Implementasi conflict policies.
- [ ] Implementasi move to trash.
- [ ] Implementasi trash list.
- [ ] Implementasi restore.
- [ ] Implementasi retention metadata.
- [ ] Pastikan internal folder tidak terlihat.

### Phase 6 — Search

- [ ] Buat schema `file_index`.
- [ ] Implementasi index initial scan.
- [ ] Implementasi update index setelah operasi API.
- [ ] Implementasi `/search`.
- [ ] Tambahkan pagination/limit.
- [ ] Buat `storagectl index rebuild`.
- [ ] Buat `storagectl index verify`.
- [ ] Buat reconciliation timer.

### Phase 7 — Upload resumable

- [ ] Embed `tusd` v2.
- [ ] Lindungi semua upload dengan auth middleware.
- [ ] Kaitkan upload ke device/session.
- [ ] Validasi tujuan file.
- [ ] Preflight free space.
- [ ] Simpan metadata upload.
- [ ] Implementasi resume via `HEAD`.
- [ ] Implementasi sequential streaming.
- [ ] Implementasi pause/cancel.
- [ ] Implementasi expiration cleanup.
- [ ] Implementasi atomic finalize.
- [ ] File parsial tidak tampil pada browser.
- [ ] Uji disconnect dan reconnect.
- [ ] Uji server restart.

### Phase 8 — Download resumable

- [ ] Implementasi streaming download.
- [ ] Implementasi HTTP Range.
- [ ] Implementasi ETag/mtime validation.
- [ ] Implementasi cancel.
- [ ] Implementasi resume lokal.
- [ ] Implementasi rename `.downloading` ke final.
- [ ] Uji server file berubah saat resume.

### Phase 9 — Flutter foundation

- [ ] Buat Flutter Android dan Linux.
- [ ] Tambahkan Riverpod.
- [ ] Tambahkan Dio.
- [ ] Tambahkan flutter_secure_storage.
- [ ] Tambahkan Drift.
- [ ] Tambahkan file picker/selector.
- [ ] Tambahkan path_provider.
- [ ] Tambahkan package_info_plus.
- [ ] Buat device UUID permanen.
- [ ] Buat typed API errors.
- [ ] Buat auth interceptor dan refresh lock.

### Phase 10 — Flutter startup dan auth

- [ ] Implementasi server health screen.
- [ ] Implementasi retry.
- [ ] Implementasi handshake.
- [ ] Implementasi update required.
- [ ] Implementasi maintenance.
- [ ] Implementasi login.
- [ ] Simpan token aman.
- [ ] Implementasi auto refresh.
- [ ] Implementasi logout.
- [ ] Implementasi device session list/revoke.

### Phase 11 — Flutter file explorer

- [ ] Implementasi folder navigation.
- [ ] Implementasi list/sort.
- [ ] Implementasi create folder.
- [ ] Implementasi rename/move.
- [ ] Implementasi delete to trash.
- [ ] Implementasi restore.
- [ ] Implementasi search.
- [ ] Implementasi error dan empty states.
- [ ] Implementasi pull-to-refresh/reload.

### Phase 12 — Flutter transfer manager

- [ ] Buat transfer queue Drift.
- [ ] Implementasi upload create/resume.
- [ ] Implementasi download resume.
- [ ] Tampilkan progress, speed, ETA.
- [ ] Implementasi pause.
- [ ] Implementasi retry.
- [ ] Implementasi cancel.
- [ ] Pulihkan antrean setelah app restart.
- [ ] Cek file sumber belum berubah.
- [ ] Cek storage lokal cukup.
- [ ] Batasi concurrency default 1.

### Phase 13 — Tailscale dan deployment

- [ ] Install Tailscale pada Debian.
- [ ] Install Tailscale pada Arch.
- [ ] Install Tailscale pada Android.
- [ ] Tetapkan hostname `storage-debian`.
- [ ] Aktifkan MagicDNS/HTTPS.
- [ ] Konfigurasi Tailscale Serve ke localhost API.
- [ ] Pastikan Funnel mati.
- [ ] Terapkan ACL/grants minimal.
- [ ] Uji endpoint dari Arch.
- [ ] Uji endpoint dari Android melalui data seluler.
- [ ] Pastikan Arch menunjukkan koneksi `direct`.
- [ ] Benchmark Ethernet melalui Tailscale.

### Phase 14 — QA dan hardening MVP

- [ ] Unit test backend minimal 80% pada modul kritis.
- [ ] Unit/widget test Flutter.
- [ ] Integration test login.
- [ ] Integration test file operations.
- [ ] Test path traversal.
- [ ] Test token expired/revoked.
- [ ] Test HDD tidak ter-mount.
- [ ] Test HDD penuh.
- [ ] Test rename conflict.
- [ ] Test app dimatikan saat transfer.
- [ ] Test jaringan putus.
- [ ] Test reboot Debian saat upload.
- [ ] Test 1 GB.
- [ ] Test 5 GB.
- [ ] Test 20 GB.
- [ ] Test 100 GB.
- [ ] Periksa penggunaan RAM.
- [ ] Periksa log tidak mengandung secret.
- [ ] Backup SQLite dan lakukan restore test.

### Phase 15 — Rilis MVP

- [ ] Tetapkan versi `0.1.0`.
- [ ] Build binary Go release.
- [ ] Install systemd unit.
- [ ] Jalankan migration.
- [ ] Buat akun user pertama.
- [ ] Build APK signed.
- [ ] Build Flutter Linux.
- [ ] Simpan signing key dengan aman.
- [ ] Dokumentasikan instalasi dan recovery.
- [ ] Tag Git release.
- [ ] Pertahankan backup lama sampai burn-in selesai.

---

## 23. Scope MVP yang dianggap selesai

MVP selesai jika:

- Debian boot dan service otomatis aktif.
- Storage mount terverifikasi sebelum API menerima write.
- Arch dan Android dapat mengakses satu URL privat.
- Tidak ada domain berbayar atau port publik.
- Satu akun dapat login di dua perangkat.
- Sesi Arch dan HP dapat dicabut terpisah.
- Client/server compatibility check berfungsi.
- User dapat list, mkdir, rename, move, search, trash, dan restore.
- Upload dan download streaming.
- Transfer dapat dilanjutkan setelah koneksi putus.
- Transfer dapat dipulihkan setelah aplikasi dibuka ulang.
- File parsial tidak terlihat sebagai final.
- Test 100 GB berhasil atau, jika kapasitas test tidak memungkinkan, test setara menunjukkan RAM konstan dan resume pada offset besar.
- Backup dan restore database telah diuji.

---

## 24. Roadmap versi penuh

### 24.1 Transfer dan background

- [ ] Android foreground service.
- [ ] Persistable SAF URI.
- [ ] Continue saat layar mati.
- [ ] Wi-Fi only toggle.
- [ ] Data-cellular warning.
- [ ] Adaptive chunk sizing.
- [ ] Optional concurrency 2.
- [ ] Per-chunk checksum.
- [ ] Full-file background checksum.
- [ ] Bandwidth limit.
- [ ] Transfer priority.
- [ ] Scheduled transfer.

### 24.2 File explorer

- [ ] Multi-select.
- [ ] Batch operations.
- [ ] Grid/list.
- [ ] Thumbnail cache.
- [ ] Image preview.
- [ ] Audio/video preview.
- [ ] PDF preview.
- [ ] Favorite.
- [ ] Recent files.
- [ ] Folder size calculation.
- [ ] Duplicate detector.
- [ ] Advanced sorting/filtering.

### 24.3 Search dan indeks

- [ ] SQLite FTS.
- [ ] Fuzzy search.
- [ ] Filter extension/MIME.
- [ ] Filter ukuran/tanggal.
- [ ] inotify incremental update.
- [ ] Index reconciliation otomatis.
- [ ] Optional content indexing.

### 24.4 Trash dan lifecycle

- [ ] UI retention settings.
- [ ] Restore conflict wizard.
- [ ] Per-file purge.
- [ ] Automatic cleanup report.
- [ ] Orphaned upload recovery.

### 24.5 Device dan security

- [ ] Biometric unlock token.
- [ ] New-device notification.
- [ ] Session history.
- [ ] Suspicious login throttling.
- [ ] Token reuse detection.
- [ ] App update manifest signature.
- [ ] Key rotation procedure.
- [ ] Security audit checklist.

### 24.6 Operasional

- [ ] Dashboard kesehatan lokal Debian.
- [ ] SMART alert.
- [ ] Temperature alert.
- [ ] Free-space alert.
- [ ] Backup status.
- [ ] Prometheus metrics opsional.
- [ ] Structured audit viewer.
- [ ] Database automated backup.
- [ ] Disaster recovery drill.

### 24.7 Update

- [ ] Signed server release bundle.
- [ ] Database migration preflight.
- [ ] Automatic health check after update.
- [ ] Rollback binary.
- [ ] Signed APK manifest.
- [ ] In-app update notification.
- [ ] Update required grace period.

### 24.8 Fitur masa depan opsional

- [ ] Akun user tambahan.
- [ ] Quota per user.
- [ ] Folder sharing privat.
- [ ] Read-only guest.
- [ ] Client Windows/macOS.
- [ ] Optional self-hosted Headscale.

Fitur opsional ini bukan bagian kebutuhan sekarang dan tidak boleh memperlambat MVP.

---

## 25. Pengujian

### 25.1 Backend unit

- Password hash/verify.
- Token issue/rotate/revoke.
- Version comparison.
- Path normalization.
- Storage marker.
- Free-space guard.
- Trash/restore.
- Upload state transition.
- Index update.
- Error mapping.

### 25.2 Client unit/widget

- Startup state.
- Login state.
- Token refresh locking.
- File browser reducer/provider.
- Transfer progress calculation.
- Retry/backoff.
- Version UI.
- Error/empty/loading UI.

### 25.3 Integration matrix

| Skenario | Arch | Android |
| --- | ---: | ---: |
| Login | Wajib | Wajib |
| List/search | Wajib | Wajib |
| Upload kecil | Wajib | Wajib |
| Download kecil | Wajib | Wajib |
| Resume disconnect | Wajib | Wajib |
| Resume app restart | Wajib | Wajib |
| Ethernet direct | Wajib | N/A |
| Data seluler | N/A | Wajib |
| 100 GB | Utama | Setelah foreground/resume stabil |

### 25.4 Fault injection

- Cabut Ethernet.
- Matikan Wi-Fi/data.
- Nonaktifkan Tailscale.
- Restart API.
- Reboot Debian.
- Unmount HDD.
- Isi HDD hingga batas minimum.
- Hapus file parsial.
- Ubah file sumber saat upload pause.
- Ubah file server saat download pause.
- Revoke session saat transfer.

### 25.5 Target performa awal

- RAM tidak bertambah sebanding ukuran file.
- API respons list folder normal terasa instan.
- Search 130 ribu entri tetap responsif.
- Local transfer mendekati kemampuan HDD/jaringan.
- Tidak ada relay Tailscale pada pengujian Ethernet.
- CPU dan suhu server tetap aman selama transfer panjang.
- Tidak ada file corrupt/parsial yang tampil sebagai final.

---

## 26. Operasional dan backup

### 26.1 systemd

Service:

- `storage-api.service`
- `tailscaled.service`

Timer:

- `storage-index-reconcile.timer`
- `storage-upload-cleanup.timer`
- `storage-trash-purge.timer`
- `storage-smart-check.timer`
- `storage-db-backup.timer`

### 26.2 Logging

- Gunakan journald.
- Log structured JSON/text.
- Sertakan request ID, device ID terpotong, operasi, status, durasi.
- Jangan log password/token.
- Terapkan retention agar NVMe tidak penuh.

### 26.3 Backup

Minimum:

- Backup database rutin.
- Backup file user ke media kedua.
- Simpan konfigurasi dan migration.
- Simpan APK signing key terpisah.
- Uji restore, bukan hanya membuat backup.

SQLite WAL tidak boleh dibackup dengan menyalin `app.db` sembarangan saat aktif. Gunakan SQLite backup API atau checkpoint/backup command yang benar.

### 26.4 SMART dan temperatur

- Periksa atribut SMART HDD.
- Periksa bad sector/pending sector.
- Pantau suhu saat transfer panjang.
- Pastikan laptop tidak suspend ketika lid tertutup.
- Atur boot kembali setelah listrik padam jika BIOS mendukung.
- Baterai laptop dapat menjadi UPS singkat, tetapi bukan pengganti backup.

---

## 27. Risiko dan mitigasi

| Risiko | Dampak | Mitigasi |
| --- | --- | --- |
| HDD rusak | Kehilangan data | SMART, backup kedua, restore test |
| HDD gagal mount | Data masuk NVMe | Mount marker dan write guard |
| HP hilang | Token dicuri | Per-device revoke, secure storage |
| Password bocor | Akun diambil | Argon2id, rate limit, rotasi |
| App mati saat upload | Transfer berhenti | TUS resume + Drift checkpoint |
| Internet lambat | Transfer sangat lama | Resume, ETA, Wi-Fi policy |
| Tailscale relay | Throughput rendah | Pastikan koneksi direct |
| Server mati saat finalize | File parsial/orphan | Atomic rename dan recovery scan |
| Path traversal | File OS terbaca | Relative path guard, no symlink |
| Database corrupt | Login/metadata gagal | WAL, backup, integrity check |
| Update tidak kompatibel | Client gagal masuk | API version + minimum client version |
| Storage penuh | Upload gagal | Preflight + safety margin |
| Source file berubah | Resume corrupt | Size/mtime/fingerprint validation |
| File overwrite besar | Butuh ruang ganda | Tampilkan estimasi ruang |
| Signing key hilang | APK tidak dapat update | Backup offline aman |

---

## 28. Milestone

| Milestone | Hasil |
| --- | --- |
| M0 — Data safe | Checksum dan storage siap |
| M1 — Server alive | Health API, mount guard, systemd |
| M2 — Identity | Login, device, session, handshake |
| M3 — File core | Browser, search, trash |
| M4 — Transfer | Upload/download resumable |
| M5 — Client | Flutter Arch dan Android |
| M6 — Private network | Tailscale Serve dan koneksi direct |
| M7 — Validation | Fault tests dan 100 GB |
| M8 — MVP release | Build signed dan dokumentasi |
| M9 — Full | Background, preview, advanced index, monitoring |

---

## 29. Definition of Done

Sebuah fitur dianggap selesai jika:

- Kode diformat dan lolos static analysis.
- Unit test lulus.
- Integration path utama lulus.
- Error state memiliki pesan yang jelas.
- Tidak membocorkan secret ke log.
- Dokumentasi diperbarui.
- Permission server sudah diuji.
- Restart/reconnect sudah diuji jika relevan.
- Tidak menambah akses admin jaringan.
- Tidak membuat file parsial terlihat sebagai final.

---

## 30. Default yang dapat diubah

| Variabel | Default |
| --- | --- |
| Project name | Private Storage Explorer |
| Debian hostname | `storage-debian` |
| App username | `doni` |
| API version | `1` |
| Local API listen | `127.0.0.1:8080` |
| Storage root | `/srv/storage/files` |
| Upload temp | `/srv/storage/.uploads` |
| Trash | `/srv/storage/.trash` |
| Trash retention | 30 hari |
| Upload expiration | 7 hari |
| Logical checkpoint | 16 MiB |
| Transfer concurrency | 1 |
| Access token | 15 menit |
| Refresh token | 30 hari |
| Minimum password | 12 karakter |

---

## 31. Urutan implementasi yang direkomendasikan

1. Jangan coding transfer sebelum data lama dan filesystem aman.
2. Siapkan ext4, mount guard, dan health endpoint.
3. Siapkan Tailscale dan pastikan koneksi Arch direct.
4. Buat auth, device session, dan handshake.
5. Buat file API tanpa upload besar terlebih dahulu.
6. Buat TUS upload dan Range download.
7. Buat Flutter startup/login.
8. Buat browser/search/trash.
9. Buat transfer queue dan resume.
10. Uji bertahap 1 GB → 5 GB → 20 GB → 100 GB.
11. Hardening, backup, restore, dan fault test.
12. Baru rilis MVP dan lanjutkan fitur full.

---

## 32. Referensi teknis

- Tailscale Serve: <https://tailscale.com/docs/features/tailscale-serve>
- Tailscale HTTPS certificates: <https://tailscale.com/docs/how-to/set-up-https-certificates>
- Tailscale connection types: <https://tailscale.com/docs/reference/connection-types>
- TUS resumable upload protocol: <https://tus.io/protocols/resumable-upload>
- tusd Go embedding: <https://tus.github.io/tusd/advanced-topics/usage-package/>
- OWASP Password Storage: <https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html>
- SQLite WAL: <https://www.sqlite.org/wal.html>
- Flutter: <https://docs.flutter.dev/>
- Riverpod: <https://pub.dev/packages/flutter_riverpod>
- Dio: <https://pub.dev/packages/dio>
- Drift: <https://pub.dev/packages/drift>
- Flutter Secure Storage: <https://pub.dev/packages/flutter_secure_storage>
- package_info_plus: <https://pub.dev/packages/package_info_plus>

---

## 33. Catatan keputusan lanjutan

Keputusan berikut dapat ditetapkan ketika implementasi dimulai, tanpa mengubah arsitektur utama:

- Nama final aplikasi dan package ID Android.
- Minimum Android SDK.
- Tampilan UI/tema.
- Apakah login memakai biometric unlock lokal.
- Nilai final retention trash.
- Batas maksimum file berdasarkan kapasitas HDD.
- Algoritma checksum penuh.
- Jadwal backup dan media backup kedua.
- Format distribusi Flutter Linux.

Prinsip yang tidak berubah: **Debian adalah authority; Arch dan HP adalah client user biasa dengan sesi terpisah; aplikasi menggunakan API privat melalui Tailscale; file besar menggunakan transfer resumable; tidak ada domain berbayar atau port publik.**
