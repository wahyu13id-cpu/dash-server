# Server Dashboard

Dashboard web untuk mengelola server: **terminal interaktif (PTY) yang langsung terbuka**,
**file manager dengan akses penuh ke filesystem**, autentikasi sesi, dan jejak audit. Backend
Go modular monolith, frontend React + TypeScript.

> ⚠️ **Penting — keamanan.** Aplikasi ini memberikan akses shell **root** dan operasi file ke
> **seluruh filesystem** server. **Hanya jalankan di jaringan tepercaya, atau di belakang
> HTTPS/VPN.** Jangan pernah mengekspos port aplikasi langsung ke internet tanpa TLS dan
> kredensial kuat. Lihat [Catatan Keamanan](#catatan-keamanan).

---

## Daftar Isi

- [Fitur](#fitur)
- [Arsitektur & Struktur](#arsitektur--struktur)
- [Prasyarat](#prasyarat)
- [Pemasangan cepat (satu perintah)](#pemasangan-cepat-satu-perintah)
- [Menjalankan secara lokal (development)](#menjalankan-secara-lokal-development)
- [Menjalankan di produksi (systemd + nginx)](#menjalankan-di-produksi-systemd--nginx)
- [Konfigurasi (environment)](#konfigurasi-environment)
- [Referensi API](#referensi-api)
- [Perintah CLI](#perintah-cli)
- [Protokol WebSocket terminal](#protokol-websocket-terminal)
- [Perintah lint / test / build](#perintah-lint--test--build)
- [Catatan Keamanan](#catatan-keamanan)
- [Batasan yang masih ada](#batasan-yang-masih-ada)
- [Menyesuaikan tampilan dengan referensi visual](#menyesuaikan-tampilan-dengan-referensi-visual)

---

## Fitur

- **Autentikasi & sesi**: login/logout, sesi server-side (token di-hash), cookie `HttpOnly`,
  proteksi CSRF (double-submit cookie), validasi Origin, rate-limit login, ganti kata sandi.
- **Profil pengguna (lengkap)**: halaman **Profil** (dari menu pengguna di kanan atas) untuk
  mengubah **nama tampilan**, **nama pengguna (login)**, dan **kata sandi** dalam satu tempat.
  Setiap perubahan wajib menyertakan kata sandi saat ini sebagai konfirmasi. Nama pengguna
  divalidasi (3-32 karakter, huruf kecil/angka/titik/strip/garis bawah) dan dicek bentrok dengan
  akun lain (kolom `username` bersifat `UNIQUE COLLATE NOCASE`), jadi tidak mungkin membuat dua
  akun dengan nama yang sama. Mengganti nama pengguna langsung berlaku untuk login berikutnya
  tanpa memutus sesi yang sedang berjalan; mengganti kata sandi **mengakhiri semua sesi lain**
  dan mempertahankan sesi ini. Halaman yang sama menampilkan ringkasan akun (peran, tanggal
  dibuat, jumlah sesi aktif, status 2FA + sisa kode pemulihan) dan detail sesi saat ini
  (waktu mulai/berakhir, alamat IP, ringkasan perangkat dari User-Agent), plus indikator
  kekuatan kata sandi langsung saat mengetik.
- **Autentikasi dua faktor (TOTP)**: kompatibel dengan Google Authenticator/Authy/1Password.
  Login menjadi dua langkah (kata sandi → kode 6 digit) tanpa membuat sesi sebelum faktor kedua
  lolos; penyiapan lewat halaman **Keamanan** dengan QR code, 10 kode pemulihan sekali pakai,
  dan proteksi anti-replay.
- **Dashboard**: pemantauan server langsung (**CPU** total + per-core + sparkline, **memori**,
  **disk** per filesystem, **jaringan** per antarmuka, **Top 10 proses** berdasarkan CPU dan
  memori), plus ringkasan uptime, sesi terminal aktif, jumlah entri di `/`, batas file, dan
  feed aktivitas dari audit log. Metrik diambil langsung dari `/proc` (tanpa dependensi luar).
- **Terminal**: shell PTY nyata (`/bin/bash`) via WebSocket yang **otomatis terbuka saat
  halaman dibuka** — tanpa manajemen sesi. Output streaming, resize, status koneksi, dan
  tombol "Mulai ulang shell". Dilengkapi **tab sesi** seperti SSH client desktop: tab pertama
  adalah shell root lokal, tab tambahan bisa berupa sesi SSH ke host tertentu. Menutup tab
  mengakhiri sesinya; berpindah halaman **tidak** menutup sesi (timeout idle 30 menit), dan
  tersedia aksi "Tutup semua sesi".
- **Terminal mengambang (jendela desktop)**: dari file manager, *Buka di Terminal* membuka shell
  root sebagai **jendela yang mengambang di atas halaman**, bukan halaman baru. Jendelanya
  berperilaku seperti jendela desktop: **digeser** dengan menyeret bilah judul, **diubah
  ukurannya** dari sudut kanan bawah, **diperkecil** jadi bilah judul saja, **memenuhi layar**
  dengan klik dua kali pada judul, ditumpuk (yang aktif naik ke depan), dan ditutup dengan tombol
  × — menutup jendela mengakhiri sesi PTY-nya saja. Jendela **bertahan saat berpindah halaman**
  (shell tetap jalan) dan daftarnya disimpan di `sessionStorage`, sehingga reload menempel ulang
  ke sesi yang masih hidup. Bisa dibuka beberapa sekaligus (maksimal 6); membuka folder yang
  sama dua kali hanya memfokuskan jendela yang sudah ada.
- **Status strip host di terminal**: satu baris ringkas di atas terminal (gaya status bar
  MobaXterm) berisi **hostname, CPU, memori, pemakaian bandwidth, dan disk** dari mesin yang
  sedang dipakai. Untuk shell lokal angkanya berasal dari sampler `/proc` yang sama dengan
  dashboard; untuk sesi SSH, angka host **remote** diambil langsung dari remote lewat koneksi
  ssh terpisah (read-only, script kecil dibaca dari stdin). Hanya tab yang terlihat yang
  melakukan polling, jadi halaman ini tidak menambah beban berarti.
- **SSH connection manager**: kelola koneksi SSH dengan sumber kebenaran **`~/.ssh/config`**
  standar OpenSSH — format yang sama yang dipakai `ssh` di terminal dan **VSCode Remote-SSH**,
  jadi host yang dibuat di sini langsung tersedia di sana (dan sebaliknya). Tambah/ubah/hapus
  host lengkap dengan Alias, HostName, User, Port, `IdentityFile`, `ProxyJump`, deskripsi, dan
  direktif tambahan apa pun; ada pratinjau blok sebelum disimpan, impor dari perintah
  `ssh user@host -p 2222` yang ditempel, favorit, catatan, pemakaian terakhir, uji koneksi
  (BatchMode, tidak pernah menggantung), konfigurasi efektif dari `ssh -G`, serta pengelolaan
  kunci host (`known_hosts`) dengan sidik jari SHA-256. Sesi dijalankan lewat **binary `ssh`
  sungguhan**, sehingga ProxyJump, agent forwarding, keyboard-interactive, dan prompt kunci host
  berperilaku persis seperti di command line. Password/passphrase opsional dapat disimpan
  **terenkripsi** (AES-256-GCM) dan diberikan ke ssh lewat `SSH_ASKPASS` — tidak pernah muncul
  di disk sebagai teks biasa, argumen proses, atau log; kredensial hanya dipakai setelah kunci
  host dipercaya, sehingga koneksi pertama tetap meminta konfirmasi sidik jari seperti `ssh` biasa.
- **File manager bergaya Google Drive**: default membuka `/root`, dapat dipindah ke path mana pun
  (`/var`, `/etc`, `/home`, …) dengan mengetikkan path pada kolom lokasi; breadcrumb, daftar
  file/folder, preview & edit teks, buat file/folder, rename, hapus (dengan konfirmasi), dan
  **unduh** berkas lewat ikon ⬇ di setiap baris (di samping ikon hapus) — berkas dialirkan apa
  adanya ke browser (mendukung resume/`Range`, tanpa dibaca seluruhnya ke memori, jadi berkas
  berukuran besar pun aman). Berkas dari laptop bisa **diunggah** lewat tombol *Unggah* atau cukup
  **drag & drop** ke halaman: tiap berkas punya progress bar sendiri, nama yang sudah ada
  ditanyakan (timpa/lewati) bukan ditimpa diam-diam, dan berkas ditulis ke file sementara lalu
  di-`rename` sehingga unggahan yang gagal tidak pernah meninggalkan file separuh di direktori
  tujuan. Interaksi daftar mengikuti pola Drive: **klik = pilih, klik dua kali = buka**,
  Ctrl/Cmd+klik menambah pilihan, Shift+klik memilih rentang, dan **klik kanan** pada baris
  maupun pada area kosong membuka menu aksi. Dari menu itu tersedia: *Buka di Terminal* (shell
  root sebagai **jendela mengambang** yang bisa digeser tepat di folder tersebut — lihat bagian
  Terminal di atas), **Salin / Potong / Tempel**, *Salin ke folder…* dan
  *Pindahkan ke folder…* dengan pemilih folder, **Unduh sebagai ZIP** (satu arsip untuk banyak
  pilihan), **Ekstrak** arsip `.zip`, **Izin…** (editor bit permission), *Properti* (pemilik,
  grup, inode, hard link, symlink), *Salin path*, dan hapus massal. Baris juga bisa
  **diseret ke folder** untuk memindahkan berkas, dan seluruh aksi tersedia lewat keyboard
  (`Ctrl+A/C/X/V`, `F2`, `Delete`, `Enter`, `Shift+F10`, panah).
- **Mode tampilan file manager (ala file manager Linux desktop)**: daftar berkas bisa
  ditampilkan dalam tiga tata letak yang dipilih dari tombol segmen di kanan atas halaman —
  **ikon** (grid ubin dengan ikon besar, centang pilihan di pojok, tombol `…` untuk menu),
  **daftar** (tabel dengan kolom Nama/Ukuran/Diubah dan tombol aksi per baris), dan
  **ringkas** (baris padat satu layar penuh dengan kolom jenis, ukuran, dan waktu). Pilihannya
  disimpan di `localStorage` (`dash.files.view`) sehingga bertahan setelah reload.
  Urutan daftar juga bisa diatur: **Nama, Jenis, Ukuran, Diubah** plus tombol arah
  naik/turun, dan header kolom pada mode daftar bisa diklik langsung untuk mengganti kunci
  urutan. Pengurutan mengikuti kebiasaan file manager Linux: **folder selalu di atas** (baik
  menaik maupun menurun), perbandingan nama *locale-aware* dan numerik sehingga `berkas2`
  muncul sebelum `berkas10` dan besar/kecil huruf tidak menentukan urutan. Saat diurutkan
  menurut **Jenis**, berkas dikelompokkan per jenis (Folder, Gambar, Video, Audio, Dokumen,
  Arsip, Kode, Teks, Lainnya) dengan judul grup dan jumlahnya — tiap jenis juga punya warna
  ikon sendiri, seperti Nautilus/Dolphin.
- **Status bar & menu klik kanan ala file manager Linux desktop**: di bagian bawah daftar ada
  **status bar teks** (menempel di atas footer, tetap terlihat saat daftar di-scroll) yang
  menampilkan ringkasan folder (`12 item · 3 folder · 9 berkas · 2,4 MB`) dan berubah menjadi
  `3 item dipilih — 2 folder · 1 berkas · 1,2 MB` saat ada yang dipilih — tanpa kartu di atas
  daftar, jadi tampilannya sama seperti Nautilus/Dolphin (seleksi cukup teks; aksinya lewat
  klik kanan, `Ctrl+C/X/V`, `F2`, `Delete`, atau `Esc` untuk membatalkan). **Menu klik kanan
  tidak lagi hilang saat halaman di-scroll**: menu terikat pada baris/area tempat ia dibuka dan
  ikut bergerak bersama konten, baru menutup kalau barisnya benar-benar keluar dari layar
  (posisinya tetap dijepit agar tidak keluar viewport).
- **Halaman Sistem (operasional inti)**: empat panel baru di grup menu **Sistem**, semuanya
  menjalankan perintah lewat `exec` dengan argumen tetap (tanpa shell) dan memvalidasi apa pun
  yang datang dari permintaan:
  - **Layanan** — daftar seluruh unit `.service` (status runtime dari `systemctl list-units`
    digabung status boot dari `list-unit-files`), pencarian, filter *Semua/Berjalan/Berhenti/
    Gagal* berikut jumlahnya, dan penyegaran otomatis. Aksi **mulai / mulai ulang / hentikan**
    tersedia per baris (hentikan & mulai ulang lewat konfirmasi). Klik nama unit membuka detail:
    properti penting (`ActiveState`, `FragmentPath`, PID utama, memori, waktu mulai, kebijakan
    `Restart`), ringkasan **log terakhir unit**, serta tombol aksi. Nama unit divalidasi sebagai
    `*.service` sehingga halaman ini tidak bisa menyentuh mount/socket/timer.
  - **Proses** — daftar proses dibaca langsung dari `/proc` (tanpa `ps`), berisi PID, pengguna,
    perintah, **CPU%**, **memori%**, RSS, status, umur proses, dan prioritas. Bisa dicari
    (PID/nama/perintah/pengguna), diurutkan menurut CPU, memori, umur, nama, atau PID, dan
    disegarkan otomatis tiap 3 detik. Aksi per baris: mengubah **nice** (−5/0/10/19) dan
    mengirim **SIGTERM** dengan konfirmasi. PID 1 (init) sengaja selalu ditolak.
  - **Log** — penampil log terpusat: pilih sumber dari daftar di kiri (journal sistem, journal
    **per unit**, atau berkas di `/var/log`) lalu baca di kanan. Untuk journal tersedia filter
    **level** (err/warning/notice/info/debug), **rentang waktu** (15 menit … 7 hari, atau sejak
    boot), jumlah baris (100–2000), dan pencarian teks (dijalankan di seluruh journal, bukan
    hanya baris yang tampil). Ada mode **otomatis 3 detik** yang menempel ke baris terbaru selama
    posisi scroll masih di bawah, tombol **bungkus baris**, **salin**, dan **unduh** `.txt`.
    Pembacaan berkas dibatasi pada `DASH_OPS_LOG_DIR` dan symlink diselesaikan lalu diverifikasi
    ulang, jadi jalur seperti `../` atau symlink keluar direktori tetap ditolak.
  - **Pembaruan** — ringkasan jumlah pembaruan, **pembaruan keamanan**, paket yang akan dihapus,
    dan penanda **perlu reboot** (dari `/var/run/reboot-required` + daftar paketnya). Daftar
    paket yang bisa diperbarui menampilkan versi terpasang → kandidat beserta asalnya.
    `Perbarui daftar paket` (apt-get update / dnf makecache) dan `Tingkatkan semua`
    (apt-get upgrade / dnf upgrade, opsional **dist-upgrade**) berjalan sebagai **pekerjaan latar
    belakang**: respons langsung kembali, keluaran perintah dialirkan ke panel yang bisa dipantau
    sampai selesai, sehingga permintaan HTTP tidak pernah menunggu puluhan menit atau terputus
    timeout. Mendukung **apt** (Debian/Ubuntu) dan **dnf/yum** (RHEL/Fedora); halaman lain
    otomatis menyembunyikan diri bila host tidak memilikinya.
  Setiap aksi yang mengubah keadaan (kontrol layanan, sinyal proses, ubah prioritas, pembaruan
  paket) dicatat ke **audit log** bersama pengguna dan IP-nya; percobaan dengan nilai tidak sah
  juga tercatat sebagai `security.denied`.
- **UI**: sidebar dapat diciutkan, responsif untuk desktop/tablet/ponsel, serta loading,
  empty, dan error state di semua halaman.

---

## Arsitektur & Struktur

Backend memisahkan **handler HTTP**, **business logic**, dan **akses data**:

```
.
├── cmd/server/            # entry point aplikasi
├── internal/
│   ├── auth/              # autentikasi, sesi, hashing password, 2FA (TOTP)
│   ├── config/            # konfigurasi dari environment + validasi
│   ├── db/                # buka SQLite + runner migrasi
│   ├── totp/              # TOTP RFC 6238, kode pemulihan, QR (stdlib + go-qrcode)
│   ├── terminal/          # PTY, lifecycle sesi, metadata sesi
│   ├── files/             # business logic operasi file
│   ├── sshconfig/         # parser + penulis ~/.ssh/config (termasuk Include)
│   ├── sshsvc/            # domain SSH: host, uji koneksi, known_hosts, sesi
│   ├── sshsecret/         # kredensial SSH terenkripsi (AES-256-GCM + HKDF)
│   ├── sshaskpass/        # broker SSH_ASKPASS via unix socket (0600)
│   ├── sysinfo/           # sampling metrik host dari /proc (CPU/memori/disk/net/proses)
│   ├── sysops/            # operasi host: systemd, proses+sinyal, log, pembaruan paket
│   ├── workspace/         # validasi batas akses filesystem (sandbox opsional)
│   ├── audit/             # pencatatan aksi penting
│   └── httpapi/           # routing, middleware, handler HTTP + WebSocket
├── migrations/            # migrasi SQL (embedded ke binary)
├── frontend/              # React + TypeScript (Vite)
├── deploy/
│   ├── nginx/             # template reverse proxy (di-render oleh install.sh)
│   └── systemd/           # unit service
├── .github/workflows/     # CI (uji) + Release (aset rilis untuk installer)
├── install.sh             # pemasangan cepat satu perintah (lihat di bawah)
├── .env                   # konfigurasi PRODUKSI (dimuat otomatis, tidak di-commit)
├── .env.example           # template untuk referensi
└── Makefile
```

Komunikasi: **REST** untuk operasi biasa, **WebSocket** untuk stream terminal.
Database: **SQLite** (pengguna + status 2FA, sesi, metadata sesi terminal, audit).

---

## Prasyarat

- **Go 1.24+**
- **Node.js 20+** dan npm (untuk membangun frontend)
- **SQLite** (opsional untuk inspeksi manual; driver sudah tertanam di binary)
- Untuk produksi: **nginx** dan **systemd**

---

## Pemasangan cepat (satu perintah)

### Cara tercepat — satu perintah dari GitHub

Di server baru (baru saja di-*install* OS-nya), satu perintah ini memasang semuanya:

```bash
curl -fsSL https://raw.githubusercontent.com/wahyu13id-cpu/dash-server/main/install.sh | sudo bash
```

Script itu akan mengunduh kode dari repo sendiri, memakai **aset Release** siap-pakai bila ada
(biner + frontend; tidak perlu Go/Node), dan **membangun dari sumber** bila Release belum
dibuat — dependensi dipasang otomatis. Ingin memeriksa isi script lebih dulu:

```bash
curl -fsSL https://raw.githubusercontent.com/wahyu13id-cpu/dash-server/main/install.sh -o /tmp/install.sh
less /tmp/install.sh          # baca dulu, lalu:
sudo bash /tmp/install.sh
```

Repo lain / repo privat dapat ditunjuk lewat flag:

```bash
# repo lain (branch main)
curl -fsSL https://raw.githubusercontent.com/wahyu13id-cpu/dash-server/main/install.sh | sudo bash -s -- --repo user/repo

# repo privat: berikan token baca (fine-grained, izin Contents: read) — tidak disimpan
sudo bash install.sh --repo user/repo-privat --repo-token github_pat_xxx --yes

# paksa membangun dari sumber (abaikan aset Release)
sudo bash install.sh --no-release --yes
```

### Cara klasik — dari direktori proyek

Untuk server baru, seluruh proses — dependensi, build, pengguna & direktori, rahasia,
systemd, nginx, HTTPS, sampai **akun admin pertama** — dikerjakan oleh `install.sh`:

```bash
cd /path/ke/sumber/dashboard
sudo bash install.sh          # atau: sudo make quick-install
```

Script menanyakan tiga hal (semua bisa dilewati dengan Enter):

1. **Akses** — punya domain (HTTPS otomatis via Let's Encrypt) atau tanpa domain (lewat IP).
   **Domain bersifat opsional**: tanpa domain, dashboard tetap langsung bisa dipakai.
2. **Akun admin** — nama pengguna + kata sandi (min. 10 karakter, mengandung huruf dan angka).
   Kosongkan kata sandi agar dibuatkan acak dan ditampilkan **sekali** di akhir pemasangan.
3. **Opsi lanjutan** — port internal, batas file manager (`/` = akses penuh atau kotak pasir),
   dan apakah port 80/443 dibuka di firewall (ufw/firewalld).

Di akhir, script mencetak URL, nama pengguna, dan (bila dibuatkan) kata sandi login.

### Pemakaian non-interaktif

```bash
# Domain + HTTPS Let's Encrypt
sudo bash install.sh --domain dash.example.com --email saya@example.com --generate-password --yes

# Tanpa domain, HTTPS self-signed (browser akan memberi peringatan sertifikat)
sudo bash install.sh --no-domain --tls selfsigned --yes

# Tanpa domain, HTTP langsung ke port 8080 (paling sederhana)
sudo bash install.sh --yes

# Lihat seluruh rencana TANPA mengubah apa pun
sudo bash install.sh --domain dash.example.com --dry-run

# Perbarui pemasangan yang sudah ada (biner + frontend baru, konfigurasi & nginx dipertahankan)
sudo bash install.sh

# Tulis ulang konfigurasi (tanya ulang semua opsi)
sudo bash install.sh --reconfigure

# Lepaskan pemasangan (tambahkan --purge untuk sekalian menghapus basis data)
sudo bash install.sh --uninstall
```

Jalankan `bash install.sh --help` untuk daftar lengkap opsi.

### Apa saja yang dikerjakan

| Langkah | Rincian |
| --- | --- |
| Dependensi | `curl openssl xz-utils sqlite3 nginx` (+ `certbot` bila memakai domain). **Go** diambil dari rilis resmi go.dev dan **Node.js 20+** dari nodejs.org hanya bila belum ada/terlalu lama, dengan verifikasi checksum SHA-256. |
| Build | Bila aset **Release** tersedia (hanya pada pemasangan lewat `curl \| bash`), biner + frontend diunduh dan checksum SHA-256-nya diverifikasi. Jika tidak, dibangun dari sumber: `go build ./cmd/server` dan `npm ci && npm run build`. Otomatis dilewati bila `bin/dashboard` + `frontend/dist` sudah ada (dipaksa dengan `--rebuild`). |
| Pengguna & direktori | pengguna sistem `dashboard`, direktori `/opt/dashboard`, `/etc/dashboard`, `/var/lib/dashboard`, `/run/dashboard`. |
| Rahasia | `DASH_SESSION_SECRET` 64 karakter acak (dipakai ulang pada pemasangan berikutnya agar sesi lama tetap valid). |
| Konfigurasi | `/etc/dashboard/dashboard.env` (mode `0640`) berisi semua variabel produksi. |
| Layanan | unit systemd `dashboard.service` (berjalan sebagai **root** — lihat [Catatan Keamanan](#catatan-keamanan)). |
| Akun admin | dibuat sekali saat start pertama dari `DASH_BOOTSTRAP_USER`/`DASH_BOOTSTRAP_PASSWORD`. Kata sandi itu **dihapus kembali dari file env** setelah akun terbukti ada di database. |
| nginx | vhost dari `deploy/nginx/dashboard.conf.template` ke `sites-available` (Debian) atau `conf.d` (RHEL); WebSocket siap; `client_max_body_size` mengikuti `DASH_MAX_UPLOAD_SIZE`. |
| HTTPS | Let's Encrypt via certbot (langsung dengan redirect) atau sertifikat self-signed (`--tls selfsigned`). |
| Verifikasi | status layanan, `/api/health` lokal, URL publik, dan keberadaan akun admin di database. |

Bila `certbot` gagal (mis. DNS domain belum mengarah ke server atau port 80 belum terbuka),
dashboard tetap terpasang lewat HTTP dan script mencetak perintah certbot untuk dijalankan nanti.

Semua langkah dicatat ke `/var/log/dashboard-install-<tanggal>.log`. Untuk menguji helper
internal skrip tanpa menyentuh sistem: `bash install.sh --self-test`.

---

## Menjalankan secara lokal (development)

```bash
# 1. Konfigurasi
# Server memuat .env di direktori ini secara otomatis (env asli menang).
# File .env yang ada berisi nilai PRODUKSI. Untuk development, salin template:
cp .env.example .env.dev
# ...lalu jalankan dengan file itu: DASH_ENV_FILE=.env.dev make run
# atau cukup edit .env dan sesuaikan untuk lokal.

# Buat secret sesi (bila belum ada):
openssl rand -hex 32        # salin hasilnya ke DASH_SESSION_SECRET

# Untuk pengembangan lokal lewat http://localhost, set:
#   DASH_COOKIE_SECURE=false
#   DASH_ALLOWED_ORIGINS=http://localhost:5173,http://127.0.0.1:5173
#   DASH_BOOTSTRAP_PASSWORD=passwordku123   (agar tidak perlu menyalin password acak)

# 2. (Opsional) build frontend agar disajikan oleh server Go
make build-frontend

# 3. Jalankan backend
make run            # setara: go run ./cmd/server
```

Buka **http://localhost:8080**.

Untuk pengembangan frontend dengan hot-reload:

```bash
make dev-frontend   # Vite di http://localhost:5173, otomatis proxy /api ke :8080
# jalankan `make run` di terminal lain
```

> Saat pertama kali dijalankan, jika tabel `users` kosong, akun admin dibuat. Bila
> `DASH_BOOTSTRAP_PASSWORD` kosong, password acak dicetak **sekali** di stdout dan di log.
> **Segera ubah** password tersebut melalui menu pengguna → *Ubah kata sandi*.

---

## Menjalankan di produksi (systemd + nginx)

> **Cara tercepat:** jalankan `sudo bash install.sh` — lihat
> [Pemasangan cepat](#pemasangan-cepat-satu-perintah). Seluruh langkah di bawah ini
> dilakukan otomatis oleh script tersebut. Ikuti langkah manual ini bila Anda ingin
> mengendalikan setiap detail pemasangan.

Semua langkah di bawah dijalankan sebagai root.

> ⚠️ **Service berjalan sebagai root (terminal = shell root penuh).**
> Unit `deploy/systemd/dashboard.service` memakai `User=root` agar terminal web memberi
> shell root yang benar-benar berkuasa (`CapEff=000001ffffffffff`, bisa `apt`, mengubah
> `/etc`, dsb.). Karena itu sebagian besar hardening systemd (`NoNewPrivileges`,
> `CapabilityBoundingSet=`, `ProtectSystem`, `ProtectHome`, `RestrictSUIDSGID`,
> `ReadWritePaths`) **sengaja dimatikan** — kata lain, proses HTTP API, file manager, dan
> akses database juga berjalan sebagai root. Perlakukan dashboard ini setara dengan akses
> root jarak jauh: **wajib** di belakang HTTPS + firewall/VPN dan jangan pernah diekspos
> ke jaringan tak tepercaya. Untuk kembali ke mode non-root, ubah `User=root`/`Group=root`
> di unit menjadi `dashboard` dan aktifkan kembali opsi hardening yang relevan.

### 1. Build

```bash
cd /var/www/dashboard
make build-all          # build frontend -> frontend/dist, binary -> bin/dashboard
```

### 2. Pengguna, direktori, dan secrets

```bash
useradd --system --home /var/lib/dashboard --shell /usr/sbin/nologin dashboard
install -d -o dashboard -g dashboard -m 0750 /var/lib/dashboard
install -d -o dashboard -g dashboard -m 0750 /srv/dashboard/workspace
install -d -m 0750 /etc/dashboard
install -d -m 0755 /opt/dashboard
```

> Catatan: karena service berjalan sebagai `root`, user `dashboard` di atas hanya dipakai
> sebagai **pemilik file database**, bukan lagi pemilik proses. Root tetap dapat
> membaca/menulisnya. Baris `/srv/dashboard/workspace` hanya diperlukan bila Anda memilih
> `DASH_WORKSPACE_ROOT` yang sempit (sandbox).

Buat `/etc/dashboard/dashboard.env` (mode `0640`, owner `root:dashboard`):

```bash
cat > /etc/dashboard/dashboard.env <<'EOF'
DASH_ADDR=127.0.0.1:8080
DASH_STATIC_DIR=/opt/dashboard/frontend/dist
DASH_ALLOWED_ORIGINS=https://dash.example.com,http://dash.example.com
DASH_COOKIE_SECURE=false          # ubah ke true setelah HTTPS aktif
DASH_TRUSTED_PROXY=true
DASH_SESSION_SECRET=GANTI_DENGAN_openssl_rand_hex_32
DASH_BOOTSTRAP_USER=admin
DASH_BOOTSTRAP_PASSWORD=
DASH_DB_PATH=/var/lib/dashboard/dashboard.db
DASH_WORKSPACE_ROOT=/              # "/" = akses penuh; path sempit = sandbox
DASH_HOME_DIR=/root
DASH_LOG_LEVEL=info
EOF
chown root:dashboard /etc/dashboard/dashboard.env
chmod 0640 /etc/dashboard/dashboard.env
```

### 3. Pasang binary, frontend, dan service

```bash
install -m 0755 bin/dashboard /usr/local/bin/dashboard
install -d -m 0755 /opt/dashboard
cp -r frontend/dist /opt/dashboard/frontend-dist
# sesuaikan path di env bila berbeda, mis. DASH_STATIC_DIR=/opt/dashboard/frontend-dist

install -m 0644 deploy/systemd/dashboard.service /etc/systemd/system/dashboard.service
systemctl daemon-reload
systemctl enable --now dashboard
systemctl status dashboard
```

> Jika password acak dicetak di log, lihat dengan `journalctl -u dashboard -n 30`.

### 4. nginx

```bash
install -m 0644 deploy/nginx/upgrade-map.conf /etc/nginx/conf.d/upgrade-map.conf
# vhost di-render dari template (ganti nama domain sesuai milik Anda):
sed -e 's|__SERVER_NAME__|dash.example.com|g' \
    -e 's|__DEFAULT_SERVER__||g' \
    -e 's|__APP_PORT__|8080|g' \
    -e 's|__MAX_BODY__|512m|g' \
    deploy/nginx/dashboard.conf.template \
    > /etc/nginx/sites-available/dash.example.com.conf
ln -sf /etc/nginx/sites-available/dash.example.com.conf \
    /etc/nginx/sites-enabled/dash.example.com.conf
rm -f /etc/nginx/sites-enabled/default     # opsional
nginx -t && systemctl reload nginx
```

### 5. Aktifkan HTTPS

```bash
apt-get install -y certbot python3-certbot-nginx
certbot --nginx -d dash.example.com
# Setelah sertifikat aktif, ubah:
#   DASH_COOKIE_SECURE=true
# lalu:
systemctl restart dashboard
```

Verifikasi:

```bash
curl -s http://127.0.0.1:8080/api/health
curl -sI https://dash.example.com | head
```

---

## Konfigurasi (environment)

Server memuat file `.env` di direktori kerja secara otomatis saat startup (dapat diubah
dengan `DASH_ENV_FILE`). Variabel yang sudah ada di environment **tidak** ditimpa, sehingga
systemd/shell selalu menang. Contoh nilai ada di [`.env.example`](.env.example); file
[`.env`](.env) berisi konfigurasi **produksi** dan tidak di-commit (lihat `.gitignore`).

Ringkasan variabel:

| Variabel | Default | Keterangan |
| --- | --- | --- |
| `DASH_ENV_FILE` | `./.env` | Path file env yang dimuat (opsional). |
| `DASH_ADDR` | `:8080` | Alamat listen HTTP. |
| `DASH_DB_PATH` | `./data/dashboard.db` | Lokasi file SQLite. |
| `DASH_WORKSPACE_ROOT` | `./workspace` | Direktori teratas yang boleh disentuh file manager. Set `/` untuk akses penuh ke seluruh filesystem. |
| `DASH_HOME_DIR` | `/root` | Direktori kerja (dan `HOME`) untuk shell PTY baru. |
| `DASH_STATIC_DIR` | `./frontend/dist` | Direktori build frontend. Kosong = SPA tidak disajikan. |
| `DASH_SESSION_SECRET` | — (**wajib**) | ≥ 32 karakter. |
| `DASH_ALLOWED_ORIGINS` | `https://dash.example.com` | Daftar origin yang diizinkan (REST + WS). |
| `DASH_COOKIE_SECURE` | `true` | Set `false` hanya untuk http lokal. |
| `DASH_TRUSTED_PROXY` | `true` | Percaya `X-Forwarded-*` dari reverse proxy. |
| `DASH_SESSION_TTL` | `12h` | Masa berlaku sesi login. |
| `DASH_SHELL` | `/bin/bash` | Shell untuk PTY (tidak dibangun dari input pengguna). |
| `DASH_TERMINAL_MAX_SESSIONS` | `8` | Batas sesi PTY global. |
| `DASH_TERMINAL_MAX_PER_USER` | `8` | Batas sesi PTY per pengguna (dinaikkan agar cukup untuk tab). |
| `DASH_TERMINAL_IDLE_TIMEOUT` | `30m` | Sesi tanpa klien ditutup otomatis. Saat kedua batas di atas penuh, sesi tanpa klien yang sudah diam ≥ 2 menit diambil alih lebih dulu (lihat catatan di bawah). |
| `DASH_TERMINAL_HISTORY` | `true` | Simpan riwayat perintah shell ke `$HOME/.bash_history` (bertahan antar sesi). |
| `DASH_MONITOR_INTERVAL` | `2s` | Interval sampling metrik host (CPU/memori/disk/jaringan/proses) untuk dashboard. |
| `DASH_TOTP_ISSUER` | `Server Dashboard` | Nama penerbit yang tampil di aplikasi autentikator (Google Authenticator dkk.). |
| `DASH_SSH_ENABLED` | `true` | Aktifkan SSH connection manager. |
| `DASH_SSH_CONFIG` | `$DASH_HOME_DIR/.ssh/config` | File konfigurasi SSH yang boleh ditulis. File yang masuk lewat `Include` selalu hanya baca. |
| `DASH_SSH_BIN` | `ssh` | Binary ssh untuk sesi dan diagnostik (`ssh -G`, uji koneksi). |
| `DASH_SSH_KEYSCAN_BIN` | `ssh-keyscan` | Dipakai saat memindai kunci host. |
| `DASH_SSH_KEYGEN_BIN` | `ssh-keygen` | Dipakai untuk `known_hosts` (`-F`/`-R`) dan sidik jari kunci. |
| `DASH_SSH_STATS` | `true` | Probe read-only untuk angka host **remote** pada status strip terminal (CPU, memori, bandwidth, disk). Sesi shell lokal tidak terpengaruh; set `false` untuk mematikan probe sekaligus. |
| `DASH_OPS_ENABLED` | `true` | Aktifkan halaman **Sistem**: manajer layanan systemd, daftar proses, penampil log, dan info pembaruan paket. |
| `DASH_OPS_SYSTEMCTL_BIN` | `systemctl` | Binary systemd untuk kontrol layanan. Kosongkan untuk mematikan kontrol layanan pada host tanpa systemd. |
| `DASH_OPS_JOURNALCTL_BIN` | `journalctl` | Binary journalctl untuk membaca journal systemd. |
| `DASH_OPS_LOG_DIR` | `/var/log` | Satu-satunya direktori yang boleh dibaca penampil log. Symlink diselesaikan lalu diverifikasi ulang agar tidak bisa keluar dari direktori ini. |
| `DASH_OPS_PROC_ROOT` | `/proc` | Titik mount procfs untuk daftar proses dan sinyal. |
| `DASH_MAX_READ_SIZE` | `2M` | Batas baca file (endpoint content). |
| `DASH_MAX_WRITE_SIZE` | `5M` | Batas tulis file. |
| `DASH_MAX_UPLOAD_SIZE` | `512M` | Batas ukuran **satu berkas** yang diunggah lewat file manager. Wajib diselaraskan dengan `client_max_body_size 512m;` pada nginx, karena proxy menolak lebih dulu sebelum aplikasi sempat membaca body. |
| `DASH_MAX_LIST_ENTRIES` | `5000` | Jumlah maksimum entri per listing direktori (sisanya ditandai truncated). |
| `DASH_AUDIT_RETENTION_DAYS` | `30` | Audit lebih tua dari ini dipangkas; `0` menonaktifkan. |
| `DASH_BOOTSTRAP_USER` | `admin` | Akun admin awal (dibuat bila tabel users kosong). |
| `DASH_BOOTSTRAP_PASSWORD` | — | Bila kosong, password acak dibuat & dicetak sekali. |
| `DASH_LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error`. |

Konfigurasi divalidasi saat startup; aplikasi menolak berjalan bila tidak valid (mis.
`DASH_SESSION_SECRET` terlalu pendek, shell tidak ada).

---

## Referensi API

Semua endpoint mengembalikan error dengan bentuk konsisten:

```json
{ "error": { "code": "not_found", "message": "path not found" } }
```

Endpoint ber-otentikasi memakai cookie `dash_session`. Permintaan yang mengubah data
(`POST`/`PUT`/`PATCH`/`DELETE`) **wajib** menyertakan header `X-CSRF-Token` yang sama dengan
cookie `dash_csrf`.

| Method | Path | Auth | Deskripsi |
| --- | --- | --- | --- |
| `GET` | `/api/health` | — | Status server (tanpa rahasia). |
| `POST` | `/api/auth/login` | — | Langkah 1 login. Tanpa 2FA: mengeset cookie sesi + CSRF dan membalas `{"status":"ok"}`. Dengan 2FA: membalas `{"status":"totp_required","challenge":"…"}` **tanpa** cookie. |
| `POST` | `/api/auth/login/totp` | — | Langkah 2 login (`{"challenge","code"}`); `code` boleh kode TOTP 6 digit atau kode pemulihan. Mengeset cookie sesi + CSRF. |
| `POST` | `/api/auth/logout` | ✅ | Menghapus sesi. |
| `GET` | `/api/auth/me` | ✅ | Data pengguna aktif. |
| `POST` | `/api/auth/password` | ✅ | Ganti kata sandi; mengakhiri sesi lain. |
| `GET` | `/api/auth/profile` | ✅ | Profil lengkap: data akun, jumlah sesi aktif, status 2FA + sisa kode pemulihan, detail sesi ini (mulai, berakhir, IP, User-Agent), dan batas kebijakan nama pengguna/kata sandi. |
| `PATCH` | `/api/auth/profile` | ✅ | Ubah profil sendiri (`{"currentPassword","username?","displayName?","newPassword?"}`). Field yang tidak dikirim tidak diubah; `currentPassword` selalu wajib (salah → `401`, nama terpakai → `409`, nama tidak valid → `400 invalid_username`, kata sandi lemah → `400 weak_password`). Bila `newPassword` diisi, semua sesi lain diakhiri dan cookie sesi + CSRF dirotasi untuk peramban ini. |
| `GET` | `/api/auth/totp` | ✅ | Status 2FA: aktif, sisa kode pemulihan, penerbit, akun. |
| `POST` | `/api/auth/totp/setup` | ✅ | Mulai penyiapan: mengembalikan `secret`, `uri` `otpauth://`, dan QR (`qrPng` data-URI). Secret belum aktif. |
| `POST` | `/api/auth/totp/activate` | ✅ | Konfirmasi kode (`{"code"}`) → 2FA aktif, mengembalikan 10 `recoveryCodes` (hanya sekali). |
| `POST` | `/api/auth/totp/disable` | ✅ | Nonaktifkan 2FA (`{"password","code"}`). |
| `POST` | `/api/auth/totp/recovery/regenerate` | ✅ | Buat ulang kode pemulihan (`{"password","code"}`); kode lama langsung tidak berlaku. |
| `GET` | `/api/stats` | ✅ | Ringkasan dashboard. |
| `GET` | `/api/system` | ✅ | Snapshot metrik host: CPU, memori, disk, jaringan, Top 10 proses. |
| `GET` | `/api/audit` | ✅ | Aktivitas terbaru (audit log). |
| `GET` | `/api/files?path=` | ✅ | Daftar isi direktori. |
| `GET` | `/api/files/content?path=` | ✅ | Baca konten teks file. |
| `GET` | `/api/files/download?path=` | ✅ | Unduh berkas apa adanya (`Content-Disposition: attachment`, `application/octet-stream`, mendukung `Range`/resume). Direktori ditolak `400`. |
| `PUT` | `/api/files/content?path=` | ✅ | Tulis konten file (`{"content":"..."}`). |
| `POST` | `/api/files` | ✅ | Buat file/folder (`{"path","kind":"file|folder"}`). |
| `POST` | `/api/files/upload?path=<dir>&name=<file>` | ✅ | Unggah satu berkas: body = isi berkas mentah (`application/octet-stream`, bukan multipart), `overwrite=1` untuk menimpa. `409` bila nama sudah ada, `413` bila melebihi `DASH_MAX_UPLOAD_SIZE`. |
| `PATCH` | `/api/files` | ✅ | Rename/move (`{"from","to"}`). |
| `DELETE` | `/api/files?path=` | ✅ | Hapus file/folder. |
| `POST` | `/api/files/copy` | ✅ | Salin berkas/folder (`{"from","to","overwrite"}`). Folder disalin rekursif, symlink dibuat ulang sebagai symlink, dan folder tidak bisa disalin ke dalam subdirektori dirinya sendiri (`400`). `409` bila tujuan sudah ada tanpa `overwrite`. |
| `POST` | `/api/files/move` | ✅ | Pindahkan (`{"from","to"}`) — `rename` bila memungkinkan, dengan fallback salin+lhapus antar-device. Tujuan yang sudah ada ditolak `409` (tidak pernah menimpa). |
| `PATCH` | `/api/files/mode` | ✅ | Ubah izin (`{"path","mode":"0644"}`). Hanya sembilan bit permission yang diterima: setuid/setgid/sticky dan nilai di atas `0777` ditolak `400`. |
| `GET` | `/api/files/stat` | ✅ | Properti rinci satu path: `owner`, `group`, `uid`, `gid`, `links`, `inode`, `linkTarget`. |
| `GET` | `/api/files/zip?path=&path=` | ✅ | Alirkan berkas/folder sebagai arsip ZIP (`application/zip`, dibuat sambil dikirim sehingga folder besar tidak perlu file sementara). `path` boleh diulang untuk membungkus banyak pilihan jadi satu arsip; `name` mengubah nama unduhan. |
| `POST` | `/api/files/unzip` | ✅ | Ekstrak arsip (`{"path","dest","overwrite"}`). Nama entri divalidasi agar tidak bisa keluar dari `dest` (zip-slip ditolak `400`), ekspansi dibatasi, symlink di dalam arsip dilewati, dan tidak ada yang ditulis sebelum seluruh arsip lolos validasi. `dest` kosong = folder di samping arsip dengan nama arsip tersebut. |
| `GET` | `/api/terminal/sessions` | ✅ | Daftar sesi terminal (termasuk `kind` dan `target`). |
| `POST` | `/api/terminal/sessions` | ✅ | Buat sesi PTY baru; `{"kind":"shell"}` atau `{"kind":"ssh","target":"<alias>"}`, plus `cols`/`rows`. `dir` (opsional, hanya untuk shell lokal) memulai shell di direktori tersebut — dipakai tombol *Buka di Terminal* di file manager; path divalidasi lewat sandbox workspace dan `400` bila bukan direktori yang ada. |
| `WS` | `/api/terminal/sessions/{id}/stream` | ✅ | Stream PTY (cookie + validasi Origin). |
| `DELETE` | `/api/terminal/sessions/{id}` | ✅ | Tutup sesi & proses grupnya. |
| `GET` | `/api/terminal/sessions/{id}/stats` | ✅ | Metrik host untuk status strip sesi: `scope` (`local`/`remote`), hostname, CPU, memori, jaringan, disk. Selalu `200`; `available:false` + `message` bila angka tidak bisa dibaca (mis. kunci host belum dipercaya). |
| `GET` | `/api/ssh/state` | ✅ | Seluruh data halaman SSH: host, blok global/wildcard, berkas config, kunci `~/.ssh`, peringatan. |
| `GET` | `/api/ssh/keys` | ✅ | Daftar kunci privat di `~/.ssh` beserta sidik jari (dari file `.pub`). |
| `POST` | `/api/ssh/parse` | ✅ | Ubah perintah `ssh …` menjadi field host (`{"command":"ssh user@host -p 2222"}`). |
| `POST` | `/api/ssh/hosts` | ✅ | Tambah blok `Host` baru. |
| `GET` | `/api/ssh/hosts/{alias}` | ✅ | Satu host + status kredensial (`hasPassword`, `hasPassphrase`). |
| `PATCH` | `/api/ssh/hosts/{alias}` | ✅ | Ubah host; mengganti `alias` akan memindahkan kredensial & catatan. |
| `DELETE` | `/api/ssh/hosts/{alias}` | ✅ | Hapus host beserta kredensial dan catatannya. |
| `POST` | `/api/ssh/hosts/{alias}/favorite` | ✅ | Tandai/lepas favorit. |
| `PUT` | `/api/ssh/hosts/{alias}/note` | ✅ | Simpan catatan (hanya di dashboard, tidak ke `~/.ssh/config`). |
| `GET` | `/api/ssh/hosts/{alias}/effective` | ✅ | Konfigurasi efektif dari `ssh -G`. |
| `POST` | `/api/ssh/hosts/{alias}/test` | ✅ | Uji koneksi tanpa prompt (`BatchMode`); mengembalikan status klasifikasi. |
| `PUT` | `/api/ssh/hosts/{alias}/secret` | ✅ | Simpan password/passphrase (`{"kind":"password\|passphrase","value":"…"}`). Nilai **tidak pernah** dikembalikan. |
| `DELETE` | `/api/ssh/hosts/{alias}/secret?kind=` | ✅ | Hapus satu atau semua kredensial host tersebut. |
| `GET` | `/api/ssh/hosts/{alias}/hostkey` | ✅ | Status kepercayaan kunci host dari `known_hosts`. |
| `POST` | `/api/ssh/hosts/{alias}/hostkey/scan` | ✅ | Pindai kunci host (`ssh-keyscan`) untuk dibandingkan sidik jarinya. |
| `POST` | `/api/ssh/hosts/{alias}/hostkey/trust` | ✅ | Setujui kunci host dan tulis ke `known_hosts` (trust-on-first-use eksplisit). |
| `DELETE` | `/api/ssh/hosts/{alias}/hostkey` | ✅ | Hapus entri host dari `known_hosts`. |
| `GET` | `/api/ops/availability` | ✅ | Kemampuan host: `systemd`, `journal`, `processes`, `packageManager` (`apt`/`dnf`/`yum`/kosong), `rebootRequired`, `logDir`. Dipakai UI untuk menyembunyikan menu yang tidak didukung. |
| `GET` | `/api/ops/services` | ✅ | Daftar seluruh unit `.service`: status runtime (`load`,`active`,`sub`), status boot (`unitFile`), keterangan, plus ringkasan jumlah berjalan/gagal. |
| `GET` | `/api/ops/services/{unit}` | ✅ | Detail satu unit: properti `systemctl show` terpilih, plus 100 entri log terakhir unit tersebut. |
| `POST` | `/api/ops/services/{unit}/action` | ✅ | Jalankan aksi (`{"action":"start\|stop\|restart\|reload\|try-restart\|enable\|disable\|mask\|unmask"}`). Nama unit wajib berakhiran `.service`. |
| `GET` | `/api/ops/processes?sort=&q=&user=&limit=` | ✅ | Tabel proses dari `/proc`: PID, induk, pengguna, status, nice, CPU%, memori%, RSS, jumlah thread, umur. `sort` ∈ `cpu`/`mem`/`pid`/`name`/`time`. |
| `POST` | `/api/ops/processes/{pid}/signal` | ✅ | Kirim sinyal (`{"signal":"TERM\|KILL\|INT\|HUP\|QUIT\|USR1\|USR2\|STOP\|CONT\|ABRT\|ALRM"}`). PID 1 selalu ditolak. |
| `POST` | `/api/ops/processes/{pid}/nice` | ✅ | Ubah prioritas (`{"nice":-20…19}`). |
| `GET` | `/api/ops/logs/sources` | ✅ | Sumber log yang bisa dipilih: journal sistem, journal per unit, dan berkas di `DASH_OPS_LOG_DIR`. |
| `GET` | `/api/ops/logs/journal?unit=&priority=&since=&lines=&search=` | ✅ | Entri journal terstruktur (`time`,`priority`,`unit`,`identifier`,`pid`,`message`). `priority` ∈ `emerg…debug`, `since` ∈ `15m`/`1h`/`24h`/`7d`/`boot`/`today`/`yesterday`, `lines` ≤ 2000, `search` dijalankan di seluruh journal (literal, tanpa regex). |
| `GET` | `/api/ops/logs/file?path=&lines=&search=` | ✅ | Ekor satu berkas log. Path wajib berada di dalam `DASH_OPS_LOG_DIR` (symlink diselesaikan ulang dan diperiksa) dan wajib berkas biasa; maksimal 1 MiB terakhir dibaca. |
| `GET` | `/api/ops/packages?refresh=1` | ✅ | Status pembaruan: daftar paket (versi terpasang → kandidat, asal, penanda keamanan), jumlah, `removals`, `rebootRequired` + paket pemicunya. Hasil simulasi di-cache 5 menit; `refresh=1` memaksa perhitungan ulang. |
| `POST` | `/api/ops/packages/refresh` | ✅ | Mulai pekerjaan latar belakang `apt-get update` / `dnf makecache`. Mengembalikan objek pekerjaan (`202`). |
| `POST` | `/api/ops/packages/upgrade` | ✅ | Mulai pekerjaan latar belakang peningkatan paket (`{"dist":false\|true}`). `202` + objek pekerjaan. |
| `GET` | `/api/ops/jobs/{id}` | ✅ | Status pekerjaan latar belakang: `running`, `exitCode`, `output` (maks. 4000 baris terakhir), `truncated`, `error`. |

Contoh:

```bash
# login (cookie disimpan)
curl -s -c jar.txt -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"rahasia123"}' \
  https://dash.example.com/api/auth/login

# CSRF token dari cookie
CSRF=$(awk '$6=="dash_csrf"{print $7}' jar.txt)

# buat file
curl -s -b jar.txt -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -d '{"path":"catatan.txt","kind":"file"}' \
  https://dash.example.com/api/files
```

---

## Protokol WebSocket terminal

Pesan berupa JSON. Klien → server:

```jsonc
{ "type": "input",  "data": "ls -la\n" }   // dikirim apa adanya ke stdin PTY
{ "type": "resize", "cols": 120, "rows": 30 }
{ "type": "ping" }
```

Server → klien:

```jsonc
{ "type": "ready",  "id": "..." }           // koneksi siap
{ "type": "output", "data": "..." }         // output streaming (termasuk replay scrollback)
{ "type": "exit",   "code": 0 }             // proses berakhir
{ "type": "pong" }
```

Keepalive memakai control frame ping/pong WebSocket (server → klien setiap 25 detik).

---

## Perintah lint / test / build

```bash
make help            # daftar semua target
make test            # seluruh tes Go
make test-security   # tes keamanan (traversal, symlink, auth, origin, CSRF)
make test-race       # tes dengan race detector
make cover           # ringkasan coverage
make lint            # go vet + typecheck TypeScript
make build-all       # build frontend + backend
```

---

## Perintah CLI

Binary yang sama juga menyediakan perintah administratif (tanpa menjalankan server):

```bash
dashboard help                      # daftar perintah
dashboard reset-totp --user admin   # matikan 2FA sebuah akun (break-glass)
dashboard reset-ssh-key             # hapus SEMUA kredensial SSH tersimpan
dashboard reset-ssh-key --alias web1  # hapus kredensial satu host saja
```

`reset-totp` menghapus secret TOTP **dan** seluruh kode pemulihan akun tersebut, lalu mencatat
aksinya di audit log. Pakai hanya bila ponsel **dan** kode pemulihan hilang: dijalankan langsung
di host (butuh akses filesystem ke database). Perintah ini mengikuti `DASH_DB_PATH`,
`DASH_ENV_FILE`, dan `.env` dengan aturan yang sama seperti server.

`reset-ssh-key` dipakai bila `DASH_SESSION_SECRET` berubah sehingga kredensial SSH tersimpan
tidak lagi dapat dibuka: perintah ini menghapus ciphertext yang tidak terpakai. Password SSH di
`~/.ssh/config` tidak pernah disimpan, jadi host tetap bisa dipakai — hanya perlu mengetik
password sekali lagi. `dashboard askpass <prompt>` adalah helper internal untuk menguji/memanggil
proses yang sama; di sesi sesungguhnya `ssh` **memanggil binary ini langsung** (tanpa shell, tanpa
argumen tambahan) dengan prompt sebagai satu-satunya argumen, dan perannya dikenali dari penanda
`DASH_ASKPASS_HELPER` yang hanya ada di environment sesi SSH. Jangan dijalankan manual.

---

## Catatan Keamanan

- ⚠️ **Terminal menjalankan shell root penuh dan service berjalan sebagai root.** Ini keputusan
  operasional yang disengaja; konsekuensinya seluruh proses aplikasi berjalan sebagai root dan
  hardening systemd yang membatasi root dimatikan. Isolasi yang tersisa hanyalah lapisan
  aplikasi: autentikasi sesi, validasi origin, dan CSRF. Lihat peringatan di bagian
  "Menjalankan di produksi" dan jangan ekspos ke jaringan tak tepercaya.
- **Autentikasi wajib**: tidak ada endpoint terminal/file yang dapat diakses tanpa sesi login.
- **Unggahan berkas**: nama berkas divalidasi (tanpa pemisah path, tanpa `.`/`..`, tanpa byte
  NUL) dan selalu di-`join` ke direktori tujuan yang sudah di-resolve sandbox, sehingga unggahan
  tidak bisa keluar dari workspace; target symlink **diganti sebagai link**, bukan diikuti
  (tidak bisa menulis lewat link ke luar sandbox). Body di-cap saat streaming (`DASH_MAX_UPLOAD_SIZE`)
  jadi permintaan raksasa berhenti lebih awal, byte masuk ke file sementara di direktori tujuan
  lalu di-`rename` (tidak pernah ada file separuh), dan hanya ukuran + nama yang dicatat ke audit
  (`file.upload`) — isi berkas tidak masuk log. Nama yang sudah ada ditolak `409` kecuali
  `overwrite=1` dikirim eksplisit, dan permintaan tetap wajib membawa header CSRF + `Origin` yang
  sah. Batas `client_max_body_size`/`proxy_request_buffering` di nginx harus diselaraskan
  (lihat `deploy/nginx/`).
- **Unduhan selalu dipaksa jadi lampiran**: endpoint unduh mengirim
  `Content-Disposition: attachment` + `application/octet-stream` untuk **semua** jenis berkas,
  jadi berkas `.html`/`.svg` yang diunggah tidak akan pernah dirender (dan menjalankan skrip)
  di origin dashboard; berkas dialirkan langsung dari deskriptor file (`http.ServeContent`),
  bukan dibaca ke memori.
- **Operasi berkas lanjutan (salin, pindah, ZIP, izin)**: semuanya lewat sandbox workspace yang
  sama seperti operasi lain, jadi path di luar `DASH_WORKSPACE_ROOT` tetap ditolak `403` dan
  root workspace tetap tidak bisa diubah. Folder tidak bisa disalin/dipindah ke dalam
  subdirektori dirinya sendiri (`400`), salin tidak pernah menimpa tanpa `overwrite`, pindah
  tidak pernah menimpa sama sekali, dan symlink selalu dibuat ulang sebagai link (tidak pernah
  diikuti) sehingga salinan tidak bisa dipakai membaca ke luar sandbox.
  - **`chmod` menolak bit berbahaya**: hanya sembilan bit permission yang diterima. Setuid,
    setgid, dan sticky — baik sebagai flag Go maupun sebagai angka oktal `4755`/`2755`/`1755` —
    ditolak `400`. Alasannya langsung: dashboard berjalan sebagai root, jadi satu `chmod u+s`
    pada biner apa pun akan memberi siapa pun yang bisa mengeksekusinya hak akses root.
  - **Ekstraksi ZIP tahan zip-slip**: setiap nama entri dibersihkan dan diverifikasi tetap berada
    di bawah direktori tujuan, sehingga `../../etc/cron.d/x` maupun nama absolut ditolak `400`.
    Ekspansi dibatasi (20.000 entri, 4 GiB) agar zip bomb tidak memenuhi disk, entri symlink
    dilewati, mode hasil ekstraksi dinormalkan (bit setuid/setgid/sticky dan write untuk
    grup/lainnya dibuang), dan **tidak ada byte pun yang ditulis** sebelum seluruh arsip lolos
    validasi — arsip yang ditolak tidak meninggalkan pohon setengah jadi.
  - **Arsip dikirim streaming**: ZIP dibuat sambil dikirim, tanpa file sementara di server, dan
    batas 20.000 entri mencegah pohon tak berujung. Nama unduhan dari klien hanya diambil sebagai
    nama berkas polos (pemisah path, karakter kontrol, dan tanda kutip dibuang).
  - Semua operasi ini tercatat di audit (`file.copy`, `file.move`, `file.zip`, `file.unzip`,
    `file.chmod`, `file.stat`), hanya path/ukuran/mode — tidak pernah isi berkas.
- **Autentikasi dua faktor (opsional)**: TOTP RFC 6238 dengan implementasi internal (stdlib —
  tanpa framework autentikasi pihak ketiga). Detail yang dijaga: sesi **tidak** dibuat sebelum
  faktor kedua lolos; challenge login disimpan di memori (TTL 5 menit), terikat ke user + IP,
  dan sekali pakai; percobaan kode dibatasi rate-limit terpisah; kode yang sudah dipakai
  ditolak (anti-replay); kode pemulihan disimpan sebagai hash SHA-256 dan sekali pakai.
  Login juga selalu meminta kode (tidak ada "ingat perangkat").
- **WebSocket**: diamankan dengan cookie sesi + validasi `Origin` (allow-list / same-origin).
- **Perubahan profil diautentikasi ulang**: `PATCH /api/auth/profile` selalu meminta kata sandi
  saat ini (bahkan untuk mengubah nama tampilan saja), sehingga sesi yang dicuri tidak cukup
  untuk mengambil alih akun atau mengganti kredensialnya. Nama pengguna yang baru divalidasi
  formatnya dan dicek bentrok lewat kolom `UNIQUE COLLATE NOCASE`, sehingga tidak ada dua akun
  bernama sama (dengan huruf besar/kecil berbeda sekalipun). Mengganti kata sandi menghapus
  **semua** sesi pengguna lalu menerbitkan satu sesi baru untuk peramban yang memintanya, jadi
  perangkat lain langsung keluar. Field yang tidak dikirim tidak pernah tersentuh, dan error
  validasi memakai kode stabil (`invalid_username`, `weak_password`, `conflict`) tanpa
  membocorkan detail internal.
- **Tanpa shell injection**: tidak ada perintah shell yang dirangkai dari input pengguna.
  Shell dijalankan melalui PTY; input hanya mengalir ke stdin proses.
- **SSH connection manager**:
  - **Sumber kebenaran standar**: host disimpan sebagai blok `Host` di `~/.ssh/config` (format
    OpenSSH, sama dengan yang dipakai VSCode Remote-SSH). Tidak ada format proprietary, dan
    dashboard **tidak** menulis ke file yang masuk lewat `Include` (hanya baca).
  - **Tanpa injeksi argumen**: satu-satunya input pengguna yang sampai ke command line `ssh`
    adalah alias, divalidasi dengan pola `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$` dan wajib ada di
    config. Argumen dijalankan langsung (argv), **tidak pernah** lewat shell.
  - **Penulisan aman**: perubahan config ditulis atomik (temp + `rename`), mode `0600`, dengan
    cadangan `~/.ssh/config.bak.<waktu>` (disimpan maksimal 10), dan deteksi konflik bila file
    berubah di luar dashboard.
  - **Kunci host tidak pernah dilonggarkan**: `StrictHostKeyChecking` tidak diubah. Pada koneksi
    pertama, sesi SSH meminta konfirmasi seperti `ssh` biasa; mempercayai kunci lewat UI adalah
    tindakan eksplisit yang menampilkan sidik jari SHA-256 lebih dulu. Kredensial tersimpan
    **sengaja tidak dipakai** selama kunci host belum dipercaya, supaya pertanyaan sidik jari
    sampai ke pengguna: sesi pertama tetap interaktif (ketik `yes` lalu password sekali), sesi
    berikutnya otomatis.
  - **`known_hosts` dipatok eksplisit**: `ssh` meng-expand `~/.ssh/known_hosts` dari entri
    passwd pengguna yang menjalankan service, **bukan** dari `$HOME`, sehingga sesi selalu diberi
    `-o UserKnownHostsFile=<DASH_HOME_DIR>/.ssh/known_hosts` — file yang sama yang diperiksa dan
    ditulis UI (dan yang ditampilkan di panel konfigurasi efektif).
  - **Kredensial opsional & write-only**: password/passphrase disimpan terenkripsi
    (AES-256-GCM, kunci HKDF-SHA256 dari `DASH_SESSION_SECRET`, AAD mengikat jenis + alias) dan
    diberikan ke `ssh` lewat `SSH_ASKPASS` dengan broker unix socket `0600` bertoken sekali
    pakai — tidak pernah muncul di disk sebagai teks biasa, di argv, atau di log. API hanya bisa
    menyimpan/menghapus; tidak ada endpoint yang membacanya kembali. Karena OpenSSH menjalankan
    `SSH_ASKPASS` dengan `exec()` (bukan lewat shell), `SSH_ASKPASS` diisi **path binary saja**
    dan binary itu mengenali dirinya sebagai helper dari environment; prompt yang bukan permintaan
    password/passphrase (mis. konfirmasi sidik jari) tidak pernah dijawab.
  - **Batas jujur**: kunci enkripsi berada di file environment (`/etc/dashboard/dashboard.env`,
    mode `0600`) di host yang sama. Ini melindungi database/cadangan yang bocor, **bukan** host
    yang sudah dikuasai penyerang. Bila tidak ingin kredensial tersimpan sama sekali, cukup
    jangan mengisi password/passphrase dan ssh akan menanyakannya di terminal.

- **Lifecycle PTY**: setiap sesi dijalankan sebagai session leader pada PTY sendiri. Menutup
  sesi mengirim `SIGKILL` ke **seluruh session proses** (bukan hanya process group), sehingga
  job latar belakang yang dibuat di dalam shell tidak menjadi proses yatim. Sesi idle tanpa
  klien ditutup otomatis; sesi "menggantung" dari run sebelumnya ditandai `closed` saat startup.
- **Sesi bertahan saat reload**: daftar tab terminal dicerminkan ke `sessionStorage`, jadi
  me-reload halaman akan **menyambung kembali ke PTY yang sudah berjalan** (id sesi diverifikasi
  ke server lewat `GET /api/terminal/sessions` dulu) alih-alih membuat set sesi baru. Inilah
  yang mencegah tab lama menumpuk tanpa klien dan menghabiskan batas sesi.
- **Batas sesi tidak mengunci diri**: saat `DASH_TERMINAL_MAX_SESSIONS`/`_PER_USER` tercapai,
  manager lebih dulu mengambil alih sesi **tanpa klien** yang sudah diam ≥ 2 menit (sisa
  reload/tab yang ditutup — biasanya shell yang hanya duduk di prompt). Sesi yang masih punya
  klien atau sedang mengeluarkan output (build, transfer) tidak pernah diambil alih; kalau semua
  sesi benar-benar terpakai, permintaan gagal dengan pesan yang jelas di UI.
- **Isi terminal**: output PTY tidak pernah ditulis ke disk; hanya scrollback in-memory selama
  sesi hidup yang tersedia untuk replay. Riwayat **perintah** shell disimpan di
  `$HOME/.bash_history` agar bertahan antar sesi (set `DASH_TERMINAL_HISTORY=false` untuk
  mematikannya). Hati-hati: perintah berisi rahasia akan tercatat di file itu.
- **Probe metrik host remote**: status strip sesi SSH mengambil angkanya dengan menjalankan
  skrip **read-only** di host tujuan. Skrip dikirim lewat **stdin** ke `sh -s`, jadi tidak ada
  string perintah yang dirakit dari input pengguna; isinya hanya membaca `/proc`,
  `/proc/loadavg`, dan `df`, tanpa mengubah apa pun di host. Kredensial tersimpan hanya dipakai
  **setelah kunci host dipercaya** (gerbang yang sama dengan sesi biasa) dan probe gagal cepat
  bila tidak ada kredensial/kunci yang bisa dipakai, sehingga UI menampilkan alasannya alih-alih
  menggantung. Nonaktifkan dengan `DASH_SSH_STATS=false`.
- **Koneksi mux untuk probe**: supaya polling tidak menjadi login ssh baru setiap beberapa
  detik, probe memakai satu koneksi `ControlMaster` yang di-multiplex untuk alias tersebut
  (`ControlPersist=120`). Socket kontrol ada di `/run/dashboard/sshmux` dengan direktori `0700`
  dan socket `0600` (hanya root), dan socket tertinggal dibersihkan sebelum dipakai. Yang perlu
  diketahui: selama ±2 menit setelah aktivitas terakhir, host tersebut masih dapat dijangkau
  dari server ini tanpa autentikasi ulang — setara dengan kredensial tersimpan yang memang
  sudah ada di database terenkripsi, tetapi tetap perlu disadari. Hanya permintaan yang
  berhasil (200) yang tidak dicatat di log; kegagalan probe tetap tercatat.
- **Akses file penuh (sesuai konfigurasi)**: `DASH_WORKSPACE_ROOT` menentukan batas teratas
  operasi file. Pada deployment ini diset `/`, artinya file manager dapat membaca/menulis/
  menghapus **di mana saja** (sebagai root), kecuali direktori root filesystem `/` itu sendiri
  yang dilindungi. Set ke path yang lebih sempit (mis. `/srv/dashboard/workspace`) bila ingin
  sandbox asli; mekanisme `..`/symlink tetap memvalidasi terhadap batas tersebut
  (`internal/workspace`).
- **Batas ukuran & tipe**: batas baca/tulis file dan jumlah entri listing dikonfigurasi;
  file biner tidak dapat diedit sebagai teks.
- **Tanpa kebocoran rahasia**: password di-hash bcrypt; token sesi disimpan sebagai hash;
  kode pemulihan 2FA disimpan sebagai hash SHA-256; error tidak memuat detail internal; isi
  terminal **tidak** dicatat di log. Secret TOTP tersimpan di SQLite (diperlukan untuk
  verifikasi) dan dilindungi izin file database (`0640`, hanya root/dashboard).
- **Secret via environment**: tidak ada kredensial hard-coded.
- **CSRF**: double-submit cookie untuk semua permintaan mutasi.
- **Header keamanan**: CSP, `X-Frame-Options: DENY`, `nosniff`, `Referrer-Policy`.
- **Audit**: login sukses/gagal, operasi file, siklus sesi terminal, seluruh peristiwa 2FA
  (`auth.totp_setup`, `auth.totp_enabled`, `auth.totp_disabled`, `auth.totp_failed`,
  `auth.recovery_used`), dan perubahan profil (`auth.profile_update`, dengan detail
  `username lama -> baru, display name, password`; **kata sandi itu sendiri tidak pernah
  ditulis**) tercatat (metadata saja — bukan isi). Percobaan re-autentikasi yang gagal pada
  operasi sensitif juga tercatat dengan `success = 0`.

**Rekomendasi operasional:** jalankan di belakang nginx dengan HTTPS (skrip di atas),
batasi akses dengan firewall/VPN bila memungkinkan, dan gunakan password yang kuat.

---

## Batasan yang masih ada

1. **Tampilan saat ini adalah tafsiran awal** bergaya dashboard admin, dibangun sesuai keinginan
   dan referensi Anda sendiri (lihat bagian berikutnya). Elemen visual (warna aksen,
   ikon, kepadatan tabel) mudah disesuaikan dan **ditandai** untuk direvisi.
2. **Tidak ada multi-user UI/role**: tabel `users` mendukung banyak pengguna dan peran, tetapi
   belum ada antarmuka manajemen pengguna (hanya akun admin awal + ganti password).
3. **Terminal tanpa rekaman**: output PTY tidak disimpan (by design). Riwayat hanya scrollback
   in-memory selama sesi hidup.
4. **Upload & download berkas** sudah ada (unggah: tombol *Unggah* / drag & drop dengan progress
   per berkas; unduh: ikon ⬇ per baris, mendukung resume; **unduh banyak pilihan / folder
   sebagai satu ZIP**, dan **ekstrak `.zip`** dari menu klik kanan). Yang belum: **unggah folder
   rekursif** dan unggah arsip langsung dari browser — folder harus dikompres dulu di sisi
   pengguna sebelum diunggah.
5. **SQLite single-writer**: cukup untuk beban satu server; bukan untuk skala besar/multi-node.
6. **Recovery password & reset via email belum ada**; 2FA (TOTP) sudah tersedia, dengan
   break-glass `dashboard reset-totp --user <nama>` bila ponsel dan kode pemulihan hilang.
7. **`disable`/hapus akun pengguna** hanya tersedia lewat database.
8. **Terminal & file manager berjalan sebagai root penuh** (keputusan operasional). Terminal
   otomatis membuka satu shell root dan file manager default di `/root` dengan akses seluruh
   filesystem. Tidak ada isolasi OS-level antara aksi dashboard dan sistem; lihat
   "Catatan Keamanan".

---

## Menyesuaikan tampilan dengan referensi visual

Bagian yang **mudah disesuaikan dengan referensi visual Anda**:

- **Warna & tema**: variabel di `frontend/src/styles/global.css` (`:root`) — aksen saat ini
  `--accent: #6366f1`. Ganti sesuai palet referensi.
- **Lebar & perilaku sidebar**: `--sidebar-w`, `--sidebar-w-collapsed`, dan breakpoint
  `@media (max-width: 900px)`.
- **Struktur halaman Dashboard**: susunan kartu statistik & aktivitas di
  `frontend/src/pages/DashboardPage.tsx`.
- **Kepadatan tabel file**: grid `grid-template-columns` pada `.file-table__head` / `.file-row`.
- **Ikon**: set ikon generik di `frontend/src/components/Icons.tsx` (tanpa aset merek pihak lain).
- **Toolbar terminal**: susunan tombol di `frontend/src/pages/TerminalPage.tsx`.

Tidak ada logo, aset, atau merek pihak lain yang digunakan — hanya pola tata letak dan interaksi, dibangun sesuai keinginan dan referensi Anda sendiri.
