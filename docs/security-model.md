# Security Model SwaDrive

## Aset yang Dilindungi

- file pengguna dan metadata;
- password hash dan session state;
- raw bearer session token pada client;
- release artifact dan deployment configuration;
- administrator access;
- private-network control plane.

## Trust Boundaries

```text
client input
    -> private network boundary
    -> HTTP parsing and authentication
    -> resource authorization
    -> filesystem containment
    -> restricted Linux process
```

Setiap boundary tetap diperlukan. Perangkat yang dapat mencapai server belum tentu berhak membaca resource.

## Security Invariants

1. File API MUST implement application-level authentication and authorization before serving user data.
2. Protected resource decisions memakai identity dari authentication middleware, bukan user ID dari request body.
3. Logical path harus lolos parser dan semua physical operation tetap berada di bawah `os.Root` configured storage.
4. Service process tidak boleh menulis binary, service unit, administrator configuration, atau home administrator.
5. Raw password, token, file content, dan private host path tidak boleh dicatat pada log.
6. Client tidak menerima OS credential atau private-network administration credential.
7. Partial upload tidak terlihat sebagai file selesai.
8. Normal list, metadata, search, trash listing, dan upload status tidak membaca HDD.
9. Password hash hanya berada pada credential boundary auth; normal User/Identity tidak dapat membawanya.
10. Marker volume adalah identity check, bukan bukti mount; production wajib memverifikasi mount dan ownership di OS.
11. Process flock hanya mengoordinasikan proses SwaDrive yang bekerja sama; ia bukan isolasi terhadap hostile same-UID writer.

## Tested Backend-v1 Controls

- unauthenticated dan revoked session ditolak;
- user A tidak dapat membaca/menulis resource user B;
- absolute path, `..`, encoded traversal, null byte, dan symlink escape ditolak;
- rename/move tidak dapat keluar dari storage root;
- upload size limit dan free-space failure ditangani;
- download/upload memakai bounded buffer;
- log assertion memastikan token dan file content tidak tercatat.
- account+IP login limiter, block-transition audit suppression, Argon2 admission,
  dan login-only body deadline/admission;
- generation-safe metadata reindex dan fail-closed unhealthy index;
- parallel chunk integrity, finalizing startup recovery, dan orphan-part admin
  reconciliation policy;
- independent-process database lock dan volume-marker validation.

## Cryptography and Encryption Truth

| Property | Backend v1 |
| --- | --- |
| Password storage | Argon2id one-way hash; bukan encryption |
| Session DB storage | SHA-256 digest dari random 256-bit opaque token |
| Raw bearer persistence | tidak disimpan server-side |
| TLS di Go app | tidak ada |
| Transport confidentiality | didelegasikan ke Tailscale deployment yang harus diverifikasi kemudian |
| User-file application encryption at rest | tidak diimplementasikan |
| SQLite application encryption at rest | tidak diimplementasikan |

Physical-at-rest confidentiality adalah keputusan OS/storage terpisah, misalnya
full-disk/filesystem encryption dan key lifecycle. Hashing tidak disebut
encryption.

## Resource and Failure Limits

Argon2 (default 4), upload chunks (8), downloads (32), dan login requests (64)
memiliki process-local concurrency gates. Login request body dibatasi 64 KiB dan
login-only read deadline 15 detik. Listing/search/audit pagination, upload count,
chunk count/size, DB pool, startup reconciliation, dan admin orphan scan juga
dibatasi.

Audit API tetap append-only. `login_rate_limited` ditulis hanya saat account/IP
bucket melintasi threshold, bukan pada setiap request yang sudah diblokir.
Threshold request tetap memiliki `login_failure`; block event memakai reason code
yang hanya mengidentifikasi jenis bucket, bukan raw credential. Jika block audit
gagal, limiter tetap blocked dan event tidak dicoba ulang pada setiap denial.
Limiter memang process-local; restart memulai window baru.

Lifecycle policy backend v1 adalah:

- limiter entries expire in-process setelah block/window stale; tidak ada map
  evidence baru di luar dua map yang masing-masing capped 10.000;
- `login_rate_limited` hanya satu event per account/IP transition sehingga satu
  block interval tidak menghasilkan audit rows sebanding dengan denial traffic;
- expired/revoked session dan terminal upload history tetap disimpan oleh v1;
- interrupted generation ditandai obsolete pada reindex berikutnya dan seluruh
  obsolete rows dibersihkan batch 500 setelah successful generation switch;
- audit events tetap append-only dan tidak dihapus otomatis.

Sebelum production, operator harus menetapkan database-size budget, alert 70%/
85%, backup, dan offline archive schedule. Target policy yang harus direview
adalah memindahkan terminal session/upload history yang lebih tua dari 90 hari
dan audit lebih tua dari 365 hari ke administrator-controlled archive, dengan
maintenance command/migration yang diuji terlebih dahulu. Retention tersebut
belum diimplementasikan pada source v1 karena silent audit deletion akan mengubah
semantik keamanan. Ini adalah production capacity limitation yang eksplisit,
bukan klaim bahwa SQLite growth berhenti selamanya.

Filesystem+SQLite tidak diklaim atomic. Known trash/upload states direconcile
secara targeted. Durable unhealthy intent membuat crash mkdir/move fail closed
sampai explicit reindex. Setelah intent committed, request cancellation tidak
menjadi satu-satunya lifetime untuk index/audit finalization atau compensation
cleanup: bounded internal repair context menyelesaikannya, sementara kegagalan
repair tetap fail closed. Admin orphan-part cleanup hanya memeriksa internal
uploads directory, membatasi scan, memerlukan age minimum, strict random `.part`
name, regular file, DB absence, dan explicit `-apply`; ia tidak pernah publish.

## Current Limits

Backend v1 masih owner-only dan satu process per database/storage root. Ia tidak
menahan malicious root/same-UID writer, bind mount berbahaya, hard-link DB alias,
atau dua DB berbeda menuju satu root. State area harus writable untuk SQLite
DB/WAL/SHM dan coordination lock, sehingga flock bukan security boundary terhadap
hostile same-UID writer. Mounted storage root dan marker harus administrator-
controlled, sedangkan `files/`, `uploads/`, dan `trash/` adalah service-writable
boundary. Exact ownership/mode/systemd policy belum dipilih atau diverifikasi.
Content search/OCR/thumbnails dan application encryption at rest tidak ada.
Production belum diverifikasi atau dideploy oleh source phase ini.
