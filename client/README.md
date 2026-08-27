# Client Flutter SwaDrive

Client Flutter untuk Linux dan Android masih berupa scaffold. Fitur produk
belum diimplementasikan; status proyek dan arsitektur tersedia dalam
[README repository](../README.md).

## Kontrak Error v1.0.1

Implementasi client berikutnya harus membedakan kegagalan koneksi dari respons
API yang valid:

| Kondisi | Pesan untuk pengguna |
| --- | --- |
| Jaringan, Tailscale, atau API tidak dapat dijangkau | `Server tidak dapat dijangkau` |
| HTTP 503 dengan `error.code=storage_unavailable` | `Penyimpanan server tidak tersedia` |
| HTTP 503 dengan `error.code=server_busy` | `Server sedang sibuk` |

`GET /api/v1/health` tetap memakai HTTP 200 ketika proses sehat dan storage
tidak tersedia, dengan respons
`{"status":"degraded","storage":"unavailable"}`. Tidak ada lapisan jaringan
atau UI baru yang ditambahkan dalam patch ini.
