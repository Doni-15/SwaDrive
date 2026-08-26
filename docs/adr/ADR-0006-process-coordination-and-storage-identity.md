# ADR-0006: Mengoordinasikan Satu Proses dan Memverifikasi Identitas Storage Root

- **Status:** Diterima
- **Cakupan:** Batas operasional backend `v1.0.0`

## Konteks

Backend `v1.0.0` memakai process-local mutation lock dan resource gate,
sedangkan SQLite menserialisasi writer. Menjalankan server kedua yang tidak
menyadari proses pertama atau command administrasi lokal terhadap database
yang sama dapat melewati jaminan process-local tersebut. Menjalankan service
pada fallback directory biasa ketika content disk yang dituju tidak tersedia
juga dapat menempatkan byte pengguna pada filesystem yang salah.

SQLite harus dapat membuat database, WAL, dan SHM dalam area state yang telah
dikonfigurasi. File lock di samping database dapat mengoordinasikan proses yang
bekerja sama, tetapi tidak dapat melindungi dari proses berbahaya dengan UID
yang sama atau pihak lain yang dapat mengganti entry dalam state directory yang
dapat ditulis.

## Keputusan

Server dan command administrasi lokal mengambil non-blocking flock yang
diturunkan dari path database kanonis. Mekanisme ini mencegah beberapa proses
SwaDrive yang didukung memakai database yang sama secara concurrent, termasuk
melalui alias symlink normal. Backend `v1.0.0` mendukung satu proses dan satu
pasangan database dan storage root. Alias database melalui hard link serta
database berbeda yang diarahkan ke satu storage root tidak didukung.

Sebelum membuka atau menginisialisasi subdirectory penyimpanan, storage manager
membuka root yang dikonfigurasi melalui `os.Root` dan mewajibkan
`.swadrive-volume` berupa regular file berukuran terbatas dengan nilai yang sama
persis dengan `SWADRIVE_STORAGE_VOLUME_ID`. Marker merupakan pemeriksaan
identitas aplikasi, bukan bukti bahwa HDD yang dituju telah ter-mount.

Deployment production harus menjaga storage root yang ter-mount dan marker
tetap dikendalikan administrator serta tidak dapat diganti oleh runtime service
account. Subdirectory `files/`, `uploads/`, dan `trash/` merupakan content
boundary yang dapat ditulis service. Area state harus tetap dapat ditulis
secukupnya untuk operasi database/WAL/SHM SQLite dan coordination lock di
sampingnya. Flock bukan batas keamanan terhadap writer berbahaya dengan UID
yang sama. Ownership, mode permission, urutan mount, dan pembatasan `systemd`
yang tepat merupakan tanggung jawab deployment. Batas tersebut harus
diverifikasi secara independen dari kontrol pada level source.

## Konsekuensi

- Proses server dan command administrasi yang bekerja sama segera gagal,
  alih-alih berbagi satu database sementara masing-masing memakai in-process
  lock yang independen.
- Marker volume yang salah, hilang, terlalu besar, malformed, atau bukan regular
  file mencegah inisialisasi storage sebelum subdirectory tersebut dibuat.
- Marker mendeteksi kesalahan konfigurasi atau fallback biasa, tetapi tidak
  dapat mendeteksi setiap bind mount atau substitusi filesystem berprivilege.
- Identitas storage yang dikendalikan administrator dan path penyimpanan yang
  dapat ditulis service memerlukan batas ownership terpisah saat deployment.
- `root` atau proses dengan UID yang sama dan bersifat berbahaya tetap berada
  di luar model koordinasi ini; ownership OS dan isolasi service harus
  menyediakan batas keamanan tersebut.

## Alternatif yang Ditolak

- **Memperlakukan marker sebagai bukti mount:** ditolak karena pathname dan
  marker tidak dapat menetapkan kondisi mount dan ownership kernel secara
  lengkap.
- **Memperlakukan flock sebagai isolasi proses berbahaya:** ditolak karena pihak
  yang dapat mengganti state file milik UID yang sama dapat melewati koordinasi
  berbasis file.
- **Mendukung beberapa proses server pada backend `v1.0.0`:** ditolak karena
  lock upload dan file process-local serta perilaku writer SQLite memerlukan
  desain koordinasi bersama yang terpisah.
