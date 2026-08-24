# Security Model SwaDrive

## Aset yang Dilindungi

- file pengguna dan metadata;
- password hash dan session state;
- application signing secret;
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
3. Resolved filesystem path harus tetap berada di bawah configured storage root setelah normalisasi dan symlink evaluation.
4. Service process tidak boleh menulis binary, service unit, administrator configuration, atau home administrator.
5. Raw password, token, file content, dan private host path tidak boleh dicatat pada log.
6. Client tidak menerima OS credential atau private-network administration credential.
7. Partial upload tidak terlihat sebagai file selesai.

## Test Minimum Sebelum File API

- unauthenticated dan revoked session ditolak;
- user A tidak dapat membaca/menulis resource user B;
- absolute path, `..`, encoded traversal, null byte, dan symlink escape ditolak;
- rename/move tidak dapat keluar dari storage root;
- upload size limit dan free-space failure ditangani;
- download/upload memakai bounded buffer;
- log assertion memastikan token dan file content tidak tercatat.

## Current Limit

File API belum ada, sehingga ketiadaan application authentication belum menjadi bypass terhadap user-data endpoint. Invariant di atas adalah blocking gate sebelum endpoint tersebut boleh dibuat public pada private tailnet.
