# ADR-0003: Menggunakan Autentikasi Aplikasi dengan Opaque Server-Side Session

- **Status:** Diterima
- **Cakupan:** Arsitektur yang telah ditetapkan

## Konteks

Tailscale menentukan perangkat yang dapat mencapai service jaringan privat
SwaDrive, tetapi keterjangkauan jaringan tidak menetapkan akun aplikasi maupun
memberikan otorisasi pada level aplikasi. SwaDrive juga memerlukan beberapa
session perangkat yang dapat kedaluwarsa dan dicabut secara independen tanpa
mengekspos credential yang dapat digunakan ulang dalam persistent storage.

Password dan credential session bersifat sensitif terhadap keamanan. Formatnya
harus mendukung verifikasi yang aman dan upgrade parameter di masa mendatang
tanpa memerlukan siklus hidup signing key JWT atau sistem refresh token pada
fase awal produk.

## Keputusan

Go menyediakan autentikasi dan otorisasi aplikasi secara independen dari
Tailscale. Akun aplikasi SwaDrive disimpan dalam SQLite. Pada tahap awal tidak
ada endpoint self-registration publik. Pembuatan owner awal diimplementasikan
melalui command `bootstrap-owner` yang dikendalikan administrator lokal; apakah
owner benar-benar telah disediakan di production tetap merupakan state
deployment. Model data tetap mendukung penambahan pengguna.

Password di-hash dengan Argon2id dan disimpan dalam format encoded
self-describing yang memuat versi algoritma, parameter, salt, dan derived hash.
Parameter serta verifikasi hashing password dipusatkan agar hash baru dapat
memakai parameter yang ditingkatkan, sementara hash lama tetap dapat
diverifikasi. Seluruh pekerjaan Argon2id melewati satu process-local gate
berbasis context yang disediakan melalui dependency injection, dengan default
empat operasi concurrent, agar banjir autentikasi tidak dapat melipatgandakan
memori derivasi 64 MiB tanpa batas. Bootstrap owner memakai gate yang sama. Hash
password hanya tersedia dalam tipe credential autentikasi yang tidak diekspor;
model pengguna dan identitas terautentikasi normal tidak dapat membawa data
credential.

Autentikasi memakai opaque server-side session, bukan JWT. Setiap login
menghasilkan token acak kriptografis 256-bit dengan `crypto/rand`. Token mentah
dalam base64 URL-safe tanpa padding hanya dikembalikan kepada client dan tidak
pernah dicatat dalam log. SQLite hanya menyimpan `SHA-256(raw token)`. Request
yang dilindungi mengirim credential sebagai
`Authorization: Bearer <opaque-token>`.

Setiap login atau perangkat memiliki baris session sendiri. Session memiliki
masa berlaku awal absolut selama 30 hari dan dapat dicabut secara independen.
Logout hanya mencabut session saat ini; endpoint pengelolaan session dapat
mencabut session perangkat lain tanpa mencabut semua session milik pengguna.
Autentikasi awal tidak memakai refresh token atau sliding expiration.

Request yang dilindungi memvalidasi bahwa session tersedia, belum dicabut atau
kedaluwarsa, dan dimiliki oleh pengguna aktif yang masih ada. Keberhasilan dan
kegagalan autentikasi, logout, pencabutan session, serta event akun sensitif
lainnya diaudit tanpa mencatat password, hash password, atau raw session token.

Ketika state keamanan dan audit memakai SQLite yang sama, pembuatan owner awal,
pembuatan session yang berhasil, logout, dan pencabutan session menambahkan
baris audit dalam transaction eksplisit yang sama dengan perubahan state.
Keduanya di-commit bersama atau tidak sama sekali. Pembatasan kegagalan login
tetap process-local dan memakai bucket username+peer yang terbatas serta bucket
username spray peer-wide yang lebih longgar.

Kebijakan username+peer memblokir setelah 8 kegagalan dalam 5 menit, sedangkan
kebijakan spray peer-wide memblokir setelah 40 kegagalan; setiap block berlaku
selama 15 menit. Request yang melewati threshold mencatat `login_failure` biasa
dan satu transition event `login_rate_limited` yang hanya mengidentifikasi jenis
bucket. Request yang ditolak oleh bucket yang sudah diblokir tidak melakukan
pekerjaan Argon2 dan tidak menambahkan baris audit. Transisi concurrent dicegah
oleh mutex limiter. Jika persistensi audit transisi gagal, login tetap tidak
berhasil, bucket process-local tetap diblokir, dan penolakan berikutnya tidak
mencoba ulang penulisan tanpa batas. Restart secara sengaja memulai window
limiter process-local yang baru.

Handler login publik menerima paling banyak 64 request concurrent dan
menetapkan deadline pembacaan body selama 15 detik sebelum membaca body JSON
yang dibatasi 64 KiB. Kontrol khusus route ini tidak menetapkan timeout global
pada transfer streaming.

`GET /api/v1/health` tetap tidak memerlukan autentikasi pada level aplikasi.
Keterjangkauan jaringannya tetap dibatasi oleh batas Tailscale privat.

## Konsekuensi

- Kompromi terhadap keterjangkauan perangkat Tailscale saja tidak memberikan
  identitas atau permission aplikasi.
- Pengungkapan database tidak langsung memperlihatkan plaintext password atau
  bearer session token, meskipun hash password dan metadata session tetap
  sensitif terhadap keamanan.
- Perangkat yang hilang atau tidak lagi dipercaya dapat dicabut secara
  independen, dan satu pengguna dapat memakai beberapa perangkat secara
  concurrent.
- Server harus melakukan lookup ke database untuk request yang dilindungi dan
  menerapkan rule masa berlaku, pencabutan, pengguna nonaktif, serta otorisasi.
- Concurrency Argon2 dibatasi per proses, sehingga deployment dengan beberapa
  proses memerlukan desain kontrol resource bersama yang terpisah.
- Client harus melindungi raw token dalam secure storage yang sesuai dengan
  platform.
- Perilaku session awal tetap sederhana dan dapat diprediksi, dengan konsekuensi
  login baru diperlukan setelah masa berlaku absolut berakhir.
- Riwayat autentikasi append-only tetap memerlukan pemantauan kapasitas
  production dan kebijakan retention atau archive yang eksplisit; aplikasi
  tidak mengubah atau memangkas baris audit secara diam-diam.

## Alternatif yang Ditolak

- **Memperlakukan identitas Tailscale sebagai autentikasi aplikasi:** ditolak
  karena otorisasi jaringan atau perangkat dan otorisasi akun aplikasi
  merupakan batas keamanan yang berbeda.
- **JWT access token:** ditolak karena pencabutan langsung per perangkat tetap
  memerlukan server-side state dan menambah kompleksitas signing key serta
  claim lifecycle tanpa manfaat awal.
- **Menyimpan raw session token:** ditolak karena pengungkapan database akan
  langsung mengekspos bearer credential yang masih aktif.
- **Memakai satu session bersama atau global:** ditolak karena pencabutan satu
  perangkat akan menyebabkan semua perangkat logout tanpa kebutuhan.
- **Refresh token atau sliding expiration:** ditolak pada fase awal karena
  menambah kompleksitas session lifecycle yang dapat dihindari.
- **Self-registration publik:** ditolak karena deployment privat awal memakai
  bootstrap owner yang dikendalikan administrator dan tidak memerlukan
  permukaan pembuatan akun yang terbuka.
