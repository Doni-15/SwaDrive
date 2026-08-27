# Architecture Decision Records

Folder ini memuat keputusan jangka panjang yang menjelaskan alasan di balik
arsitektur SwaDrive yang telah ditetapkan. Status proyek saat ini dan rencana
implementasi mendatang tersedia dalam [README](../../README.md) utama.

## Keputusan yang Diterima

- [ADR-0001](ADR-0001-tailscale-plus-go-api.md): Tailscale menyediakan akses
  jaringan privat, Go HTTP API membawa data aplikasi, dan SSH hanya digunakan
  untuk administrasi.
- [ADR-0002](ADR-0002-restricted-service-account.md): backend berjalan dengan
  service account khusus yang terbatas.
- [ADR-0003](ADR-0003-application-authentication-and-sessions.md): autentikasi
  aplikasi tetap terpisah dari Tailscale dan memakai password Argon2id dengan
  server-side session berbasis opaque token yang dapat dicabut secara
  independen.
- [ADR-0004](ADR-0004-persistent-fixed-chunk-resumable-uploads.md): upload
  memakai fixed chunk persisten, penulisan yang diverifikasi, dan publikasi
  atomik.
- [ADR-0005](ADR-0005-nvme-metadata-plane-and-hdd-content-plane.md): SQLite
  melayani metadata plane, isi file pengguna tetap berada pada filesystem di
  data disk, dan celah mutasi yang diketahui ditangani secara fail-closed
  melalui durable state.
- [ADR-0006](ADR-0006-process-coordination-and-storage-identity.md): proses yang
  bekerja sama berkoordinasi melalui process lock kanonis untuk database dan
  mengikat operasi pada identitas storage yang disediakan administrator.
- [ADR-0007](ADR-0007-swadrive-naming-and-legacy-production-identifiers.md):
  SwaDrive adalah nama proyek saat ini, sedangkan identifier lama di production
  dipertahankan jika risiko migrasi tidak sebanding dengan manfaat rename.

ADR baru harus memuat status, konteks, keputusan, dan konsekuensi. Perubahan
keputusan harus menggantikan ADR sebelumnya secara eksplisit, bukan menulis
ulang riwayatnya secara diam-diam.

Klarifikasi status implementasi dan correctness dapat memperbarui ADR yang
telah diterima tanpa mengubah keputusannya. Keputusan khusus mengenai koordinasi
proses dan identitas storage dicatat terpisah sebagai ADR-0006, sedangkan
keputusan penamaan proyek dan identifier lama di production dicatat sebagai
ADR-0007.
