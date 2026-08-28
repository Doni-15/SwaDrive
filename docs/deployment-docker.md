# Deployment Docker SwaDrive

Profil ini menjalankan backend Go saja. Client Flutter Android/Linux tetap
aplikasi native dan tidak dijalankan sebagai container production.

## Model Image

`server/Dockerfile` memakai build multi-stage dengan base image yang dipin ke
digest. Stage builder menghasilkan binary statis `swadrive-server`,
`swadrive-admin`, dan `swadrive-healthcheck`. Stage runtime distroless hanya
membawa ketiga binary tersebut dan berjalan sebagai UID/GID `65532`.

`server/.dockerignore` membatasi build context ke Go module/source dan tidak
mengirim `.git`, client Flutter, build output, database, `.env`, atau secret.
Image tidak memuat password, token, Tailscale auth key, maupun konfigurasi
production privat.

## Menyiapkan Storage Persisten

Compose memakai dua bind mount yang wajib sudah ada:

- state/SQLite pada `SWADRIVE_STATE_DIR`;
- content root pada `SWADRIVE_STORAGE_DIR`.

Keduanya memakai `bind.create_host_path: false`. Salah ketik atau directory host
yang belum dibuat membuat Compose gagal sebelum container dijalankan; Compose
tidak boleh membuat directory root-owned yang tidak diharapkan.

Contoh berikut harus disesuaikan operator. UID/GID `65532` boleh diganti melalui
mekanisme deployment terpisah jika host memakai identity mapping lain, tetapi
container tidak boleh dijalankan sebagai root hanya untuk menghindari penataan
permission.

```bash
export SWADRIVE_STATE_DIR=/var/lib/swadrive-container
export SWADRIVE_STORAGE_DIR=/srv/swadrive-container
export SWADRIVE_STORAGE_VOLUME_ID='<unique-non-secret-volume-id>'

sudo install -d -o 65532 -g 65532 -m 0750 "$SWADRIVE_STATE_DIR"
sudo install -d -o root -g root -m 0755 "$SWADRIVE_STORAGE_DIR"
printf '%s\n' "$SWADRIVE_STORAGE_VOLUME_ID" | sudo tee "$SWADRIVE_STORAGE_DIR/.swadrive-volume" >/dev/null
sudo chown root:root "$SWADRIVE_STORAGE_DIR/.swadrive-volume"
sudo chmod 0644 "$SWADRIVE_STORAGE_DIR/.swadrive-volume"
sudo install -d -o 65532 -g 65532 -m 0750 \
  "$SWADRIVE_STORAGE_DIR/files" \
  "$SWADRIVE_STORAGE_DIR/uploads" \
  "$SWADRIVE_STORAGE_DIR/trash"
```

Content root, marker, dan mount point harus tetap dikendalikan administrator.
Hanya `files/`, `uploads/`, dan `trash/` yang writable oleh runtime. Ketiganya
wajib berada pada filesystem yang sama. Marker adalah identity check aplikasi,
bukan bukti bahwa volume yang benar sudah ter-mount; verifikasi mount/ownership
host tetap wajib.

Jangan memakai Docker anonymous volume untuk data penting tanpa kebijakan
backup yang jelas. Menghapus bind directory atau volume berarti menghapus state
atau content di luar lifecycle container.

## Build dan Start

Environment di atas harus tersedia pada shell operator atau file `.env` lokal
yang tidak dikomit. Build dan validasi konfigurasi:

```bash
docker compose config
docker compose build
```

Bootstrap owner dilakukan ketika server berhenti agar process lock database
tetap eksklusif:

```bash
docker compose stop server
docker compose run --rm --entrypoint /usr/local/bin/swadrive-admin server \
  bootstrap-owner -database /var/lib/swadrive/swadrive.db -username '<owner>'
docker compose up -d
```

Password dibaca interaktif tanpa masuk image, Compose file, environment, atau
command history. Untuk rotation/recovery credential, gunakan pola stop/run/up
yang sama dengan command berikut; seluruh session lama dicabut secara atomik:

```bash
docker compose run --rm --entrypoint /usr/local/bin/swadrive-admin server \
  set-owner-password -database /var/lib/swadrive/swadrive.db -username '<owner>'
```

Command `reindex` dan `reconcile-upload-parts` juga tersedia melalui binary
admin. Server harus dihentikan; selalu tinjau reconciliation dry-run sebelum
menambahkan `-apply`.

## Networking dan Tailscale

Container mendengarkan `0.0.0.0:8080` hanya di network namespace container.
Compose memublikasikannya ke `127.0.0.1` host secara default. Pilihan ini cocok
untuk Tailscale Serve atau reverse proxy lokal yang menjaga batas transport.

Untuk akses direct-tailnet, set `SWADRIVE_BIND_ADDRESS` ke alamat Tailscale host
yang spesifik sebelum `docker compose up`; jangan gunakan `0.0.0.0` kecuali
firewall deny-by-default dan seluruh interface exposure telah diaudit.
`EXPOSE 8080` pada image tidak memublikasikan port dengan sendirinya.

Untuk deployment native tanpa Compose, perhatikan perubahan upgrade `v1.0.2`:
default listener berubah dari `:8080` menjadi `127.0.0.1:8080`. Operator yang
sebelumnya bergantung pada default all-interface untuk akses direct-tailnet harus
menetapkan `SWADRIVE_LISTEN_ADDRESS='<tailscale-ip>:8080'` secara eksplisit.
Kontrak HTTP API tetap kompatibel; yang berubah adalah default konfigurasi
deployment.

Docker/Tailscale hanya menentukan reachability. Bearer authentication,
authorization owner, validation, dan filesystem confinement tetap ditegakkan
backend untuk setiap request sensitif.

## Health, Readiness, dan Shutdown

Image health check memanggil `GET /api/v1/health` dari dalam container. Endpoint
ini tetap HTTP 200 dalam degraded storage mode agar Docker tidak me-restart
control plane secara sia-sia. Status storage diprobe dengan cache maksimum lima
detik; setiap operasi content tetap memvalidasi volume secara independen.

`GET /api/v1/ready` memeriksa DB, index, dan metadata reconciliation gate.
Endpoint ini dapat mengembalikan HTTP 503 meskipun liveness proses masih sehat.

```bash
docker compose ps
curl http://127.0.0.1:${SWADRIVE_HOST_PORT:-8080}/api/v1/health
curl http://127.0.0.1:${SWADRIVE_HOST_PORT:-8080}/api/v1/ready
```

Compose memberi grace period 40 detik; server menangani SIGTERM dan memberi
shutdown/cleanup hingga 30 detik. Gunakan:

```bash
docker compose stop
```

Hindari `docker kill` untuk operasi normal.

## Update dan Backup

Sebelum update, lakukan backup konsisten SQLite beserta content sesuai RPO/RTO
operator. Trash bukan backup. Untuk snapshot offline sederhana, hentikan server
sebelum menyalin database, WAL/SHM yang relevan, marker, dan seluruh content
root; lakukan restore drill pada host terpisah.

```bash
docker compose build --pull
docker compose stop
# lakukan backup konsisten dan review migration/release notes
docker compose up -d
docker compose ps
```

Build tidak otomatis mengubah service production. Review digest base image,
diff, hasil CI, backup, dan release notes sebelum rollout.

## Hardening yang Diterapkan

- runtime non-root dan distroless;
- root filesystem read-only;
- seluruh Linux capability di-drop dan `no-new-privileges` aktif;
- writable scope hanya bind mount state/content dan tmpfs `/tmp` terbatas;
- port host loopback secara default;
- health checker statis tanpa shell/curl/package manager;
- log rotation Docker lokal;
- process limit dan graceful-stop budget;
- secret/config privat disuplai dari luar image.

Kontrol ini tidak menggantikan permission host, Tailscale ACL, firewall,
encryption at rest, backup, monitoring, patching host, atau pembatasan akses ke
Docker daemon.
