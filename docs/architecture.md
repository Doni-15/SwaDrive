# Arsitektur SwaDrive

Dokumen ini menjelaskan arsitektur publik SwaDrive tanpa bergantung pada
hostname, username, alamat akun, atau path tertentu di production. Status proyek
dan rilis dijelaskan dalam [README](../README.md), sedangkan kontrol keamanan
dirinci dalam [model keamanan](security-model.md).

## Komponen

```text
client Flutter
    |
    | tailnet privat, hanya port aplikasi
    v
storage server
    |
    +-- Go API yang dikelola systemd
    |      service account terbatas
    |
    +-- control/metadata plane pada NVMe
    |      SQLite: auth, session, audit, state upload/trash, metadata index
    |
    +-- content plane pada HDD (satu filesystem)
           files/ uploads/ trash/
```

Go API merupakan satu-satunya data plane aplikasi. SSH atau secure copy hanya
digunakan oleh administrator untuk deployment; keduanya bukan protokol yang
dipakai client.

## Batas Service dan Identitas

| Identitas | Kewenangan | Tidak boleh |
| --- | --- | --- |
| Akun administrator | OS, rilis, dan konfigurasi | Menangani request aplikasi sebagai proses runtime |
| Service account | Data dan state yang memang perlu diubah API | Menggunakan sudo, login interaktif, mengganti binary, atau mengatur tailnet |
| Anggota tailnet | Mencapai port aplikasi yang diizinkan | Memperoleh hak aplikasi secara otomatis |
| Akun aplikasi SwaDrive | Mengoperasikan resource sesuai otorisasi | Memperoleh akses OS atau administrasi tailnet |

## Batas Kepemilikan

- `$RELEASE_DIR` dan binary dimiliki administrator, dengan akses baca dan
  eksekusi bagi service;
- `$CONFIG_DIR` dikendalikan administrator, dengan akses baca minimum bagi
  service;
- `$STORAGE_ROOT` dan `.swadrive-volume` merupakan batas mount yang dikendalikan
  administrator; `files/`, `uploads/`, dan `trash/` di bawahnya merupakan batas
  penyimpanan yang dapat ditulis service;
- `$STATE_DIR` dipilih administrator, tetapi dapat ditulis secukupnya oleh
  service untuk database SQLite, WAL, SHM, state aplikasi, dan lock untuk
  koordinasi proses;
- `$LOG_DIR` dapat ditulis service, tanpa secret atau isi file.

Masalah permission harus diperbaiki secara spesifik. `chmod 777`, menjalankan
backend sebagai `root`, atau memberikan sudo kepada service account bukan
solusi.

## Batas Jaringan

Kebijakan jaringan privat hanya memberi perangkat anggota biasa akses ke port
aplikasi pada perangkat server yang memiliki tag. Rule berikut merupakan pola
generik, bukan salinan konfigurasi operasional:

```json
{
  "grants": [
    {
      "src": ["<client-member-identity>"],
      "dst": ["<storage-server-tag>"],
      "ip": ["tcp:<application-port>"]
    }
  ]
}
```

Firewall host tetap default-deny dan hanya menerima port aplikasi melalui
interface jaringan privat.

## Alur Request

1. Client mengirim request HTTP melalui tailnet privat ke Go API.
2. Backend mengurai input, memvalidasi session, menerapkan otorisasi owner,
   lalu memvalidasi logical path sebelum mengakses storage.
3. Operasi metadata normal membaca SQLite pada NVMe. HDD baru diakses untuk
   membaca isi file atau melakukan mutasi filesystem.
4. Perubahan state, metadata index, dan audit log diselesaikan melalui
   transaction, compensation, atau recovery sesuai jenis operasinya.

Pada `v1.0.1`, provider storage memisahkan ketersediaan proses HTTP dari
ketersediaan content plane. Login, `/auth/me`, pengelolaan session, health, dan
API lain yang hanya membaca control/metadata plane dapat tetap bekerja secara
aman ketika HDD tidak dapat diverifikasi. Setiap operasi yang memerlukan byte
atau mutasi content terlebih dahulu melewati provider tersebut dan menerima
`storage.ErrUnavailable` jika batas storage tertutup.

Pada `v1.0.2`, startup tetap memeriksa kesehatan index melalui SQLite ketika
content storage tidak tersedia. Jika terdapat trash `trashing/restoring` atau
upload `finalizing` yang membutuhkan filesystem untuk reconciliation, metadata
gate ditutup dan endpoint metadata mengembalikan `metadata_unavailable`.
Autentikasi dan session tetap tersedia. `GET /api/v1/ready` memeriksa database,
index, dan gate ini tanpa menjadikan ketersediaan HDD sebagai liveness proses.

## Control Plane, Metadata Plane, dan Content Plane

Isi file disimpan pada filesystem di content plane, bukan sebagai BLOB
database. SQLite pada NVMe menyimpan akun aplikasi SwaDrive, session aplikasi,
audit event, state operasional upload dan trash, serta metadata index yang hanya
berisi metadata.

Listing normal, metadata, pencarian, listing trash, dan status upload hanya
membaca SQLite: **metadata plane tidak boleh membangunkan data disk**.

HDD diakses hanya ketika byte atau mutasi memang diminta: upload chunk,
download dengan Range, mkdir, move, trash, restore, recovery atas objek yang
sudah diketahui, dan reindex eksplisit oleh administrator lokal. `files/`,
`uploads/`, dan `trash/` wajib berada pada filesystem yang sama agar rename
untuk publikasi, trash, dan restore tetap atomik dalam batas filesystem. Upload
parsial tidak masuk ke metadata index sebelum dipublikasikan.

File API memakai logical path yang melalui parser dan `os.Root`; physical path
pada host tidak diterima dari client maupun dikembalikan kepadanya. Metadata
index merupakan derived state yang dapat dibangun ulang dengan generation
switch. Reindex tidak berjalan otomatis saat startup atau browsing.

## Koordinasi Proses dan Identitas Storage

Backend menggunakan process lock kanonis yang diturunkan dari path database.
Server dan command administrasi lokal mengambil lock tersebut secara
non-blocking. Backend `v1.0.0` mengasumsikan satu proses menggunakan satu
database dan satu storage root. Alias database melalui symlink telah diuji; alias
hard link atau dua database berbeda yang mengarah ke storage root yang sama
bukan model deployment yang didukung.

Flock tersebut mengoordinasikan proses SwaDrive yang bekerja sama. Ia bukan
batas keamanan terhadap proses berbahaya dengan UID yang sama atau pihak lain
yang dapat mengganti file dalam area state yang harus dapat ditulis untuk
kebutuhan SQLite.

Storage root memiliki `.swadrive-volume` berisi
`SWADRIVE_STORAGE_VOLUME_ID`. Marker dengan isi salah, hilang, atau bukan
regular file ditolak sebelum subdirectory penyimpanan diinisialisasi. Marker
merupakan identitas aplikasi, bukan bukti mount. Deployment di production
mensyaratkan parent directory milik administrator serta verifikasi dan urutan
mount melalui OS dan `systemd`. Storage root yang ter-mount beserta marker tidak
boleh dapat diganti service, sedangkan subdirectory penyimpanan memang dapat
ditulis service. Mode permission dan konfigurasi unit yang tepat bergantung pada
deployment dan tidak disimpan dalam repository ini.

Provider `v1.0.1` membuka root terverifikasi untuk setiap operasi content,
memeriksa kembali marker, keberadaan `files/`, `uploads/`, dan `trash/`, serta
persyaratan satu filesystem sebelum meneruskan operasi. Root terverifikasi
berbasis `os.Root` memastikan operasi yang sudah dimulai tetap terikat pada
filesystem tersebut dan tidak berpindah ke directory lokal di balik mount
point. State ketersediaan aman untuk akses serentak dan hanya dapat berubah dari
`available` menjadi `unavailable` selama masa hidup proses, sehingga path
yang muncul kembali tidak langsung membuka data plane.

## Model Kegagalan

Filesystem dan SQLite tidak dapat berbagi satu transaction. Trash, restore, dan
finalisasi upload menyimpan transitional state agar reconciliation terarah dapat
dilakukan saat startup. Mkdir dan move menandai metadata index sebagai unhealthy
dalam durable state sebelum mutasi filesystem, lalu membersihkan tanda tersebut
dalam transaction SQLite yang juga mengubah index dan audit log. Setelah intent
atau side effect filesystem terjadi, commit metadata, audit, compensation, dan
fallback fail-closed memakai internal repair context yang terbatas dan tidak
dibatalkan hanya karena request client terputus. Aturan ini berlaku untuk mkdir,
move, trash, restore, complete, cancel, dan cleanup upload. Kegagalan repair
internal tetap membiarkan transitional state atau index unhealthy. Crash di
tengah operasi
membuat startup fail-closed sampai reindex eksplisit dilakukan, bukan memicu
HDD scan. Upload part yang dibuat sebelum DB commit tidak dipublikasikan;
administrator dapat menjalankan `reconcile-upload-parts` dengan pembatasan
umur, nama, dan tipe setelah meninjau dry-run.

Jika storage tidak tersedia pada startup, reconciliation trash dan upload yang
memerlukan content plane ditunda dan HTTP server tetap dimulai dalam degraded
storage mode. Jika storage hilang pada runtime, probe berikutnya menutup provider
dan cleanup upload tidak menghentikan proses. `v1.0.1` sengaja tidak melakukan
pemulihan otomatis di dalam proses. Operator harus mengembalikan volume yang
benar dan me-restart backend; startup berikutnya memverifikasi identitas
storage, directory wajib, satu filesystem, reconciliation yang diketahui, dan
kesehatan metadata sebelum data plane tersedia kembali.

## Arsitektur Runtime dan Deployment

Deployment production yang terdokumentasi menjalankan satu proses backend pada
Debian sebagai service `systemd` dengan runtime service account terbatas.
`v1.0.2` juga menyediakan profil Docker opsional: build multi-stage, runtime
distroless UID/GID `65532`, root filesystem read-only, capability kosong, state
dan content pada bind mount persisten, serta host port loopback sebagai default.
Container bukan pengganti Tailscale/firewall dan tidak memuat client Flutter.
Panduan operasional tersedia dalam [deployment Docker](deployment-docker.md).

SQLite dan state operasional tetap berada pada storage cepat yang persisten,
sedangkan isi file berada pada content volume yang persisten. Detail source
terbaru tersedia dalam [catatan rilis `v1.0.2`](releases/v1.0.2.md). Unit
`systemd` aktual tetap harus mengizinkan backend dimulai tanpa HDD tanpa
mengurangi service account, permission, sandbox, firewall, atau batas
Tailscale. Status client tersedia dalam [README](../README.md).
