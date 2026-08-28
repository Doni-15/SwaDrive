# Kebijakan Keamanan

## Versi yang Didukung

SwaDrive `v1.0.0` adalah baseline deployment production yang terdokumentasi;
source keamanan terbaru adalah `v1.0.2`. Perbaikan keamanan dikembangkan
terhadap source code terbaru yang relevan dan wajib
mempertahankan invariant keamanan `v1.0.0` yang telah didokumentasikan.

## Melaporkan Kerentanan

Pelaporan kerentanan dipisahkan dari kontribusi umum. Repository ini tidak
menerima pull request eksternal, tetapi laporan kerentanan tetap dapat
disampaikan secara bertanggung jawab melalui jalur privat apabila tersedia.

Jangan membuka issue publik yang berisi detail exploit, credential, data
infrastruktur privat, atau file pengguna.

Gunakan GitHub private vulnerability reporting hanya jika opsi
`Report a vulnerability` tersedia untuk repository ini. Jika opsi tersebut
tidak tersedia, repository ini belum menyediakan jalur pelaporan privat
alternatif; jangan mempublikasikan detail sensitif dalam issue atau discussion.

Sertakan revision yang terdampak, dampak, kondisi reproduksi, dan proof of
concept minimal yang tidak memuat secret. Berikan waktu kepada
maintainer untuk melakukan investigasi sebelum pengungkapan publik.

## Cakupan Keamanan

Dokumen ini mengatur pelaporan dan kebijakan keamanan. Arsitektur teknis, trust
boundary, invariant, dan keterbatasan sistem dijelaskan terpisah dalam
[model keamanan SwaDrive](docs/security-model.md).

Area prioritas tinggi meliputi:

- bypass autentikasi atau otorisasi;
- path traversal atau symlink escape;
- pengungkapan atau perubahan file yang tidak semestinya;
- privilege escalation melampaui service account terbatas;
- kebocoran session atau token, maupun kegagalan pencabutan;
- pemrosesan upload yang tidak aman;
- eksposur jaringan yang tidak disengaja.

Backend saat ini memakai hash password Argon2id dan hanya menyimpan digest
SHA-256 dari opaque session token. Backend tidak menyediakan application-level
encryption untuk file pengguna atau SQLite, dan tidak melakukan terminasi TLS.
Akses di production menggunakan batas jaringan privat Tailscale. Laporan tidak
boleh menyebut hashing sebagai encryption.

Marker `.swadrive-volume` merupakan pemeriksaan identitas aplikasi, bukan bukti
bahwa HDD yang dituju telah ter-mount. Kontrol mount, ownership, dan `systemd`
di production tetap menjadi batas keamanan independen meskipun seluruh test
terhadap source code lulus.

Flock untuk database mengoordinasikan proses SwaDrive yang bekerja sama;
mekanisme ini bukan batas keamanan terhadap proses berbahaya dengan UID yang
sama. SQLite memerlukan area state yang dapat ditulis service untuk file
DB/WAL/SHM.
Deployment di production harus memastikan storage root yang ter-mount dan marker
tetap dikendalikan administrator, sedangkan hanya batas penyimpanan `files/`,
`uploads/`, dan `trash/` yang dapat ditulis oleh runtime service account. Mode
permission systemd production tetap bergantung pada deployment. Profil Docker
menjalankan UID/GID non-root `65532`, root filesystem read-only, tanpa Linux
capability, dengan state/content pada bind mount yang dipersiapkan operator.
Port Compose default hanya dipublikasikan ke loopback; akses tailnet harus
memakai Tailscale Serve atau alamat Tailscale host yang dipilih eksplisit,
bukan `0.0.0.0`.

## Informasi Sensitif

Jangan pernah mengirim auth key aktif, password, private key, raw session token,
recovery code, salinan database di production, log yang berisi secret, atau data
privat pengguna. Samarkan detail host yang tidak diperlukan untuk mereproduksi
masalah.
