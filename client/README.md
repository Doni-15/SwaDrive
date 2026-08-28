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
| HTTP 408 dengan `error.code=request_timeout` | `Permintaan melewati batas waktu server` |
| HTTP 408 dengan `error.code=request_cancelled` | `Permintaan dibatalkan` |

`GET /api/v1/health` tetap memakai HTTP 200 ketika proses sehat dan storage
tidak tersedia, dengan respons
`{"status":"degraded","storage":"unavailable"}`. Tidak ada lapisan jaringan
atau UI baru yang ditambahkan dalam patch ini.

`GET /api/v1/ready` mengembalikan HTTP 200 bila database dan metadata plane
siap; HTTP 503 memakai error envelope dengan `error.code=not_ready`. Setelah
upload selesai, response `whole_sha256` selalu berisi SHA-256 hasil verifikasi
server, termasuk ketika create request tidak mengirim checksum keseluruhan.
Body JSON atau upload yang melewati deadline dan masih dapat menerima response
memakai `request_timeout`; cancellation context memakai `request_cancelled`.
Payload JSON yang malformed tetap memakai `invalid_json`, bukan timeout.
Implementasi transfer client harus memberi budget sedikit di atas 5 menit 30
detik untuk chunk/completion agar selaras dengan operation dan response budget
backend, sambil tetap menyediakan cancellation yang nyata.

Scaffold saat ini belum memanggil endpoint Go apa pun. Batas kontrak yang akan
diimplementasikan berada pada bearer `Authorization`, JSON/error envelope,
status storage/metadata, upload fixed-chunk beserta offset/checksum, serta
download streaming/Range. Backend tetap menjadi authority autentikasi,
otorisasi, ownership, validation, dan filesystem confinement.
