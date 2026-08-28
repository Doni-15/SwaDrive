# Model Keamanan SwaDrive

Dokumen ini menjelaskan aset, trust boundary, identitas, kontrol keamanan, dan
keterbatasan yang berlaku pada baseline SwaDrive `v1.0.0` beserta hardening
source hingga `v1.0.2`. Keputusan historis yang mendasarinya tetap dicatat dalam
[ADR](adr/README.md). Jalur untuk melaporkan kerentanan dijelaskan terpisah
dalam [kebijakan keamanan](../SECURITY.md).

## Aset yang Dilindungi

- file pengguna dan metadata;
- hash password dan session state;
- raw bearer token untuk session pada client;
- release artifact dan konfigurasi deployment;
- akses administrator;
- control plane untuk jaringan privat.

## Trust Boundary

```text
input client
    -> batas jaringan privat
    -> parsing HTTP dan autentikasi
    -> otorisasi resource
    -> filesystem containment
    -> proses Linux terbatas
```

Setiap batas tetap diperlukan. Perangkat yang dapat mencapai server belum tentu
berhak membaca resource.

## Identitas dan Kewenangan

- akun administrator mengelola OS, deployment, konfigurasi, dan recovery lokal;
- identitas perangkat Tailscale hanya menentukan keterjangkauan jaringan;
- akun aplikasi SwaDrive menentukan identitas dan hak atas resource aplikasi;
- service account membatasi kewenangan proses backend pada host Linux.

Keterjangkauan melalui Tailscale tidak memberikan hak aplikasi. Demikian pula,
session aplikasi tidak memberikan akses OS atau kewenangan administrasi
tailnet.

## Autentikasi, Otorisasi, dan Session

Password akun aplikasi SwaDrive disimpan sebagai hash Argon2id. Login
menghasilkan opaque bearer token acak 256-bit; server hanya menyimpan digest
SHA-256 dari token tersebut. Setiap perangkat memiliki server-side session yang
terpisah, dengan masa berlaku absolut 30 hari dan dapat dicabut secara
independen.

Administrator lokal dapat menjalankan `set-owner-password` di bawah process
lock. Perintah ini mengganti hash password, mencabut seluruh session owner, dan
menulis audit event dalam satu transaction; kegagalan audit/commit membatalkan
seluruh perubahan.

Middleware autentikasi membentuk identitas untuk request yang dilindungi.
Keputusan otorisasi memakai identitas tersebut, bukan user ID dari request
body. File API pada `v1.0.0` hanya tersedia bagi role owner. Health endpoint
tidak memerlukan autentikasi aplikasi, tetapi tetap berada di balik batas
jaringan privat.

## Invariant Keamanan

1. File API MUST menerapkan autentikasi dan otorisasi pada level aplikasi
   sebelum menyajikan data pengguna.
2. Keputusan atas resource yang dilindungi memakai identitas dari middleware
   autentikasi, bukan user ID dari request body.
3. Logical path harus lolos parser dan seluruh operasi fisik tetap berada di
   bawah storage `os.Root` yang telah dikonfigurasi.
4. Proses backend tidak boleh menulis binary, service unit, konfigurasi
   administrator, atau home directory milik administrator.
5. Raw password, token, isi file, dan physical path internal di host tidak
   boleh dicatat dalam log.
6. Client tidak menerima credential OS atau credential administrasi jaringan
   privat.
7. Upload parsial tidak terlihat sebagai file selesai.
8. Listing normal, metadata, pencarian, listing trash, dan status upload tidak
   membaca HDD.
9. Hash password hanya tersedia dalam credential boundary milik modul
   autentikasi; model `User` dan `Identity` normal tidak dapat membawanya.
10. Marker volume merupakan pemeriksaan identitas, bukan bukti mount;
    production wajib memverifikasi mount dan ownership pada OS.
11. Flock hanya mengoordinasikan proses SwaDrive yang bekerja sama;
    mekanisme ini bukan isolasi terhadap writer berbahaya dengan UID yang sama.
12. Kegagalan membuka atau memverifikasi content storage hanya boleh
    menurunkan data plane; proses HTTP, autentikasi, dan session tidak boleh
    ikut gagal selama SQLite/control plane sehat.
13. Setiap operasi content wajib memakai root yang baru saja lolos validasi
    marker, directory wajib, dan persyaratan satu filesystem. Directory lokal di
    balik mount point tidak boleh digunakan sebagai fallback.

## Kontrol yang Telah Diuji

- request tanpa autentikasi dan session yang telah dicabut ditolak;
- pengguna A tidak dapat membaca atau menulis resource pengguna B;
- absolute path, `..`, encoded traversal, null byte, dan symlink escape ditolak;
- rename dan move tidak dapat keluar dari storage root;
- batas ukuran upload dan kegagalan akibat ruang kosong ditangani;
- download dan upload memakai buffer terbatas;
- assertion pada log memastikan token dan isi file tidak tercatat;
- login limiter berbasis akun+IP, pencegahan audit berulang saat transisi block,
  Argon2 admission, serta deadline dan admission khusus body login;
- metadata reindex dengan pergantian generation yang aman dan unhealthy index
  yang fail-closed;
- integritas chunk paralel, recovery saat startup untuk status `finalizing`,
  dan kebijakan reconciliation terhadap orphan upload part oleh administrator;
- database lock antarproses dan validasi marker volume.
- startup tanpa volume content yang valid, health degraded, error khusus
  `storage_unavailable`, runtime storage loss, dan penolakan penulisan ke
  fallback root.
- cancellation tepat setelah trash/restore/finalisasi/cancel tidak memutus
  repair metadata; startup degraded menutup metadata gate jika reconciliation
  filesystem masih pending.
- body JSON/chunk dan writer download memiliki deadline terbatas; stored chunk
  di-hash ulang saat completion meskipun whole checksum client tidak tersedia.
- reset password owner mencabut seluruh session secara transaction-safe.

## Kebijakan Regression Test

Setiap endpoint yang dilindungi harus memiliki negative authorization test.
Implementasi path file harus menguji traversal, symlink escape, conflict,
permission failure, dan cancellation.

## Kriptografi, Transport, dan Encryption at Rest

| Properti | SwaDrive `v1.0.0` |
| --- | --- |
| Penyimpanan password | Hash satu arah Argon2id; bukan encryption |
| Penyimpanan session di database | Digest SHA-256 dari opaque token acak 256-bit |
| Persistensi raw bearer token | Tidak disimpan pada server |
| TLS pada aplikasi Go | Tidak tersedia |
| Kerahasiaan transport | Didelegasikan ke batas Tailscale privat di production |
| Encryption at rest untuk file pada level aplikasi | Tidak diimplementasikan |
| Encryption at rest SQLite pada level aplikasi | Tidak diimplementasikan |

Kerahasiaan data fisik saat disimpan merupakan keputusan OS dan storage yang
terpisah, misalnya full-disk atau filesystem encryption beserta key lifecycle.
Hashing bukan encryption.

## Batas Resource dan Model Kegagalan

Argon2 (default 4), upload chunk (8), download (32), dan request login (64)
masing-masing memiliki process-local concurrency gate. Request body untuk login
dibatasi 64 KiB dengan read deadline khusus login selama 15 detik. Server
memakai read/write timeout umum 30 detik; JSON body dibatasi 30 detik, chunk
upload 5 menit, dan deadline writer download diperbarui hanya ketika progress
masih terjadi.
Listing/search/audit pagination, upload count, chunk count/size, DB pool,
startup reconciliation, dan admin orphan scan juga dibatasi.

Audit API tetap append-only. `login_rate_limited` ditulis hanya ketika bucket
akun/IP melintasi threshold, bukan pada setiap request yang sudah diblokir.
Request yang mencapai threshold tetap memiliki `login_failure`; block event
memakai reason code yang hanya mengidentifikasi jenis bucket, bukan raw
credential. Jika audit untuk block gagal, limiter tetap memblokir dan event
tidak dicoba ulang pada setiap penolakan. Limiter bersifat process-local;
restart memulai window baru.

Kebijakan lifecycle backend `v1.0.0` adalah:

- entry pada limiter kedaluwarsa di dalam proses setelah block atau window tidak
  lagi aktif; implementasi hanya memakai dua map yang masing-masing dibatasi
  10.000 entry;
- `login_rate_limited` hanya menghasilkan satu event per transisi akun/IP,
  sehingga satu interval block tidak menghasilkan baris audit sebanding dengan
  traffic penolakan;
- session yang kedaluwarsa atau dicabut dan riwayat upload yang telah mencapai
  status terminal tetap disimpan oleh `v1.0.0`;
- generation yang terputus ditandai obsolete pada reindex berikutnya, lalu
  seluruh baris obsolete dibersihkan dalam batch 500 setelah generation switch
  berhasil;
- audit event tetap append-only dan tidak dihapus secara otomatis.

Penetapan budget ukuran database, alert 70%/85%, backup, dan jadwal pemindahan
ke archive secara offline tetap menjadi tanggung jawab operator di production.
Kebijakan yang masih harus ditinjau menargetkan pemindahan riwayat session dan
upload berstatus terminal yang lebih tua dari 90 hari, serta audit yang lebih
tua dari 365 hari, ke archive yang dikendalikan administrator. Pemindahan akan
memakai maintenance command atau migration yang diuji terlebih dahulu.
Retention tersebut belum diimplementasikan dalam source code `v1.0.0` karena
penghapusan audit secara diam-diam akan mengubah semantik keamanan. Ini
merupakan keterbatasan kapasitas di production yang eksplisit, bukan klaim bahwa
pertumbuhan SQLite berhenti selamanya.

Filesystem dan SQLite tidak diklaim atomik. State trash dan upload yang telah
diketahui ditangani melalui reconciliation terarah. Unhealthy intent yang
disimpan secara durable membuat crash pada mkdir atau move menjadi fail-closed
sampai reindex eksplisit dilakukan. Setelah intent di-commit, finalisasi index
dan audit log maupun compensation cleanup tidak hanya bergantung pada lifetime
request. Internal repair context yang terbatas menyelesaikannya, sedangkan
kegagalan repair tetap fail-closed. Cleanup orphan part oleh administrator hanya
memeriksa directory internal `uploads/`, menggunakan scan terbatas, serta
mensyaratkan umur minimum, nama `.part` acak yang ketat, regular file, ketiadaan
entry database, dan opsi `-apply` eksplisit; operasi ini tidak pernah
memublikasikan file.

Pada `v1.0.2`, state ketersediaan storage aman untuk akses serentak dan hanya
dapat bertransisi dari `available` ke `unavailable` selama proses berjalan. Probe
memverifikasi marker, directory wajib, dan satu filesystem sebelum content
access. Kegagalan probe menghasilkan error domain `storage.ErrUnavailable`,
yang dipetakan menjadi HTTP 503 `storage_unavailable` tanpa path fisik,
detail mount, nama perangkat, atau error kernel. Transisi state dicatat sekali;
request berikutnya tidak menghasilkan log probe berulang yang berisik. Health
probe dibatasi paling sering sekali setiap lima detik; operasi content tetap
melakukan validasi storage sendiri. Readiness memeriksa DB, index, dan pending
reconciliation metadata secara terpisah.

Provider tidak melakukan recovery otomatis. Setelah volume yang benar kembali,
backend harus di-restart agar validasi storage, reconciliation trash/upload,
dan pemeriksaan kesehatan metadata berjalan sebelum content access dibuka.
Pembatasan ini mempertahankan operasi terputus yang sudah diketahui dan
mencegah path yang sekadar muncul kembali langsung dianggap aman.

## Keterbatasan yang Diketahui

API file, upload, dan audit pada `v1.0.0` masih hanya tersedia untuk owner, serta
hanya mendukung satu proses per pasangan database dan storage root. Backend
tidak melindungi dari `root` berbahaya, writer dengan UID yang sama, bind mount
berbahaya, alias database melalui hard link, atau dua database berbeda yang
mengarah ke satu root.

Area state harus dapat ditulis untuk file SQLite DB/WAL/SHM dan lock untuk
koordinasi proses. Karena itu, flock bukan batas keamanan terhadap writer
berbahaya dengan UID yang sama. Storage root yang ter-mount dan marker harus
dikendalikan administrator, sedangkan `files/`, `uploads/`, dan `trash/`
merupakan batas yang dapat ditulis service. Mode ownership yang tepat dan
konfigurasi unit `systemd` bergantung pada deployment dan tidak dipublikasikan
dalam repository.

Profil Docker menjalankan image distroless sebagai UID/GID `65532`, root
filesystem read-only, tanpa Linux capability, dan hanya menulis bind mount state
serta content. Marker/root storage harus tetap dikendalikan operator host.
Compose memublikasikan port ke loopback secara default; memilih alamat Tailscale
host harus eksplisit dan tidak menggantikan autentikasi aplikasi.

Content search, OCR, thumbnail, dan encryption at rest pada level aplikasi tidak
tersedia. Backend `v1.0.0` menjadi baseline untuk production pada 2026-08-26;
client Flutter tetap berupa scaffold. Source `v1.0.2` tidak menyediakan
pemulihan storage di dalam proses; restart backend tetap diperlukan setelah volume
yang diharapkan kembali tersedia.
