#!/bin/bash
# uninstall.sh — Remove OTAPulse from the system
#
# Usage:
#   uninstall.sh [OPTIONS]
#
# Options:
#   --prefix DIR    Installation prefix (default: /)
#   --purge         Also remove configuration and state data
#   -h, --help      Show this help message

set -euo pipefail

PREFIX="/"
PURGE=false

# Colours
if [[ -t 1 ]] && [[ "${NO_COLOR:-}" == "" ]]; then
    RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
    BLUE='\033[0;34m'; BOLD='\033[1m'; RESET='\033[0m'
else
    RED='' GREEN='' YELLOW='' BLUE='' BOLD='' RESET=''
fi

info()  { printf "${BLUE}==>${RESET} %s\n" "$*"; }
ok()    { printf "${GREEN}==>${RESET} %s\n" "$*"; }
warn()  { printf "${YELLOW}==>${RESET} %s\n" "$*"; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --prefix) PREFIX="$2"; shift 2 ;;
        --purge)  PURGE=true; shift ;;
        -h|--help) sed -n '2,/^$/s/^# \?//p' "$0"; exit 0 ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

if [[ "$(id -u)" -ne 0 ]] && [[ "$PREFIX" == "/" ]]; then
    echo "This script must be run as root" >&2
    exit 1
fi

printf "\n${BOLD}OTAPulse Uninstaller${RESET}\n"
printf '═%.0s' {1..40}; printf '\n'

# Stop services
info "Stopping services..."
if command -v systemctl &>/dev/null && [[ -d /run/systemd/system ]]; then
    systemctl stop otapulse-client.service 2>/dev/null || true
    systemctl disable otapulse-client.service 2>/dev/null || true
    systemctl stop otapulse-boot-reconcile.service 2>/dev/null || true
    systemctl disable otapulse-boot-reconcile.service 2>/dev/null || true
elif command -v rc-service &>/dev/null; then
    rc-service otapulse stop 2>/dev/null || true
    rc-update del otapulse 2>/dev/null || true
elif [[ -f /etc/init.d/otapulse ]]; then
    /etc/init.d/otapulse stop 2>/dev/null || true
fi

# Remove files
info "Removing files..."
rm -f "${PREFIX}/usr/bin/otapulse"
rm -f "${PREFIX}/usr/bin/mender"
rm -f "${PREFIX}/usr/bin/otapulse-configure.sh"
rm -f "${PREFIX}/usr/bin/otapulse-precheck.sh"
rm -f "${PREFIX}/usr/bin/otapulse-verify.sh"
rm -rf "${PREFIX}/usr/share/otapulse"
rm -f "${PREFIX}/usr/share/dbus-1/system.d/io.otapulse."*.conf

# Remove service files
rm -f "${PREFIX}/lib/systemd/system/otapulse-client.service"
rm -f "${PREFIX}/lib/systemd/system/otapulse-boot-reconcile.service"
rm -f "${PREFIX}/etc/init.d/otapulse"

if command -v systemctl &>/dev/null && [[ -d /run/systemd/system ]]; then
    systemctl daemon-reload 2>/dev/null || true
fi

ok "OTAPulse binaries and scripts removed"

# Purge config and state
if [[ "$PURGE" == "true" ]]; then
    info "Purging configuration and state..."
    rm -rf "${PREFIX}/etc/otapulse"
    rm -rf "${PREFIX}/var/lib/otapulse"
    warn "/data/ota/ preserved (contains boot state — remove manually if safe)"
    ok "Configuration purged"
else
    info "Configuration preserved at ${PREFIX}/etc/otapulse/"
    info "Run with --purge to remove configuration"
fi

printf "\n${GREEN}${BOLD}OTAPulse uninstalled.${RESET}\n\n"
