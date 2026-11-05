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

## Dashboard

Dua kolom: kontrol di kiri, pustaka voice di kanan. Di layar sempit keduanya bertumpuk.

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

Halaman dan asetnya di-embed ke binary: `index.html`, `app.css`, `app.js`, `favicon.svg`. Tanpa
framework, tanpa langkah build, tanpa aset dari CDN. Ketiga aset dikirim dengan `ETag` dan
`Cache-Control: no-cache`; halaman shell-nya `no-store`, supaya upgrade binary tidak pernah menyisakan
JS lama di cache browser.

---

## Konfigurasi

Flag, dengan padanan environment variable:

| Flag | Env | Default | Keterangan |
|---|---|---|---|
| `-addr` | `CARTESIA_GW_ADDR` | `127.0.0.1:20140` | Alamat listen. |
| `-gateway-token` | `CARTESIA_GW_TOKEN` | — | Token yang harus dibawa klien. **Wajib** kalau `-addr` bukan loopback. |
| `-db` | `CARTESIA_GW_DB` | `~/.cartesia-gw/data.sqlite` | Berkas SQLite. |
| `-cartesia-version` | `CARTESIA_GW_VERSION_HEADER` | `2026-08-14` | Nilai header `Cartesia-Version`. Cartesia menolak request tanpa header ini. |
| `-cartesia-base-url` | `CARTESIA_GW_BASE_URL` | `https://api.cartesia.ai` | Untuk mengarahkan ke proxy sendiri. |
| `-retention-days` | `CARTESIA_GW_RETENTION_DAYS` | `30` | Lama riwayat request disimpan. |
| `-rotation` | `CARTESIA_GW_ROTATION` | `least-recently-used` | Strategi pemilihan key sebelum dashboard menyimpan satu: `least-recently-used`, `round-robin`, `random`, `sticky`. |
| `-cors-origin` | `CARTESIA_GW_CORS_ORIGIN` | — | Satu origin yang diizinkan untuk `/v1/*`. Kosong = CORS mati. |
| `-request-timeout` | — | `5m` | Batas waktu per sintesis (0 = tanpa batas). |
| `-log-level` | `CARTESIA_GW_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |

---

## Rotasi key dan cooldown

Strategi pemilihan key bisa diganti dari dashboard (panel **API keys**) dan berlaku pada permintaan
berikutnya tanpa restart. Empat pilihan:

| Strategi | Perilaku |
|---|---|
| `least-recently-used` | **Default.** Key yang paling lama tidak dipakai dipilih lebih dulu. Tetap menyebar rata saat key ditambah atau dihapus, karena tidak menyimpan urutan. |
| `round-robin` | Bergilir berurutan menurut id, kembali ke key pertama setelah yang terakhir. Urutannya bisa diprediksi. |
| `random` | Acak dari key yang tidak sedang diparkir. |
| `sticky` | Satu key dipakai terus sampai kena limit, jadi kuota key lain tetap utuh. |

Semua strategi mengabaikan key yang sedang cooldown selama masih ada key yang bebas. Yang tersimpan
di database adalah sumber kebenarannya; `-rotation` hanya menentukan nilai awal untuk database yang
belum pernah diatur dari dashboard.

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

- Default hanya listen di loopback. Bind ke alamat jaringan **tanpa** `-gateway-token` ditolak saat
  start — dashboard ini menampilkan API key Cartesia Anda.
- `/v1/*` dan `/api/*` butuh token kalau token diatur. Halaman statisnya tidak membawa rahasia, jadi
  tetap disajikan; token disimpan di `localStorage` setelah Anda menempelkannya.
- Secret key hanya dikembalikan oleh `GET /api/keys/{id}/reveal` atas permintaan eksplisit;
  daftar key selalu bertopeng.
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
```

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

**`CARTESIA_GW_TOKEN` wajib.** Container listen di `0.0.0.0` di dalam jaringannya, dan binary
menolak bind non-loopback tanpa token — container akan langsung exit dengan pesan jelas. Token
dipakai klien sebagai `Authorization: Bearer …` dan juga untuk `/api/*` (dashboard).

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
