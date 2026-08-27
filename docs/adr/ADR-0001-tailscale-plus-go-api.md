# ADR-0001: Menggunakan Tailscale untuk Akses Jaringan Privat dan Go HTTP untuk Data Aplikasi

- **Status:** Diterima
- **Cakupan:** Arsitektur yang telah ditetapkan

## Konteks

SwaDrive memerlukan akses privat dari client Linux dan Android tanpa mengekspos
storage server melalui public port forwarding. Keterjangkauan jaringan,
autentikasi aplikasi, dan administrasi Linux menyelesaikan masalah yang berbeda
dan harus tetap dipisahkan.

SSH/SCP dapat mendukung bootstrap administrasi dan deployment sementara, tetapi
aplikasi yang dibangun di atas SSH akan membawa credential administrator serta
mekanisme akses server ke dalam client Flutter.

## Keputusan

Gunakan Tailscale untuk keterjangkauan jaringan privat dari perangkat ke server
dan Go HTTP API untuk seluruh operasi data aplikasi.

```text
Tailscale          akses perangkat/jaringan
Go HTTP API        autentikasi, otorisasi, dan operasi file aplikasi
SSH/SCP            bootstrap dan deployment khusus administrator
```

Prefix API awal adalah `/api/v1` dan listener awal menggunakan TCP 8080. Grant
Tailscale yang dimaksud hanya mengizinkan identitas Member biasa yang dipilih
secara eksplisit untuk mencapai storage server bertag melalui TCP 8080. Client
Flutter tetap menjadi perangkat Member biasa.

Keputusan ini tidak mewajibkan Tailscale Serve. Backend `v1.0.0`
mengimplementasikan protokol resumable upload melalui Go HTTP API. Terminasi
HTTPS dapat dievaluasi kemudian tanpa mengubah batas antara akses jaringan
privat dan otorisasi aplikasi.

Backend `v1.0.0` tidak melakukan terminasi TLS di dalam proses Go. Kerahasiaan
transport bergantung pada penggunaan jalur Tailscale dan verifikasi ACL yang
tepat di production. Hal ini bukan klaim encryption pada level aplikasi dan
tidak menyediakan encryption at rest untuk file atau SQLite.

## Konsekuensi

- Public port forwarding tidak diperlukan.
- Flutter tidak memerlukan SSH private key atau jalur transfer SFTP/SCP.
- API dapat mendukung streaming, Range request, progress, dan resumable transfer
  dengan semantik HTTP.
- Keterjangkauan melalui Tailscale tidak memberikan hak akses aplikasi; Go
  tetap harus mengautentikasi dan mengotorisasi setiap operasi yang dilindungi.
- Perangkat client memerlukan koneksi Tailscale yang berfungsi untuk memakai
  API privat.
- Setiap perubahan listener atau TLS di masa mendatang harus mempertahankan
  akses jaringan yang sempit dan didokumentasikan secara eksplisit.
