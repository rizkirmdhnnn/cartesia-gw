# Reverse proxy untuk cartesia-gw di aaPanel

Container `cartesia-gw` hanya listen di `127.0.0.1:20140` (compose mempublikasikannya ke
loopback). Nginx milik aaPanel yang menjadi pintu publik: TLS, domain, dan meneruskan permintaan
ke container.

Dokumen ini khusus soal proxy-nya: pilih salah satu jalur, lalu cocokkan hasil akhirnya dengan
bagian *Verifikasi*.

```
klien ──HTTPS 443──▶ Nginx aaPanel ──HTTP 127.0.0.1:20140──▶ container cartesia-gw
```

Asumsi: container sudah jalan dan `curl -s http://127.0.0.1:20140/healthz` mengembalikan
`{"status":"ok",...}`. Kalau belum, selesaikan dulu deploy container-nya; proxy tidak bisa
menutupi backend yang mati.

Domain contoh di dokumen ini: **`tts.contoh.com`**. Ganti dengan domain Anda.

---

## Pilih jalur

| Jalur | Cocok kalau | Yang perlu disiapkan |
|---|---|---|
| **A. Website aaPanel + Reverse Proxy** (disarankan) | Tidak mau mengedit Nginx manual; SSL dikelola panel | Satu site di aaPanel, saja |
| **B. Edit vhost Nginx langsung** | Ingin semua header/timeout terlihat di satu tempat | Akses Config file site |
| **C. nginx di luar aaPanel** (mis. reverse proxy di server lain, Cloudflare Tunnel) | Panel bukan pemilik port 443 | Host proxy sendiri |

Semua jalur memakai blok `location` yang sama di bagian berikut.

---

## Blok inti (yang wajib sama di semua jalur)

Nilai default Nginx dirancang untuk HTML pendek. Audio TTS berperilaku sebaliknya: body besar,
dihasilkan bertahap, dan klien memutarnya sambil mengalir. Karena itu tiga hal ini tidak opsional:

| Direktif | Kenapa |
|---|---|
| `proxy_buffering off` | Tanpa ini Nginx menumpuk ~4×8 KB sebelum meneruskan. Suara baru berbunyi setelah upstream "berhenti sejenak" atau selesai — pengalaman streaming hilang. |
| `gzip off` | MP3/WAV/PCM sudah terkompresi. Meng-gzip hanya menambah CPU dan latency, dan merusak streaming bila buffering dimatikan. |
| `proxy_read_timeout 600s` | Satu sintesis bisa berjalan sampai `-request-timeout` gateway (default 5 menit). Default Nginx 60 detik akan memutusnya di tengah audio. |

```nginx
client_max_body_size 1m;

location / {
    proxy_pass http://127.0.0.1:20140;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    proxy_buffering off;
    proxy_cache off;
    gzip off;

    proxy_read_timeout 600s;
    proxy_send_timeout 600s;
}

# Probe health, tidak perlu dicatat di access log.
location = /healthz {
    access_log off;
    proxy_pass http://127.0.0.1:20140;
}
```

`client_max_body_size 1m` cukup: body terbesar adalah JSON teks (beberapa KB). Audio berjalan di
arah sebaliknya, jadi batas body tidak mempengaruhinya.

---

## Jalur A — Website aaPanel + Reverse Proxy

1. **Website → Add site**
   - Domain: `tts.contoh.com`
   - PHP: **Pure static** (tidak ada PHP)
   - Database: *None*
2. Buka site → **SSL → Let's Encrypt** → centang domain → **Apply**, lalu aktifkan
   **Force HTTPS**. Pastikan A record domain sudah mengarah ke VPS sebelum menekan Apply.
3. Buka site → **Reverse Proxy → Add reverse proxy**:
   - **Proxy name**: `cartesia-gw`
   - **Target URL**: `http://127.0.0.1:20140`
   - **Send Domain**: `$host` (default)
   - **Cache**: *off*
   - Simpan.
4. Panel menulis konfigurasi di
   `/www/server/panel/vhost/nginx/proxy/tts.contoh.com/*.conf` dan menyisipkannya ke vhost.
   Buka file itu lewat **Files** (atau terminal), lalu tambahkan tiga direktif streaming ke blok
   `location` miliknya:

   ```nginx
   proxy_buffering off;
   gzip off;
   proxy_read_timeout 600s;
   proxy_send_timeout 600s;
   ```

5. Klik **Save** di editor (atau **Config file → Save**) agar panel menjalankan `nginx -t` dan
   reload.

Catatan aaPanel:

- Jangan menambah `location /` kedua di **Config file** selama blok dari Reverse Proxy masih
  ada — Nginx menolak `duplicate location "/"`. Satu `location /`, dari mana pun asalnya.
- Kalau `location = /healthz` bentrok, hapus saja blok itu; healthz tetap terjangkau lewat
  `location /`.
- **Force HTTPS** dibuat panel sebagai server block port 80 dengan `return 301`; tidak ada yang
  perlu ditambah di sisi proxy.

## Jalur B — Edit vhost Nginx langsung

1. Selesaikan langkah 1–2 Jalur A (site + SSL).
2. Site → **Config file**. Cari `server` yang memuat `listen 443`.
3. Hapus baris `include .../proxy/*.conf;` (kalau ada, karena Reverse Proxy jalur A membuatnya)
   dan baris `include enable-php-*.conf;` kalau site dibuat sebagai PHP — keduanya akan
   mendefinisikan perilaku root yang bertabrakan.
4. Ganti `root`/`index`/`location ~ \.php$` dan blok `location /` buatan panel dengan blok inti
   di atas.
5. **Save**. Panel me-reload Nginx; kalau gagal, pesan `nginx -t` muncul di jendela yang sama.

## Jalur C — nginx di luar aaPanel

Kalau port 443 dipegang server lain (atau panel di VPS berbeda), konfigurasi minimalnya:

```nginx
server {
    listen 443 ssl http2;
    server_name tts.contoh.com;

    ssl_certificate     /path/fullchain.pem;
    ssl_certificate_key /path/privkey.pem;

    client_max_body_size 1m;

    location / {
        proxy_pass http://IP_VPS:20140;   # atau 127.0.0.1 bila satu mesin
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_buffering off;
        gzip off;
        proxy_read_timeout 600s;
    }
}

server {
    listen 80;
    server_name tts.contoh.com;
    return 301 https://$host$request_uri;
}
```

Pada jalur ini container harus bisa dijangkau dari luar: ubah `CARTESIA_GW_BIND=0.0.0.0` di `.env`
lalu `docker compose up -d --force-recreate`, **dan** kunci port 20140 di firewall dari internet
(`ufw deny 20140`, atau atur security group hanya dari IP proxy). Token gateway tetap wajib —
tanpa itu binary menolak start, karena dashboard menampilkan API key Cartesia Anda.

Cloudflare Tunnel (tanpa membuka port sama sekali): arahkan `cloudflared` ke
`http://127.0.0.1:20140`; pastikan opsi *buffer responses* tidak diaktifkan untuk domain itu,
karena sama saja dengan `proxy_buffering on`.

---

## Verifikasi

Dari komputer Anda (bukan VPS) — perintah di bawah memakai shell bash; di PowerShell ganti
`-o /dev/null` dengan `-o NUL` dan kutip argumen `-w`.

```bash
# 1. Proxy hidup dan meneruskan ke gateway
curl -s https://tts.contoh.com/healthz
#    {"cartesiaVersion":"...","gatewayTokenIsSet":true,"keys":N,"status":"ok",...}

# 2. Redirect HTTP -> HTTPS
curl -sI http://tts.contoh.com/healthz | grep -i '^location'
#    location: https://tts.contoh.com/healthz

# 3. Auth ditegakkan di belakang proxy
curl -s -o /dev/null -w "%{http_code}\n" -X POST https://tts.contoh.com/v1/audio/speech \
  -H "Content-Type: application/json" -d '{}'
#    401  (tanpa token) / 400 atau 200 (dengan Authorization: Bearer TOKEN)

# 4. Streaming benar-benar mengalir
curl -N -o /dev/null -w "first_byte=%{time_starttransfer}s total=%{time_total}s\n" \
  -X POST https://tts.contoh.com/v1/audio/speech \
  -H "Authorization: Bearer TOKEN" -H "Content-Type: application/json" \
  -d '{"model":"sonic-3","input":"Satu dua tiga empat lima.","voice":"<voice-uuid>"}'
```

Di tes 4, `first_byte` harus jauh lebih kecil daripada `total`. Kalau keduanya hampir sama,
jawabannya ditahan buffer — lihat troubleshooting.

Di dashboard (`https://tts.contoh.com/`) tab **Requests**: latensi tercatat dalam milidetik dan
status `200`. Dashboard sendiri memakai header `X-Gateway-Token`; tidak ada konfigurasi CORS
yang diperlukan selama halaman dan API berada di origin yang sama (yang memang terjadi ketika
semua dilayani lewat proxy ini).

---

## Troubleshooting

**`502 Bad Gateway`.**
Nginx tidak menemukan backend. Di VPS: `curl -s http://127.0.0.1:20140/healthz`. Gagal →
masalah container (`docker ps`, `docker logs cartesia-gw --tail 20`). Berhasil → periksa
`proxy_pass` dan bahwa Nginx tidak berjalan di network namespace berbeda (mis. di dalam
container lain, sehingga `127.0.0.1` bukan host).

**`504 Gateway Time-out` pada input panjang.**
`proxy_read_timeout` masih 60s. Tambahkan `600s` di blok `location` (dan `proxy_send_timeout`
untuk klien lambat).

**Audio baru berbunyi setelah selesai / jeda beberapa detik di awal.**
`proxy_buffering off` belum aktif pada blok yang benar. Cek: `nginx -T | grep -A20 "location /"`
di VPS. Kalau direktorinya ada tapi efeknya tidak, kemungkinan ada `proxy_buffering on;`
setelahnya, atau jalurnya lewat CDN yang membuffer.

**`413 Request Entity Too Large`.**
`client_max_body_size` di vhost lebih kecil dari 1m (nilai global ada di
`/www/server/nginx/conf/nginx.conf`). Set 1m di blok `server`.

**`403 Forbidden` / halaman kosong aaPanel.**
Site masih berisi `root` + `index.php` milik aaPanel sehingga `location /` menunjuk ke folder,
bukan ke proxy. Hapus `root`/`index` dari `server` itu, atau pastikan blok proxy adalah
`location /` yang aktif.

**`duplicate location "/"` saat Save.**
Ada dua definisi: satu dari Reverse Proxy (file di `vhost/nginx/proxy/<domain>/`) dan satu yang
Anda tempel manual. Pilih satu — edit file proxy-nya saja.

**Dashboard terbuka tapi semua panel kosong / `401`.**
Nginx tidak menghapus header. Pastikan tidak ada `proxy_set_header Authorization "";` dan bahwa
Anda mengisi token di `localStorage` (lihat panduan deploy, langkah 5).

**Mixed content (halaman HTTPS, panggilan HTTP).**
Semua fetch dashboard relatif terhadap path halaman, jadi ini hanya terjadi kalau halaman dibuka
lewat IP/skema lain. Buka selalu `https://tts.contoh.com/`.

---

## Ringkasan checklist

- [ ] `curl -s http://127.0.0.1:20140/healthz` di VPS → `"status":"ok"`
- [ ] Site aaPanel ada, SSL Let's Encrypt aktif, Force HTTPS aktif
- [ ] Tepat satu `location /` → `proxy_pass http://127.0.0.1:20140`
- [ ] `proxy_buffering off`, `gzip off`, `proxy_read_timeout 600s` ada di blok itu
- [ ] `https://<domain>/healthz` → JSON yang sama
- [ ] `curl -N` ke `/v1/audio/speech`: `first_byte` ≪ `total`
- [ ] Dashboard bisa membuka API keys setelah token diisi di `localStorage`
- [ ] (Jalur C) port 20140 diblokir dari internet
