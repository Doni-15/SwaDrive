# Kebijakan Keamanan

## Versi yang Didukung

SwaDrive `v1.0.0` adalah baseline production stabil saat ini. Perbaikan
keamanan dikembangkan terhadap source terbaru yang relevan dan wajib
mempertahankan invariant keamanan `v1.0.0` yang telah didokumentasikan.

## Melaporkan Kerentanan

Jangan membuka issue publik yang berisi detail exploit, credential, data
infrastruktur privat, atau file pengguna.

Gunakan GitHub private vulnerability reporting jika fitur tersebut diaktifkan
untuk repository ini. Belum ada alamat pelaporan privat alternatif; jangan
mempublikasikan detail sensitif dalam issue atau discussion.

Sertakan revision yang terdampak, dampak, kondisi reproduksi, dan proof of
concept minimal yang sudah dibersihkan dari secret. Berikan waktu kepada
maintainer untuk melakukan investigasi sebelum pengungkapan publik.

## Cakupan Keamanan

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
Akses production menggunakan batas jaringan privat Tailscale. Laporan tidak
boleh menyebut hashing sebagai encryption.

Marker `.swadrive-volume` merupakan pemeriksaan identitas aplikasi, bukan bukti
bahwa HDD yang dituju telah ter-mount. Kontrol mount, ownership, dan `systemd`
di production tetap menjadi batas keamanan independen meskipun seluruh test pada
source code lulus.

Flock database mengoordinasikan proses SwaDrive yang bekerja sama; mekanisme ini
bukan batas keamanan terhadap proses berbahaya dengan UID yang sama. SQLite
memerlukan area state yang dapat ditulis service untuk file DB/WAL/SHM.
Deployment production harus memastikan storage root yang ter-mount dan marker
tetap dikendalikan administrator, sedangkan hanya content boundary `files/`,
`uploads/`, dan `trash/` yang dapat ditulis oleh runtime service account. Mode
permission yang tepat bergantung pada deployment dan tidak dipublikasikan dalam
repository ini.

## Informasi Sensitif

Jangan pernah mengirim auth key aktif, password, private key, raw session token,
recovery code, salinan database production, log yang berisi secret, atau data
privat pengguna. Samarkan detail host yang tidak diperlukan untuk mereproduksi
masalah.
