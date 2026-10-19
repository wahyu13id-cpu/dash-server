#!/usr/bin/env bash
# =============================================================================
#  Server Dashboard — pemasangan cepat (quick setup) satu perintah
# =============================================================================
#
#  Skrip ini memasang Server Dashboard dari nol sampai siap online:
#    dependensi (Go/Node/nginx) -> build -> user & direktori -> rahasia ->
#    konfigurasi -> systemd -> nginx -> HTTPS (opsional) -> akun admin -> verifikasi
#
#  ---- Cara 1: satu perintah (dari GitHub) ------------------------------------
#
#      curl -fsSL https://raw.githubusercontent.com/wahyu13id-cpu/dash-server/main/install.sh | sudo bash
#
#    Kode sumber diunduh otomatis dari repo (lihat REPO_SLUG di bawah). Bila repo
#    Anda punya Release, installer memakai binary + frontend siap-pakai; kalau
#    tidak ada, ia membangun dari sumber (Go/Node dipasang otomatis bila perlu).
#
#    Ingin memeriksa dulu sebelum menjalankan:
#      curl -fsSL .../install.sh -o /tmp/install.sh && less /tmp/install.sh
#      sudo bash /tmp/install.sh
#
#  ---- Cara 2: dari direktori proyek -----------------------------------------
#
#      cd /path/ke/dashboard && sudo bash install.sh
#
#    Kode di sekitar skrip dipakai apa adanya (tanpa unduh).
#
#  Contoh non-interaktif:
#
#      sudo bash install.sh --domain dashboard.example.com --email saya@example.com \
#          --admin-user admin --generate-password --yes
#
#      sudo bash install.sh --no-domain --tls selfsigned --yes          # tanpa domain
#      sudo bash install.sh --yes                                       # IP + HTTP (paling sederhana)
#      sudo bash install.sh --no-release --yes                          # paksa build dari sumber
#      sudo bash install.sh --uninstall                                 # lepaskan pemasangan
#
#  Domain bersifat OPSIONAL: tanpa domain, dashboard tetap jalan (lewat IP).
#
#  Lihat `bash install.sh --help` untuk daftar lengkap opsi.
# =============================================================================
set -Eeuo pipefail

INSTALLER_VERSION="1.1.0"

# ---------------------------------------------------------------------------
# Repo GitHub sumber aplikasi.
# Dipakai HANYA bila skrip dijalankan tanpa kode sumber di sekitarnya (mis. lewat
# `curl | bash`): kode sumber diunduh dari sini, dan aset Release dipakai bila ada.
# Bisa ditimpa lewat flag --repo atau env DASH_REPO.
# ---------------------------------------------------------------------------
REPO_SLUG="${DASH_REPO:-wahyu13id-cpu/dash-server}"
REPO_REF="${DASH_REPO_REF:-main}"
REPO_TOKEN="${DASH_REPO_TOKEN:-}"
RELEASE_TAG="${DASH_RELEASE:-latest}"

# ---------------------------------------------------------------------------
# Nilai bawaan (dapat diubah lewat flag)
# ---------------------------------------------------------------------------
APP_NAME="dashboard"
SERVICE_NAME="dashboard"
SERVICE_USER="dashboard"          # pemilik berkas; proses tetap berjalan sebagai root
BIN_PATH="/usr/local/bin/dashboard"
APP_DIR="/opt/dashboard"
ENV_DIR="/etc/dashboard"
ENV_FILE="/etc/dashboard/dashboard.env"
DATA_DIR="/var/lib/dashboard"
SERVICE_FILE="/etc/systemd/system/dashboard.service"
NGINX_AVAILABLE="/etc/nginx/sites-available/dashboard.conf"
NGINX_ENABLED="/etc/nginx/sites-enabled/dashboard.conf"
NGINX_UPGRADE_MAP="/etc/nginx/conf.d/upgrade-map.conf"
SELF_SIGNED_DIR="/etc/ssl/dashboard"
SELF_SIGNED_CRT="${SELF_SIGNED_DIR}/dashboard.crt"
SELF_SIGNED_KEY="${SELF_SIGNED_DIR}/dashboard.key"

APP_PORT="8080"
LISTEN_ADDR=""                    # kosong = ditentukan otomatis
DOMAIN=""
ACME_EMAIL=""
ACCESS_MODE=""                    # domain | ip
TLS_MODE=""                       # letsencrypt | selfsigned | none
ADMIN_USER="admin"
ADMIN_PASSWORD=""
ADMIN_PASSWORD_FILE=""
GEN_PASSWORD=0
WORKSPACE_ROOT="/"
HOME_DIR="/root"
MAX_UPLOAD="512M"
SESSION_TTL="12h"
TOTP_ISSUER="Server Dashboard"

WITH_NGINX=1
WITH_FIREWALL=""                  # "" = tanya/none, 1 = buka, 0 = jangan
SKIP_DEPS=0
DO_BUILD="auto"                   # auto | yes | no
ASSUME_YES=0
RECONFIGURE=0
DRY_RUN=0
UNINSTALL=0
PURGE=0
SELF_TEST=0
SOURCE_ARG=""
LOG_LEVEL="info"

# Keadaan internal (diisi saat berjalan)
EXPLICIT_CONFIG=0                 # pengguna menyebut flag yang mengubah konfigurasi
SEEN_FLAGS=""                     # daftar flag yang benar-benar diberikan pengguna
EXISTING=0
WRITE_ENV=1
CONFIGURE_NGINX=1
GO_MIN="1.24"
SRC_DIR=""
PM=""
OS_NAME=""
ARCH_RAW=""
IP_ADDR=""
PUBLIC_URL=""
ALLOWED_ORIGINS=""
SERVER_NAME=""
DEFAULT_SERVER=""
COOKIE_SECURE="false"
TRUSTED_PROXY="true"
SESSION_SECRET=""
NGINX_MAX_BODY="512m"
ADMIN_PASSWORD_GENERATED=0
BUILD_TMP=""
APP_VERSION=""
OLD_ORIGINS=""
OLD_COOKIE_SECURE=""
OLD_TRUSTED_PROXY=""
BIN_SRC=""                        # berkas binary siap pasang
FRONTEND_SRC=""                   # direktori frontend siap pasang
RELEASE_STAGE=""                  # direktori kerja unduhan aset rilis
EXPLICIT_SOURCE=0                 # pengguna memberi --source
SRC_IS_LOCAL=0                    # sumber berasal dari direktori proyek (bukan unduhan)

# ---------------------------------------------------------------------------
# Warna & logging
# ---------------------------------------------------------------------------
if [[ -t 1 ]]; then
  C_RESET=$'\033[0m'; C_BOLD=$'\033[1m'; C_DIM=$'\033[2m'
  C_RED=$'\033[31m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_CYAN=$'\033[36m'
else
  C_RESET=""; C_BOLD=""; C_DIM=""; C_RED=""; C_GREEN=""; C_YELLOW=""; C_CYAN=""
fi

info()  { printf '%b\n' "${C_CYAN}==>${C_RESET} $*"; }
ok()    { printf '%b\n' "${C_GREEN}  ok${C_RESET} $*"; }
warn()  { printf '%b\n' "${C_YELLOW}  !${C_RESET} $*" >&2; }
err()   { printf '%b\n' "${C_RED}  x${C_RESET} $*" >&2; }
die()   { err "$*"; if [[ -n "$LOG_FILE" ]]; then printf '%b\n' "${C_DIM}  log: ${LOG_FILE}${C_RESET}" >&2; fi; exit 1; }
step()  { printf '\n%b\n' "${C_BOLD}== $* ==${C_RESET}"; }
detail(){ printf '%b\n' "${C_DIM}    $*${C_RESET}"; }

LOG_FILE=""
on_error() {
  local code=$1 line=$2
  err "pemasangan GAGAL (exit $code) pada baris $line."
  [[ -n "$LOG_FILE" ]] && err "periksa log lengkap: $LOG_FILE"
  exit "$code"
}
trap 'on_error $? $LINENO' ERR

# ---------------------------------------------------------------------------
# Helper
# ---------------------------------------------------------------------------

# run: jalankan perintah, atau tampilkan saja saat --dry-run.
run() {
  if [[ $DRY_RUN -eq 1 ]]; then
    printf '%b\n' "${C_DIM}    [dry-run] $*${C_RESET}"
    return 0
  fi
  "$@"
}

# run_sh: seperti run, tapi untuk potongan shell (pipe, redirect).
run_sh() {
  if [[ $DRY_RUN -eq 1 ]]; then
    printf '%b\n' "${C_DIM}    [dry-run] sh -c '$*'${C_RESET}"
    return 0
  fi
  bash -c "$*"
}

fetch() { curl -fsSL --retry 3 --connect-timeout 20 "$1"; }

have_tty() { [[ -r /dev/tty && -w /dev/tty ]]; }

# ask <pertanyaan> [bawaan] -> stdout
ask() {
  local prompt="$1" default="${2-}" answer=""
  [[ -n "$default" ]] && prompt="$prompt [$default]"
  prompt="$prompt: "
  if have_tty; then read -r -p "$prompt" answer </dev/tty || true
  else read -r -p "$prompt" answer || true; fi
  printf '%s' "${answer:-$default}"
}

# ask_secret <pertanyaan> -> stdout (input disembunyikan)
ask_secret() {
  local prompt="$1" answer=""
  if have_tty; then read -r -s -p "$prompt" answer </dev/tty || true
  else read -r -s -p "$prompt" answer || true; fi
  printf '\n' >&2
  printf '%s' "$answer"
}

# confirm <pertanyaan> [y|n]
confirm() {
  local prompt="$1" default="${2:-y}" hint="[Y/n]" answer=""
  [[ "$default" == "n" ]] && hint="[y/N]"
  if have_tty; then read -r -p "$prompt $hint " answer </dev/tty || true
  else read -r -p "$prompt $hint " answer || true; fi
  answer="${answer:-$default}"
  [[ "${answer,,}" == y* ]]
}

# write_file <path> <mode> <owner:group>  (isi dari stdin, atau "-" untuk kosong)
write_file() {
  local path="$1" mode="$2" owner="${3:-root:root}" content
  content="$(cat)"
  if [[ $DRY_RUN -eq 1 ]]; then
    detail "[dry-run] tulis $path (mode $mode, $owner)"
    return 0
  fi
  printf '%s\n' "$content" >"$path"
  chmod "$mode" "$path"
  if [[ "$owner" != "-" ]]; then chown "$owner" "$path" 2>/dev/null || true; fi
}

# install_src <sumber> <tujuan> <mode>
install_src() {
  local src="$1" dst="$2" mode="${3:-0644}"
  [[ -f "$src" ]] || die "berkas sumber tidak ditemukan: $src"
  run install -m "$mode" "$src" "$dst"
}

# version_ge <versi> <minimum> -> 0 kalau versi >= minimum
version_ge() {
  local v="$1" need="$2" i x y
  local -a _v=() _n=()
  v="${v#v}"; need="${need#v}"
  IFS='.' read -r -a _v <<<"$v"
  IFS='.' read -r -a _n <<<"$need"
  for i in 0 1 2; do
    x="${_v[$i]:-0}"; y="${_n[$i]:-0}"
    x="${x//[!0-9]/}"; y="${y//[!0-9]/}"
    x="${x:-0}"; y="${y:-0}"
    if ((10#$x > 10#$y)); then return 0; fi
    if ((10#$x < 10#$y)); then return 1; fi
  done
  return 0
}

# gen_password -> kata sandi acak 20 karakter yang memenuhi kebijakan (huruf+angka)
gen_password() {
  local chars='ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789'
  local n=${#chars} out="" b
  while ((${#out} < 20)); do
    b=$(openssl rand -hex 1)
    out+="${chars:$((16#$b % n)):1}"
  done
  printf 'A%s9' "${out:1:18}"
}

password_ok() {
  local pw="$1"
  ((${#pw} >= 10 && ${#pw} <= 200)) || return 1
  [[ "$pw" =~ [A-Za-z] ]] || return 1
  [[ "$pw" =~ [0-9] ]] || return 1
}

username_ok() {
  local u="$1"
  ((${#u} >= 3 && ${#u} <= 32)) || return 1
  [[ "$u" =~ ^[a-z0-9][a-z0-9._-]*[a-z0-9]$ ]] || return 1
}

# env_value <kunci> [berkas] -> nilai dari berkas env yang ada
env_value() {
  local key="$1" file="${2:-$ENV_FILE}"
  [[ -f "$file" ]] || return 0
  sed -n "s/^${key}=//p" "$file" 2>/dev/null | tail -n1
}

# ---------------------------------------------------------------------------
# Bantuan
# ---------------------------------------------------------------------------
usage() {
  cat <<'EOF'
Server Dashboard — pemasangan cepat (quick setup)

Pemakaian:
  sudo bash install.sh [opsi]

Opsi utama:
  --domain NAMA             Pasang untuk domain ini (HTTPS otomatis via Let's Encrypt).
  --no-domain               Tanpa domain; dashboard diakses lewat IP server.
  --email ALAMAT            Email pendaftaran Let's Encrypt (disarankan bila pakai domain).
  --tls MODE                letsencrypt | selfsigned | none
                            (bawaan: letsencrypt kalau ada domain, none kalau tanpa domain)
  --admin-user NAMA         Nama pengguna admin pertama (bawaan: admin).
  --admin-password SANDI    Kata sandi admin (min. 10 karakter, ada huruf & angka).
  --admin-password-file F   Ambil kata sandi admin dari berkas (baris pertama).
  --generate-password       Buat kata sandi admin acak dan tampilkan sekali di akhir.
  --port N                  Port internal aplikasi (bawaan: 8080).
  --listen ALAMAT           Alamat bind aplikasi (bawaan: 127.0.0.1:PORT dengan nginx,
                            0.0.0.0:PORT tanpa nginx).
  --workspace PATH          Batas atas file manager (bawaan: / = akses penuh).
  --home-dir PATH           Direktori awal (HOME) shell terminal (bawaan: /root).
  --max-upload UKURAN       Batas unggah berkas (bawaan: 512M).
  --session-ttl DURASI      Umur sesi login (bawaan: 12h).
  --totp-issuer NAMA        Nama penerbit di aplikasi authenticator (bawaan: Server Dashboard).
  --log-level LEVEL         debug | info | warn | error (bawaan: info).

Jaringan & layanan:
  --no-nginx                Jangan pasang/atur nginx (akses langsung ke port aplikasi).
  --firewall                Buka port 80/443 di ufw/firewalld (tanpa bertanya).
  --no-firewall             Jangan menyentuh firewall.

Build & mode:
  --no-build                Pakai bin/dashboard + frontend/dist yang sudah ada.
  --rebuild                 Paksa build ulang walau artefak sudah ada.
  --skip-deps               Jangan pasang paket sistem (Go/Node/nginx harus sudah ada).
  --source DIR              Direktori sumber proyek (bawaan: direktori skrip ini).

Sumber dari GitHub (untuk pemakaian `curl | bash`):
  --repo USER/REPO          Repo GitHub sumber aplikasi (bawaan: repo proyek ini);
                            kode diunduh otomatis bila tidak ada di sekitar skrip.
                            Bisa juga: DASH_REPO=user/repo
  --repo-ref REF            Branch atau tag sumber (bawaan: main). DASH_REPO_REF
  --repo-token TOKEN        Token baca untuk repo privat (fine-grained, izin Contents: read).
                            DASH_REPO_TOKEN. Token tidak disimpan di server.
  --release TAG|latest|none Pakai aset Release siap-pakai (bawaan: latest). Bila tidak
                            tersedia, installer otomatis build dari sumber.
                            --no-release = paksa build dari sumber. DASH_RELEASE
  --reconfigure             Tanya ulang konfigurasi walau sudah pernah terpasang.
  --yes, -y                 Non-interaktif: pakai flag/nilai bawaan tanpa bertanya.
  --dry-run                 Tampilkan langkah yang akan dijalankan tanpa mengubah sistem.
  -v, --verbose             Tampilkan perintah yang dijalankan.
  -h, --help                Tampilkan bantuan ini.

Lain-lain:
  --uninstall               Hentikan dan hapus layanan, unit systemd, dan vhost nginx.
  --purge                   Dengan --uninstall: hapus juga /etc/dashboard dan /var/lib/dashboard
                            (basis data ikut terhapus).
  --self-test               Uji helper internal skrip ini (tidak mengubah sistem).

Contoh:
  sudo bash install.sh
  sudo bash install.sh --repo wahyu13id-cpu/dash-server --yes
  curl -fsSL https://raw.githubusercontent.com/wahyu13id-cpu/dash-server/main/install.sh | sudo bash
  sudo bash install.sh --domain dash.example.com --email saya@example.com -y
  sudo bash install.sh --no-domain --tls selfsigned -y
  sudo bash install.sh --yes --admin-user admin --generate-password
  sudo bash install.sh --dry-run --domain dash.example.com
  sudo bash install.sh --uninstall
EOF
}

# ---------------------------------------------------------------------------
# Parsing argumen
# ---------------------------------------------------------------------------

# Flag yang mengubah isi konfigurasi. Dipakai untuk memutuskan apakah pemasangan
# ulang perlu menulis ulang env/nginx (mode perbarui hanya mengganti biner).
CONFIG_FLAGS=(
  --domain --email --tls --admin-user --admin-password --admin-password-file
  --generate-password --port --listen --workspace --home-dir --max-upload
  --session-ttl --totp-issuer --log-level --no-nginx --firewall --no-firewall
)

is_config_flag() {
  local f="$1" c
  for c in "${CONFIG_FLAGS[@]}"; do
    if [[ "$f" == "$c" || "$f" == "$c"=* ]]; then return 0; fi
  done
  return 1
}

# given <flag> -> 0 kalau pengguna benar-benar menyebut flag tersebut
given() { [[ " ${SEEN_FLAGS} " == *" $1 "* ]]; }

parse_args() {
  while (($#)); do
    SEEN_FLAGS="${SEEN_FLAGS} ${1%%=*}"
    if is_config_flag "$1"; then EXPLICIT_CONFIG=1; fi
    case "$1" in
      -h|--help)                usage; exit 0 ;;
      --self-test)              SELF_TEST=1; shift ;;
      --domain)                 DOMAIN="${2:-}"; ACCESS_MODE="domain"; shift 2 ;;
      --domain=*)               DOMAIN="${1#*=}"; ACCESS_MODE="domain"; shift ;;
      --no-domain)              ACCESS_MODE="ip"; shift ;;
      --email)                  ACME_EMAIL="${2:-}"; shift 2 ;;
      --email=*)                ACME_EMAIL="${1#*=}"; shift ;;
      --tls)                    TLS_MODE="${2:-}"; shift 2 ;;
      --tls=*)                  TLS_MODE="${1#*=}"; shift ;;
      --admin-user)             ADMIN_USER="${2:-}"; shift 2 ;;
      --admin-user=*)           ADMIN_USER="${1#*=}"; shift ;;
      --admin-password)         ADMIN_PASSWORD="${2:-}"; shift 2 ;;
      --admin-password=*)       ADMIN_PASSWORD="${1#*=}"; shift ;;
      --admin-password-file)    ADMIN_PASSWORD_FILE="${2:-}"; shift 2 ;;
      --admin-password-file=*)  ADMIN_PASSWORD_FILE="${1#*=}"; shift ;;
      --generate-password)      GEN_PASSWORD=1; shift ;;
      --port)                   APP_PORT="${2:-}"; shift 2 ;;
      --port=*)                 APP_PORT="${1#*=}"; shift ;;
      --listen)                 LISTEN_ADDR="${2:-}"; shift 2 ;;
      --listen=*)               LISTEN_ADDR="${1#*=}"; shift ;;
      --workspace)              WORKSPACE_ROOT="${2:-}"; shift 2 ;;
      --workspace=*)            WORKSPACE_ROOT="${1#*=}"; shift ;;
      --home-dir)               HOME_DIR="${2:-}"; shift 2 ;;
      --home-dir=*)             HOME_DIR="${1#*=}"; shift ;;
      --max-upload)             MAX_UPLOAD="${2:-}"; shift 2 ;;
      --max-upload=*)           MAX_UPLOAD="${1#*=}"; shift ;;
      --session-ttl)            SESSION_TTL="${2:-}"; shift 2 ;;
      --session-ttl=*)          SESSION_TTL="${1#*=}"; shift ;;
      --totp-issuer)            TOTP_ISSUER="${2:-}"; shift 2 ;;
      --totp-issuer=*)          TOTP_ISSUER="${1#*=}"; shift ;;
      --log-level)              LOG_LEVEL="${2:-}"; shift 2 ;;
      --log-level=*)            LOG_LEVEL="${1#*=}"; shift ;;
      --no-nginx)               WITH_NGINX=0; shift ;;
      --firewall)               WITH_FIREWALL=1; shift ;;
      --no-firewall)            WITH_FIREWALL=0; shift ;;
      --no-build)               DO_BUILD="no"; shift ;;
      --rebuild)                DO_BUILD="yes"; shift ;;
      --repo)                   REPO_SLUG="${2:-}"; shift 2 ;;
      --repo=*)                 REPO_SLUG="${1#*=}"; shift ;;
      --repo-ref)               REPO_REF="${2:-}"; shift 2 ;;
      --repo-ref=*)             REPO_REF="${1#*=}"; shift ;;
      --repo-token)             REPO_TOKEN="${2:-}"; shift 2 ;;
      --repo-token=*)           REPO_TOKEN="${1#*=}"; shift ;;
      --release)                RELEASE_TAG="${2:-}"; shift 2 ;;
      --release=*)              RELEASE_TAG="${1#*=}"; shift ;;
      --no-release)             RELEASE_TAG="none"; shift ;;
      --skip-deps)              SKIP_DEPS=1; shift ;;
      --source)                 SOURCE_ARG="${2:-}"; EXPLICIT_SOURCE=1; shift 2 ;;
      --source=*)               SOURCE_ARG="${1#*=}"; EXPLICIT_SOURCE=1; shift ;;
      --reconfigure)            RECONFIGURE=1; shift ;;
      -y|--yes)                 ASSUME_YES=1; shift ;;
      --dry-run)                DRY_RUN=1; shift ;;
      -v|--verbose)             set -x; shift ;;
      --uninstall)              UNINSTALL=1; shift ;;
      --purge)                  PURGE=1; shift ;;
      *)                        die "opsi tidak dikenal: $1 (lihat --help)" ;;
    esac
  done
}

# ---------------------------------------------------------------------------
# Uji mandiri (tanpa menyentuh sistem): memeriksa helper inti skrip ini.
# ---------------------------------------------------------------------------
self_test() {
  local fails=0
  okf() { printf '%b\n' "${C_GREEN}  ok${C_RESET} $*"; }
  badf() { printf '%b\n' "${C_RED}  x ${C_RESET} $*"; fails=$((fails + 1)); }
  step "Uji mandiri installer"

  # version_ge
  version_ge 1.24.4 1.24.0 && okf "version_ge 1.24.4 >= 1.24.0" || badf "version_ge 1.24.4 >= 1.24.0"
  version_ge 1.24.0 1.24.0 && okf "version_ge 1.24.0 >= 1.24.0" || badf "version_ge sama"
  version_ge 1.23.9 1.24.0 && badf "version_ge 1.23.9 < 1.24.0 seharusnya gagal" || okf "version_ge 1.23.9 < 1.24.0"
  version_ge v22.23.3 20.0.0 && okf "version_ge v22.23.3 >= 20.0.0" || badf "version_ge v-prefix"
  version_ge go1.25 1.24.0 && okf "version_ge go1.25 >= 1.24.0" || badf "version_ge prefix go"

  # username_ok (samakan dengan internal/auth)
  username_ok admin && okf "username 'admin' valid" || badf "username 'admin'"
  username_ok a && badf "username terlalu pendek seharusnya invalid" || okf "username terlalu pendek ditolak"
  username_ok admin- && badf "username diakhiri '-' seharusnya invalid" || okf "username akhiran '-' ditolak"
  username_ok .admin && badf "username diawali '.' seharusnya invalid" || okf "username awalan '.' ditolak"
  username_ok admin_2 && okf "username 'admin_2' valid" || badf "username 'admin_2'"

  # password_ok (samakan dengan auth.ValidatePasswordStrength)
  password_ok "rahasia12345" && okf "password 12 karakter (huruf+angka) valid" || badf "password valid"
  password_ok "pendek1" && badf "password < 10 seharusnya invalid" || okf "password < 10 ditolak"
  password_ok "hanyahurufsaja" && badf "password tanpa angka seharusnya invalid" || okf "password tanpa angka ditolak"
  password_ok "1234567890" && badf "password tanpa huruf seharusnya invalid" || okf "password tanpa huruf ditolak"

  # gen_password harus selalu memenuhi kebijakan
  local i pw
  for i in 1 2 3 4 5 6 7 8 9 10; do
    pw="$(gen_password)"
    if (( ${#pw} != 20 )) || ! password_ok "$pw"; then
      badf "gen_password menghasilkan '$pw' (tidak memenuhi kebijakan)"
    fi
  done
  ((fails == 0)) && okf "gen_password 10x memenuhi kebijakan (20 karakter, huruf+angka)"

  # normalize_repo_slug
  [[ "$(normalize_repo_slug 'wahyu13id-cpu/dash-server')" == "wahyu13id-cpu/dash-server" ]] \
    && okf "slug user/repo diterima apa adanya" || badf "slug user/repo"
  [[ "$(normalize_repo_slug 'https://github.com/wahyu13id-cpu/dash-server')" == "wahyu13id-cpu/dash-server" ]] \
    && okf "URL GitHub dinormalkan" || badf "normalisasi URL GitHub"
  [[ "$(normalize_repo_slug 'https://github.com/user/repo.git')" == "user/repo" ]] \
    && okf "akhiran .git dipangkas" || badf "pemangkasan .git"
  [[ "$(normalize_repo_slug 'git@github.com:user/repo.git')" == "user/repo" ]] \
    && okf "bentuk SSH dinormalkan" || badf "normalisasi bentuk SSH"

  # render template nginx: pastikan semua placeholder tergantikan
  # (dilewati bila skrip dijalankan tanpa kode sumber, mis. `curl | bash`)
  local tpl out
  for tpl in "$SRC_DIR/deploy/nginx/dashboard.conf.template" "$SRC_DIR/deploy/nginx/dashboard-tls.conf.template"; do
    if [[ ! -f "$tpl" ]]; then
      if [[ -f "$SRC_DIR/go.mod" ]]; then badf "template hilang: $tpl"; else okf "template $(basename "$tpl") dilewati (tanpa kode sumber)"; fi
      continue
    fi
    out="$(sed \
      -e "s|__SERVER_NAME__|dash.example.com|g" \
      -e "s|__DEFAULT_SERVER__||g" \
      -e "s|__APP_PORT__|8080|g" \
      -e "s|__MAX_BODY__|512m|g" \
      -e "s|__CERT_FILE__|/etc/ssl/dashboard/dashboard.crt|g" \
      -e "s|__KEY_FILE__|/etc/ssl/dashboard/dashboard.key|g" \
      "$tpl")"
    # Abaikan baris komentar saat memeriksa sisa placeholder.
    if grep -v '^[[:space:]]*#' <<<"$out" | grep -q '__[A-Z_]*__'; then
      badf "placeholder tersisa di $tpl: $(grep -v '^[[:space:]]*#' <<<"$out" | grep -o '__[A-Z_]*__' | sort -u | tr '\n' ' ')"
    else
      okf "template $(basename "$tpl") bersih dari placeholder"
    fi
  done

  # helper env_value terhadap berkas contoh
  local tmp; tmp="$(mktemp)"
  printf 'A=1\nB=dua tiga\n' >"$tmp"
  [[ "$(env_value A "$tmp")" == "1" ]] && okf "env_value membaca kunci" || badf "env_value kunci"
  [[ "$(env_value B "$tmp")" == "dua tiga" ]] && okf "env_value membaca nilai berspasi" || badf "env_value nilai berspasi"
  rm -f "$tmp"

  printf '\n'
  if ((fails == 0)); then
    ok "semua uji mandiri lulus"
    return 0
  fi
  err "$fails uji mandiri gagal"
  return 1
}

# ---------------------------------------------------------------------------
# Deteksi sistem
# ---------------------------------------------------------------------------
detect_system() {
  if [[ -r /etc/os-release ]]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    OS_NAME="${PRETTY_NAME:-${ID:-linux}}"
    OS_ID="${ID:-}"
  else
    OS_NAME="$(uname -s)"; OS_ID=""
  fi

  if command -v apt-get >/dev/null 2>&1; then PM="apt"
  elif command -v dnf >/dev/null 2>&1; then PM="dnf"
  elif command -v yum >/dev/null 2>&1; then PM="yum"
  else PM="none"; fi

  ARCH_RAW="$(uname -m)"
  IP_ADDR="$(detect_ip)"

  # Debian/Ubuntu: sites-available + sites-enabled. RHEL/Fedora: conf.d saja.
  if [[ -d /etc/nginx/sites-enabled ]]; then
    NGINX_AVAILABLE="/etc/nginx/sites-available/dashboard.conf"
    NGINX_ENABLED="/etc/nginx/sites-enabled/dashboard.conf"
  elif [[ -d /etc/nginx/conf.d ]]; then
    NGINX_AVAILABLE="/etc/nginx/conf.d/dashboard.conf"
    NGINX_ENABLED="/etc/nginx/conf.d/dashboard.conf"
  fi
}

detect_ip() {
  local ip=""
  if command -v ip >/dev/null 2>&1; then
    ip="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}')"
  fi
  if [[ -z "$ip" ]] && command -v hostname >/dev/null 2>&1; then
    ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
  fi
  printf '%s' "${ip:-127.0.0.1}"
}

go_ok() {
  command -v go >/dev/null 2>&1 || return 1
  local have; have="$(go env GOVERSION 2>/dev/null || true)"
  [[ -n "$have" ]] || return 1
  version_ge "$have" "$GO_MIN"
}

node_ok() {
  command -v node >/dev/null 2>&1 || return 1
  version_ge "$(node --version 2>/dev/null || echo 0)" "20.0.0"
}

# ---------------------------------------------------------------------------
# Prompt interaktif
# ---------------------------------------------------------------------------
interactive() { [[ $ASSUME_YES -eq 0 ]] && have_tty; }

prompt_all() {
  step "Konfigurasi"

  # --- mode akses ---
  if ! given --domain && ! given --no-domain; then
    printf '\nAkses dashboard:\n'
    printf '  1) Punya domain  -> HTTPS otomatis (Let%s Encrypt), disarankan\n' "'s"
    printf '  2) Tanpa domain  -> lewat alamat IP %s (HTTP)\n' "$IP_ADDR"
    printf '  3) Tanpa domain  -> lewat IP dengan HTTPS self-signed (browser memberi peringatan)\n'
    local choice default_choice=1
    if [[ "$ACCESS_MODE" == "ip" ]]; then default_choice=2; fi
    choice="$(ask 'Pilih' "$default_choice")"
    case "$choice" in
      1) ACCESS_MODE="domain" ;;
      2) ACCESS_MODE="ip"; TLS_MODE="none" ;;
      3) ACCESS_MODE="ip"; TLS_MODE="selfsigned" ;;
      *) die "pilihan tidak valid: $choice" ;;
    esac
  fi

  if [[ "$ACCESS_MODE" == "domain" ]]; then
    if ! given --domain; then
      local d
      d="$(ask 'Domain (mis. dash.example.com)' "${DOMAIN}")"
      while [[ -z "$d" ]]; do
        warn "domain tidak boleh kosong (atau jalankan ulang dengan --no-domain)."
        d="$(ask 'Domain (mis. dash.example.com)' '')"
      done
      DOMAIN="$d"
    fi
    if [[ -z "$ACME_EMAIL" ]]; then
      ACME_EMAIL="$(ask 'Email untuk Let'"'"'s Encrypt (boleh kosong)' '')"
    fi
    if ! given --tls; then
      printf 'HTTPS untuk domain ini:\n'
      printf '  1) Let%s Encrypt — sertifikat resmi (disarankan)\n' "'s"
      printf '  2) Self-signed — hanya untuk uji coba (browser memberi peringatan)\n'
      printf '  3) Tanpa HTTPS — HTTP saja (tidak disarankan)\n'
      case "$(ask 'Pilih' '1')" in
        2) TLS_MODE="selfsigned" ;;
        3) TLS_MODE="none" ;;
        *) TLS_MODE="letsencrypt" ;;
      esac
    fi
  else
    TLS_MODE="${TLS_MODE:-none}"
  fi

  # --- akun admin ---
  if [[ $EXISTING -eq 1 ]]; then
    info "akun admin sudah ada; ubah nama pengguna/kata sandi lewat halaman Profil saya di aplikasi."
  elif [[ -z "$ADMIN_PASSWORD" && -z "$ADMIN_PASSWORD_FILE" && $GEN_PASSWORD -eq 0 ]]; then
    printf '\nAkun admin pertama (dibuat sekali saat layanan pertama kali dijalankan):\n'
    ADMIN_USER="$(ask 'Nama pengguna' "$ADMIN_USER")"
    local p1 p2
    while :; do
      p1="$(ask_secret 'Kata sandi (min. 10 karakter, ada huruf & angka; kosong = dibuatkan acak): ')"
      if [[ -z "$p1" ]]; then
        GEN_PASSWORD=1
        break
      fi
      if ! password_ok "$p1"; then
        warn "kata sandi terlalu lemah: minimal 10 karakter serta mengandung huruf dan angka."
        continue
      fi
      p2="$(ask_secret 'Ulangi kata sandi: ')"
      if [[ "$p1" != "$p2" ]]; then
        warn "kata sandi tidak sama, coba lagi."
        continue
      fi
      ADMIN_PASSWORD="$p1"
      break
    done
  fi

  # --- opsi lanjutan ---
  if confirm 'Ubah opsi lanjutan (port, batas file manager, firewall)?' 'n'; then
    APP_PORT="$(ask 'Port internal aplikasi' "$APP_PORT")"
    printf 'Batas file manager:\n  1) /  -> akses seluruh filesystem (sesuai desain: terminal = root)\n  2) %s/workspace -> kotak pasir\n' "$DATA_DIR"
    local ws_choice=1
    if [[ "$WORKSPACE_ROOT" != "/" ]]; then ws_choice=2; fi
    if [[ "$(ask 'Pilih' "$ws_choice")" == "2" ]]; then
      WORKSPACE_ROOT="${DATA_DIR}/workspace"
    else
      WORKSPACE_ROOT="/"
    fi
    local fw
    fw="$(ask 'Buka port 80/443 di firewall? (y/n)' "${WITH_FIREWALL:-y}")"
    if [[ "${fw,,}" == y* ]]; then WITH_FIREWALL=1; else WITH_FIREWALL=0; fi
  fi
}

finalize_config() {
  APP_PORT="${APP_PORT//[!0-9]/}"
  [[ -n "$APP_PORT" ]] || die "port tidak valid"
  TLS_MODE="${TLS_MODE:-none}"

  # nginx menentukan alamat bind aplikasi.
  if [[ -z "$LISTEN_ADDR" ]]; then
    if [[ $WITH_NGINX -eq 1 ]]; then LISTEN_ADDR="127.0.0.1:${APP_PORT}"
    else LISTEN_ADDR="0.0.0.0:${APP_PORT}"; fi
  fi

  if [[ "$ACCESS_MODE" == "domain" ]]; then
    DOMAIN="${DOMAIN#http://}"; DOMAIN="${DOMAIN#https://}"; DOMAIN="${DOMAIN%%/*}"
    if [[ -z "$DOMAIN" ]]; then die "domain kosong"; fi
  fi

  case "$TLS_MODE" in
    letsencrypt|selfsigned|none) ;;
    *) die "--tls harus salah satu dari: letsencrypt, selfsigned, none" ;;
  esac
  if [[ "$TLS_MODE" == "letsencrypt" && -z "$DOMAIN" ]]; then
    die "--tls letsencrypt memerlukan --domain"
  fi
  if [[ $WITH_NGINX -eq 0 && "$TLS_MODE" == "letsencrypt" ]]; then
    die "--tls letsencrypt memerlukan nginx (jangan pakai --no-nginx)"
  fi

  # Batas unggah harus angka + satuan opsional (mis. 512M) agar dibaca aplikasi.
  MAX_UPLOAD="${MAX_UPLOAD:-512M}"
  if [[ ! "$MAX_UPLOAD" =~ ^[0-9]+[KMGkmg]?[Bb]?$ ]]; then
    die "--max-upload tidak valid: gunakan angka dengan satuan opsional, mis. 512M"
  fi

  # Nama server & origin yang diizinkan.
  if [[ "$ACCESS_MODE" == "domain" ]]; then
    SERVER_NAME="$DOMAIN"
    DEFAULT_SERVER=""
    ALLOWED_ORIGINS="https://${DOMAIN},http://${DOMAIN}"
    PUBLIC_URL="https://${DOMAIN}"
    if [[ $WITH_NGINX -eq 0 ]]; then PUBLIC_URL="http://${DOMAIN}:${APP_PORT}"; fi
  else
    SERVER_NAME="_"
    DEFAULT_SERVER=" default_server"
    if [[ "$TLS_MODE" == "none" ]]; then PUBLIC_URL="http://${IP_ADDR}"
    else PUBLIC_URL="https://${IP_ADDR}"; fi
    ALLOWED_ORIGINS="http://${IP_ADDR},https://${IP_ADDR}"
    if [[ $WITH_NGINX -eq 0 ]]; then
      DEFAULT_SERVER=""
      PUBLIC_URL="http://${IP_ADDR}:${APP_PORT}"
      ALLOWED_ORIGINS="http://${IP_ADDR}:${APP_PORT},https://${IP_ADDR}:${APP_PORT}"
    fi
  fi

  COOKIE_SECURE="false"
  if [[ "$TLS_MODE" != "none" ]]; then COOKIE_SECURE="true"; fi
  TRUSTED_PROXY="true"
  if [[ $WITH_NGINX -eq 0 ]]; then TRUSTED_PROXY="false"; fi

  # Mode perbarui (tanpa --reconfigure): pertahankan nilai lama apa adanya.
  if [[ -n "${OLD_ORIGINS:-}" && $WRITE_ENV -eq 0 ]]; then
    ALLOWED_ORIGINS="$OLD_ORIGINS"
    if [[ -n "${OLD_COOKIE_SECURE:-}" ]]; then COOKIE_SECURE="$OLD_COOKIE_SECURE"; fi
    if [[ -n "${OLD_TRUSTED_PROXY:-}" ]]; then TRUSTED_PROXY="$OLD_TRUSTED_PROXY"; fi
  fi

  # Batas unggah nginx mengikuti DASH_MAX_UPLOAD_SIZE.
  NGINX_MAX_BODY="$(printf '%s' "$MAX_UPLOAD" | tr '[:upper:]' '[:lower:]')"
  if [[ "$NGINX_MAX_BODY" =~ [0-9]$ ]]; then NGINX_MAX_BODY="${NGINX_MAX_BODY}b"; fi

  # Akun admin.
  ADMIN_USER="${ADMIN_USER:-admin}"
  if ! username_ok "$ADMIN_USER"; then
    die "nama pengguna tidak valid: 3-32 karakter, diawali/diakhiri huruf atau angka, hanya huruf kecil/angka/titik/strip/garis bawah"
  fi
  if [[ $GEN_PASSWORD -eq 1 ]]; then
    ADMIN_PASSWORD="$(gen_password)"
    ADMIN_PASSWORD_GENERATED=1
  fi

  # Sandi untuk aplikasi: hanya dipakai saat bootstrap, lalu dihapus dari env.
  if [[ -n "$ADMIN_PASSWORD" ]] && ! password_ok "$ADMIN_PASSWORD"; then
    die "kata sandi admin terlalu lemah: minimal 10 karakter serta mengandung huruf dan angka"
  fi
}

show_plan() {
  step "Rencana pemasangan"
  printf '  %-22s %s\n' "Sistem" "$OS_NAME ($ARCH_RAW, paket: $PM)"
  printf '  %-22s %s\n' "URL publik" "${C_BOLD}${PUBLIC_URL}${C_RESET}"
  printf '  %-22s %s\n' "Akses" "$([[ "$ACCESS_MODE" == domain ]] && echo "domain: $DOMAIN" || echo "IP: $IP_ADDR")"
  printf '  %-22s %s\n' "TLS" "$TLS_MODE"
  printf '  %-22s %s\n' "nginx" "$([[ $WITH_NGINX -eq 1 ]] && echo "ya (proxy 80/443 -> $LISTEN_ADDR)" || echo "tidak (aplikasi langsung di $LISTEN_ADDR)")"
  printf '  %-22s %s\n' "Aplikasi" "$BIN_PATH, frontend di $APP_DIR/frontend"
  printf '  %-22s %s\n' "Database" "$DATA_DIR/dashboard.db"
  printf '  %-22s %s\n' "File manager" "$WORKSPACE_ROOT"
  printf '  %-22s %s\n' "Akun admin" "$ADMIN_USER$([[ ${ADMIN_PASSWORD_GENERATED:-0} -eq 1 ]] && echo ' (kata sandi dibuatkan acak)')"
  printf '  %-22s %s\n' "Sumber" "$([[ $SRC_IS_LOCAL -eq 1 ]] && echo "kode lokal: $SRC_DIR" || echo "repo GitHub: ${REPO_SLUG:-?}@${REPO_REF}")"
  if [[ "$DO_BUILD" == "no" ]]; then
    printf '  %-22s %s\n' "Build" "dilewati (--no-build: $SRC_DIR/bin/dashboard + frontend/dist)"
  elif [[ -n "$BIN_SRC" ]]; then
    printf '  %-22s %s\n' "Build" "dilewati — memakai aset Release (${RELEASE_TAG})"
  elif [[ $SRC_IS_LOCAL -eq 0 && "$RELEASE_TAG" != "none" ]]; then
    printf '  %-22s %s\n' "Build" "aset Release bila ada, jika tidak build dari sumber"
  else
    printf '  %-22s %s\n' "Build" "dari sumber ($SRC_DIR)"
  fi
  if [[ -n "${ADMIN_PASSWORD:-}" && ${ADMIN_PASSWORD_GENERATED:-0} -eq 0 ]]; then
    printf '  %-22s %s\n' "Kata sandi" "(disimpan sementara untuk bootstrap, lalu dihapus dari env)"
  fi
  printf '\n'
  if ! interactive; then
    if [[ $ASSUME_YES -eq 0 ]]; then
      warn "bukan sesi interaktif dan --yes tidak diberikan; melanjutkan dengan nilai di atas."
    fi
    return 0
  fi
  confirm "Lanjutkan pemasangan?" "y" || die "dibatalkan oleh pengguna"
}

# ---------------------------------------------------------------------------
# Sumber aplikasi dari GitHub
# ---------------------------------------------------------------------------
repo_ok() { [[ -n "$REPO_SLUG" ]]; }

# normalize_repo_slug: terima "user/repo", URL GitHub, atau bentuk git@github.com:user/repo.git
normalize_repo_slug() {
  local s="$1"
  s="${s#https://github.com/}"; s="${s#http://github.com/}"; s="${s#github.com/}"
  s="${s#git@github.com:}"; s="${s#/}"
  s="${s%.git}"; s="${s%/}"
  printf '%s' "$s"
}

# gh_curl <url> [tujuan]: unduh dari GitHub, memakai token bila repo privat.
gh_curl() {
  local url="$1" dest="${2:-}"
  if [[ -n "$REPO_TOKEN" ]]; then
    if [[ -n "$dest" ]]; then curl -fsSL --retry 3 --connect-timeout 20 -H "Authorization: Bearer ${REPO_TOKEN}" -o "$dest" "$url"
    else curl -fsSL --retry 3 --connect-timeout 20 -H "Authorization: Bearer ${REPO_TOKEN}" "$url"; fi
  else
    if [[ -n "$dest" ]]; then curl -fsSL --retry 3 --connect-timeout 20 -o "$dest" "$url"
    else curl -fsSL --retry 3 --connect-timeout 20 "$url"; fi
  fi
}

# Unduh tarball kode sumber dari GitHub ke direktori tujuan.
fetch_repo_source() {
  local dest="$1" url tmp
  url="https://api.github.com/repos/${REPO_SLUG}/tarball/${REPO_REF}"
  tmp="$(mktemp -d)"
  info "mengunduh kode sumber ${REPO_SLUG}@${REPO_REF}"
  run rm -rf "$dest"
  run install -d -m 0755 "$dest"
  if ! gh_curl "$url" "$tmp/src.tar.gz"; then
    rm -rf "$tmp"
    die "gagal mengunduh kode sumber ${REPO_SLUG}@${REPO_REF}. Periksa --repo/--repo-ref; repo privat butuh --repo-token."
  fi
  run tar -xzf "$tmp/src.tar.gz" -C "$dest" --strip-components=1
  run rm -rf "$tmp"
  if [[ $DRY_RUN -eq 0 && ! -f "$dest/go.mod" ]]; then
    die "isi repo tidak seperti yang diharapkan (go.mod tidak ditemukan di ${REPO_SLUG}@${REPO_REF})"
  fi
  ok "kode sumber siap: $dest"
}

# Tentukan SRC_DIR: kode di sekitar skrip, --source, atau unduh dari GitHub.
prepare_source() {
  step "Sumber aplikasi"

  if [[ -n "$SOURCE_ARG" ]]; then SRC_DIR="$SOURCE_ARG"; fi

  if [[ -f "$SRC_DIR/go.mod" && -f "$SRC_DIR/frontend/package.json" ]]; then
    SRC_IS_LOCAL=1
    ok "memakai kode sumber lokal: $SRC_DIR"
    return 0
  fi

  if [[ $EXPLICIT_SOURCE -eq 1 ]]; then
    die "kode sumber tidak lengkap di '$SRC_DIR' (perlu go.mod + frontend/package.json)"
  fi

  if ! repo_ok; then
    die "kode sumber tidak ditemukan di '$SRC_DIR' dan repo GitHub belum ditentukan.
  Pilihan: (1) jalankan dari direktori proyek, (2) isi REPO_SLUG di dalam install.sh,
  atau (3) berikan --repo user/nama-repo (opsional --repo-ref <branch/tag>)."
  fi

  SRC_DIR="${APP_DIR}/src"
  if [[ $DRY_RUN -eq 1 ]]; then
    detail "[dry-run] unduh ${REPO_SLUG}@${REPO_REF} -> $SRC_DIR"
    return 0
  fi
  fetch_repo_source "$SRC_DIR"
}

# Coba pakai aset Release (binary + frontend siap-pakai) bila repo punya rilis.
# Sukses -> BIN_SRC & FRONTEND_SRC terisi. Gagal -> pembangun dari sumber.
fetch_release_assets() {
  repo_ok || return 1

  local arch arch_raw="${ARCH_RAW:-$(uname -m)}"
  case "$arch_raw" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) detail "arsitektur ${arch_raw} belum punya aset rilis -> build dari sumber"; return 1 ;;
  esac

  local base
  if [[ "$RELEASE_TAG" == "latest" ]]; then
    base="https://github.com/${REPO_SLUG}/releases/latest/download"
  else
    base="https://github.com/${REPO_SLUG}/releases/download/${RELEASE_TAG}"
  fi

  local stage; stage="$(mktemp -d)"
  info "mencoba aset rilis ${REPO_SLUG} (${RELEASE_TAG}, linux/${arch})"

  if ! gh_curl "${base}/dashboard-linux-${arch}.tar.gz" "$stage/dashboard-linux-${arch}.tar.gz"; then
    detail "aset binary belum tersedia -> build dari sumber"
    rm -rf "$stage"; return 1
  fi
  if ! gh_curl "${base}/frontend-dist.tar.gz" "$stage/frontend-dist.tar.gz"; then
    detail "aset frontend belum tersedia -> build dari sumber"
    rm -rf "$stage"; return 1
  fi

  # Verifikasi checksum bila rilis menyertakan SHA256SUMS (dibuat oleh workflow).
  if gh_curl "${base}/SHA256SUMS" "$stage/SHA256SUMS" 2>/dev/null; then
    if ! ( cd "$stage" && sha256sum -c --ignore-missing SHA256SUMS >/dev/null 2>&1 ); then
      warn "checksum aset rilis TIDAK cocok; unduhan dibatalkan demi keamanan."
      rm -rf "$stage"; return 1
    fi
    ok "checksum aset rilis terverifikasi"
  else
    warn "rilis tanpa SHA256SUMS; melanjutkan tanpa verifikasi checksum"
  fi

  mkdir -p "$stage/frontend"
  if ! tar -xzf "$stage/dashboard-linux-${arch}.tar.gz" -C "$stage" \
     || ! tar -xzf "$stage/frontend-dist.tar.gz" -C "$stage/frontend"; then
    warn "isi aset rilis tidak dapat dibuka -> build dari sumber"
    rm -rf "$stage"; return 1
  fi
  if [[ ! -f "$stage/dashboard" || ! -f "$stage/frontend/index.html" ]]; then
    warn "isi aset rilis tidak lengkap -> build dari sumber"
    rm -rf "$stage"; return 1
  fi

  RELEASE_STAGE="$stage"
  BIN_SRC="$stage/dashboard"
  FRONTEND_SRC="$stage/frontend"
  ok "aset rilis siap dipakai (${RELEASE_TAG}, linux/${arch})"
  return 0
}

# Tentukan artefak mana yang akan dipasang (aset rilis, artefak lokal, atau build).
choose_artifacts() {
  if [[ "$DO_BUILD" == "no" ]]; then
    [[ -f "$SRC_DIR/bin/dashboard" ]] || die "--no-build dipilih tetapi $SRC_DIR/bin/dashboard tidak ada"
    [[ -f "$SRC_DIR/frontend/dist/index.html" ]] || die "--no-build dipilih tetapi $SRC_DIR/frontend/dist tidak ada"
    BIN_SRC="$SRC_DIR/bin/dashboard"
    FRONTEND_SRC="$SRC_DIR/frontend/dist"
    ok "memakai artefak lokal yang sudah ada"
    return 0
  fi
  if [[ "$DO_BUILD" == "yes" ]]; then return 0; fi
  if [[ $SRC_IS_LOCAL -eq 1 ]]; then return 0; fi           # dari direktori proyek: build dari sumber
  if ! repo_ok || [[ "$RELEASE_TAG" == "none" ]]; then return 0; fi

  if [[ $DRY_RUN -eq 1 ]]; then
    detail "[dry-run] coba aset Release ${REPO_SLUG} (${RELEASE_TAG}): dashboard-linux-<arch>.tar.gz + frontend-dist.tar.gz"
    detail "[dry-run] bila tidak ada -> go build ./cmd/server + npm ci && npm run build"
    return 0
  fi
  fetch_release_assets || true
}


install_packages() {
  local pkgs=(ca-certificates curl openssl xz-utils sqlite3)
  [[ $WITH_NGINX -eq 1 ]] && pkgs+=(nginx)
  if [[ "$TLS_MODE" == "letsencrypt" ]]; then
    pkgs+=(certbot)
    [[ "$PM" == "apt" ]] && pkgs+=(python3-certbot-nginx)
    [[ "$PM" == "dnf" || "$PM" == "yum" ]] && pkgs+=(python3-certbot-nginx)
  fi

  case "$PM" in
    apt)
      local missing=() p
      for p in "${pkgs[@]}"; do
        dpkg -s "$p" >/dev/null 2>&1 || missing+=("$p")
      done
      ((${#missing[@]})) || { ok "paket sistem sudah lengkap"; return 0; }
      info "memasang paket: ${missing[*]}"
      run env DEBIAN_FRONTEND=noninteractive apt-get update -qq
      run env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends "${missing[@]}"
      ;;
    dnf|yum)
      info "memasang paket: ${pkgs[*]}"
      run "$PM" -y -q install "${pkgs[@]}"
      ;;
    none)
      warn "manajer paket tidak dikenali; lewati pemasangan paket sistem."
      warn "pastikan ini sudah ada: curl openssl nginx certbot sqlite3 xz."
      ;;
  esac
}

install_go_if_needed() {
  if go_ok; then
    ok "Go $(go env GOVERSION | sed 's/^go//') sudah memenuhi syarat (>= $GO_MIN)"
    return 0
  fi
  command -v go >/dev/null 2>&1 \
    && warn "Go yang terpasang ($(go env GOVERSION 2>/dev/null)) lebih lama dari $GO_MIN; memasang versi resmi terbaru." \
    || info "Go belum ada; memasang dari berkas resmi go.dev."

  local arch="$ARCH_RAW"
  case "$arch" in
    x86_64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) die "pemasangan Go otomatis belum mendukung arsitektur $arch; pasang Go manual lalu jalankan ulang dengan --skip-deps" ;;
  esac

  local ver; ver="$(fetch 'https://go.dev/VERSION?m=text' | head -n1)"
  [[ "$ver" =~ ^go[0-9] ]] || die "tidak dapat menentukan versi Go terbaru"
  local url="https://go.dev/dl/${ver}.linux-${arch}.tar.gz"
  local tmp; tmp="$(mktemp -d)"

  info "mengunduh ${ver} (${arch})"
  run curl -fsSL --retry 3 -o "$tmp/go.tgz" "$url"

  local want got
  want="$(fetch "${url}.sha256" 2>/dev/null | awk '{print $1}' || true)"
  if [[ $DRY_RUN -eq 0 && -n "$want" ]]; then
    got="$(sha256sum "$tmp/go.tgz" | awk '{print $1}')"
    [[ "$got" == "$want" ]] || die "checksum Go tidak cocok; unduhan mungkin rusak"
    ok "checksum Go terverifikasi"
  fi

  run rm -rf /usr/local/go
  run tar -C /usr/local -xzf "$tmp/go.tgz"
  run ln -sf /usr/local/go/bin/go /usr/local/bin/go
  run ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
  run rm -rf "$tmp"
  hash -r 2>/dev/null || true
  command -v go >/dev/null 2>&1 || [[ $DRY_RUN -eq 1 ]] || die "Go gagal dipasang"
  ok "Go terpasang di /usr/local/go"
}

install_node_if_needed() {
  if node_ok; then
    ok "Node.js $(node --version) sudah memenuhi syarat (>= 20)"
    return 0
  fi
  info "Node.js belum ada / terlalu lama; memasang rilis resmi nodejs.org."

  local arch="$ARCH_RAW"
  case "$arch" in
    x86_64) arch="x64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) die "pemasangan Node.js otomatis belum mendukung arsitektur $arch; pasang Node 20+ manual lalu jalankan ulang dengan --skip-deps" ;;
  esac

  local major line="" shasums sum tarball base
  for major in 24 22 20; do
    base="https://nodejs.org/dist/latest-v${major}.x"
    shasums="$(fetch "${base}/SHASUMS256.txt" 2>/dev/null || true)"
    [[ -n "$shasums" ]] || continue
    line="$(grep -E "node-v[0-9.]+-linux-${arch}\.tar\.xz$" <<<"$shasums" | head -n1 || true)"
    [[ -n "$line" ]] && break
  done
  [[ -n "$line" ]] || die "tidak menemukan berkas Node.js untuk arsitektur $arch"

  sum="$(awk '{print $1}' <<<"$line")"
  tarball="$(awk '{print $2}' <<<"$line")"
  local tmp; tmp="$(mktemp -d)"

  info "mengunduh ${tarball}"
  run curl -fsSL --retry 3 -o "$tmp/node.tar.xz" "${base}/${tarball}"
  if [[ $DRY_RUN -eq 0 ]]; then
    local got; got="$(sha256sum "$tmp/node.tar.xz" | awk '{print $1}')"
    [[ "$got" == "$sum" ]] || die "checksum Node.js tidak cocok; unduhan mungkin rusak"
    ok "checksum Node.js terverifikasi"
  fi

  run tar -C /usr/local --strip-components=1 -xJf "$tmp/node.tar.xz"
  run rm -rf "$tmp"
  hash -r 2>/dev/null || true
  command -v node >/dev/null 2>&1 || [[ $DRY_RUN -eq 1 ]] || die "Node.js gagal dipasang"
  ok "Node.js $(node --version 2>/dev/null || echo '(terpasang)') di /usr/local"
}

install_dependencies() {
  step "Dependensi"
  if [[ $SKIP_DEPS -eq 1 ]]; then
    warn "melewati pemasangan paket sistem (--skip-deps)"
  else
    install_packages
  fi

  if [[ -n "$BIN_SRC" ]]; then
    ok "melewati Go/Node: aplikasi dipasang dari artefak siap-pakai"
    return 0
  fi

  install_go_if_needed
  install_node_if_needed
}

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------
build_app() {
  step "Build"

  # Artefak sudah ditentukan choose_artifacts (aset Release / --no-build): tidak perlu build.
  if [[ -n "$BIN_SRC" ]]; then
    ok "memakai artefak siap-pakai — langkah build dilewati"
    return 0
  fi

  if [[ $DRY_RUN -eq 1 ]]; then
    detail "[dry-run] go build ./cmd/server  ->  <tmp>/dashboard"
    detail "[dry-run] npm ci (atau npm install) + npm run build di $SRC_DIR/frontend"
    BUILD_TMP="$(mktemp -d)"
    BIN_SRC="$BUILD_TMP/dashboard"
    FRONTEND_SRC="$SRC_DIR/frontend/dist"
    APP_VERSION="install-dry-run"
    return 0
  fi

  if [[ "$DO_BUILD" == "auto" && -f "$SRC_DIR/bin/dashboard" && -f "$SRC_DIR/frontend/dist/index.html" ]]; then
    info "artefak hasil build sudah ada (bin/dashboard + frontend/dist)"
    if interactive && ! confirm "Build ulang dari sumber?" "n"; then
      BIN_SRC="$SRC_DIR/bin/dashboard"
      FRONTEND_SRC="$SRC_DIR/frontend/dist"
      ok "memakai artefak yang ada (pakai --rebuild kalau ingin memaksa build ulang)"
      return 0
    fi
  fi

  BUILD_TMP="$(mktemp -d)"
  BIN_SRC="$BUILD_TMP/dashboard"
  FRONTEND_SRC="$SRC_DIR/frontend/dist"
  APP_VERSION="install-$(date -u +%Y%m%d%H%M)"

  info "membangun backend (Go)"
  ( cd "$SRC_DIR" \
    && GOFLAGS=-mod=mod GOTOOLCHAIN=local CGO_ENABLED=0 \
       go build -trimpath -ldflags "-X dashboard/internal/httpapi.Version=${APP_VERSION}" \
       -o "$BUILD_TMP/dashboard" ./cmd/server ) \
    || die "build backend gagal"
  ok "backend terbangun ($(du -h "$BUILD_TMP/dashboard" | awk '{print $1}'))"

  info "membangun frontend (Vite)"
  if [[ -f "$SRC_DIR/frontend/package-lock.json" ]]; then
    ( cd "$SRC_DIR/frontend" && npm ci --no-audit --no-fund --loglevel=error ) \
      || ( cd "$SRC_DIR/frontend" && npm install --no-audit --no-fund )
  else
    ( cd "$SRC_DIR/frontend" && npm install --no-audit --no-fund )
  fi
  ( cd "$SRC_DIR/frontend" && npm run build ) || die "build frontend gagal"
  [[ -f "$SRC_DIR/frontend/dist/index.html" ]] || die "hasil build frontend tidak ditemukan (frontend/dist/index.html)"
  ok "frontend terbangun"
}

# ---------------------------------------------------------------------------
# Direktori, user, rahasia
# ---------------------------------------------------------------------------
prepare_host() {
  step "Direktori & pengguna"

  if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
    run useradd --system --home "$DATA_DIR" --shell /usr/sbin/nologin "$SERVICE_USER"
    ok "pengguna sistem '$SERVICE_USER' dibuat (pemilik berkas; proses tetap root)"
  else
    ok "pengguna '$SERVICE_USER' sudah ada"
  fi

  run install -d -m 0755 "$APP_DIR"
  run install -d -m 0750 "$ENV_DIR"
  run install -d -m 0750 "$DATA_DIR"
  if [[ "$WORKSPACE_ROOT" != "/" ]]; then
    run install -d -m 0750 "$WORKSPACE_ROOT"
  fi
  ok "direktori siap: $APP_DIR, $ENV_DIR, $DATA_DIR"

  # Rahasia: pertahankan yang lama bila sudah ada, supaya sesi lama tetap valid.
  local existing_secret; existing_secret="$(env_value DASH_SESSION_SECRET)"
  if [[ -n "$existing_secret" ]]; then
    SESSION_SECRET="$existing_secret"
    ok "DASH_SESSION_SECRET lama dipakai ulang"
  else
    SESSION_SECRET="$(openssl rand -hex 32)"
    ok "DASH_SESSION_SECRET baru dibuat (64 karakter acak)"
  fi

  # Berkas SSH sesi milik root (terminal berjalan sebagai root).
  run install -d -m 0700 "${HOME_DIR}/.ssh"
  run install -d -m 0755 /run/dashboard
}

# ---------------------------------------------------------------------------
# Konfigurasi /etc/dashboard/dashboard.env
# ---------------------------------------------------------------------------
write_env_file() {
  step "Konfigurasi"

  if [[ $WRITE_ENV -eq 0 ]]; then
    ok "konfigurasi yang ada dipertahankan: $ENV_FILE (pakai --reconfigure untuk mengubah)"
    return 0
  fi

  local bootstrap_lines=""
  if [[ -n "$ADMIN_PASSWORD" ]]; then
    bootstrap_lines="DASH_BOOTSTRAP_USER=${ADMIN_USER}
DASH_BOOTSTRAP_PASSWORD=${ADMIN_PASSWORD}"
  else
    bootstrap_lines="DASH_BOOTSTRAP_USER=${ADMIN_USER}
# Kata sandi kosong: dibuat acak dan dicetak sekali oleh layanan saat pertama start.
DASH_BOOTSTRAP_PASSWORD="
  fi

  write_file "$ENV_FILE" 0640 "root:${SERVICE_USER}" <<EOF
# Server Dashboard — konfigurasi produksi.
# Dibuat otomatis oleh install.sh v${INSTALLER_VERSION} pada $(date -u +'%Y-%m-%d %H:%M:%SZ').
# Edit manual lalu: systemctl restart ${SERVICE_NAME}
#
# PENTING: layanan ini berjalan sebagai root dan terminalnya adalah shell root
# penuh. Jangan pernah diekspos ke jaringan tak tepercaya.

# ---- HTTP -------------------------------------------------------------------
DASH_ADDR=${LISTEN_ADDR}
DASH_STATIC_DIR=${APP_DIR}/frontend
DASH_ALLOWED_ORIGINS=${ALLOWED_ORIGINS}
DASH_COOKIE_SECURE=${COOKIE_SECURE}
DASH_TRUSTED_PROXY=${TRUSTED_PROXY}

# ---- Rahasia (jangan dibagikan) --------------------------------------------
DASH_SESSION_SECRET=${SESSION_SECRET}

# Akun admin pertama (hanya dibuat bila tabel users kosong).
${bootstrap_lines}

# ---- Penyimpanan -----------------------------------------------------------
DASH_DB_PATH=${DATA_DIR}/dashboard.db
DASH_WORKSPACE_ROOT=${WORKSPACE_ROOT}
DASH_HOME_DIR=${HOME_DIR}

# ---- Sesi ------------------------------------------------------------------
DASH_SESSION_TTL=${SESSION_TTL}

# ---- Terminal --------------------------------------------------------------
DASH_SHELL=/bin/bash
DASH_TERMINAL_MAX_SESSIONS=8
DASH_TERMINAL_MAX_PER_USER=8
DASH_TERMINAL_IDLE_TIMEOUT=30m
DASH_TERMINAL_SCROLLBACK_BYTES=65536
DASH_TERMINAL_HISTORY=true

# ---- Manajer koneksi SSH ---------------------------------------------------
DASH_SSH_ENABLED=true
DASH_SSH_CONFIG=
DASH_SSH_BIN=ssh
DASH_SSH_KEYSCAN_BIN=ssh-keyscan
DASH_SSH_KEYGEN_BIN=ssh-keygen
DASH_SSH_STATS=true

# ---- Pemantauan host -------------------------------------------------------
DASH_MONITOR_INTERVAL=1s

# ---- Operasi host (layanan, proses, log, pembaruan paket) -------------------
DASH_OPS_ENABLED=true
DASH_OPS_SYSTEMCTL_BIN=systemctl
DASH_OPS_JOURNALCTL_BIN=journalctl
DASH_OPS_LOG_DIR=/var/log
DASH_OPS_PROC_ROOT=/proc

# ---- Autentikasi dua faktor (TOTP) -----------------------------------------
DASH_TOTP_ISSUER=${TOTP_ISSUER}

# ---- Batas berkas ----------------------------------------------------------
DASH_MAX_READ_SIZE=64M
DASH_MAX_WRITE_SIZE=64M
DASH_MAX_LIST_ENTRIES=50000
# Harus sejalan dengan client_max_body_size di nginx.
DASH_MAX_UPLOAD_SIZE=${MAX_UPLOAD}

# ---- Audit -----------------------------------------------------------------
DASH_AUDIT_RETENTION_DAYS=30

# ---- Log -------------------------------------------------------------------
DASH_LOG_LEVEL=${LOG_LEVEL}
EOF

  ok "konfigurasi ditulis: $ENV_FILE (0640 root:${SERVICE_USER})"
}

# ---------------------------------------------------------------------------
# Binary, frontend, unit systemd
# ---------------------------------------------------------------------------
install_app() {
  step "Pemasangan aplikasi"

  if [[ $DRY_RUN -eq 0 ]]; then
    [[ -n "$BIN_SRC" && -f "$BIN_SRC" ]] || die "binary aplikasi tidak tersedia (unduhan rilis / build gagal)"
    [[ -n "$FRONTEND_SRC" && -f "$FRONTEND_SRC/index.html" ]] || die "frontend hasil build tidak tersedia"
  fi

  run install -m 0755 "$BIN_SRC" "$BIN_PATH"
  ok "binary: $BIN_PATH"

  # Frontend: bersihkan aset lama supaya tidak ada berkas yatim.
  run install -d -m 0755 "$APP_DIR/frontend"
  run_sh "rm -f ${APP_DIR}/frontend/assets/index-*.js ${APP_DIR}/frontend/assets/index-*.css"
  run_sh "cp -a ${FRONTEND_SRC}/. ${APP_DIR}/frontend/"
  ok "frontend: $APP_DIR/frontend"

  local unit_src="$SRC_DIR/deploy/systemd/dashboard.service"
  if [[ ! -f "$unit_src" ]]; then
    if [[ $DRY_RUN -eq 1 ]]; then
      detail "[dry-run] unit systemd dari $unit_src (kode sumber belum diunduh)"
      return 0
    fi
    die "unit systemd tidak ditemukan: $unit_src"
  fi
  # Path di unit sudah sesuai dengan konstanta di skrip ini; hanya baris
  # Documentation yang disesuaikan dengan URL pemasangan Anda.
  local unit_doc
  if [[ -n "$PUBLIC_URL" ]]; then unit_doc="Documentation=${PUBLIC_URL}"; else unit_doc="# Documentation: (URL belum ditentukan)"; fi
  sed -e "s|^Documentation=.*$|${unit_doc}|" "$unit_src" | write_file "$SERVICE_FILE" 0644 root:root
  ok "unit systemd: $SERVICE_FILE"

  run systemctl daemon-reload
  run systemctl enable "$SERVICE_NAME"
}

# ---------------------------------------------------------------------------
# nginx
# ---------------------------------------------------------------------------
configure_nginx() {
  [[ $WITH_NGINX -eq 1 ]] || { warn "melewati nginx (--no-nginx)"; return 0; }
  if [[ $CONFIGURE_NGINX -eq 0 ]]; then
    ok "vhost nginx yang ada dipertahankan tanpa diubah (pakai --reconfigure untuk mengubah)"
    return 0
  fi
  step "nginx"

  command -v nginx >/dev/null 2>&1 || die "nginx tidak terpasang"

  if [[ ! -f "$SRC_DIR/deploy/nginx/dashboard.conf.template" ]]; then
    if [[ $DRY_RUN -eq 1 ]]; then
      detail "[dry-run] vhost nginx dari $SRC_DIR/deploy/nginx/*.template (kode sumber belum diunduh)"
      return 0
    fi
    die "template nginx tidak ditemukan di $SRC_DIR/deploy/nginx"
  fi

  install_src "$SRC_DIR/deploy/nginx/upgrade-map.conf" "$NGINX_UPGRADE_MAP" 0644

  local template="$SRC_DIR/deploy/nginx/dashboard.conf.template"
  if [[ "$TLS_MODE" == "selfsigned" ]]; then
    template="$SRC_DIR/deploy/nginx/dashboard-tls.conf.template"
  fi
  [[ -f "$template" ]] || die "template nginx tidak ditemukan: $template"

  local rendered
  rendered="$(sed \
    -e "s|__SERVER_NAME__|${SERVER_NAME}|g" \
    -e "s|__DEFAULT_SERVER__|${DEFAULT_SERVER}|g" \
    -e "s|__APP_PORT__|${APP_PORT}|g" \
    -e "s|__MAX_BODY__|${NGINX_MAX_BODY}|g" \
    -e "s|__CERT_FILE__|${SELF_SIGNED_CRT}|g" \
    -e "s|__KEY_FILE__|${SELF_SIGNED_KEY}|g" \
    "$template")"

  printf '%s\n' "$rendered" | write_file "$NGINX_AVAILABLE" 0644
  if [[ "$NGINX_AVAILABLE" != "$NGINX_ENABLED" ]]; then
    run ln -sf "$NGINX_AVAILABLE" "$NGINX_ENABLED"
  fi

  # Mode IP memakai default_server: vhost bawaan distro harus minggir.
  if [[ "$ACCESS_MODE" == "ip" && -e /etc/nginx/sites-enabled/default ]]; then
    warn "menonaktifkan vhost bawaan /etc/nginx/sites-enabled/default (dicadangkan)"
    run_sh "mv /etc/nginx/sites-enabled/default /etc/nginx/sites-enabled/default.disabled-by-dashboard 2>/dev/null || true"
  fi

  # Peringatkan bila ada vhost lain yang sudah mem-proxy aplikasi ini (dobel).
  local other target
  for other in /etc/nginx/sites-enabled/*; do
    [[ -e "$other" ]] || continue
    target="$(readlink -f "$other" 2>/dev/null || echo "$other")"
    [[ "$target" == "$NGINX_AVAILABLE" ]] && continue
    if grep -qsE "127\\.0\\.0\\.1:${APP_PORT}|dashboard_app|dashboard_go" "$target" 2>/dev/null; then
      warn "vhost lain ($(basename "$other")) juga mem-proxy aplikasi ini."
      warn "nonaktifkan salah satu agar tidak dobel: rm -f /etc/nginx/sites-enabled/$(basename "$other")"
    fi
  done

  if [[ $DRY_RUN -eq 0 ]]; then
    nginx -t || die "konfigurasi nginx tidak valid; periksa $NGINX_AVAILABLE"
  else
    detail "[dry-run] nginx -t"
  fi
  run systemctl reload nginx
  ok "vhost aktif: $NGINX_ENABLED (server_name ${SERVER_NAME})"
}

# Sertifikat self-signed (CN + SAN berisi domain/IP).
make_self_signed_cert() {
  step "Sertifikat self-signed"
  local cn="/CN=dashboard" san="IP:${IP_ADDR}"
  if [[ "$ACCESS_MODE" == "domain" && -n "$DOMAIN" ]]; then
    cn="/CN=${DOMAIN}"
    san="DNS:${DOMAIN},IP:${IP_ADDR}"
  fi

  run install -d -m 0750 "$SELF_SIGNED_DIR"
  run openssl req -x509 -nodes -newkey rsa:2048 -days 825 \
    -keyout "$SELF_SIGNED_KEY" -out "$SELF_SIGNED_CRT" \
    -subj "$cn" -addext "subjectAltName=${san}" \
    -addext "basicConstraints=critical,CA:FALSE" \
    -addext "keyUsage=critical,digitalSignature,keyEncipherment"
  run chmod 0640 "$SELF_SIGNED_KEY"
  ok "sertifikat: $SELF_SIGNED_CRT (berlaku 825 hari)"
}

# Let's Encrypt via certbot (certbot yang menulis blok 443 + redirect).
enable_letsencrypt() {
  step "HTTPS Let's Encrypt"
  command -v certbot >/dev/null 2>&1 || die "certbot tidak terpasang"

  local args=(--nginx -d "$DOMAIN" --non-interactive --agree-tos --redirect)
  if [[ -n "$ACME_EMAIL" ]]; then
    args+=(-m "$ACME_EMAIL")
  else
    args+=(--register-unsafely-without-email)
  fi

  if [[ $DRY_RUN -eq 1 ]]; then
    detail "[dry-run] certbot ${args[*]}"
    return 0
  fi

  if certbot "${args[@]}"; then
    ok "sertifikat Let's Encrypt terpasang dan HTTP dialihkan ke HTTPS"
    return 0
  fi

  warn "certbot gagal (biasanya karena DNS domain belum mengarah ke server ini,"
  warn "atau port 80 belum bisa diakses dari internet). Dashboard tetap dapat dipakai lewat HTTP."
  warn "Setelah DNS benar, jalankan: certbot --nginx -d ${DOMAIN} --redirect"
  warn "lalu ubah DASH_COOKIE_SECURE=true di $ENV_FILE dan restart layanan."
  TLS_MODE="none"
  COOKIE_SECURE="false"
  return 0
}

# ---------------------------------------------------------------------------
# Firewall
# ---------------------------------------------------------------------------
configure_firewall() {
  [[ "$WITH_FIREWALL" == "1" ]] || return 0
  step "Firewall"

  if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qi '^Status: active'; then
    [[ $WITH_NGINX -eq 1 ]] && run ufw allow 'Nginx Full' || { run ufw allow 80/tcp; run ufw allow 443/tcp; }
    ok "aturan ufw ditambahkan (SSH tidak disentuh)"
  elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    run firewall-cmd --permanent --add-service=http
    run firewall-cmd --permanent --add-service=https
    run firewall-cmd --reload
    ok "aturan firewalld ditambahkan (SSH tidak disentuh)"
  else
    warn "ufw/firewalld tidak aktif; lewati pengaturan firewall."
    warn "kalau ada firewall lain (mis. iptables/security group cloud), buka port 80 dan 443 sendiri."
  fi
}

# ---------------------------------------------------------------------------
# Menyalakan layanan & verifikasi
# ---------------------------------------------------------------------------
start_and_verify() {
  step "Menjalankan layanan"

  if systemctl is-active --quiet "$SERVICE_NAME"; then
    info "me-restart layanan (sesi terminal yang sedang terbuka akan terputus)"
  fi
  run systemctl restart "$SERVICE_NAME"

  if [[ $DRY_RUN -eq 1 ]]; then
    detail "[dry-run] verifikasi dilewati"
    return 0
  fi

  sleep 3
  systemctl is-active --quiet "$SERVICE_NAME" || {
    err "layanan gagal berjalan. 40 baris log terakhir:"
    journalctl -u "$SERVICE_NAME" -n 40 --no-pager >&2 || true
    die "perbaiki konfigurasi di $ENV_FILE lalu: systemctl restart $SERVICE_NAME"
  }
  ok "layanan aktif (${SERVICE_NAME}.service)"

  # 1) aplikasi langsung
  local health=""
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    health="$(curl -fsS --max-time 3 "http://127.0.0.1:${APP_PORT}/api/health" 2>/dev/null || true)"
    [[ -n "$health" ]] && break
    sleep 1
  done
  if [[ -n "$health" ]]; then ok "API lokal: $health"; else warn "API lokal belum membalas di 127.0.0.1:${APP_PORT}"; fi

  # 2) lewat nginx / URL publik
  local code
  code="$(curl -k -s -o /dev/null -w '%{http_code}' --max-time 8 "$PUBLIC_URL/" 2>/dev/null || true)"
  if [[ "$code" == "200" ]]; then ok "URL publik ${PUBLIC_URL} menjawab 200"
  else warn "URL publik ${PUBLIC_URL} menjawab '${code:-tidak ada balasan}'; kalau baru pasang DNS/TLS, tunggu sebentar."; fi

  # 3) akun admin benar-benar dibuat
  local created=0
  if command -v sqlite3 >/dev/null 2>&1 && [[ -f "${DATA_DIR}/dashboard.db" ]]; then
    if [[ "$(sqlite3 "${DATA_DIR}/dashboard.db" 'SELECT COUNT(1) FROM users;' 2>/dev/null)" =~ ^[1-9] ]]; then
      created=1
    fi
  fi
  if [[ $created -eq 0 ]] && journalctl -u "$SERVICE_NAME" -n 200 --no-pager 2>/dev/null | grep -q "bootstrap admin user created"; then
    created=1
  fi
  if [[ $created -eq 1 ]]; then
    ok "akun admin siap di database"
    # Kata sandi bootstrap tidak diperlukan lagi: hapus dari berkas env.
    if grep -q '^DASH_BOOTSTRAP_PASSWORD=.\+' "$ENV_FILE" 2>/dev/null; then
      local tmp="${ENV_FILE}.tmp.$$"
      grep -v '^DASH_BOOTSTRAP_PASSWORD=' "$ENV_FILE" >"$tmp"
      printf 'DASH_BOOTSTRAP_PASSWORD=\n' >>"$tmp"
      chmod 0640 "$tmp"
      chown "root:${SERVICE_USER}" "$tmp" 2>/dev/null || true
      mv "$tmp" "$ENV_FILE"
      ok "kata sandi bootstrap dihapus dari $ENV_FILE (akun sudah dibuat)"
    fi
  else
    warn "belum dapat memastikan akun admin dibuat; periksa: journalctl -u $SERVICE_NAME | tail"
  fi
}

# ---------------------------------------------------------------------------
# Ringkasan
# ---------------------------------------------------------------------------
summary() {
  if [[ $DRY_RUN -eq 1 ]]; then
    step "Selesai (dry-run)"
    printf '\n'
    printf '%b\n' "${C_YELLOW}${C_BOLD}  Tidak ada perubahan yang dilakukan (mode --dry-run).${C_RESET}"
    printf '\n'
    printf '  Rencana di atas akan dijalankan bila tanpa --dry-run. URL yang dituju: %s\n' "${PUBLIC_URL}"
    printf '\n'
    return 0
  fi

  step "Selesai"
  printf '\n'
  printf '%b\n' "${C_GREEN}${C_BOLD}  Dashboard siap dipakai!${C_RESET}"
  printf '\n'
  printf '  %-18s %b\n' "URL" "${C_BOLD}${PUBLIC_URL}${C_RESET}"
  printf '  %-18s %s\n' "Nama pengguna" "${ADMIN_USER}"
  if [[ ${ADMIN_PASSWORD_GENERATED:-0} -eq 1 ]]; then
    printf '  %-18s %b\n' "Kata sandi" "${C_BOLD}${ADMIN_PASSWORD}${C_RESET}  ${C_YELLOW}(ditampilkan sekali, segera ganti)${C_RESET}"
  else
    printf '  %-18s %s\n' "Kata sandi" "(yang Anda isi tadi)"
  fi
  printf '\n'
  printf '  Langkah berikutnya:\n'
  printf '   1. Buka %s dan login.\n' "$PUBLIC_URL"
  printf '   2. Aktifkan 2FA di halaman Keamanan (scan QR dengan Google Authenticator/Authy).\n'
  printf '   3. Ganti nama pengguna/kata sandi kapan saja di menu pengguna -> Profil saya.\n'
  if [[ "$TLS_MODE" == "selfsigned" ]]; then
    printf '   4. Browser akan memperingatkan sertifikat self-signed; itu wajar. Pasang domain\n'
    printf '      lalu jalankan certbot untuk sertifikat resmi.\n'
  elif [[ "$TLS_MODE" == "none" && "$ACCESS_MODE" == "ip" ]]; then
    printf '   4. Koneksi masih HTTP (tanpa enkripsi). Sangat disarankan memakai domain + HTTPS,\n'
    printf '      atau jalankan ulang: sudo bash install.sh --tls selfsigned --reconfigure\n'
  fi
  printf '\n'
  printf '  Perintah berguna:\n'
  printf '   systemctl status %s\n' "$SERVICE_NAME"
  printf '   journalctl -u %s -f\n' "$SERVICE_NAME"
  printf '   %s reset-totp --user %s      # darurat: matikan 2FA\n' "$BIN_PATH" "$ADMIN_USER"
  printf '   bash install.sh --reconfigure               # ubah konfigurasi\n'
  printf '   bash install.sh --uninstall                 # lepaskan pemasangan\n'
  printf '\n'
  printf '%b\n' "${C_YELLOW}  Peringatan keamanan:${C_RESET} proses ini berjalan sebagai root dan terminal webnya"
  printf '%b\n' "  adalah shell root penuh. Jangan diekspos tanpa HTTPS/firewall/VPN."
  printf '\n'
  if [[ -n "$LOG_FILE" ]]; then printf '  Log pemasangan: %s\n\n' "$LOG_FILE"; fi
}

# ---------------------------------------------------------------------------
# Uninstall
# ---------------------------------------------------------------------------
do_uninstall() {
  step "Melepas pemasangan"
  if [[ $ASSUME_YES -eq 0 ]] && ! confirm "Hentikan dan hapus layanan ${SERVICE_NAME}?" "n"; then
    die "dibatalkan"
  fi

  run systemctl disable --now "$SERVICE_NAME" || true
  run rm -f "$SERVICE_FILE"
  run systemctl daemon-reload

  if command -v nginx >/dev/null 2>&1; then
    run rm -f "$NGINX_ENABLED" "$NGINX_AVAILABLE"
    if [[ -e /etc/nginx/sites-enabled/default.disabled-by-dashboard ]]; then
      run mv /etc/nginx/sites-enabled/default.disabled-by-dashboard /etc/nginx/sites-enabled/default
    fi
    if [[ $DRY_RUN -eq 0 ]]; then nginx -t >/dev/null 2>&1 && systemctl reload nginx || true; fi
  fi

  run rm -f "$BIN_PATH"
  run rm -rf "$APP_DIR/frontend"

  if [[ $PURGE -eq 1 ]]; then
    if [[ $ASSUME_YES -eq 0 ]] && ! confirm "HAPUS juga basis data, konfigurasi, dan direktori data? (tidak bisa dikembalikan)" "n"; then
      warn "melewati penghapusan data"
    else
      run rm -rf "$ENV_DIR" "$DATA_DIR" "$SELF_SIGNED_DIR"
      [[ "$WORKSPACE_ROOT" != "/" ]] && run rm -rf "$WORKSPACE_ROOT"
      ok "basis data, konfigurasi, dan direktori data dihapus"
    fi
  else
    info "basis data & konfigurasi dibiarkan: $DATA_DIR, $ENV_DIR"
    info "hapus juga dengan: bash install.sh --uninstall --purge"
  fi

  ok "pemasangan dilepas"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
main() {
  parse_args "$@"

  if [[ $SELF_TEST -eq 1 ]]; then
    local st_path
    st_path="$(readlink -f "${BASH_SOURCE[0]}")"
    SRC_DIR="${SOURCE_ARG:-$(dirname "$st_path")}"
    if self_test; then exit 0; else exit 1; fi
  fi

  if [[ $DRY_RUN -eq 0 && $EUID -ne 0 ]]; then
    die "jalankan sebagai root:  sudo bash install.sh"
  fi
  if [[ $EUID -ne 0 ]]; then
    warn "dijalankan tanpa hak root; hanya --dry-run yang aman (tidak ada perubahan sistem)."
  fi

  # Lokasi sumber (aman juga saat dijalankan lewat `curl | bash`, BASH_SOURCE kosong).
  local script_path script_dir
  if [[ -n "${BASH_SOURCE[0]:-}" && -f "${BASH_SOURCE[0]}" ]]; then
    script_path="$(readlink -f "${BASH_SOURCE[0]}")"
    script_dir="$(dirname "$script_path")"
  else
    script_dir="$(pwd)"
  fi
  SRC_DIR="${SOURCE_ARG:-$script_dir}"

  # Repo GitHub: terima "user/repo", URL penuh, atau bentuk git@github.com:user/repo.git
  REPO_SLUG="$(normalize_repo_slug "$REPO_SLUG")"
  if [[ -n "$REPO_SLUG" && "$REPO_SLUG" != */* ]]; then
    die "--repo harus berbentuk user/nama-repo (diberikan: '$REPO_SLUG')"
  fi
  RELEASE_TAG="$(printf '%s' "$RELEASE_TAG" | tr '[:upper:]' '[:lower:]')"
  if [[ "$RELEASE_TAG" != "latest" && "$RELEASE_TAG" != "none" && "$RELEASE_TAG" != v* && -n "$RELEASE_TAG" ]]; then
    warn "nilai --release '$RELEASE_TAG' tidak lazim (biasanya: latest, none, atau v1.2.3)"
  fi

  LOG_FILE="/var/log/dashboard-install-$(date -u +%Y%m%d-%H%M%S).log"
  if [[ $DRY_RUN -eq 0 && $UNINSTALL -eq 0 ]]; then
    exec > >(tee -a "$LOG_FILE") 2>&1
  fi

  printf '\n%b\n' "${C_BOLD}Server Dashboard — pemasangan cepat v${INSTALLER_VERSION}${C_RESET}"
  local dr_label="tidak"
  if [[ $DRY_RUN -eq 1 ]]; then dr_label="ya (tidak ada perubahan)"; fi
  printf '%b\n' "${C_DIM}dry-run: ${dr_label}  |  log: ${LOG_FILE}${C_RESET}"

  detect_system
  info "sistem: ${OS_NAME} (${ARCH_RAW}), manajer paket: ${PM}, IP: ${IP_ADDR}"

  if [[ $UNINSTALL -eq 1 ]]; then
    # Ambil nilai lama sebisanya agar tahu apa yang harus dibersihkan.
    local v
    v="$(env_value DASH_WORKSPACE_ROOT)"
    if [[ -n "$v" ]]; then WORKSPACE_ROOT="$v"; fi
    do_uninstall
    return 0
  fi

  if [[ $PURGE -eq 1 ]]; then
    die "--purge hanya berlaku bersama --uninstall (mis. bash install.sh --uninstall --purge)"
  fi

  # Sumber aplikasi: kode lokal, atau unduh dari repo GitHub (mode curl | bash).
  prepare_source

  # Tentukan artefak yang akan dipasang: aset Release, artefak lokal, atau build.
  choose_artifacts

  GO_MIN="$(awk '/^go /{print $2}' "$SRC_DIR/go.mod" 2>/dev/null || true)"
  GO_MIN="${GO_MIN:-1.24}"

  # ---------------------------------------------------------------------------
  # Pemasangan yang sudah ada -> mode perbarui (aman) atau --reconfigure (penuh)
  # ---------------------------------------------------------------------------
  if [[ -f "$ENV_FILE" ]]; then
    EXISTING=1
    step "Pemasangan yang sudah ada terdeteksi"
    local old_addr old_ws old_home old_admin
    old_addr="$(env_value DASH_ADDR)"
    old_ws="$(env_value DASH_WORKSPACE_ROOT)"
    old_home="$(env_value DASH_HOME_DIR)"
    old_admin="$(env_value DASH_BOOTSTRAP_USER)"
    OLD_ORIGINS="$(env_value DASH_ALLOWED_ORIGINS)"
    OLD_COOKIE_SECURE="$(env_value DASH_COOKIE_SECURE)"
    OLD_TRUSTED_PROXY="$(env_value DASH_TRUSTED_PROXY)"

    # Nilai lama hanya dipakai bila flag terkait tidak diberikan pengguna.
    if ! given --port && ! given --listen; then
      APP_PORT="${old_addr##*:}"; APP_PORT="${APP_PORT:-8080}"
      if [[ -n "$old_addr" ]]; then LISTEN_ADDR="$old_addr"; fi
    fi
    if [[ -n "$old_ws" ]] && ! given --workspace; then WORKSPACE_ROOT="$old_ws"; fi
    if [[ -n "$old_home" ]] && ! given --home-dir; then HOME_DIR="$old_home"; fi
    if [[ -n "$old_admin" ]] && ! given --admin-user; then ADMIN_USER="$old_admin"; fi
    local old_upload; old_upload="$(env_value DASH_MAX_UPLOAD_SIZE)"
    if [[ -n "$old_upload" ]] && ! given --max-upload; then MAX_UPLOAD="$old_upload"; fi
    local old_ttl; old_ttl="$(env_value DASH_SESSION_TTL)"
    if [[ -n "$old_ttl" ]] && ! given --session-ttl; then SESSION_TTL="$old_ttl"; fi

    # Simpulkan mode akses/TLS dari konfigurasi lama.
    if [[ -n "$OLD_ORIGINS" ]]; then
      local first_origin="${OLD_ORIGINS%%,*}"
      local host_part="${first_origin#*://}"
      if [[ "$host_part" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+ ]]; then
        ACCESS_MODE="${ACCESS_MODE:-ip}"; IP_ADDR="${host_part%%:*}"
      elif [[ -n "$host_part" ]]; then
        ACCESS_MODE="${ACCESS_MODE:-domain}"; DOMAIN="${DOMAIN:-${host_part%%:*}}"
      fi
    fi
    if [[ -z "$TLS_MODE" ]]; then
      # Tanpa nginx, TLS Let's Encrypt tidak mungkin: anggap mode HTTP saja.
      if [[ "$OLD_COOKIE_SECURE" == "true" && $WITH_NGINX -eq 1 ]]; then TLS_MODE="letsencrypt"; else TLS_MODE="none"; fi
    fi

    if [[ $RECONFIGURE -eq 1 || $EXPLICIT_CONFIG -eq 1 ]]; then
      RECONFIGURE=1
      info "mode konfigurasi ulang: nilai lama dipakai sebagai bawaan, lalu ditulis ulang"
    else
      WRITE_ENV=0
      CONFIGURE_NGINX=0
      info "mode perbarui: biner & frontend diganti, konfigurasi/nginx dipertahankan"
      # Akun sudah ada: jangan tulis ulang kata sandi bootstrap.
      ADMIN_PASSWORD=""; ADMIN_PASSWORD_FILE=""; GEN_PASSWORD=0
    fi
    ok "konfigurasi lama dimuat (port ${APP_PORT}, akun ${ADMIN_USER}, TLS ${TLS_MODE})"
  fi

  # Tanya hanya saat pemasangan baru atau konfigurasi ulang.
  if interactive && { [[ $EXISTING -eq 0 ]] || [[ $RECONFIGURE -eq 1 ]]; }; then
    prompt_all
  fi
  if [[ -z "$ACCESS_MODE" ]]; then ACCESS_MODE="ip"; fi

  if [[ -n "$ADMIN_PASSWORD_FILE" ]]; then
    [[ -f "$ADMIN_PASSWORD_FILE" ]] || die "berkas kata sandi tidak ditemukan: $ADMIN_PASSWORD_FILE"
    ADMIN_PASSWORD="$(head -n1 "$ADMIN_PASSWORD_FILE")"
  fi

  finalize_config
  show_plan
  install_dependencies
  build_app
  prepare_host
  write_env_file
  install_app
  # Sertifikat self-signed harus ada SEBELUM nginx diuji (nginx -t memeriksa berkasnya).
  if [[ "$TLS_MODE" == "selfsigned" && $CONFIGURE_NGINX -eq 1 ]]; then make_self_signed_cert; fi
  configure_nginx
  if [[ "$TLS_MODE" == "letsencrypt" && $CONFIGURE_NGINX -eq 1 ]]; then enable_letsencrypt; fi
  # certbot bisa menurunkan mode ke "none" bila gagal: tulis ulang env bila berubah.
  if [[ $WRITE_ENV -eq 1 && "$TLS_MODE" == "none" && "$COOKIE_SECURE" != "$(env_value DASH_COOKIE_SECURE)" ]]; then
    write_env_file
  fi
  configure_firewall
  start_and_verify
  summary
}

main "$@"
