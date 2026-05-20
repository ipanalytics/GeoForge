#!/bin/bash
set -e

# GeoForge
#
# Standard Go layout:
#   cmd/builder/      -- entrypoint
#   internal/         -- consensus, geozip, output, refdata, strnorm
#   data/             -- input databases (DB-IP, MaxMind, IP2Location, Sypex)
#   release/          -- output (geo.mmdb)
#
# Run from the project root.

RESET='\033[0m'
BOLD='\033[1m'
GREEN='\033[32m'
YELLOW='\033[33m'
BLUE='\033[34m'
CYAN='\033[36m'
GRAY='\033[90m'
RED='\033[31m'

print_header() {
    echo ""
    echo -e "${BOLD}${BLUE}+--------------------------------------------------------------+${RESET}"
    echo -e "${BOLD}${BLUE}|                         GeoForge                             |${RESET}"
    echo -e "${BOLD}${BLUE}|        Consensus GeoIP Compiler | Offline | Multi-Source     |${RESET}"
    echo -e "${BOLD}${BLUE}+--------------------------------------------------------------+${RESET}"
    echo ""
}

print_section() { echo -e "${BOLD}${CYAN}>> $1${RESET}"; }
print_ok()      { echo -e "${GREEN}  +${RESET} $1"; }
print_warn()    { echo -e "${YELLOW}  !${RESET} $1"; }
print_error()   { echo -e "${RED}  x${RESET} $1"; }
print_info()    { echo -e "${GRAY}  i $1${RESET}"; }
print_divider() { echo -e "${GRAY}  ------------------------------------------------------------${RESET}"; }

acquire_lock() {
    mkdir -p release
    LOCK_FILE="release/builder.lock"
    exec 9>"$LOCK_FILE"
    if ! flock -n 9; then
        print_error "Another builder is already running."
        print_info "Lock: ${LOCK_FILE}"
        print_info "Wait for the current build to finish, or stop the stale process before retrying."
        exit 1
    fi
    print_ok "Build lock acquired: ${LOCK_FILE}"
}

check_go() {
    print_section "Environment Check"
    if ! command -v go &> /dev/null; then
        print_error "Go not installed!"
        print_info "Install: https://go.dev/doc/install"
        exit 1
    fi
    GO_VERSION=$(go version | awk '{print $3}')
    print_ok "Go installed: ${BOLD}${GO_VERSION}${RESET}"
}

check_files() {
    print_section "File Check"

    # Required source files (all under cmd/ and internal/)
    SCRIPTS=(
        "cmd/builder/main.go"
        "internal/consensus/consensus.go"
        "internal/consensus/matching.go"
        "internal/consensus/validators.go"
        "internal/geozip/resolver.go"
        "internal/output/normalize.go"
        "internal/refdata/countries.go"
        "internal/refdata/priorities.go"
        "internal/strnorm/strnorm.go"
        "go.mod"
    )
    DATA_FILES=("dbip-city-lite.csv" "GeoLite2-City.mmdb" "IP2LOCATION-LITE-DB5.BIN" "SxGeoCity.dat")

    local missing_scripts=0
    local missing_data=0

    echo ""
    echo -e "  ${BOLD}Source code:${RESET}"
    for file in "${SCRIPTS[@]}"; do
        if [ -f "$file" ]; then
            local size=$(du -h "$file" 2>/dev/null | cut -f1)
            print_ok "${file} ${GRAY}(${size})${RESET}"
        else
            print_error "${file} ${RED}(NOT FOUND)${RESET}"
            missing_scripts=1
        fi
    done

    mkdir -p data release

    echo ""
    echo -e "  ${BOLD}Databases (./data/):${RESET}"
    for file in "${DATA_FILES[@]}"; do
        if [ -f "data/${file}" ]; then
            local size=$(du -h "data/${file}" 2>/dev/null | cut -f1)
            print_ok "data/${file} ${GRAY}(${size})${RESET}"
        else
            print_warn "data/${file} ${YELLOW}(will be skipped)${RESET}"
            missing_data=1
        fi
    done

    if [ $missing_scripts -eq 1 ]; then
        echo ""
        print_error "Missing required source files!"
        exit 1
    fi
    if [ $missing_data -eq 1 ]; then
        echo ""
        print_warn "Some databases missing -- continuing with available"
    fi
    print_divider
}

download_data() {
    DATA_CHANGED=1
    if [ "${AUTO_DOWNLOAD:-1}" != "1" ]; then
        print_info "AUTO_DOWNLOAD disabled"
        return
    fi
    print_section "Data Download"
    if [ -x "scripts/download_data.sh" ]; then
        scripts/download_data.sh
    else
        bash scripts/download_data.sh
    fi
    if [ -f "data/download-changed.txt" ]; then
        DATA_CHANGED=$(cat data/download-changed.txt)
    fi
    print_divider
}

install_deps() {
    print_section "Installing Dependencies"
    echo -ne "  ${GRAY}-> go mod download...${RESET}"
    go mod download &>/dev/null
    echo -e "\r  ${GREEN}+${RESET} go mod download ${GRAY}OK${RESET}                                          "
    echo -ne "  ${GRAY}-> go mod tidy...${RESET}"
    go mod tidy &>/dev/null
    echo -e "\r  ${GREEN}+${RESET} go mod tidy ${GRAY}OK${RESET}                                              "
    print_ok "Dependencies ready"
    print_divider
}

run_tests() {
    print_section "Tests"
    echo -ne "  ${GRAY}Running unit tests...${RESET}"
    if go test ./... &>/tmp/test.log; then
        echo -e "\r  ${GREEN}+${RESET} All tests passed                                            "
    else
        echo -e "\r  ${RED}x Tests failed!${RESET}                                                "
        cat /tmp/test.log
        exit 1
    fi
    print_divider
}

compile() {
    print_section "Compilation"
    echo -ne "  ${GRAY}Building ./builder...${RESET}"
    if go build -o builder ./cmd/builder 2>&1; then
        local size=$(du -h builder 2>/dev/null | cut -f1)
        echo -e "\r  ${GREEN}+${RESET} Compiled: ${BOLD}builder${RESET} ${GRAY}(${size})${RESET}                                  "
    else
        echo -e "\r  ${RED}x Compilation error!${RESET}                                                "
        exit 1
    fi
    print_divider
}

snapshot_previous_release() {
    if [ -s "release/geo.csv" ]; then
        cp release/geo.csv release/geo.previous.csv
        print_ok "Previous CSV snapshot saved: release/geo.previous.csv"
    else
        print_info "No previous release/geo.csv snapshot found"
    fi
}

run_builder() {
    print_section "Database Generation"
    echo ""
    echo -e "${BOLD}${CYAN}  ------------------------------------------------------------${RESET}"
    ./builder
    echo -e "${BOLD}${CYAN}  ------------------------------------------------------------${RESET}"
    echo ""
}

run_quality_check() {
    print_section "Quality Check"
    local strict_flag=""
    if [ "${QUALITY_STRICT:-0}" = "1" ]; then
        strict_flag="-strict"
    fi
    echo -ne "  ${GRAY}Checking release quality...${RESET}"
    if go run ./cmd/qualitycheck ${strict_flag} > /tmp/geo-quality.log 2>&1; then
        echo -e "\r  ${GREEN}+${RESET} Quality report written: release/geo-quality-report.txt                  "
    else
        echo -e "\r  ${RED}x Quality check failed${RESET}                                      "
        cat /tmp/geo-quality.log
        exit 1
    fi
    if grep -q "previous release CSV not found" /tmp/geo-quality.log; then
        print_info "First quality baseline; next build will compare against this release"
    fi
    print_divider
}

print_footer() {
    local output_file="release/geo.mmdb"
    if [ -f "$output_file" ]; then
        local size=$(du -h "$output_file" 2>/dev/null | cut -f1)
        echo -e "${BOLD}${GREEN}+--------------------------------------------------------------+${RESET}"
        echo -e "${BOLD}${GREEN}|  DONE! File: ${output_file} ${GRAY}(${size})${GREEN}                              |${RESET}"
        echo -e "${BOLD}${GREEN}+--------------------------------------------------------------+${RESET}"
    else
        echo -e "${BOLD}${YELLOW}+--------------------------------------------------------------+${RESET}"
        echo -e "${BOLD}${YELLOW}|  Warning: output file not found                              |${RESET}"
        echo -e "${BOLD}${YELLOW}+--------------------------------------------------------------+${RESET}"
    fi
    echo ""
}

print_header
acquire_lock
export GOCACHE="${GOCACHE:-/private/tmp/geo-go-build}"
check_go
download_data
check_files
if [ "${DATA_CHANGED:-1}" = "0" ] && [ -s "release/geo.mmdb" ] && [ "${FORCE_BUILD:-0}" != "1" ]; then
    print_section "Build"
    print_ok "No source databases changed; keeping existing release/geo.mmdb"
    print_info "Set FORCE_BUILD=1 to rebuild anyway"
    print_footer
    exit 0
fi
install_deps
run_tests
compile
snapshot_previous_release
run_builder
run_quality_check
print_footer
