# Arsitektur SwaDrive

Dokumen ini menjelaskan arsitektur public yang tidak bergantung pada hostname, username, alamat akun, atau path production tertentu.

## Komponen

```text
Flutter clients
    |
    | private tailnet, application port only
    v
storage server
    |
    +-- systemd-managed Go API
    |      restricted service account
    |
    +-- NVMe control/metadata plane
    |      SQLite: auth, sessions, audit, upload/trash state, file index
    |
    +-- HDD content plane (one filesystem)
           files/ uploads/ trash/
```

Go API adalah satu-satunya application data plane. SSH atau secure copy hanya dapat dipakai administrator untuk deployment; keduanya bukan protocol client.

## Batas Identitas

| Identity | Kewenangan | Tidak boleh |
| --- | --- | --- |
| Administrator account | OS, release, dan konfigurasi | menjalankan normal request sebagai runtime |
| Service account | data/state yang memang perlu diubah API | sudo, login interaktif, mengganti binary, mengatur tailnet |
| Tailnet member | mencapai port aplikasi yang diizinkan | memperoleh hak aplikasi secara otomatis |
| Application user | operasi resource sesuai authorization | memperoleh akses OS atau tailnet administration |

## Ownership Generik

- `$RELEASE_DIR` dan binary: administrator-owned, read/execute oleh service;
- `$CONFIG_DIR`: administrator-controlled, minimal read bagi service;
- `$STORAGE_ROOT` dan `.swadrive-volume`: mounted boundary yang
  administrator-controlled; `files/`, `uploads/`, dan `trash/` di bawahnya adalah
  service-writable content boundary;
- `$STATE_DIR`: path yang dipilih administrator tetapi writable secukupnya oleh
  service untuk database SQLite, WAL, SHM, application state, dan coordination
  lock;
- `$LOG_DIR`: writable oleh service, tanpa secret atau file content.

Permission issue harus diperbaiki secara sempit. `chmod 777`, menjalankan backend sebagai root, atau memberi sudo pada service account bukan solusi.

## Network Boundary

Private-network policy hanya memberi normal member devices akses ke application port pada server-tagged device. Rule public berikut bersifat pola generik, bukan salinan konfigurasi operational:

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

Firewall host tetap default-deny dan hanya menerima application port melalui private-network interface.

## Control/Metadata Plane dan Content Plane

File bytes berada pada content filesystem, bukan database BLOB. SQLite pada
NVMe menyimpan users, opaque sessions, audit events, upload/trash operational
state, dan metadata-only file index. Normal listing, metadata, search, trash
listing, dan upload status hanya membaca SQLite: **metadata plane must not wake
the data disk**.

HDD disentuh ketika byte atau mutasi memang diminta: upload chunk, download/
Range, mkdir, move, trash, restore, recovery object yang sudah diketahui, dan
explicit local-admin reindex. `files/`, `uploads/`, dan `trash/` wajib berada
pada filesystem yang sama agar rename publication/trash/restore tetap atomic
pada boundary filesystem. Partial upload tidak masuk file index sebelum
publication.

File API memakai logical path yang melalui parser dan `os.Root`; physical host
path tidak diterima dari client dan tidak dikembalikan. Metadata index adalah
derived state yang dapat dibangun ulang dengan generation switch. Reindex tidak
berjalan otomatis saat startup atau browsing.

## Process dan Storage Ownership

Server dan local admin command mengambil non-blocking flock berdasarkan
canonical database path. Backend v1 mengasumsikan satu database dan satu storage
root dimiliki satu process. Symlink alias database diuji; hard-link alias atau
dua database berbeda menuju storage root yang sama tidak dijadikan model
deployment yang didukung.

Flock tersebut mengoordinasikan proses SwaDrive yang bekerja sama. Ia bukan
security boundary terhadap malicious same-UID process atau actor lain yang dapat
mengganti file di state area yang writable untuk kebutuhan SQLite.

Storage root memiliki `.swadrive-volume` berisi
`SWADRIVE_STORAGE_VOLUME_ID`. Marker yang salah/hilang/nonregular ditolak
sebelum content directories diinisialisasi. Marker adalah application identity,
bukan mount proof. Production memakai administrator-owned parent, mount
verification/order melalui OS dan `systemd`, mounted storage root/marker yang
tidak dapat diganti service, dan content subdirectories yang memang writable
oleh service. Exact permission modes dan unit configuration bersifat
deployment-specific dan tidak disimpan di source repository.

## Failure Model

Filesystem dan SQLite tidak dapat menjadi satu transaction. Trash, restore, dan
upload finalization menyimpan transitional state untuk targeted startup
reconciliation. Mkdir/move menandai file index unhealthy secara durable sebelum
filesystem mutation dan membersihkannya dalam transaction SQLite yang juga
mengubah index/audit. Setelah intent committed, finalization memakai bounded
internal repair context yang tidak dibatalkan hanya karena request client putus.
Repair internal yang gagal tetap meninggalkan index unhealthy. Crash di tengah
operasi membuat startup fail closed sampai explicit reindex, bukan memicu HDD
scan. Upload part yang tercipta sebelum DB commit tidak dipublish; admin dapat
menjalankan age/name/type-gated `reconcile-upload-parts` setelah meninjau dry-run.

## Status Implementasi

Backend Go v1 menyediakan application auth, owner authorization, SQLite
metadata index, logical-path file operations, trash/restore, streaming Range
download, persistent fixed-chunk resumable upload, audit, local admin commands,
resource gates, dan recovery yang dijelaskan di atas. Backend v1 menjadi
production baseline `v1.0.0` pada 2026-08-26. Deployment menjalankan service
pada Debian melalui `systemd`, memakai restricted runtime account, Tailscale
private access, SQLite metadata pada NVMe, dan file content pada HDD. Flutter
masih scaffold.
