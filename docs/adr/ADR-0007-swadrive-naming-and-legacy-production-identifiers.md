# ADR-0007: Penamaan SwaDrive dan Identifier Lama di Production

- **Status:** Diterima
- **Cakupan:** Penamaan proyek dan kompatibilitas deployment

## Konteks

SwaDrive merupakan nama proyek saat ini, tetapi sebagian identifier deployment
yang telah digunakan di production masih memakai nama lama `personalcloud`.
Identifier tersebut mencakup akun atau path yang terikat pada deployment
yang sudah berjalan dan tidak menunjukkan nama produk yang berbeda.

## Keputusan

Pertahankan identifier yang sudah terpasang di production apabila rename tidak
memberikan manfaat teknis yang sebanding dengan risiko migrasinya.

Gunakan `SwaDrive` sebagai nama produk dan `swadrive` untuk identifier baru,
kecuali kompatibilitas atau deployment yang sudah berjalan mengharuskan nama
lama. Identifier lama di production tidak di-rename hanya untuk menyeragamkan
tampilan dokumentasi.

## Konsekuensi

- Dokumentasi publik menggunakan nama SwaDrive.
- Beberapa path atau akun di production dapat tetap memakai `personalcloud`.
- Keberadaan kedua nama ini disengaja dan bukan typo.
- Migrasi penamaan terpisah hanya dilakukan jika ada alasan operasional yang
  nyata.

## Alternatif yang Ditolak

- Rename seluruh identifier di production sekarang ditolak karena menambah
  risiko migrasi tanpa manfaat teknis yang sebanding.
- Membiarkan dua nama tanpa dokumentasi keputusan ditolak karena membuat
  identifier legacy tampak seperti inkonsistensi yang tidak disengaja.
