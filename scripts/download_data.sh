#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
export LANG=C

DATA_DIR="${DATA_DIR:-data}"
DBIP_MONTH="${DBIP_MONTH:-$(date -u +%Y-%m)}"
STATE_FILE="${STATE_FILE:-$DATA_DIR/download-state.tsv}"
CHANGED_FILE="${CHANGED_FILE:-$DATA_DIR/download-changed.txt}"
DOWNLOAD_CHANGED=0

DBIP_CITY_CSV_URL="${DBIP_CITY_CSV_URL:-https://download.db-ip.com/free/dbip-city-lite-${DBIP_MONTH}.csv.gz}"
SYPEX_CITY_URL="${SYPEX_CITY_URL:-https://sypexgeo.net/files/SxGeoCity_utf8.zip}"
SYPEX_COUNTRY_URL="${SYPEX_COUNTRY_URL:-https://sypexgeo.net/files/SxGeoCountry.zip}"
IP2LOCATION_FILE="${IP2LOCATION_FILE:-DB5LITEBIN}"
MAXMIND_DB="${MAXMIND_DB:-GeoLite2-City}"
RIR_STATS_DIR="${RIR_STATS_DIR:-$DATA_DIR/rir}"
GEOFEED_DIR="${GEOFEED_DIR:-$DATA_DIR/geofeeds}"

mkdir -p "$DATA_DIR"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/geo-data.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

log() { printf '\033[36m>>\033[0m %s\n' "$*"; }
ok() { printf '\033[32m+\033[0m %s\n' "$*"; }
warn() { printf '\033[33m!\033[0m %s\n' "$*"; }
die() { printf '\033[31mx\033[0m %s\n' "$*" >&2; exit 1; }

need_cmd() {
    command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

download() {
    local url="$1"
    local out="$2"
    shift 2
    curl -fL --retry 3 --retry-delay 2 "$@" -o "$out" "$url"
}

checksum_file() {
    shasum -a 256 "$1" | awk '{print $1}'
}

remote_header() {
    local url="$1"
    local header="$2"
    shift 2
    local header_lower
    header_lower="$(printf '%s' "$header" | tr '[:upper:]' '[:lower:]')"
    curl -fsSIL "$@" "$url" |
        awk -v h="$header_lower" '
            BEGIN { FS=": *" }
            tolower($1) == h { v=$2 }
            END { gsub(/\r/, "", v); print v }
        '
}

state_set() {
    local name="$1"
    local url="$2"
    local dest="$3"
    local checksum="$4"
    local size="$5"
    local etag="$6"
    local last_modified="$7"
    local tmp="$STATE_FILE.tmp"

    mkdir -p "$(dirname "$STATE_FILE")"
    touch "$STATE_FILE"
    awk -F '\t' -v name="$name" '$1 != name' "$STATE_FILE" > "$tmp"
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$name" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$url" "$dest" "$checksum" "$size" "$etag" "$last_modified" >> "$tmp"
    mv "$tmp" "$STATE_FILE"
}

install_if_changed() {
    local name="$1"
    local url="$2"
    local src="$3"
    local dest="$4"
    local etag="${5:-}"
    local last_modified="${6:-}"

    [[ -s "$src" ]] || die "${name}: extracted file is empty"
    local new_sum old_sum size
    new_sum="$(checksum_file "$src")"
    size="$(wc -c < "$src" | tr -d ' ')"
    if [[ -s "$dest" ]]; then
        old_sum="$(checksum_file "$dest")"
        if [[ "$new_sum" == "$old_sum" ]]; then
            rm -f "$src"
            ok "${name} unchanged: $dest"
            state_set "$name" "$url" "$dest" "$new_sum" "$size" "$etag" "$last_modified"
            return 1
        fi
    fi

    mv "$src" "$dest"
    DOWNLOAD_CHANGED=1
    ok "${name} installed: $dest"
    state_set "$name" "$url" "$dest" "$new_sum" "$size" "$etag" "$last_modified"
    return 0
}

install_from_zip() {
    local zip_path="$1"
    local wanted="$2"
    local dest="$3"
    unzip -tq "$zip_path" >/dev/null 2>&1 || return 1
    local found
    found="$(zipinfo -1 "$zip_path" | grep -E "(^|/)${wanted}$" | head -1 || true)"
    if [[ -z "$found" ]]; then
        return 1
    fi
    unzip -p "$zip_path" "$found" > "$dest"
}

install_first_zip_match() {
    local zip_path="$1"
    local pattern="$2"
    local dest="$3"
    unzip -tq "$zip_path" >/dev/null 2>&1 || return 1
    local found
    found="$(zipinfo -1 "$zip_path" | grep -Ei "$pattern" | head -1 || true)"
    if [[ -z "$found" ]]; then
        return 1
    fi
    unzip -p "$zip_path" "$found" > "$dest"
}

diagnose_bad_archive() {
    local path="$1"
    local label="$2"
    local size
    size="$(wc -c < "$path" | tr -d ' ')"
    warn "${label} did not return a valid archive (${size} bytes)"
    if [[ "$size" -le 512 ]]; then
        warn "${label} response: $(tr -d '\r\n' < "$path" | head -c 240)"
    fi
}

need_cmd curl
need_cmd gzip
need_cmd unzip
need_cmd zipinfo
need_cmd tar
need_cmd shasum

download_dbip() {
    log "Downloading DB-IP City Lite CSV"
    local dest="$DATA_DIR/dbip-city-lite.csv"
    local gz="$TMP_DIR/dbip-city-lite.csv.gz"
    local etag last_modified
    etag="$(remote_header "$DBIP_CITY_CSV_URL" ETag || true)"
    last_modified="$(remote_header "$DBIP_CITY_CSV_URL" Last-Modified || true)"
    download "$DBIP_CITY_CSV_URL" "$gz"
    gzip -dc "$gz" > "$dest.tmp"
    install_if_changed "dbip-city-lite" "$DBIP_CITY_CSV_URL" "$dest.tmp" "$dest" "$etag" "$last_modified" || true
}

download_sypex_city() {
    log "Downloading Sypex City"
    local dest="$DATA_DIR/SxGeoCity.dat"
    local zip="$TMP_DIR/SxGeoCity_utf8.zip"
    local etag last_modified
    etag="$(remote_header "$SYPEX_CITY_URL" ETag || true)"
    last_modified="$(remote_header "$SYPEX_CITY_URL" Last-Modified || true)"
    download "$SYPEX_CITY_URL" "$zip"
    install_from_zip "$zip" "SxGeoCity.dat" "$dest.tmp" || die "SxGeoCity.dat not found in archive"
    install_if_changed "sypex-city" "$SYPEX_CITY_URL" "$dest.tmp" "$dest" "$etag" "$last_modified" || true
}

download_sypex_country() {
    log "Downloading Sypex Country"
    local dest="$DATA_DIR/SxGeoCountry.dat"
    local zip="$TMP_DIR/SxGeoCountry.zip"
    local etag last_modified
    etag="$(remote_header "$SYPEX_COUNTRY_URL" ETag || true)"
    last_modified="$(remote_header "$SYPEX_COUNTRY_URL" Last-Modified || true)"
    if download "$SYPEX_COUNTRY_URL" "$zip"; then
        install_from_zip "$zip" "SxGeo.dat" "$dest.tmp" ||
            install_from_zip "$zip" "SxGeoCountry.dat" "$dest.tmp" ||
            die "SxGeo country .dat not found in archive"
        install_if_changed "sypex-country" "$SYPEX_COUNTRY_URL" "$dest.tmp" "$dest" "$etag" "$last_modified" || true
    else
        warn "Sypex Country download failed; builder does not require it"
    fi
}

download_ip2location() {
    local dest="$DATA_DIR/IP2LOCATION-LITE-DB5.BIN"
    if [[ -z "${IP2LOCATION_TOKEN:-}" && -z "${IP2LOCATION_URL:-}" ]]; then
        warn "IP2LOCATION_TOKEN not set; skipping IP2Location"
        return
    fi

    log "Downloading IP2Location Lite ${IP2LOCATION_FILE}"
    local url="${IP2LOCATION_URL:-https://www.ip2location.com/download?token=${IP2LOCATION_TOKEN}&file=${IP2LOCATION_FILE}}"
    local zip="$TMP_DIR/ip2location.zip"
    local etag last_modified
    etag="$(remote_header "$url" ETag || true)"
    last_modified="$(remote_header "$url" Last-Modified || true)"
    download "$url" "$zip"
    if ! unzip -tq "$zip" >/dev/null 2>&1; then
        diagnose_bad_archive "$zip" "IP2Location"
        warn "Skipping IP2Location; builder can continue without it"
        return
    fi
    install_from_zip "$zip" "IP2LOCATION-LITE-DB5.BIN" "$dest.tmp" ||
        install_first_zip_match "$zip" 'DB5.*\.BIN$' "$dest.tmp" ||
        die "IP2LOCATION-LITE-DB5.BIN not found in archive"
    install_if_changed "ip2location-db5-lite" "$url" "$dest.tmp" "$dest" "$etag" "$last_modified" || true
}

download_maxmind() {
    local dest="$DATA_DIR/GeoLite2-City.mmdb"
    if [[ -z "${MAXMIND_ACCOUNT_ID:-}" || -z "${MAXMIND_LICENSE_KEY:-}" ]]; then
        warn "MAXMIND_ACCOUNT_ID/MAXMIND_LICENSE_KEY not set; skipping MaxMind"
        return
    fi

    log "Downloading MaxMind ${MAXMIND_DB}"
    local url="${MAXMIND_URL:-https://download.maxmind.com/geoip/databases/${MAXMIND_DB}/download?suffix=tar.gz}"
    local archive="$TMP_DIR/maxmind.archive"
    local etag last_modified
    etag="$(remote_header "$url" ETag -u "${MAXMIND_ACCOUNT_ID}:${MAXMIND_LICENSE_KEY}" || true)"
    last_modified="$(remote_header "$url" Last-Modified -u "${MAXMIND_ACCOUNT_ID}:${MAXMIND_LICENSE_KEY}" || true)"
    download "$url" "$archive" -u "${MAXMIND_ACCOUNT_ID}:${MAXMIND_LICENSE_KEY}"

    if tar -tzf "$archive" >/dev/null 2>&1; then
        local found
        found="$(tar -tzf "$archive" | grep -E '/GeoLite2-City\.mmdb$|/GeoIP2-City\.mmdb$|/.*City.*\.mmdb$' | head -1 || true)"
        [[ -n "$found" ]] || die "City .mmdb not found in MaxMind tar.gz; use MAXMIND_URL for an MMDB archive"
        tar -xOzf "$archive" "$found" > "$dest.tmp"
    elif unzip -tq "$archive" >/dev/null 2>&1; then
        install_first_zip_match "$archive" 'City.*\.mmdb$' "$dest.tmp" ||
            die "City .mmdb not found in MaxMind zip; CSV archives are not usable by this builder"
    else
        diagnose_bad_archive "$archive" "MaxMind"
        die "unsupported MaxMind archive format"
    fi

    install_if_changed "maxmind-city" "$url" "$dest.tmp" "$dest" "$etag" "$last_modified" || true
}

download_rir_stats() {
    mkdir -p "$RIR_STATS_DIR"
    local specs=(
        "afrinic|https://ftp.afrinic.net/stats/afrinic/delegated-afrinic-extended-latest"
        "apnic|https://ftp.apnic.net/stats/apnic/delegated-apnic-extended-latest"
        "arin|https://ftp.arin.net/pub/stats/arin/delegated-arin-extended-latest"
        "lacnic|https://ftp.lacnic.net/pub/stats/lacnic/delegated-lacnic-extended-latest"
        "ripencc|https://ftp.ripe.net/pub/stats/ripencc/delegated-ripencc-extended-latest"
    )
    local spec name url dest etag last_modified tmp
    for spec in "${specs[@]}"; do
        IFS='|' read -r name url <<< "$spec"
        dest="$RIR_STATS_DIR/delegated-${name}-extended-latest"
        tmp="$dest.tmp"
        log "Downloading RIR delegated stats: $name"
        etag="$(remote_header "$url" ETag || true)"
        last_modified="$(remote_header "$url" Last-Modified || true)"
        download "$url" "$tmp"
        install_if_changed "rir-${name}" "$url" "$tmp" "$dest" "$etag" "$last_modified" || true
    done
}

download_geofeeds() {
    local allowlist="${GEOFEED_ALLOWLIST:-$GEOFEED_DIR/allowlist.tsv}"
    if [[ ! -s "$allowlist" ]]; then
        warn "Geofeed allowlist not found; skipping geofeeds: $allowlist"
        return
    fi
    mkdir -p "$GEOFEED_DIR"
    local name url dest etag last_modified tmp
    while IFS=$'\t' read -r name url; do
        [[ -n "${name:-}" ]] || continue
        [[ "$name" == \#* ]] && continue
        [[ -n "${url:-}" ]] || continue
        dest="$GEOFEED_DIR/${name}.csv"
        tmp="$dest.tmp"
        log "Downloading geofeed: $name"
        etag="$(remote_header "$url" ETag || true)"
        last_modified="$(remote_header "$url" Last-Modified || true)"
        download "$url" "$tmp"
        install_if_changed "geofeed-${name}" "$url" "$tmp" "$dest" "$etag" "$last_modified" || true
    done < "$allowlist"
}

case "${DOWNLOAD_ONLY:-all}" in
    all)
        download_dbip
        download_sypex_city
        download_sypex_country
        download_ip2location
        download_maxmind
        download_rir_stats
        download_geofeeds
        ;;
    dbip) download_dbip ;;
    sypex-city) download_sypex_city ;;
    sypex-country) download_sypex_country ;;
    ip2location) download_ip2location ;;
    maxmind) download_maxmind ;;
    rir) download_rir_stats ;;
    geofeeds) download_geofeeds ;;
    *)
        die "unknown DOWNLOAD_ONLY=${DOWNLOAD_ONLY}; use all, dbip, sypex-city, sypex-country, ip2location, maxmind, rir, geofeeds"
        ;;
esac

printf '%s\n' "$DOWNLOAD_CHANGED" > "$CHANGED_FILE"
ok "Data download step complete"
