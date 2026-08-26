# ADR-0005: Menggunakan Metadata Plane pada NVMe dan Content Plane pada HDD

- **Status:** Diterima
- **Cakupan:** Arsitektur storage backend `v1.0.0`

## Konteks

Target production SwaDrive memisahkan storage NVMe yang cepat dan selalu aktif
dari HDD berkapasitas besar. Operasi seperti browsing directory, lookup
metadata, pencarian nama atau path file, status upload, listing trash, session,
dan riwayat audit merupakan pembacaan kecil yang sering dilakukan. Melakukan
tree walk atau stat pada filesystem pengguna untuk operasi tersebut akan
membangunkan HDD dan mengubah navigasi biasa menjadi I/O acak pada data disk.

Byte pada filesystem tetap otoritatif, sedangkan SQLite tidak dapat melakukan
commit secara atomik bersama rename filesystem. Karena itu, tampilan metadata
harus dapat dibangun ulang tanpa membuang tampilan lengkap terakhir jika
rebuild oleh administrator terputus.

## Keputusan

Gunakan `/var/lib/personalcloud` pada NVMe sebagai control dan metadata plane.
SQLite menyimpan state autentikasi dan session, state audit, state operasional
upload dan trash, serta index `file_entries` yang hanya berisi metadata. Entry
memuat logical path, nilai parent/name/search, jenis, ukuran, timestamp,
checksum opsional hanya jika sudah diketahui, dan asosiasi trash opsional.
Entry tidak pernah memuat isi file atau physical path `/srv`.

Gunakan `/srv/personalcloud` pada HDD sebagai content plane. Directory `files/`,
`uploads/`, dan `trash/` harus berada pada satu filesystem agar publikasi
upload, trash, dan restore dapat memakai semantik rename dalam filesystem yang
sama tanpa overwrite. Storage manager membandingkan filesystem device ID saat
dibuka dan menolak konfigurasi yang terpisah.

Invariant yang berlaku: **metadata plane tidak boleh membangunkan data disk**.
Request listing normal, metadata, pencarian, listing trash, dan status upload
hanya memakai SQLite. Download terlebih dahulu mencari metadata SQLite, lalu
membuka file yang diminta. Upload, mkdir, move, trash, restore, recovery
terbatas atas objek terputus yang sudah diketahui, dan reindex eksplisit oleh
administrator dapat mengakses HDD.

Metadata index memakai generation. Reindex membuat generation `building`,
menelusuri `files/` dan objek trash yang diketahui secara berurutan di luar
transaction, melakukan insert dalam transaction singkat dan terbatas,
memvalidasi jumlah baris, lalu mengganti singleton active-generation pointer
secara atomik. Active generation lama kemudian menjadi obsolete dan dibersihkan
dalam batch terbatas. Kegagalan sebelum pergantian membuat generation
sebelumnya tetap aktif. Reindex merupakan operasi lokal `swadrive-admin`, tidak
memiliki HTTP endpoint, mengecualikan `uploads/`, serta tidak pernah dijalankan
oleh startup atau browsing normal.

Mutasi normal memelihara active generation secara inkremental. Mkdir menambahkan
satu baris; move memperbarui path root dan turunannya; trash mengasosiasikan dan
menyembunyikan subtree yang dipertahankan; restore menghapus asosiasi tersebut;
finalisasi upload menambahkan baris file hanya setelah publikasi. Setelah tahap
filesystem, setiap operasi menggabungkan perubahan index SQLite, state
operasional, dan audit log dalam transaction eksplisit. Satu process-local
mutation coordinator yang dipakai bersama mencegah operasi file yang terlihat
dan publikasi upload berselang melewati batas tersebut.

Karena filesystem dan SQLite tidak dapat memakai satu transaction, compensation
yang aman dicoba ketika tahap SQLite gagal. Jika compensation juga gagal, index
ditandai unhealthy dan pembacaan metadata menjadi fail-closed sampai reindex
eksplisit dilakukan. Startup reconciliation hanya memeriksa record trash yang
durable dan upload `finalizing` dalam batch terbatas; proses ini tidak pernah
melakukan scan HDD secara umum.

Mkdir dan move juga menyimpan mutation intent kecil yang menandai kondisi
unhealthy sebelum tahap filesystem. Transaction index dan audit log yang
berhasil menghapus intent yang sesuai; kegagalan yang berhasil dikompensasi
secara aman menghapusnya setelah
itu. Proses yang berhenti pada salah satu sisi batas filesystem dan SQLite
membiarkan intent tetap durable. Setelah intent di-commit, finalisasi SQLite dan
penghapusan intent memakai context singkat serta terbatas yang terpisah dari
pembatalan request. Karena itu, client yang terputus saja tidak dapat
meninggalkan operasi yang seharusnya sudah diperbaiki; kegagalan pada internal
repair yang terbatas tetap membiarkan index unhealthy dan fail-closed. Targeted
reconciliation untuk trash dan upload berjalan terlebih dahulu, lalu startup
mensyaratkan index yang healthy sebelum melayani HTTP. Intent mkdir atau move
yang belum terselesaikan membuat startup fail-closed sampai administrator
menjalankan reindex berbasis generation secara eksplisit. Intent mendeteksi
ketidaksesuaian tanpa memindai HDD saat startup.

Koordinasi satu proses dan pengikatan identitas storage root diatur oleh
[ADR-0006](ADR-0006-process-coordination-and-storage-identity.md).

Backend `v1.0.0` hanya mengindeks metadata. Backend tidak mengekstrak teks
dokumen, EXIF, thumbnail, frame media, embedding, wajah, atau hash keseluruhan
file di background. SQLite pure-Go yang disertakan menyediakan FTS5, tetapi
pencarian metadata berparameter biasa digunakan untuk menghindari index kedua
yang harus disinkronkan pada produk awal.

## Konsekuensi

- Traffic browsing, pencarian, dan status normal tetap dilayani oleh NVMe/SQLite
  dan tidak membangunkan HDD.
- Reindex dapat membangunkan dan menelusuri HDD, tetapi hanya sebagai tindakan
  eksplisit dan khusus oleh administrator lokal.
- Crash saat membangun generation tidak dapat mengganti tampilan metadata aktif
  lengkap terakhir dengan tampilan parsial.
- Perubahan langsung di luar aplikasi terhadap `/srv/personalcloud/files` tidak
  terlihat sampai reindex; mutasi aplikasi harus melalui path service atau
  repository.
- Contains-search dapat memindai generation SQLite aktif meskipun hasil dan
  page dibatasi.
- Lock dan state kesehatan index mengasumsikan satu proses backend memiliki
  setiap database dan storage root. Kepemilikan storage oleh beberapa proses
  memerlukan desain berikutnya.
- Index SQLite merupakan derived state yang dapat diperbaiki, tetapi
  ketidaksesuaian sementara antara filesystem dan index tetap dapat terjadi
  akibat crash; backend mendeteksi state yang diketahui, melakukan compensation
  jika aman, dan menjadi fail-closed alih-alih mengklaim konsistensi sempurna.
- Durable intent mkdir atau move yang belum terselesaikan membuat startup dan
  metadata tidak tersedia sampai reindex eksplisit; availability dikorbankan
  agar metadata yang diketahui stale tidak disajikan.

## Alternatif yang Ditolak

- **Menelusuri HDD untuk setiap listing atau pencarian:** ditolak karena
  browsing akan membangunkan dan memindai content disk secara acak.
- **Menghapus dan membangun ulang index saat ini di tempat:** ditolak karena
  crash akan membuat satu-satunya tampilan metadata menjadi parsial.
- **Menjalankan reindex otomatis pada setiap startup:** ditolak karena restart
  biasa tidak boleh menyebabkan full HDD scan.
- **Menyimpan isi file atau extracted content dalam SQLite:** ditolak
  karena NVMe merupakan control/metadata plane, bukan content store duplikat.
- **Langsung menambahkan FTS5 beserta synchronization trigger:** ditolak karena
  pencarian metadata biasa memadai untuk `v1.0.0` dan memiliki lebih sedikit
  derived state.
