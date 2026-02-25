#!/bin/bash
#
# OTA-Pulse Buildroot Post-Build Script
#
# This script runs after Buildroot creates the target filesystem.
# It performs final configuration and setup for OTA functionality.
#

set -e

BOARD_DIR="$(dirname $0)"
TARGET_DIR="$1"

echo "=== OTA-Pulse Post-Build Script ==="

# ==============================================================================
# Phase 1: Run comprehensive sanity checks
# ==============================================================================
SANITY_SCRIPT="${BOARD_DIR}/otapulse-sanity-check.sh"
if [ -f "$SANITY_SCRIPT" ] && [ -x "$SANITY_SCRIPT" ]; then
    "$SANITY_SCRIPT" "$TARGET_DIR"
fi

# ==============================================================================
# Validate OTA-Pulse is installed in the target filesystem
# ==============================================================================

OTAPULSE_ERRORS=0

# Check binary
if [ ! -f "${TARGET_DIR}/usr/bin/otapulse" ]; then
    echo "ERROR: otapulse binary not found in target filesystem!"
    echo "       Expected: /usr/bin/otapulse"
    echo "       Make sure BR2_PACKAGE_OTAPULSE=y is set in your defconfig."
    OTAPULSE_ERRORS=$((OTAPULSE_ERRORS + 1))
fi

# Check systemd service
if [ ! -f "${TARGET_DIR}/usr/lib/systemd/system/otapulse.service" ]; then
    echo "ERROR: otapulse systemd service not installed!"
    echo "       Expected: /usr/lib/systemd/system/otapulse.service"
    echo "       Make sure BR2_PACKAGE_OTAPULSE_SYSTEMD=y is set."
    OTAPULSE_ERRORS=$((OTAPULSE_ERRORS + 1))
fi

# Check configuration file
if [ ! -f "${TARGET_DIR}/etc/otapulse/otapulse.conf" ]; then
    echo "ERROR: otapulse configuration file not found!"
    echo "       Expected: /etc/otapulse/otapulse.conf"
    OTAPULSE_ERRORS=$((OTAPULSE_ERRORS + 1))
fi

# Check verification key
if [ ! -f "${TARGET_DIR}/etc/otapulse/keys/artifact-verify-key.pem" ]; then
    echo "ERROR: artifact verification key not installed!"
    echo "       Expected: /etc/otapulse/keys/artifact-verify-key.pem"
    echo "       Make sure BR2_PACKAGE_OTAPULSE_VERIFY_KEY points to a valid key file."
    OTAPULSE_ERRORS=$((OTAPULSE_ERRORS + 1))
fi

# Check systemd itself is present
if [ ! -d "${TARGET_DIR}/usr/lib/systemd" ]; then
    echo "ERROR: systemd not found in target filesystem!"
    echo "       OTA-Pulse requires BR2_INIT_SYSTEMD=y."
    OTAPULSE_ERRORS=$((OTAPULSE_ERRORS + 1))
fi

if [ ${OTAPULSE_ERRORS} -gt 0 ]; then
    echo ""
    echo "FATAL: ${OTAPULSE_ERRORS} OTA-Pulse installation error(s) found."
    echo "       The image will not have a functioning OTA agent."
    echo "       Fix the errors above and rebuild."
    exit 1
fi

echo "OTA-Pulse installation verified: binary, service, config, and key present."

# Ensure required directories exist
mkdir -p "${TARGET_DIR}/etc/otapulse/scripts"
mkdir -p "${TARGET_DIR}/etc/otapulse/keys"
mkdir -p "${TARGET_DIR}/var/lib/otapulse"
mkdir -p "${TARGET_DIR}/data"

# Create fstab entry for data partition if not present
if ! grep -q "/data" "${TARGET_DIR}/etc/fstab" 2>/dev/null; then
    echo "" >> "${TARGET_DIR}/etc/fstab"
    echo "# OTA-Pulse persistent data partition" >> "${TARGET_DIR}/etc/fstab"
    echo "LABEL=data    /data    ext4    defaults,noatime,nofail,x-systemd.device-timeout=60    0    2" >> "${TARGET_DIR}/etc/fstab"
fi

# Create /etc/os-release if not exists (for inventory scripts)
if [ ! -f "${TARGET_DIR}/etc/os-release" ]; then
    cat > "${TARGET_DIR}/etc/os-release" << EOF
NAME="OTA-Pulse Buildroot"
VERSION="${BR2_VERSION:-unknown}"
ID=buildroot
VERSION_ID="${BR2_VERSION:-unknown}"
PRETTY_NAME="OTA-Pulse Buildroot Linux"
EOF
fi

# Ship an empty machine-id so systemd treats every first boot as
# "uninitialized" and uses a transient ID until the real one is set.
# The otapulse-machine-id.service (runs after /data is mounted) will
# generate a unique per-device ID on the very first boot, persist it
# to /data/etc/machine-id, and restore it on every subsequent boot
# — including after A/B OTA rootfs updates.
: > "${TARGET_DIR}/etc/machine-id"

# Setup SSH host keys persistence
if [ -d "${TARGET_DIR}/etc/ssh" ]; then
    for key in ssh_host_rsa_key ssh_host_ecdsa_key ssh_host_ed25519_key; do
        if [ ! -L "${TARGET_DIR}/etc/ssh/${key}" ]; then
            rm -f "${TARGET_DIR}/etc/ssh/${key}"
            rm -f "${TARGET_DIR}/etc/ssh/${key}.pub"
            ln -sf "/data/etc/ssh/${key}" "${TARGET_DIR}/etc/ssh/${key}"
            ln -sf "/data/etc/ssh/${key}.pub" "${TARGET_DIR}/etc/ssh/${key}.pub"
        fi
    done
fi

# Install dev SSH authorized_keys if configured (for QEMU / dev builds)
br2_config_get() {
    sed -n "s/^${1}=\"\(.*\)\"$/\1/p" "${BR2_CONFIG:-/dev/null}"
}

DEV_SSH_KEY="$(br2_config_get BR2_PACKAGE_OTAPULSE_DEV_SSH_KEY)"
if [ -n "${DEV_SSH_KEY}" ] && [ -f "${DEV_SSH_KEY}" ]; then
    install -d -m 0700 "${TARGET_DIR}/root/.ssh"
    install -m 0600 "${DEV_SSH_KEY}" "${TARGET_DIR}/root/.ssh/authorized_keys"
    echo "Installed dev SSH key: ${DEV_SSH_KEY}"
elif [ -n "${DEV_SSH_KEY}" ]; then
    echo "WARNING: BR2_PACKAGE_OTAPULSE_DEV_SSH_KEY set but file not found: ${DEV_SSH_KEY}"
fi

# Create build-info file for firmware identification
BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
BUILD_HOST=$(hostname 2>/dev/null || echo "unknown")

cat > "${TARGET_DIR}/etc/build-info" << EOF
BUILD_DATE=${BUILD_DATE}
BUILD_HOST=${BUILD_HOST}
OTAPULSE_VERSION=${BR2_PACKAGE_OTAPULSE_VERSION:-1.0.0}
DEVICE_TYPE=${BR2_PACKAGE_OTAPULSE_DEVICE_TYPE:-generic}
EOF

# Set proper permissions
chmod 755 "${TARGET_DIR}/etc/otapulse"
chmod 755 "${TARGET_DIR}/etc/otapulse/scripts"
chmod 700 "${TARGET_DIR}/var/lib/otapulse"

echo "=== OTA-Pulse Post-Build Complete ==="
