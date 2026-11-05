# cartesia-gw

Gateway satu binary yang menaruh endpoint **OpenAI-compatible `/v1/audio/speech`** di depan API
text-to-speech Cartesia. Untuk pemakaian sendiri/internal: rotasi beberapa API key, cooldown saat
kunci kena limit, riwayat pemakaian, dan satu halaman dashboard.

Dibuat karena `9router-go` menyembunyikan Cartesia (`hidden: true`) — provider itu terdaftar di
katalog upstream tapi handler-nya tidak pernah ikut diport, sehingga permintaannya akan ditolak.
Alat ini tidak menyentuh `9router-go` sama sekali: ia hidup sendiri, hanya bicara Cartesia.

---

## Mulai cepat

```bash
make build
./bin/cartesia-gw
# buka http://127.0.0.1:20140/ → tab API keys → tempel key sk_car_…
```

Lalu panggil seperti endpoint OpenAI biasa:

```bash
curl -X POST http://127.0.0.1:20140/v1/audio/speech \
  -H "Content-Type: application/json" \
  -d '{"model":"sonic-3","input":"Halo dunia","voice":"<voice-uuid>"}' \
  --output speech.mp3
```

Voice ID diambil dari dashboard (tombol **Muat voice**) atau dari
[Voice Library](https://play.cartesia.ai/voices).

---

## Endpoint

| Method | Path | Keterangan |
|---|---|---|
| `POST` | `/v1/audio/speech` | Sintesis. Body mengikuti bentuk OpenAI. |
| `GET` | `/v1/models` | Katalog model Sonic. |
| `GET` | `/v1/audio/voices` | Daftar voice Cartesia (`?q=`, `?language=`, `?limit=`, `?starting_after=`). |
| `GET` | `/v1/audio/voices/{id}/preview` | Memutar contoh suara voice tersebut. |
| `GET` | `/healthz` | Status + jumlah key. Tidak butuh token. |
| `GET` | `/` | Dashboard. |
| `GET`/`POST`/`PATCH`/`DELETE` | `/api/…` | API dashboard (key, riwayat, overview). |

Tidak ada jalur native Cartesia (`/tts/sse`, `/tts/websocket`, `/infill`). Yang tidak dipakai tidak
dibuka.

---

## Kontrak

**Body `POST /v1/audio/speech`**

| Field | Wajib | Catatan |
|---|---|---|
| `model` | tidak | `sonic-3.6`, `sonic-3.5`, `sonic-3`, `sonic-latest`. Default `sonic-3`. Prefix `cartesia/` diterima dan dibuang. |
| `input` | ya | Teks. |
| `voice` | ya | Voice ID Cartesia. **Tidak ada default** — mengirim suara yang tidak diminta lebih buruk daripada error. |
| `response_format` | tidak | `mp3` (default), `wav`, `pcm`. `opus`/`aac`/`flac` **ditolak 400**, bukan diam-diam diganti mp3. |
| `speed` | tidak | `0.6`–`1.5`. Di luar itu ditolak, bukan dijepit diam-diam. |
| `volume` | tidak | `0.5`–`2.0`. Ditolak kalau di luar rentang, sama seperti `speed`. |
| `emotion` | tidak | Dari 58 nilai resmi Cartesia (`calm`, `excited`, `sad`, …). Nilai di luar daftar ditolak dengan pesan yang menyebut jumlahnya. Hanya berpengaruh pada `sonic-3` ke atas. |
| `language` / `locale` | tidak | Kirim salah satu, jangan keduanya (Cartesia menolak pasangan itu). |

**`?response_format=json`** pada query memilih *envelope*, bukan container: balasannya
`{"audio":"<base64>","format":"mp3"}`. Ini konvensi yang sudah dipakai klien 9router, jadi klien
lama tidak perlu diubah.

**Bentuk yang dikirim ke Cartesia** dibangun ulang, bukan diteruskan:

```json
{
  "model_id": "sonic-3",
  "transcript": "Halo dunia",
  "voice": "<voice-uuid>",
  "output_format": {"container": "mp3", "sample_rate": 44100, "bit_rate": 128000},
  "generation_config": {"speed": 1.2, "volume": 1.4, "emotion": "calm"}
}
```

`generation_config` hanya muncul kalau Anda mengisi salah satu kontrolnya; kalau tidak, model memakai
default-nya sendiri.

plus header `Authorization: Bearer sk_car_…` dan `Cartesia-Version`.

**Streaming.** Untuk `mp3`/`wav`/`pcm`, body upstream diteruskan apa adanya sambil mengalir, jadi
audio mulai berbunyi sebelum Cartesia selesai. Buffer hanya dipakai untuk envelope `json`, karena
base64 butuh seluruh byte.

---

## Seberapa setia ke Cartesia?

Tidak semuanya klon. Ini pemetaannya, supaya tidak ada yang dikira passthrough padahal bukan:

| Di sini | Padanan Cartesia | Sifat |
|---|---|---|
| `POST /v1/audio/speech` | `POST /tts/bytes` | **Diadaptasi.** Byte audionya identik; bentuk request dan envelope-nya milik OpenAI. |
| `GET /v1/audio/voices` | `GET /voices` | **Passthrough.** Objek voice diteruskan apa adanya, termasuk field yang alat ini tidak pakai. |
| `GET /v1/audio/voices/{id}/preview` | `preview_file_url` | **Diadaptasi.** Server yang mengambil berkasnya, karena URL itu butuh header Authorization. |
| `GET /v1/models` | tidak ada | Milik alat ini. Cartesia tidak punya endpoint katalog model. |
| Body error | bentuk error Cartesia | **Diganti** dengan envelope `{"error":{"message","type"}}`. |

**Yang berbeda pada request.** Bentuk yang diterima adalah milik OpenAI, bukan Cartesia: `input` →
`transcript`, `voice` berupa string (bukan objek), dan `response_format` string (`opus`/`aac`/`flac`
ditolak) → objek `output_format` dengan `container`, `sample_rate`, dan `bit_rate`/`encoding` yang
wajib. Query `?response_format=json` juga bukan milik Cartesia — itu konvensi 9router.

**Yang berbeda pada response suara.** Byte-nya sama; yang dipetakan hanya `Content-Type`
(`audio/mpeg`, `audio/wav`, `audio/pcm`). `Content-Length` tidak dikirim (respons mengalir/chunked),
dan header lain dari Cartesia — termasuk request id dan header rate limit — tidak diteruskan.

**Yang berbeda pada status.** `400`, `402`, `404`, `422`, dan `429` diteruskan apa adanya. `401` dan
`403` **tidak**: itu masalah kredensial di sisi gateway, bukan di sisi klien, jadi jadi `502`. `5xx`
dan kegagalan jaringan juga `502`.

**Yang belum diverifikasi.** Skema error Cartesia sendiri belum pernah saya lihat langsung —
dokumentasinya di balik login dan saya belum punya key. Karena body error-nya memang diganti dengan
envelope sendiri, yang hilang adalah struktur aslinya; teks pesannya tetap diteruskan utuh di dalam
`message`, dan tersimpan juga di kolom Error pada tabel riwayat.

---

## Login dan client API keys

Dua kredensial berbeda, dan itu disengaja:

| Kredensial | Untuk apa | Disimpan di |
|---|---|---|
| **Akun operator** (username + password) | Membuka dashboard: kelola Cartesia keys, buat client key, ubah rotasi. | bcrypt di SQLite; sesi berupa cookie `HttpOnly` yang hanya menyimpan hash-nya di server. |
| **Client API key** (`dk_…`) | Memanggil `/v1/*`: sintesis, daftar model, daftar voice, preview. | Hash SHA-256 di SQLite; secret asli hanya ditampilkan sekali saat dibuat. |

**Kunjungan pertama** menampilkan layar login dengan akun default **`admin` / `admin`**. Itu bukan
akun kerja: setelah masuk, console mengunci diri ke satu layar **Ganti password** dan API menjawab
`403` sampai password diganti (minimal 12 karakter, tidak boleh sama dengan `admin` atau username).
Mengganti password mengeluarkan semua sesi, lalu Anda masuk lagi dengan password baru. Akun default
dibuat otomatis saat database pertama kali dibuka, jadi tidak ada lagi formulir "buat akun".

**Lupa password?** Jalankan satu perintah di host, lalu masuk lagi dengan `admin`/`admin` dan ganti:

```bash
# Docker
docker exec cartesia-gw-cartesia-gw-1 /cartesia-gw -db /data/data.sqlite -reset-password
# binary biasa
./cartesia-gw -db ~/.cartesia-gw/data.sqlite -reset-password
```

Perintah itu mengembalikan login ke `admin`/`admin`, menandai wajib-ganti lagi, dan mencabut semua
sesi. Ia hanya bisa dipakai dari host (bukan lewat HTTP).

Sebelum ada akun sama sekali (mis. `-reset-password` di jalankan pada DB kosong), hanya **loopback**
yang boleh memanggil `/v1`; begitu akun atau client key pertama ada, semua pemanggil wajib memakai
kredensial, termasuk dari loopback.

**Client key** dibuat di panel **Client Keys**: beri nama, pilih masa berlaku (atau tanpa batas).
Secret tampil sekali di kotak hijau — salin saat itu juga. Tersedia **putar** (secret baru, key lama
langsung mati), **nonaktifkan** (berlaku pada request berikutnya), dan **hapus**. Tabel hanya
menampilkan prefix (`dk_WbDxVPuIi…`) dan jumlah request.

**Akun & sesi** ada di panel paling bawah: ganti username atau password (password sekarang wajib
diisi), atau **Keluarkan semua sesi**. Mengganti password **menghapus semua sesi** — termasuk browser
yang sedang dipakai — lalu Anda masuk lagi. Kebijakan ini dipilih karena satu akun operator, satu
kebiasaan buruk: kalau password bocor, sesi di perangkat lain tidak boleh ikut bertahan.

**Proteksi di lapisan HTTP:**

- Cookie sesi: `HttpOnly`, `SameSite=Strict`, `Secure` bila request datang via TLS/`X-Forwarded-Proto`
  dari loopback (yaitu reverse proxy Anda).
- Setiap penulisan (`POST`/`PATCH`/`DELETE`) lewat cookie wajib membawa header `X-Requested-With:
  cartesia-gw`. Form di origin lain tidak bisa menambah header ini tanpa preflight CORS yang tidak
  pernah diberikan — itu pengganti token CSRF.
- Login dibatasi 10 percobaan gagal per alamat per 15 menit. bcrypt cost 12 (~250 ms) membuat setiap
  percobaan tetap mahal.
- `CARTESIA_GW_TOKEN` masih diterima: saat akun atau client key pertama dibuat, nilainya diimpor sekali
  sebagai client key bernama *imported dari CARTESIA_GW_TOKEN*, jadi skrip yang sudah memakai token lama
  tetap jalan. Setelah itu token itu boleh dihapus dari `.env`.

**Upgrade dari versi sebelumnya.** Instalasi lama tidak punya akun; build ini membuatnya saat start
(`admin`/`admin`) dan langsung meminta ganti password. Token `CARTESIA_GW_TOKEN` yang sudah ada
diimpor sekali sebagai client key, jadi skrip lama tetap jalan.

---

## Traffic map

Panel di atas workspace adalah peta trafik 24 jam: **client keys** di kiri, **Cartesia keys** di
kanan, gateway di tengah. Satu request ke `GET /api/routing` mengisi semuanya, jadi kedua sisi selalu
menggambarkan jendela waktu dan keadaan picker yang sama.

- **Ketebalan garis** sebanding dengan porsi request 24 jam key/client itu (dalam akar kuadrat, supaya
  key kecil tetap terlihat). Garis yang berhenti bergerak berarti key itu sedang cooldown/nonaktif.
- **Warna titik:** hijau siap, oranye cooldown, abu-abu nonaktif atau client key kedaluwarsa.
- **Ring "berikutnya"** menandai key yang akan dipilih **berikutnya** oleh strategi aktif. Nilainya
  dihitung dengan query yang sama seperti `PickKey` (tapi tanpa mencatat pemakaian), jadi ia prediksi
  yang jujur, bukan taksiran.
- **Angka per kartu:** request 24 jam, porsi (%), dan untuk key — prioritas bila strategi `priority`.
  Masuk ke angka prioritas di kartu untuk mengubahnya langsung.

---

## Strategi rotasi

Lima strategi, bisa diganti dari dashboard dan berlaku pada request berikutnya tanpa restart:

| Strategi | Perilaku |
|---|---|
| `least-recently-used` | **Default.** Key yang paling lama menganggur dipakai lebih dulu. |
| `round-robin` | Bergilir menurut id, kembali ke awal. Urutan bisa diprediksi. |
| `random` | Acak dari key yang tidak sedang cooldown. |
| `sticky` | Satu key dipakai terus sampai cooldown; key lain menyimpan kuotanya. |
| `priority` | Angka prioritas terkecil dicoba lebih dulu (0 = paling dulu), LRU di antara nilai yang sama. Ini cara mengekspresikan "primer + cadangan": key prioritas 10 hanya beralih ke 60 saat 10 cooldown atau nonaktif. |

Semua strategi mengabaikan key yang sedang cooldown selama ada key bebas, dan jatuh ke key yang paling
cepat bebas kalau semuanya diparkir.

---

## Dashboard

Enam halaman terpisah, dipilih dari menu kiri dan tercermin di URL (`#/overview`, `#/synthesize`,
`#/history`, `#/keys`, `#/clients`, `#/account`). Detail tiap halaman ada di
[`docs/ui-pages.md`](docs/ui-pages.md). Login mendarat di **Synthesize**.

Halaman **Synthesize** dua kolom: kontrol di kiri, pustaka voice di kanan. Di layar sempit keduanya
bertumpuk.

**Pustaka voice** — bukan dropdown. Tiap baris punya tombol putar untuk mendengar contoh suara tanpa
meninggalkan halaman, dan menampilkan lokal aksen (dari `accents[].locale`), gender, serta penanda
`pro` / `milik sendiri`. Ada pencarian (server-side, 320 ms setelah berhenti mengetik), filter bahasa,
filter gender, dan tombol **Muat lagi** yang mengikuti kursor `next_page` Cartesia. Klik barisnya untuk
memilih.

**Kontrol sintesis** — model, format, slider Speed dan Volume, dropdown Emotion, plus penghitung
karakter dan kata. Batas slider diambil dari `GET /api/overview`, bukan ditulis ulang di JavaScript,
jadi nilai yang ditolak server tidak akan pernah ditawarkan UI.

**Pemutaran** — mp3 diputar lewat MediaSource Extensions: suara mulai berbunyi sambil Cartesia masih
mengirim sisanya. Terukur pada upstream yang mengirim 160 KB bertahap: **byte pertama 4,6 ms, selesai
1,84 dtk**. Browser yang tidak mendukung (dan format wav/pcm, yang tidak bisa di-append) otomatis
memakai jalur tunggu-unduh-penuh.

**Unduh tidak menagih dua kali.** Byte yang sudah mengalir dikumpulkan sambil diputar, jadi tombol
**Unduh** menyimpan dari memori. Cartesia menagih per karakter, dan menghasilkan ulang hanya untuk
mengunduh adalah pembayaran kedua untuk audio yang sama.

Preferensi terakhir (model, format, emotion, speed, volume, voice) disimpan di `localStorage`.
`Ctrl`/`Cmd`+`Enter` di kotak teks langsung men-generate.

Halaman dan asetnya di-embed ke binary: `index.html`, `app.css`, `app.js`, `auth.js`, `routing.js`,
`clients.js`, `favicon.svg`. Tanpa framework, tanpa langkah build, tanpa aset dari CDN. Aset dikirim
dengan `ETag` dan `Cache-Control: no-cache`; halaman shell-nya `no-store`, supaya upgrade binary tidak
pernah menyisakan JS lama di cache browser.

---

## Konfigurasi

Flag, dengan padanan environment variable:

| Flag | Env | Default | Keterangan |
|---|---|---|---|
| `-addr` | `CARTESIA_GW_ADDR` | `127.0.0.1:20140` | Alamat listen. |
| `-gateway-token` | `CARTESIA_GW_TOKEN` | — | Token bersama lama. Masih mengunci `/v1` saat tidak ada client key, mengaktifkan login wajib, dan diimpor sekali sebagai client key pertama. Kalau kosong, autentikasi bergantung pada akun + client key. |
| `-db` | `CARTESIA_GW_DB` | `~/.cartesia-gw/data.sqlite` | Berkas SQLite. |
| `-cartesia-version` | `CARTESIA_GW_VERSION_HEADER` | `2026-08-14` | Nilai header `Cartesia-Version`. Cartesia menolak request tanpa header ini. |
| `-cartesia-base-url` | `CARTESIA_GW_BASE_URL` | `https://api.cartesia.ai` | Untuk mengarahkan ke proxy sendiri. |
| `-retention-days` | `CARTESIA_GW_RETENTION_DAYS` | `30` | Lama riwayat request disimpan. |
| `-rotation` | `CARTESIA_GW_ROTATION` | `least-recently-used` | Strategi awal: `least-recently-used`, `round-robin`, `random`, `sticky`, `priority`. |
| `-cors-origin` | `CARTESIA_GW_CORS_ORIGIN` | — | Satu origin yang diizinkan untuk `/v1/*`. Kosong = CORS mati. |
| `-request-timeout` | — | `5m` | Batas waktu per sintesis (0 = tanpa batas). |
| `-log-level` | `CARTESIA_GW_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |

---

## Rotasi key dan cooldown

Strategi dijelaskan di bagian [Strategi rotasi](#strategi-rotasi). Yang tersimpan di database adalah
sumber kebenarannya; `-rotation` hanya menentukan nilai awal untuk database yang belum pernah diatur
dari dashboard.

**Kenapa urutannya disimpan dalam milidetik.** Kolom `last_used_at` berpresisi detik — cukup untuk
ditampilkan, tidak cukup untuk memilih. Beberapa permintaan yang jatuh dalam detik yang sama akan
seri, tie-break jatuh ke `id ASC`, dan key ber-id terkecil melayani semuanya sampai detik berganti.
Terukur pada key a/b/c, enam permintaan berturut-turut: `a b c a a a`. Urutan sekarang disimpan di
kolom `last_used_ms` terpisah, dan hasilnya `a b c a b c`. Database dari build lama mendapat kolom
itu otomatis saat dibuka.

- Kegagalan yang menyangkut key (`401`, `402`, `403`, `429`, `5xx`, error jaringan) memarkir key itu
  dengan backoff `60s → 2m → 4m → … → 30m`. `402` (kredit habis) langsung 30 menit.
- `Retry-After` dari Cartesia menang atas backoff, dibatasi 1 jam.
- Respons sukses membersihkan strike dan cooldown key tersebut.
- `400` dan `404` **tidak** memarkir key dan **tidak** diulang ke key lain: itu masalah payload atau
  voice, dan mencobanya di key kedua hanya membakar satu request untuk jawaban yang sama.
- Kalau semua key sedang cooldown, yang paling cepat bebas tetap dicoba. Menolak mencoba sama sekali
  akan mengubah hiccup sesaat menjadi gangguan.
- Tanpa key sama sekali: `503` dengan pesan yang menunjuk dashboard, bukan error upstream.

---

## Yang disimpan (dan yang tidak)

Tiga tabel: `keys`, `requests`, `settings`, dan tidak ada lagi.

`settings` menyimpan strategi rotasi yang dipilih dan kursor round-robin.

`requests` menyimpan waktu, model, voice id, jumlah karakter, ukuran audio, key mana yang dipakai,
status, latensi, dan pesan error. **Teks input dan audionya tidak disimpan** — yang pertama adalah
isi percakapan Anda, yang kedua besar, dan keduanya tidak menjawab pertanyaan apa pun yang ditanyakan
dashboard.

Riwayat dipangkas otomatis: sekali saat start, lalu tiap 6 jam, lebih tua dari `-retention-days`.

---

## Keamanan

- Default hanya listen di loopback. Bind ke alamat jaringan **tanpa** kredensial (token atau akun)
  ditolak saat start — dashboard ini menampilkan API key Cartesia Anda.
- `/api/*` hanya terbuka lewat **sesi operator** (cookie `HttpOnly`); `/v1/*` hanya lewat **client
  key**. Client key tidak bisa membuka dashboard, sesi browser tidak bisa dipakai skrip tanpa cookie.
- Secret Cartesia dikembalikan hanya oleh `GET /api/keys/{id}/reveal` atas permintaan eksplisit;
  daftar key selalu bertopeng. Secret client key tidak pernah bisa diambil lagi setelah dibuat.
- Semua secret di database disimpan sebagai hash (client key SHA-256, password bcrypt); sesi
  disimpan sebagai hash dan bisa dicabut semua sekaligus.
- Login dibatasi 10 kegagalan per 15 menit per alamat; bcrypt cost 12 membuat tiap percobaan mahal.
- URL preview voice divalidasi: hanya `https://*.cartesia.ai` yang boleh diambil. URL yang datang
  dari respons pihak ketiga tidak diperlakukan sebagai tepercaya.
- CORS mati kecuali Anda mengisi `-cors-origin`.

---

## Operasional

```bash
make test        # unit test, tanpa jaringan
make test-race
make vet
make cross       # 6 target, CGO_ENABLED=0
make docker
node scripts/smoke-docker.mjs   # smoke test terhadap instance yang sudah jalan
```

Smoke test membaca `BASE` (default `http://127.0.0.1:20999`) dan membuat akun operator sendiri, jadi
aman dijalankan terhadap container sekali pakai. Contoh untuk image lokal:

```bash
docker run --rm -d -p 127.0.0.1:20999:20140 \
  -e CARTESIA_GW_TOKEN=token-sementara-untuk-tes \
  --name cgw-smoke cartesia-gw:0.1.0
BASE=http://127.0.0.1:20999 node scripts/smoke-docker.mjs
docker rm -f cgw-smoke
```

> Catatan: image yang berjalan di `0.0.0.0` **wajib** punya `CARTESIA_GW_TOKEN` (atau akun yang sudah
> dibuat) saat start; tanpa keduanya binary menolak listen.

Contoh unit systemd:

```ini
[Unit]
Description=cartesia-gw
After=network-online.target

[Service]
ExecStart=/usr/local/bin/cartesia-gw -addr 127.0.0.1:20140 -db /var/lib/cartesia-gw/data.sqlite
Environment=CARTESIA_GW_LOG_LEVEL=warn
Restart=on-failure
StateDirectory=cartesia-gw

[Install]
WantedBy=multi-user.target
```

---

## Deploy di VPS dengan Docker

Image-nya distroless: binary statis, tanpa shell, berjalan sebagai `nonroot`, dan
`/cartesia-gw` adalah PID 1 sehingga `docker stop` memicu shutdown anggun.

Berkas pendamping:

| Berkas | Isi |
|---|---|
| `docker-compose.yml` | Satu service `cartesia-gw`, port dipublikasikan di loopback, volume `data` untuk SQLite. |
| `docker-compose.tls.yml` | Lapisan opsional: Caddy 2 dengan sertifikat Let's Encrypt otomatis + HSTS. |
| `Caddyfile` | Konfigurasi Caddy (`{$DOMAIN}`); `flush_interval -1` supaya audio streaming tidak ditahan buffer. |
| `Makefile.docker` | Target `env`, `up`, `up-tls`, `logs`, `backup`. Perlu shell POSIX, jadi terpisah dari `Makefile`. |
| `.env.example` | Semua env yang dibaca compose. `.env` di-gitignore. |

### 1. Siapkan host

Docker Engine + plugin Compose di VPS (Debian/Ubuntu):

```bash
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER   # logout/login agar grup berlaku
docker compose version          # v2.x
```

Clone repo, lalu buat `.env` dengan token acak:

```bash
git clone https://github.com/rizkirmdhnnn/cartesia-gw && cd cartesia-gw
make -f Makefile.docker env
```

Atau manual: `cp .env.example .env`, isi `CARTESIA_GW_TOKEN` dengan `openssl rand -hex 32`,
lalu `chmod 600 .env`.

**`CARTESIA_GW_TOKEN` wajib saat start.** Container listen di `0.0.0.0` di dalam jaringannya, dan
binary menolak bind non-loopback tanpa kredensial apa pun — container akan langsung exit dengan pesan
jelas. Token ini berfungsi ganda:

1. **kunci bootstrap**: saat dashboard pertama kali dibuka, masuk dengan password = token ini untuk
   membuat akun operator;
2. **client key pertama**: token diimpor otomatis sebagai client key, jadi skrip yang sudah memakainya
   tetap jalan setelah Anda mulai membuat key `dk_…`.

Setelah akun dan client key dibuat, token boleh diganti di `.env` (dan container di-recreate); tidak
ada yang bergantung padanya lagi. Untuk deployment baru tanpa skrip lama, nilai acak apa pun cukup.

### 2. Jalankan

```bash
make -f Makefile.docker up      # docker compose up -d --build
make -f Makefile.docker ps
curl http://127.0.0.1:20140/healthz
```

Port hanya dipublikasikan ke `127.0.0.1` (`CARTESIA_GW_BIND`), jadi belum bisa diakses dari
internet. Buka dashboard lewat SSH tunnel:

```bash
ssh -L 20140:127.0.0.1:20140 user@vps
# lalu http://127.0.0.1:20140/ di browser Anda
```

### 3. Ekspos dengan HTTPS (disarankan)

Isi juga `DOMAIN` (dan `ACME_EMAIL`) di `.env`, arahkan DNS A/AAAA ke VPS, buka port 80/443,
lalu:

```bash
make -f Makefile.docker up-tls
```

Perintah itu setara dengan
`docker compose -f docker-compose.yml -f docker-compose.tls.yml --profile tls up -d --build`.
Dengan lapisan ini port 20140 **tidak** dipublikasikan lagi (`ports: !reset []`); satu-satunya
pintu adalah Caddy di 80/443, yang meneruskan ke `cartesia-gw:20140` di jaringan internal compose.
Sertifikat diambil dan diperpanjang Caddy sendiri (data di volume `caddy_data`).

Klien kemudian memanggil:

```bash
curl -X POST https://$DOMAIN/v1/audio/speech \
  -H "Authorization: Bearer $CARTESIA_GW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"model":"sonic-3","input":"Halo dunia","voice":"<voice-uuid>"}' \
  --output speech.mp3
```

Panduan khusus untuk VPS berpaneL **aaPanel** (modul Docker + Nginx/SSL bawaan panel, tanpa
Caddy) ada di [`docs/deploy-aapanel.md`](docs/deploy-aapanel.md).

Kalau memakai nginx/Traefik yang sudah ada di host, lewati lapisan TLS. Set
`CARTESIA_GW_BIND=0.0.0.0` **hanya** jika port itu dikunci firewall dari internet
(`ufw deny 20140` / security group), lalu proxy ke `127.0.0.1:20140`.

### 4. Update, backup, lihat log

```bash
git pull
make -f Makefile.docker up          # rebuild + recreate, volume data tetap
make -f Makefile.docker logs
make -f Makefile.docker backup      # ./backups/cartesia-gw-<tanggal>.tgz
```

Database SQLite ada di volume `cartesia-gw_data` (mount `/data`, berisi `data.sqlite` beserta
`-wal`/`-shm`). Karena itu `backup` menyalin seluruh direktori, bukan satu berkas: menyalin
`data.sqlite` saja saat WAL belum checkpoint menghasilkan backup yang tertinggal.

**Catatan operasional container:**

- Versi yang ter-embed di binary diambil dari `--build-arg VERSION`, yang di compose diisi
  `CARTESIA_GW_IMAGE_TAG` (default `.env.example`: `0.1.0`, sama dengan `VERSION`). Kalau
  `VERSION` naik, naikkan juga nilai itu — atau `CARTESIA_GW_IMAGE_TAG=$(cat VERSION) make -f
  Makefile.docker up` — supaya `/healthz` melaporkan versi rilis, bukan `dev`.
- Tidak ada `HEALTHCHECK`: image distroless tidak punya shell atau curl, dan probe `/dev/tcp`
  butuh bash yang juga tidak ada. Pakai `GET /healthz` dari luar — uptime checker, reverse proxy,
  atau healthcheck Caddy pada lapisan TLS.
- `stop_grace_period: 15s` memberi sintesis yang sedang berjalan waktu menyelesaikan sebelum
  container dihentikan paksa.
- API key Cartesia tidak masuk ke env atau compose; mereka ditambahkan lewat dashboard dan
  disimpan di SQLite. Volume `data` karena itu harus diperlakukan sebagai rahasia.

---

## Struktur

```
cmd/cartesia-gw        wiring, flag, shutdown, pemangkas riwayat
internal/config        flag/env + penolakan bind jaringan tanpa token
internal/cartesia      terjemahan payload, klien HTTP, katalog model + emotion
internal/store         SQLite: key + state cooldown, riwayat request
internal/gateway       route, terjemahan, rotasi, auth, API dashboard
internal/gateway/ui    index.html + app.css + app.js + favicon, di-embed ke binary
```

Frontend-nya tiga berkas (`index.html`, `app.css`, `app.js`) yang di-embed lewat `//go:embed`, tanpa
Node, tanpa langkah build, tanpa aset dari CDN — halaman harus tetap tampil di host yang tidak punya
akses keluar.

## Batasan

- Hanya TTS. Tidak ada chat, embedding, STT, combo, atau fallback lintas provider.
- Tidak ada autentikasi multi-user: satu token, satu operator.
- Belum ada rate limit per klien. Kalau nanti dipakai lebih dari satu klien, batas per key ada di
  Cartesia, bukan di sini.
- `emotion` hanya berpengaruh pada `sonic-3` dan yang lebih baru; gateway meneruskannya apa adanya
  untuk model apa pun, sesuai kontrak Cartesia.
- Kalau upstream mengirim body tanpa `Content-Length` dan tanpa chunked lalu menahan koneksinya,
  permintaan itu berhenti di `-request-timeout` (default 5 menit). Cartesia sendiri selalu memframing
  responsnya, jadi ini hanya soal proxy yang salah di tengah jalan.
