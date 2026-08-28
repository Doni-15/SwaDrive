# Client Flutter SwaDrive

Client Flutter untuk Linux dan Android masih berupa scaffold. Fitur produk
belum diimplementasikan; status proyek dan arsitektur tersedia dalam
[README repository](../README.md).

## Batas Kontrak v1.0.2

Implementasi client berikutnya harus membedakan kegagalan koneksi dari respons
API yang valid:

| Kondisi | Pesan untuk pengguna |
| --- | --- |
| Jaringan, Tailscale, atau API tidak dapat dijangkau | `Server tidak dapat dijangkau` |
| HTTP 503 dengan `error.code=storage_unavailable` | `Penyimpanan server tidak tersedia` |
| HTTP 503 dengan `error.code=server_busy` | `Server sedang sibuk` |
| HTTP 503 dengan `error.code=metadata_unavailable` | `Metadata server belum tersedia` |

`GET /api/v1/health` tetap memakai HTTP 200 ketika proses sehat dan storage
tidak tersedia, dengan respons
`{"status":"degraded","storage":"unavailable"}`. Tidak ada lapisan jaringan
atau UI baru yang ditambahkan dalam patch ini.

`GET /api/v1/ready` mengembalikan HTTP 200 bila database dan metadata plane
siap; HTTP 503 memakai error envelope dengan `error.code=not_ready`. Setelah
upload selesai, response `whole_sha256` selalu berisi SHA-256 hasil verifikasi
server, termasuk ketika create request tidak mengirim checksum keseluruhan.

Scaffold saat ini belum memanggil endpoint Go apa pun. Batas kontrak yang akan
diimplementasikan berada pada bearer `Authorization`, JSON/error envelope,
status storage/metadata, upload fixed-chunk beserta offset/checksum, serta
download streaming/Range. Backend tetap menjadi authority autentikasi,
otorisasi, ownership, validation, dan filesystem confinement.
