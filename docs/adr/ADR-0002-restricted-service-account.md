# ADR-0002: Menjalankan Backend dengan Service Account Khusus yang Terbatas

- **Status:** Diterima
- **Cakupan:** Arsitektur yang telah ditetapkan

## Konteks

Backend akan memproses nama yang dikendalikan pengguna, upload, download,
metadata, dan operasi filesystem. Kompromi terhadap backend tidak boleh secara
otomatis memberikan kewenangan `root`, kewenangan administrator,
kewenangan deployment, atau akses filesystem yang luas.

## Keputusan

Jalankan backend Go dengan service account khusus yang terbatas di Linux, bukan
sebagai `root` atau akun administrator.

Service account menggunakan shell noninteraktif, tidak memiliki sudo, login
interaktif atau SSH, maupun kewenangan administrasi Tailscale. Akun ini hanya
boleh menulis path storage, state, dan log aplikasi yang memerlukan mutasi saat
runtime, serta membaca konfigurasi yang disediakan administrator sesuai
kebutuhan.

Administrator memilih path state, tetapi runtime service account harus dapat
membuat dan menulis file database, WAL, dan SHM SQLite di sana. Flock di samping
database mengoordinasikan proses SwaDrive yang bekerja sama; mekanisme ini
bukan batas keamanan terhadap proses berbahaya yang berjalan dengan UID sama
atau pihak lain yang dapat mengganti file dalam area state yang dapat ditulis
tersebut.

Storage root yang ter-mount dan marker `.swadrive-volume` tetap dikendalikan
administrator serta tidak boleh dapat diganti oleh runtime service account.
Subdirectory penyimpanan `files/`, `uploads/`, dan `trash/` yang telah disediakan
sebelumnya merupakan batas data yang dapat ditulis service. Ownership, mode,
urutan mount, dan rule writable path `systemd` yang tepat di production menjadi
tanggung jawab deployment serta harus mendukung batas ini tanpa memberikan
akses yang lebih luas.

Binary yang digunakan di production tetap dimiliki administrator dalam
directory rilis, sedangkan konfigurasi tetap dikendalikan administrator dalam
directory konfigurasi yang terpisah. Runtime service account tidak boleh dapat
mengganti executable untuk backend.

## Konsekuensi

- Kompromi terhadap backend memiliki batas privilege dan filesystem Linux yang
  lebih sempit.
- Aktivitas administrator, instalasi rilis, dan runtime aplikasi tetap dapat
  diaudit sebagai kewenangan yang terpisah.
- Ownership dan rule writable path `systemd` memerlukan pemeliharaan yang
  disengaja.
- Kegagalan permission harus diperbaiki secara sempit; `chmod 777`, akses sudo,
  atau menjalankan service sebagai administrator bukan workaround yang dapat
  diterima.
- Sandboxing `systemd` tambahan dapat diterapkan setelah service minimal
  berfungsi.
