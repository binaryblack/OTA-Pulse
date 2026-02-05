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

# Ensure required directories exist
mkdir -p "${TARGET_DIR}/etc/otapulse/scripts"
mkdir -p "${TARGET_DIR}/etc/otapulse/keys"
mkdir -p "${TARGET_DIR}/var/lib/otapulse"
mkdir -p "${TARGET_DIR}/data"

# Create fstab entry for data partition if not present
if ! grep -q "/data" "${TARGET_DIR}/etc/fstab" 2>/dev/null; then
    echo "" >> "${TARGET_DIR}/etc/fstab"
    echo "# OTA-Pulse persistent data partition" >> "${TARGET_DIR}/etc/fstab"
    echo "LABEL=data    /data    ext4    defaults,noatime    0    2" >> "${TARGET_DIR}/etc/fstab"
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

# Setup machine-id persistence (symlink to /data)
if [ ! -L "${TARGET_DIR}/etc/machine-id" ]; then
    rm -f "${TARGET_DIR}/etc/machine-id"
    mkdir -p "${TARGET_DIR}/data/etc"
    ln -sf /data/etc/machine-id "${TARGET_DIR}/etc/machine-id"
fi

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
