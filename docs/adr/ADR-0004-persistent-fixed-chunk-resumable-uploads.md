# ADR-0004: Menggunakan Resumable Upload Berbasis Fixed Chunk yang Persisten

- **Status:** Diterima
- **Cakupan:** Arsitektur backend `v1.0.0`

## Konteks

SwaDrive harus dapat melakukan upload file kecil maupun file yang jauh lebih
besar daripada memori yang tersedia melalui jaringan seluler yang tidak stabil.
Transfer yang terputus harus dapat dilanjutkan setelah kegagalan request atau
restart server tanpa mengekspos file parsial dalam file tree normal. Finalisasi
harus menolak isi yang hilang atau rusak dan tidak boleh mengganti file yang
sudah ada pada path tujuan secara diam-diam.

Mengadopsi route terpisah untuk file kecil akan menduplikasi rule transfer dan
integritas. Menyimpan satu file sementara per chunk juga akan memperbanyak objek
filesystem dan memerlukan tahap assembly atau penyalinan kedua saat finalisasi.

## Keputusan

Gunakan satu protocol resumable upload berbasis server-side session untuk semua
ukuran file. Pembuatan upload menyimpan logical target path, ukuran total,
ukuran fixed chunk, jumlah total chunk, masa berlaku, status, dan SHA-256
keseluruhan file yang opsional. Ukuran chunk yang diizinkan adalah 1, 2, 4, 8,
dan 16 MiB; default-nya 4 MiB dan maksimum 16 MiB.

Setiap upload memiliki satu file `.part` dengan nama acak di bawah root internal
`uploads/`. Chunk ditulis pada offset deterministik. SQLite mencatat index,
offset, ukuran, SHA-256, dan waktu penerimaan setiap chunk yang telah
diverifikasi menggunakan composite primary key. Server memverifikasi panjang
tepat yang diharapkan dan checksum chunk dari client sebelum mencatat progress.
Retry pada index yang sama dengan panjang dan checksum yang sama berhasil
secara idempotent; isi yang berkonflik ditolak. Body dialirkan melalui
buffer 64 KiB ke offset deterministik; satu chunk lengkap tidak pernah
ditampung dalam RAM. Index berbeda dapat memakai akses per-upload bersama dan
menulis secara concurrent. Gate sementara per index menserialisasi retry pada
index yang sama, sedangkan finalisasi, pembatalan, dan cleanup mengambil akses
per-upload eksklusif serta menunggu chunk yang masih berjalan.

Finalisasi diserialisasi per upload. Proses ini mensyaratkan semua chunk,
memverifikasi ukuran file `.part` dan SHA-256 keseluruhan file yang opsional,
melakukan sync file, mengubah status database menjadi `finalizing`, lalu secara
atomik melakukan rename pada file `.part` ke dalam `files/` pada storage root
yang sama. Upload kemudian ditandai `completed` dan baris metadata index aktif
ditambahkan. Status perantara memungkinkan service yang telah restart
merekonsiliasi rename yang berhasil sebelum pembaruan status dan index
terakhir. File yang sudah ada pada path tujuan tidak pernah ditimpa. Status
`completed`, penambahan file index, dan audit event finalisasi di-commit dalam
satu transaction SQLite; kegagalan
membiarkan state `finalizing` yang durable untuk recovery dan menandai
ketidaksesuaian metadata yang diketahui sebagai unhealthy.

Sebelum melayani HTTP, startup hanya memeriksa upload `finalizing` yang telah
diketahui dalam batch terbatas. Jika part tersedia dan path tujuan belum ada,
status dikembalikan ke pending. Jika part tidak ada dan path tujuan tersedia,
index dikonfirmasi dan upload diselesaikan beserta audit. Jika keduanya tersedia
atau keduanya tidak tersedia, startup dihentikan. Ini merupakan targeted
reconciliation, bukan content tree scan. Satu process-local mutation
coordinator yang dipakai bersama mencegah operasi move atau trash secara
concurrent berselang di antara publikasi dan commit index.

Upload yang belum selesai kedaluwarsa setelah 24 jam. Periodic worker yang dapat
dibatalkan menghapus file `.part` yang kedaluwarsa dan menandai upload session
dalam database sebagai expired. Saat pembuatan, ruang untuk upload lengkap
beserta reserve yang dapat dikonfigurasi diperiksa; penerimaan chunk memeriksa
ulang ruang tersebut. Default reserve adalah 1 GiB. Pemeriksaan ini bersifat
advisory, bukan reservation. Secara default, server membatasi stream chunk
concurrent hingga delapan dan upload aktif hingga 100 per pengguna. Setiap
upload dibatasi hingga 1,000,000 chunk. Dengan chunk terkecil, batas ini masih
mengizinkan sekitar 1 TiB. Pilihan chunk yang lebih besar memungkinkan file
yang lebih besar secara proporsional.

Pembuatan upload pasti memiliki window sempit filesystem-before-SQLite, tempat
file internal `.part` dapat tersedia tanpa baris upload. File tersebut tidak
pernah dipublikasikan atau terlihat melalui API metadata. Recovery merupakan
operasi offline eksplisit oleh administrator lokal: `reconcile-upload-parts`
terlebih dahulu melaporkan dry-run, hanya memindai directory `uploads/` yang
bersifat internal dalam batch terbatas, dan hanya mempertimbangkan nama `.part`
yang mengikuti format hex 128-bit dengan huruf lowercase secara ketat, berupa
regular file, lebih tua daripada umur minimum yang dikonfigurasi, serta tidak
tercatat dalam SQLite. Penghapusan memerlukan `-apply`. Command tersebut tidak
pernah membaca isi file atau mencetak nama internal maupun host path. Jika
batas scan terlampaui, command gagal tanpa melakukan penghapusan parsial.

Jika pembatalan atau kedaluwarsa menghapus part yang diketahui dan proses
berhenti sebelum memperbarui SQLite, cleanup setelah restart memperlakukan part
yang hilang sebagai sudah dihapus dan secara durable menandai baris pending yang
diketahui sebagai expired. Proses ini tidak pernah membuat entry index.

Download memakai perilaku byte Range HTTP standar, bukan protocol download
berbasis session yang terpisah.

## Konsekuensi

- File kecil dan besar memakai satu path integritas dan publikasi.
- Progress upload bertahan melewati restart proses tanpa menyimpan isi file
  dalam SQLite atau mempertahankan file terpisah untuk setiap chunk.
- Memori penyalinan chunk sekitar 64 KiB per request aktif, bukan proporsional
  terhadap ukuran chunk 1-16 MiB yang dipilih.
- Isi parsial tetap berada di luar API listing, pencarian, dan download
  normal sampai publikasi atomik berhasil.
- Pembacaan status upload normal dan upload parsial tetap berada pada metadata
  plane SQLite dan tidak pernah memerlukan HDD tree scan.
- Client harus menyimpan upload ID, ukuran chunk yang dipilih, dan
  source-file identity yang diperlukan untuk melanjutkan upload dengan aman.
- Sinkronisasi per upload bersifat process-local; backend `v1.0.0` mengasumsikan
  satu proses backend memiliki database dan storage root yang dikonfigurasi.
- Pemeriksaan preflight mengurangi risiko kehabisan disk, tetapi bukan quota
  reservation; upload concurrent dan writer lain dapat memakai ruang di antara
  pemeriksaan.

## Alternatif yang Ditolak

- **Direct upload terpisah untuk file kecil:** ditolak karena menduplikasi
  perilaku validasi, otorisasi, audit, conflict, dan finalisasi.
- **JWT atau progress upload hanya pada client:** ditolak karena progress yang
  aman terhadap restart dan state integritas otoritatif harus berada pada
  server.
- **Satu file sementara per chunk:** ditolak karena menambah objek filesystem
  dan memerlukan pekerjaan assembly saat finalisasi.
- **Fixed chunk 8 MiB untuk setiap client:** ditolak karena koneksi seluler yang
  tidak stabil memperoleh manfaat dari chunk 1-4 MiB, sedangkan client dengan
  koneksi stabil dapat memilih 8 atau 16 MiB.
- **Overwrite path tujuan secara diam-diam:** ditolak karena upload tidak boleh
  menghancurkan file yang sudah ada tanpa kebijakan overwrite eksplisit di masa
  mendatang.
- **TUS pada backend `v1.0.0`:** ditolak karena menambah kompleksitas protocol
  atau framework yang tidak diperlukan untuk kebutuhan privat satu server saat
  ini; keputusan ini dapat ditinjau ulang melalui ADR pengganti jika kebutuhan
  interoperability berubah.
