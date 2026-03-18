#!/bin/bash
# install.sh — Generic OTAPulse installer for any Linux distribution
#
# Detects architecture, init system, and libc, then installs the OTA agent
# binary, scripts, and appropriate service file.
#
# Usage:
#   install.sh [OPTIONS]
#
# Options:
#   --prefix DIR      Installation prefix (default: /)
#   --bin-dir DIR     Binary directory (default: /usr/bin)
#   --conf-dir DIR    Config directory (default: /etc/otapulse)
#   --data-dir DIR    Data directory (default: /usr/share/otapulse)
#   --skip-service    Don't install init service
#   --skip-configure  Don't run configuration wizard
#   -h, --help        Show this help message

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ---------------------------------------------------------------------------
# Defaults
# ---------------------------------------------------------------------------
PREFIX="/"
BIN_DIR="/usr/bin"
CONF_DIR="/etc/otapulse"
DATA_DIR="/usr/share/otapulse"
SYSTEMD_DIR="/lib/systemd/system"
SKIP_SERVICE=false
SKIP_CONFIGURE=false

# ---------------------------------------------------------------------------
# Terminal colours
# ---------------------------------------------------------------------------
if [[ -t 1 ]] && [[ "${NO_COLOR:-}" == "" ]]; then
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[1;33m'
    BLUE='\033[0;34m'
    BOLD='\033[1m'
    RESET='\033[0m'
else
    RED='' GREEN='' YELLOW='' BLUE='' BOLD='' RESET=''
fi

info()  { printf "${BLUE}==>${RESET} %s\n" "$*"; }
ok()    { printf "${GREEN}==>${RESET} %s\n" "$*"; }
warn()  { printf "${YELLOW}==>${RESET} %s\n" "$*"; }
error() { printf "${RED}==>${RESET} %s\n" "$*" >&2; }

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
while [[ $# -gt 0 ]]; do
    case "$1" in
        --prefix)          PREFIX="$2"; shift 2 ;;
        --bin-dir)         BIN_DIR="$2"; shift 2 ;;
        --conf-dir)        CONF_DIR="$2"; shift 2 ;;
        --data-dir)        DATA_DIR="$2"; shift 2 ;;
        --skip-service)    SKIP_SERVICE=true; shift ;;
        --skip-configure)  SKIP_CONFIGURE=true; shift ;;
        -h|--help)         sed -n '2,/^$/s/^# \?//p' "$0"; exit 0 ;;
        *)                 error "Unknown option: $1"; exit 1 ;;
    esac
done

# ---------------------------------------------------------------------------
# Root check
# ---------------------------------------------------------------------------
if [[ "$(id -u)" -ne 0 ]] && [[ "$PREFIX" == "/" ]]; then
    error "This script must be run as root (or use --prefix for local install)"
    exit 1
fi

# ---------------------------------------------------------------------------
# Detect system
# ---------------------------------------------------------------------------
printf "\n${BOLD}OTAPulse Installer${RESET}\n"
printf '═%.0s' {1..50}; printf '\n'

# Architecture
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|amd64)   ARCH_NAME="amd64" ;;
    aarch64|arm64)   ARCH_NAME="arm64" ;;
    armv7*|armhf)    ARCH_NAME="armhf" ;;
    armv6*)          ARCH_NAME="armv6" ;;
    *)               ARCH_NAME="$ARCH" ;;
esac

# Init system
INIT_SYSTEM="unknown"
if command -v systemctl &>/dev/null && [[ -d /run/systemd/system ]]; then
    INIT_SYSTEM="systemd"
elif command -v rc-service &>/dev/null; then
    INIT_SYSTEM="openrc"
elif [[ -f /etc/init.d/rc ]]; then
    INIT_SYSTEM="sysvinit"
fi

# Libc
LIBC="unknown"
if ldd --version 2>&1 | grep -qi glibc; then
    LIBC="glibc"
elif ldd --version 2>&1 | grep -qi musl; then
    LIBC="musl"
fi

info "Architecture: ${ARCH} (${ARCH_NAME})"
info "Init system:  ${INIT_SYSTEM}"
info "C library:    ${LIBC}"
info "Prefix:       ${PREFIX}"

# ---------------------------------------------------------------------------
# Check for binary
# ---------------------------------------------------------------------------
BINARY=""
if [[ -f "$SCRIPT_DIR/otapulse" ]]; then
    BINARY="$SCRIPT_DIR/otapulse"
elif [[ -f "$SCRIPT_DIR/../soc-ota-agent/otapulse" ]]; then
    BINARY="$SCRIPT_DIR/../soc-ota-agent/otapulse"
fi

if [[ -z "$BINARY" ]]; then
    error "OTAPulse binary not found."
    error "Build it first: cd soc-ota-agent && make build"
    exit 1
fi

info "Binary: $BINARY"

# ---------------------------------------------------------------------------
# Install binary
# ---------------------------------------------------------------------------
info "Installing binary..."
install -d -m 755 "${PREFIX}${BIN_DIR}"
install -m 755 "$BINARY" "${PREFIX}${BIN_DIR}/otapulse"
ln -sf otapulse "${PREFIX}${BIN_DIR}/mender"
ok "Binary installed to ${PREFIX}${BIN_DIR}/otapulse"

# ---------------------------------------------------------------------------
# Install configuration directory
# ---------------------------------------------------------------------------
info "Creating configuration directories..."
install -d -m 755 "${PREFIX}${CONF_DIR}"
install -d -m 755 "${PREFIX}/var/lib/otapulse"
install -d -m 755 "${PREFIX}/data/ota"
ok "Directories created"

# ---------------------------------------------------------------------------
# Install support scripts
# ---------------------------------------------------------------------------
info "Installing support scripts..."
AGENT_SUPPORT="$SCRIPT_DIR/../soc-ota-agent/support"

if [[ -d "$AGENT_SUPPORT" ]]; then
    # Identity scripts
    install -d -m 755 "${PREFIX}${DATA_DIR}/identity"
    if [[ -f "$AGENT_SUPPORT/otapulse-device-identity" ]]; then
        install -m 755 "$AGENT_SUPPORT/otapulse-device-identity" "${PREFIX}${DATA_DIR}/identity/"
    fi

    # Inventory scripts
    install -d -m 755 "${PREFIX}${DATA_DIR}/inventory"
    for script in "$AGENT_SUPPORT"/otapulse-inventory-*; do
        [[ -f "$script" ]] && install -m 755 "$script" "${PREFIX}${DATA_DIR}/inventory/"
    done

    # Update modules
    install -d -m 755 "${PREFIX}${DATA_DIR}/modules/v3"
    for module in "$AGENT_SUPPORT"/modules/*; do
        [[ -f "$module" ]] && install -m 755 "$module" "${PREFIX}${DATA_DIR}/modules/v3/"
    done

    # D-Bus policy files
    if [[ -d "$AGENT_SUPPORT/dbus" ]]; then
        install -d -m 755 "${PREFIX}/usr/share/dbus-1/system.d"
        for policy in "$AGENT_SUPPORT"/dbus/*.conf; do
            [[ -f "$policy" ]] && install -m 644 "$policy" "${PREFIX}/usr/share/dbus-1/system.d/"
        done
    fi

    ok "Support scripts installed"
else
    warn "Support scripts not found at $AGENT_SUPPORT — skipping"
fi

# ---------------------------------------------------------------------------
# Install utility scripts
# ---------------------------------------------------------------------------
SCRIPTS_DIR="$SCRIPT_DIR/../scripts"
if [[ -d "$SCRIPTS_DIR" ]]; then
    for script in otapulse-configure.sh otapulse-precheck.sh otapulse-verify.sh; do
        if [[ -f "$SCRIPTS_DIR/$script" ]]; then
            install -m 755 "$SCRIPTS_DIR/$script" "${PREFIX}${BIN_DIR}/"
        fi
    done
    ok "Utility scripts installed"
fi

# ---------------------------------------------------------------------------
# Install service file
# ---------------------------------------------------------------------------
if [[ "$SKIP_SERVICE" != "true" ]]; then
    info "Installing service file (${INIT_SYSTEM})..."

    case "$INIT_SYSTEM" in
        systemd)
            install -d -m 755 "${PREFIX}${SYSTEMD_DIR}"
            if [[ -f "$AGENT_SUPPORT/otapulse-client.service" ]]; then
                install -m 644 "$AGENT_SUPPORT/otapulse-client.service" "${PREFIX}${SYSTEMD_DIR}/"
            fi
            if [[ -f "$AGENT_SUPPORT/otapulse-boot-reconcile.service" ]]; then
                install -m 644 "$AGENT_SUPPORT/otapulse-boot-reconcile.service" "${PREFIX}${SYSTEMD_DIR}/"
            fi
            ok "Systemd service installed"
            ;;

        openrc)
            install -d -m 755 "${PREFIX}/etc/init.d"
            cat > "${PREFIX}/etc/init.d/otapulse" <<'OPENRC_EOF'
#!/sbin/openrc-run

name="otapulse"
description="OTAPulse OTA Update Agent"
command="/usr/bin/otapulse"
command_args="daemon"
command_background=true
pidfile="/run/${RC_SVCNAME}.pid"

depend() {
    need net localmount
    after firewall
}
OPENRC_EOF
            chmod 755 "${PREFIX}/etc/init.d/otapulse"
            ok "OpenRC service installed"
            ;;

        sysvinit)
            install -d -m 755 "${PREFIX}/etc/init.d"
            cat > "${PREFIX}/etc/init.d/otapulse" <<'SYSV_EOF'
#!/bin/sh
### BEGIN INIT INFO
# Provides:          otapulse
# Required-Start:    $remote_fs $syslog $network
# Required-Stop:     $remote_fs $syslog $network
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Short-Description: OTAPulse OTA Update Agent
### END INIT INFO

DAEMON=/usr/bin/otapulse
PIDFILE=/run/otapulse.pid

case "$1" in
    start)
        echo "Starting otapulse..."
        start-stop-daemon --start --background --make-pidfile \
            --pidfile "$PIDFILE" --exec "$DAEMON" -- daemon
        ;;
    stop)
        echo "Stopping otapulse..."
        start-stop-daemon --stop --pidfile "$PIDFILE"
        rm -f "$PIDFILE"
        ;;
    restart)
        $0 stop
        $0 start
        ;;
    *)
        echo "Usage: $0 {start|stop|restart}"
        exit 1
        ;;
esac
SYSV_EOF
            chmod 755 "${PREFIX}/etc/init.d/otapulse"
            ok "SysV init script installed"
            ;;

        *)
            warn "Unknown init system — service file not installed"
            warn "You'll need to start otapulse manually: otapulse daemon"
            ;;
    esac
fi

# ---------------------------------------------------------------------------
# Run configuration wizard
# ---------------------------------------------------------------------------
if [[ "$SKIP_CONFIGURE" != "true" ]] && [[ ! -f "${PREFIX}${CONF_DIR}/otapulse.conf" ]]; then
    printf "\n"
    if [[ -f "${PREFIX}${BIN_DIR}/otapulse-configure.sh" ]]; then
        info "No configuration found. Running configuration wizard..."
        bash "${PREFIX}${BIN_DIR}/otapulse-configure.sh" \
            --output "${PREFIX}${CONF_DIR}/otapulse.conf" || true
    else
        warn "No configuration found. Create ${CONF_DIR}/otapulse.conf before starting."
    fi
fi

# ---------------------------------------------------------------------------
# Run pre-build check
# ---------------------------------------------------------------------------
if [[ -f "${PREFIX}${BIN_DIR}/otapulse-precheck.sh" ]] && [[ -f "${PREFIX}${CONF_DIR}/otapulse.conf" ]]; then
    info "Validating configuration..."
    bash "${PREFIX}${BIN_DIR}/otapulse-precheck.sh" \
        --config "${PREFIX}${CONF_DIR}/otapulse.conf" || true
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
printf "\n"
printf '═%.0s' {1..50}; printf '\n'
printf "${BOLD}${GREEN}OTAPulse installed successfully!${RESET}\n"
printf '═%.0s' {1..50}; printf '\n'
printf "\n"
printf "  Binary:  ${PREFIX}${BIN_DIR}/otapulse\n"
printf "  Config:  ${PREFIX}${CONF_DIR}/otapulse.conf\n"
printf "  Scripts: ${PREFIX}${DATA_DIR}/\n"
printf "\n"

case "$INIT_SYSTEM" in
    systemd)
        printf "  Start:   systemctl enable --now otapulse-client\n"
        printf "  Status:  systemctl status otapulse-client\n"
        printf "  Logs:    journalctl -u otapulse-client\n"
        ;;
    openrc)
        printf "  Start:   rc-service otapulse start\n"
        printf "  Enable:  rc-update add otapulse default\n"
        ;;
    sysvinit)
        printf "  Start:   /etc/init.d/otapulse start\n"
        ;;
esac
printf "\n"
