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
  "output_format": {"container": "mp3", "sample_rate": 44100, "bit_rate": 128000}
}
```

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
| `-cors-origin` | `CARTESIA_GW_CORS_ORIGIN` | — | Satu origin yang diizinkan untuk `/v1/*`. Kosong = CORS mati. |
| `-request-timeout` | — | `5m` | Batas waktu per sintesis (0 = tanpa batas). |
| `-log-level` | `CARTESIA_GW_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |

---

## Rotasi key dan cooldown

- Key dipilih **round-robin menurut yang paling lama tidak dipakai**, bukan acak.
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

Tiga tabel: `keys`, `requests`, dan tidak ada lagi.

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

## Struktur

```
cmd/cartesia-gw        wiring, flag, shutdown, pemangkas riwayat
internal/config        flag/env + penolakan bind jaringan tanpa token
internal/cartesia      terjemahan payload, klien HTTP, katalog model
internal/store         SQLite: key + state cooldown, riwayat request
internal/gateway       route, terjemahan, rotasi, auth, API dashboard
internal/gateway/ui    satu halaman HTML yang di-embed ke binary
```

Frontend-nya satu berkas HTML dengan JS biasa, di-embed lewat `//go:embed`. Tanpa Node, tanpa
langkah build, tanpa aset dari CDN — halaman harus tetap tampil di host yang tidak punya akses
keluar.

## Batasan

- Hanya TTS. Tidak ada chat, embedding, STT, combo, atau fallback lintas provider.
- Tidak ada autentikasi multi-user: satu token, satu operator.
- Belum ada rate limit per klien. Kalau nanti dipakai lebih dari satu klien, batas per key ada di
  Cartesia, bukan di sini.
