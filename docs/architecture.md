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
    +-- application state directory
    +-- storage directory
    +-- incomplete-upload directory
    +-- trash directory
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
- `$STORAGE_ROOT`: writable hanya oleh service sesuai kebutuhan;
- `$STATE_DIR`: writable oleh service untuk database/application state;
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

## Data dan Transfer

File bytes tetap berada pada filesystem, bukan database BLOB. Database planned menyimpan users, sessions, resource metadata, upload state, dan audit events. Download/upload harus streaming, incomplete upload dipisahkan, dan finalization dilakukan secara atomic bila memungkinkan.

## Status Implementasi

Yang sudah tersedia pada repository hanya Go health endpoint dan Flutter scaffold. Authentication, filesystem operations, database schema, serta transfer protocol belum diimplementasikan.
