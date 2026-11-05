# Deploy cartesia-gw di aaPanel (Docker)

Panduan langkah demi langkah untuk VPS yang panelnya **aaPanel**, memakai modul **Docker**
bawaan aaPanel sebagai manajer container dan **Website** bawaan aaPanel sebagai reverse proxy
HTTPS. Tidak perlu mengetik compose manual di terminal kalau tidak mau; keduanya disediakan.

Hasil akhir:

```
Internet ──HTTPS 443──▶ Nginx aaPanel (proyek web + SSL Let's Encrypt)
                                    │  proxy ke 127.0.0.1:20140
                                    ▼
                        container cartesia-gw (distroless, nonroot)
                                    ▼
                        volume cartesia-gw_data -> /data/data.sqlite
```

Container **tidak** membuka port ke internet: compose mempublikasikannya hanya di loopback.
Satu-satunya pintu adalah Nginx dari aaPanel, yang sudah punya SSL.

---

## 0. Yang perlu disiapkan

| Hal | Keterangan |
|---|---|
| VPS | 1 vCPU / 1 GB RAM sudah cukup; image akhir ±20 MB, RSS gateway ±30 MB |
| aaPanel | versi terbaru (modul Docker ada di menu kiri, bernama **Docker** / **Docker 管理器**) |
| Domain | mis. `tts.contoh.com`, A record ke IP VPS |
| Port | 80 dan 443 terbuka (dipakai aaPanel + Let's Encrypt) |

Di aaPanel, pastikan di **App Store → Docker** plugin-nya terpasang (status *Installed*). Ini
memasang Docker Engine dan `docker compose` v2. Kalau menu Docker tidak ada, cari di App Store
→ kategori *Docker*.

---

## 1. Ambil sumber dan siapkan `.env`

Buka **Files** di aaPanel, lalu **Terminal** (tombol di kanan atas panel). Jangan taruh project di
`/www/wwwroot/<domain>` milik Website — proyek web itu nanti hanya berisi konfigurasi proxy,
sedangkan kode gateway tinggal di luar itu.

Panduan ini memakai **`/www/dk_project/dk_app/cartesia-gw`** (folder yang Anda pilih). Path-nya
boleh diganti apa saja; yang penting seluruh perintah di bawah dijalankan dari folder itu dan
folder tersebut persisten (di dalam `/www`, bukan `/tmp` atau `/root` yang di-clean).

```bash
mkdir -p /www/dk_project/dk_app/cartesia-gw
cd /www/dk_project/dk_app/cartesia-gw
git clone https://github.com/rizkirmdhnnn/cartesia-gw.git .
git status            # pastikan ada Dockerfile, docker-compose.yml, .env.example
```

> **Folder milik aaPanel.** Kalau `dk_app` ternyata dipakai aaPanel untuk hal lain, tetap aman
> dipakai selama subfolder `cartesia-gw` hanya berisi repo ini. Hal yang boleh dimiliki aaPanel
> hanyalah **site** (konfigurasi Nginx) — site tidak akan pernah menulis ke folder ini.

Buat token bootstrap. Token ini **wajib ada saat start** (container listen di `0.0.0.0`) dan
sekaligus diimpor sebagai client key pertama supaya skrip lama tetap jalan:

```bash
cp .env.example .env
TOKEN=$(openssl rand -hex 32)
sed -i "s|^CARTESIA_GW_TOKEN=.*|CARTESIA_GW_TOKEN=$TOKEN|" .env
chmod 600 .env
echo "$TOKEN"        # CATAT: dipakai sebagai password saat pertama kali masuk dashboard
```

Edit `.env` dan pastikan tiga baris ini:

```ini
CARTESIA_GW_TOKEN=<token hasil generate di atas>
CARTESIA_GW_BIND=127.0.0.1
CARTESIA_GW_IMAGE_TAG=0.1.0
```

`CARTESIA_GW_BIND=127.0.0.1` penting: compose akan menerbitkan port sebagai
`127.0.0.1:20140` — hanya Nginx lokal yang bisa menjangkaunya.

> Kalau repo masih berupa fork belum di-merge, clone fork Anda:
> `git clone https://github.com/<akun>/cartesia-gw.git .` lalu `git checkout feat/docker-deploy`.
> Cek ada `docker-compose.yml` di direktori itu — kalau tidak ada, PR belum masuk ke branch
> yang Anda clone.
>
> **Sudah ada salinan sebelumnya?** Jangan clone kedua kali ke folder yang sama
> (`fatal: destination path '.' already exists`). Masuk saja ke foldernya lalu `git pull`.
>
> **Tanpa git?** Unduh source dari GitHub (Code → Download ZIP), unggah ke
> `/www/dk_project/dk_app/cartesia-gw` lewat **Files → Upload**, lalu ekstrak di situ. `Dockerfile`
> harus berada langsung di folder itu, bukan di subfolder `cartesia-gw/` di dalamnya.

---

## 2a. Build image (cara visual: menu Docker aaPanel)

1. Menu kiri **Docker** → tab **Images** → tombol **Build Image**.
2. Isi:
   - **Directory**: `/www/dk_project/dk_app/cartesia-gw`
   - **Dockerfile**: `Dockerfile` (default)
   - **Image name**: `cartesia-gw`
   - **Tag**: `0.1.0`
3. Klik **Build/Submit**. Build pertama mengunduh `golang:1.27-alpine` dan modul Go, ±2–4 menit
   tergantung jaringan. Sukses ditandai image `cartesia-gw:0.1.0` muncul di daftar (ukuran
   ±20 MB).

Kalau build gagal, lihat log di jendela build; penyebab paling umum adalah pertanyaan DNS/timeout
ke `proxy.golang.org` (lihat *Troubleshooting* di bawah).

## 2b. Build image (cara terminal)

```bash
cd /www/dk_project/dk_app/cartesia-gw
docker compose build          # membaca CARTESIA_GW_IMAGE_TAG dari .env
docker images | grep cartesia-gw
```

Keduanya setara — pilih salah satu, jangan keduanya.

---

## 3. Jalankan container (cara visual: menu Docker aaPanel)

1. **Docker** → tab **Containers** → **Create Container** → pilih mode **Compose** (kalau versi
   aaPanel Anda menampilkan pilihan *Compose*; beberapa versi menamainya *Add Compose Project*).
2. Isi:
   - **Name**: `cartesia-gw`
   - **Compose file**: tempel isi file di bawah **setelah** mengganti `<TOKEN>` dengan token Anda,
     **atau** arahkan ke `/www/dk_project/dk_app/cartesia-gw/docker-compose.yml` bila panel mendukung path.
   - **Environment / `.env` file**: `/www/dk_project/dk_app/cartesia-gw/.env` (kalau ada kolomnya).
3. Submit. Tunggu status container menjadi **running**.

Isi compose yang siap tempel (versi mandiri, tidak bergantung pada `.env`, untuk panel yang
tidak bisa membaca `.env`):

```yaml
services:
  cartesia-gw:
    image: cartesia-gw:0.1.0
    container_name: cartesia-gw
    restart: unless-stopped
    environment:
      CARTESIA_GW_ADDR: 0.0.0.0:20140
      CARTESIA_GW_DB: /data/data.sqlite
      CARTESIA_GW_TOKEN: "<TOKEN>"
      CARTESIA_GW_LOG_LEVEL: info
      CARTESIA_GW_ROTATION: least-recently-used
      CARTESIA_GW_RETENTION_DAYS: "30"
      CARTESIA_GW_VERSION_HEADER: "2026-08-14"
      CARTESIA_GW_BASE_URL: https://api.cartesia.ai
    ports:
      - "127.0.0.1:20140:20140"
    volumes:
      - data:/data
    stop_grace_period: 15s

volumes:
  data:
```

Jika panel selalu membuat volume dengan prefix, nama volume hasilnya bisa
`cartesia-gw_data` atau `<nama-project>_data`. Catat namanya — dipakai saat backup
(`Docker → Volumes`).

## 3'. Jalankan container (cara terminal)

```bash
cd /www/dk_project/dk_app/cartesia-gw
docker compose up -d
docker compose ps
```

Keluaran yang benar:

```
NAME                 STATUS          PORTS
cartesia-gw          Up 5 seconds    127.0.0.1:20140->20140/tcp
```

Lalu verifikasi dari dalam VPS:

```bash
curl -s http://127.0.0.1:20140/healthz
```

```json
{"cartesiaVersion":"2026-08-14","gatewayTokenIsSet":true,"keys":0,
 "retentionDays":30,"status":"ok","version":"0.1.0"}
```

`gatewayTokenIsSet: true` wajib. Kalau container langsung `restarting`, jalankan
`docker logs cartesia-gw --tail 20` — pesannya hampir pasti
`addr "0.0.0.0:20140" is not loopback: -gateway-token is required`, artinya token tidak
terbaca (lihat *Troubleshooting*).

---

## 4. Website + reverse proxy HTTPS di aaPanel

1. **Website** → **Add site**:
   - Domain: `tts.contoh.com`
   - Database: *NoSQL/None*, PHP: *Pure static* (tidak ada PHP yang dipakai)
   - PEM/mode lain: biarkan default.
2. Buka site itu → tab **SSL** → **Let's Encrypt** → centang domain → **Apply**. Tunggu
   sertifikat terbit, lalu aktifkan **Force HTTPS**.
3. Tab **Config file** (atau **Reverse Proxy** → *Add reverse proxy*). Cara paling tahan-lama
   adalah edit Nginx-nya langsung. Ringkasan blok yang wajib ada, alasan tiap direktif, dan
   troubleshooting khusus proxy ada di [`deploy-aapanel-proxy.md`](deploy-aapanel-proxy.md);
   intinya di bawah.
   Cari `server` yang memuat `listen 443`, lalu hapus/timpa blok
   `location /` yang dibuat aaPanel dengan yang di bawah. Jangan menaruh blok ini langsung di
   bawah `server` kalau aaPanel sudah menyisipkan `include enable-php-*.conf` atau
   `include proxy/*.conf` — hapus dulu baris include itu supaya tidak ada dua `location /`.

```nginx
# di dalam server { listen 443 ssl http2; ... } milik tts.contoh.com

client_max_body_size 1m;

location / {
    proxy_pass http://127.0.0.1:20140;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    # Audio disintesis sambil dialirkan. Buffer default Nginx (4×8k) membuat
    # pemutaran tertunda beberapa detik; matikan buffering-nya.
    proxy_buffering off;
    proxy_cache off;
    gzip off;

    # Satu permintaan bisa streaming sampai batas -request-timeout (default 5m).
    proxy_read_timeout 600s;
    proxy_send_timeout 600s;
}

# healthz tidak perlu ikut ke log akses: probe uptime memanggilnya tiap menit.
location = /healthz {
    access_log off;
    proxy_pass http://127.0.0.1:20140;
}
```

**Kalau aaPanel menolak menyimpan karena `duplicate location "/"`**, pakai panel-nya saja:
**Reverse Proxy → Add reverse proxy** dengan target `http://127.0.0.1:20140`, simpan, lalu buka
file proxy yang dibuat (`/www/server/panel/vhost/nginx/proxy/<domain>/*.conf`) dan tambahkan
`proxy_buffering off;`, `gzip off;`, `proxy_read_timeout 600s;` ke blok `location` di situ.

**Kalau `location = /healthz` bentrok dengan blok aaPanel**, cukup hapus blok itu — yang penting
`location /` sudah benar. Healthz tetap jalan lewat `location /`.

4. Klik **Save**; aaPanel menjalankan `nginx -t` dan reload otomatis.
5. Uji dari komputer Anda:

```bash
curl -s https://tts.contoh.com/healthz
```

Kalau ini mengembalikan JSON yang sama seperti di langkah 3, jalur TLS sudah beres.

---

## 5. Buat akun operator, tambahkan Cartesia key, buat client key

1. Buka `https://tts.contoh.com/`. Masuk dengan akun default **`admin` / `admin`**. Console langsung
   meminta **ganti password** — isi password baru (minimal 12 karakter), simpan, lalu masuk lagi.
2. Panel **Cartesia Keys** → tempel key `sk_car_…` dari
   [Cartesia dashboard](https://play.cartesia.ai/console/api-keys) → **Tambah**.
3. Panel **Client Keys** → beri nama (mis. `n8n-produksi`), pilih masa berlaku, **Buat key**. Secret
   `dk_…` muncul **sekali** — salin sekarang; itulah yang dipakai klien.
4. Panel **Traffic Map** menunjukkan aliran trafik client → gateway → tiap Cartesia key, termasuk
   key mana yang akan dipakai berikutnya.
5. Coba **Synthesize** di workspace — kalau suara keluar, semuanya tersambung.

> **Lupa password?** `docker exec cartesia-gw-cartesia-gw-1 /cartesia-gw -db /data/data.sqlite -reset-password`
> mengembalikan login ke `admin`/`admin` (wajib ganti lagi, semua sesi dicabut). Ganti nama container
> sesuai `docker compose ps`.

> **Token `CARTESIA_GW_TOKEN`** tetap wajib saat start (container listen di `0.0.0.0`), dan sekali
> diimpor sebagai client key bernama *imported dari CARTESIA_GW_TOKEN* supaya skrip lama tetap jalan.
> Setelah punya key `dk_…` sendiri, key hasil impor itu boleh dihapus.

> Jangan masuk dengan `http://IP-VPS:20140`. Port itu hanya listen di loopback, dan
> sengaja tidak bisa dibuka dari luar.

---

## 6. Panggil dari klien

```bash
curl -X POST https://tts.contoh.com/v1/audio/speech \
  -H "Authorization: Bearer dk_XXXXXXXX" \
  -H "Content-Type: application/json" \
  -d '{"model":"sonic-3","input":"Halo dari aaPanel","voice":"<voice-uuid>"}' \
  --output speech.mp3
```

OpenAI SDK cukup diarahkan ke base URL `https://tts.contoh.com/v1` dengan `api_key` = client key
`dk_…` (bukan key Cartesia, dan bukan token bootstrap).

---

## 7. Update versi

```bash
cd /www/dk_project/dk_app/cartesia-gw
git pull
docker compose up -d --build        # volume data tidak tersentuh
docker compose ps
curl -s http://127.0.0.1:20140/healthz   # cek "version" sudah berubah
```

Lewat panel: **Docker → Images → Build Image** dengan tag baru, lalu **Containers → Recreate**
pada container `cartesia-gw`. Kalau memakai tag baru, ubah `image:` di compose atau
`CARTESIA_GW_IMAGE_TAG` di `.env` dulu.

Saat `recreate`, Docker mengirim SIGTERM ke binary (PID 1) dan menunggu sampai 15 detik
(`stop_grace_period`) — sintesis yang sedang berjalan diberi waktu selesai, bukan dipotong.

---

## 8. Backup database

Yang perlu diselamatkan hanya volume `data` (berisi SQLite + `-wal`). Salin **seluruh direktori**,
bukan `data.sqlite` saja: di mode WAL, sebagian transaksi masih di `-wal`.

Lewat panel: **Docker → Volumes** → `cartesia-gw_data` → *Backup/Download*.

Lewat terminal:

```bash
docker run --rm \
  -v cartesia-gw_data:/data:ro \
  -v /www/backup/cartesia-gw:/backup \
  alpine sh -c 'tar czf /backup/data-$(date +%F).tgz -C /data .'
```

Pastikan nama volume sesuai (`docker volume ls | grep data`). Restore: hentikan container,
kosongkan volume, ekstrak tar-nya, start lagi.

---

## Troubleshooting

**Container `restarting` terus / `docker logs` menyebut `-gateway-token is required`.**
Compose tidak menemukan `CARTESIA_GW_TOKEN` (kosong = dianggap tidak diisi). Cek
`docker compose config | grep TOKEN` dari `/www/dk_project/dk_app/cartesia-gw`; kalau kosong, berarti `.env`
tidak terbaca (mis. compose dijalankan dari direktori lain) atau Anda memakai compose yang
disimpan di panel tanpa `environment`-nya. Isi token langsung di compose seperti langkah 3.

**Build gagal dengan `dial tcp: lookup proxy.golang.org ... timeout`.**
DNS VPS bermasalah. Sementara lewati proxy Go:

```bash
GOPROXY=https://goproxy.cn,direct docker compose build
```

(di panel: tambahkan `ARG GOPROXY=https://goproxy.cn,direct` lalu `ENV` sebelum baris
`go mod download` di Dockerfile — atau build via terminal sekali saja, lalu pakai image yang
sudah ada).

**`502 Bad Gateway` di browser.**
Nginx tidak menemukan gateway. `curl http://127.0.0.1:20140/healthz` di VPS akan
memperjelas: kalau gagal, masalahnya container; kalau berhasil, masalahnya blok `proxy_pass`.

**`403 Forbidden` dengan pesan `404` dari Nginx.**
aaPanel membuat blok `location /` sendiri; pastikan blok itu sudah diganti (langkah 4.3), bukan
ditambahkan sebagai blok kedua.

**Audio terasa tertunda beberapa detik sebelum berbunyi.**
`proxy_buffering off` belum aktif. Cek dengan `curl -N` langsung terhadap container (tanpa
Nginx): kalau instan, penyebabnya buffering Nginx.

**Dashboard muncul tapi semua panel kosong / `401`.**
Token di `localStorage` salah atau tidak diisi. Ulangi langkah 5; token yang benar adalah nilai
`CARTESIA_GW_TOKEN` di `.env`.

**Ingin mengganti nama volume jadi `/data/cartesia-gw` di mana-mana.**
Tidak perlu. Cukup ingat: nama volume adalah `<project>_data`, dan proyeknya bernama
`cartesia-gw` (`name:` di `docker-compose.yml`), jadi `cartesia-gw_data` — stabil walau direktori
dipindah atau di-rename.

---

## Checklist singkat

- [ ] Docker plugin aaPanel terpasang, `docker compose version` ≥ 2.24
- [ ] `/www/dk_project/dk_app/cartesia-gw` berisi kode terbaru + `docker-compose.yml`
- [ ] `.env` berisi `CARTESIA_GW_TOKEN` (chmod 600), `CARTESIA_GW_BIND=127.0.0.1`
- [ ] Image `cartesia-gw:<tag>` ter-build
- [ ] Container running; `curl 127.0.0.1:20140/healthz` → `"status":"ok"`, `gatewayTokenIsSet:true`
- [ ] Website aaPanel punya SSL Let's Encrypt + Force HTTPS
- [ ] Blok `location /` memakai `proxy_buffering off`
- [ ] `localStorage` dashboard diisi token; panel API keys bisa dibuka
- [ ] `curl` ke `https://<domain>/v1/audio/speech` mengembalikan MP3
- [ ] Volume `cartesia-gw_data` masuk jadwal backup aaPanel/rsync
