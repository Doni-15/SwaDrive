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

## Model Kegagalan

Filesystem dan SQLite tidak dapat berbagi satu transaction. Trash, restore, dan
finalisasi upload menyimpan transitional state agar reconciliation terarah dapat
dilakukan saat startup. Mkdir dan move menandai metadata index sebagai unhealthy
dalam durable state sebelum mutasi filesystem, lalu membersihkan tanda tersebut
dalam transaction SQLite yang juga mengubah index dan audit log. Setelah intent
di-commit, finalisasi menggunakan internal repair context yang terbatas dan
tidak dibatalkan hanya karena request client terputus. Kegagalan repair
internal tetap membiarkan index berstatus unhealthy. Crash di tengah operasi
membuat startup fail-closed sampai reindex eksplisit dilakukan, bukan memicu
HDD scan. Upload part yang dibuat sebelum DB commit tidak dipublikasikan;
administrator dapat menjalankan `reconcile-upload-parts` dengan pembatasan
umur, nama, dan tipe setelah meninjau dry-run.

## Arsitektur Runtime dan Deployment

Deployment di production menjalankan satu proses backend pada Debian sebagai
service `systemd` dengan runtime service account terbatas. Akses aplikasi hanya
diterima melalui tailnet privat. SQLite dan state operasional berada pada NVMe,
sedangkan isi file berada pada HDD. Detail versi yang telah dirilis tersedia
dalam [catatan rilis `v1.0.0`](releases/v1.0.0.md); status client tersedia dalam
[README](../README.md).
