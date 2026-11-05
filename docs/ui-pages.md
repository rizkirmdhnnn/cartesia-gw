# Halaman dashboard

Console punya enam halaman, satu terlihat pada satu waktu. Menu kiri dan URL
(`#/…`) selalu sinkron: refresh, back/forward, dan link yang dibagikan mendarat di halaman yang sama.

| Hash | Menu | Isi |
|---|---|---|
| `#/overview` | Overview | KPI strip + **Traffic map** (client → gateway → key Cartesia, porsi 24 jam, key "berikutnya"). |
| `#/synthesize` | Synthesize | **Default saat login.** Kontrol sintesis di kiri, pustaka voice di kanan, pemutar, unduhan, cURL. |
| `#/history` | Riwayat | Tabel audit request (tabel scroll di dalam kartu, header sticky). |
| `#/keys` | Cartesia Keys | Kredensial upstream: tambah key, prioritas, aktif/nonaktif, reveal, pilihan strategi rotasi. |
| `#/clients` | Client Keys | Kredensial klien `dk_…`: buat, putar, nonaktifkan, hapus, kotak secret sekali-tampil. |
| `#/account` | Akun & Sesi | Ganti username/password, cabut semua sesi. |

**Halaman:** `#/foo` yang tidak dikenal jatuh ke `synthesize`. Tanpa hash, login mendarat di
`synthesize`; hash disimpan dengan `history.replaceState`, jadi tidak menambah riwayat setiap klik
menu.

**Pemuatan data.** Saat sesi aktif, `/api/overview`, `/api/requests`, `/api/keys`, `/api/routing`,
dan `/api/clients` dimuat sekali untuk mengisi badge dan angka ringkas. Panel halaman lain memuat
datanya saat halaman dibuka (voice library, akun), lalu ticker 30 detik menyegarkan angka ringkas dan
peta trafik selama tab terlihat.

**Kenapa hash, bukan path.** Binary menyajikan satu shell di `/`; hash tidak pernah sampai ke server,
jadi tidak perlu rewrite atau fallback route, dan reverse proxy apa pun bisa meneruskannya apa adanya.

Peta trafik menggambar ulang garisnya (`requestAnimationFrame`) setiap kali halaman Overview tampil
atau jendela berubah ukuran — SVGS tanpa `width`/`height` bisa jatuh ke viewport 300×150 di beberapa
mesin dan menggambar garis di luar kotak, jadi ukurannya juga dikunci atribut dan CSS.
